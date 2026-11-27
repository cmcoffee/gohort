package anvil

// Measuring Anvil: a fixed set of small coding tasks, each in a scratch
// repository with a check the agent never sees, run through Anvil's own
// tools and scored pass or fail.
//
// Without it every change to the prompt or the tools is a guess. A run picks
// a setting (the worker or the lead, with the project check or without), runs
// every task in a fresh copy, and records for each whether its hidden check
// passed, how many rounds, steps and tokens it took, how long, and what the agent
// said it had done. Comparing two runs says which tasks a change moved.
//
// It runs in the server (one at a time, an administrator's, started by hand
// and never on a schedule) because the database has one writer: a
// command-line run could not share it with the running server. It spends
// real model time.
//
// The tasks live in evaltasks/: task.json (the request, the project's check,
// the hidden check), repo/ (the starting tree), hidden/ (files copied in
// after the agent finishes, then the hidden check run), and solution/ (a
// reference answer, used only by the test proving each hidden check fails
// on the start and passes on the solution). Go files and go.mod carry a
// ".task" suffix there, so the go tool never builds them as part of gohort.
//
// An app task (kind "app") starts from a gohort app's folder. It has no
// commands: the turn's own end checks the app would install, and its hidden
// part is a checks.json that replaces the app's, scored by running the app's
// checks as Anvil's app_checks does (script checks, and page checks in the
// browser). It passes when every one of them ran and passed.
//
// The run is an approximation of a chat turn, said plainly: it is Anvil's
// prompt, its project briefing, its tools and its check, on the host's own
// loop guardrails, but not the chat path's memory and notes.

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/ui"
)

//go:embed evaltasks
var evalFS embed.FS

const (
	evalTable       = "anvil_eval_runs"
	evalTaskTimeout = 15 * time.Minute
	evalHiddenLimit = 5 * time.Minute
	evalMaxRounds   = 40
	evalProjectID   = "anvil-eval"
	// evalOutcomeFor is how long a finished run's outcome is shown on a
	// page arriving after it.
	evalOutcomeFor = time.Hour
)

// evalSetting is one way to run the task set.
type evalSetting struct {
	ID, Label, Desc string
	Lead, Check     bool
	// NoScript leaves tool_script out, to measure what it changes
	// (docs/design/code-mode.md).
	NoScript bool
}

var evalSettings = []evalSetting{
	{"worker-check", "Worker, with the check", "The local model, with each task's check run after its edits.", false, true, false},
	{"worker-check-noscript", "Worker, with the check, without tool_script", "As Worker, with the check, but with no tool_script: compare the two to see whether scripts save rounds.", false, true, true},
	{"worker-plain", "Worker, without the check", "The local model, with no check: how Anvil worked before.", false, false, false},
	{"lead-check", "Lead, with the check", "The lead model, with each task's check run after its edits.", true, true, false},
	{"lead-plain", "Lead, without the check", "The lead model, with no check.", true, false, false},
}

func evalSettingByID(id string) (evalSetting, bool) {
	for _, s := range evalSettings {
		if s.ID == id {
			return s, true
		}
	}
	return evalSetting{}, false
}

// evalTask is one task of the set.
type evalTask struct {
	Name        string `json:"-"`
	Title       string `json:"title"`
	Lang        string `json:"lang"`
	Request     string `json:"request"`
	Check       string `json:"check"`
	HiddenCheck string `json:"hidden_check"`
	// Kind is "app" for a task on a gohort app's folder, "" for code.
	Kind string `json:"kind,omitempty"`
}

// isApp reports a task on an app's folder, scored by the app's checks.
func (t evalTask) isApp() bool { return t.Kind == "app" }

// loadEvalTasks reads the task set, in name order.
func loadEvalTasks() ([]evalTask, error) {
	entries, err := fs.ReadDir(evalFS, "evaltasks")
	if err != nil {
		return nil, err
	}
	var out []evalTask
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := evalFS.ReadFile(path.Join("evaltasks", e.Name(), "task.json"))
		if err != nil {
			return nil, fmt.Errorf("task %s: %w", e.Name(), err)
		}
		var t evalTask
		if err := json.Unmarshal(b, &t); err != nil {
			return nil, fmt.Errorf("task %s: %w", e.Name(), err)
		}
		t.Name = e.Name()
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// writeEvalFiles copies one part of a task (repo, hidden, solution) into dir,
// dropping the ".task" suffix.
func writeEvalFiles(task, part, dir string) error {
	root := path.Join("evaltasks", task, part)
	return fs.WalkDir(evalFS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root && os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel := strings.TrimSuffix(strings.TrimPrefix(p, root+"/"), ".task")
		b, err := evalFS.ReadFile(p)
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		mode := os.FileMode(0o600)
		if strings.HasSuffix(rel, ".sh") {
			mode = 0o700
		}
		return os.WriteFile(dst, b, mode)
	})
}

// evalTaskResult is how one task went.
type evalTaskResult struct {
	Task   string `json:"task"`
	Title  string `json:"title"`
	Lang   string `json:"lang"`
	Passed bool   `json:"passed"`
	Steps  int    `json:"steps"`
	// Rounds is how many times the model was called; Steps counts the tool
	// calls those rounds made. Zero on runs from before it was kept.
	Rounds  int     `json:"rounds,omitempty"`
	Tokens  int     `json:"tokens"`
	Seconds float64 `json:"seconds"`
	// RanOn is the tier that served the rounds ("lead", "worker", or both),
	// which can differ from the setting when a privacy pin keeps the lead
	// out.
	RanOn  string `json:"ran_on"`
	Reply  string `json:"reply"`
	Hidden string `json:"hidden"` // the end of the hidden check's output
	Error  string `json:"error,omitempty"`
}

// evalRun is one run of the set under one setting.
type evalRun struct {
	ID      string           `json:"id"`
	Setting string           `json:"setting"`
	Label   string           `json:"label"`
	By      string           `json:"by"`
	Started time.Time        `json:"started"`
	Seconds float64          `json:"seconds"`
	Passed  int              `json:"passed"`
	Total   int              `json:"total"`
	Stopped bool             `json:"stopped,omitempty"`
	Results []evalTaskResult `json:"results"`
}

// evalState is the run going now, or the last one's outcome.
type evalState struct {
	mu       sync.Mutex
	setting  string
	running  bool
	progress string
	outcome  string
	ended    time.Time
	cancel   context.CancelFunc
}

func (s *evalState) set(progress string) {
	s.mu.Lock()
	s.progress = progress
	s.mu.Unlock()
}

// evalInit guards creating an app's evalState.
var evalInit sync.Mutex

// evalSt is the app's run state, made on first use.
func (T *Anvil) evalSt() *evalState {
	evalInit.Lock()
	defer evalInit.Unlock()
	if T.eval == nil {
		T.eval = &evalState{}
	}
	return T.eval
}

// evalRunner runs one task with Anvil's tools and reports how it went. A
// variable so a test can run the set without a model.
type evalRunner func(ctx context.Context, c *toolCtx, task evalTask, s evalSetting) (evalAgentResult, error)

// evalAgentResult is how one task's run went, before its hidden check.
type evalAgentResult struct {
	Reply                 string
	Steps, Rounds, Tokens int
	RanOn                 string // the tier that served it ("lead", "worker", or both)
}

// runEvalAgent is the real runner: Anvil's prompt, briefing and tools on the
// app's own agent loop, every confirmation allowed (nobody is watching), and
// the project check as the turn's condition on ending when the setting has
// one.
func (T *Anvil) runEvalAgent(ctx context.Context, c *toolCtx, task evalTask, s evalSetting) (evalAgentResult, error) {
	tier := WORKER
	if s.Lead {
		tier = LEAD
	}
	var mu sync.Mutex
	steps, rounds, tokens := 0, 0, 0
	tiers := map[string]bool{}
	cfg := AgentLoopConfig{
		SystemPrompt: coderPrompt + T.projectContext(c.project, c.base),
		Tools:        c.codeTools(),
		MaxRounds:    evalMaxRounds,
		Tier:         tier,
		Confirm:      func(string, string) bool { return true },
		OnStep: func(st StepInfo) {
			mu.Lock()
			steps += len(st.ToolCalls)
			rounds = max(rounds, st.Round)
			if st.Tier != "" && st.Tier != "unset" {
				tiers[st.Tier] = true
			}
			mu.Unlock()
		},
		OnResponse: func(r *Response) {
			if r == nil {
				return
			}
			mu.Lock()
			tokens += r.InputTokens + r.OutputTokens
			mu.Unlock()
		},
	}
	if s.Check {
		cfg.FinishCheck = func(reply string) (string, string) { return c.finishCheck(reply), "" }
	}
	if h := agenthost.Get(); h != nil {
		cfg = h.AppLoopGuard(ctx, c.user, anvilAgentID).Apply(cfg)
	}
	name := task.Title
	if name == "" {
		name = task.Name
	}
	ctx = ListLoopAs(ctx, c.user, "Anvil eval: "+name)
	resp, _, err := T.RunAgentLoop(ctx, []Message{{Role: "user", Content: task.Request}}, cfg)
	reply := ""
	if resp != nil {
		reply = resp.Content
	}
	var ran []string
	for t := range tiers {
		ran = append(ran, t)
	}
	sort.Strings(ran)
	return evalAgentResult{Reply: reply, Steps: steps, Rounds: rounds, Tokens: tokens, RanOn: strings.Join(ran, "+")}, err
}

// evalOne runs task in a fresh copy under dir and scores it.
func (T *Anvil) evalOne(ctx context.Context, run evalRunner, user, runID string, task evalTask, s evalSetting, dir string) evalTaskResult {
	res := evalTaskResult{Task: task.Name, Title: task.Title, Lang: task.Lang}
	start := time.Now()
	defer func() { res.Seconds = time.Since(start).Round(time.Second).Seconds() }()
	fail := func(err error) evalTaskResult {
		res.Error = err.Error()
		return res
	}
	if err := writeEvalFiles(task.Name, "repo", dir); err != nil {
		return fail(err)
	}
	if err := evalCommit(dir); err != nil {
		return fail(err)
	}
	// One project id for every task and run, so they share one package cache
	// rather than each leaving its own behind.
	p := Project{ID: evalProjectID, Name: task.Title, Kind: KindClone, Owner: user}
	if s.Check {
		p.CheckCommand = task.Check
	}
	c := &toolCtx{udb: UserDB(OpenCache(), user), user: user, project: p, base: dir, sessionID: runID + "-" + task.Name, noScript: s.NoScript}
	tctx, cancel := context.WithTimeout(ctx, evalTaskTimeout)
	got, err := run(tctx, c, task, s)
	cancel()
	res.Reply, res.Steps, res.Rounds, res.Tokens, res.RanOn = truncateEval(got.Reply, 1200), got.Steps, got.Rounds, got.Tokens, got.RanOn
	if err != nil {
		res.Error = err.Error()
	}
	if ctx.Err() != nil {
		return res
	}
	if err := writeEvalFiles(task.Name, "hidden", dir); err != nil {
		return fail(err)
	}
	res.Passed, res.Hidden = scoreEvalTask(ctx, c, task)
	return res
}

// scoreEvalTask runs a task's hidden check on c's tree, its hidden files
// already copied in: whether it passed, and the end of what it said.
func scoreEvalTask(ctx context.Context, c *toolCtx, task evalTask) (bool, string) {
	if !task.isApp() {
		out := c.sandboxed(ctx, task.HiddenCheck, evalHiddenLimit)
		return out.Err == nil && !out.TimedOut, tailOutput(out.Output, 1500)
	}
	o, err := appHost()
	if err != nil {
		return false, err.Error()
	}
	ctx, cancel := context.WithTimeout(ctx, evalHiddenLimit)
	defer cancel()
	score, err := o.ScoreAppFolder(ctx, c.user, c.base)
	if err != nil {
		return false, "The app did not build: " + err.Error()
	}
	return score.Passed > 0 && score.Failed == 0 && score.Skip == 0, tailOutput(score.Report, 1500)
}

// evalCommit makes dir a repository with its starting tree committed, so the
// agent's git tool and the changes view work as they do on a real checkout.
func evalCommit(dir string) error {
	if _, err := runGit(dir, "init", "-q"); err != nil {
		return err
	}
	if _, err := runGit(dir, "add", "-A"); err != nil {
		return err
	}
	_, err := runGit(dir, "-c", "user.name=Anvil", "-c", "user.email=anvil@example.invalid", "commit", "-q", "-m", "start")
	return err
}

func truncateEval(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// runEval runs every task under s, reporting as it goes, and stores the run.
func (T *Anvil) runEval(ctx context.Context, run evalRunner, user string, s evalSetting, st *evalState) evalRun {
	r := evalRun{ID: UUIDv4(), Setting: s.ID, Label: s.Label, By: user, Started: time.Now().UTC()}
	tasks, err := loadEvalTasks()
	if err != nil {
		st.set("Could not read the task set: " + err.Error())
		return r
	}
	r.Total = len(tasks)
	base := filepath.Join(WorkspacesDir(), ".anvil-eval", r.ID)
	defer os.RemoveAll(base)
	for i, task := range tasks {
		if ctx.Err() != nil {
			r.Stopped = true
			break
		}
		st.set(fmt.Sprintf("Task %d of %d: %s - %d passed so far - %s", i+1, len(tasks), task.Title, r.Passed, time.Since(r.Started).Round(time.Second)))
		res := T.evalOne(ctx, run, user, r.ID, task, s, filepath.Join(base, task.Name))
		if ctx.Err() != nil {
			r.Stopped = true
			break
		}
		r.Results = append(r.Results, res)
		if res.Passed {
			r.Passed++
		}
	}
	r.Seconds = time.Since(r.Started).Round(time.Second).Seconds()
	T.DB.Set(evalTable, r.ID, r)
	return r
}

// evalOutcome says how a run ended, for the page.
func evalOutcome(r evalRun) string {
	took := time.Duration(r.Seconds) * time.Second
	if r.Stopped {
		return fmt.Sprintf("Stopped after %d of %d tasks: %d passed, in %s.", len(r.Results), r.Total, r.Passed, took)
	}
	return fmt.Sprintf("Done: %d of %d tasks passed, in %s.", r.Passed, r.Total, took)
}

// --- the page's endpoints (administrators only) ------------------------------

func (T *Anvil) evalAdmin(c *jsonapi.Call) error {
	if !callerIsAdmin(c.R) {
		return jsonapi.Forbidden("measuring Anvil is an administrator's")
	}
	return nil
}

// evalRuns is every stored run, newest first.
func (T *Anvil) evalRuns() []evalRun {
	var out []evalRun
	for _, k := range T.DB.Keys(evalTable) {
		var r evalRun
		if T.DB.Get(evalTable, k, &r) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	return out
}

// handleEvalSettings lists the settings a run can use, each with when it last
// ran and how it went.
func (T *Anvil) handleEvalSettings(c *jsonapi.Call) (any, error) {
	if err := T.evalAdmin(c); err != nil {
		return nil, err
	}
	last := map[string]evalRun{}
	for _, r := range T.evalRuns() {
		if _, seen := last[r.Setting]; !seen {
			last[r.Setting] = r
		}
	}
	var out []map[string]any
	for _, s := range evalSettings {
		hist := "Never run."
		if r, ok := last[s.ID]; ok {
			hist = fmt.Sprintf("Last run %s: %d of %d passed.", r.Started.Local().Format("Jan 2 15:04"), r.Passed, r.Total)
		}
		out = append(out, map[string]any{"id": s.ID, "label": s.Label, "desc": s.Desc, "history": hist})
	}
	return out, nil
}

// handleEvalStart starts a run under ?setting=, unless one is going.
func (T *Anvil) handleEvalStart(c *jsonapi.Call, _ struct{}) (any, error) {
	if err := T.evalAdmin(c); err != nil {
		return nil, err
	}
	s, ok := evalSettingByID(c.R.URL.Query().Get("setting"))
	if !ok {
		return nil, jsonapi.BadRequest("no such setting")
	}
	if T.LLM == nil {
		return nil, jsonapi.BadRequest("no model is configured")
	}
	if s.Lead && T.LeadLLM == nil {
		return nil, jsonapi.BadRequest("no lead model is configured")
	}
	if WorkspacesDir() == "" {
		return nil, jsonapi.BadRequest("the workspaces folder is not configured, and the tasks run there")
	}
	return T.startEval(c.User, s, T.runEvalAgent)
}

// startEval starts a run in the background, on a context of its own so it
// outlives the request that started it.
func (T *Anvil) startEval(user string, s evalSetting, run evalRunner) (any, error) {
	st := T.evalSt()
	st.mu.Lock()
	if st.running {
		going := st.setting
		st.mu.Unlock()
		if going == s.ID {
			return map[string]any{"running": true, "message": "Already running."}, nil
		}
		return nil, jsonapi.Status(409, "another run is going; stop it or wait for it")
	}
	ctx, cancel := context.WithCancel(context.Background())
	st.running, st.setting, st.cancel, st.outcome = true, s.ID, cancel, ""
	st.progress = "Starting"
	st.mu.Unlock()
	go func() {
		defer cancel()
		r := T.runEval(ctx, run, user, s, st)
		st.mu.Lock()
		st.running, st.progress, st.cancel = false, "", nil
		st.outcome, st.ended = evalOutcome(r), time.Now()
		st.mu.Unlock()
	}()
	return map[string]any{"running": true, "message": "Started."}, nil
}

// handleEvalProgress answers what the run under ?setting= is doing, or how it
// ended, for an hour after.
func (T *Anvil) handleEvalProgress(c *jsonapi.Call) (any, error) {
	if err := T.evalAdmin(c); err != nil {
		return nil, err
	}
	st := T.evalSt()
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.setting != c.R.URL.Query().Get("setting") {
		return map[string]any{}, nil
	}
	if st.running {
		return map[string]any{"progress": st.progress}, nil
	}
	if st.outcome != "" && time.Since(st.ended) < evalOutcomeFor {
		return map[string]any{"outcome": st.outcome}, nil
	}
	return map[string]any{}, nil
}

// handleEvalStop stops the run going, which ends at its next task.
func (T *Anvil) handleEvalStop(c *jsonapi.Call, _ struct{}) (any, error) {
	if err := T.evalAdmin(c); err != nil {
		return nil, err
	}
	st := T.evalSt()
	st.mu.Lock()
	if st.cancel != nil {
		st.cancel()
		st.progress = "Stopping"
	}
	st.mu.Unlock()
	return map[string]any{"ok": true}, nil
}

// handleEvalRunList is the stored runs, one row each.
func (T *Anvil) handleEvalRunList(c *jsonapi.Call) (any, error) {
	if err := T.evalAdmin(c); err != nil {
		return nil, err
	}
	rows := []map[string]any{}
	for _, r := range T.evalRuns() {
		state := fmt.Sprintf("%d of %d", r.Passed, r.Total)
		rounds, steps := 0, 0
		for _, res := range r.Results {
			rounds += res.Rounds
			steps += res.Steps
		}
		if r.Stopped {
			state += " (stopped)"
		}
		rows = append(rows, map[string]any{
			"id": r.ID, "when": r.Started.Local().Format("Jan 2 15:04"), "setting": r.Label,
			"passed": state, "took": (time.Duration(r.Seconds) * time.Second).String(), "by": r.By,
			"rounds": rounds, "steps": steps,
		})
	}
	return rows, nil
}

// handleEvalLatest is the newest run's tasks, one row each.
func (T *Anvil) handleEvalLatest(c *jsonapi.Call) (any, error) {
	if err := T.evalAdmin(c); err != nil {
		return nil, err
	}
	rows := []map[string]any{}
	runs := T.evalRuns()
	if len(runs) == 0 {
		return rows, nil
	}
	for _, res := range runs[0].Results {
		result := "failed"
		if res.Passed {
			result = "passed"
		}
		if res.Error != "" {
			result += ": " + res.Error
		}
		rows = append(rows, map[string]any{
			"task": res.Title, "lang": res.Lang, "result": result, "rounds": res.Rounds, "steps": res.Steps,
			"tokens": res.Tokens, "took": (time.Duration(res.Seconds) * time.Second).String(), "ran_on": res.RanOn,
			"reply": res.Reply, "hidden": res.Hidden,
		})
	}
	return rows, nil
}

// handleEvalPage is where an administrator runs the task set and reads how it
// went.
func (T *Anvil) handleEvalPage(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	if !callerIsAdmin(r) {
		http.Error(w, "measuring Anvil is an administrator's", http.StatusForbidden)
		return
	}
	n := 0
	if tasks, err := loadEvalTasks(); err == nil {
		n = len(tasks)
	}
	page := ui.Page{
		Title:     "Measure Anvil",
		ShowTitle: true,
		BackURL:   "/anvil",
		MaxWidth:  "1100px",
		Sections: []ui.Section{{
			Title: "Run the task set",
			Subtitle: fmt.Sprintf("%d small coding tasks in Go, Python and JavaScript, each in a scratch repository with a check Anvil never sees. "+
				"A run takes each task in turn and scores it passed or failed. It uses real model time, one run at a time.", n),
			Body: ui.ActionList{
				Source:         "api/eval/settings",
				LabelField:     "label",
				DescField:      "desc",
				HistoryField:   "history",
				PostTo:         "api/eval/start?setting={id}",
				ButtonText:     "Run",
				ProgressSource: "api/eval/progress?setting={id}",
				CancelTo:       "api/eval/stop?setting={id}",
				Invalidate:     []string{"api/eval/runs", "api/eval/latest"},
			},
		}, {
			Title:    "Runs",
			Subtitle: "Newest first. Compare two runs to see whether a change helped.",
			Body: ui.Table{
				Source: "api/eval/runs",
				RowKey: "id",
				Columns: []ui.Col{
					{Field: "when", Label: "When"},
					{Field: "setting", Label: "Setting", Flex: 2},
					{Field: "passed", Label: "Passed"},
					{Field: "rounds", Label: "Rounds", Mute: true},
					{Field: "steps", Label: "Steps", Mute: true},
					{Field: "took", Label: "Took", Mute: true},
					{Field: "by", Label: "By", Mute: true},
				},
				EmptyText: "Nothing run yet.",
			},
		}, {
			Title:    "The latest run, task by task",
			Subtitle: "What each task asked, whether its hidden check passed, and what Anvil said it had done.",
			Body: ui.Table{
				Source: "api/eval/latest",
				RowKey: "task",
				Columns: []ui.Col{
					{Field: "task", Label: "Task", Flex: 2},
					{Field: "lang", Label: "Language", Mute: true},
					{Field: "result", Label: "Result", Flex: 2},
					{Field: "rounds", Label: "Rounds", Mute: true},
					{Field: "steps", Label: "Steps", Mute: true},
					{Field: "took", Label: "Took", Mute: true},
					{Field: "ran_on", Label: "Ran on", Mute: true},
					{Field: "reply", Label: "Anvil said", Flex: 4, Mute: true},
				},
				EmptyText: "Nothing run yet.",
			},
		}},
	}
	page.ServeHTTP(w, r)
}
