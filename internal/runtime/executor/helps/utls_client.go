package helps

import (
	"context"
	stdtls "crypto/tls"
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

const (
	// utlsConnReadIdleTimeout / utlsConnPingTimeout health-check pooled HTTP/2
	// connections. They are shared and long-lived, so they sit idle between
	// requests; without active PINGs a connection the upstream (or the egress
	// proxy) dropped silently would keep being handed out and fail the next
	// request routed onto it. http2.Transport uses these to ping idle conns and
	// evict dead ones from its pool.
	utlsConnReadIdleTimeout = 30 * time.Second
	utlsConnPingTimeout     = 15 * time.Second
)

// utlsRoundTripper implements http.RoundTripper using utls with the Chrome
// fingerprint to bypass Cloudflare's TLS fingerprinting on the protected
// upstream hosts.
//
// Connection management is delegated to an http2.Transport whose DialTLSContext
// performs the proxy dial + uTLS Chrome handshake. The Transport owns the
// connection pool: it reuses live connections across requests, opens new ones
// only when the existing ones are saturated, health-checks idle connections via
// PING, and reaps dead ones — so connections (and their readLoop goroutines and
// sockets) do not leak.
//
// This replaced an earlier hand-rolled single-connection-per-host map that
// leaked: when a cached connection was momentarily saturated (at the peer's
// MaxConcurrentStreams) it was overwritten without being closed, orphaning a
// connection that was thereafter never reused and never closed — one leaked
// readLoop goroutine + socket + framer buffers each. Under load this grew to
// tens of thousands of idle connections and tens of GiB of RSS. http2.Transport's
// pool avoids that entirely while keeping connections reused (no per-request dial).
type utlsRoundTripper struct {
	transport *http2.Transport
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

	transport := &http2.Transport{
		ReadIdleTimeout: utlsConnReadIdleTimeout,
		PingTimeout:     utlsConnPingTimeout,
		// cfg carries the SNI/ALPN http2 would use on its built-in TLS path; we
		// ignore it and drive uTLS ourselves with the Chrome fingerprint (which
		// negotiates the h2 ALPN). When DialTLSContext is set, http2.Transport
		// trusts the returned conn and performs no ALPN re-check, and a uTLS conn
		// simply leaves ClientConn.tlsState nil (it is informational only).
		DialTLSContext: func(ctx context.Context, network, addr string, _ *stdtls.Config) (net.Conn, error) {
			rawConn, err := dialContext(ctx, dialer, network, addr)
			if err != nil {
				return nil, err
			}
			host, _, errSplit := net.SplitHostPort(addr)
			if errSplit != nil {
				host = addr
			}
			tlsConn := tls.UClient(rawConn, &tls.Config{ServerName: host}, tls.HelloChrome_Auto)
			if errHandshake := tlsConn.HandshakeContext(ctx); errHandshake != nil {
				_ = rawConn.Close()
				return nil, errHandshake
			}
			return tlsConn, nil
		},
	}

	return &utlsRoundTripper{transport: transport}
}

func (t *utlsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.transport.RoundTrip(req)
}

// contextDialer is implemented by proxy.Direct and the socks5/http proxy dialers
// (proxy.ContextDialer). dialContext uses it so an in-flight request's context
// cancellation aborts the dial instead of leaking it.
type contextDialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

func dialContext(ctx context.Context, dialer proxy.Dialer, network, addr string) (net.Conn, error) {
	if cd, ok := dialer.(contextDialer); ok {
		return cd.DialContext(ctx, network, addr)
	}
	return dialer.Dial(network, addr)
}

// utlsRoundTripperRegistry pools one shared *utlsRoundTripper (and therefore one
// shared http2.Transport connection pool) per resolved proxy URL. Previously
// NewUtlsHTTPClient built a fresh round tripper — and a fresh, empty connection
// pool — on every request, so every Codex/Claude upstream call opened a brand-new
// TCP+uTLS connection and never reused a kept-alive one. That outbound connect()
// + uTLS handshake was the dominant CPU cost under load.
//
// Keying by proxyURL is safe for per-credential sticky egress: the credential's
// egress identity is fully encoded in the proxy URL (see EffectiveProxyURL /
// credentialEgressProxyURL, which inject the credential's stable index as the
// proxy username), so two requests sharing a proxyURL share the same egress IP,
// and the pooled connections stay sticky to that egress. The number of distinct
// proxy URLs is bounded by the credential count.
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
