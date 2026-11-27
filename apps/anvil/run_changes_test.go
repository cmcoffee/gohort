package anvil

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What a command changes is recorded like a file tool's write: a file clean
// before the command reverts to HEAD's content, one already changed reverts
// to what it was just before, a new file is removed and a deleted one comes
// back. Ignored output, and files the command did not touch, are left out;
// a file whose HEAD content may not be what was on disk is named as not
// recorded. Both where the project is its repository's root (git runs in
// the sandbox) and where it is a folder inside one (git runs on the server).
func TestARunsChangesAreRecordedAndRevert(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git is not installed")
	}
	for _, sub := range []string{"", "proj"} {
		repo := t.TempDir()
		initRepo(t, repo)
		base := filepath.Join(repo, sub)
		files := map[string]string{
			"a.txt":          "one\n",
			"b.txt":          "b\n",
			"e.txt":          "gone soon\n",
			"keep.txt":       "same\n",
			".gitignore":     "build/\n",
			".gitattributes": "*.lfs filter=lfs\n",
			"f.lfs":          "pointer\n",
		}
		for name, body := range files {
			mustWrite(t, filepath.Join(base, name), body)
		}
		commitAll(t, repo, "start")
		mustWrite(t, filepath.Join(base, "b.txt"), "b2\n")        // changed before the command
		mustWrite(t, filepath.Join(base, "c.txt"), "untracked\n") // there before, untouched

		c, _ := checkCtx(t, Project{Kind: KindClone})
		c.base = base
		run, _ := toolNamed(t, c, "run")
		out, err := run.Handler(context.Background(), map[string]any{"command": "printf 'two\\n' > a.txt; printf 'b3\\n' > b.txt; printf 'new\\n' > d.txt; rm e.txt; printf 'x\\n' > f.lfs; mkdir -p build && echo x > build/out"})
		if err != nil {
			t.Fatalf("%q: %v", sub, err)
		}
		for _, want := range []string{"a.txt (+1 -1)", "b.txt (+1 -1)", "d.txt (new, +1)", "e.txt (deleted)", "f.lfs (converted by a git filter"} {
			if !strings.Contains(out, want) {
				t.Errorf("%q: the result does not say %q:\n%s", sub, want, out)
			}
		}

		got := map[string]EditEntry{}
		for _, e := range listEdits(c.udb, c.project.ID, "s1") {
			got[e.Path] = e
		}
		if len(got) != 4 {
			t.Fatalf("%q: recorded %v", sub, editPaths(got))
		}
		if got["a.txt"].Before != "one\n" || got["b.txt"].Before != "b2\n" || got["d.txt"].Kind != EditCreate || got["e.txt"].Before != "gone soon\n" {
			t.Fatalf("%q: pre-images %+v", sub, got)
		}
		for _, e := range got {
			if err := revertEdit(c.udb, c.project, base, e); err != nil {
				t.Fatalf("%q: revert %s: %v", sub, e.Path, err)
			}
		}
		for name, want := range map[string]string{"a.txt": "one\n", "b.txt": "b2\n", "e.txt": "gone soon\n", "c.txt": "untracked\n"} {
			if b, _ := os.ReadFile(filepath.Join(base, name)); string(b) != want {
				t.Errorf("%q: %s after revert: %q", sub, name, b)
			}
		}
		if _, err := os.Stat(filepath.Join(base, "d.txt")); !os.IsNotExist(err) {
			t.Errorf("%q: the new file survived its revert", sub)
		}
	}
}

// A command that changes nothing says nothing more than its output, and a
// project outside git records nothing.
func TestARunThatChangesNothingSaysNothing(t *testing.T) {
	c, _ := checkCtx(t, Project{Kind: KindClone})
	mustWrite(t, filepath.Join(c.base, "a.txt"), "x\n")
	run, _ := toolNamed(t, c, "run")
	out, err := run.Handler(context.Background(), map[string]any{"command": "printf 'y\\n' > a.txt; echo done"})
	if err != nil || out != "done" {
		t.Fatalf("outside git: %q %v", out, err)
	}
	if n := len(listEdits(c.udb, c.project.ID, "")); n != 0 {
		t.Fatalf("outside git recorded %d", n)
	}
}

func editPaths(m map[string]EditEntry) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
