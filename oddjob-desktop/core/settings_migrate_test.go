package core

import (
	"os"
	"path/filepath"
	"testing"
)

// Settings saved under the app's old name are found: the directory is moved
// to the new name once, and left alone when the new one already exists.
func TestOldSettingsDirectoryIsMoved(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	t.Setenv("HOME", base)
	old := filepath.Join(base, LEGACY_SETTINGS_DIR_NAME)
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(old, "settings.json"), []byte("{}"), 0o644)
	dir, err := settings_dir()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(dir) != SETTINGS_DIR_NAME {
		t.Fatalf("settings dir = %s", dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "settings.json")); err != nil {
		t.Errorf("the old settings did not move: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("the old directory should be gone after the move")
	}
}
