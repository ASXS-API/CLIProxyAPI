package helps

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	log "github.com/sirupsen/logrus"
)

type responseHeaderRetryRoundTripper struct {
	base     http.RoundTripper
	provider string
	initial  time.Duration
	maximum  time.Duration
}

type cancelOnCloseReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
	once   sync.Once
}

func (c *cancelOnCloseReadCloser) Close() error {
	errClose := c.ReadCloser.Close()
	c.once.Do(c.cancel)
	return errClose
}

// ResponseHeaderTimeoutError marks an upstream timeout that happened after the
// request body was written and before response headers were received.
type ResponseHeaderTimeoutError struct {
	HeaderTimeout time.Duration
	Attempts      int
	Method        string
	Host          string
	Path          string
	Cause         error
}

func (e *ResponseHeaderTimeoutError) Error() string {
	if e == nil {
		return ""
	}
	target := e.Host
	if e.Path != "" {
		target += e.Path
	}
	if target == "" {
		target = "upstream"
	}
	if e.Attempts <= 1 {
		return fmt.Sprintf("upstream response header timeout after %s waiting for %s", e.HeaderTimeout, target)
	}
	return fmt.Sprintf("upstream response header timeout after %d attempts, last wait %s for %s", e.Attempts, e.HeaderTimeout, target)
}

func (e *ResponseHeaderTimeoutError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *ResponseHeaderTimeoutError) StatusCode() int {
	return http.StatusRequestTimeout
}

func (e *ResponseHeaderTimeoutError) Timeout() bool {
	return true
}

func (e *ResponseHeaderTimeoutError) Temporary() bool {
	return true
}

func newResponseHeaderRetryRoundTripper(base http.RoundTripper, cfg *config.Config, provider string) http.RoundTripper {
	enabled, initial, maximum := upstreamResponseHeaderTimeoutSettings(cfg)
	if !enabled {
		return base
	}
	if base == nil {
		base = http.DefaultTransport
	}
	return &responseHeaderRetryRoundTripper{
		base:     base,
		provider: strings.TrimSpace(provider),
		initial:  initial,
		maximum:  maximum,
	}
}

func upstreamResponseHeaderTimeoutSettings(cfg *config.Config) (bool, time.Duration, time.Duration) {
	if cfg == nil {
		return false, 0, 0
	}
	return cfg.UpstreamResponseHeaderTimeout.Effective()
}

func (rt *responseHeaderRetryRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
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

	timeouts := responseHeaderTimeoutAttempts(rt.initial, rt.maximum)
	if len(timeouts) == 0 {
		return base.RoundTrip(req)
	}

	replayable := requestBodyReplayable(req)
	var lastTimeout *ResponseHeaderTimeoutError
	for idx, timeout := range timeouts {
		attemptReq, errReq := requestForResponseHeaderAttempt(req, idx)
		if errReq != nil {
			logResponseHeaderTimeoutReplayFailure(req, rt.provider, idx+1, errReq)
			if lastTimeout != nil {
				return nil, lastTimeout
			}
			return nil, errReq
		}

		resp, errRoundTrip := rt.roundTripAttempt(attemptReq, timeout, idx+1)
		if errRoundTrip == nil {
			if idx > 0 {
				logResponseHeaderTimeoutRetrySuccess(req, rt.provider, idx+1, timeout)
			}
			return resp, nil
		}

		timeoutErr, ok := errRoundTrip.(*ResponseHeaderTimeoutError)
		if !ok {
			return resp, errRoundTrip
		}
		lastTimeout = timeoutErr

		if idx == len(timeouts)-1 {
			logResponseHeaderTimeoutExhausted(req, rt.provider, idx+1, timeout)
			return nil, timeoutErr
		}
		if !replayable {
			logResponseHeaderTimeoutNotReplayable(req, rt.provider, idx+1, timeout)
			return nil, timeoutErr
		}

		nextTimeout := timeouts[idx+1]
		logResponseHeaderTimeoutRetry(req, rt.provider, idx+1, timeout, nextTimeout)
	}

	if lastTimeout != nil {
		return nil, lastTimeout
	}
	return base.RoundTrip(req)
}

func (rt *responseHeaderRetryRoundTripper) roundTripAttempt(req *http.Request, timeout time.Duration, attempt int) (*http.Response, error) {
	if timeout <= 0 {
		return rt.base.RoundTrip(req)
	}

	ctx, cancel := context.WithCancel(req.Context())

	var timedOut atomic.Bool
	var timerMu sync.Mutex
	var timer *time.Timer
	startTimer := func(info httptrace.WroteRequestInfo) {
		// WroteRequest fires after the request headers and body have been written.
		if info.Err != nil {
			return
		}
		timerMu.Lock()
		if timer == nil {
			timer = time.AfterFunc(timeout, func() {
				timedOut.Store(true)
				cancel()
			})
		}
		timerMu.Unlock()
	}
	stopTimer := func() {
		timerMu.Lock()
		if timer != nil {
			timer.Stop()
		}
		timerMu.Unlock()
	}

	trace := &httptrace.ClientTrace{WroteRequest: startTimer}
	tracedReq := req.WithContext(httptrace.WithClientTrace(ctx, trace))
	resp, errRoundTrip := rt.base.RoundTrip(tracedReq)
	stopTimer()
	if timedOut.Load() && errRoundTrip != nil {
		cancel()
		return nil, &ResponseHeaderTimeoutError{
			HeaderTimeout: timeout,
			Attempts:      attempt,
			Method:        safeRequestMethod(req),
			Host:          safeRequestHost(req),
			Path:          safeRequestPath(req),
			Cause:         errRoundTrip,
		}
	}
	if errRoundTrip != nil {
		cancel()
		return resp, errRoundTrip
	}
	if resp == nil || resp.Body == nil {
		cancel()
		return resp, nil
	}
	resp.Body = &cancelOnCloseReadCloser{ReadCloser: resp.Body, cancel: cancel}
	return resp, errRoundTrip
}

func responseHeaderTimeoutAttempts(initial, maximum time.Duration) []time.Duration {
	if initial <= 0 || maximum <= 0 {
		return nil
	}
	if maximum < initial {
		maximum = initial
	}
	attempts := make([]time.Duration, 0, 4)
	for current := initial; ; {
		attempts = append(attempts, current)
		if current >= maximum {
			break
		}
		next := current * 2
		if next <= current || next > maximum {
			next = maximum
		}
		current = next
	}
	return attempts
}

func requestBodyReplayable(req *http.Request) bool {
	if req == nil || req.Body == nil || req.Body == http.NoBody {
		return true
	}
	return req.GetBody != nil
}

func requestForResponseHeaderAttempt(req *http.Request, attempt int) (*http.Request, error) {
	if req == nil {
		return nil, fmt.Errorf("request is nil")
	}
	attemptReq := req.Clone(req.Context())
	attemptReq.GetBody = req.GetBody
	if attempt == 0 {
		attemptReq.Body = req.Body
		return attemptReq, nil
	}
	if req.Body == nil || req.Body == http.NoBody {
		attemptReq.Body = req.Body
		return attemptReq, nil
	}
	if req.GetBody == nil {
		return nil, fmt.Errorf("request body is not replayable")
	}
	body, errGetBody := req.GetBody()
	if errGetBody != nil {
		return nil, fmt.Errorf("recreate request body for retry: %w", errGetBody)
	}
	attemptReq.Body = body
	return attemptReq, nil
}

func responseHeaderTimeoutLogEntry(req *http.Request, provider string) *log.Entry {
	fields := log.Fields{
		"provider": provider,
		"method":   safeRequestMethod(req),
		"host":     safeRequestHost(req),
		"path":     safeRequestPath(req),
	}
	if req != nil {
		if reqID := logging.GetRequestID(req.Context()); reqID != "" {
			fields["request_id"] = reqID
		}
	}
	return log.WithFields(fields)
}

func logResponseHeaderTimeoutRetry(req *http.Request, provider string, attempt int, timeout, nextTimeout time.Duration) {
	responseHeaderTimeoutLogEntry(req, provider).WithFields(log.Fields{
		"attempt":      attempt,
		"timeout":      timeout.String(),
		"next_timeout": nextTimeout.String(),
	}).Warn("upstream response header timeout; retrying")
}

func logResponseHeaderTimeoutRetrySuccess(req *http.Request, provider string, attempt int, timeout time.Duration) {
	responseHeaderTimeoutLogEntry(req, provider).WithFields(log.Fields{
		"attempt": attempt,
		"timeout": timeout.String(),
	}).Warn("upstream response header timeout retry succeeded")
}

func logResponseHeaderTimeoutExhausted(req *http.Request, provider string, attempt int, timeout time.Duration) {
	responseHeaderTimeoutLogEntry(req, provider).WithFields(log.Fields{
		"attempt": attempt,
		"timeout": timeout.String(),
	}).Warn("upstream response header timeout; retries exhausted")
}

func logResponseHeaderTimeoutNotReplayable(req *http.Request, provider string, attempt int, timeout time.Duration) {
	responseHeaderTimeoutLogEntry(req, provider).WithFields(log.Fields{
		"attempt": attempt,
		"timeout": timeout.String(),
	}).Warn("upstream response header timeout; request body is not replayable")
}

func logResponseHeaderTimeoutReplayFailure(req *http.Request, provider string, attempt int, err error) {
	responseHeaderTimeoutLogEntry(req, provider).WithFields(log.Fields{
		"attempt": attempt,
		"error":   err,
	}).Warn("upstream response header timeout retry failed to recreate request body")
}

func safeRequestMethod(req *http.Request) string {
	if req == nil {
		return ""
	}
	return req.Method
}

func safeRequestHost(req *http.Request) string {
	if req == nil || req.URL == nil {
		return ""
	}
	return req.URL.Host
}

func safeRequestPath(req *http.Request) string {
	if req == nil || req.URL == nil {
		return ""
	}
	path := req.URL.EscapedPath()
	if path == "" {
		return "/"
	}
	return path
}
