package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"sshbrowse/internal/profile"
)

func testBackupPreferences() map[string]string {
	return map[string]string{
		"sshbrowse.sidebar.width": "248", "sshbrowse.sidebar.visible": "true",
		"sshbrowse.tiling.enabled": "false", "sshbrowse.terminal.rightClickToPaste": "false",
		"sshbrowse.terminal.copyOnSelection": "true", "sshbrowse.updates.checkOnStartup": "true",
		"sshbrowse.terminal.scrollbackLines": "1000", "sshbrowse.sidebar.collapsed": `[]`,
		"sshbrowse.terminal.pasteWarningsDisabled": "false", "sshbrowse.appearance.theme": "classic",
		"sshbrowse.appearance.terminalColors": "follow", "sshbrowse.appearance.uiSize": "standard",
		"sshbrowse.appearance.terminalFontSize": "14", "sshbrowse.interface.scale": "1",
		"sshbrowse.appearance.terminalFontName": "system",
	}
}

func testImportBackup(t *testing.T) []byte {
	t.Helper()
	data, err := encodeBackup(profile.Snapshot{Connections: []profile.Connection{{ID: "example", Name: "Example", Host: "example.invalid"}}, Folders: []string{}}, testBackupPreferences())
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func newTestBackup(t *testing.T) *Backup {
	t.Helper()
	path := filepath.Join(t.TempDir(), "connections.json")
	return NewBackup(profile.NewStore(path), path, nil)
}

func TestFirstImportCreatesPrivateConfigAndRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "sshbrowse", "connections.json")
	b := NewBackup(profile.NewStore(path), path, nil)
	preview, err := b.stage(testImportBackup(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("preview wrote a connection file")
	}
	result, err := b.Apply(preview.Token, false, testBackupPreferences())
	if err != nil || result.Added != 1 {
		t.Fatalf("first import: %+v, %v", result, err)
	}
	// Windows mode bits do not express Unix file permissions.
	if info, err := os.Stat(filepath.Dir(path)); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o700) {
		t.Fatal("first import did not create a private config directory")
	}
	data, err := readBackupFile(result.RecoveryPath)
	if err != nil {
		t.Fatal(err)
	}
	var recovery backupFile
	if err := json.Unmarshal(data, &recovery); err != nil || recovery.Profile == nil || len(recovery.Profile.Connections) != 0 {
		t.Fatal("first import did not back up the empty setup")
	}
}

func TestRestoreRepairsBrokenCurrentRouting(t *testing.T) {
	b := newTestBackup(t)
	broken := []byte(`{"version":1,"connections":[{"id":"a","host":"a.invalid","jumpConnectionId":"b"},{"id":"b","host":"b.invalid","jumpConnectionId":"a"}],"folders":[]}`)
	if err := os.WriteFile(b.storePath, broken, 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := encodeBackup(profile.Snapshot{Connections: []profile.Connection{{ID: "healthy", Host: "healthy.invalid"}}, Folders: []string{}}, testBackupPreferences())
	if err != nil {
		t.Fatal(err)
	}
	preview, err := b.stage(data)
	if err != nil {
		t.Fatalf("broken current routing blocked preview: %v", err)
	}
	if preview.MergeError == "" {
		t.Fatal("preview did not explain why adding is unavailable")
	}
	if _, err := b.Apply(preview.Token, false, testBackupPreferences()); err == nil {
		t.Fatal("merge accepted broken current routing")
	}
	unchanged, _ := os.ReadFile(b.storePath)
	if !bytes.Equal(unchanged, broken) {
		t.Fatal("rejected merge changed the current library")
	}
	result, err := b.Apply(preview.Token, true, testBackupPreferences())
	if err != nil || result.Added != 1 {
		t.Fatalf("restore failed: %v", err)
	}
	loaded, err := b.store.List()
	if err != nil || len(loaded) != 1 || loaded[0].ID != "healthy" {
		t.Fatal("restore did not replace broken routing")
	}
	recovery, err := readBackupFile(result.RecoveryPath)
	if err != nil {
		t.Fatal(err)
	}
	var previous backupFile
	if err := json.Unmarshal(recovery, &previous); err != nil || len(previous.Profile.Connections) != 2 || previous.Profile.Connections[0].JumpConnectionID != "b" {
		t.Fatal("recovery did not preserve the original broken library")
	}
}

func TestBackupStageApplyAndRecovery(t *testing.T) {
	b := newTestBackup(t)
	old, err := b.store.Save(profile.Connection{Name: "Existing", Host: "old.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	incoming := profile.Snapshot{Connections: []profile.Connection{{ID: "imported", Name: "New", Host: "new.invalid", Folder: "Work/Servers", IdentityFile: "~/.ssh/example", Provenance: &profile.Provenance{SSHConfigAlias: "example"}}}, Folders: []string{"Empty", "Work", "Work/Servers"}, Onboarding: profile.OnboardingState{SSHConfigImportAnswered: true}}
	data, err := encodeBackup(incoming, testBackupPreferences())
	if err != nil {
		t.Fatal(err)
	}
	preview, err := b.stage(data)
	if err != nil || preview.ExistingCount != 1 || len(preview.Connections) != 1 {
		t.Fatalf("preview failed: %v", err)
	}
	result, err := b.Apply(preview.Token, true, testBackupPreferences())
	if err != nil || result.Added != 1 || result.RecoveryPath != b.recoveryPath() {
		t.Fatalf("apply failed: %v", err)
	}
	loaded, err := b.store.Snapshot()
	if err != nil || loaded.Connections[0].ID != "imported" || !loaded.Onboarding.SSHConfigImportAnswered {
		t.Fatalf("replacement failed: %v", err)
	}
	recovery, err := readBackupFile(result.RecoveryPath)
	if err != nil {
		t.Fatal(err)
	}
	var parsed backupFile
	if err := json.Unmarshal(recovery, &parsed); err != nil || parsed.Profile.Connections[0].ID != old.ID || !reflect.DeepEqual(parsed.Preferences, testBackupPreferences()) {
		t.Fatal("recovery backup did not retain the previous setup")
	}
	if _, err := b.Apply(preview.Token, true, testBackupPreferences()); err == nil {
		t.Fatal("applied a consumed token")
	}
	if info, err := os.Stat(result.RecoveryPath); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatal("recovery permissions are not private")
	}
}

func TestRejectedBackupDoesNotChangeProfiles(t *testing.T) {
	b := newTestBackup(t)
	if _, err := b.store.Save(profile.Connection{Host: "existing.invalid"}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(b.storePath)
	snapshot, _ := b.store.Snapshot()
	valid, _ := encodeBackup(snapshot, testBackupPreferences())
	cases := [][]byte{
		[]byte(`{}`),
		bytes.Replace(valid, []byte(`"version": 1`), []byte(`"version": 2`), 1),
		append(append([]byte{}, valid...), []byte(`{}`)...),
		[]byte(strings.Repeat(" ", maxBackupBytes+1)),
		bytes.Replace(valid, []byte(`"sshbrowse.interface.scale": "1"`), []byte(`"sshbrowse.interface.scale": "9"`), 1),
	}
	for i, data := range cases {
		if _, err := b.stage(data); err == nil {
			t.Errorf("case %d accepted", i)
		}
		after, _ := os.ReadFile(b.storePath)
		if !bytes.Equal(before, after) {
			t.Fatalf("case %d changed the profile", i)
		}
	}
}

func TestBackupPreviewCancellationAndExpiry(t *testing.T) {
	b := newTestBackup(t)
	preview, err := b.stage(testImportBackup(t))
	if err != nil {
		t.Fatal(err)
	}
	b.Cancel("unrelated-token")
	if b.pending == nil {
		t.Fatal("cancelled a different preview")
	}
	b.Cancel(preview.Token)
	if _, err := b.Apply(preview.Token, false, testBackupPreferences()); err == nil {
		t.Fatal("cancelled preview accepted")
	}
	preview, _ = b.stage(testImportBackup(t))
	b.pending.expires = time.Now().Add(-time.Second)
	if _, err := b.Apply(preview.Token, false, testBackupPreferences()); err == nil {
		t.Fatal("expired preview accepted")
	}
}

func TestBackupExportProtectsActiveFiles(t *testing.T) {
	b := newTestBackup(t)
	for _, path := range []string{b.storePath, b.storePath + ".lock", b.recoveryPath(), filepath.Join(filepath.Dir(b.storePath), "window.json")} {
		if err := b.exportFile(path, testBackupPreferences()); err == nil {
			t.Fatal("export could overwrite active app data")
		}
	}
	output := filepath.Join(t.TempDir(), "backup.json")
	if err := b.exportFile(output, testBackupPreferences()); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(output); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatal("export permissions are not private")
	}
}

func TestBackupPreferenceValidation(t *testing.T) {
	for key, value := range map[string]string{
		"sshbrowse.sidebar.visible": "yes", "sshbrowse.terminal.scrollbackLines": "-1",
		"sshbrowse.appearance.theme": "unknown", "sshbrowse.interface.scale": "NaN",
		"sshbrowse.sidebar.collapsed": `["../bad"]`,
	} {
		preferences := testBackupPreferences()
		preferences[key] = value
		if err := validateBackupPreferences(preferences); err == nil {
			t.Errorf("accepted invalid preference %s", key)
		}
	}
	preferences := testBackupPreferences()
	preferences["sshbrowse.updates.lastStartupCheck"] = "1"
	if err := validateBackupPreferences(preferences); err == nil {
		t.Fatal("backup accepted transient state")
	}
}

func TestBackupExportProtectsCaseAliasesBeforeFilesExist(t *testing.T) {
	b := newTestBackup(t)
	for _, protected := range []string{b.storePath, b.recoveryPath(), filepath.Join(filepath.Dir(b.storePath), "window.json")} {
		alias := filepath.Join(filepath.Dir(protected), strings.ToUpper(filepath.Base(protected)))
		if err := b.exportFile(alias, testBackupPreferences()); err == nil {
			t.Fatalf("export accepted a case-only alias of %s", filepath.Base(protected))
		}
		if _, err := os.Stat(alias); !os.IsNotExist(err) {
			t.Fatal("rejected export created a file")
		}
	}
	// A different backup filename in the same directory remains usable.
	if err := os.MkdirAll(filepath.Dir(b.storePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := b.exportFile(filepath.Join(filepath.Dir(b.storePath), "my-backup.json"), testBackupPreferences()); err != nil {
		t.Fatal(err)
	}
}

func TestBackupRejectsMissingOrNullProfileArrays(t *testing.T) {
	b := newTestBackup(t)
	if _, err := b.store.Save(profile.Connection{Host: "saved.invalid"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(b.storePath)
	if err != nil {
		t.Fatal(err)
	}
	preferences, err := json.Marshal(testBackupPreferences())
	if err != nil {
		t.Fatal(err)
	}
	for _, profileJSON := range []string{
		`{}`, `{"folders":[]}`, `{"connections":[]}`,
		`{"connections":null,"folders":[]}`, `{"connections":[],"folders":null}`,
	} {
		data := []byte(fmt.Sprintf(`{"format":"sshbrowse-backup","version":1,"profile":%s,"preferences":%s}`, profileJSON, preferences))
		if _, err := b.stage(data); err == nil {
			t.Fatalf("accepted incomplete profile: %s", profileJSON)
		}
		if b.pending != nil {
			t.Fatal("invalid backup staged an import")
		}
	}
	after, err := os.ReadFile(b.storePath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rejected backup changed the current library")
	}
	if _, err := os.Stat(b.recoveryPath()); !os.IsNotExist(err) {
		t.Fatal("rejected backup wrote a recovery file")
	}
	// Explicit empty arrays represent a valid backup of an empty setup.
	data := []byte(fmt.Sprintf(`{"format":"sshbrowse-backup","version":1,"profile":{"connections":[],"folders":[]},"preferences":%s}`, preferences))
	if preview, err := b.stage(data); err != nil || len(preview.Connections) != 0 || len(preview.Folders) != 0 {
		t.Fatalf("empty backup rejected: %v", err)
	}
}

func TestBackupPreviewMergeActionsMatchImport(t *testing.T) {
	b := newTestBackup(t)
	unchanged, err := b.store.Save(profile.Connection{Name: "Unchanged", Host: "same.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := b.store.Save(profile.Connection{Name: "Local", Host: "local.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	incoming := profile.Snapshot{Connections: []profile.Connection{
		unchanged,
		{ID: "new", Name: "New", Host: "new.invalid", JumpConnectionID: changed.ID},
		{ID: changed.ID, Name: "Imported", Host: "imported.invalid"},
	}, Folders: []string{}}
	data, err := encodeBackup(incoming, testBackupPreferences())
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(b.storePath)
	preview, err := b.stage(data)
	if err != nil || preview.MergeError != "" || !reflect.DeepEqual(preview.MergeActions, []string{"skip", "add", "copy"}) {
		t.Fatalf("merge preview: %+v, %v", preview, err)
	}
	after, _ := os.ReadFile(b.storePath)
	if !bytes.Equal(before, after) {
		t.Fatal("preview changed saved connections")
	}
	result, err := b.Apply(preview.Token, false, testBackupPreferences())
	if err != nil || result.Added != 2 || result.Skipped != 1 {
		t.Fatalf("import disagrees with preview: %+v, %v", result, err)
	}
	preview, err = b.stage(data)
	if err != nil || !reflect.DeepEqual(preview.MergeActions, []string{"skip", "skip", "skip"}) {
		t.Fatalf("repeat preview did not recognize imported copies: %+v, %v", preview, err)
	}
}

func TestBackupWithoutPreferences(t *testing.T) {
	b := newTestBackup(t)
	data, err := encodeBackup(profile.Snapshot{Connections: []profile.Connection{{ID: "portable", Host: "portable.invalid"}}, Folders: []string{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"preferences"`)) {
		t.Fatal("connection-only export included preferences")
	}
	preview, err := b.stage(data)
	if err != nil || preview.Preferences != nil {
		t.Fatalf("connection-only preview: %+v, %v", preview, err)
	}
	result, err := b.Apply(preview.Token, false, testBackupPreferences())
	if err != nil || result.Added != 1 {
		t.Fatalf("connection-only import: %+v, %v", result, err)
	}
	recoveryData, err := readBackupFile(result.RecoveryPath)
	if err != nil {
		t.Fatal(err)
	}
	var recovery backupFile
	if err := json.Unmarshal(recoveryData, &recovery); err != nil || !reflect.DeepEqual(recovery.Preferences, testBackupPreferences()) {
		t.Fatal("connection-only import lost recovery preferences")
	}
	// Preferences remain complete and validated when a file includes them.
	invalid := bytes.Replace(data, []byte(`"format":`), []byte(`"preferences": {}, "format":`), 1)
	if _, err := b.stage(invalid); err == nil {
		t.Fatal("accepted an incomplete preference map")
	}
}
