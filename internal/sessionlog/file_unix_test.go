//go:build !windows

package sessionlog

import (
	"os"
	"testing"
)

func TestRecorderCreatesOwnerOnlyFile(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	recorder, err := New(Options{Directory: directory, Label: "private"})
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close("test"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(recorder.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("transcript mode = %04o, want 0600", got)
	}
	directoryInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if got := directoryInfo.Mode().Perm(); got != 0o755 {
		t.Fatalf("existing directory mode changed to %04o, want 0755", got)
	}
}
