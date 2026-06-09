package auth

import (
	"testing"
	"time"
)

// TestTemporaryAuthStore_CleanupSweepThrottled verifies that the expiry/prune
// sweep invoked from the hot per-request paths (rememberLocal/markDeleted/get) is
// rate-limited: a second call within sessionAffinityTemporaryAuthCleanupSweepInterval
// must not re-run the O(num_auths × sessions) sweep, while a call after the
// interval still reclaims expired entries. The dedicated cleanupLoop goroutine
// remains the correctness backstop; the throttle only removes the per-request cost.
func TestTemporaryAuthStore_CleanupSweepThrottled(t *testing.T) {
	store := newSessionAffinityTemporaryAuthStore(time.Hour, 5)
	defer store.Stop()

	// First hot-path call always sweeps and stamps lastCleanup ~= now.
	store.rememberLocal(&Auth{ID: "a", Provider: "codex", Status: StatusActive})
	if store.lastCleanup.IsZero() {
		t.Fatal("first sweep must stamp lastCleanup")
	}

	// Inject an already-expired ghost entry directly under the lock.
	store.mu.Lock()
	store.auths["ghost"] = &sessionAffinityTemporaryAuthEntry{
		auth:      &Auth{ID: "ghost"},
		expiresAt: time.Now().Add(-time.Hour),
	}
	store.mu.Unlock()

	// A second hot-path call within the throttle window must be suppressed, so the
	// expired ghost survives — proving the per-request sweep no longer runs.
	store.rememberLocal(&Auth{ID: "b", Provider: "codex", Status: StatusActive})
	store.mu.Lock()
	_, ghostSurvives := store.auths["ghost"]
	store.mu.Unlock()
	if !ghostSurvives {
		t.Fatal("throttle failed: expired entry was swept within the interval")
	}

	// Once the interval has elapsed, the sweep runs again and reclaims the ghost.
	store.mu.Lock()
	store.lastCleanup = time.Now().Add(-2 * sessionAffinityTemporaryAuthCleanupSweepInterval)
	store.mu.Unlock()
	store.rememberLocal(&Auth{ID: "c", Provider: "codex", Status: StatusActive})
	store.mu.Lock()
	_, ghostStillThere := store.auths["ghost"]
	store.mu.Unlock()
	if ghostStillThere {
		t.Fatal("sweep after the interval must reclaim the expired entry")
	}
}
