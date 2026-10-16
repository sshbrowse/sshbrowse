package workspaces

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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

	workspacePath := filepath.Join(filepath.Dir(connectionsPath), workspaceFileName)
	info, err := os.Stat(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	// Windows does not expose Unix permission bits through os.FileMode.
	if runtime.GOOS != "windows" {
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("workspace file mode = %04o, want 0600", got)
		}
	}
	data, err := os.ReadFile(workspacePath)
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

	renamed, err := store.Rename(created.ID, "Renamed")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.ID != created.ID || renamed.Name != "Renamed" {
		t.Fatalf("renamed workspace = %+v", renamed)
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
		{name: "missing selection", data: []byte(`{"version":1,"workspaces":[{"id":"id-1","name":"bad","layout":{"tabs":[{"panes":[{"kind":"ssh","connectionId":"saved-id"}]}],"activeTab":0}}]}`)},
		{name: "null connection ID", data: []byte(`{"version":1,"workspaces":[{"id":"id-1","name":"bad","layout":{"tabs":[{"panes":[{"kind":"local","connectionId":null}],"selectedPane":0}],"activeTab":0}}]}`)},
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

func TestOversizedWorkspaceDataWarnsAndIsNotOverwritten(t *testing.T) {
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

func TestStoredWorkspaceRetainsConnectionIDsWithoutConnectionLookup(t *testing.T) {
	connectionsPath := filepath.Join(t.TempDir(), "connections.json")
	store := NewStore(connectionsPath)
	created, err := store.Save("Unavailable connection", oneSSHLayout("missing-connection-id"))
	if err != nil {
		t.Fatal(err)
	}
	state := NewStore(connectionsPath).Load()
	if state.Warning != "" || len(state.Workspaces) != 1 {
		t.Fatalf("Load() = %+v", state)
	}
	if got := state.Workspaces[0].Layout.Tabs[0].Panes[0].ConnectionID; got != "missing-connection-id" {
		t.Fatalf("connection ID = %q", got)
	}
	if state.Workspaces[0].ID != created.ID {
		t.Fatalf("workspace ID changed across reload: %q, want %q", state.Workspaces[0].ID, created.ID)
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

func TestConcurrentStoresDoNotLoseNamedUpdates(t *testing.T) {
	connectionsPath := filepath.Join(t.TempDir(), "connections.json")
	const workspaceCount = 12
	start := make(chan struct{})
	errCh := make(chan error, workspaceCount)
	var writers sync.WaitGroup
	for i := 0; i < workspaceCount; i++ {
		writers.Add(1)
		go func(index int) {
			defer writers.Done()
			<-start
			_, err := NewStore(connectionsPath).Save(fmt.Sprintf("Workspace %02d", index), emptyLayout())
			errCh <- err
		}(i)
	}
	close(start)
	writers.Wait()
	close(errCh)
	for err := range errCh {
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

func sortedKeys(values map[string]any) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}
