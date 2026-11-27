package anvil

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An anchor that differs from the file only in what the model cannot see or
// does not reproduce lands anyway, and says how loosely it was read: CRLF
// line endings (which the new text takes on), curly quotes and dashes, spaces
// at line ends. Leading indentation is never loosened.
func TestALooseAnchorLandsAndSaysHow(t *testing.T) {
	cases := []struct {
		name, file, old, new, want string
		tier                       matchTier
	}{
		{"exact", "a\nb\nc\n", "b\n", "B\n", "a\nB\nc\n", matchExact},
		{"crlf", "a\r\nb\r\nc\r\n", "a\nb\n", "x\ny\n", "x\r\ny\r\nc\r\n", matchLineEndings},
		{"curly quotes in the file", "say(\u201Chi\u201D) \u2013 ok\n", `say("hi") - ok`, `say("bye")`, "say(\"bye\")\n", matchTypography},
		{"curly quotes in the anchor", "it's here\n", "it\u2019s here", "it is here", "it is here\n", matchTypography},
		{"trailing spaces", "x := 1   \ny := 2\t\nz := 3\n", "x := 1\ny := 2\n", "x, y := 1, 2\n", "x, y := 1, 2\nz := 3\n", matchTrailingSpace},
		{"trailing spaces with crlf", "x := 1  \r\ny := 2\r\n", "x := 1\ny := 2", "x := 9\ny := 8", "x := 9\r\ny := 8\r\n", matchTrailingSpace},
	}
	for _, tc := range cases {
		m := findAnchor(tc.file, tc.old, tc.new)
		if len(m.spans) != 1 {
			t.Errorf("%s: %d matches", tc.name, len(m.spans))
			continue
		}
		if m.tier != tc.tier {
			t.Errorf("%s: tier %d, want %d", tc.name, m.tier, tc.tier)
		}
		if got := m.apply(tc.file); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
		if (m.tier == matchExact) != (m.tier.note() == "") {
			t.Errorf("%s: note %q", tc.name, m.tier.note())
		}
	}

	for _, miss := range []struct{ file, old string }{
		{"def f():\n    return 1\n", "def f():\n  return 1\n"}, // indentation is code
		{"a\nb\n", "b\n\n"}, // no break after the last line
		{"abc\n", "zzz"},
	} {
		if m := findAnchor(miss.file, miss.old, "x"); len(m.spans) != 0 {
			t.Errorf("%q matched in %q", miss.old, miss.file)
		}
	}
}

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
