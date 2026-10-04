package workspaces

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
)

func TestNamedWorkspaceCRUDPersistsOnlyLayoutMetadata(t *testing.T) {
	connectionsPath := filepath.Join(t.TempDir(), "connections.json")
	store := NewStore(connectionsPath)
	wantLayout := Layout{
		ActiveTab: 0,
		Tabs: []Tab{{SelectedPane: 1, Panes: []Pane{
			{Kind: "ssh", ConnectionID: "saved-connection-id"},
			{Kind: "local"},
		}}},
	}

	created, err := store.Save("  Workbench  ", wantLayout)
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "Workbench" || created.ID == "" || created.Layout.ActiveTab != 0 {
		t.Fatalf("created workspace = %+v", created)
	}
	state := store.Load()
	if state.Warning != "" || len(state.Workspaces) != 1 || !reflect.DeepEqual(state.Workspaces[0], created) {
		t.Fatalf("loaded state = %+v", state)
	}

	renamed, err := store.Rename(created.ID, "Renamed")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.ID != created.ID || renamed.Name != "Renamed" {
		t.Fatalf("renamed workspace = %+v", renamed)
	}

	data, err := os.ReadFile(filepath.Join(filepath.Dir(connectionsPath), workspaceFileName))
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if got := sortedKeys(stored); got != "version,workspaces" {
		t.Fatalf("top-level stored fields = %s", got)
	}
	workspaces := stored["workspaces"].([]any)
	workspace := workspaces[0].(map[string]any)
	if got := sortedKeys(workspace); got != "id,layout,name" {
		t.Fatalf("workspace stored fields = %s", got)
	}
	if strings.Contains(string(data), "terminalOutput") || strings.Contains(string(data), "password") || strings.Contains(string(data), "sessionId") {
		t.Fatalf("unexpected runtime or credential data in saved file: %s", data)
	}

	if err := store.Delete(created.ID); err != nil {
		t.Fatal(err)
	}
	if state := store.Load(); len(state.Workspaces) != 0 || state.Warning != "" {
		t.Fatalf("state after delete = %+v", state)
	}
}

func TestLoadMalformedNamedDataWarnsAndMutationsDoNotOverwrite(t *testing.T) {
	connectionsPath := filepath.Join(t.TempDir(), "connections.json")
	workspacePath := filepath.Join(filepath.Dir(connectionsPath), workspaceFileName)
	tests := []struct {
		name string
		data []byte
	}{
		{name: "invalid JSON", data: []byte("{")},
		{name: "unknown field", data: []byte(`{"version":1,"workspaces":[],"terminalOutput":"secret"}`)},
		{name: "unsupported version", data: []byte(`{"version":2,"workspaces":[]}`)},
		{name: "invalid layout", data: []byte(`{"version":1,"workspaces":[{"id":"id-1","name":"bad","layout":{"tabs":[],"activeTab":0}}]}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile(workspacePath, tt.data, 0o600); err != nil {
				t.Fatal(err)
			}
			state := NewStore(connectionsPath).Load()
			if state.Warning == "" || len(state.Workspaces) != 0 {
				t.Fatalf("Load() = %+v", state)
			}
			if strings.Contains(state.Warning, "secret") || strings.Contains(state.Warning, "terminalOutput") {
				t.Fatalf("warning echoed malformed file content: %q", state.Warning)
			}
			if _, err := NewStore(connectionsPath).Save("New", emptyLayout()); err == nil {
				t.Fatal("Save overwrote malformed data")
			}
			got, err := os.ReadFile(workspacePath)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(tt.data) {
				t.Fatalf("malformed data changed: got %q, want %q", got, tt.data)
			}
		})
	}
}

func TestOversizedWorkspaceDataWarnsWithoutReadingUnboundedFile(t *testing.T) {
	connectionsPath := filepath.Join(t.TempDir(), "connections.json")
	workspacePath := filepath.Join(filepath.Dir(connectionsPath), workspaceFileName)
	if err := os.WriteFile(workspacePath, make([]byte, maxFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	state := NewStore(connectionsPath).Load()
	if state.Warning == "" || len(state.Workspaces) != 0 {
		t.Fatalf("Load() = %+v", state)
	}
	if _, err := NewStore(connectionsPath).Save("New", emptyLayout()); err == nil {
		t.Fatal("Save overwrote oversized data")
	}
}

func TestRecoveryWaitsForExplicitResolutionAndPersistsReplacement(t *testing.T) {
	connectionsPath := filepath.Join(t.TempDir(), "connections.json")
	first := NewStore(connectionsPath)
	oldLayout := oneSSHLayout("connection-one")
	if err := first.PersistCurrent(oldLayout); err != nil {
		t.Fatal(err)
	}

	second := NewStore(connectionsPath)
	state := second.Load()
	if state.Warning != "" || state.Recovery == nil || !layoutsEqual(*state.Recovery, oldLayout) {
		t.Fatalf("startup recovery = %+v", state)
	}
	if err := second.PersistCurrent(oneSSHLayout("connection-two")); err != nil {
		t.Fatal(err)
	}
	third := NewStore(connectionsPath)
	state = third.Load()
	if state.Recovery == nil || !layoutsEqual(*state.Recovery, oldLayout) {
		t.Fatalf("unresolved recovery was replaced: %+v", state)
	}

	newLayout := oneSSHLayout("connection-two")
	if err := second.ResolveRecovery(newLayout); err != nil {
		t.Fatal(err)
	}
	if state := second.Load(); state.Recovery != nil {
		t.Fatalf("recovery was reoffered in the same process: %+v", state)
	}
	if err := second.PersistCurrent(oneSSHLayout("connection-three")); err != nil {
		t.Fatal(err)
	}
	restarted := NewStore(connectionsPath).Load()
	if restarted.Recovery == nil || !layoutsEqual(*restarted.Recovery, oneSSHLayout("connection-three")) {
		t.Fatalf("new checkpoint after resolve = %+v", restarted)
	}
}

func TestRecoveryRemainsPendingWhenDiskChangesBeforeResolve(t *testing.T) {
	connectionsPath := filepath.Join(t.TempDir(), "connections.json")
	initial := NewStore(connectionsPath)
	if err := initial.PersistCurrent(oneSSHLayout("saved-id")); err != nil {
		t.Fatal(err)
	}
	store := NewStore(connectionsPath)
	if state := store.Load(); state.Recovery == nil {
		t.Fatal("expected pending recovery")
	}
	recoveryPath := filepath.Join(filepath.Dir(connectionsPath), recoveryFileName)
	if err := os.Remove(recoveryPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(recoveryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.ResolveRecovery(emptyLayout()); !errors.Is(err, ErrRecoveryChanged) {
		t.Fatalf("ResolveRecovery error = %v, want ErrRecoveryChanged", err)
	}
	state := store.Load()
	if state.Recovery == nil || !layoutsEqual(*state.Recovery, oneSSHLayout("saved-id")) {
		t.Fatalf("failed resolution lost pending recovery: %+v", state)
	}
}

func TestRecoveryMalformedDataWarnsAndIsNotOverwritten(t *testing.T) {
	connectionsPath := filepath.Join(t.TempDir(), "connections.json")
	recoveryPath := filepath.Join(filepath.Dir(connectionsPath), recoveryFileName)
	bad := []byte(`{"version":99,"layout":{"tabs":[],"activeTab":-1}}`)
	if err := os.WriteFile(recoveryPath, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(connectionsPath)
	state := store.Load()
	if state.Warning == "" || state.Recovery != nil || !state.RecoveryUnavailable {
		t.Fatalf("Load() = %+v", state)
	}
	if err := store.PersistCurrent(emptyLayout()); err == nil {
		t.Fatal("PersistCurrent overwrote malformed recovery data")
	}
	got, err := os.ReadFile(recoveryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(bad) {
		t.Fatalf("malformed recovery changed: got %q, want %q", got, bad)
	}
	if err := store.ResolveRecovery(emptyLayout()); err != nil {
		t.Fatalf("explicitly discard malformed recovery: %v", err)
	}
	state = store.Load()
	if state.RecoveryUnavailable || state.Recovery != nil {
		t.Fatalf("recovery remained unavailable after discard: %+v", state)
	}
}

func TestRecoveryRejectsStaleSnapshotsAcrossStores(t *testing.T) {
	connectionsPath := filepath.Join(t.TempDir(), "connections.json")
	staleEmpty := NewStore(connectionsPath)
	if state := staleEmpty.Load(); state.Warning != "" || state.Recovery != nil {
		t.Fatalf("initial state = %+v", state)
	}
	writer := NewStore(connectionsPath)
	if err := writer.PersistCurrent(oneSSHLayout("new-checkpoint")); err != nil {
		t.Fatal(err)
	}
	if err := staleEmpty.PersistCurrent(oneSSHLayout("stale-checkpoint")); !errors.Is(err, ErrRecoveryChanged) {
		t.Fatalf("stale autosave error = %v, want ErrRecoveryChanged", err)
	}
	state := NewStore(connectionsPath).Load()
	if state.Recovery == nil || !layoutsEqual(*state.Recovery, oneSSHLayout("new-checkpoint")) {
		t.Fatalf("stale autosave replaced newer checkpoint: %+v", state)
	}

	first := NewStore(connectionsPath)
	second := NewStore(connectionsPath)
	if first.Load().Recovery == nil || second.Load().Recovery == nil {
		t.Fatal("both stores should have loaded the checkpoint")
	}
	if err := first.ResolveRecovery(oneSSHLayout("first-restoration")); err != nil {
		t.Fatal(err)
	}
	if err := second.ResolveRecovery(oneSSHLayout("stale-restoration")); !errors.Is(err, ErrRecoveryChanged) {
		t.Fatalf("stale resolution error = %v, want ErrRecoveryChanged", err)
	}
	state = NewStore(connectionsPath).Load()
	if state.Recovery == nil || !layoutsEqual(*state.Recovery, oneSSHLayout("first-restoration")) {
		t.Fatalf("stale resolution replaced newer checkpoint: %+v", state)
	}
}

func TestOversizedRecoveryCanBeExplicitlyDiscarded(t *testing.T) {
	connectionsPath := filepath.Join(t.TempDir(), "connections.json")
	recoveryPath := filepath.Join(filepath.Dir(connectionsPath), recoveryFileName)
	if err := os.WriteFile(recoveryPath, make([]byte, maxFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(connectionsPath)
	state := store.Load()
	if !state.RecoveryUnavailable || state.Recovery != nil {
		t.Fatalf("oversized recovery state = %+v", state)
	}
	if err := store.ResolveRecovery(emptyLayout()); err != nil {
		t.Fatalf("explicitly discard oversized recovery: %v", err)
	}
	state = NewStore(connectionsPath).Load()
	if state.RecoveryUnavailable || state.Recovery != nil {
		t.Fatalf("recovery remained unavailable after discard: %+v", state)
	}
}

func TestLayoutValidationBoundsAndKinds(t *testing.T) {
	tooManyTabs := Layout{ActiveTab: 0, Tabs: make([]Tab, maxTabs+1)}
	if _, err := normalizeLayout(tooManyTabs); err == nil {
		t.Fatal("accepted too many tabs")
	}
	badPane := oneSSHLayout(" ")
	if _, err := normalizeLayout(badPane); err == nil {
		t.Fatal("accepted an empty SSH connection ID")
	}
	badLocal := Layout{ActiveTab: 0, Tabs: []Tab{{SelectedPane: 0, Panes: []Pane{{Kind: "local", ConnectionID: "id"}}}}}
	if _, err := normalizeLayout(badLocal); err == nil {
		t.Fatal("accepted a connection ID on a local pane")
	}
	validUnavailable := Layout{ActiveTab: 0, Tabs: []Tab{{SelectedPane: 0, Panes: []Pane{{Kind: "unavailable"}}}}}
	if _, err := normalizeLayout(validUnavailable); err != nil {
		t.Fatalf("rejected an unavailable unsaved pane: %v", err)
	}
	invalidUnavailable := Layout{ActiveTab: 0, Tabs: []Tab{{SelectedPane: 0, Panes: []Pane{{Kind: "unavailable", ConnectionID: "removed-connection"}}}}}
	if _, err := normalizeLayout(invalidUnavailable); err == nil {
		t.Fatal("accepted a connection ID on an unavailable pane")
	}
	emptyTab := Layout{ActiveTab: 0, Tabs: []Tab{{SelectedPane: -1, Panes: []Pane{}}}}
	if _, err := normalizeLayout(emptyTab); err == nil {
		t.Fatal("accepted an empty tab")
	}
	if _, err := normalizeLayout(emptyLayout()); err != nil {
		t.Fatalf("rejected empty layout: %v", err)
	}
}

func TestEmptyRecoveryLayoutIsNotOfferedAfterRestart(t *testing.T) {
	connectionsPath := filepath.Join(t.TempDir(), "connections.json")
	if err := NewStore(connectionsPath).PersistCurrent(emptyLayout()); err != nil {
		t.Fatal(err)
	}
	state := NewStore(connectionsPath).Load()
	if state.Warning != "" || state.RecoveryUnavailable || state.Recovery != nil {
		t.Fatalf("empty recovery should not be offered: %+v", state)
	}
}

func TestStoredLayoutRequiresSelectionFields(t *testing.T) {
	connectionsPath := filepath.Join(t.TempDir(), "connections.json")
	workspacePath := filepath.Join(filepath.Dir(connectionsPath), workspaceFileName)
	bad := []byte(`{"version":1,"workspaces":[{"id":"id-1","name":"Incomplete","layout":{"tabs":[{"panes":[{"kind":"ssh","connectionId":"saved-id"}]}],"activeTab":0}}]}`)
	if err := os.WriteFile(workspacePath, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	state := NewStore(connectionsPath).Load()
	if state.Warning == "" || len(state.Workspaces) != 0 {
		t.Fatalf("Load() accepted a missing selectedPane: %+v", state)
	}
}

func TestConcurrentStoresDoNotLoseNamedUpdates(t *testing.T) {
	connectionsPath := filepath.Join(t.TempDir(), "connections.json")
	const workspaceCount = 12
	start := make(chan struct{})
	errors := make(chan error, workspaceCount)
	var writers sync.WaitGroup
	for i := 0; i < workspaceCount; i++ {
		writers.Add(1)
		go func(index int) {
			defer writers.Done()
			<-start
			_, err := NewStore(connectionsPath).Save(fmt.Sprintf("Workspace %02d", index), emptyLayout())
			errors <- err
		}(i)
	}
	close(start)
	writers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if state := NewStore(connectionsPath).Load(); len(state.Workspaces) != workspaceCount || state.Warning != "" {
		t.Fatalf("concurrent saves lost a workspace: %+v", state)
	}
}

func TestWorkspaceLimitAndCaseInsensitiveNames(t *testing.T) {
	connectionsPath := filepath.Join(t.TempDir(), "connections.json")
	store := NewStore(connectionsPath)
	if _, err := store.Save("Work", emptyLayout()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(" work ", emptyLayout()); err == nil {
		t.Fatal("accepted a duplicate workspace name")
	}
	for i := 1; i < maxWorkspaces; i++ {
		if _, err := store.Save(fmt.Sprintf("Work %03d", i), emptyLayout()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Save("Overflow", emptyLayout()); err == nil {
		t.Fatal("accepted more than the workspace limit")
	}
}

func emptyLayout() Layout {
	return Layout{ActiveTab: -1, Tabs: []Tab{}}
}

func oneSSHLayout(connectionID string) Layout {
	return Layout{ActiveTab: 0, Tabs: []Tab{{SelectedPane: 0, Panes: []Pane{{Kind: "ssh", ConnectionID: connectionID}}}}}
}

func layoutsEqual(left, right Layout) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return string(leftJSON) == string(rightJSON)
}

func sortedKeys(values map[string]any) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}
