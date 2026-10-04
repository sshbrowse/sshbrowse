// Package app is the Wails glue: services bound to the frontend and the events
// sent to it. It is the only package that imports Wails.
package app

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/adrg/xdg"
	"github.com/wailsapp/wails/v3/pkg/application"

	"sshbrowse/internal/profile"
	"sshbrowse/internal/session"
	"sshbrowse/internal/sessionlog"
	"sshbrowse/internal/sshcmd"
)

const (
	EventSessionData             = "session:data"
	EventSessionExit             = "session:exit"
	sessionShutdownTimeout       = 10 * time.Second
	defaultRecordingSetupTimeout = 5 * time.Second
	// 20 tabs of 9 tiles, the same limits the frontend enforces.
	maxSessions = 180
)

// SessionData carries one chunk of terminal output. Data is base64 so raw
// bytes survive the JSON event payload; xterm decodes it back to bytes.
type SessionData struct {
	ID       int    `json:"id"`
	Sequence int    `json:"sequence"`
	Data     string `json:"data"`
}

type SessionExit struct {
	ID       int `json:"id"`
	ExitCode int `json:"exitCode"`
}

// Sessions is bound to the frontend. Each method is callable from TypeScript.
type Sessions struct {
	app                 *application.App
	store               *profile.Store
	makeRecorder        func(sessionlog.Options) (sessionRecorder, error)
	loggingStateEmitter func(SessionLoggingState)

	mu                    sync.Mutex
	sessions              map[int]*managedSession
	shuttingDown          bool
	loggingOwners         map[int]*loggingEntry
	recordingSetupSlots   chan struct{}
	recordingSetupTimeout time.Duration
}

var _ application.ServiceShutdown = (*Sessions)(nil)

type managedSession struct {
	running  *session.Session
	delivery *outputDelivery
	logging  *loggingController
}

func NewSessions(app *application.App, store *profile.Store) *Sessions {
	return &Sessions{
		app:                   app,
		store:                 store,
		makeRecorder:          newSessionRecorder,
		sessions:              make(map[int]*managedSession),
		loggingOwners:         make(map[int]*loggingEntry),
		recordingSetupSlots:   make(chan struct{}, maxSessions),
		recordingSetupTimeout: defaultRecordingSetupTimeout,
	}
}

// Open starts ssh for a connection, saved or not, in a new pty under a session
// id the frontend chose. The frontend picks the id so it is already listening
// before the first byte of output is emitted.
func (s *Sessions) Open(id int, connection profile.Connection, cols, rows int) error {
	if connection.Host == "" {
		return fmt.Errorf("connection has no host")
	}
	if err := sshcmd.RequireSSHClient(); err != nil {
		return err
	}
	argv, err := s.connectionArgv(connection, false)
	if err != nil {
		return err
	}
	label := connection.Name
	if label == "" {
		label = connection.Host
	}
	return s.start(id, session.Options{Argv: argv, Cols: cols, Rows: rows}, label, "ssh", connection.LogOutput)
}

// OpenSFTP starts sftp for a connection in the same PTY setup as Open.
func (s *Sessions) OpenSFTP(id int, connection profile.Connection, cols, rows int) error {
	if connection.Host == "" {
		return fmt.Errorf("connection has no host")
	}
	localDir, err := sftpLocalDirectory()
	if err != nil {
		return err
	}
	if err := sshcmd.RequireSFTPClient(); err != nil {
		return err
	}
	argv, err := s.connectionArgv(connection, true)
	if err != nil {
		return err
	}
	label := connection.Name
	if label == "" {
		label = connection.Host
	}
	return s.start(id, session.Options{Argv: argv, Dir: localDir, Cols: cols, Rows: rows}, label, "sftp", connection.LogOutput)
}

// Resolve saved jumps from the current store, so edits to a hop apply to both
// SSH and SFTP without copying its settings into dependent connections.
func (s *Sessions) connectionArgv(connection profile.Connection, sftp bool) ([]string, error) {
	var connections []profile.Connection
	if connection.JumpConnectionID != "" {
		if s.store == nil {
			return nil, fmt.Errorf("saved jump connection store is unavailable")
		}
		var err error
		connections, err = s.store.List()
		if err != nil {
			return nil, err
		}
	}
	return sshcmd.ConnectionArgv(connection, connections, sftp)
}

func sftpLocalDirectory() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("sftp session: home directory: %w", err)
	}
	return sftpLocalDirectoryForHome(home, xdg.UserDirs.Download), nil
}

func sftpLocalDirectoryForHome(home, downloads string) string {
	info, err := os.Stat(downloads)
	if err == nil && info.IsDir() && canEnterDirectory(downloads) {
		return downloads
	}
	// An unavailable or non-directory Downloads path must not prevent SFTP from opening.
	return home
}

// OpenLocal starts an interactive login shell in the user's home directory.
func (s *Sessions) OpenLocal(id, cols, rows int) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("local terminal: home directory: %w", err)
	}
	argv, err := localShell()
	if err != nil {
		return err
	}
	return s.start(id, session.Options{Argv: argv, Dir: home, Cols: cols, Rows: rows}, "local", "local", false)
}

func (s *Sessions) start(id int, options session.Options, label, kind string, automaticLogging bool) error {
	if label == "" {
		label = kind
	}
	managed := &managedSession{logging: &loggingController{
		state:        SessionLoggingState{ID: id},
		label:        label,
		kind:         kind,
		setupSlots:   s.recordingSetupSlots,
		setupTimeout: s.recordingSetupTimeout,
		makeRecorder: s.makeRecorder,
		publish:      s.emitLoggingState,
	}}
	if err := s.reserveSessionStart(id, managed); err != nil {
		return err
	}

	if automaticLogging {
		// Settings reads and recorder creation share one bounded setup wait.
		// A logging setup failure is reported through session:logging and must
		// not prevent the terminal process from starting.
		_, _ = managed.logging.Start(s.GetLoggingSettings)
	}

	// Recheck because shutdown or another start may have changed state while
	// configuration or recorder setup was in progress.
	s.mu.Lock()
	if err := s.startValidationErrorLocked(id, managed); err != nil {
		s.mu.Unlock()
		return errors.Join(err, s.finalizeSession(managed, "session did not start"))
	}

	delivery := newOutputDelivery(outputAcknowledgementTimeout, func(event SessionData) {
		if s.app != nil {
			s.app.Event.Emit(EventSessionData, event)
		}
	})
	options.OnData = func(data []byte) bool {
		managed.logging.Write(data)
		return delivery.deliver(id, base64.StdEncoding.EncodeToString(data))
	}
	options.CancelData = delivery.Stop
	options.OnExit = func(exitCode int) {
		delivery.Stop()
		// Finalization errors are emitted in logging state and retained for Close.
		_ = s.finalizeSession(managed, managed.logging.EndReason())
		if s.app != nil {
			s.app.Event.Emit(EventSessionExit, SessionExit{ID: id, ExitCode: exitCode})
		}
	}
	started, err := session.Start(options)
	if err != nil {
		s.mu.Unlock()
		delivery.Stop()
		return errors.Join(err, s.finalizeSession(managed, "session failed to start"))
	}
	managed.running = started
	managed.delivery = delivery
	s.sessions[id] = managed
	s.mu.Unlock()
	return nil
}

// Write sends keyboard input to a session.
func (s *Sessions) Write(id int, data string) error {
	found, err := s.lookup(id)
	if err != nil {
		return err
	}
	return found.running.Write([]byte(data))
}

func (s *Sessions) Resize(id, cols, rows int) error {
	found, err := s.lookup(id)
	if err != nil {
		return err
	}
	return found.running.Resize(cols, rows)
}

// AcknowledgeOutput releases the PTY reader after xterm has consumed a write.
func (s *Sessions) AcknowledgeOutput(id, sequence int) error {
	found, err := s.lookup(id)
	if err != nil {
		return err
	}
	return found.delivery.acknowledge(sequence)
}

// Close hangs up a session. The exit event follows once the process is gone.
func (s *Sessions) Close(id int) error {
	found, err := s.lookup(id)
	if err != nil {
		s.mu.Lock()
		retained, exists := s.retainedLoggingResultLocked(id)
		s.mu.Unlock()
		if exists {
			return retained.finalizationErr
		}
		return err
	}
	found.logging.SetEndReason("session closed; unread output may be omitted")
	found.delivery.Stop()
	closeErr := found.running.Close()
	_, loggingErr := found.logging.Finalize(found.logging.EndReason())
	return errors.Join(closeErr, loggingErr)
}

// ServiceShutdown is called by Wails when the application quits. Sessions are
// closed in parallel so a few stuck ones cannot add up to a long quit.
func (s *Sessions) ServiceShutdown() error {
	s.mu.Lock()
	s.shuttingDown = true
	open := make([]*managedSession, 0, len(s.sessions))
	for _, running := range s.sessions {
		open = append(open, running)
		running.logging.SetEndReason("session closed; unread output may be omitted")
		running.delivery.Stop()
	}
	reserved := make([]*managedSession, 0, len(s.loggingOwners))
	for _, entry := range s.loggingOwners {
		if entry.owner != nil {
			reserved = append(reserved, entry.owner)
		}
	}
	s.mu.Unlock()
	for _, managed := range reserved {
		managed.logging.Cancel()
	}

	completed := make(chan struct{}, len(open))
	for _, running := range open {
		go func(running *managedSession) {
			// Shutdown cannot recover from per-session close errors; the outer
			// timeout still bounds quitting.
			if running.running != nil {
				_ = running.running.Close()
			}
			_, _ = running.logging.Finalize(running.logging.EndReason())
			completed <- struct{}{}
		}(running)
	}
	timer := time.NewTimer(sessionShutdownTimeout)
	defer timer.Stop()
	for range open {
		select {
		case <-completed:
		case <-timer.C:
			return fmt.Errorf("session shutdown timed out")
		}
	}
	return nil
}

// count is how many processes are still running, for the quit guard.
func (s *Sessions) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

func (s *Sessions) lookup(id int) (*managedSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	found, ok := s.sessions[id]
	if !ok {
		return nil, fmt.Errorf("session %d not found", id)
	}
	return found, nil
}
