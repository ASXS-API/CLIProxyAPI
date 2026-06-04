package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type temporaryAffinityTestCall struct {
	authID    string
	temporary bool
}

type temporaryAffinityTestExecutor struct {
	mu             sync.Mutex
	calls          []temporaryAffinityTestCall
	failTemporary  bool
	failAuthIDs    map[string]bool
	temporaryFails int
}

func (e *temporaryAffinityTestExecutor) Identifier() string { return "codex" }

func (e *temporaryAffinityTestExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	call := temporaryAffinityTestCall{}
	if auth != nil {
		call.authID = auth.ID
		call.temporary = auth.temporaryAffinity
	}
	e.calls = append(e.calls, call)
	if e.failTemporary && call.temporary {
		e.temporaryFails++
		return cliproxyexecutor.Response{}, errors.New("temporary auth failed")
	}
	if e.failAuthIDs != nil && e.failAuthIDs[call.authID] {
		return cliproxyexecutor.Response{}, errors.New("auth failed")
	}
	return cliproxyexecutor.Response{Payload: []byte(call.authID)}, nil
}

func (e *temporaryAffinityTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "ExecuteStream not implemented"}
}

func (e *temporaryAffinityTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *temporaryAffinityTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.Execute(ctx, auth, req, opts)
}

func (e *temporaryAffinityTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func (e *temporaryAffinityTestExecutor) snapshotCalls() []temporaryAffinityTestCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]temporaryAffinityTestCall, len(e.calls))
	copy(out, e.calls)
	return out
}

func newTemporaryAffinityTestManager(t *testing.T, selector Selector) (*Manager, *temporaryAffinityTestExecutor) {
	t.Helper()
	manager := NewManager(nil, selector, nil)
	executor := &temporaryAffinityTestExecutor{}
	manager.RegisterExecutor(executor)
	ctx := context.Background()
	for _, auth := range []*Auth{
		{ID: "auth-a", Provider: "codex", Label: "primary", Status: StatusActive},
		{ID: "auth-b", Provider: "codex", Label: "fallback", Status: StatusActive},
	} {
		if _, err := manager.Register(ctx, auth); err != nil {
			t.Fatalf("register auth %s: %v", auth.ID, err)
		}
	}
	return manager, executor
}

func temporaryAffinityRequest() (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	payload := []byte(`{"metadata":{"user_id":"user_test_account__session_00000000-0000-0000-0000-000000000001"}}`)
	return cliproxyexecutor.Request{Model: "", Payload: payload}, cliproxyexecutor.Options{OriginalRequest: payload}
}

func TestManagerExecute_SessionAffinityUsesTemporaryAuthAfterRemoval(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
	})
	defer selector.Stop()
	manager, executor := newTemporaryAffinityTestManager(t, selector)
	req, opts := temporaryAffinityRequest()
	ctx := context.Background()

	resp, err := manager.Execute(ctx, []string{"codex"}, req, opts)
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	if string(resp.Payload) != "auth-a" {
		t.Fatalf("first payload = %q, want auth-a", string(resp.Payload))
	}

	manager.Remove(ctx, "auth-a")
	if _, ok := manager.GetByID("auth-a"); ok {
		t.Fatalf("auth-a should be removed from local manager state")
	}

	resp, err = manager.Execute(ctx, []string{"codex"}, req, opts)
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if string(resp.Payload) != "auth-a" {
		t.Fatalf("second payload = %q, want temporary auth-a", string(resp.Payload))
	}

	calls := executor.snapshotCalls()
	if len(calls) != 2 {
		t.Fatalf("calls = %v, want 2 calls", calls)
	}
	if !calls[1].temporary || calls[1].authID != "auth-a" {
		t.Fatalf("second call = %+v, want temporary auth-a", calls[1])
	}
}

func TestManagerExecute_TemporaryAuthFailureRebindsAfterFallbackSuccess(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
	})
	defer selector.Stop()
	manager, executor := newTemporaryAffinityTestManager(t, selector)
	executor.failTemporary = true
	req, opts := temporaryAffinityRequest()
	ctx := context.Background()

	if _, err := manager.Execute(ctx, []string{"codex"}, req, opts); err != nil {
		t.Fatalf("initial Execute: %v", err)
	}
	manager.Remove(ctx, "auth-a")

	resp, err := manager.Execute(ctx, []string{"codex"}, req, opts)
	if err != nil {
		t.Fatalf("Execute after removal: %v", err)
	}
	if string(resp.Payload) != "auth-b" {
		t.Fatalf("payload after removal = %q, want auth-b fallback", string(resp.Payload))
	}

	calls := executor.snapshotCalls()
	tempFailures := 0
	for _, call := range calls {
		if call.authID == "auth-a" && call.temporary {
			tempFailures++
		}
	}
	if tempFailures != 1 {
		t.Fatalf("temporary auth failures = %d, want 1; calls=%+v", tempFailures, calls)
	}

	resp, err = manager.Execute(ctx, []string{"codex"}, req, opts)
	if err != nil {
		t.Fatalf("Execute after successful fallback rebind: %v", err)
	}
	if string(resp.Payload) != "auth-b" {
		t.Fatalf("payload after successful fallback rebind = %q, want auth-b", string(resp.Payload))
	}
	callsAfterRebind := executor.snapshotCalls()
	for _, call := range callsAfterRebind[len(calls):] {
		if call.authID == "auth-a" && call.temporary {
			t.Fatalf("temporary auth was retried after successful fallback rebind; new calls=%+v", callsAfterRebind[len(calls):])
		}
	}
}

func TestManagerExecute_TemporaryAuthFailureDoesNotRebindUntilFallbackSucceeds(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
	})
	defer selector.Stop()
	manager, executor := newTemporaryAffinityTestManager(t, selector)
	executor.failTemporary = true
	executor.failAuthIDs = map[string]bool{"auth-b": true}
	req, opts := temporaryAffinityRequest()
	ctx := context.Background()

	if _, err := manager.Execute(ctx, []string{"codex"}, req, opts); err != nil {
		t.Fatalf("initial Execute: %v", err)
	}
	manager.Remove(ctx, "auth-a")

	if _, err := manager.Execute(ctx, []string{"codex"}, req, opts); err == nil {
		t.Fatalf("Execute after removal with failing fallback succeeded, want error")
	}

	callsAfterFailure := executor.snapshotCalls()
	executor.failAuthIDs["auth-b"] = false

	resp, err := manager.Execute(ctx, []string{"codex"}, req, opts)
	if err != nil {
		t.Fatalf("Execute after fallback recovery: %v", err)
	}
	if string(resp.Payload) != "auth-b" {
		t.Fatalf("payload after fallback recovery = %q, want auth-b", string(resp.Payload))
	}

	newCalls := executor.snapshotCalls()[len(callsAfterFailure):]
	if len(newCalls) < 2 {
		t.Fatalf("new calls = %+v, want temporary auth-a followed by auth-b", newCalls)
	}
	if !newCalls[0].temporary || newCalls[0].authID != "auth-a" {
		t.Fatalf("first retry call = %+v, want temporary auth-a before rebind", newCalls[0])
	}
	if newCalls[len(newCalls)-1].temporary || newCalls[len(newCalls)-1].authID != "auth-b" {
		t.Fatalf("last retry call = %+v, want successful auth-b fallback", newCalls[len(newCalls)-1])
	}
}

func TestManagerExecute_LocalDisabledAuthDoesNotUseTemporaryAuth(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
	})
	defer selector.Stop()
	manager, executor := newTemporaryAffinityTestManager(t, selector)
	req, opts := temporaryAffinityRequest()
	ctx := context.Background()

	if _, err := manager.Execute(ctx, []string{"codex"}, req, opts); err != nil {
		t.Fatalf("initial Execute: %v", err)
	}
	if _, err := manager.Update(ctx, &Auth{ID: "auth-a", Provider: "codex", Label: "primary", Disabled: true, Status: StatusDisabled}); err != nil {
		t.Fatalf("disable auth-a: %v", err)
	}

	resp, err := manager.Execute(ctx, []string{"codex"}, req, opts)
	if err != nil {
		t.Fatalf("Execute after disabling bound auth: %v", err)
	}
	if string(resp.Payload) != "auth-b" {
		t.Fatalf("payload after disabling bound auth = %q, want auth-b", string(resp.Payload))
	}
	calls := executor.snapshotCalls()
	if len(calls) != 2 {
		t.Fatalf("calls = %+v, want 2 calls", calls)
	}
	if calls[1].temporary {
		t.Fatalf("disabled local auth should not be executed from temporary memory: calls=%+v", calls)
	}
}

func TestManagerExecute_NoTemporaryAuthWithoutSessionAffinity(t *testing.T) {
	manager, executor := newTemporaryAffinityTestManager(t, &RoundRobinSelector{})
	req, opts := temporaryAffinityRequest()
	ctx := context.Background()

	if _, err := manager.Execute(ctx, []string{"codex"}, req, opts); err != nil {
		t.Fatalf("initial Execute: %v", err)
	}
	manager.Remove(ctx, "auth-a")
	resp, err := manager.Execute(ctx, []string{"codex"}, req, opts)
	if err != nil {
		t.Fatalf("Execute after removal: %v", err)
	}
	if string(resp.Payload) != "auth-b" {
		t.Fatalf("payload after removal = %q, want auth-b", string(resp.Payload))
	}
	for _, call := range executor.snapshotCalls() {
		if call.temporary {
			t.Fatalf("temporary auth used without session affinity: calls=%+v", executor.snapshotCalls())
		}
	}
}
