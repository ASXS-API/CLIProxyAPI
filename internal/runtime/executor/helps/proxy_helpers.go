package helps

import (
	"context"
	"net/http"
	"net/http/httptrace"
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
