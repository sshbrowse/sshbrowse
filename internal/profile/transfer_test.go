package profile

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSnapshotPreservesConnectionData(t *testing.T) {
	incoming := Snapshot{
		Connections: []Connection{
			{
				ID: "hop", Folder: " Servers / Production ", Name: "jump, primary\nline", Host: "jump.example",
				Port: 2201, IdentityFile: "/home/test/.ssh/key", LocalForwards: []string{"8080:localhost:80", "local,forward"},
				RemoteForwards: []string{"9000:localhost:90"}, DynamicForwards: []string{"1080"},
				AgentForwarding: true, Provenance: &Provenance{SSHConfigAlias: "prod,alias"},
			},
			{ID: "app", Host: "app.example", JumpConnectionID: "hop", X11Forwarding: true},
		},
		Folders:    []string{"Empty", " Servers / Production "},
		Onboarding: OnboardingState{SSHConfigImportAnswered: true},
	}

	normalized, err := NormalizeSnapshot(incoming)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := normalized.Folders, []string{"Empty", "Servers", "Servers/Production"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("folders = %#v, want %#v", got, want)
	}
	if got, want := normalized.Connections[0].LocalForwards, []string{"8080:localhost:80", "local,forward"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("local forwards = %#v, want %#v", got, want)
	}
	if normalized.Connections[0].Provenance == nil || normalized.Connections[0].Provenance.SSHConfigAlias != "prod,alias" {
		t.Fatalf("provenance was not preserved: %#v", normalized.Connections[0].Provenance)
	}

	store := NewStore(filepath.Join(t.TempDir(), "connections.json"))
	current, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplySnapshot(normalized, true, SnapshotRevision(current), nil); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Snapshot()
	if err != nil || !reflect.DeepEqual(loaded, normalized) {
		t.Fatalf("saved snapshot differs: %v", err)
	}
}

func TestApplySnapshotMergeRelinksCollisionsAndIsIdempotent(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "connections.json"))
	if err := store.write(fileFormat{Connections: []Connection{{ID: "hop", Host: "local-hop", Name: "Local hop"}}, Folders: []string{}}); err != nil {
		t.Fatal(err)
	}
	current, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	incoming := Snapshot{
		Connections: []Connection{
			{ID: "main", Host: "app.example", JumpConnectionID: "hop"},
			{ID: "hop", Host: "imported-hop", Name: "Imported hop"},
		},
		Folders: []string{"Archive/Empty"},
	}

	result, err := store.ApplySnapshot(incoming, false, SnapshotRevision(current), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 2 || result.Skipped != 0 {
		t.Fatalf("result = %#v, want two additions", result)
	}
	connections, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]Connection, len(connections))
	for _, connection := range connections {
		byID[connection.ID] = connection
	}
	importedMain := byID["main"]
	if importedMain.JumpConnectionID == "" || importedMain.JumpConnectionID == "hop" {
		t.Fatalf("imported jump was not relinked away from local collision: %#v", importedMain)
	}
	importedHop, ok := byID[importedMain.JumpConnectionID]
	if !ok || importedHop.Host != "imported-hop" {
		t.Fatalf("imported jump points to wrong connection: %#v", importedHop)
	}

	current, err = store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	result, err = store.ApplySnapshot(incoming, false, SnapshotRevision(current), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped != 2 || result.Added != 0 {
		t.Fatalf("repeat import result = %#v, want two skips", result)
	}
	connectionsAgain, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(connectionsAgain) != len(connections) {
		t.Fatalf("repeat import grew profile from %d to %d connections", len(connections), len(connectionsAgain))
	}
	folders, err := store.Folders()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(folders, []string{"Archive", "Archive/Empty"}) {
		t.Fatalf("folders = %#v", folders)
	}
}

func TestApplySnapshotMergeKeepsDanglingJumpsDangling(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "connections.json"))
	if err := store.write(fileFormat{Connections: []Connection{{ID: "external-id", Host: "local.example"}}, Folders: []string{}}); err != nil {
		t.Fatal(err)
	}
	current, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	incoming := Snapshot{Connections: []Connection{{ID: "imported", Host: "remote.example", JumpConnectionID: "external-id"}}}
	if _, err := store.ApplySnapshot(incoming, false, SnapshotRevision(current), nil); err != nil {
		t.Fatal(err)
	}
	connections, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	var imported Connection
	for _, connection := range connections {
		if connection.ID == "imported" {
			imported = connection
		}
	}
	if imported.JumpConnectionID == "external-id" {
		t.Fatal("unresolved incoming jump bound to an unrelated local connection")
	}
	for _, connection := range connections {
		if connection.ID == imported.JumpConnectionID {
			t.Fatalf("rewritten missing jump ID unexpectedly exists: %q", imported.JumpConnectionID)
		}
	}
	if got := SnapshotWarnings(Snapshot{Connections: []Connection{{ID: "imported", Host: "remote.example", JumpConnectionID: "missing"}}}); len(got) == 0 {
		t.Fatal("missing jump warning was not returned")
	}
}

func TestMergeDoesNotBindExistingMissingJumps(t *testing.T) {
	incoming, err := NormalizeSnapshot(Snapshot{Connections: []Connection{
		{ID: "ghost", Host: "imported.invalid"},
		{ID: "dependent", Host: "dependent.invalid", JumpConnectionID: "ghost"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// A generated replacement ID must also avoid any missing local target.
	generated := transferID(SnapshotRevision(incoming), "ghost", 0)
	current, err := NormalizeSnapshot(Snapshot{Connections: []Connection{
		{ID: "local", Host: "local.invalid", JumpConnectionID: "ghost"},
		{ID: "other-local", Host: "other.invalid", JumpConnectionID: generated},
	}})
	if err != nil {
		t.Fatal(err)
	}
	merged, result, err := mergeSnapshots(current, incoming)
	if err != nil || result.Added != 2 {
		t.Fatalf("merge: %+v, %v", result, err)
	}
	if !reflect.DeepEqual(merged.Connections[:2], current.Connections) {
		t.Fatal("merge changed existing connections")
	}
	imported := merged.Connections[2]
	if imported.ID == "ghost" || imported.ID == generated || merged.Connections[3].JumpConnectionID != imported.ID {
		t.Fatal("import bound a missing local route or failed to relink the imported route")
	}
	again, result, err := mergeSnapshots(merged, incoming)
	if err != nil || result.Added != 0 || result.Skipped != 2 || !reflect.DeepEqual(again, merged) {
		t.Fatalf("re-import was not idempotent: %+v, %v", result, err)
	}
}

func TestApplySnapshotStaleOrBackupFailureDoesNotWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.json")
	store := NewStore(path)
	if _, err := store.Save(Connection{Host: "before.example"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	_, err = store.ApplySnapshot(Snapshot{Connections: []Connection{{ID: "new", Host: "new.example"}}}, true, "stale", func(Snapshot) error {
		called = true
		return nil
	})
	if err == nil || called {
		t.Fatalf("stale revision err=%v callbackCalled=%v", err, called)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("stale transfer changed the profile")
	}

	current, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	backupErr := errors.New("backup unavailable")
	_, err = store.ApplySnapshot(Snapshot{Connections: []Connection{{ID: "new", Host: "new.example"}}}, true, SnapshotRevision(current), func(Snapshot) error {
		return backupErr
	})
	if !errors.Is(err, backupErr) {
		t.Fatalf("backup failure err = %v", err)
	}
	after, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("backup callback failure changed the profile")
	}
}

func TestInvalidSnapshotDoesNotWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.json")
	store := NewStore(path)
	if _, err := store.Save(Connection{Host: "before.example"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	invalid := Snapshot{Connections: []Connection{{ID: "dup", Host: "one.example"}, {ID: "dup", Host: "two.example"}}}
	callbackCalled := false
	if _, err := store.ApplySnapshot(invalid, true, SnapshotRevision(current), func(Snapshot) error {
		callbackCalled = true
		return nil
	}); err == nil {
		t.Fatal("duplicate IDs were accepted")
	}
	if callbackCalled {
		t.Fatal("recovery callback ran for invalid snapshot")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("invalid snapshot changed the profile")
	}
}

func TestNormalizeSnapshotRejectsInvalidRoutesAndPorts(t *testing.T) {
	cases := []Snapshot{
		{Connections: []Connection{{ID: "a", Host: "a.example", Port: 65536}}},
		{Connections: []Connection{{ID: "a", Host: "a.example", JumpConnectionID: "b"}, {ID: "b", Host: "b.example", JumpConnectionID: "a"}}},
	}
	for i, snapshot := range cases {
		if _, err := NormalizeSnapshot(snapshot); err == nil {
			t.Fatalf("case %d was accepted", i)
		}
	}

	tooManyHops := Snapshot{Connections: make([]Connection, 9)}
	for i := range tooManyHops.Connections {
		tooManyHops.Connections[i] = Connection{ID: fmt.Sprintf("hop-%d", i), Host: "hop.example"}
		if i < len(tooManyHops.Connections)-1 {
			tooManyHops.Connections[i].JumpConnectionID = fmt.Sprintf("hop-%d", i+1)
		} else {
			tooManyHops.Connections[i].JumpConnectionID = "missing"
		}
	}
	if _, err := NormalizeSnapshot(tooManyHops); err == nil {
		t.Fatal("route with eight saved hops and a ninth dangling hop was accepted")
	}
}

func TestMergeRepeatedMissingJumpImportIsIdempotent(t *testing.T) {
	incoming, err := NormalizeSnapshot(Snapshot{Connections: []Connection{
		{ID: "app", Host: "app.invalid", JumpConnectionID: "missing-hop"},
		{ID: "other-app", Host: "other.invalid", JumpConnectionID: "missing-hop"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, collision := range []bool{false, true} {
		t.Run(fmt.Sprintf("collision=%t", collision), func(t *testing.T) {
			current := Snapshot{}
			if collision {
				current.Connections = []Connection{{ID: "app", Host: "local.invalid"}}
			}
			current, err := NormalizeSnapshot(current)
			if err != nil {
				t.Fatal(err)
			}
			merged, result, err := mergeSnapshots(current, incoming)
			if err != nil || result.Added != 2 {
				t.Fatalf("first import: %+v, %v", result, err)
			}
			for repeat := 0; repeat < 3; repeat++ {
				again, result, err := mergeSnapshots(merged, incoming)
				if err != nil || result.Added != 0 || result.Skipped != 2 || !reflect.DeepEqual(again, merged) {
					t.Fatalf("repeat %d changed the library: %+v, %v", repeat+1, result, err)
				}
			}
			jumpID := merged.Connections[len(current.Connections)].JumpConnectionID
			if jumpID == "" || merged.Connections[len(current.Connections)+1].JumpConnectionID != jumpID {
				t.Fatal("shared missing jump target was not preserved")
			}
			for _, connection := range merged.Connections {
				if connection.ID == jumpID {
					t.Fatal("missing jump target bound to a real connection")
				}
			}
		})
	}
}

func TestSnapshotTransfersHundredsOfConnectionsAndFolderOrder(t *testing.T) {
	incoming := Snapshot{Connections: make([]Connection, 500), Folders: make([]string, 50)}
	for i := range incoming.Folders {
		incoming.Folders[i] = fmt.Sprintf("Work/Group-%02d", i)
	}
	for i := range incoming.Connections {
		incoming.Connections[i] = Connection{ID: fmt.Sprintf("host-%03d", i), Host: "host.invalid", Folder: incoming.Folders[i%50]}
	}
	normalized, err := NormalizeSnapshot(incoming)
	if err != nil {
		t.Fatal(err)
	}
	wantFolders := append([]string{"Work"}, incoming.Folders...)
	if !reflect.DeepEqual(normalized.Folders, wantFolders) {
		t.Fatal("normalization changed folder order or repeated ancestors")
	}
	store := NewStore(filepath.Join(t.TempDir(), "connections.json"))
	current, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplySnapshot(normalized, true, SnapshotRevision(current), nil); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Snapshot()
	if err != nil || !reflect.DeepEqual(loaded, normalized) {
		t.Fatalf("transferred setup differs: %v", err)
	}
}
