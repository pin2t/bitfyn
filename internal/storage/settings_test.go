package storage

import "path/filepath"
import "testing"

// TestSettings checks that an unset setting reads empty, that a value is
// stored and replaced, and that an empty value removes it.
func TestSettings(t *testing.T) {
	var s, err = Open(filepath.Join(t.TempDir(), "w.db"), "")
	if err != nil { t.Fatalf("Open: %v", err) }
	defer s.Close()
	var check = func(want string) {
		t.Helper()
		var got, err = s.Setting(SettingPinnedPeer)
		if err != nil || got != want {
			t.Fatalf("Setting = %q, %v; want %q", got, err, want)
		}
	}
	check("")
	for _, value := range []string{"192.0.2.1:8333", "[2001:db8::1]:8333"} {
		if err := s.SetSetting(SettingPinnedPeer, value); err != nil {
			t.Fatalf("SetSetting(%q): %v", value, err)
		}
		check(value)
	}
	if err := s.SetSetting(SettingPinnedPeer, ""); err != nil {
		t.Fatalf("SetSetting(empty): %v", err)
	}
	check("")
}
