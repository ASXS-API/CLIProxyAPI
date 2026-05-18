package helps

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestNewProxyAwareHTTPClientDirectBypassesGlobalProxy(t *testing.T) {
	resetProxyTransportPoolsForTest()

	var globalProxyHits atomic.Int64
	globalProxy := newTestHTTPProxy(t, &globalProxyHits)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(upstream.Close)

	client := NewProxyAwareHTTPClient(
		context.Background(),
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: globalProxy.URL}},
		&cliproxyauth.Auth{ProxyURL: "direct"},
		0,
	)

	resp, err := client.Get(upstream.URL)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	closeResponseBody(t, resp)

	if got := globalProxyHits.Load(); got != 0 {
		t.Fatalf("global proxy hits = %d, want 0", got)
	}
	if snapshot := proxyTransportPoolSnapshotForTest(globalProxy.URL); len(snapshot) != 0 {
		t.Fatalf("global proxy pool size = %d, want 0", len(snapshot))
	}
}

func TestNewProxyAwareHTTPClientUsesPooledConfigProxy(t *testing.T) {
	resetProxyTransportPoolsForTest()

	var proxyHits atomic.Int64
	proxy := newTestHTTPProxy(t, &proxyHits)
	client := NewProxyAwareHTTPClient(
		context.Background(),
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: proxy.URL}},
		&cliproxyauth.Auth{Provider: "codex"},
		0,
	)

	resp, err := client.Get("http://upstream.example.test/pooled")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	snapshot := proxyTransportPoolSnapshotForTest(proxy.URL)
	if len(snapshot) != 1 || snapshot[0] != 1 {
		t.Fatalf("active snapshot before close = %v, want [1]", snapshot)
	}
	closeResponseBody(t, resp)
	snapshot = proxyTransportPoolSnapshotForTest(proxy.URL)
	if len(snapshot) != 1 || snapshot[0] != 0 {
		t.Fatalf("active snapshot after close = %v, want [0]", snapshot)
	}
	if got := proxyHits.Load(); got != 1 {
		t.Fatalf("proxy hits = %d, want 1", got)
	}
}

func TestNewProxyAwareHTTPClientAuthProxyDoesNotUsePoolOrContextRoundTripper(t *testing.T) {
	resetProxyTransportPoolsForTest()

	var authProxyHits atomic.Int64
	authProxy := newTestHTTPProxy(t, &authProxyHits)
	var contextHits atomic.Int64
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		contextHits.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	}))

	client := NewProxyAwareHTTPClient(
		ctx,
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"}},
		&cliproxyauth.Auth{Provider: "codex", ProxyURL: authProxy.URL},
		0,
	)

	resp, err := client.Get("http://upstream.example.test/auth-proxy")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	closeResponseBody(t, resp)

	if got := authProxyHits.Load(); got != 1 {
		t.Fatalf("auth proxy hits = %d, want 1", got)
	}
	if got := contextHits.Load(); got != 0 {
		t.Fatalf("context round tripper hits = %d, want 0", got)
	}
	if snapshot := proxyTransportPoolSnapshotForTest(authProxy.URL); len(snapshot) != 0 {
		t.Fatalf("auth proxy pool size = %d, want 0", len(snapshot))
	}
}

func TestProxyTransportPoolOpensSecondTransportAfterActiveLimit(t *testing.T) {
	resetProxyTransportPoolsForTest()

	proxy := newTestHTTPProxy(t, nil)
	client := NewProxyAwareHTTPClient(
		context.Background(),
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: proxy.URL}},
		&cliproxyauth.Auth{Provider: "codex"},
		0,
	)

	responses := make([]*http.Response, 0, proxyPoolMaxActivePerTransport+1)
	t.Cleanup(func() {
		closeResponses(t, responses)
	})

	for i := 0; i < proxyPoolMaxActivePerTransport+1; i++ {
		resp, err := client.Get(fmt.Sprintf("http://upstream.example.test/stream-%d", i))
		if err != nil {
			t.Fatalf("Get(%d) error = %v", i, err)
		}
		responses = append(responses, resp)
	}

	snapshot := proxyTransportPoolSnapshotForTest(proxy.URL)
	if len(snapshot) != 2 || snapshot[0] != proxyPoolMaxActivePerTransport || snapshot[1] != 1 {
		t.Fatalf("active snapshot = %v, want [%d 1]", snapshot, proxyPoolMaxActivePerTransport)
	}
}

func TestProxyTransportPoolFallsBackWhenFull(t *testing.T) {
	resetProxyTransportPoolsForTest()

	var proxyHits atomic.Int64
	proxy := newTestHTTPProxy(t, &proxyHits)
	client := NewProxyAwareHTTPClient(
		context.Background(),
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: proxy.URL}},
		&cliproxyauth.Auth{Provider: "codex"},
		0,
	)

	maxActive := proxyPoolMaxTransports * proxyPoolMaxActivePerTransport
	responses := make([]*http.Response, 0, maxActive+1)
	t.Cleanup(func() {
		closeResponses(t, responses)
	})

	for i := 0; i < maxActive+1; i++ {
		resp, err := client.Get(fmt.Sprintf("http://upstream.example.test/full-%d", i))
		if err != nil {
			t.Fatalf("Get(%d) error = %v", i, err)
		}
		responses = append(responses, resp)
	}

	snapshot := proxyTransportPoolSnapshotForTest(proxy.URL)
	if len(snapshot) != proxyPoolMaxTransports {
		t.Fatalf("pool size = %d, want %d", len(snapshot), proxyPoolMaxTransports)
	}
	for i, active := range snapshot {
		if active != proxyPoolMaxActivePerTransport {
			t.Fatalf("transport %d active = %d, want %d", i, active, proxyPoolMaxActivePerTransport)
		}
	}
	if got := proxyHits.Load(); got != int64(maxActive+1) {
		t.Fatalf("proxy hits = %d, want %d", got, maxActive+1)
	}
}

func TestProxyTransportPoolReusesReleasedTransport(t *testing.T) {
	resetProxyTransportPoolsForTest()

	proxy := newTestHTTPProxy(t, nil)
	client := NewProxyAwareHTTPClient(
		context.Background(),
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: proxy.URL}},
		nil,
		0,
	)

	resp, err := client.Get("http://upstream.example.test/release-1")
	if err != nil {
		t.Fatalf("first Get() error = %v", err)
	}
	closeResponseBody(t, resp)

	resp, err = client.Get("http://upstream.example.test/release-2")
	if err != nil {
		t.Fatalf("second Get() error = %v", err)
	}
	defer closeResponseBody(t, resp)

	snapshot := proxyTransportPoolSnapshotForTest(proxy.URL)
	if len(snapshot) != 1 || snapshot[0] != 1 {
		t.Fatalf("active snapshot = %v, want [1]", snapshot)
	}
}

func TestProxyTransportPoolRoundTripErrorReleasesSlot(t *testing.T) {
	resetProxyTransportPoolsForTest()

	proxyURL := unusedHTTPProxyURL(t)
	client := NewProxyAwareHTTPClient(
		context.Background(),
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: proxyURL}},
		nil,
		0,
	)

	_, err := client.Get("http://upstream.example.test/error")
	if err == nil {
		t.Fatal("Get() error = nil, want error")
	}

	snapshot := proxyTransportPoolSnapshotForTest(proxyURL)
	if len(snapshot) != 1 || snapshot[0] != 0 {
		t.Fatalf("active snapshot after error = %v, want [0]", snapshot)
	}
}

func TestProxyTransportPoolConcurrentAcquireRelease(t *testing.T) {
	resetProxyTransportPoolsForTest()

	proxy := newTestHTTPProxy(t, nil)
	client := NewProxyAwareHTTPClient(
		context.Background(),
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: proxy.URL}},
		nil,
		0,
	)

	const requests = 64
	responses := make(chan *http.Response, requests)
	errs := make(chan error, requests)
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, err := client.Get(fmt.Sprintf("http://upstream.example.test/concurrent-%d", i))
			if err != nil {
				errs <- err
				return
			}
			responses <- resp
		}(i)
	}
	wg.Wait()
	close(responses)
	close(errs)

	for err := range errs {
		t.Fatalf("Get() error = %v", err)
	}

	var openResponses []*http.Response
	for resp := range responses {
		openResponses = append(openResponses, resp)
	}
	t.Cleanup(func() {
		closeResponses(t, openResponses)
	})

	snapshot := proxyTransportPoolSnapshotForTest(proxy.URL)
	for i, active := range snapshot {
		if active > proxyPoolMaxActivePerTransport {
			t.Fatalf("transport %d active = %d, want <= %d; snapshot=%v", i, active, proxyPoolMaxActivePerTransport, snapshot)
		}
	}
	if got := len(openResponses); got != requests {
		t.Fatalf("responses = %d, want %d", got, requests)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newTestHTTPProxy(t *testing.T, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(server.Close)
	return server
}

func unusedHTTPProxyURL(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	return "http://" + addr
}

func closeResponses(t *testing.T, responses []*http.Response) {
	t.Helper()
	for _, resp := range responses {
		closeResponseBody(t, resp)
	}
}

func closeResponseBody(t *testing.T, resp *http.Response) {
	t.Helper()
	if resp == nil || resp.Body == nil {
		return
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("response body close error = %v", err)
	}
}

func resetProxyTransportPoolsForTest() {
	globalProxyTransportPools = &proxyTransportPoolRegistry{pools: make(map[string]*proxyTransportPool)}
	globalProxyTransportID.Store(0)
	globalProxyStreamID.Store(0)
}

func proxyTransportPoolSnapshotForTest(proxyURL string) []int {
	pool := globalProxyTransportPools.pool(proxyURL)
	pool.mu.Lock()
	defer pool.mu.Unlock()

	active := make([]int, len(pool.transports))
	for i, transport := range pool.transports {
		active[i] = transport.active
	}
	return active
}
