package core

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
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
