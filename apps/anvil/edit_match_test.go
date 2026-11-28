package anvil

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Through the tool: an ambiguous anchor is refused unless replace_all, which
// changes every one and says how many; a loose match says so; a miss whose
// new text is already there says the edit may have been made.
func TestEditFileReplaceAllAndLooseMatches(t *testing.T) {
	c, _ := checkCtx(t, Project{})
	edit, _ := toolNamed(t, c, "edit_file")
	path := filepath.Join(c.base, "a.go")
	mustWrite(t, path, "old()\nold()\nfmt.Println(\u201Chi\u201D)   \n")
	call := func(args map[string]any) (string, error) {
		args["path"] = "a.go"
		return edit.Handler(context.Background(), args)
	}

	if _, err := call(map[string]any{"old_text": "old()", "new_text": "renamed()"}); err == nil || !strings.Contains(err.Error(), "replace_all") {
		t.Fatalf("an ambiguous anchor: %v", err)
	}
	out, err := call(map[string]any{"old_text": "old()", "new_text": "renamed()", "replace_all": true})
	if err != nil || !strings.Contains(out, "2 places") {
		t.Fatalf("replace_all: %q %v", out, err)
	}
	out, err = call(map[string]any{"old_text": `fmt.Println("hi")`, "new_text": `fmt.Println("hello")`})
	if err != nil || !strings.Contains(out, "curly quotes") {
		t.Fatalf("a typographic match: %q %v", out, err)
	}
	b, _ := os.ReadFile(path)
	if want := "renamed()\nrenamed()\nfmt.Println(\"hello\")   \n"; string(b) != want {
		t.Fatalf("the file: %q", b)
	}
	_, err = call(map[string]any{"old_text": "something not there", "new_text": "renamed()\nrenamed()\nfmt.Println"})
	if err == nil || !strings.Contains(err.Error(), "already there") {
		t.Fatalf("an edit made already: %v", err)
	}
}
