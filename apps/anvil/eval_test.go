package anvil

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
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

// needToolchains skips the test unless each named language's toolchain runs.
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
		if task.isApp() {
			continue // TestEveryAppTaskFailsAtTheStartAndPassesWithItsSolution
		}
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
	// The app tasks are scored by the app host, which this run has not got:
	// left alone, they fail as unsolved ones do.
	solves := func(task evalTask) bool { return task.Lang != "Go" && task.Lang != "App" }
	run := func(ctx context.Context, c *toolCtx, task evalTask, s evalSetting) (evalAgentResult, error) {
		if c.project.CheckCommand != task.Check {
			t.Errorf("%s: the project's check is %q", task.Name, c.project.CheckCommand)
		}
		if solves(task) {
			if err := writeEvalFiles(task.Name, "solution", c.base); err != nil {
				return evalAgentResult{}, err
			}
			return evalAgentResult{Reply: "Done.", Steps: 2, Rounds: 2, Tokens: 100, RanOn: "worker"}, nil
		}
		return evalAgentResult{Reply: "Could not do it.", Steps: 1, Rounds: 1, Tokens: 50, RanOn: "worker"}, nil
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
	run := func(ctx context.Context, c *toolCtx, task evalTask, s evalSetting) (evalAgentResult, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return evalAgentResult{}, ctx.Err()
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
	if !res.Passed || res.Steps != 2 || res.Rounds != 3 || !strings.Contains(res.Reply, "Fixed") {
		t.Fatalf("%+v", res)
	}
}

// An eval task's agent loop is listed among the running work while it runs,
// for the user who started the eval, by the task's title.
func TestAnEvalTaskIsListedWithTheRunningWork(t *testing.T) {
	needToolchains(t, "Go")
	evalWorkspace(t)
	T := evalApp(t)
	hold := make(chan struct{})
	T.LLM = &FakeLLM{Turns: []FakeTurn{{Wait: hold, Content: "Looked.", Repeat: true}}}
	tasks, _ := loadEvalTasks()
	var task evalTask
	for _, tk := range tasks {
		if tk.Name == "go_sum_skips_last" {
			task = tk
		}
	}
	if task.Title == "" {
		t.Fatalf("the task has no title to list it by: %+v", task)
	}
	s, _ := evalSettingByID("worker-check")
	done := make(chan struct{})
	go func() {
		defer close(done)
		T.evalOne(context.Background(), T.runEvalAgent, "root", "r1", task, s, filepath.Join(WorkspacesDir(), "one"))
	}()
	var row LiveEntry
	for deadline := time.Now().Add(10 * time.Second); row.ID == "" && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		for _, e := range AllLiveSessions() {
			if strings.HasPrefix(e.CancelURL, "/api/live/stop?id=") && e.Owner == "root" {
				row = e
			}
		}
	}
	close(hold)
	<-done
	if row.Label != "Anvil eval: "+task.Title {
		t.Errorf("the eval task is listed as %q, want %q", row.Label, "Anvil eval: "+task.Title)
	}
}

// evalAppHost is an orchestrate host to build and check app folders, and a
// workspaces folder outside the host's temp folder, which the sandbox the
// app's scripts run in does not see.
func evalAppHost(t *testing.T) {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no cache folder for scripts to run from")
	}
	ws, err := os.MkdirTemp(cache, "gohort-anvil-evalapps-")
	if err != nil {
		t.Skip("no cache folder for scripts to run from")
	}
	prevRoot, prevAssets, prevWS, prevHost := RootDB, AppAssetsDir(), WorkspacesDir(), agenthost.Get()
	RootDB = &DBase{Store: kvlite.MemStore()}
	SetAppAssetsDir(t.TempDir())
	SetWorkspacesDir(ws)
	o := &orchestrate.OrchestrateApp{}
	o.DB = &DBase{Store: kvlite.MemStore()}
	agenthost.Register(o)
	t.Cleanup(func() {
		RootDB = prevRoot
		SetAppAssetsDir(prevAssets)
		SetWorkspacesDir(prevWS)
		agenthost.Register(prevHost)
		removeAllWritable(ws)
	})
	os.MkdirAll(filepath.Join(ws, "root"), 0o755)
}

// Every app task's hidden checks fail on the folder the agent starts from
// and pass on the reference solution, and each folder builds as an app. A
// page check needs a browser: where there is none it does not run, and only
// the script checks are held to it here.
func TestEveryAppTaskFailsAtTheStartAndPassesWithItsSolution(t *testing.T) {
	needToolchains(t, "Python")
	evalAppHost(t)
	tasks, err := loadEvalTasks()
	if err != nil {
		t.Fatal(err)
	}
	apps := 0
	for _, task := range tasks {
		if !task.isApp() {
			continue
		}
		apps++
		if task.Title == "" || task.Request == "" || task.Lang != "App" {
			t.Errorf("%s is missing a field: %+v", task.Name, task)
		}
		for _, parts := range [][]string{{"repo", "hidden"}, {"repo", "solution", "hidden"}} {
			dir := filepath.Join(WorkspacesDir(), "root", task.Name+"-"+parts[len(parts)-2])
			for _, part := range parts {
				if err := writeEvalFiles(task.Name, part, dir); err != nil {
					t.Fatal(err)
				}
			}
			solved := len(parts) == 3
			o, _ := appHost()
			score, err := o.ScoreAppFolder(context.Background(), "root", dir)
			if err != nil {
				// A start whose hidden checks name a script it has not got
				// yet does not build: that scores as a fail, as it should.
				if solved {
					t.Errorf("%s: the solution does not build: %v", task.Name, err)
				}
				continue
			}
			if strings.Contains(score.Report, "could not run") {
				t.Skipf("no script can run here: %s", score.Report)
			}
			switch {
			case !solved && score.Failed == 0:
				t.Errorf("%s: the hidden checks pass before any work:\n%s", task.Name, score.Report)
			case solved && (score.Failed > 0 || score.Passed == 0):
				t.Errorf("%s: the hidden checks fail on the solution:\n%s", task.Name, score.Report)
			}
			if solved && score.Skip > 0 {
				t.Logf("%s: %d check(s) not run here (no browser?):\n%s", task.Name, score.Skip, score.Report)
			}
		}
	}
	if apps < 3 {
		t.Fatalf("%d app tasks", apps)
	}
}

// Builder's setting runs the app tasks only, each in a folder named for the
// app's slug, which is how Builder finds an app in its workspace.
func TestBuilderIsMeasuredOnTheAppTasksOnly(t *testing.T) {
	evalWorkspace(t)
	T := evalApp(t)
	var mu sync.Mutex
	var folders []string
	run := func(ctx context.Context, c *toolCtx, task evalTask, s evalSetting) (evalAgentResult, error) {
		mu.Lock()
		folders = append(folders, filepath.Base(c.base))
		mu.Unlock()
		if !task.isApp() {
			t.Errorf("Builder was given %s, not an app task", task.Name)
		}
		if _, err := os.Stat(filepath.Join(c.base, "app.json")); err != nil {
			t.Errorf("%s: the folder has no app.json", task.Name)
		}
		return evalAgentResult{Reply: "Could not do it."}, nil
	}
	s, ok := evalSettingByID("builder-apps")
	if !ok || !s.Builder {
		t.Fatal("no Builder setting")
	}
	if _, err := T.startEval("root", s, run); err != nil {
		t.Fatal(err)
	}
	waitEval(t, T, s.ID)
	r := T.evalRuns()[0]
	apps := 0
	tasks, _ := loadEvalTasks()
	for _, task := range tasks {
		if task.isApp() {
			apps++
		}
	}
	if r.Total != apps || len(folders) != apps {
		t.Fatalf("ran %d of %d tasks (%d app tasks): %v", len(folders), r.Total, apps, folders)
	}
	for _, f := range folders {
		if !strings.HasPrefix(f, "anvil-eval-") || !strings.HasSuffix(f, ".app") {
			t.Errorf("folder %q is not <slug>.app", f)
		}
	}
}
