//go:build !windows

package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sshbrowse/internal/profile"
	"sshbrowse/internal/session"
	"sshbrowse/internal/sessionlog"
)

type retryTestRecorder struct {
	mu          sync.Mutex
	path        string
	closeErrors []error
	closeCalls  int
	done        chan struct{}
	completed   bool
}

func (r *retryTestRecorder) Write([]byte) error { return nil }

func (r *retryTestRecorder) Close(string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var err error
	if r.closeCalls < len(r.closeErrors) {
		err = r.closeErrors[r.closeCalls]
	}
	r.closeCalls++
	if !errors.Is(err, sessionlog.ErrCloseTimeout) && !r.completed {
		if r.done == nil {
			r.done = make(chan struct{})
		}
		close(r.done)
		r.completed = true
	}
	return err
}

func (r *retryTestRecorder) Path() string { return r.path }

func (r *retryTestRecorder) Done() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done == nil {
		r.done = make(chan struct{})
	}
	return r.done
}

func TestFailedSessionStartRetainsAutomaticLog(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		name := "process launch failure"
		if shutdown {
			name = "shutdown after recording setup"
		}
		t.Run(name, func(t *testing.T) {
			store, directory := testLoggingStore(t)
			sessions := NewSessions(nil, store)
			var finalState SessionLoggingState
			sessions.loggingStateEmitter = func(state SessionLoggingState) {
				finalState = state
				if shutdown && state.Active {
					if err := sessions.ServiceShutdown(); err != nil {
						t.Fatal(err)
					}
				}
			}
			err := sessions.start(1, session.Options{
				Argv: []string{filepath.Join(directory, "missing-program")},
				Cols: 80,
				Rows: 24,
			}, "test", "ssh", true)
			if err == nil || shutdown && !strings.Contains(err.Error(), "shutting down") {
				t.Fatalf("failed start returned %v", err)
			}
			state, err := sessions.GetLoggingState(1)
			if err != nil {
				t.Fatalf("failed start lost its logging state: %v", err)
			}
			if state != finalState || state.Active || state.Path == "" {
				t.Fatalf("retained state = %+v, final event = %+v", state, finalState)
			}
			path, err := sessions.recordingPathForID(1)
			if err != nil || path != state.Path {
				t.Fatalf("Show log target = %q, error = %v", path, err)
			}
			content, err := os.ReadFile(path)
			if err != nil || !strings.Contains(string(content), "[Recording ended: session") {
				t.Fatalf("failed-start log was not finalized: %v", err)
			}
			if sessions.count() != 0 || countPendingLoggingOwners(sessions) != 0 {
				t.Fatal("failed start still owns a session slot")
			}
			waitForSetupSlotCount(t, sessions, 0)
			if err := sessions.Close(1); err != nil {
				t.Fatalf("close failed-start pane: %v", err)
			}
			if err := sessions.ReleaseLoggingState(1); err != nil {
				t.Fatal(err)
			}
			if _, err := sessions.recordingPathForID(1); err == nil {
				t.Fatal("released failed-start pane kept its log path")
			}
		})
	}
}

func TestReleaseDuringFailedStartPreventsHistoryRetention(t *testing.T) {
	store, directory := testLoggingStore(t)
	sessions := NewSessions(nil, store)
	recorder := &blockedCloseRecorder{
		closeStarted: make(chan struct{}),
		releaseClose: make(chan struct{}),
	}
	sessions.makeRecorder = func(sessionlog.Options) (sessionRecorder, error) {
		return recorder, nil
	}
	startResult := make(chan error, 1)
	go func() {
		startResult <- sessions.start(2, session.Options{
			Argv: []string{filepath.Join(directory, "missing-program")},
			Cols: 80,
			Rows: 24,
		}, "test", "ssh", true)
	}()
	select {
	case <-recorder.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("failed start did not begin recording finalization")
	}
	if err := sessions.ReleaseLoggingState(2); err != nil {
		t.Fatal(err)
	}
	close(recorder.releaseClose)
	select {
	case err := <-startResult:
		if !errors.Is(err, sessionlog.ErrCloseTimeout) {
			t.Fatalf("failed start omitted its finalization error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("failed start did not return after finalization")
	}
	if _, err := sessions.GetLoggingState(2); err == nil {
		t.Fatal("failed start retained state after the pane released it")
	}
	waitForSetupSlotCount(t, sessions, 0)
}

func TestAutomaticLoggingFailureDoesNotPreventSessionStart(t *testing.T) {
	directory := t.TempDir()
	invalidDirectory := filepath.Join(directory, "file")
	if err := os.WriteFile(invalidDirectory, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := profile.NewStore(filepath.Join(directory, "connections.json"))
	if err := store.SaveLoggingSettings(profile.SessionLoggingSettings{
		Directory:          invalidDirectory,
		MaxFileSizeMB:      10,
		MaxRecordingSizeMB: 100,
	}); err != nil {
		t.Fatal(err)
	}
	sessions := NewSessions(nil, store)
	err := sessions.start(1, session.Options{
		Argv: []string{"/bin/sh", "-c", "sleep 30"},
		Cols: 80,
		Rows: 24,
	}, "test", "ssh", true)
	if err != nil {
		t.Fatalf("logging failure stopped session startup: %v", err)
	}
	state, err := sessions.GetLoggingState(1)
	if err != nil {
		t.Fatal(err)
	}
	if state.Active || !strings.Contains(state.Error, "session log directory") {
		t.Fatalf("logging state after recorder creation failure = %+v", state)
	}
	if err := sessions.Close(1); err != nil {
		t.Fatalf("close session after logging failure: %v", err)
	}
}

func TestAutomaticRecordingFinalizesWhenProcessExitsImmediately(t *testing.T) {
	directory := t.TempDir()
	store := profile.NewStore(filepath.Join(directory, "connections.json"))
	if err := store.SaveLoggingSettings(profile.SessionLoggingSettings{
		Directory:          directory,
		MaxFileSizeMB:      10,
		MaxRecordingSizeMB: 100,
	}); err != nil {
		t.Fatal(err)
	}
	sessions := NewSessions(nil, store)
	if err := sessions.start(1, session.Options{
		Argv: []string{"/bin/sh", "-c", "printf 'automatic-start-output'; exit 0"},
		Cols: 80,
		Rows: 24,
	}, "test", "ssh", true); err != nil {
		t.Fatalf("start immediately exiting session: %v", err)
	}
	ackDeadline := time.Now().Add(time.Second)
	for {
		if err := sessions.AcknowledgeOutput(1, 1); err == nil {
			break
		}
		if time.Now().After(ackDeadline) {
			t.Fatal("first terminal output did not reach acknowledgement")
		}
		time.Sleep(time.Millisecond)
	}
	deadline := time.Now().Add(3 * time.Second)
	for sessions.count() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if count := sessions.count(); count != 0 {
		t.Fatalf("immediately exited process remains in session map: %d", count)
	}
	state, err := sessions.GetLoggingState(1)
	if err != nil {
		t.Fatalf("get finalized state after immediate exit: %v", err)
	}
	if state.Active || state.Path == "" || state.Error != "" {
		t.Fatalf("final logging state = %+v", state)
	}
	logs, err := filepath.Glob(filepath.Join(directory, "*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("recording files = %v, want one finalized transcript", logs)
	}
	info, err := os.Stat(logs[0])
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Fatal("finalized transcript is empty")
	}
	content, err := os.ReadFile(logs[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "automatic-start-output") || !strings.Contains(string(content), "[Recording ended: process exited at ") {
		t.Fatalf("transcript does not contain the process exit footer: %q", content)
	}

	if err := sessions.start(1, session.Options{
		Argv: []string{"/bin/sh", "-c", "sleep 30"},
		Cols: 80,
		Rows: 24,
	}, "test", "local", false); err != nil {
		t.Fatalf("reuse session id: %v", err)
	}
	state, err = sessions.GetLoggingState(1)
	if err != nil {
		t.Fatal(err)
	}
	if state.Path != "" || state.Error != "" || state.Active {
		t.Fatalf("new session received stale logging state: %+v", state)
	}
	if err := sessions.Close(1); err != nil {
		t.Fatalf("close reused session: %v", err)
	}
}

func TestTimedOutRecorderIsRetainedAndMustCloseBeforeRestart(t *testing.T) {
	directory := t.TempDir()
	store := profile.NewStore(filepath.Join(directory, "connections.json"))
	sessions := NewSessions(nil, store)
	first := &retryTestRecorder{
		path:        filepath.Join(directory, "first.txt"),
		closeErrors: []error{sessionlog.ErrCloseTimeout, sessionlog.ErrCloseTimeout, nil},
	}
	second := &retryTestRecorder{path: filepath.Join(directory, "second.txt")}
	created := 0
	sessions.makeRecorder = func(sessionlog.Options) (sessionRecorder, error) {
		created++
		if created == 1 {
			return first, nil
		}
		return second, nil
	}
	if err := sessions.start(1, session.Options{
		Argv: []string{"/bin/sh", "-c", "sleep 30"},
		Cols: 80,
		Rows: 24,
	}, "test", "local", false); err != nil {
		t.Fatal(err)
	}
	if state, err := sessions.StartLogging(1); err != nil || !state.Active {
		t.Fatalf("start first recording: state=%+v error=%v", state, err)
	}
	if state, err := sessions.StopLogging(1); !errors.Is(err, sessionlog.ErrCloseTimeout) || state.Active {
		t.Fatalf("first stop: state=%+v error=%v", state, err)
	}
	managed, err := sessions.lookup(1)
	if err != nil {
		t.Fatal(err)
	}
	managed.logging.mu.Lock()
	retained := managed.logging.recorder == first
	managed.logging.mu.Unlock()
	if !retained {
		t.Fatal("timed-out recorder handle was discarded")
	}
	if state, err := sessions.StartLogging(1); !errors.Is(err, sessionlog.ErrCloseTimeout) || state.Active {
		t.Fatalf("restart with an unfinalized recorder: state=%+v error=%v", state, err)
	}
	if created != 1 {
		t.Fatalf("created %d additional recorders before prior close completed", created-1)
	}
	if state, err := sessions.StartLogging(1); err != nil || !state.Active {
		t.Fatalf("restart after close retry: state=%+v error=%v", state, err)
	}
	if created != 2 {
		t.Fatalf("recorder factory calls = %d, want 2", created)
	}
	if _, err := sessions.StopLogging(1); err != nil {
		t.Fatalf("stop replacement recorder: %v", err)
	}
	if err := sessions.Close(1); err != nil {
		t.Fatalf("close shell session: %v", err)
	}
}

func TestSessionCloseAndExitFinalizeRecorderOnce(t *testing.T) {
	sessions := NewSessions(nil, nil)
	if err := sessions.start(1, session.Options{
		Argv: []string{"/bin/sh", "-c", "sleep 30"},
		Cols: 80,
		Rows: 24,
	}, "test", "local", false); err != nil {
		t.Fatal(err)
	}
	recorder := &blockedCloseRecorder{
		closeStarted: make(chan struct{}),
		releaseClose: make(chan struct{}),
	}
	managed, err := sessions.lookup(1)
	if err != nil {
		t.Fatal(err)
	}
	managed.logging.mu.Lock()
	managed.logging.recorder = recorder
	managed.logging.state.Active = true
	managed.logging.mu.Unlock()

	closeResult := make(chan error, 1)
	go func() { closeResult <- sessions.Close(1) }()
	select {
	case <-recorder.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("session close did not reach recorder finalization")
	}
	close(recorder.releaseClose)
	select {
	case err := <-closeResult:
		if !errors.Is(err, sessionlog.ErrCloseTimeout) {
			t.Fatalf("session close error = %v, want cached recorder timeout", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("session close did not return after recorder finalization")
	}
	state, err := sessions.GetLoggingState(1)
	if err != nil {
		t.Fatal(err)
	}
	if state.Active || state.Error == "" {
		t.Fatalf("terminal logging state = %+v", state)
	}
	recorder.mu.Lock()
	closeCalls := recorder.closeCalls
	recorder.mu.Unlock()
	if closeCalls != 1 {
		t.Fatalf("recorder close calls = %d, want 1", closeCalls)
	}
}

func TestResolvedCloseTimeoutClearsOnlyItsOwnError(t *testing.T) {
	tests := []struct {
		name           string
		initialError   string
		wantFinalError string
	}{
		{name: "resolved timeout"},
		{name: "real write error survives", initialError: "session recording stopped after a write error", wantFinalError: "session recording stopped after a write error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			managed := &managedSession{logging: testLoggingController(
				SessionLoggingState{ID: 15, Active: true, Error: test.initialError},
				&retryTestRecorder{closeErrors: []error{sessionlog.ErrCloseTimeout, nil}},
			)}
			state, err := managed.logging.Stop("stopped by user")
			if !errors.Is(err, sessionlog.ErrCloseTimeout) || state.Error != test.initialError && state.Error != sessionlog.ErrCloseTimeout.Error() {
				t.Fatalf("timed out close state=%+v error=%v", state, err)
			}
			state, err = managed.logging.Stop("stopped by user")
			if err != nil {
				t.Fatalf("retry finalization: %v", err)
			}
			if state.Error != test.wantFinalError {
				t.Fatalf("error after successful finalization = %q, want %q", state.Error, test.wantFinalError)
			}
		})
	}
}

type deferredCloseRecorder struct {
	path         string
	closeStarted chan struct{}
	done         chan struct{}
	closeOnce    sync.Once
}

func (*deferredCloseRecorder) Write([]byte) error { return nil }

func (r *deferredCloseRecorder) Close(string) error {
	r.closeOnce.Do(func() { close(r.closeStarted) })
	return sessionlog.ErrCloseTimeout
}

func (r *deferredCloseRecorder) Path() string          { return r.path }
func (r *deferredCloseRecorder) Done() <-chan struct{} { return r.done }

func testLoggingStore(t *testing.T) (*profile.Store, string) {
	t.Helper()
	directory := t.TempDir()
	store := profile.NewStore(filepath.Join(directory, "connections.json"))
	if err := store.SaveLoggingSettings(profile.SessionLoggingSettings{
		Directory:          directory,
		MaxFileSizeMB:      1,
		MaxRecordingSizeMB: 1,
	}); err != nil {
		t.Fatal(err)
	}
	return store, directory
}

func countPendingLoggingOwners(sessions *Sessions) int {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	count := 0
	for _, entry := range sessions.loggingOwners {
		if entry.owner != nil {
			count++
		}
	}
	return count
}

func waitForSessionStopped(t *testing.T, sessions *Sessions) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for sessions.count() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if count := sessions.count(); count != 0 {
		t.Fatalf("session count = %d, want 0", count)
	}
}

func waitForSetupCleared(t *testing.T, managed *managedSession) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		managed.logging.mu.Lock()
		pending := managed.logging.setup
		managed.logging.mu.Unlock()
		if pending == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("recording setup remained pending")
}

func waitForSetupSlotCount(t *testing.T, sessions *Sessions, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(sessions.recordingSetupSlots) == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("setup slot count = %d, want %d", len(sessions.recordingSetupSlots), want)
}

func TestBlockedSettingsReadTimesOutAndLateSetupStaysBounded(t *testing.T) {
	store, directory := testLoggingStore(t)
	sessions := NewSessions(nil, store)
	sessions.recordingSetupTimeout = 40 * time.Millisecond
	recorder := &deferredCloseRecorder{
		path:         filepath.Join(directory, "late-settings.txt"),
		closeStarted: make(chan struct{}),
		done:         make(chan struct{}),
	}
	factoryStarted := make(chan struct{}, 1)
	sessions.makeRecorder = func(sessionlog.Options) (sessionRecorder, error) {
		factoryStarted <- struct{}{}
		return recorder, nil
	}
	if err := sessions.start(19, session.Options{
		Argv: []string{"/bin/sh", "-c", "sleep 30"},
		Cols: 80,
		Rows: 24,
	}, "test", "local", false); err != nil {
		t.Fatalf("start session: %v", err)
	}
	managed, err := sessions.lookup(19)
	if err != nil {
		t.Fatal(err)
	}
	settingsEntered := make(chan struct{})
	releaseSettings := make(chan struct{})
	loadSettings := func() (LoggingSettings, error) {
		close(settingsEntered)
		<-releaseSettings
		return LoggingSettings{
			Directory:          directory,
			MaxFileSizeMB:      1,
			MaxRecordingSizeMB: 1,
		}, nil
	}
	type setupResult struct {
		state SessionLoggingState
		err   error
	}
	result := make(chan setupResult, 1)
	go func() {
		state, err := managed.logging.Start(loadSettings)
		result <- setupResult{state: state, err: err}
	}()
	select {
	case <-settingsEntered:
	case <-time.After(time.Second):
		t.Fatal("settings loader did not start")
	}
	select {
	case got := <-result:
		if got.err == nil || !strings.Contains(got.err.Error(), "timed out") {
			t.Fatalf("blocked settings result = state %+v, error %v", got.state, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("settings timeout did not return while the loader was blocked")
	}
	select {
	case <-factoryStarted:
		t.Fatal("recorder factory ran before settings returned")
	default:
	}
	if len(sessions.recordingSetupSlots) != 1 {
		t.Fatalf("setup slots after settings timeout = %d, want 1", len(sessions.recordingSetupSlots))
	}
	if _, err := managed.logging.Start(loadSettings); err == nil || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("retry while settings loader remained blocked: %v", err)
	}

	closeResult := make(chan error, 1)
	go func() { closeResult <- sessions.Close(19) }()
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatalf("close session with blocked settings read: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminal close waited for the blocked settings loader")
	}
	waitForSessionStopped(t, sessions)

	close(releaseSettings)
	select {
	case <-factoryStarted:
	case <-time.After(time.Second):
		t.Fatal("recorder factory did not start after settings returned")
	}
	select {
	case <-recorder.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("canceled late recorder was not closed")
	}
	if len(sessions.recordingSetupSlots) != 1 {
		t.Fatalf("setup slots after late close timeout = %d, want 1 until writer completion", len(sessions.recordingSetupSlots))
	}
	managed.logging.mu.Lock()
	setupStillPending := managed.logging.setup != nil
	managed.logging.mu.Unlock()
	if !setupStillPending {
		t.Fatal("setup token cleared while late writer was still pending")
	}
	close(recorder.done)
	waitForSetupCleared(t, managed)
	waitForSetupSlotCount(t, sessions, 0)
}

func TestAutomaticSetupTimeoutAllowsTerminalAndLateRetry(t *testing.T) {
	store, directory := testLoggingStore(t)
	sessions := NewSessions(nil, store)
	sessions.recordingSetupTimeout = 25 * time.Millisecond
	newEntered := make(chan struct{})
	releaseNew := make(chan struct{})
	late := &deferredCloseRecorder{
		path:         filepath.Join(directory, "late.txt"),
		closeStarted: make(chan struct{}),
		done:         make(chan struct{}),
	}
	retry := &retryTestRecorder{path: filepath.Join(directory, "retry.txt")}
	var factoryMu sync.Mutex
	factoryCalls := 0
	sessions.makeRecorder = func(sessionlog.Options) (sessionRecorder, error) {
		factoryMu.Lock()
		factoryCalls++
		call := factoryCalls
		factoryMu.Unlock()
		if call == 1 {
			close(newEntered)
			<-releaseNew
			return late, nil
		}
		return retry, nil
	}

	started := time.Now()
	if err := sessions.start(1, session.Options{
		Argv: []string{"/bin/sh", "-c", "sleep 30"},
		Cols: 80,
		Rows: 24,
	}, "test", "ssh", true); err != nil {
		t.Fatalf("automatic setup timeout prevented terminal start: %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("terminal start waited %s for recorder setup", elapsed)
	}
	select {
	case <-newEntered:
	case <-time.After(time.Second):
		t.Fatal("recorder factory did not start")
	}
	if sessions.count() != 1 {
		t.Fatalf("session count = %d, want 1 after setup timeout", sessions.count())
	}
	if len(sessions.recordingSetupSlots) != 1 {
		t.Fatalf("setup slots held by blocked factory = %d, want 1", len(sessions.recordingSetupSlots))
	}
	state, err := sessions.GetLoggingState(1)
	if err != nil || !strings.Contains(state.Error, "timed out") {
		t.Fatalf("state after automatic setup timeout = %+v, error=%v", state, err)
	}
	if _, err := sessions.StartLogging(1); err == nil || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("retry while first factory was pending: %v", err)
	}
	factoryMu.Lock()
	callsWhilePending := factoryCalls
	factoryMu.Unlock()
	if callsWhilePending != 1 {
		t.Fatalf("recorder factory calls while setup pending = %d, want 1", callsWhilePending)
	}

	close(releaseNew)
	select {
	case <-late.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("late recorder was not closed after timeout")
	}
	if len(sessions.recordingSetupSlots) != 1 {
		t.Fatalf("setup slots after close timeout = %d, want 1 until writer completion", len(sessions.recordingSetupSlots))
	}
	if _, err := sessions.StartLogging(1); err == nil || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("retry before late writer completion: %v", err)
	}
	close(late.done)
	managed, err := sessions.lookup(1)
	if err != nil {
		t.Fatal(err)
	}
	waitForSetupCleared(t, managed)
	waitForSetupSlotCount(t, sessions, 0)
	state, err = sessions.StartLogging(1)
	if err != nil || !state.Active {
		t.Fatalf("retry after late writer completion: state=%+v error=%v", state, err)
	}
	factoryMu.Lock()
	callsAfterRetry := factoryCalls
	factoryMu.Unlock()
	if callsAfterRetry != 2 {
		t.Fatalf("recorder factory calls after retry = %d, want 2", callsAfterRetry)
	}
	if err := sessions.Close(1); err != nil {
		t.Fatalf("close session after successful retry: %v", err)
	}
}

func TestAcceptedRecorderKeepsSetupSlotUntilWriterCompletion(t *testing.T) {
	store, directory := testLoggingStore(t)
	sessions := NewSessions(nil, store)
	recorder := &deferredCloseRecorder{
		path:         filepath.Join(directory, "stalled-finalization.txt"),
		closeStarted: make(chan struct{}),
		done:         make(chan struct{}),
	}
	sessions.makeRecorder = func(sessionlog.Options) (sessionRecorder, error) {
		return recorder, nil
	}
	if err := sessions.start(18, session.Options{
		Argv: []string{"/bin/sh", "-c", "sleep 30"},
		Cols: 80,
		Rows: 24,
	}, "test", "ssh", false); err != nil {
		t.Fatalf("start session: %v", err)
	}
	if state, err := sessions.StartLogging(18); err != nil || !state.Active {
		t.Fatalf("start recording: state=%+v error=%v", state, err)
	}
	managed, err := sessions.lookup(18)
	if err != nil {
		t.Fatal(err)
	}
	waitForSetupCleared(t, managed)
	waitForSetupSlotCount(t, sessions, 1)

	err = sessions.Close(18)
	if !errors.Is(err, sessionlog.ErrCloseTimeout) {
		t.Fatalf("close with stalled finalization: %v", err)
	}
	select {
	case <-recorder.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("recorder finalization did not start")
	}
	waitForSessionStopped(t, sessions)
	if err := sessions.ReleaseLoggingState(18); err != nil {
		t.Fatalf("release retained pane state: %v", err)
	}
	if len(sessions.recordingSetupSlots) != 1 {
		t.Fatalf("setup slots after pane release = %d, want 1 until writer completion", len(sessions.recordingSetupSlots))
	}

	close(recorder.done)
	waitForSetupSlotCount(t, sessions, 0)
}

func TestCloseDoesNotWaitForBlockedRecorderFactory(t *testing.T) {
	store, directory := testLoggingStore(t)
	sessions := NewSessions(nil, store)
	sessions.recordingSetupTimeout = 20 * time.Millisecond
	newEntered := make(chan struct{})
	releaseNew := make(chan struct{})
	late := &retryTestRecorder{path: filepath.Join(directory, "late.txt")}
	sessions.makeRecorder = func(sessionlog.Options) (sessionRecorder, error) {
		close(newEntered)
		<-releaseNew
		return late, nil
	}
	if err := sessions.start(2, session.Options{
		Argv: []string{"/bin/sh", "-c", "sleep 30"},
		Cols: 80,
		Rows: 24,
	}, "test", "ssh", true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-newEntered:
	case <-time.After(time.Second):
		t.Fatal("recorder factory did not start")
	}
	closeResult := make(chan error, 1)
	go func() { closeResult <- sessions.Close(2) }()
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatalf("close after setup timeout: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminal close waited for the blocked recorder factory")
	}
	close(releaseNew)
	waitForSessionStopped(t, sessions)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		late.mu.Lock()
		closed := late.closeCalls
		late.mu.Unlock()
		if closed == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	state, err := sessions.GetLoggingState(2)
	if err != nil || state.Active {
		t.Fatalf("state after terminal close = %+v, error=%v", state, err)
	}
	late.mu.Lock()
	closeCalls := late.closeCalls
	late.mu.Unlock()
	if closeCalls != 1 {
		t.Fatalf("late recorder close calls = %d, want 1", closeCalls)
	}
}

func TestShutdownCancelsPendingRecorderSetup(t *testing.T) {
	store, directory := testLoggingStore(t)
	sessions := NewSessions(nil, store)
	sessions.recordingSetupTimeout = 5 * time.Second
	newEntered := make(chan struct{})
	releaseNew := make(chan struct{})
	late := &retryTestRecorder{path: filepath.Join(directory, "late.txt")}
	sessions.makeRecorder = func(sessionlog.Options) (sessionRecorder, error) {
		close(newEntered)
		<-releaseNew
		return late, nil
	}
	startResult := make(chan error, 1)
	go func() {
		startResult <- sessions.start(3, session.Options{
			Argv: []string{"/bin/sh", "-c", "sleep 30"},
			Cols: 80,
			Rows: 24,
		}, "test", "ssh", true)
	}()
	select {
	case <-newEntered:
	case <-time.After(time.Second):
		t.Fatal("recorder factory did not start")
	}
	if err := sessions.ServiceShutdown(); err != nil {
		t.Fatalf("shutdown with pending setup: %v", err)
	}
	select {
	case err := <-startResult:
		if err == nil || !strings.Contains(err.Error(), "shutting down") {
			t.Fatalf("pending start result = %v, want shutdown error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel pending setup wait")
	}
	if countPendingLoggingOwners(sessions) != 0 {
		t.Fatalf("pending logging owners after failed shutdown start = %d, want 0", countPendingLoggingOwners(sessions))
	}
	close(releaseNew)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		late.mu.Lock()
		closed := late.closeCalls
		late.mu.Unlock()
		if closed == 1 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("late recorder was not closed after shutdown")
}

func TestSessionLimitCountsActiveIDsOnce(t *testing.T) {
	sessions := NewSessions(nil, nil)
	for id := 1; id < maxSessions; id++ {
		managed := &managedSession{logging: testLoggingController(SessionLoggingState{ID: id}, nil)}
		sessions.sessions[id] = managed
		sessions.loggingOwners[id] = &loggingEntry{owner: managed}
	}
	managed := &managedSession{}
	if err := sessions.reserveSessionStart(maxSessions, managed); err != nil {
		t.Fatalf("reserve final available session slot: %v", err)
	}
	if err := sessions.reserveSessionStart(maxSessions+1, &managedSession{}); err == nil || !strings.Contains(err.Error(), "session limit") {
		t.Fatalf("reserve beyond session limit = %v, want limit error", err)
	}
}

func TestRetainedLoggingLimitRequiresPaneRelease(t *testing.T) {
	store, directory := testLoggingStore(t)
	sessions := NewSessions(nil, store)
	sessions.mu.Lock()
	for id := 1; id <= maxLoggingHistory; id++ {
		sessions.loggingOwners[id] = &loggingEntry{retained: retainedLoggingState{
			state: SessionLoggingState{ID: id},
		}}
	}
	sessions.mu.Unlock()

	var factoryMu sync.Mutex
	factoryCalls := 0
	sessions.makeRecorder = func(options sessionlog.Options) (sessionRecorder, error) {
		factoryMu.Lock()
		factoryCalls++
		call := factoryCalls
		factoryMu.Unlock()
		return &retryTestRecorder{path: filepath.Join(directory, fmt.Sprintf("retained-%03d.txt", call))}, nil
	}
	options := session.Options{Argv: []string{"/bin/sh", "-c", "sleep 30"}, Cols: 80, Rows: 24}
	if err := sessions.start(maxLoggingHistory+1, options, "test", "local", true); err == nil || !strings.Contains(err.Error(), "retained session logging limit") {
		t.Fatalf("start beyond retained logging limit = %v, want limit error", err)
	}
	if sessions.count() != 0 {
		t.Fatalf("session count after rejected start = %d, want 0", sessions.count())
	}
	factoryMu.Lock()
	calls := factoryCalls
	factoryMu.Unlock()
	if calls != 0 {
		t.Fatalf("recorder factories after rejected start = %d, want 0", calls)
	}
	if err := sessions.ReleaseLoggingState(1); err != nil {
		t.Fatalf("release retained pane state: %v", err)
	}
	if err := sessions.start(maxLoggingHistory+1, options, "test", "local", true); err != nil {
		t.Fatalf("start after releasing retained pane state: %v", err)
	}
	if sessions.count() != 1 {
		t.Fatalf("session count after released-state retry = %d, want 1", sessions.count())
	}
	factoryMu.Lock()
	calls = factoryCalls
	factoryMu.Unlock()
	if calls != 1 {
		t.Fatalf("recorder factories after accepted retry = %d, want 1", calls)
	}
	if err := sessions.Close(maxLoggingHistory + 1); err != nil {
		t.Fatalf("close retry session: %v", err)
	}
}

func TestRecorderSetupConcurrencyLimitDoesNotStartAnotherFactory(t *testing.T) {
	sessions := NewSessions(nil, nil)
	factoryCalls := 0
	sessions.makeRecorder = func(sessionlog.Options) (sessionRecorder, error) {
		factoryCalls++
		return &retryTestRecorder{path: "/tmp/unexpected.txt"}, nil
	}
	if err := sessions.start(16, session.Options{
		Argv: []string{"/bin/sh", "-c", "sleep 30"},
		Cols: 80,
		Rows: 24,
	}, "test", "local", false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < cap(sessions.recordingSetupSlots); i++ {
		sessions.recordingSetupSlots <- struct{}{}
	}
	if _, err := sessions.StartLogging(16); err == nil || !strings.Contains(err.Error(), "concurrency limit") {
		t.Fatalf("StartLogging with exhausted setup slots = %v, want limit error", err)
	}
	if factoryCalls != 0 {
		t.Fatalf("factory calls at setup limit = %d, want 0", factoryCalls)
	}
	for i := 0; i < cap(sessions.recordingSetupSlots); i++ {
		<-sessions.recordingSetupSlots
	}
	if err := sessions.Close(16); err != nil {
		t.Fatal(err)
	}
}

func TestRecorderFailureCannotEmitBeforeStaleActiveState(t *testing.T) {
	store, directory := testLoggingStore(t)
	sessions := NewSessions(nil, store)
	events := make(chan SessionLoggingState, 2)
	sessions.loggingStateEmitter = func(state SessionLoggingState) { events <- state }
	onErrorReady := make(chan func(error), 1)
	sessions.makeRecorder = func(options sessionlog.Options) (sessionRecorder, error) {
		onErrorReady <- options.OnError
		return &retryTestRecorder{path: filepath.Join(directory, "event-order.txt")}, nil
	}
	if err := sessions.start(17, session.Options{
		Argv: []string{"/bin/sh", "-c", "sleep 30"},
		Cols: 80,
		Rows: 24,
	}, "test", "local", false); err != nil {
		t.Fatal(err)
	}
	managed, err := sessions.lookup(17)
	if err != nil {
		t.Fatal(err)
	}
	managed.logging.eventMu.Lock()
	startResult := make(chan error, 1)
	go func() {
		_, err := sessions.StartLogging(17)
		startResult <- err
	}()
	var onError func(error)
	select {
	case onError = <-onErrorReady:
	case <-time.After(time.Second):
		managed.logging.eventMu.Unlock()
		t.Fatal("recorder setup did not return its error callback")
	}
	managed.logging.mu.Lock()
	pending := managed.logging.setup
	managed.logging.mu.Unlock()
	if pending == nil {
		managed.logging.eventMu.Unlock()
		t.Fatal("recorder setup was not retained while event lock was held")
	}
	select {
	case <-pending.ready:
	case <-time.After(time.Second):
		managed.logging.eventMu.Unlock()
		t.Fatal("recorder setup did not become ready")
	}
	managed.logging.mu.Lock()
	activeBeforeEmit := managed.logging.state.Active
	managed.logging.mu.Unlock()
	if activeBeforeEmit {
		managed.logging.eventMu.Unlock()
		t.Fatal("recorder became active before it could serialize its state event")
	}
	errorDone := make(chan struct{})
	go func() {
		onError(errors.New("recording write failed"))
		close(errorDone)
	}()
	time.Sleep(10 * time.Millisecond)
	managed.logging.eventMu.Unlock()
	select {
	case <-startResult:
	case <-time.After(time.Second):
		t.Fatal("StartLogging did not return after event lock release")
	}
	select {
	case <-errorDone:
	case <-time.After(time.Second):
		t.Fatal("recorder error did not emit after event lock release")
	}
	state, err := sessions.GetLoggingState(17)
	if err != nil || state.Active || state.Error != "recording write failed" {
		t.Fatalf("logging state after concurrent setup error = %+v, error=%v", state, err)
	}
	seenError := false
	for len(events) > 0 {
		event := <-events
		if seenError && event.Active {
			t.Fatalf("stale active state emitted after error: %+v", event)
		}
		if event.Error != "" {
			seenError = true
		}
	}
	if !seenError {
		t.Fatal("no logging error state was emitted")
	}
	if err := sessions.Close(17); err != nil {
		t.Fatal(err)
	}
}

func TestCloseAfterExitReturnsCachedFinalizationResult(t *testing.T) {
	sessions := NewSessions(nil, nil)
	if err := sessions.start(4, session.Options{
		Argv: []string{"/bin/sh", "-c", "exit 0"},
		Cols: 80,
		Rows: 24,
	}, "test", "local", false); err != nil {
		t.Fatal(err)
	}
	waitForSessionStopped(t, sessions)
	if err := sessions.Close(4); err != nil {
		t.Fatalf("close after successful natural exit: %v", err)
	}

	if err := sessions.start(5, session.Options{
		Argv: []string{"/bin/sh", "-c", "sleep 0.05"},
		Cols: 80,
		Rows: 24,
	}, "test", "local", false); err != nil {
		t.Fatal(err)
	}
	managed, err := sessions.lookup(5)
	if err != nil {
		t.Fatal(err)
	}
	finalErr := errors.New("recording finalization failed")
	recorder := &retryTestRecorder{closeErrors: []error{finalErr}}
	managed.logging.mu.Lock()
	managed.logging.recorder = recorder
	managed.logging.state.Active = true
	managed.logging.mu.Unlock()
	waitForSessionStopped(t, sessions)
	if err := sessions.Close(5); !errors.Is(err, finalErr) {
		t.Fatalf("close after natural exit with finalization error = %v, want %v", err, finalErr)
	}
	if err := sessions.Close(5); !errors.Is(err, finalErr) {
		t.Fatalf("close after failed natural exit finalization = %v, want %v", err, finalErr)
	}
	recorder.mu.Lock()
	closeCalls := recorder.closeCalls
	recorder.mu.Unlock()
	if closeCalls != 1 {
		t.Fatalf("natural-exit recorder close calls = %d, want 1", closeCalls)
	}
}

func TestRetainedLogPathSurvivesOtherSessionRecordingRestarts(t *testing.T) {
	store, directory := testLoggingStore(t)
	sessions := NewSessions(nil, store)
	firstPath := filepath.Join(directory, "first.txt")
	if err := os.WriteFile(firstPath, []byte("first transcript"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sessions.start(6, session.Options{
		Argv: []string{"/bin/sh", "-c", "sleep 30"},
		Cols: 80,
		Rows: 24,
	}, "first", "local", false); err != nil {
		t.Fatal(err)
	}
	firstManaged, err := sessions.lookup(6)
	if err != nil {
		t.Fatal(err)
	}
	firstManaged.logging.mu.Lock()
	firstManaged.logging.state.Path = firstPath
	firstManaged.logging.mu.Unlock()
	if err := sessions.Close(6); err != nil {
		t.Fatal(err)
	}

	var recordingNumber int
	sessions.makeRecorder = func(sessionlog.Options) (sessionRecorder, error) {
		recordingNumber++
		return &retryTestRecorder{path: filepath.Join(directory, fmt.Sprintf("other-%03d.txt", recordingNumber))}, nil
	}
	if err := sessions.start(7, session.Options{
		Argv: []string{"/bin/sh", "-c", "sleep 30"},
		Cols: 80,
		Rows: 24,
	}, "second", "local", false); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < maxLoggingHistory+1; attempt++ {
		if _, err := sessions.StartLogging(7); err != nil {
			t.Fatalf("start recording %d: %v", attempt+1, err)
		}
		if _, err := sessions.StopLogging(7); err != nil {
			t.Fatalf("stop recording %d: %v", attempt+1, err)
		}
	}
	resolved, err := sessions.recordingPathForID(6)
	if err != nil || resolved != firstPath {
		t.Fatalf("retained Show Log target after %d other recordings = %q, error=%v", maxLoggingHistory+1, resolved, err)
	}
	if err := sessions.ReleaseLoggingState(6); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.recordingPathForID(6); err == nil {
		t.Fatal("released logging state remained available")
	}
	if err := sessions.Close(7); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseDuringClosePreventsHistoryRecreation(t *testing.T) {
	sessions := NewSessions(nil, nil)
	if err := sessions.start(8, session.Options{
		Argv: []string{"/bin/sh", "-c", "sleep 30"},
		Cols: 80,
		Rows: 24,
	}, "test", "local", false); err != nil {
		t.Fatal(err)
	}
	managed, err := sessions.lookup(8)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &blockedCloseRecorder{
		closeStarted: make(chan struct{}),
		releaseClose: make(chan struct{}),
	}
	managed.logging.mu.Lock()
	managed.logging.recorder = recorder
	managed.logging.state.Active = true
	managed.logging.mu.Unlock()
	closeResult := make(chan error, 1)
	go func() { closeResult <- sessions.Close(8) }()
	select {
	case <-recorder.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("close did not begin recorder finalization")
	}
	if err := sessions.ReleaseLoggingState(8); err != nil {
		t.Fatal(err)
	}
	close(recorder.releaseClose)
	select {
	case <-closeResult:
	case <-time.After(3 * time.Second):
		t.Fatal("close did not finish after recorder release")
	}
	waitForSessionStopped(t, sessions)
	if _, err := sessions.GetLoggingState(8); err == nil {
		t.Fatal("OnExit recreated history after logging state was released")
	}
}
