package appscript

import (
	"os"
	"path/filepath"
	"testing"
)

// An app's libraries land in its own directory, not the workspace root where
// the owner's files are, and one the app no longer has is taken away.
func TestLibrariesDeployIntoTheirOwnDirectory(t *testing.T) {
	ws := t.TempDir()
	os.WriteFile(filepath.Join(ws, "engine.py"), []byte("# the owner's own file"), 0o644)
	rel, err := deployLibs(ws, "game", map[string]string{"engine": "def price(n):\n    return n\n", "rules": "MAX = 3\n"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(ws, rel, "engine.py")); string(got) != "def price(n):\n    return n\n" {
		t.Errorf("engine.py = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(ws, "engine.py")); string(got) != "# the owner's own file" {
		t.Errorf("the owner's engine.py was overwritten: %q", got)
	}
	if _, err := deployLibs(ws, "game", map[string]string{"engine": "def price(n):\n    return n * 2\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws, rel, "rules.py")); !os.IsNotExist(err) {
		t.Error("a library the app dropped is still importable")
	}
	if got, _ := os.ReadFile(filepath.Join(ws, rel, "engine.py")); string(got) != "def price(n):\n    return n * 2\n" {
		t.Errorf("an edited library was not redeployed: %q", got)
	}
	other, _ := deployLibs(ws, "other", map[string]string{"engine": "X = 1\n"})
	if other == rel {
		t.Error("two apps share one library directory")
	}
	if _, err := deployLibs(ws, "game", map[string]string{"../evil": "x"}); err == nil {
		t.Error("a name with a path in it was written")
	}
}
