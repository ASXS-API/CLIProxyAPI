package helps

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type utlsClientRoundTripFunc func(*http.Request) (*http.Response, error)

func (f utlsClientRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestNewUtlsHTTPClientUsesContextRoundTripperForProtectedHost(t *testing.T) {
	t.Parallel()

	called := false
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", utlsClientRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		called = true
		if req.URL.Hostname() != "chatgpt.com" {
			t.Fatalf("hostname = %q, want chatgpt.com", req.URL.Hostname())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("{}")),
			Request:    req,
		}, nil
	}))

	client := NewUtlsHTTPClient(ctx, nil, nil, 0)
	resp, err := client.Get("https://chatgpt.com/backend-api/codex/responses")
	if err != nil {
		t.Fatalf("client.Get returned error: %v", err)
	}
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatalf("response body close returned error: %v", errClose)
	}
	if !called {
		t.Fatal("expected context RoundTripper to handle protected host request")
	}
}

func TestUtlsRoundTripperRegistryPoolsByProxyURL(t *testing.T) {
	t.Parallel()

	reg := &utlsRoundTripperRegistry{entries: make(map[string]*utlsRoundTripper)}

	const proxyA = "socks5://1:secret@127.0.0.1:1080"
	a1 := reg.get(proxyA)
	a2 := reg.get(proxyA)
	if a1 != a2 {
		t.Fatal("same proxy URL must return the same pooled round tripper")
	}

	b := reg.get("socks5://2:secret@127.0.0.1:1080")
	if a1 == b {
		t.Fatal("different proxy URLs (sticky egress identities) must return different round trippers")
	}

	direct1 := reg.get("")
	direct2 := reg.get("")
	if direct1 != direct2 {
		t.Fatal("empty proxy URL (direct) must return the same pooled round tripper")
	}
	if direct1 == a1 {
		t.Fatal("direct and proxied round trippers must differ")
	}
}
