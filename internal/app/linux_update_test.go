//go:build linux

package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/github"
)

func TestLinuxUserInstallTargetRequiresDefaultNonSymlinkPath(t *testing.T) {
	home, target := makeLinuxInstall(t)
	if got, err := linuxUserInstallTarget(home, target, uint32(os.Geteuid())); err != nil || got != target {
		t.Fatalf("target = %q, %v; want %q", got, err, target)
	}
	if _, err := linuxUserInstallTarget(home, "/usr/bin/sshbrowse", uint32(os.Geteuid())); err == nil {
		t.Fatal("system path was eligible for replacement")
	}
	other := filepath.Join(filepath.Dir(target), "real-sshbrowse")
	writeLinuxFile(t, other, "#!/bin/sh\nexit 0\n", 0o755)
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, target); err != nil {
		t.Fatal(err)
	}
	if _, err := linuxUserInstallTarget(home, target, uint32(os.Geteuid())); err == nil {
		t.Fatal("symlink executable was eligible for replacement")
	}
}

func TestLinuxReleaseAssetMatcherRequiresRawAMD64Binary(t *testing.T) {
	assets := []github.ReleaseAsset{
		{Name: "sshbrowse_2.0.0_amd64.deb"},
		{Name: "SSHBrowse-Linux-x86_64.tar.gz"},
		{Name: "SSHBrowse-Linux-x86_64"},
	}
	if got := releaseAssetMatcher(updater.CheckRequest{Platform: "linux", Arch: "amd64"}, assets); got != 2 {
		t.Fatalf("Linux update asset index = %d, want 2", got)
	}
	if got := releaseAssetMatcher(updater.CheckRequest{Platform: "linux", Arch: "arm64"}, assets); got != -1 {
		t.Fatalf("Linux ARM64 update asset index = %d, want -1", got)
	}
}

func TestPrepareLinuxUpdateFailureKeepsDownloadAndCleansStage(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("in-app updates are disabled for root")
	}
	home, target := makeLinuxInstall(t)
	t.Setenv("HOME", home)
	download := filepath.Join(t.TempDir(), "bad-update")
	writeLinuxFile(t, download, "#!/bin/sh\nexit 1\n", 0o755)
	if _, err := prepareLinuxUpdate(context.Background(), target, download); err == nil || !strings.Contains(err.Error(), "preflight") {
		t.Fatalf("prepare error = %v, want preflight failure", err)
	}
	if _, err := os.Stat(download); err != nil {
		t.Fatalf("verified download was removed after failed preparation: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil || len(entries) != 1 || entries[0].Name() != "sshbrowse" {
		t.Fatalf("installation directory after failed preparation = %v, %v", entries, err)
	}
}

func TestFinishLinuxUpdatePreservesTargetWhenAtomicReplaceFails(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "sshbrowse")
	writeLinuxFile(t, target, "#!/bin/sh\nprintf old > '"+filepath.Join(directory, "launched")+"'\n", 0o755)
	stage, err := os.MkdirTemp(directory, ".sshbrowse-update-*")
	if err != nil {
		t.Fatal(err)
	}
	prepared := preparedLinuxUpdate{target: target, candidate: filepath.Join(stage, "missing"), recovery: filepath.Join(stage, "recovery"), directory: stage}
	if err := os.Link(target, prepared.recovery); err != nil {
		t.Fatal(err)
	}
	if err := finishLinuxUpdate(prepared); err == nil {
		t.Fatal("missing candidate unexpectedly replaced target")
	}
	data, err := os.ReadFile(target)
	if err != nil || !strings.Contains(string(data), "printf old") {
		t.Fatalf("target after failed rename = %q, %v", data, err)
	}
	waitForFileContents(t, filepath.Join(directory, "launched"), "old")
}

func TestFinishLinuxUpdateRollsBackAndRelaunchesOldBinary(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "sshbrowse")
	stage, err := os.MkdirTemp(directory, ".sshbrowse-update-*")
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(directory, "launched")
	writeLinuxFile(t, target, "#!/bin/sh\nprintf old > '"+marker+"'\n", 0o755)
	prepared := preparedLinuxUpdate{target: target, candidate: filepath.Join(stage, "candidate"), recovery: filepath.Join(stage, "recovery"), directory: stage}
	writeLinuxFile(t, prepared.candidate, "not an executable format", 0o755)
	if err := os.Link(target, prepared.recovery); err != nil {
		t.Fatal(err)
	}
	if err := finishLinuxUpdate(prepared); err == nil {
		t.Fatal("invalid candidate launch unexpectedly succeeded")
	}
	data, err := os.ReadFile(target)
	if err != nil || !strings.Contains(string(data), "printf old") {
		t.Fatalf("target after rollback = %q, %v", data, err)
	}
	waitForFileContents(t, marker, "old")
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage directory remains after rollback: %v", err)
	}
}

func makeLinuxInstall(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	target := filepath.Join(home, ".local", "bin", "sshbrowse")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	writeLinuxFile(t, target, "#!/bin/sh\nexit 0\n", 0o755)
	return home, target
}

func writeLinuxFile(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}

func waitForFileContents(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && string(data) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("file %q did not contain %q", path, want)
}
