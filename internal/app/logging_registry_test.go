package app

import "testing"

func TestStaleFinalizationDoesNotRetireReusedSessionID(t *testing.T) {
	sessions := NewSessions(nil, nil)
	old := &managedSession{logging: testLoggingController(
		SessionLoggingState{ID: 42, Path: "/tmp/old-session.txt"}, nil,
	)}
	if err := sessions.reserveSessionStart(42, old); err != nil {
		t.Fatalf("reserve original session ID: %v", err)
	}
	if err := sessions.finalizeSession(old, "session exited"); err != nil {
		t.Fatalf("finalize original session: %v", err)
	}

	newOwner := &managedSession{logging: testLoggingController(
		SessionLoggingState{ID: 42, Path: "/tmp/new-session.txt"}, nil,
	)}
	if err := sessions.reserveSessionStart(42, newOwner); err != nil {
		t.Fatalf("reuse retained session ID: %v", err)
	}

	// A delayed duplicate finalization from the previous owner must not replace
	// the new reservation with its cached result.
	if err := sessions.finalizeSession(old, "late process exit"); err != nil {
		t.Fatalf("repeat original session finalization: %v", err)
	}
	sessions.mu.Lock()
	entry := sessions.loggingOwners[42]
	sessions.mu.Unlock()
	if entry == nil || entry.owner != newOwner {
		t.Fatalf("session ID owner after stale finalization = %+v, want new owner", entry)
	}

	if err := sessions.finalizeSession(newOwner, "session exited"); err != nil {
		t.Fatalf("finalize reused session: %v", err)
	}
	state, err := sessions.GetLoggingState(42)
	if err != nil {
		t.Fatalf("get reused session logging state: %v", err)
	}
	if state.Path != "/tmp/new-session.txt" {
		t.Fatalf("retained logging state after reuse = %+v, want new session path", state)
	}
}
