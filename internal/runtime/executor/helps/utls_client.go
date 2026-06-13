package helps

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	tls "github.com/refraction-networking/utls"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

// utlsRoundTripper implements http.RoundTripper using utls with Chrome fingerprint
// to bypass Cloudflare's TLS fingerprinting on Anthropic domains.
type utlsRoundTripper struct {
	mu          sync.Mutex
	connections map[string]*http2.ClientConn
	pending     map[string]*sync.Cond
	dialer      proxy.Dialer
}

func newUtlsRoundTripper(proxyURL string) *utlsRoundTripper {
	var dialer proxy.Dialer = proxy.Direct
	if proxyURL != "" {
		proxyDialer, mode, errBuild := proxyutil.BuildDialer(proxyURL)
		if errBuild != nil {
			log.Errorf("utls: failed to configure proxy dialer for %q: %v", proxyutil.Redact(proxyURL), errBuild)
		} else if mode != proxyutil.ModeInherit && proxyDialer != nil {
			dialer = proxyDialer
		}
	}
	return &utlsRoundTripper{
		connections: make(map[string]*http2.ClientConn),
		pending:     make(map[string]*sync.Cond),
		dialer:      dialer,
	}
}

const (
	// utlsConnReadIdleTimeout / utlsConnPingTimeout health-check pooled HTTP/2
	// connections. Because a utlsRoundTripper is now shared and long-lived,
	// connections sit idle between requests; without active PINGs a connection
	// the upstream (or the egress proxy) dropped silently would keep reporting
	// CanTakeNewRequest()==true and fail the next request routed onto it.
	utlsConnReadIdleTimeout = 30 * time.Second
	utlsConnPingTimeout     = 15 * time.Second
)

// utlsRoundTripperRegistry pools one shared *utlsRoundTripper per resolved proxy
// URL. Previously NewUtlsHTTPClient built a fresh round tripper — and therefore
// a fresh, empty HTTP/2 connection pool — on every request, so every Codex/Claude
// upstream call opened a brand-new TCP+uTLS connection and never reused a
// kept-alive one. That outbound connect() + uTLS handshake was the dominant CPU
// cost under load (live profiling showed connect() alone at ~32% of CPU with
// extreme connection turnover). Sharing the round tripper lets its per-host
// HTTP/2 connection map actually be reused across requests.
//
// Keying by proxyURL is safe for per-credential sticky egress: the credential's
// egress identity is fully encoded in the proxy URL (see EffectiveProxyURL /
// credentialEgressProxyURL, which inject the credential's stable index as the
// proxy username), so two requests sharing a proxyURL share the same egress IP,
// and the pooled per-host connection stays sticky to that egress. The number of
// distinct proxy URLs is bounded by the credential count.
type utlsRoundTripperRegistry struct {
	mu      sync.Mutex
	entries map[string]*utlsRoundTripper
}

var globalUtlsRoundTrippers = &utlsRoundTripperRegistry{
	entries: make(map[string]*utlsRoundTripper),
}

func (r *utlsRoundTripperRegistry) get(proxyURL string) *utlsRoundTripper {
	r.mu.Lock()
	defer r.mu.Unlock()
	rt, ok := r.entries[proxyURL]
	if !ok {
		rt = newUtlsRoundTripper(proxyURL)
		r.entries[proxyURL] = rt
	}
	return rt
}

func (t *utlsRoundTripper) getOrCreateConnection(host, addr string) (*http2.ClientConn, error) {
	t.mu.Lock()

	if h2Conn, ok := t.connections[host]; ok && h2Conn.CanTakeNewRequest() {
		t.mu.Unlock()
		return h2Conn, nil
	}

	if cond, ok := t.pending[host]; ok {
		cond.Wait()
		if h2Conn, ok := t.connections[host]; ok && h2Conn.CanTakeNewRequest() {
			t.mu.Unlock()
			return h2Conn, nil
		}
	}

	cond := sync.NewCond(&t.mu)
	t.pending[host] = cond
	t.mu.Unlock()

	h2Conn, err := t.createConnection(host, addr)

	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.pending, host)
	cond.Broadcast()

	if err != nil {
		return nil, err
	}

	t.connections[host] = h2Conn
	return h2Conn, nil
}

func (t *utlsRoundTripper) createConnection(host, addr string) (*http2.ClientConn, error) {
	conn, err := t.dialer.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{ServerName: host}
	tlsConn := tls.UClient(conn, tlsConfig, tls.HelloChrome_Auto)

	if err := tlsConn.Handshake(); err != nil {
		conn.Close()
		return nil, err
	}

	tr := &http2.Transport{
		ReadIdleTimeout: utlsConnReadIdleTimeout,
		PingTimeout:     utlsConnPingTimeout,
	}
	h2Conn, err := tr.NewClientConn(tlsConn)
	if err != nil {
		tlsConn.Close()
		return nil, err
	}

	return h2Conn, nil
}

func (t *utlsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	hostname := req.URL.Hostname()
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}
	addr := net.JoinHostPort(hostname, port)

	h2Conn, err := t.getOrCreateConnection(hostname, addr)
	if err != nil {
		return nil, err
	}

	resp, err := h2Conn.RoundTrip(req)
	if err != nil {
		t.mu.Lock()
		if cached, ok := t.connections[hostname]; ok && cached == h2Conn {
			delete(t.connections, hostname)
		}
		t.mu.Unlock()
		return nil, err
	}

	return resp, nil
}

// utlsProtectedHosts contains the hosts that should use utls Chrome TLS fingerprint
// to bypass Cloudflare's TLS fingerprinting.
var utlsProtectedHosts = map[string]struct{}{
	"api.anthropic.com": {},
	"chatgpt.com":       {},
}

// fallbackRoundTripper uses utls for protected HTTPS hosts and falls back to
// standard transport for all other requests.
type fallbackRoundTripper struct {
	utls     http.RoundTripper
	fallback http.RoundTripper
}

func (f *fallbackRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme == "https" {
		if _, ok := utlsProtectedHosts[strings.ToLower(req.URL.Hostname())]; ok {
			return f.utls.RoundTrip(req)
		}
	}
	return f.fallback.RoundTrip(req)
}

// NewUtlsHTTPClient creates an HTTP client using utls Chrome TLS fingerprint.
// Use this for provider requests that need a Chrome-like TLS fingerprint.
// Falls back to standard transport for non-HTTPS requests.
func NewUtlsHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	proxyURL := strings.TrimSpace(EffectiveProxyURL(cfg, auth))

	var ctxRoundTripper http.RoundTripper
	if ctx != nil {
		ctxRoundTripper, _ = ctx.Value("cliproxy.roundtripper").(http.RoundTripper)
	}

	var utlsRT http.RoundTripper
	var standardTransport http.RoundTripper = http.DefaultTransport
	switch {
	case proxyURL != "":
		utlsRT = globalUtlsRoundTrippers.get(proxyURL)
		if transport := buildProxyTransport(proxyURL); transport != nil {
			standardTransport = transport
		}
	case ctxRoundTripper != nil:
		utlsRT = ctxRoundTripper
		standardTransport = ctxRoundTripper
	default:
		utlsRT = globalUtlsRoundTrippers.get(proxyURL)
	}

	client := &http.Client{
		Transport: newResponseHeaderRetryRoundTripper(&fallbackRoundTripper{
			utls:     utlsRT,
			fallback: standardTransport,
		}, cfg, proxyReuseProvider(auth)),
	}
	if timeout > 0 {
		client.Timeout = timeout
	}
	return client
}
