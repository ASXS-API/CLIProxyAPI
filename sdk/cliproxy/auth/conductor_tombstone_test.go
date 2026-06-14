package auth

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// TestManager_RemovedTombstone_StateMachine verifies the lock-free removal
// tombstone: Remove marks an auth removed, and (re-)Register clears it.
func TestManager_RemovedTombstone_StateMachine(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()

	auth := &Auth{ID: "tomb-auth", Provider: "codex", Status: StatusActive}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("register: %v", err)
	}
	if manager.isAuthRemoved(auth.ID) {
		t.Fatalf("freshly registered auth must not be tombstoned")
	}

	manager.MarkAuthRemoved(auth.ID)
	if !manager.isAuthRemoved(auth.ID) {
		t.Fatalf("MarkAuthRemoved must tombstone the auth")
	}

	// Re-registering the same ID clears the tombstone (legitimate re-add).
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("re-register: %v", err)
	}
	if manager.isAuthRemoved(auth.ID) {
		t.Fatalf("re-register must clear the removal tombstone")
	}

	// Remove tombstones as well (so picks skip it before the async cleanup runs).
	manager.MarkAuthRemoved(auth.ID)
	if !manager.isAuthRemoved(auth.ID) {
		t.Fatalf("expected tombstone set")
	}
	manager.Remove(ctx, auth.ID)
	if !manager.isAuthRemoved(auth.ID) {
		t.Fatalf("Remove must keep/set the tombstone")
	}
}

// TestManager_PickSkipsTombstonedAuth verifies that a tombstoned auth is never
// selected by the pick paths, even while it is still present in the runtime maps
// (the window between a fast DELETE and the async runtime cleanup completing).
func TestManager_PickSkipsTombstonedAuth(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(&replaceAwareExecutor{id: "codex"})
	ctx := context.Background()

	authA := &Auth{ID: "pick-a", Provider: "codex", Status: StatusActive, Metadata: map[string]any{"email": "a@example.com"}}
	if _, err := manager.Register(ctx, authA); err != nil {
		t.Fatalf("register a: %v", err)
	}

	// Before tombstone, the only auth is selectable.
	if got := mustPickMixedID(t, manager, ctx); got != "pick-a" {
		t.Fatalf("expected pick-a before removal, got %q", got)
	}

	// Tombstone it (still in maps). As the only auth, selection must now fail.
	manager.MarkAuthRemoved("pick-a")
	if auth, _, _, err := manager.pickNextMixed(ctx, []string{"codex"}, "", cliproxyexecutor.Options{}, nil); err == nil {
		t.Fatalf("expected no auth available after tombstoning the only auth, got %#v", auth)
	}

	// A second live auth is still selectable while pick-a stays tombstoned.
	authB := &Auth{ID: "pick-b", Provider: "codex", Status: StatusActive, Metadata: map[string]any{"email": "b@example.com"}}
	if _, err := manager.Register(ctx, authB); err != nil {
		t.Fatalf("register b: %v", err)
	}
	for i := 0; i < 20; i++ {
		if got := mustPickMixedID(t, manager, ctx); got == "pick-a" {
			t.Fatalf("tombstoned pick-a must never be selected, got it on iteration %d", i)
		}
	}

	// Re-registering pick-a clears the tombstone so it becomes selectable again.
	if _, err := manager.Register(ctx, authA); err != nil {
		t.Fatalf("re-register a: %v", err)
	}
	if manager.isAuthRemoved("pick-a") {
		t.Fatalf("re-register must clear the removal tombstone")
	}
}

func mustPickMixedID(t *testing.T, m *Manager, ctx context.Context) string {
	t.Helper()
	auth, _, _, err := m.pickNextMixed(ctx, []string{"codex"}, "", cliproxyexecutor.Options{}, nil)
	if err != nil {
		t.Fatalf("pickNextMixed: %v", err)
	}
	if auth == nil {
		t.Fatalf("pickNextMixed returned nil auth")
	}
	return auth.ID
}
