package app

import (
	"encoding/json"
	"fmt"
	"strconv"

	"sshbrowse/internal/profile"
)

// A backup carries only portable user preferences. Update throttling and
// arbitrary WebView storage are deliberately outside this format.
var backupPreferenceRules = map[string]func(string) bool{
	"sshbrowse.sidebar.width":                  integerPreference(180, 360),
	"sshbrowse.sidebar.visible":                booleanPreference,
	"sshbrowse.tiling.enabled":                 booleanPreference,
	"sshbrowse.terminal.rightClickToPaste":     booleanPreference,
	"sshbrowse.terminal.copyOnSelection":       booleanPreference,
	"sshbrowse.updates.checkOnStartup":         booleanPreference,
	"sshbrowse.terminal.scrollbackLines":       integerPreference(0, 50000),
	"sshbrowse.sidebar.collapsed":              collapsedFoldersPreference,
	"sshbrowse.terminal.pasteWarningsDisabled": booleanPreference,
	"sshbrowse.appearance.theme":               enumPreference("warm", "classic", "moss", "fjord", "oled", "contrast"),
	"sshbrowse.appearance.terminalColors":      enumPreference("follow", "neutral"),
	"sshbrowse.appearance.uiSize":              enumPreference("standard", "large"),
	"sshbrowse.appearance.terminalFontSize":    integerPreference(8, 32),
	"sshbrowse.interface.scale":                scalePreference,
	"sshbrowse.appearance.terminalFontName":    enumPreference("system", "jetbrains", "menlo", "consolas", "dejavu"),
}

func validateBackupPreferences(preferences map[string]string) error {
	if len(preferences) != len(backupPreferenceRules) {
		return fmt.Errorf("backup must contain all %d portable preferences", len(backupPreferenceRules))
	}
	for key, value := range preferences {
		valid, ok := backupPreferenceRules[key]
		if !ok {
			return fmt.Errorf("unsupported backup preference %q", key)
		}
		if len(value) > 64*1024 || !valid(value) {
			return fmt.Errorf("invalid backup preference %q", key)
		}
	}
	return nil
}

func booleanPreference(value string) bool { return value == "true" || value == "false" }

func integerPreference(minimum, maximum int) func(string) bool {
	return func(value string) bool {
		parsed, err := strconv.Atoi(value)
		return err == nil && parsed >= minimum && parsed <= maximum && strconv.Itoa(parsed) == value
	}
}

func enumPreference(values ...string) func(string) bool {
	return func(value string) bool {
		for _, allowed := range values {
			if value == allowed {
				return true
			}
		}
		return false
	}
}

func scalePreference(value string) bool {
	return enumPreference("0.8", "0.9", "1", "1.0", "1.1", "1.2", "1.3", "1.4", "1.5", "1.6")(value)
}

func collapsedFoldersPreference(value string) bool {
	var paths []string
	if json.Unmarshal([]byte(value), &paths) != nil || paths == nil {
		return false
	}
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		normalized, err := profile.NormalizeFolderPath(path)
		if err != nil || normalized == "" || normalized != path || seen[path] {
			return false
		}
		seen[path] = true
	}
	return true
}
