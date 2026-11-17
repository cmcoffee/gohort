package anvil

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/sandbox"
	"github.com/cmcoffee/snugforge/kvlite"
)

// evalWorkspace gives the tasks a workspaces folder, which is where they run
// and where the package cache lives.
func evalWorkspace(t *testing.T) {
	t.Helper()
	prev := WorkspacesDir()
	dir := t.TempDir()
	SetWorkspacesDir(dir)
	t.Cleanup(func() {
		SetWorkspacesDir(prev)
		removeAllWritable(dir)
	})
}

//
// Asked in the sandbox the tasks run in, not of the host: a CI runner had
// /usr/bin/go, the sandbox could not run it, and the task's check failed
// with "go: not found" instead of the test skipping.
func needToolchains(t *testing.T, langs ...string) {
	t.Helper()
	probes := map[string]string{"Go": "go version", "Python": "python3 --version", "JavaScript": "node --version"}
	for _, l := range langs {
		c := &toolCtx{project: Project{ID: "eval-toolchain"}, base: t.TempDir()}
		if res := c.sandboxed(context.Background(), probes[l], 30*time.Second); res.Err != nil || res.TimedOut {
			t.Skipf("%s does not run in the sandbox here: %s", l, strings.TrimSpace(res.Output))
		}
	}
}

// hiddenPasses runs a task's hidden check over the given parts, in order.
func hiddenPasses(t *testing.T, task evalTask, parts ...string) (bool, string) {
	t.Helper()
	dir := t.TempDir()
	for _, part := range parts {
		if err := writeEvalFiles(task.Name, part, dir); err != nil {
			t.Fatal(err)
		}
	}
	c := &toolCtx{project: Project{ID: "eval-validate"}, base: dir}
	res := c.sandboxed(context.Background(), task.HiddenCheck, evalHiddenLimit)
	return res.Err == nil && !res.TimedOut, res.Output
}

// Every task's hidden check fails on the tree the agent starts from and passes
// on the reference solution: otherwise a task scores something other than
// whether the work was done.
func TestEveryTaskFailsAtTheStartAndPassesWithItsSolution(t *testing.T) {
	if testing.Short() {
		t.Skip("builds every task")
	}
	needToolchains(t, "Go", "Python", "JavaScript")
	if !sandbox.ScopesReads() {
		t.Skip("no confining sandbox on this host")
	}
	evalWorkspace(t)
	tasks, err := loadEvalTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) < 10 {
		t.Fatalf("%d tasks", len(tasks))
	}
	for _, task := range tasks {
		if task.Title == "" || task.Request == "" || task.Check == "" || task.HiddenCheck == "" {
			t.Errorf("%s is missing a field: %+v", task.Name, task)
			continue
		}
		if ok, out := hiddenPasses(t, task, "repo", "hidden"); ok {
			t.Errorf("%s: the hidden check passes before any work:\n%s", task.Name, out)
		}
		if ok, out := hiddenPasses(t, task, "repo", "solution", "hidden"); !ok {
			t.Errorf("%s: the hidden check fails on the solution:\n%s", task.Name, out)
		}
	}
}

// evalApp is an Anvil with a store, for runs.
func evalApp(t *testing.T) *Anvil {
	t.Helper()
	T := &Anvil{}
	T.DB = &DBase{Store: kvlite.MemStore()}
	return T
}

// waitEval waits for the run under setting to report how it ended.
func waitEval(t *testing.T, T *Anvil, setting string) string {
	t.Helper()
	st := T.evalSt()
	for i := 0; i < 1200; i++ {
		st.mu.Lock()
		running, outcome, got := st.running, st.outcome, st.setting
		st.mu.Unlock()
		if !running && outcome != "" && got == setting {
			return outcome
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the run never ended")
	return ""
}

// A run takes every task in a fresh copy, scores each by its hidden check, and
// stores the run: here a runner that solves the Python and JavaScript tasks
// and leaves the Go ones alone.
func TestARunScoresEveryTaskAndStoresIt(t *testing.T) {
	needToolchains(t, "Go", "Python", "JavaScript")
	evalWorkspace(t)
	T := evalApp(t)
	solves := func(task evalTask) bool { return task.Lang != "Go" }
	run := func(ctx context.Context, c *toolCtx, task evalTask, s evalSetting) (string, int, int, string, error) {
		if c.project.CheckCommand != task.Check {
			t.Errorf("%s: the project's check is %q", task.Name, c.project.CheckCommand)
		}
		if solves(task) {
			if err := writeEvalFiles(task.Name, "solution", c.base); err != nil {
				return "", 0, 0, "", err
			}
			return "Done.", 2, 100, "worker", nil
		}
		return "Could not do it.", 1, 50, "worker", nil
	}
	s, _ := evalSettingByID("worker-check")
	if _, err := T.startEval("root", s, run); err != nil {
		t.Fatal(err)
	}
	outcome := waitEval(t, T, s.ID)
	runs := T.evalRuns()
	if len(runs) != 1 {
		t.Fatalf("%d runs stored", len(runs))
	}
	r := runs[0]
	want := 0
	tasks, _ := loadEvalTasks()
	for _, task := range tasks {
		if solves(task) {
			want++
		}
	}
	if r.Total != len(tasks) || len(r.Results) != len(tasks) || r.Passed != want {
		t.Fatalf("run: %d of %d passed, %d results; want %d passed", r.Passed, r.Total, len(r.Results), want)
	}
	for _, res := range r.Results {
		if res.Passed != solves(evalTask{Lang: res.Lang}) {
			t.Errorf("%s: passed=%v\n%s", res.Task, res.Passed, res.Hidden)
		}
	}
	if !strings.Contains(outcome, "Done") || !strings.Contains(outcome, "of") {
		t.Errorf("outcome %q", outcome)
	}
	if _, err := os.Stat(filepath.Join(WorkspacesDir(), ".anvil-eval", r.ID)); err == nil {
		t.Error("the run's scratch copies were left behind")
	}
}

// Stop ends a run at its next task and says so.
func TestStopEndsARun(t *testing.T) {
	evalWorkspace(t)
	T := evalApp(t)
	started := make(chan struct{}, 1)
	run := func(ctx context.Context, c *toolCtx, task evalTask, s evalSetting) (string, int, int, string, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return "", 0, 0, "", ctx.Err()
	}
	s, _ := evalSettingByID("worker-plain")
	if _, err := T.startEval("root", s, run); err != nil {
		t.Fatal(err)
	}
	if again, _ := T.startEval("root", s, run); !strings.Contains(again.(map[string]any)["message"].(string), "Already") {
		t.Errorf("a second press: %v", again)
	}
	other, _ := evalSettingByID("lead-plain")
	if _, err := T.startEval("root", other, run); err == nil {
		t.Error("a second run started alongside the first")
	}
	<-started
	st := T.evalSt()
	st.mu.Lock()
	st.cancel()
	st.mu.Unlock()
	if outcome := waitEval(t, T, s.ID); !strings.HasPrefix(outcome, "Stopped") {
		t.Errorf("outcome %q", outcome)
	}
	if r := T.evalRuns(); len(r) != 1 || !r[0].Stopped {
		t.Errorf("stored: %+v", r)
	}
}

// Measuring is an administrator's: nobody else reaches the page or its
// endpoints.
func TestMeasuringIsAnAdministrators(t *testing.T) {
	do, _ := anvilRoutes(t)
	for _, rt := range []struct{ method, path string }{
		{"GET", "/eval"}, {"GET", "/api/eval/settings"}, {"POST", "/api/eval/start?setting=worker-plain"},
		{"GET", "/api/eval/progress?setting=worker-plain"}, {"POST", "/api/eval/stop?setting=worker-plain"},
		{"GET", "/api/eval/runs"}, {"GET", "/api/eval/latest"},
	} {
		if code := do("alice", rt.method, rt.path, "{}"); code != http.StatusForbidden {
			t.Errorf("alice, %s %s answered %d", rt.method, rt.path, code)
		}
	}
	if code := do("root", "GET", "/api/eval/settings", ""); code != http.StatusOK {
		t.Errorf("an administrator's settings list answered %d", code)
	}
}

// The real runner drives Anvil's own tools and check: a scripted model that
// makes the fix with edit_file scores a pass, its step counted, and with the
// check on the check runs before the turn ends.
func TestTheRunnerUsesAnvilsToolsAndCheck(t *testing.T) {
	needToolchains(t, "Go")
	evalWorkspace(t)
	T := evalApp(t)
	T.LLM = &FakeLLM{Turns: []FakeTurn{
		{ToolCalls: []ToolCall{{ID: "1", Name: "read_file", Args: map[string]any{"path": "sum.go"}}}},
		{ToolCalls: []ToolCall{{ID: "2", Name: "edit_file", Args: map[string]any{"path": "sum.go", "old_text": "i < len(xs)-1", "new_text": "i < len(xs)"}}}},
		{Content: "Fixed the loop bound in Sum."},
	}}
	tasks, _ := loadEvalTasks()
	var task evalTask
	for _, tk := range tasks {
		if tk.Name == "go_sum_skips_last" {
			task = tk
		}
	}
	s, _ := evalSettingByID("worker-check")
	res := T.evalOne(context.Background(), T.runEvalAgent, "root", "r1", task, s, filepath.Join(WorkspacesDir(), "one"))
	if !res.Passed || res.Steps != 2 || !strings.Contains(res.Reply, "Fixed") {
		t.Fatalf("%+v", res)
	}
}
