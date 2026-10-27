package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/kvlite"
)

// A pipeline run is long by nature, so the transcript is stored as it streams
// rather than at the end: a run only visible on completion is one nobody can
// check on, compare against, or learn a regression from.

// The store takes the HOST's database — runs live wherever that host keeps
// them, which is what let this move packages without moving any data.
func runsDB(t *testing.T) Database {
	t.Helper()
	return &DBase{Store: kvlite.MemStore()}
}

func TestPipelineRunsRoundTrip(t *testing.T) {
	db := runsDB(t)
	run := PipelineRun{
		ID: "r1", PipelineID: "p1", Title: "why is the sky blue",
		Date:   time.Now(),
		Blocks: []PipelineRunBlock{{ID: "stage-1", Type: "worker", Title: "decompose", Body: "three sub-questions"}},
	}
	SavePipelineRun(db, "u", run)

	got, ok := LoadPipelineRun(db, "u", "p1", "r1")
	if !ok {
		t.Fatal("saved run should load back")
	}
	if got.Title != run.Title || len(got.Blocks) != 1 || got.Blocks[0].Body != "three sub-questions" {
		t.Fatalf("round trip lost content: %+v", got)
	}
}

// Runs are scoped per pipeline — one pipeline's history must not show another's.
func TestPipelineRunsAreScopedAndOrdered(t *testing.T) {
	db := runsDB(t)
	base := time.Now()
	SavePipelineRun(db, "u", PipelineRun{ID: "old", PipelineID: "p1", Title: "older", Date: base.Add(-time.Hour)})
	SavePipelineRun(db, "u", PipelineRun{ID: "new", PipelineID: "p1", Title: "newer", Date: base})
	SavePipelineRun(db, "u", PipelineRun{ID: "other", PipelineID: "p2", Title: "different pipeline", Date: base})

	runs := ListPipelineRuns(db, "u", "p1")
	if len(runs) != 2 {
		t.Fatalf("p1 should have 2 runs, got %d", len(runs))
	}
	if runs[0].ID != "new" {
		t.Errorf("newest run must come first, got %q", runs[0].ID)
	}
	for _, r := range runs {
		if r.PipelineID != "p1" {
			t.Errorf("another pipeline's run leaked in: %+v", r)
		}
	}
	// And a run is per-user.
	if other := ListPipelineRuns(db, "someone-else", "p1"); len(other) != 0 {
		t.Errorf("runs must not cross users, got %d", len(other))
	}
}

// An interrupted run stays marked Running rather than reading as
// finished-with-no-output, which would look like the pipeline produced nothing
// instead of that it was cut off.
func TestInterruptedRunStaysMarkedRunning(t *testing.T) {
	db := runsDB(t)
	SavePipelineRun(db, "u", PipelineRun{ID: "r1", PipelineID: "p1", Title: "q", Date: time.Now(), Running: true})
	got, _ := LoadPipelineRun(db, "u", "p1", "r1")
	if !got.Running {
		t.Error("a run recorded mid-flight must stay marked running")
	}
	if got.Output != "" || got.Err != "" {
		t.Error("an interrupted run should carry neither output nor error")
	}
}

func TestPipelineRunTitleTrims(t *testing.T) {
	if got := PipelineRunTitle("  what   is\n\nthe answer  "); got != "what is the answer" {
		t.Errorf("title = %q, want whitespace collapsed", got)
	}
	long := strings.Repeat("a", 200)
	got := PipelineRunTitle(long)
	if len([]rune(got)) > 91 {
		t.Errorf("title should be capped for the sidebar, got %d chars", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a truncated title should show it was cut: %q", got)
	}
}

// Three places have to agree on how a stored run identifies itself: the sidebar
// LIST, the single-run LOAD, and the panel that reads both. Two of them said
// "ID"; the list said "id". Every row came back undefined — no title, no date,
// nothing to click — which reads exactly like a history that was never saved,
// so the hunt starts at storage, where nothing is wrong.
func TestSessionRowMatchesThePanelDefaults(t *testing.T) {
	b, err := json.Marshal(PipelineSessionRow{ID: "r1", Title: "why", Date: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ID", "Title", "Date"} {
		if _, ok := got[key]; !ok {
			t.Errorf("the list row must carry %q — the panel reads that key by default; got %v", key, got)
		}
	}
	// And the panel's defaults are still those keys. If someone lowercases the
	// runtime defaults, this row has to move with them.
	js, err := os.ReadFile("ui/assets/runtime/40_pipeline_panel.js")
	if err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]string{
		"session_id_field":    "ID",
		"session_title_field": "Title",
		"session_date_field":  "Date",
	} {
		m := regexp.MustCompile(`cfg\.` + field + `\s*\|\|\s*'([^']+)'`).FindStringSubmatch(string(js))
		if m == nil {
			t.Errorf("could not find the panel default for %s", field)
			continue
		}
		if m[1] != want {
			t.Errorf("panel defaults %s to %q but the server sends %q", field, m[1], want)
		}
	}
}

// The panel posts exactly what the author declared. Reading only "input" and
// "topic" meant a form whose box was named anything else got 400 "input
// required" back against a filled-in form — with the app's own field label
// staring at the user. Naming the box is the author's call.
func TestPipelineRunInputAcceptsTheFieldTheAuthorNamed(t *testing.T) {
	cases := []struct{ body, want, why string }{
		{`{"input":"a"}`, "a", "the documented name still works"},
		{`{"topic":"b"}`, "b", "so does the other documented name"},
		{`{"proposition":"remote work is worse"}`, "remote work is worse", "an author's own name must work"},
		{`{"question":"why","input":"real"}`, "real", "input wins over an earlier field"},
		{`{"question":"why","topic":"real"}`, "real", "topic wins too"},
		{`{"first":"one","second":"two"}`, "one", "otherwise the FIRST field, in declared order"},
		{`{"ready":true,"count":3,"q":"text"}`, "text", "a toggle or a number is never the question"},
		{`{"q":"   "}`, "", "blank is not an input"},
		{`{}`, "", "nothing submitted"},
		{`not json`, "", "garbage in, refusal out"},
	}
	for _, c := range cases {
		if got := PipelineRunInput([]byte(c.body)); got != c.want {
			t.Errorf("%s: PipelineRunInput(%s) = %q, want %q", c.why, c.body, got, c.want)
		}
	}
}

// A run still in flight is not history: half a transcript handed to something
// that asked for a result reads as a complete one.
func TestLatestPipelineRunSkipsOneStillRunning(t *testing.T) {
	db := runsDB(t)
	base := time.Now().Add(-time.Hour)
	SavePipelineRun(db, "u", PipelineRun{ID: "old", PipelineID: "p1", Title: "first", Date: base, Output: "done"})
	SavePipelineRun(db, "u", PipelineRun{ID: "live", PipelineID: "p1", Title: "second", Date: base.Add(time.Minute), Running: true})

	run, ok := LatestPipelineRun(db, "u", "p1")
	if !ok || run.ID != "old" {
		t.Errorf("want the last FINISHED run, got %q ok=%v", run.ID, ok)
	}
	if _, ok := LatestPipelineRun(db, "someone-else", "p1"); ok {
		t.Error("runs are per-user; a shared app must not hand over someone else's")
	}
	if _, ok := LatestPipelineRun(db, "u", ""); ok {
		t.Error("no pipeline bound, no run")
	}
}

// A pipeline app that asks for three things and can only use one is asking for
// two of them as decoration: the debate app that wanted a proposition and two
// sides templated {side_a} against a value that never arrived, and the model
// saw the braces.
func TestSubmissionExposesEveryFieldAsATemplateVar(t *testing.T) {
	input, vars := PipelineRunSubmission([]byte(
		`{"proposition":"remote work is worse","side_a":"FOR","side_b":"AGAINST","rounds":3,"strict":true}`))
	if input != "remote work is worse" {
		t.Errorf("the first string field is still the input, got %q", input)
	}
	for k, want := range map[string]string{
		"{proposition}": "remote work is worse",
		"{side_a}":      "FOR",
		"{side_b}":      "AGAINST",
		"{rounds}":      "3",    // not 3.0 — JSON has one number type
		"{strict}":      "true", // a toggle reads as text, not as nothing
	} {
		if vars[k] != want {
			t.Errorf("vars[%s] = %q, want %q", k, vars[k], want)
		}
	}
	// The field taken as the input is ALSO a var, so a prompt can name it
	// either way.
	if vars["{proposition}"] == "" {
		t.Error("the input field must still be addressable by its own name")
	}
}

// The form belongs to the app author; the template vocabulary belongs to the
// framework. A field named "input" must not be able to redefine {input} under a
// pipeline that was authored against it.
func TestSubmissionSkipsReservedTemplateNames(t *testing.T) {
	_, vars := PipelineRunSubmission([]byte(
		`{"topic":"real question","input":"also real","item":"x","iteration":"9","prev":"y"}`))
	for _, reserved := range []string{"{input}", "{item}", "{iteration}", "{prev}"} {
		if _, taken := vars[reserved]; taken {
			t.Errorf("%s is the interpreter's; a form field must not claim it", reserved)
		}
	}
	if vars["{topic}"] != "real question" {
		t.Errorf("an ordinary field is unaffected, got %q", vars["{topic}"])
	}
}

// Run vars reach EVERY stage, including inside a loop body — which is the whole
// reason they live on the run instead of being threaded through runList, since
// a loop body builds its own vars and passes those down.
func TestRunVarsReachLoopBodies(t *testing.T) {
	r := &pipelineRun{vars: map[string]string{"{side_a}": "FOR", "{tone}": "harsh"}}
	got := r.applyRunVars("Argue {side_a} in a {tone} register, pass {iteration}")
	want := "Argue FOR in a harsh register, pass {iteration}"
	if got != want {
		t.Errorf("applyRunVars = %q, want %q", got, want)
	}
	// No vars is not a crash, and leaves the template alone.
	empty := &pipelineRun{}
	if s := empty.applyRunVars("nothing {here}"); s != "nothing {here}" {
		t.Errorf("a run with no form values must not alter the prompt, got %q", s)
	}
}

// A finished run can be put through a follow-up: a report written from it, a
// re-synthesis, a fold of its own follow-ups back into it. Each makes a new
// run linked to its parent, on the same runner as any run.

// followUpSurface is a run surface with two follow-ups whose work records what
// it was handed.
func followUpSurface(t *testing.T) (RunSurface, *followUpCalls) {
	t.Helper()
	calls := &followUpCalls{}
	s := runSurface(t, func(ctx context.Context, input string, _ map[string]string, sink PipelineSink) (string, error) {
		return "the answer to " + input, nil
	})
	s.FollowUps = []PipelineDef{
		{Name: "Write report", Description: "A briefing from the result", Stages: []PipelineStage{{Name: "w", Prompt: "{input}"}}},
		{Name: "Consolidate", Stages: []PipelineStage{{Name: "c", Prompt: "{input} {children}"}}},
	}
	s.FollowUpWork = func(def PipelineDef) RunWork {
		return func(ctx context.Context, input string, vars map[string]string, sink PipelineSink) (string, error) {
			calls.add(def.Name, input, vars)
			return def.Name + " of: " + input, nil
		}
	}
	return s, calls
}

type followUpCalls struct {
	mu  sync.Mutex
	got []struct {
		name, input string
		vars        map[string]string
	}
}

func (c *followUpCalls) add(name, input string, vars map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, struct {
		name, input string
		vars        map[string]string
	}{name, input, vars})
}

func (c *followUpCalls) last() (string, string, map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.got) == 0 {
		return "", "", nil
	}
	g := c.got[len(c.got)-1]
	return g.name, g.input, g.vars
}

// waitFinished waits for a stored run to stop running.
func waitFinished(t *testing.T, s RunSurface, id string) PipelineRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if run, ok := LoadPipelineRun(s.DB, s.User, s.OwnerID, id); ok && !run.Running {
			return run
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("run %s never finished", id)
	return PipelineRun{}
}

func followUp(s RunSurface, name, runID string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	(&AppCore{}).ServeRuns(rec, httptest.NewRequest(http.MethodPost, "/followup/"+name+"/"+runID, nil), s, "followup/"+name+"/"+runID)
	return rec
}

// The panel asks what a run can be put through.
func TestFollowUpsAreListedForThePanel(t *testing.T) {
	s, _ := followUpSurface(t)
	rec := httptest.NewRecorder()
	(&AppCore{}).ServeRuns(rec, httptest.NewRequest(http.MethodGet, "/followups", nil), s, "followups")
	var list []map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 2 || list[0]["name"] != "write_report" || list[0]["label"] != "Write report" || list[0]["title"] != "A briefing from the result" {
		t.Fatalf("follow-ups listed as %v", list)
	}
	none := runSurface(t, nil)
	rec = httptest.NewRecorder()
	(&AppCore{}).ServeRuns(rec, httptest.NewRequest(http.MethodGet, "/followups", nil), none, "followups")
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("a surface with none answers an empty list, got %q", rec.Body.String())
	}
}

// A follow-up makes a new run from the finished one: its output in, linked
// back to it, and listed with the way back.
func TestAFollowUpMakesALinkedRunFromTheResult(t *testing.T) {
	s, calls := followUpSurface(t)
	parentID, _ := startRun(t, s)
	parent := waitFinished(t, s, parentID)

	rec := followUp(s, "write_report", parentID)
	if !strings.Contains(rec.Body.String(), "event: done") {
		t.Fatalf("the follow-up must stream to the end:\n%s", rec.Body.String())
	}
	name, input, vars := calls.last()
	if name != "Write report" || input != parent.Output {
		t.Errorf("follow-up ran %q over %q, want Write report over the parent's output %q", name, input, parent.Output)
	}
	if vars["{parent_input}"] != "go" {
		t.Errorf("{parent_input} must be what the parent was asked, got %q", vars["{parent_input}"])
	}
	var child PipelineRun
	for _, r := range ListPipelineRuns(s.DB, s.User, s.OwnerID) {
		if r.ParentID == parentID {
			child = r
		}
	}
	if child.ID == "" || child.FollowUp != "write_report" || !strings.HasPrefix(child.Title, "Write report: ") || child.Output != "Write report of: "+parent.Output {
		t.Fatalf("the follow-up run must be stored, linked and titled: %+v", child)
	}
	list := httptest.NewRecorder()
	(&AppCore{}).ServeRuns(list, httptest.NewRequest(http.MethodGet, "/sessions", nil), s, "sessions")
	if !strings.Contains(list.Body.String(), `"ParentID":"`+parentID+`"`) {
		t.Errorf("the sidebar row must carry ParentID, for the way back:\n%s", list.Body.String())
	}
}

// A consolidation reads what the earlier follow-ups produced.
func TestAFollowUpReadsTheRunsChildren(t *testing.T) {
	s, calls := followUpSurface(t)
	parentID, _ := startRun(t, s)
	waitFinished(t, s, parentID)
	followUp(s, "write_report", parentID)
	followUp(s, "consolidate", parentID)
	_, _, vars := calls.last()
	if !strings.Contains(vars["{children}"], "## Write report: ") || !strings.Contains(vars["{children}"], "Write report of:") {
		t.Errorf("{children} must carry each finished child under its title, got %q", vars["{children}"])
	}
}

// What cannot be followed up says why rather than starting a run that fails.
func TestAFollowUpRefusesWhatItCannotRun(t *testing.T) {
	s, _ := followUpSurface(t)
	SavePipelineRun(s.DB, s.User, PipelineRun{ID: "running12345", PipelineID: s.OwnerID, Title: "x", Running: true})
	SavePipelineRun(s.DB, s.User, PipelineRun{ID: "emptyout1234", PipelineID: s.OwnerID, Title: "x"})
	for _, c := range []struct {
		name, run string
		code      int
	}{
		{"write_report", "running12345", http.StatusConflict},
		{"write_report", "emptyout1234", http.StatusBadRequest},
		{"no_such_followup", "emptyout1234", http.StatusNotFound},
		{"write_report", "nobodysrun12", http.StatusNotFound},
	} {
		if got := followUp(s, c.name, c.run).Code; got != c.code {
			t.Errorf("%s on %s: %d, want %d", c.name, c.run, got, c.code)
		}
	}
	// Another user's run is not there to follow up.
	other := s
	other.User = "someone-else"
	parentID, _ := startRun(t, s)
	waitFinished(t, s, parentID)
	if got := followUp(other, "write_report", parentID).Code; got != http.StatusNotFound {
		t.Errorf("another user's run: %d, want 404", got)
	}
}

// A follow-up run interrupted by a restart resumes as that follow-up, not as
// the pipeline it belongs to.
func TestAFollowUpRunResumesAsItself(t *testing.T) {
	withQueue(t)
	s, calls := followUpSurface(t)
	s.Kind = "followtest"
	run := PipelineRun{ID: "child1234567", PipelineID: s.OwnerID, Title: "Write report: go", Date: time.Now(), Running: true, ParentID: "p1", FollowUp: "write_report"}
	SavePipelineRun(s.DB, s.User, run)
	QueueAdd(run.ID, "run:followtest", run.Title, runQueueParams{User: s.User, OwnerID: s.OwnerID, Input: "the result", FollowUp: "write_report"}, "")
	(&AppCore{}).RegisterRunRestore("followtest", func(user, ownerID, key string) (RunSurface, bool) { return s, true })
	QueueRestore()
	got := waitFinished(t, s, run.ID)
	if name, input, _ := calls.last(); name != "Write report" || input != "the result" || got.Output != "Write report of: the result" {
		t.Errorf("resumed as %q over %q (output %q), want the follow-up over its stored input", name, input, got.Output)
	}
}

// Follow-ups are checked when the pipeline is saved.
func TestFollowUpsAreCheckedOnSave(t *testing.T) {
	base := func(fus ...PipelineDef) PipelineDef {
		return PipelineDef{Name: "p", Stages: []PipelineStage{{Name: "s", Kind: StageWorker, Prompt: "x"}}, FollowUps: fus}
	}
	ok := PipelineDef{Name: "Report", Stages: []PipelineStage{{Name: "r", Kind: StageWorker, Prompt: "{input}"}}}
	if err := base(ok).Validate(); err != nil {
		t.Fatalf("a good follow-up refused: %v", err)
	}
	nested := ok
	nested.FollowUps = []PipelineDef{ok}
	for name, bad := range map[string]PipelineDef{
		"unnamed":   base(PipelineDef{Stages: ok.Stages}),
		"duplicate": base(ok, ok),
		"nested":    base(nested),
		"no stages": base(PipelineDef{Name: "Empty"}),
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%s follow-up should be refused", name)
		}
	}
}

// A pipeline's suggest recipe backs its form's Suggest button: run inside the
// click with what is already typed as its input, its output handed back for the
// form (a JSON list as choices, text as text), and a probe that says whether
// there is a button to draw.
func TestSuggestRunsInsideTheClickAndFillsTheForm(t *testing.T) {
	s := runSurface(t, func(context.Context, string, map[string]string, PipelineSink) (string, error) { return "", nil })
	var gotInput string
	reply := "```json\n[\"Should cities ban cars?\", \"Is remote work here to stay?\"]\n```"
	s.Suggest = &PipelineDef{Name: "Find a topic", Stages: []PipelineStage{{Name: "pick", Prompt: "{input}"}}}
	s.FollowUpWork = func(def PipelineDef) RunWork {
		return func(_ context.Context, input string, _ map[string]string, _ PipelineSink) (string, error) {
			gotInput = input
			return reply, nil
		}
	}
	get := func(url string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		(&AppCore{}).ServeRuns(w, httptest.NewRequest(http.MethodGet, url, nil), s, "suggest")
		return w
	}
	if w := get("/suggest?probe=1"); !strings.Contains(w.Body.String(), `"label":"Find a topic"`) {
		t.Errorf("the probe names the button: %d %s", w.Code, w.Body.String())
	}
	w := get("/suggest?input=transport")
	if gotInput != "transport" {
		t.Errorf("what is typed is the suggest pipeline's input, got %q", gotInput)
	}
	var list []string
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list) != 2 {
		t.Errorf("a fenced JSON list comes back as JSON choices: %q %s", w.Header().Get("Content-Type"), w.Body.String())
	}
	reply = "Should cities ban cars?"
	if w := get("/suggest"); !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") || w.Body.String() != reply {
		t.Errorf("plain text comes back as text: %q %q", w.Header().Get("Content-Type"), w.Body.String())
	}
	s.Suggest = nil
	if w := get("/suggest?probe=1"); w.Code != http.StatusNoContent {
		t.Errorf("no suggest recipe, no button: probe gave %d", w.Code)
	}
	if w := get("/suggest"); w.Code != http.StatusNotFound {
		t.Errorf("no suggest recipe to run: %d", w.Code)
	}
}

// The pipeline's suggest reaches its run surface, and is checked on save.
func TestSuggestIsCarriedAndChecked(t *testing.T) {
	stages := []PipelineStage{{Name: "a", Prompt: "x"}}
	def := PipelineDef{Name: "d", Stages: stages, Suggest: &PipelineDef{Stages: stages}}
	if (&AppCore{}).RunsOf(PipelineRunSurface{Def: def}).Suggest == nil {
		t.Error("the run surface must carry the pipeline's suggest")
	}
	for name, bad := range map[string]*PipelineDef{
		"empty":           {},
		"with follow-ups": {Stages: stages, FollowUps: []PipelineDef{{Name: "f", Stages: stages}}},
		"a broken stage":  {Stages: []PipelineStage{{Name: "a", Prompt: "{stage:nope}"}}},
	} {
		if err := (PipelineDef{Name: "d", Stages: stages, Suggest: bad}).Validate(); err == nil {
			t.Errorf("a suggest that is %s should be refused", name)
		}
	}
}
