package app

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"sshbrowse/internal/sessionlog"
)

type sessionRecorder interface {
	Write([]byte) error
	Close(string) error
	Path() string
	// Done closes after the writer releases its file, even if Close timed out.
	Done() <-chan struct{}
}

func newSessionRecorder(options sessionlog.Options) (sessionRecorder, error) {
	recorder, err := sessionlog.New(options)
	if err != nil {
		return nil, err
	}
	return recorder, nil
}

type recordingSetupState uint8

const (
	recordingSetupStarting recordingSetupState = iota
	recordingSetupReady
	recordingSetupAccepted
	recordingSetupCanceled
)

// All setup fields are protected by loggingController.mu. The worker owns a
// recorder until the caller accepts it, or until canceled setup cleanup ends.
type recordingSetup struct {
	state     recordingSetupState
	recorder  sessionRecorder
	path      string
	err       error
	readyTime time.Time
	ready     chan struct{}
	canceled  chan struct{}
	decision  chan struct{}
}

// loggingController owns one terminal's recordings. Setup phase, recorder
// lifetime, and terminal exit are separate: a failed or timed-out operation may
// still own unfinished filesystem work, which must finish before another starts.
// Nested locks follow opMu, eventMu, mu. opMu serializes recorder operations;
// eventMu orders publication; mu protects state and the entire setup handshake.
// Filesystem work never holds mu, and this controller never takes Sessions.mu.
type loggingController struct {
	opMu                sync.Mutex
	eventMu             sync.Mutex
	mu                  sync.Mutex
	recorder            sessionRecorder
	setup               *recordingSetup
	state               SessionLoggingState
	generation          uint64
	closeTimeoutMessage string
	label               string
	kind                string
	finished            bool
	endReason           string
	finalization        *retainedLoggingState
	makeRecorder        func(sessionlog.Options) (sessionRecorder, error)
	setupSlots          chan struct{}
	setupTimeout        time.Duration
	publish             func(SessionLoggingState)
}

func (c *loggingController) Stop(reason string) (SessionLoggingState, error) {
	return c.finish(reason, false)
}

func (c *loggingController) Finalize(reason string) (SessionLoggingState, error) {
	return c.finish(reason, true)
}

func (c *loggingController) emit(state SessionLoggingState) {
	if c.publish != nil {
		c.publish(state)
	}
}

func (c *loggingController) State() SessionLoggingState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

func (c *loggingController) Start(loadSettings func() (LoggingSettings, error)) (SessionLoggingState, error) {
	c.opMu.Lock()

	c.mu.Lock()
	if c.finished {
		state := c.state
		c.mu.Unlock()
		c.opMu.Unlock()
		return state, errors.New("session has already closed")
	}
	if c.state.Active {
		state := c.state
		c.mu.Unlock()
		c.opMu.Unlock()
		return state, nil
	}
	if c.setup != nil {
		state := c.state
		c.mu.Unlock()
		c.opMu.Unlock()
		return state, errors.New("session logging setup is still pending")
	}
	previous := c.recorder
	c.generation++
	generation := c.generation
	c.state.Active = false
	c.state.Error = ""
	c.closeTimeoutMessage = ""
	c.mu.Unlock()

	if previous != nil {
		if err := previous.Close("previous recording stopped"); errors.Is(err, sessionlog.ErrCloseTimeout) {
			c.fail(generation, err)
			state := c.State()
			c.opMu.Unlock()
			return state, err
		}
		c.mu.Lock()
		if c.recorder == previous {
			c.recorder = nil
		}
		c.mu.Unlock()
	}
	if err := c.reserveSetupSlot(); err != nil {
		c.fail(generation, err)
		state := c.State()
		c.opMu.Unlock()
		return state, err
	}

	makeRecorder := c.makeRecorder
	if makeRecorder == nil {
		makeRecorder = newSessionRecorder
	}
	pending := &recordingSetup{
		state:    recordingSetupStarting,
		ready:    make(chan struct{}),
		canceled: make(chan struct{}),
		decision: make(chan struct{}),
	}
	c.mu.Lock()
	if c.generation != generation || c.finished || c.setup != nil {
		state := c.state
		c.mu.Unlock()
		c.opMu.Unlock()
		<-c.setupSlots
		return state, errors.New("recording state changed during setup")
	}
	c.setup = pending
	c.mu.Unlock()
	c.opMu.Unlock()

	timeout := c.setupTimeout
	if timeout <= 0 {
		timeout = defaultRecordingSetupTimeout
	}
	deadline := time.Now().Add(timeout)
	go c.runSetup(pending, generation, loadSettings, makeRecorder)

	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-pending.ready:
		return c.completeSetup(pending, generation, deadline)
	case <-timer.C:
		return c.completeSetup(pending, generation, deadline)
	case <-pending.canceled:
		return c.State(), errors.New("session logging setup was canceled")
	}
}

func (c *loggingController) reserveSetupSlot() error {
	select {
	case c.setupSlots <- struct{}{}:
		return nil
	default:
		return errors.New("session logging concurrency limit reached")
	}
}

func (c *loggingController) runSetup(pending *recordingSetup, generation uint64, loadSettings func() (LoggingSettings, error), makeRecorder func(sessionlog.Options) (sessionRecorder, error)) {
	defer func() { <-c.setupSlots }()

	settings, err := loadSettings()
	var recorder sessionRecorder
	if err == nil {
		options := sessionlog.Options{
			Directory:     settings.Directory,
			Label:         c.label,
			Kind:          c.kind,
			MaxFileBytes:  int64(settings.MaxFileSizeMB) * megabyte,
			MaxTotalBytes: int64(settings.MaxRecordingSizeMB) * megabyte,
			OnError: func(err error) {
				c.fail(generation, err)
			},
		}
		recorder, err = makeRecorder(options)
	}
	if err != nil {
		recorder = nil
	}
	path := ""
	if err == nil && recorder == nil {
		err = errors.New("session logging recorder setup returned no recorder")
	}
	if err == nil {
		path = recorder.Path()
		if path == "" {
			err = errors.New("session logging recorder returned an empty path")
		} else if resolvedPath, resolveErr := filepath.Abs(path); resolveErr != nil {
			err = fmt.Errorf("resolve session log path: %w", resolveErr)
		} else {
			path = filepath.Clean(resolvedPath)
		}
	}
	if err != nil && recorder != nil {
		closeLateSetupRecorder(recorder, "recording setup failed")
		recorder = nil
	}

	c.mu.Lock()
	if pending.state == recordingSetupCanceled {
		c.mu.Unlock()
		if recorder != nil {
			closeLateSetupRecorder(recorder, "recording setup canceled")
		}
		c.clearSetup(pending)
		return
	}
	pending.recorder = recorder
	pending.path = path
	pending.err = err
	pending.readyTime = time.Now()
	pending.state = recordingSetupReady
	close(pending.ready)
	c.mu.Unlock()

	<-pending.decision
	c.mu.Lock()
	canceled := pending.state == recordingSetupCanceled
	if canceled {
		recorder = pending.recorder
	}
	c.mu.Unlock()
	if canceled {
		if recorder != nil {
			closeLateSetupRecorder(recorder, "recording setup canceled")
		}
		c.clearSetup(pending)
	} else if recorder != nil {
		<-recorder.Done()
	}
}

// A superseded setup cannot report useful close errors to its former caller.
// Keep its slot and setup token until Done, including after a close timeout.
func closeLateSetupRecorder(recorder sessionRecorder, reason string) {
	_ = recorder.Close(reason)
	<-recorder.Done()
}

func (c *loggingController) completeSetup(pending *recordingSetup, generation uint64, deadline time.Time) (SessionLoggingState, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()

	c.eventMu.Lock()
	c.mu.Lock()
	if c.generation != generation || c.finished || c.setup != pending {
		state := c.state
		c.cancelPendingSetupLocked(pending)
		c.mu.Unlock()
		c.eventMu.Unlock()
		return state, errors.New("session logging setup was canceled")
	}

	if pending.state != recordingSetupReady || pending.readyTime.After(deadline) {
		if pending.state == recordingSetupStarting || pending.state == recordingSetupReady {
			c.cancelPendingSetupLocked(pending)
		}
		state := c.state
		timedOut := pending.state == recordingSetupCanceled
		c.mu.Unlock()
		c.eventMu.Unlock()
		if timedOut {
			failure := errors.New("session logging setup timed out")
			c.fail(generation, failure)
			return c.State(), failure
		}
		return state, errors.New("session logging setup was canceled")
	}

	if c.state.Error != "" {
		failure := errors.New(c.state.Error)
		c.cancelPendingSetupLocked(pending)
		state := c.state
		c.mu.Unlock()
		c.eventMu.Unlock()
		return state, failure
	}

	pending.state = recordingSetupAccepted
	close(pending.decision)
	recorder := pending.recorder
	path := pending.path
	setupErr := pending.err
	c.setup = nil
	if setupErr != nil {
		c.mu.Unlock()
		c.eventMu.Unlock()
		c.fail(generation, setupErr)
		return c.State(), setupErr
	}
	c.recorder = recorder
	c.state.Active = true
	c.state.Path = path
	c.state.Error = ""
	c.closeTimeoutMessage = ""
	state := c.state
	c.mu.Unlock()
	c.emit(state)
	c.eventMu.Unlock()
	return state, nil
}

// The decision channel transfers recorder ownership to the controller or
// cancels the handoff. Only the setup worker may close a canceled late recorder.
func (c *loggingController) cancelPendingSetupLocked(pending *recordingSetup) {
	if pending.state != recordingSetupStarting && pending.state != recordingSetupReady {
		return
	}
	pending.state = recordingSetupCanceled
	close(pending.canceled)
	close(pending.decision)
}

func (c *loggingController) clearSetup(pending *recordingSetup) {
	c.mu.Lock()
	if c.setup == pending {
		c.setup = nil
	}
	c.mu.Unlock()
}

// Cancel ends terminal ownership promptly without waiting for filesystem work.
// Finalize performs the bounded recorder close and caches the terminal result.
func (c *loggingController) Cancel() { c.cancelSetup(true) }

func (c *loggingController) cancelSetup(markFinished bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if markFinished {
		c.finished = true
	}
	c.generation++
	// Invalidate writes atomically with the generation they would snapshot.
	c.state.Active = false
	if c.setup != nil {
		c.cancelPendingSetupLocked(c.setup)
	}
}

func (c *loggingController) fail(generation uint64, failure error) {
	if failure == nil {
		return
	}
	c.eventMu.Lock()
	c.mu.Lock()
	if generation != c.generation || (c.state.Error != "" && c.closeTimeoutMessage == "") {
		c.mu.Unlock()
		c.eventMu.Unlock()
		return
	}
	c.state.Active = false
	c.state.Error = failure.Error()
	if errors.Is(failure, sessionlog.ErrCloseTimeout) {
		c.closeTimeoutMessage = failure.Error()
	} else {
		c.closeTimeoutMessage = ""
	}
	state := c.state
	c.mu.Unlock()
	c.emit(state)
	c.eventMu.Unlock()
}

func (c *loggingController) Write(data []byte) {
	c.mu.Lock()
	recorder := c.recorder
	generation := c.generation
	active := c.state.Active
	c.mu.Unlock()
	if recorder == nil || !active {
		return
	}
	if err := recorder.Write(data); err != nil {
		c.fail(generation, err)
	}
}

func (c *loggingController) finish(reason string, markFinished bool) (SessionLoggingState, error) {
	c.cancelSetup(markFinished)
	c.opMu.Lock()
	defer c.opMu.Unlock()
	// A setup may have been scheduled while the first cancellation waited for
	// the operation lock. Cancel again before touching the recorder state.
	c.cancelSetup(markFinished)

	c.eventMu.Lock()
	c.mu.Lock()
	if c.finalization != nil {
		state := c.finalization.state
		closeErr := c.finalization.finalizationErr
		c.mu.Unlock()
		c.eventMu.Unlock()
		return state, closeErr
	}
	recorder := c.recorder
	c.state.Active = false
	c.mu.Unlock()
	c.eventMu.Unlock()

	var closeErr error
	if recorder != nil {
		closeErr = recorder.Close(reason)
	}
	c.eventMu.Lock()
	c.mu.Lock()
	if errors.Is(closeErr, sessionlog.ErrCloseTimeout) {
		// Keep the handle for explicit retries while the process is live.
		// Terminal-end callers reuse the first finalization result below.
	} else if c.recorder == recorder {
		c.recorder = nil
	}
	if closeErr == nil && c.closeTimeoutMessage != "" && c.state.Error == c.closeTimeoutMessage {
		c.state.Error = ""
		c.closeTimeoutMessage = ""
	} else if closeErr != nil {
		if c.closeTimeoutMessage != "" && c.state.Error == c.closeTimeoutMessage && !errors.Is(closeErr, sessionlog.ErrCloseTimeout) {
			c.state.Error = closeErr.Error()
			c.closeTimeoutMessage = ""
		} else if c.state.Error == "" {
			c.state.Error = closeErr.Error()
			if errors.Is(closeErr, sessionlog.ErrCloseTimeout) {
				c.closeTimeoutMessage = closeErr.Error()
			}
		}
	}
	state := c.state
	if markFinished {
		c.finalization = &retainedLoggingState{state: state, finalizationErr: closeErr}
	}
	c.mu.Unlock()
	c.emit(state)
	c.eventMu.Unlock()
	return state, closeErr
}

func (c *loggingController) SetEndReason(reason string) {
	c.mu.Lock()
	if !c.finished {
		c.endReason = reason
	}
	c.mu.Unlock()
}

func (c *loggingController) EndReason() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.endReason != "" {
		return c.endReason
	}
	return "process exited"
}
