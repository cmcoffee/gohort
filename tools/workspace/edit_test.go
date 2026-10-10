package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/oddjob/core"
)

// edit changes text in place: one exact match, or every match with all; a
// find that matches nothing or several places is refused, saying so.
func TestEditChangesOneExactMatch(t *testing.T) {
	dir := t.TempDir()
	sess := &ToolSession{WorkspaceDir: dir, Username: "u"}
	p := filepath.Join(dir, "data", "now.py")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("a = d.get('max_temp')\nb = d.get('max_temp')\nc = 1\n"), 0o644)

	if _, err := handleEdit(map[string]any{"path": "data/now.py", "find": "c = 1", "replace": "c = 2"}, sess); err != nil {
		t.Fatal(err)
	}
	if _, err := handleEdit(map[string]any{"path": "data/now.py", "find": "d.get('max_temp')", "replace": "x"}, sess); err == nil || !strings.Contains(err.Error(), "2 times") {
		t.Fatalf("ambiguous find: %v", err)
	}
	if _, err := handleEdit(map[string]any{"path": "data/now.py", "find": "not there", "replace": "x"}, sess); err == nil || !strings.Contains(err.Error(), "does not appear") {
		t.Fatalf("missing find: %v", err)
	}
	if _, err := handleEdit(map[string]any{"path": "data/now.py", "find": "max_temp", "replace": "temp_high_f", "all": true}, sess); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "a = d.get('temp_high_f')\nb = d.get('temp_high_f')\nc = 2\n" {
		t.Fatalf("file = %q", b)
	}
	if _, err := handleEdit(map[string]any{"path": "../escape.py", "find": "x", "replace": "y"}, sess); err == nil {
		t.Fatal("an edit outside the workspace was allowed")
	}
}
