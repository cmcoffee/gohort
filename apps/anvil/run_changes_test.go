package anvil

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
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
			".gitattributes": "*.lfs filter=anvil-test\n", // no such filter, so git stores it as is everywhere
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

// runTestTune sets tunables for a test, as core/bgrun's tests do: values far
// below what the admin page allows, so a run detaches in a tenth of a second.
func runTestTune(t *testing.T, vals map[string]float64) {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	for k, v := range vals {
		db.Set(WebTable, k, v)
	}
	SetTunablesDB(db)
	t.Cleanup(func() { SetTunablesDB(nil) })
}

// A command still running at the detach time moves to the background: the
// turn gets the notice at once, and the output arrives later as the task's
// result, with what the command changed recorded. The turn being over by
// then, no card goes into it; and a file a later turn wrote while the
// command ran stays that turn's, not the command's.
func TestALongRunMovesToTheBackgroundAndStillRecordsItsChanges(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git is not installed")
	}
	runTestTune(t, map[string]float64{"tune_tool_detach_after": 0.1, "tune_tool_idle_limit": 60})
	delivered := make(chan TaskProduct, 1)
	prev := TaskRunnerFunc
	TaskRunnerFunc = func(sess *ToolSession, label string, fn func(ctx context.Context) (TaskProduct, error)) (TaskRun, error) {
		go func() {
			out, _ := fn(context.Background())
			delivered <- out
		}()
		return TaskRun{ID: "task-1", Label: label}, nil
	}
	t.Cleanup(func() { TaskRunnerFunc = prev })

	repo := t.TempDir()
	initRepo(t, repo)
	mustWrite(t, filepath.Join(repo, "a.txt"), "one\n")
	mustWrite(t, filepath.Join(repo, "b.txt"), "bee\n")
	commitAll(t, repo, "start")
	c, _ := checkCtx(t, Project{Kind: KindClone})
	c.base = repo
	cards := 0
	c.emit = func(string, EditEntry) { cards++ }
	run, _ := toolNamed(t, c, "run")

	turnCtx, endTurn := context.WithCancel(context.Background())
	sess := &ToolSession{Ctx: turnCtx, Username: "u", DeliverySessionID: "chat-1"}
	out, err := run.Handler(sess.ContextWithSession(turnCtx), map[string]any{"command": "sleep 1; printf 'two\\n' > a.txt; printf 'B\\n' > b.txt; echo built"})
	if err != nil {
		t.Fatal(err)
	}
	if notice, marked := TakeFrameworkResultMark(out); !marked || !strings.Contains(notice, "task-1") {
		t.Fatalf("the turn did not get the background notice: %q", out)
	}
	endTurn()

	// A later turn writes b.txt while the command is still running... and
	// the command then writes it too; the later write is the one recorded.
	later, _ := checkCtx(t, Project{ID: c.project.ID, Kind: KindClone})
	later.base, later.udb = repo, c.udb
	edit, _ := toolNamed(t, later, "edit_file")
	if _, err := edit.Handler(context.Background(), map[string]any{"path": "b.txt", "old_text": "bee", "new_text": "bees"}); err != nil {
		t.Fatal(err)
	}

	var got TaskProduct
	select {
	case got = <-delivered:
	case <-time.After(30 * time.Second):
		t.Fatal("the background run never delivered")
	}
	if !strings.Contains(got.Text, "built") || !strings.Contains(got.Text, "a.txt (+1 -1)") || strings.Contains(got.Text, "b.txt") {
		t.Fatalf("what the background run delivered:\n%s", got.Text)
	}
	if cards != 0 {
		t.Errorf("%d card(s) went into a turn that had ended", cards)
	}
	n := 0
	for _, e := range listEdits(c.udb, c.project.ID, "") {
		if e.Path == "a.txt" && e.Before == "one\n" && e.After == "two\n" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("a.txt recorded %d times", n)
	}
}
