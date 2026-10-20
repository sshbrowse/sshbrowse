package app

import (
	"runtime"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func TestLinuxAndWindowsUseFrontendMenuAndRegisterContextMenus(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS uses its native application menu")
	}

	wailsApp := application.New(application.Options{Name: "SSHBrowse"})
	menu := BuildMenu(wailsApp)
	if menu != nil {
		t.Fatal("Linux and Windows should render the application menu in Svelte")
	}

	for _, name := range []string{
		"saved-connection",
		"saved-folder",
		"saved-folder-empty",
		"saved-sidebar",
		"terminal",
		"session-recording-start",
		"session-recording-stop",
	} {
		if _, ok := wailsApp.ContextMenu.Get(name); !ok {
			t.Fatalf("context menu %q is not registered", name)
		}
	}
	terminalMenu, ok := wailsApp.ContextMenu.Get("terminal")
	if !ok {
		t.Fatal("terminal context menu is not registered")
	}
	for _, label := range []string{"Copy Selection", "Paste Clipboard", "Find in Terminal…"} {
		if terminalMenu.FindByLabel(label) == nil {
			t.Fatalf("terminal context menu item %q is missing", label)
		}
	}
	for _, test := range []struct {
		menu  string
		label string
	}{
		{menu: "saved-connection", label: "Connect"},
		{menu: "saved-connection", label: "Connect with SFTP"},
		{menu: "saved-connection", label: "Edit…"},
		{menu: "saved-connection", label: "Duplicate…"},
		{menu: "saved-connection", label: "New Folder…"},
		{menu: "saved-connection", label: "Move to Folder…"},
		{menu: "saved-connection", label: "Delete…"},
		{menu: "saved-folder", label: "Connect all"},
		{menu: "saved-folder", label: "New connection…"},
		{menu: "saved-folder", label: "New Subfolder…"},
		{menu: "saved-folder", label: "Rename…"},
		{menu: "saved-folder", label: "Delete"},
		{menu: "saved-folder-empty", label: "New connection…"},
		{menu: "saved-folder-empty", label: "New Subfolder…"},
		{menu: "saved-folder-empty", label: "Rename…"},
		{menu: "saved-folder-empty", label: "Delete"},
		{menu: "saved-sidebar", label: "New Folder…"},
	} {
		contextMenu, ok := wailsApp.ContextMenu.Get(test.menu)
		if !ok || contextMenu.FindByLabel(test.label) == nil {
			t.Fatalf("context menu %q is missing item %q", test.menu, test.label)
		}
	}
	emptyFolderMenu, _ := wailsApp.ContextMenu.Get("saved-folder-empty")
	for _, label := range []string{"Connect", "Connect all"} {
		if emptyFolderMenu.FindByLabel(label) != nil {
			t.Fatalf("empty folder menu must hide %q", label)
		}
	}
}

func TestMacOSKeepsNativeEditRolesAndAddsSettings(t *testing.T) {
	wailsApp := application.New(application.Options{Name: "SSHBrowse"})
	menu := buildNativeMenu(wailsApp, nil)
	for _, role := range []application.Role{
		application.Undo,
		application.Redo,
		application.Cut,
		application.Copy,
		application.Paste,
		application.SelectAll,
	} {
		if menu.FindByRole(role) == nil {
			t.Errorf("macOS native Edit role %v is missing", role)
		}
	}
	appMenu := menu.FindByLabel("SSHBrowse")
	if appMenu == nil || appMenu.GetSubmenu() == nil {
		t.Fatal("macOS application menu is missing")
	}
	settings := appMenu.GetSubmenu().FindByLabel("Settings…")
	if settings == nil {
		t.Fatal("macOS application menu is missing Settings…")
	}
	shortcut := "Ctrl+,"
	if runtime.GOOS == "darwin" {
		shortcut = "Cmd+,"
	}
	if settings.GetAccelerator() != shortcut {
		t.Fatalf("Settings shortcut = %q, want %q", settings.GetAccelerator(), shortcut)
	}
	if menu.FindByLabel("Settings") != nil {
		t.Fatal("macOS should not have a top-level Settings menu")
	}
	find := menu.FindByLabel("Find in Terminal…")
	if find == nil {
		t.Fatal("macOS Edit menu is missing Find in Terminal…")
	}
	findShortcut := "Ctrl+F"
	if runtime.GOOS == "darwin" {
		findShortcut = "Cmd+F"
	}
	if find.GetAccelerator() != findShortcut {
		t.Fatalf("Find shortcut = %q, want %q", find.GetAccelerator(), findShortcut)
	}

	shellItem := menu.FindByLabel("Shell")
	if shellItem == nil || shellItem.GetSubmenu() == nil {
		t.Fatal("macOS native menu is missing Shell")
	}
	shell := shellItem.GetSubmenu()
	startRecording := shell.FindByLabel("Start Recording")
	stopRecording := shell.FindByLabel("Stop Recording")
	closeTab := shell.FindByLabel("Close Tab")
	if startRecording == nil || stopRecording == nil || closeTab == nil {
		t.Fatal("Shell menu is missing recording actions or Close Tab")
	}
	if startRecording.Enabled() || stopRecording.Enabled() {
		t.Fatal("recording menu actions should initially be disabled")
	}
	startIndex, stopIndex, closeIndex := -1, -1, -1
	for i := 0; ; i++ {
		item := shell.ItemAt(i)
		if item == nil {
			break
		}
		switch item.Label() {
		case "Start Recording":
			startIndex = i
		case "Stop Recording":
			stopIndex = i
		case "Close Tab":
			closeIndex = i
		}
	}
	if !(startIndex >= 0 && startIndex < stopIndex && stopIndex < closeIndex) {
		t.Fatalf("Shell menu order = Start %d, Stop %d, Close %d; want Start < Stop < Close", startIndex, stopIndex, closeIndex)
	}
	if EventMenuStartRecording != "menu:startRecording" || EventMenuStopRecording != "menu:stopRecording" {
		t.Fatal("recording menu action event names changed")
	}
	if !setRecordingMenuAvailability(startRecording, stopRecording, map[string]any{
		"canStart": true,
		"canStop":  false,
	}) {
		t.Fatal("valid recording availability payload was rejected")
	}
	if !startRecording.Enabled() || stopRecording.Enabled() {
		t.Fatal("recording menu availability did not update menu item states")
	}
	if setRecordingMenuAvailability(startRecording, stopRecording, map[string]any{"canStart": false}) {
		t.Fatal("incomplete recording availability payload was accepted")
	}
	if !startRecording.Enabled() || stopRecording.Enabled() {
		t.Fatal("invalid availability payload changed menu item states")
	}
}

func TestRecordingContextMenusHaveOneActionEach(t *testing.T) {
	wailsApp := application.New(application.Options{Name: "SSHBrowse"})
	registerContextMenus(wailsApp)

	for _, test := range []struct {
		name  string
		label string
	}{
		{name: "session-recording-start", label: "Start Recording"},
		{name: "session-recording-stop", label: "Stop Recording"},
	} {
		menu, ok := wailsApp.ContextMenu.Get(test.name)
		if !ok {
			t.Fatalf("recording context menu %q is not registered", test.name)
		}
		if menu.ItemAt(0) == nil || menu.ItemAt(0).Label() != test.label || menu.ItemAt(1) != nil {
			t.Fatalf("context menu %q should contain only %q", test.name, test.label)
		}
	}
}
