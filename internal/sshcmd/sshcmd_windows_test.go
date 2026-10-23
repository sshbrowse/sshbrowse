//go:build windows

package sshcmd

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsMissingOpenSSHClientErrorNamesTheRequiredFeature(t *testing.T) {
	path := filepath.Join(t.TempDir(), "OpenSSH Client", "ssh.exe")
	err := requireWindowsClient(path, "ssh")
	if err == nil || !strings.Contains(err.Error(), "Windows OpenSSH ssh client") || !strings.Contains(err.Error(), "optional feature") {
		t.Fatalf("missing client error = %v", err)
	}
}
