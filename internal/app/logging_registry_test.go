package app

import "testing"

func TestStaleFinalizationDoesNotDeleteReusedSessionID(t *testing.T) {
	sessions := NewSessions(nil, nil)
	old := &managedSession{logging: testLoggingController(
		SessionLoggingState{ID: 42}, nil,
	)}
	if err := sessions.reserveSessionStart(42, old); err != nil {
		t.Fatalf("reserve original session ID: %v", err)
	}
	if err := sessions.finalizeSession(old, "session failed to start"); err != nil {
		t.Fatalf("finalize original session: %v", err)
	}

	newOwner := &managedSession{logging: testLoggingController(
		SessionLoggingState{ID: 42}, nil,
	)}
	if err := sessions.reserveSessionStart(42, newOwner); err != nil {
		t.Fatalf("reuse finalized session ID: %v", err)
	}

	// A delayed duplicate finalization from the old owner must not remove the
	// new startup reservation.
	if err := sessions.finalizeSession(old, "late process exit"); err != nil {
		t.Fatalf("repeat original session finalization: %v", err)
	}
	sessions.mu.Lock()
	starting := sessions.starting[42]
	sessions.mu.Unlock()
	if starting != newOwner {
		t.Fatalf("session ID owner after stale finalization = %p, want %p", starting, newOwner)
	}
	if err := sessions.finalizeSession(newOwner, "session failed to start"); err != nil {
		t.Fatalf("finalize new reservation: %v", err)
	}
}
