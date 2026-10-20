package app

import (
	"errors"
	"sync"
	"testing"
	"time"

	"sshbrowse/internal/sessionlog"
)

func TestQuitWaitsForCleanupWithoutBlockingCaller(t *testing.T) {
	sessions := NewSessions(nil, nil)
	guard := NewQuitGuard(nil, sessions)
	guard.confirmed.Store(true)
	quit := make(chan bool, 1)
	guard.quit = func() { quit <- guard.ShouldQuit() }
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	recorder := &blockedCloseRecorder{closeStarted: make(chan struct{}), releaseClose: release}
	sessions.sessions[1] = &managedSession{
		delivery: newOutputDelivery(time.Second, nil),
		logging:  testLoggingController(SessionLoggingState{ID: 1, Active: true}, recorder),
	}
	returned := make(chan bool, 1)
	go func() {
		allowed := guard.ShouldQuit()
		_ = sessions.ServiceShutdown() // The native hook also must return during cleanup.
		returned <- allowed
	}()
	select {
	case allowed := <-returned:
		if allowed {
			t.Fatal("quit allowed before cleanup")
		}
	case <-time.After(time.Second):
		t.Fatal("quit guard or service hook blocked its caller")
	}
	select {
	case <-recorder.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not reach the blocked recorder")
	}
	if guard.ShouldQuit() {
		t.Fatal("repeated quit allowed before cleanup")
	}
	select {
	case <-quit:
		t.Fatal("native quit retried before cleanup")
	default:
	}
	unblock()
	select {
	case allowed := <-quit:
		if !allowed {
			t.Fatal("native quit retry rejected after cleanup")
		}
	case <-time.After(time.Second):
		t.Fatal("native quit was not retried")
	}
	if err := sessions.shutdown(); !errors.Is(err, sessionlog.ErrCloseTimeout) {
		t.Fatalf("cleanup error = %v, want recorder timeout", err)
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.closeCalls != 1 {
		t.Fatalf("recorder closed %d times, want 1", recorder.closeCalls)
	}
}
