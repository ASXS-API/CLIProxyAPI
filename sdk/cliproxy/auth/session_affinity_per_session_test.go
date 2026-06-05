package auth

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// TestTemporaryAuthStore_PerSessionFailureCountingAndDedup verifies that failure
// counting is scoped per session (cache key), counts at most once per request,
// and that one session reaching the threshold does not affect another session.
func TestTemporaryAuthStore_PerSessionFailureCountingAndDedup(t *testing.T) {
	store := newSessionAffinityTemporaryAuthStore(time.Hour, 5)
	defer store.Stop()

	src := &Auth{ID: "auth-x", Provider: "codex", Status: StatusActive}
	store.rememberLocal(src)
	store.markDeleted(src)

	tempAuth, ok := store.get("auth-x")
	if !ok || !tempAuth.temporaryAffinity {
		t.Fatalf("expected temporary auth from store, got ok=%v auth=%+v", ok, tempAuth)
	}

	failFor := func(cacheKey, requestID string) bool {
		a := tempAuth.Clone()
		a.temporaryAffinityCacheKey = cacheKey
		return store.recordFailure(a, errors.New("boom"), requestID)
	}

	// Same request ID must not increment more than once (multi-model retries).
	failFor("K1", "r1")
	failFor("K1", "r1")
	failFor("K1", "r1")
	if store.sessionExceeded("auth-x", "K1") {
		t.Fatalf("K1 should not be exceeded after a single deduped request")
	}

	// Four more distinct requests -> 5 total -> exceeded.
	for i := 2; i <= 5; i++ {
		failFor("K1", "r"+strconv.Itoa(i))
	}
	if !store.sessionExceeded("auth-x", "K1") {
		t.Fatalf("K1 should be exceeded after 5 distinct request failures")
	}

	// A different session must be unaffected.
	if store.sessionExceeded("auth-x", "K2") {
		t.Fatalf("K2 must be independent of K1 failures")
	}

	// Success on K1 resets only that session's streak.
	successAuth := tempAuth.Clone()
	successAuth.temporaryAffinityCacheKey = "K1"
	store.recordSuccess(successAuth)
	if store.sessionExceeded("auth-x", "K1") {
		t.Fatalf("K1 streak should be cleared after success")
	}
}

// TestTemporaryAuthStore_RememberLocalClearsSessionFailures verifies that
// re-adding the local credential drops all per-session failure streaks.
func TestTemporaryAuthStore_RememberLocalClearsSessionFailures(t *testing.T) {
	store := newSessionAffinityTemporaryAuthStore(time.Hour, 5)
	defer store.Stop()

	src := &Auth{ID: "auth-x", Provider: "codex", Status: StatusActive}
	store.rememberLocal(src)
	store.markDeleted(src)

	tempAuth, _ := store.get("auth-x")
	for i := 0; i < 5; i++ {
		a := tempAuth.Clone()
		a.temporaryAffinityCacheKey = "K1"
		store.recordFailure(a, errors.New("boom"), "r"+strconv.Itoa(i))
	}
	if !store.sessionExceeded("auth-x", "K1") {
		t.Fatalf("precondition: K1 should be exceeded")
	}

	store.rememberLocal(src)
	if store.sessionExceeded("auth-x", "K1") {
		t.Fatalf("rememberLocal must clear per-session failure streaks")
	}
}

// TestSessionAffinity_DetachExceededSessionWhenLiveAvailable verifies that once a
// session exhausts its per-session budget it is routed to a live credential
// (and the binding is rebound), while the shared temporary entry survives.
func TestSessionAffinity_DetachExceededSessionWhenLiveAvailable(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
	})
	defer selector.Stop()
	ctx := context.Background()

	payload := []byte(`{"metadata":{"user_id":"user_x_account__session_00000000-0000-0000-0000-0000000000aa"}}`)
	opts := cliproxyexecutor.Options{OriginalRequest: payload}
	primaryID := ExtractSessionID(nil, payload, nil)
	cacheKey := "codex::" + primaryID + "::"

	live := &Auth{ID: "live-1", Provider: "codex", Status: StatusActive}
	tempSrc := &Auth{ID: "temp-1", Provider: "codex", Status: StatusActive}

	// Simulate temp-1 having been local then removed, with the session bound to it.
	selector.temporaryAuths.rememberLocal(tempSrc)
	selector.temporaryAuths.markDeleted(tempSrc)
	selector.cache.Set(cacheKey, "temp-1")

	// Not yet exceeded: the temporary credential is served even though a live one exists.
	got, err := selector.Pick(ctx, "codex", "", opts, []*Auth{live})
	if err != nil {
		t.Fatalf("initial pick: %v", err)
	}
	if got.ID != "temp-1" || !got.temporaryAffinity {
		t.Fatalf("expected temporary temp-1, got id=%s temporary=%v", got.ID, got.temporaryAffinity)
	}

	for i := 0; i < 5; i++ {
		a := got.Clone()
		selector.RecordTemporaryAuthFailure(a, errors.New("boom"), "r"+strconv.Itoa(i))
	}

	// Exceeded + live available -> detach to the live credential.
	got2, err := selector.Pick(ctx, "codex", "", opts, []*Auth{live})
	if err != nil {
		t.Fatalf("pick after exceed: %v", err)
	}
	if got2.ID != "live-1" || got2.temporaryAffinity {
		t.Fatalf("expected detach to live-1, got id=%s temporary=%v", got2.ID, got2.temporaryAffinity)
	}

	// The shared temporary entry must still exist for other sessions.
	if _, ok := selector.temporaryAuths.get("temp-1"); !ok {
		t.Fatalf("temporary entry temp-1 must survive a single session's failures")
	}
}

// TestFilterExecutionModels_TemporaryAffinityBypassesBlocked verifies that a
// temporary (in-memory) credential whose frozen snapshot is marked unavailable
// is still attempted, so residual value can be squeezed out of it.
func TestFilterExecutionModels_TemporaryAffinityBypassesBlocked(t *testing.T) {
	m := NewManager(nil, &RoundRobinSelector{}, nil)
	auth := &Auth{
		ID:       "blocked-1",
		Provider: "codex",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			"gpt-5.5": {Unavailable: true, NextRetryAfter: timeNowPlusHour()},
		},
	}

	// A normal credential with a blocked model yields no executable models.
	if got := m.filterExecutionModels(auth, "gpt-5.5", []string{"gpt-5.5"}, false); len(got) != 0 {
		t.Fatalf("blocked non-temporary auth should yield no models, got %v", got)
	}

	// The same credential as a temporary-affinity snapshot bypasses the block.
	auth.temporaryAffinity = true
	got := m.filterExecutionModels(auth, "gpt-5.5", []string{"gpt-5.5"}, false)
	if len(got) != 1 || got[0] != "gpt-5.5" {
		t.Fatalf("temporary-affinity auth must bypass the blocked filter, got %v", got)
	}
}

func timeNowPlusHour() time.Time { return time.Now().Add(time.Hour) }

// TestSessionAffinity_KeepTemporaryWhenNoLiveAvailable verifies that an exceeded
// session keeps using the temporary credential (degraded) rather than hard
// failing when there is no live credential to take over.
func TestSessionAffinity_KeepTemporaryWhenNoLiveAvailable(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
	})
	defer selector.Stop()
	ctx := context.Background()

	payload := []byte(`{"metadata":{"user_id":"user_y_account__session_00000000-0000-0000-0000-0000000000bb"}}`)
	opts := cliproxyexecutor.Options{OriginalRequest: payload}
	primaryID := ExtractSessionID(nil, payload, nil)
	cacheKey := "codex::" + primaryID + "::"

	tempSrc := &Auth{ID: "temp-1", Provider: "codex", Status: StatusActive}
	selector.temporaryAuths.rememberLocal(tempSrc)
	selector.temporaryAuths.markDeleted(tempSrc)
	selector.cache.Set(cacheKey, "temp-1")

	// Drive failures using a temp-affinity auth carrying the session cache key.
	failAuth := &Auth{ID: "temp-1", Provider: "codex"}
	failAuth.temporaryAffinity = true
	failAuth.temporaryAffinityCacheKey = cacheKey
	for i := 0; i < 5; i++ {
		selector.RecordTemporaryAuthFailure(failAuth.Clone(), errors.New("boom"), "r"+strconv.Itoa(i))
	}
	if !selector.temporaryAuths.sessionExceeded("temp-1", cacheKey) {
		t.Fatalf("precondition: session should be exceeded")
	}

	// No live credential available -> must still serve the temporary credential.
	got, err := selector.Pick(ctx, "codex", "", opts, nil)
	if err != nil {
		t.Fatalf("pick with no live auth returned error: %v", err)
	}
	if got.ID != "temp-1" || !got.temporaryAffinity {
		t.Fatalf("expected degraded temp-1, got id=%s temporary=%v", got.ID, got.temporaryAffinity)
	}
}
