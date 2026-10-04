package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"sshbrowse/internal/profile"
	"sshbrowse/internal/sessionlog"
)

const (
	EventSessionLogging = "session:logging"
	maxLoggingHistory   = 256
	megabyte            = 1024 * 1024
)

// LoggingSettings controls where session transcripts are written and their
// size limits. An empty directory resolves to the platform default.
type LoggingSettings struct {
	Directory          string `json:"directory"`
	MaxFileSizeMB      int    `json:"maxFileSizeMB"`
	MaxRecordingSizeMB int    `json:"maxRecordingSizeMB"`
}

// SessionLoggingState is emitted whenever a session's recording state changes.
type SessionLoggingState struct {
	ID     int    `json:"id"`
	Active bool   `json:"active"`
	Path   string `json:"path"`
	Error  string `json:"error"`
}

func defaultLoggingSettings() (LoggingSettings, error) {
	directory, err := sessionlog.DefaultDirectory()
	if err != nil {
		return LoggingSettings{}, fmt.Errorf("session logging: default directory: %w", err)
	}
	return LoggingSettings{
		Directory:          directory,
		MaxFileSizeMB:      int(sessionlog.DefaultMaxFileBytes / megabyte),
		MaxRecordingSizeMB: int(sessionlog.DefaultMaxTotalBytes / megabyte),
	}, nil
}

func normalizeLoggingSettings(settings LoggingSettings) (LoggingSettings, error) {
	if settings.MaxFileSizeMB < 1 || settings.MaxFileSizeMB > 1024 {
		return LoggingSettings{}, errors.New("maximum log file size must be between 1 and 1024 MB")
	}
	if settings.MaxRecordingSizeMB < settings.MaxFileSizeMB || settings.MaxRecordingSizeMB > 10240 {
		return LoggingSettings{}, errors.New("maximum recording size must be at least the file size and no more than 10240 MB")
	}
	directory := settings.Directory
	if directory == "" {
		defaults, err := defaultLoggingSettings()
		if err != nil {
			return LoggingSettings{}, err
		}
		directory = defaults.Directory
	}
	resolved, err := filepath.Abs(directory)
	if err != nil {
		return LoggingSettings{}, fmt.Errorf("session logging: resolve directory: %w", err)
	}
	settings.Directory = filepath.Clean(resolved)
	return settings, nil
}

// GetLoggingSettings returns the stored settings, using platform defaults for
// profile files created before session logging was added.
func (s *Sessions) GetLoggingSettings() (LoggingSettings, error) {
	if s.store == nil {
		return LoggingSettings{}, errors.New("session logging settings store is unavailable")
	}
	saved, err := s.store.LoggingSettings()
	if err != nil {
		return LoggingSettings{}, fmt.Errorf("session logging settings: %w", err)
	}
	if saved == nil {
		defaults, err := defaultLoggingSettings()
		if err != nil {
			return LoggingSettings{}, err
		}
		return normalizeLoggingSettings(defaults)
	}
	return normalizeLoggingSettings(LoggingSettings{
		Directory:          saved.Directory,
		MaxFileSizeMB:      saved.MaxFileSizeMB,
		MaxRecordingSizeMB: saved.MaxRecordingSizeMB,
	})
}

// SaveLoggingSettings validates and stores the resolved settings.
func (s *Sessions) SaveLoggingSettings(settings LoggingSettings) (LoggingSettings, error) {
	if s.store == nil {
		return LoggingSettings{}, errors.New("session logging settings store is unavailable")
	}
	normalized, err := normalizeLoggingSettings(settings)
	if err != nil {
		return LoggingSettings{}, err
	}
	if err := s.store.SaveLoggingSettings(profile.SessionLoggingSettings{
		Directory:          normalized.Directory,
		MaxFileSizeMB:      normalized.MaxFileSizeMB,
		MaxRecordingSizeMB: normalized.MaxRecordingSizeMB,
	}); err != nil {
		return LoggingSettings{}, fmt.Errorf("save session logging settings: %w", err)
	}
	return normalized, nil
}

// ChooseLoggingDirectory shows the native directory picker.
func (s *Sessions) ChooseLoggingDirectory() (string, error) {
	if s.app == nil {
		return "", errors.New("native directory picker is unavailable")
	}
	settings, err := s.GetLoggingSettings()
	if err != nil {
		return "", err
	}
	dialog := s.app.Dialog.OpenFile().SetTitle("Choose session log folder")
	if initialDirectory := nearestExistingDirectory(settings.Directory); initialDirectory != "" {
		dialog.SetDirectory(initialDirectory)
	}
	selected, err := dialog.
		CanChooseFiles(false).
		CanChooseDirectories(true).
		CanCreateDirectories(true).
		AttachToWindow(s.app.Window.Current()).
		PromptForSingleSelection()
	if err != nil {
		return "", err
	}
	if selected == "" {
		return "", nil
	}
	resolved, err := filepath.Abs(selected)
	if err != nil {
		return "", fmt.Errorf("resolve session log folder: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect session log folder: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("session log location is not a directory")
	}
	return filepath.Clean(resolved), nil
}

func nearestExistingDirectory(path string) string {
	for path != "" {
		info, err := os.Stat(path)
		if err == nil && info.IsDir() {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			return ""
		}
		path = parent
	}
	return ""
}

// StartLogging begins recording subsequent output for a running session.
func (s *Sessions) StartLogging(id int) (SessionLoggingState, error) {
	managed, err := s.lookup(id)
	if err != nil {
		return SessionLoggingState{}, err
	}
	return managed.logging.Start(s.GetLoggingSettings)
}

// StopLogging finalizes the current recording, if one is active.
func (s *Sessions) StopLogging(id int) (SessionLoggingState, error) {
	managed, err := s.lookup(id)
	if err != nil {
		return SessionLoggingState{}, err
	}
	return managed.logging.Stop("stopped by user")
}

// GetLoggingState returns the current state for a running session.
func (s *Sessions) GetLoggingState(id int) (SessionLoggingState, error) {
	s.mu.Lock()
	managed := s.sessions[id]
	if managed == nil {
		retained, exists := s.retainedLoggingResultLocked(id)
		s.mu.Unlock()
		if exists {
			return retained.state, nil
		}
		return SessionLoggingState{}, fmt.Errorf("session %d not found", id)
	}
	s.mu.Unlock()
	return managed.logging.State(), nil
}

// ShowLogInFolder opens the containing folder of the current recording for a
// retained session. The path comes from backend state rather than the caller.
func (s *Sessions) ShowLogInFolder(id int) error {
	if s.app == nil {
		return errors.New("file manager is unavailable")
	}
	path, err := s.recordingPathForID(id)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect session log: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("session log is not a regular file")
	}
	return s.app.Browser.OpenFile(filepath.Dir(path))
}

func validRecordingPath(state SessionLoggingState) (string, error) {
	if state.Path == "" {
		return "", errors.New("session has no recording path")
	}
	resolved, err := filepath.Abs(filepath.Clean(state.Path))
	if err != nil {
		return "", fmt.Errorf("resolve session log path: %w", err)
	}
	return filepath.Clean(resolved), nil
}

func (s *Sessions) emitLoggingState(state SessionLoggingState) {
	if s.loggingStateEmitter != nil {
		s.loggingStateEmitter(state)
		return
	}
	if s.app != nil {
		s.app.Event.Emit(EventSessionLogging, state)
	}
}
