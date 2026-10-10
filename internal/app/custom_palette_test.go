package app

import "testing"

func TestBackupCustomPaletteCompatibility(t *testing.T) {
	preferences := testBackupPreferences()
	if err := validateBackupPreferences(preferences); err != nil {
		t.Fatalf("legacy backup: %v", err)
	}
	preferences["sshbrowse.appearance.theme"] = "custom"
	if err := validateBackupPreferences(preferences); err == nil {
		t.Fatal("custom theme without palette accepted")
	}
	preferences[customPalettePreferenceKey] = `{"surface":"#112233","accent":"#AABBCC","terminal":"#000000"}`
	if err := validateBackupPreferences(preferences); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{`null`, `[]`, `{}`, `{"surface":"red","accent":"#123456","terminal":"#000000"}`, `{"surface":"#123456","accent":"#123456","terminal":"#000000","extra":"#000000"}`} {
		preferences[customPalettePreferenceKey] = value
		if err := validateBackupPreferences(preferences); err == nil {
			t.Fatalf("invalid palette accepted: %s", value)
		}
	}
}

func TestWindowsCustomChromeColours(t *testing.T) {
	data := map[string]any{"background": "#112233", "text": "#AABBCC", "inactiveText": "#778899", "border": "#445566"}
	colours, ok := windowsChromeColoursFor(data)
	if !ok || colours != (windowsChromeColours{0x332211, 0xccbbaa, 0x998877, 0x665544}) {
		t.Fatalf("custom chrome conversion: %#v, %v", colours, ok)
	}
	for _, value := range []any{"custom", nil, map[string]any{}, map[string]any{"background": "red", "text": "#ffffff", "inactiveText": "#aaaaaa", "border": "#000000"}, map[string]any{"background": 42, "text": "#ffffff", "inactiveText": "#aaaaaa", "border": "#000000"}} {
		if _, ok := windowsChromeColoursFor(value); ok {
			t.Fatalf("invalid chrome payload accepted: %#v", value)
		}
	}
}
