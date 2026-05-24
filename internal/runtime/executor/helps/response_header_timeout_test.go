package helps

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

type slowReader struct {
	data  []byte
	delay time.Duration
	done  bool
}

func (r *slowReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	time.Sleep(r.delay)
	r.done = true
	return copy(p, r.data), io.EOF
}

func TestResponseHeaderTimeoutStartsAfterRequestBodyUpload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("ReadAll() error = %v", errRead)
			return
		}
		if string(body) != "payload" {
			t.Errorf("body = %q, want payload", body)
		}
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(server.Close)

	cfg := &config.Config{
		UpstreamResponseHeaderTimeout: config.UpstreamResponseHeaderTimeoutConfig{
			Enabled: true,
			Initial: config.RetryIntervalSeconds(0.02),
			Max:     config.RetryIntervalSeconds(0.02),
		},
	}
	client := &http.Client{
		Transport: newResponseHeaderRetryRoundTripper(http.DefaultTransport, cfg, "test"),
	}

	req, errReq := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, io.NopCloser(&slowReader{
		data:  []byte("payload"),
		delay: 80 * time.Millisecond,
	}))
	if errReq != nil {
		t.Fatalf("NewRequestWithContext() error = %v", errReq)
	}

	resp, errDo := client.Do(req)
	if errDo != nil {
		t.Fatalf("Do() error = %v", errDo)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		t.Fatalf("ReadAll(response) error = %v", errRead)
	}
	if string(body) != "ok" {
		t.Fatalf("response body = %q, want ok", body)
	}
}

func TestResponseHeaderTimeoutRetriesReplayableRequest(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("ReadAll() error = %v", errRead)
			return
		}
		if string(body) != "payload" {
			t.Errorf("body = %q, want payload", body)
		}
		if call == 1 {
			time.Sleep(80 * time.Millisecond)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(server.Close)

	cfg := &config.Config{
		UpstreamResponseHeaderTimeout: config.UpstreamResponseHeaderTimeoutConfig{
			Enabled: true,
			Initial: config.RetryIntervalSeconds(0.02),
			Max:     config.RetryIntervalSeconds(0.04),
		},
	}
	client := &http.Client{
		Transport: newResponseHeaderRetryRoundTripper(http.DefaultTransport, cfg, "test"),
	}

	req, errReq := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, bytes.NewReader([]byte("payload")))
	if errReq != nil {
		t.Fatalf("NewRequestWithContext() error = %v", errReq)
	}

	resp, errDo := client.Do(req)
	if errDo != nil {
		t.Fatalf("Do() error = %v", errDo)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		t.Fatalf("ReadAll(response) error = %v", errRead)
	}
	if string(body) != "ok" {
		t.Fatalf("response body = %q, want ok", body)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("calls = %d, want 2", got)
	}
}

func TestResponseHeaderTimeoutReturnsStatusErrorAfterRetries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		time.Sleep(80 * time.Millisecond)
	}))
	t.Cleanup(server.Close)

	cfg := &config.Config{
		UpstreamResponseHeaderTimeout: config.UpstreamResponseHeaderTimeoutConfig{
			Enabled: true,
			Initial: config.RetryIntervalSeconds(0.01),
			Max:     config.RetryIntervalSeconds(0.02),
		},
	}
	client := &http.Client{
		Transport: newResponseHeaderRetryRoundTripper(http.DefaultTransport, cfg, "test"),
	}

	req, errReq := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, bytes.NewReader([]byte("payload")))
	if errReq != nil {
		t.Fatalf("NewRequestWithContext() error = %v", errReq)
	}

	_, errDo := client.Do(req)
	var timeoutErr *ResponseHeaderTimeoutError
	if !errors.As(errDo, &timeoutErr) {
		t.Fatalf("error = %v, want ResponseHeaderTimeoutError", errDo)
	}
	if timeoutErr.StatusCode() != http.StatusRequestTimeout {
		t.Fatalf("StatusCode() = %d, want 408", timeoutErr.StatusCode())
	}
	if timeoutErr.Attempts != 2 {
		t.Fatalf("Attempts = %d, want 2", timeoutErr.Attempts)
	}
}
