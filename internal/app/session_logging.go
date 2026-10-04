package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"sshbrowse/internal/profile"
	"sshbrowse/internal/sessionlog"
)

const EventSessionLogging = "session:logging"

// LoggingSettings selects the root folder for session transcripts.
// An empty directory resolves to the default under the user's home.
type LoggingSettings struct {
	Directory string `json:"directory"`
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
	return LoggingSettings{Directory: directory}, nil
}

func normalizeLoggingSettings(settings LoggingSettings) (LoggingSettings, error) {
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
	return normalizeLoggingSettings(LoggingSettings{Directory: saved.Directory})
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
		Directory: normalized.Directory,
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

func (s *Sessions) emitLoggingState(state SessionLoggingState) {
	if s.loggingStateEmitter != nil {
		s.loggingStateEmitter(state)
		return
	}
	if s.app != nil {
		s.app.Event.Emit(EventSessionLogging, state)
	}
}
