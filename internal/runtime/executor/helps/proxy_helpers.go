package helps

import (
	"context"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// NewProxyAwareHTTPClient creates an HTTP client with proper proxy configuration priority:
// 1. Use auth.ProxyURL if configured (highest priority, isolated per client)
// 2. Use pooled cfg.ProxyURL transport if auth proxy is not configured
// 3. Use RoundTripper from context if neither are configured
//
// Parameters:
//   - ctx: The context containing optional RoundTripper
//   - cfg: The application configuration
//   - auth: The authentication information
//   - timeout: The client timeout (0 means no timeout)
//
// Returns:
//   - *http.Client: An HTTP client with configured proxy or transport
func NewProxyAwareHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	httpClient := &http.Client{}
	if timeout > 0 {
		httpClient.Timeout = timeout
	}

	provider := proxyReuseProvider(auth)

	// Priority 1: auth.ProxyURL remains isolated and never uses the global pool.
	if auth != nil {
		authProxyURL := strings.TrimSpace(auth.ProxyURL)
		if authProxyURL != "" {
			if transport := buildProxyTransport(authProxyURL); transport != nil {
				httpClient.Transport = newConfiguredUpstreamRoundTripper(ctx, cfg, provider, newInactiveProxyReuseRoundTripper(ctx, transport, provider, proxyReuseReasonAuthProxy))
				return httpClient
			}
			httpClient.Transport = newConfiguredUpstreamRoundTripper(ctx, cfg, provider, newInactiveProxyReuseRoundTripper(ctx, roundTripperFromContext(ctx), provider, proxyReuseReasonBuildFailed))
			return httpClient
		}
		// Priority 1.5: credential-egress proxy — per-credential sticky egress,
		// kept isolated per credential like an explicit auth proxy.
		if egressProxyURL := credentialEgressProxyURL(cfg, auth); egressProxyURL != "" {
			if transport := buildProxyTransport(egressProxyURL); transport != nil {
				httpClient.Transport = newConfiguredUpstreamRoundTripper(ctx, cfg, provider, newInactiveProxyReuseRoundTripper(ctx, transport, provider, proxyReuseReasonAuthProxy))
				return httpClient
			}
			httpClient.Transport = newConfiguredUpstreamRoundTripper(ctx, cfg, provider, newInactiveProxyReuseRoundTripper(ctx, roundTripperFromContext(ctx), provider, proxyReuseReasonBuildFailed))
			return httpClient
		}
	}

	// Priority 2: cfg.ProxyURL uses the global transport pool.
	if cfg != nil {
		cfgProxyURL := strings.TrimSpace(cfg.ProxyURL)
		if cfgProxyURL != "" {
			httpClient.Transport = newConfiguredUpstreamRoundTripper(ctx, cfg, provider, newProxyPoolRoundTripper(ctx, cfgProxyURL, provider, roundTripperFromContext(ctx)))
			return httpClient
		}
	}

	// Priority 3: Use RoundTripper from context (typically from RoundTripperFor)
	httpClient.Transport = newConfiguredUpstreamRoundTripper(ctx, cfg, provider, newInactiveProxyReuseRoundTripper(ctx, roundTripperFromContext(ctx), provider, proxyReuseReasonNoProxy))

	return httpClient
}

func newConfiguredUpstreamRoundTripper(ctx context.Context, cfg *config.Config, provider string, base http.RoundTripper) http.RoundTripper {
	return newResponseHeaderRetryRoundTripper(newUpstreamTTFBRoundTripper(ctx, base), cfg, provider)
}

type upstreamTTFBRoundTripper struct {
	ctx  context.Context
	base http.RoundTripper
}

func newUpstreamTTFBRoundTripper(ctx context.Context, base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &upstreamTTFBRoundTripper{ctx: ctx, base: base}
}

func (rt *upstreamTTFBRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt == nil {
		return http.DefaultTransport.RoundTrip(req)
	}
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}
	if req == nil {
		return base.RoundTrip(req)
	}

	start := time.Now()
	trace := &httptrace.ClientTrace{
		GotFirstResponseByte: func() {
			logging.RecordUpstreamTTFB(rt.ctx, time.Since(start))
		},
	}
	tracedReq := req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	resp, err := base.RoundTrip(tracedReq)
	if err == nil && resp != nil {
		logging.RecordUpstreamTTFB(rt.ctx, time.Since(start))
	}
	return resp, err
}

func roundTripperFromContext(ctx context.Context) http.RoundTripper {
	if ctx == nil {
		return nil
	}
	if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
		return rt
	}
	return nil
}

// buildProxyTransport creates an HTTP transport configured for the given proxy URL.
// It supports SOCKS5, HTTP, and HTTPS proxy protocols.
//
// Parameters:
//   - proxyURL: The proxy URL string (e.g., "socks5://user:pass@host:port", "http://host:port")
//
// Returns:
//   - *http.Transport: A configured transport, or nil if the proxy URL is invalid
func buildProxyTransport(proxyURL string) *http.Transport {
	transport, _, errBuild := proxyutil.BuildHTTPTransport(proxyURL)
	if errBuild != nil {
		log.Errorf("%v", errBuild)
		return nil
	}
	return transport
}

// EffectiveProxyURL resolves the proxy URL a credential should use for outbound
// upstream traffic, in precedence order:
//  1. auth.ProxyURL — explicit per-credential override (including "direct"/"none")
//  2. credential-egress proxy — cfg.CredentialEgressProxy with the credential's
//     stable index injected as the username (auto per-credential sticky egress)
//  3. cfg.ProxyURL — the global proxy
//
// Call sites that take a single resolved proxy URL string (auth refresh clients,
// websocket/utls dialers) should use this so all outbound paths for a credential
// share the same egress identity.
func EffectiveProxyURL(cfg *config.Config, auth *cliproxyauth.Auth) string {
	if auth != nil {
		if explicit := strings.TrimSpace(auth.ProxyURL); explicit != "" {
			return explicit
		}
	}
	if egress := credentialEgressProxyURL(cfg, auth); egress != "" {
		return egress
	}
	if cfg != nil {
		return strings.TrimSpace(cfg.ProxyURL)
	}
	return ""
}

// credentialEgressProxyURL returns the configured credential-egress proxy URL
// with the credential's stable index injected as the username, or "" when the
// feature is unconfigured or the credential has no derivable index.
func credentialEgressProxyURL(cfg *config.Config, auth *cliproxyauth.Auth) string {
	if cfg == nil || auth == nil {
		return ""
	}
	base := strings.TrimSpace(cfg.CredentialEgressProxy)
	if base == "" {
		return ""
	}
	idx := strings.TrimSpace(auth.EnsureIndex())
	if idx == "" {
		return ""
	}
	return injectProxyUsername(base, idx)
}

// injectProxyUsername sets the userinfo username of a proxy URL to username,
// preserving any configured password (used as the shared secret) and the
// scheme/host. Returns "" if the base URL cannot be parsed.
func injectProxyUsername(base, username string) string {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return ""
	}
	password := ""
	if u.User != nil {
		if pw, ok := u.User.Password(); ok {
			password = pw
		}
	}
	u.User = url.UserPassword(username, password)
	return u.String()
}
