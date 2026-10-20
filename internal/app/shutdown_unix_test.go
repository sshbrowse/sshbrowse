//go:build darwin || linux

package app

import (
	"os/exec"
	"testing"
	"time"

	"sshbrowse/internal/session"
)

func TestShutdownClosesElevenSessionsWithPendingOutput(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh is required for the local PTY test")
	}
	sessions := NewSessions(nil, nil)
	t.Cleanup(func() { _ = sessions.shutdown() })
	open := make([]*managedSession, 0, 11)
	for id := 0; id < 11; id++ {
		if err := sessions.start(id, session.Options{
			Argv: []string{shell, "-c", `trap '' HUP; printf READY; exec sleep 30`},
			Cols: 80, Rows: 24,
		}, "test", "ssh", false); err != nil {
			t.Fatal(err)
		}
		managed, err := sessions.lookup(id)
		if err != nil {
			t.Fatal(err)
		}
		open = append(open, managed)
		deadline := time.Now().Add(time.Second)
		for {
			managed.delivery.mu.Lock()
			pending := managed.delivery.awaiting != 0
			managed.delivery.mu.Unlock()
			if pending {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("session did not reach its pending output acknowledgement")
			}
			time.Sleep(time.Millisecond)
		}
	}
	guard := NewQuitGuard(nil, sessions)
	guard.confirmed.Store(true)
	quit := make(chan bool, 1)
	guard.quit = func() { quit <- guard.ShouldQuit() }
	if guard.ShouldQuit() {
		t.Fatal("quit allowed before session cleanup")
	}
	select {
	case allowed := <-quit:
		if !allowed {
			t.Fatal("native quit retry rejected after cleanup")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("session cleanup did not finish within one shared grace period")
	}
	if err := sessions.shutdown(); err != nil {
		t.Fatal(err)
	}
	if count := sessions.count(); count != 0 {
		t.Fatalf("%d sessions remain after shutdown", count)
	}
	for _, managed := range open {
		if err := managed.running.Close(); err != nil {
			t.Fatalf("close completed session: %v", err)
		}
		if err := managed.running.Write([]byte("test")); err == nil {
			t.Fatal("shutdown left a session PTY open")
		}
	}
}
