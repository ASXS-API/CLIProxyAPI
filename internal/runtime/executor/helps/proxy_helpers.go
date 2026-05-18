package helps

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
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
				httpClient.Transport = newInactiveProxyReuseRoundTripper(ctx, transport, provider, proxyReuseReasonAuthProxy)
				return httpClient
			}
			httpClient.Transport = newInactiveProxyReuseRoundTripper(ctx, roundTripperFromContext(ctx), provider, proxyReuseReasonBuildFailed)
			return httpClient
		}
	}

	// Priority 2: cfg.ProxyURL uses the global transport pool.
	if cfg != nil {
		cfgProxyURL := strings.TrimSpace(cfg.ProxyURL)
		if cfgProxyURL != "" {
			httpClient.Transport = newProxyPoolRoundTripper(ctx, cfgProxyURL, provider, roundTripperFromContext(ctx))
			return httpClient
		}
	}

	// Priority 3: Use RoundTripper from context (typically from RoundTripperFor)
	httpClient.Transport = newInactiveProxyReuseRoundTripper(ctx, roundTripperFromContext(ctx), provider, proxyReuseReasonNoProxy)

	return httpClient
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
