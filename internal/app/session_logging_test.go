package app

import (
	"errors"
	"sync"
	"testing"
	"time"

	"sshbrowse/internal/sessionlog"
)

type blockedCloseRecorder struct {
	mu           sync.Mutex
	closeCalls   int
	closeStarted chan struct{}
	releaseClose chan struct{}
	startOnce    sync.Once
}

func (*blockedCloseRecorder) Write([]byte) error { return nil }

func (r *blockedCloseRecorder) Close(string) error {
	r.mu.Lock()
	r.closeCalls++
	r.mu.Unlock()
	r.startOnce.Do(func() { close(r.closeStarted) })
	<-r.releaseClose
	return sessionlog.ErrCloseTimeout
}

func (*blockedCloseRecorder) Path() string { return "/tmp/session.txt" }

func (r *blockedCloseRecorder) Done() <-chan struct{} { return r.releaseClose }

func testLoggingController(state SessionLoggingState, recorder sessionRecorder) *loggingController {
	return &loggingController{state: state, recorder: recorder}
}

type stopRaceRecorder struct {
	*sessionlog.Recorder
	writeStarted chan struct{}
	writeOnce    sync.Once
}

func (r *stopRaceRecorder) Write(data []byte) error {
	r.writeOnce.Do(func() { close(r.writeStarted) })
	<-r.Recorder.Done()
	return r.Recorder.Write(data)
}

func TestStopDoesNotReportWriteRacingRecorderClose(t *testing.T) {
	realRecorder, err := sessionlog.New(sessionlog.Options{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := realRecorder.Close("test cleanup"); err != nil {
			t.Errorf("close test recorder: %v", err)
		}
	})
	recorder := &stopRaceRecorder{
		Recorder:     realRecorder,
		writeStarted: make(chan struct{}),
	}
	controller := testLoggingController(SessionLoggingState{ID: 1, Active: true}, recorder)

	// Hold Stop after its cancellation passes and before it reaches recorder.Close.
	// Without atomic deactivation, a Write could snapshot Active with the final
	// generation in this window and reach the recorder after Close.
	controller.eventMu.Lock()
	stopResult := make(chan error, 1)
	go func() {
		_, stopErr := controller.Stop("stopped by user")
		stopResult <- stopErr
	}()

	deadline := time.Now().Add(time.Second)
	generationReady := false
	for {
		controller.mu.Lock()
		generationReady = controller.generation >= 2
		controller.mu.Unlock()
		if generationReady || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}

	var writeResult chan struct{}
	if generationReady {
		writeResult = make(chan struct{})
		go func() {
			controller.Write([]byte("ordinary output"))
			close(writeResult)
		}()
		timer := time.NewTimer(time.Second)
		select {
		case <-recorder.writeStarted:
		case <-writeResult:
		case <-timer.C:
			t.Error("Write() did not reach or return before Stop() resumed")
		}
		timer.Stop()
	}

	controller.eventMu.Unlock()

	select {
	case stopErr := <-stopResult:
		if stopErr != nil {
			t.Errorf("Stop() error = %v, want nil", stopErr)
		}
	case <-time.After(6 * time.Second):
		t.Error("Stop() did not finish after publication was released")
	}
	if writeResult != nil {
		select {
		case <-writeResult:
		case <-time.After(6 * time.Second):
			t.Error("Write() did not finish after Stop() closed the recorder")
		}
	}
	if !generationReady {
		t.Fatal("Stop() did not invalidate the generation before publication")
	}
	if state := controller.State(); state.Active || state.Error != "" {
		t.Fatalf("state after clean Stop() = %+v, want inactive without an error", state)
	}
}

func TestTerminalFinalizationCachesCloseTimeout(t *testing.T) {
	recorder := &blockedCloseRecorder{
		closeStarted: make(chan struct{}),
		releaseClose: make(chan struct{}),
	}
	managed := &managedSession{logging: testLoggingController(
		SessionLoggingState{ID: 7, Active: true}, recorder,
	)}
	type result struct {
		state SessionLoggingState
		err   error
	}
	firstResult := make(chan result, 1)
	go func() {
		state, err := managed.logging.Finalize("session closed")
		firstResult <- result{state: state, err: err}
	}()
	select {
	case <-recorder.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("first terminal close did not reach recorder")
	}

	secondStarted := make(chan struct{})
	secondResult := make(chan result, 1)
	go func() {
		close(secondStarted)
		state, err := managed.logging.Finalize("process exited")
		secondResult <- result{state: state, err: err}
	}()
	<-secondStarted
	select {
	case got := <-secondResult:
		t.Fatalf("second terminal close returned before first completed: %+v", got)
	case <-time.After(20 * time.Millisecond):
	}
	close(recorder.releaseClose)
	first := <-firstResult
	second := <-secondResult
	thirdState, thirdErr := managed.logging.Finalize("shutdown")
	if !errors.Is(first.err, sessionlog.ErrCloseTimeout) ||
		!errors.Is(second.err, sessionlog.ErrCloseTimeout) ||
		!errors.Is(thirdErr, sessionlog.ErrCloseTimeout) {
		t.Fatalf("terminal errors = %v, %v, %v; want cached close timeout", first.err, second.err, thirdErr)
	}
	if first.state != second.state || first.state != thirdState || first.state.Active || first.state.Error == "" {
		t.Fatalf("terminal states = %+v, %+v, %+v", first.state, second.state, thirdState)
	}
	recorder.mu.Lock()
	closeCalls := recorder.closeCalls
	recorder.mu.Unlock()
	if closeCalls != 1 {
		t.Fatalf("recorder close calls = %d, want 1", closeCalls)
	}
}
