//go:build !windows

package app

import (
	"errors"
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

func TestFailedSessionStartFinalizesAutomaticLog(t *testing.T) {
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
			if finalState.Active || finalState.Path == "" {
				t.Fatalf("final logging event = %+v", finalState)
			}
			content, err := os.ReadFile(finalState.Path)
			if err != nil || !strings.Contains(string(content), "[Recording ended: session") {
				t.Fatalf("failed-start log was not finalized: %v", err)
			}
			if sessions.count() != 0 || countStartingSessions(sessions) != 0 {
				t.Fatal("failed start still owns a session slot")
			}
			waitForSetupSlotCount(t, sessions, 0)
			if err := sessions.Close(1); err != nil {
				t.Fatalf("close after failed start: %v", err)
			}
		})
	}
}

func TestAutomaticLoggingFailureDoesNotPreventSessionStart(t *testing.T) {
	directory := t.TempDir()
	invalidDirectory := filepath.Join(directory, "file")
	if err := os.WriteFile(invalidDirectory, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := profile.NewStore(filepath.Join(directory, "connections.json"))
	if err := store.SaveLoggingSettings(profile.SessionLoggingSettings{
		Directory: invalidDirectory,
	}); err != nil {
		t.Fatal(err)
	}
	sessions := NewSessions(nil, store)
	err := startLoggingTestShell(sessions, 1, "ssh", true)
	if err != nil {
		t.Fatalf("logging failure stopped session startup: %v", err)
	}
	state := liveLoggingState(t, sessions, 1)
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
		Directory: directory,
	}); err != nil {
		t.Fatal(err)
	}
	sessions := NewSessions(nil, store)
	finalStates := make(chan SessionLoggingState, 4)
	sessions.loggingStateEmitter = func(state SessionLoggingState) { finalStates <- state }
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
	if _, err := sessions.lookup(1); err == nil {
		t.Fatal("immediately exited process remains in the active session map")
	}
	var state SessionLoggingState
	for len(finalStates) > 0 {
		state = <-finalStates
	}
	if state.Active || state.Path == "" || state.Error != "" {
		t.Fatalf("final logging state = %+v", state)
	}
	logs, err := filepath.Glob(filepath.Join(directory, "*", "*.txt"))
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

	if err := startLoggingTestShell(sessions, 1, "local", false); err != nil {
		t.Fatalf("reuse session id: %v", err)
	}
	state = liveLoggingState(t, sessions, 1)
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
	if err := startLoggingTestShell(sessions, 1, "local", false); err != nil {
		t.Fatal(err)
	}
	if state, err := sessions.StartLogging(1, 0); err != nil || !state.Active {
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
	if state, err := sessions.StartLogging(1, 0); !errors.Is(err, sessionlog.ErrCloseTimeout) || state.Active {
		t.Fatalf("restart with an unfinalized recorder: state=%+v error=%v", state, err)
	}
	if created != 1 {
		t.Fatalf("created %d additional recorders before prior close completed", created-1)
	}
	if state, err := sessions.StartLogging(1, 0); err != nil || !state.Active {
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

func TestRestartReportsPreviousRecorderFinalizationErrorBeforeRetry(t *testing.T) {
	directory := t.TempDir()
	sessions := NewSessions(nil, profile.NewStore(filepath.Join(directory, "connections.json")))
	finalizationErr := errors.New("write session transcript footer: disk I/O error")
	first := &retryTestRecorder{
		path:        filepath.Join(directory, "first.txt"),
		closeErrors: []error{sessionlog.ErrCloseTimeout, finalizationErr},
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
	if err := startLoggingTestShell(sessions, 1, "local", false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sessions.Close(1); err != nil {
			t.Errorf("close shell session: %v", err)
		}
	})
	if _, err := sessions.StartLogging(1, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.StopLogging(1); !errors.Is(err, sessionlog.ErrCloseTimeout) {
		t.Fatalf("first stop: %v", err)
	}

	var published SessionLoggingState
	sessions.loggingStateEmitter = func(state SessionLoggingState) { published = state }
	state, err := sessions.StartLogging(1, 0)
	if !errors.Is(err, finalizationErr) || state.Active || state.Error != finalizationErr.Error() || state.Path != first.path {
		t.Fatalf("restart hid previous finalization failure: state=%+v error=%v", state, err)
	}
	if published != state {
		t.Fatalf("published state=%+v, want %+v", published, state)
	}
	if created != 1 {
		t.Fatalf("created %d recorders before reporting the previous failure", created)
	}
	if state, err := sessions.StartLogging(1, 0); err != nil || !state.Active || state.Error != "" || state.Path != second.path {
		t.Fatalf("retry after reported failure: state=%+v error=%v", state, err)
	}
	if created != 2 || first.closeCalls != 2 {
		t.Fatalf("recorder creations=%d previous close calls=%d, want 2 each", created, first.closeCalls)
	}
}

func TestSessionCloseAndExitFinalizeRecorderOnce(t *testing.T) {
	sessions := NewSessions(nil, nil)
	if err := startLoggingTestShell(sessions, 1, "local", false); err != nil {
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
	state := managed.logging.State()
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

func startLoggingTestShell(sessions *Sessions, id int, kind string, automatic bool) error {
	return sessions.start(id, session.Options{
		Argv: []string{"/bin/sh", "-c", "sleep 30"},
		Cols: 80,
		Rows: 24,
	}, "test", kind, automatic)
}

func testLoggingStore(t *testing.T) (*profile.Store, string) {
	t.Helper()
	directory := t.TempDir()
	store := profile.NewStore(filepath.Join(directory, "connections.json"))
	if err := store.SaveLoggingSettings(profile.SessionLoggingSettings{
		Directory: directory,
	}); err != nil {
		t.Fatal(err)
	}
	return store, directory
}

func countStartingSessions(sessions *Sessions) int {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	return len(sessions.starting)
}

func liveLoggingState(t *testing.T, sessions *Sessions, id int) SessionLoggingState {
	t.Helper()
	managed, err := sessions.lookup(id)
	if err != nil {
		t.Fatal(err)
	}
	return managed.logging.State()
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
	if err := startLoggingTestShell(sessions, 19, "local", false); err != nil {
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
			Directory: directory,
		}, nil
	}
	type setupResult struct {
		state SessionLoggingState
		err   error
	}
	result := make(chan setupResult, 1)
	go func() {
		state, err := managed.logging.Start(loadSettings, 0)
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
	if _, err := managed.logging.Start(loadSettings, 0); err == nil || !strings.Contains(err.Error(), "pending") {
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
	if err := startLoggingTestShell(sessions, 1, "ssh", true); err != nil {
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
	state := liveLoggingState(t, sessions, 1)
	if !strings.Contains(state.Error, "timed out") {
		t.Fatalf("state after automatic setup timeout = %+v", state)
	}
	if _, err := sessions.StartLogging(1, 0); err == nil || !strings.Contains(err.Error(), "pending") {
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
	if _, err := sessions.StartLogging(1, 0); err == nil || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("retry before late writer completion: %v", err)
	}
	close(late.done)
	managed, err := sessions.lookup(1)
	if err != nil {
		t.Fatal(err)
	}
	waitForSetupCleared(t, managed)
	waitForSetupSlotCount(t, sessions, 0)
	state, err = sessions.StartLogging(1, 0)
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
	if err := startLoggingTestShell(sessions, 18, "ssh", false); err != nil {
		t.Fatalf("start session: %v", err)
	}
	if state, err := sessions.StartLogging(18, 0); err != nil || !state.Active {
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
	if err := sessions.Close(18); err != nil {
		t.Fatalf("close after session exit: %v", err)
	}
	if len(sessions.recordingSetupSlots) != 1 {
		t.Fatalf("setup slots after session exit = %d, want 1 until writer completion", len(sessions.recordingSetupSlots))
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
	if err := startLoggingTestShell(sessions, 2, "ssh", true); err != nil {
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
		startResult <- startLoggingTestShell(sessions, 3, "ssh", true)
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
	if countStartingSessions(sessions) != 0 {
		t.Fatalf("pending logging owners after failed shutdown start = %d, want 0", countStartingSessions(sessions))
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

func TestSessionLimitCountsActiveAndStartingIDs(t *testing.T) {
	sessions := NewSessions(nil, nil)
	for id := 1; id < maxSessions; id++ {
		sessions.sessions[id] = &managedSession{}
	}
	managed := &managedSession{}
	if err := sessions.reserveSessionStart(maxSessions, managed); err != nil {
		t.Fatalf("reserve final available session slot: %v", err)
	}
	if sessions.starting[maxSessions] != managed {
		t.Fatal("pending session start was not reserved")
	}
	if err := sessions.reserveSessionStart(maxSessions+1, &managedSession{}); err == nil || !strings.Contains(err.Error(), "session limit") {
		t.Fatalf("reserve beyond session limit = %v, want limit error", err)
	}
}

func TestRecorderSetupConcurrencyLimitDoesNotStartAnotherFactory(t *testing.T) {
	sessions := NewSessions(nil, nil)
	factoryCalls := 0
	sessions.makeRecorder = func(sessionlog.Options) (sessionRecorder, error) {
		factoryCalls++
		return &retryTestRecorder{path: "/tmp/unexpected.txt"}, nil
	}
	if err := startLoggingTestShell(sessions, 16, "local", false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < cap(sessions.recordingSetupSlots); i++ {
		sessions.recordingSetupSlots <- struct{}{}
	}
	if _, err := sessions.StartLogging(16, 0); err == nil || !strings.Contains(err.Error(), "concurrency limit") {
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
	if err := startLoggingTestShell(sessions, 17, "local", false); err != nil {
		t.Fatal(err)
	}
	managed, err := sessions.lookup(17)
	if err != nil {
		t.Fatal(err)
	}
	managed.logging.eventMu.Lock()
	startResult := make(chan error, 1)
	go func() {
		_, err := sessions.StartLogging(17, 0)
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
	state := managed.logging.State()
	if state.Active || state.Error != "recording write failed" {
		t.Fatalf("logging state after concurrent setup error = %+v", state)
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

func TestCloseAfterExitIsIdempotentAndEmitsFinalError(t *testing.T) {
	sessions := NewSessions(nil, nil)
	events := make(chan SessionLoggingState, 4)
	sessions.loggingStateEmitter = func(state SessionLoggingState) { events <- state }
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
	if err := sessions.Close(4); err != nil {
		t.Fatalf("repeat close after natural exit: %v", err)
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

	seenFinalError := false
	for len(events) > 0 {
		event := <-events
		if event.ID == 5 && event.Error == finalErr.Error() {
			seenFinalError = true
		}
	}
	if !seenFinalError {
		t.Fatal("natural exit did not emit the recording finalization error")
	}
	if err := sessions.Close(5); err != nil {
		t.Fatalf("close after natural exit with finalization error: %v", err)
	}
	if err := sessions.Close(5); err != nil {
		t.Fatalf("repeat close after natural exit: %v", err)
	}
	recorder.mu.Lock()
	closeCalls := recorder.closeCalls
	recorder.mu.Unlock()
	if closeCalls != 1 {
		t.Fatalf("natural-exit recorder close calls = %d, want 1", closeCalls)
	}
}
