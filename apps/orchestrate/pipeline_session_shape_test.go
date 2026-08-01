package orchestrate

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Three places have to agree on how a stored run identifies itself: the sidebar
// LIST, the single-run LOAD, and the panel that reads both. Two of them said
// "ID"; the list said "id". Every sidebar row came back undefined — no title,
// no date, and nothing to click — which reads exactly like a run history that
// was never saved, so the hunt starts at storage, where nothing is wrong.
//
// Cross-file agreement is the whole invariant, so this reads all three.
func TestSessionListMatchesTheLoadShapeAndThePanelDefaults(t *testing.T) {
	b, err := json.Marshal(pipelineSessionRow{ID: "r1", Title: "why", Date: time.Now()})
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

	// The LOAD handler, in this same file, answers with the same keys.
	src, err := os.ReadFile("pipeline_runs.go")
	if err != nil {
		t.Fatal(err)
	}
	load := string(src)[strings.Index(string(src), "func (T *OrchestrateApp) handlePipelineSessionOne"):]
	for _, key := range []string{`"ID"`, `"Title"`, `"Date"`, `"Blocks"`} {
		if !strings.Contains(load[:800], key) {
			t.Errorf("the single-run response dropped %s — it and the list have to describe one run the same way", key)
		}
	}

	// And the panel's defaults are still those keys. If someone lowercases the
	// runtime defaults, this list has to move with them.
	js, err := os.ReadFile("../../core/ui/assets/runtime/40_pipeline_panel.js")
	if err != nil {
		t.Fatal(err)
	}
	head := string(js)
	for field, want := range map[string]string{
		"session_id_field":    "ID",
		"session_title_field": "Title",
		"session_date_field":  "Date",
	} {
		re := regexp.MustCompile(`cfg\.` + field + `\s*\|\|\s*'([^']+)'`)
		m := re.FindStringSubmatch(head)
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
func TestRunInputAcceptsTheFieldTheAuthorNamed(t *testing.T) {
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
		if got := pipelineRunInput([]byte(c.body)); got != c.want {
			t.Errorf("%s: pipelineRunInput(%s) = %q, want %q", c.why, c.body, got, c.want)
		}
	}
}

// An action script's only route to a finished run. Without it the obvious
// button — "save this run to history" — was unwritable, so it got written
// against an invented env var and printed "run a debate first" forever, passing
// every check on the way.
func TestLatestPipelineRunSkipsOneStillRunning(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{}
	app.DB = root

	base := time.Now().Add(-time.Hour)
	app.savePipelineRun("u", PipelineRun{ID: "old", PipelineID: "p1", Title: "first", Date: base, Output: "done"})
	app.savePipelineRun("u", PipelineRun{ID: "live", PipelineID: "p1", Title: "second", Date: base.Add(time.Minute), Running: true})

	run, ok := app.PublicLatestPipelineRun("u", "p1")
	if !ok {
		t.Fatal("a finished run exists and must be found")
	}
	if run.ID != "old" {
		t.Errorf("a run still in flight has half a transcript — saving it as history is worse than none; got %q", run.ID)
	}
	// Another user's runs are a different history.
	if _, ok := app.PublicLatestPipelineRun("someone-else", "p1"); ok {
		t.Error("runs are per-user; a shared app must not hand over someone else's")
	}
	if _, ok := app.PublicLatestPipelineRun("u", ""); ok {
		t.Error("no pipeline bound, no run")
	}
}
