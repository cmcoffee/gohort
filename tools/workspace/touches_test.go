package workspace

import (
	"strings"
	"testing"
)

// A write to a path is a prefix of every read of it; identical reads match.
func TestWorkspaceSaysWhatItTouches(t *testing.T) {
	cat, _ := wsRead("cat")(map[string]any{"action": "cat", "path": "./app/page.html"})
	grep, _ := wsRead("grep")(map[string]any{"action": "grep", "path": "app/page.html", "pattern": "x"})
	w, writes := wsWrite(map[string]any{"action": "edit", "path": "app/page.html"})
	if !writes || w != "file|app/page.html" {
		t.Fatalf("edit: %q %v", w, writes)
	}
	for _, r := range []string{cat, grep} {
		if !strings.HasPrefix(r, w+"|") {
			t.Errorf("a write to %q should make the read %q stale", w, r)
		}
	}
	if cat == grep {
		t.Error("a cat and a grep are different reads")
	}
	if again, _ := wsRead("cat")(map[string]any{"action": "cat", "path": "app/page.html"}); again != cat {
		t.Errorf("the same read should key the same: %q vs %q", again, cat)
	}
	if k, w := wsSwitch(nil); k != "file" || !w || !strings.HasPrefix(cat, k+"|") {
		t.Error("switching workspace should make every file read stale")
	}
}
