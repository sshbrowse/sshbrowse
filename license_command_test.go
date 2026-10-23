package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestWriteLicenseDocuments(t *testing.T) {
	var output bytes.Buffer
	handled, err := writeLicenseDocuments([]string{"sshbrowse", "--licenses"}, &output)
	if err != nil {
		t.Fatalf("writeLicenseDocuments: %v", err)
	}
	if !handled {
		t.Fatal("--licenses was not handled")
	}
	license, err := os.ReadFile("LICENSE")
	if err != nil {
		t.Fatal(err)
	}
	thirdPartyNotices, err := os.ReadFile("docs/legal/THIRD_PARTY_NOTICES.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("===== LICENSE =====\n%s\n===== THIRD_PARTY_NOTICES.txt =====\n%s\n", license, thirdPartyNotices)
	if output.String() != want {
		t.Error("license output must contain the complete license followed by the complete third-party notices")
	}
	for _, marker := range []string{
		"## github.com/wailsapp/wails/v3",
		"## @xterm/xterm",
	} {
		if !strings.Contains(output.String(), marker) {
			t.Errorf("license output missing %q", marker)
		}
	}
}
