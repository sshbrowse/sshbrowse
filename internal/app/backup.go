package app

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"sshbrowse/internal/atomicfile"
	"sshbrowse/internal/profile"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const maxBackupBytes = 2 * 1024 * 1024
const importPreviewLifetime = 30 * time.Minute

type backupFile struct {
	Format      string            `json:"format"`
	Version     int               `json:"version"`
	ExportedAt  time.Time         `json:"exportedAt"`
	Profile     *profile.Snapshot `json:"profile"`
	Preferences map[string]string `json:"preferences"`
}

type ImportPreview struct {
	Token         string               `json:"token"`
	Connections   []profile.Connection `json:"connections"`
	Folders       []string             `json:"folders"`
	Preferences   map[string]string    `json:"preferences"`
	Warnings      []string             `json:"warnings"`
	ExistingCount int                  `json:"existingCount"`
}

type ImportResult struct {
	Added        int    `json:"added"`
	Skipped      int    `json:"skipped"`
	RecoveryPath string `json:"recoveryPath"`
}

type pendingImport struct {
	preview  ImportPreview
	snapshot profile.Snapshot
	revision string
	expires  time.Time
}

// Backup stages one bounded, validated file in memory. Applying the preview
// uses these bytes, not a file that could have changed since the user saw it.
type Backup struct {
	store     *profile.Store
	storePath string
	app       *application.App
	mu        sync.Mutex
	pending   *pendingImport
}

func NewBackup(store *profile.Store, storePath string, app *application.App) *Backup {
	return &Backup{store: store, storePath: storePath, app: app}
}

func (b *Backup) Export(preferences map[string]string) (string, error) {
	if !b.mu.TryLock() {
		return "", errors.New("another import or export is in progress")
	}
	defer b.mu.Unlock()
	if err := validateBackupPreferences(preferences); err != nil {
		return "", err
	}
	if b.app == nil {
		return "", errors.New("file dialogs are unavailable")
	}
	path, err := b.app.Dialog.SaveFile().SetMessage("Export SSHBrowse backup").
		AddFilter("SSHBrowse backup", "*.json").
		SetFilename("sshbrowse-" + time.Now().Format("2006-01-02") + ".json").
		AttachToWindow(b.app.Window.Current()).PromptForSingleSelection()
	if err != nil || path == "" {
		return "", err
	}
	if strings.ToLower(filepath.Ext(path)) != ".json" {
		return "", errors.New("use a .json filename for the backup")
	}
	if err := b.exportFile(path, preferences); err != nil {
		return "", err
	}
	return path, nil
}

func (b *Backup) exportFile(path string, preferences map[string]string) error {
	for _, protected := range []string{b.storePath, b.storePath + ".lock", filepath.Join(filepath.Dir(b.storePath), "window.json"), b.recoveryPath()} {
		if sameFilePath(path, protected) {
			return errors.New("choose a backup file outside SSHBrowse's active data files")
		}
	}
	snapshot, err := b.store.Snapshot()
	if err != nil {
		return err
	}
	data, err := encodeBackup(snapshot, preferences)
	if err != nil {
		return err
	}
	return atomicfile.WriteReplace(path, data, 0o600)
}

func sameFilePath(a, b string) bool {
	if aInfo, err := os.Stat(a); err == nil {
		if bInfo, err := os.Stat(b); err == nil && os.SameFile(aInfo, bInfo) {
			return true
		}
	}
	resolve := func(path string) string {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return filepath.Clean(path)
		}
		if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
			return resolved
		}
		// Resolve the parent too, including a new file chosen through a symlink.
		if parent, err := filepath.EvalSymlinks(filepath.Dir(absolute)); err == nil {
			return filepath.Join(parent, filepath.Base(absolute))
		}
		return absolute
	}
	// Also reject case-only aliases when the target does not exist yet. This
	// deliberately protects those names even on case-sensitive volumes.
	return strings.EqualFold(resolve(a), resolve(b))
}

func (b *Backup) Preview() (*ImportPreview, error) {
	if !b.mu.TryLock() {
		return nil, errors.New("another import or export is in progress")
	}
	defer b.mu.Unlock()
	if b.app == nil {
		return nil, errors.New("file dialogs are unavailable")
	}
	path, err := b.app.Dialog.OpenFile().SetTitle("Import SSHBrowse backup").
		AddFilter("SSHBrowse backup", "*.json").
		CanChooseFiles(true).CanChooseDirectories(false).
		AttachToWindow(b.app.Window.Current()).PromptForSingleSelection()
	if err != nil || path == "" {
		return nil, err
	}
	data, err := readBackupFile(path)
	if err != nil {
		return nil, err
	}
	return b.stage(data)
}

func readBackupFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("choose a regular backup file")
	}
	if info.Size() > maxBackupBytes {
		return nil, errors.New("import exceeds the 2 MiB size limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close() // Read-only file; closing cannot change the import.
	data, err := io.ReadAll(io.LimitReader(file, maxBackupBytes+1))
	if err == nil && len(data) > maxBackupBytes {
		err = errors.New("import exceeds the 2 MiB size limit")
	}
	return data, err
}

func (b *Backup) stage(data []byte) (*ImportPreview, error) {
	if len(data) > maxBackupBytes {
		return nil, errors.New("import exceeds the 2 MiB size limit")
	}
	var parsed backupFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&parsed)
	if err == nil {
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			err = errors.New("backup contains extra data")
		}
	}
	if err == nil && (parsed.Format != "sshbrowse-backup" || parsed.Version != 1 || parsed.Profile == nil) {
		err = errors.New("unsupported SSHBrowse backup format or version")
	}
	if err == nil && (parsed.Profile.Connections == nil || parsed.Profile.Folders == nil) {
		err = errors.New("backup profile must contain connections and folders arrays")
	}
	if err == nil {
		err = validateBackupPreferences(parsed.Preferences)
	}
	if err != nil {
		return nil, fmt.Errorf("cannot import file: %w", err)
	}
	snapshot, err := profile.NormalizeSnapshot(*parsed.Profile)
	if err != nil {
		return nil, err
	}
	current, err := b.store.Snapshot()
	if err != nil {
		return nil, err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return nil, err
	}
	preview := ImportPreview{
		Token: hex.EncodeToString(random), Connections: snapshot.Connections,
		Folders: snapshot.Folders, Preferences: parsed.Preferences, Warnings: profile.SnapshotWarnings(snapshot),
		ExistingCount: len(current.Connections),
	}
	b.pending = &pendingImport{preview: preview, snapshot: snapshot, revision: profile.SnapshotRevision(current), expires: time.Now().Add(importPreviewLifetime)}
	return &preview, nil
}

func (b *Backup) Cancel(token string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pending != nil && b.pending.preview.Token == token {
		b.pending = nil
	}
}

func (b *Backup) Apply(token string, replace bool, currentPreferences map[string]string) (ImportResult, error) {
	if !b.mu.TryLock() {
		return ImportResult{}, errors.New("another import or export is in progress")
	}
	defer b.mu.Unlock()
	if b.pending == nil || b.pending.preview.Token != token || time.Now().After(b.pending.expires) {
		b.pending = nil
		return ImportResult{}, errors.New("import preview expired; choose the file again")
	}
	if err := validateBackupPreferences(currentPreferences); err != nil {
		return ImportResult{}, err
	}
	// Keep one recovery file, overwritten atomically by the next successful
	// import attempt. Both app instances hold the profile lock in this callback.
	result, err := b.store.ApplySnapshot(b.pending.snapshot, replace, b.pending.revision, func(current profile.Snapshot) error {
		data, err := encodeBackup(current, currentPreferences)
		if err != nil {
			return err
		}
		return atomicfile.WriteReplace(b.recoveryPath(), data, 0o600)
	})
	if err != nil {
		return ImportResult{}, err
	}
	b.pending = nil
	return ImportResult{Added: result.Added, Skipped: result.Skipped, RecoveryPath: b.recoveryPath()}, nil
}

func (b *Backup) recoveryPath() string {
	return filepath.Join(filepath.Dir(b.storePath), "before-import.sshbrowse.json")
}

func encodeBackup(snapshot profile.Snapshot, preferences map[string]string) ([]byte, error) {
	if err := validateBackupPreferences(preferences); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(backupFile{Format: "sshbrowse-backup", Version: 1, ExportedAt: time.Now().UTC(), Profile: &snapshot, Preferences: preferences}, "", "  ")
	if err != nil {
		return nil, err
	}
	if len(data)+1 > maxBackupBytes {
		return nil, errors.New("backup exceeds the 2 MiB size limit")
	}
	return append(data, '\n'), nil
}
