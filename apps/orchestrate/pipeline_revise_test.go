package orchestrate

// Describe a change, against a pipeline that already exists.
//
// The machine version with one real difference: a pipeline is
// VALIDATED at every door, so a revision that would not run is refused
// rather than stored — which makes the repair pass load-bearing instead
// of a polish step.

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func reviseP(t *testing.T, app *OrchestrateApp, user, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/pipelines/"+id+"/revise", strings.NewReader(body))
	w := httptest.NewRecorder()
	app.handlePipelineOne(w, asUser(r, user))
	return w
}

func TestAPipelineRevisionKeepsIdentityAndSaysWhatItChanged(t *testing.T) {
	app, udb, user, def := stageEditFixture(t)
	// A well-behaved revision: one stage added, everything else back
	// exactly as it went in — including plan's model, which is the sort
	// of setting a careless rewrite drops silently.
	app.LLM = &stubLLM{reply: `{"name":"Research","stages":[
		{"name":"plan","kind":"worker","prompt":"break it up","model":"lead",
		 "output":[{"name":"queries","type":"list","desc":"q"}]},
		{"name":"dig","kind":"fanout","fan_over":"plan.queries","prompt":"research {item}",
		 "tools":["web_search"]},
		{"name":"check","kind":"worker","prompt":"verify {stage:dig}"},
		{"name":"answer","kind":"worker","prompt":"from {stage:dig}"}]}`}

	w := reviseP(t, app, user, def.ID, `{"description":"add a stage that checks the sources"}`)
	if w.Code != 200 {
		t.Fatalf("revise: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		ID      string   `json:"id"`
		Changed []string `json:"changed"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.ID != def.ID {
		t.Fatalf("the id must survive — agents attach by it: %q vs %q", out.ID, def.ID)
	}
	joined := strings.Join(out.Changed, "; ")
	if !strings.Contains(joined, "added check") {
		t.Errorf("the reply should name what changed: %q", joined)
	}
	if strings.Contains(joined, "changed plan") {
		t.Errorf("an untouched stage should not be reported as changed: %q", joined)
	}

	stored, _ := LoadPipelineDef(udb, user, def.ID)
	if len(stored.Stages) != 4 {
		t.Fatalf("the revision was not stored: %d stages", len(stored.Stages))
	}
	if stored.Stages[0].Prompt != "break it up" {
		t.Error("a prompt the change did not touch was rewritten")
	}
}

// The difference from machines: a pipeline that would not run is not
// stored. Machines keep an imperfect draft because their checklist is
// where problems belong; a pipeline has no such place, and every other
// door refuses too.
func TestAPipelineRevisionThatWouldNotRunChangesNothing(t *testing.T) {
	app, udb, user, def := stageEditFixture(t)
	// Both attempts come back referencing a stage that does not exist.
	app.LLM = &stubLLM{reply: `{"name":"Research","stages":[
		{"name":"plan","kind":"worker","prompt":"read {stage:nowhere.at_all}"}]}`}

	w := reviseP(t, app, user, def.ID, `{"description":"break it"}`)
	if w.Code == 200 {
		t.Fatalf("a revision that would not run must be refused, got 200: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Nothing was changed") {
		t.Errorf("the refusal should say the pipeline is untouched: %s", w.Body.String())
	}
	stored, _ := LoadPipelineDef(udb, user, def.ID)
	if len(stored.Stages) != 3 || stored.Previous != nil {
		t.Errorf("a refused revision must leave the pipeline exactly as it was: %d stages", len(stored.Stages))
	}
}

// A revision can rewrite every prompt in the pipeline, and prompts are
// the part somebody actually wrote.
func TestAPipelineRevisionCanBeTakenBack(t *testing.T) {
	app, udb, user, def := stageEditFixture(t)
	app.LLM = &stubLLM{reply: `{"name":"Research","stages":[
		{"name":"plan","kind":"worker","prompt":"SOMETHING ELSE ENTIRELY",
		 "output":[{"name":"queries","type":"list","desc":"q"}]},
		{"name":"dig","kind":"fanout","fan_over":"plan.queries","prompt":"research {item}"},
		{"name":"answer","kind":"worker","prompt":"from {stage:dig}"}]}`}

	if w := reviseP(t, app, user, def.ID, `{"description":"change it"}`); w.Code != 200 {
		t.Fatalf("revise: %d %s", w.Code, w.Body.String())
	}
	after, _ := LoadPipelineDef(udb, user, def.ID)
	if after.Previous == nil {
		t.Fatal("nothing was kept, so there is nothing to undo")
	}
	if after.Previous.Previous != nil {
		t.Error("the snapshot should not carry a snapshot")
	}
	// The page offers it, and only now that there is one.
	r := httptest.NewRequest("GET", "/orchestrate/pipeline?id="+def.ID, nil)
	w := httptest.NewRecorder()
	app.handlePipelinePage(w, asUser(r, user))
	if !strings.Contains(w.Body.String(), "Undo the revision") {
		t.Error("the page does not offer the undo it just made possible")
	}

	r = httptest.NewRequest("POST", "/api/pipelines/"+def.ID+"/undo", nil)
	w = httptest.NewRecorder()
	app.handlePipelineOne(w, asUser(r, user))
	if w.Code != 200 {
		t.Fatalf("undo: %d %s", w.Code, w.Body.String())
	}
	back, _ := LoadPipelineDef(udb, user, def.ID)
	if back.Stages[0].Prompt != "break it up" {
		t.Fatalf("the original was not restored: %q", back.Stages[0].Prompt)
	}
	if back.Previous != nil {
		t.Error("undo is one step, not a toggle")
	}
	// With nothing to undo, the button is gone and the endpoint refuses.
	r = httptest.NewRequest("GET", "/orchestrate/pipeline?id="+def.ID, nil)
	w = httptest.NewRecorder()
	app.handlePipelinePage(w, asUser(r, user))
	if strings.Contains(w.Body.String(), "Undo the revision") {
		t.Error("a pipeline with no revision behind it should not offer to undo one")
	}
	r = httptest.NewRequest("POST", "/api/pipelines/"+def.ID+"/undo", nil)
	w = httptest.NewRecorder()
	app.handlePipelineOne(w, asUser(r, user))
	if w.Code != 400 {
		t.Errorf("a second undo should refuse, got %d", w.Code)
	}
}

// The snapshot is storage, not a recipe.
func TestAnExportedPipelineLeavesTheSnapshotBehind(t *testing.T) {
	d := PipelineDef{Name: "p", Stages: []PipelineStage{{Name: "s", Kind: StageWorker, Prompt: "p"}}}
	d.Previous = &PipelineDef{Name: "p", Stages: []PipelineStage{{Name: "s", Kind: StageWorker, Prompt: "old"}}}
	if ExportPipeline(d).Previous != nil {
		t.Error("the undo snapshot should not travel")
	}
	raw, _ := json.Marshal(ExportPipeline(d))
	if strings.Contains(string(raw), "previous") {
		t.Errorf("and should not appear in the recipe:\n%s", raw)
	}
}

// The reason the report exists, from life: the first version of the test
// above omitted plan's "model": "lead" from the stub reply, and the
// report caught it. A revision that quietly drops a setting nobody
// mentioned is exactly the failure this door risks, and "revised" as a
// reply would have hidden it.
func TestARevisionThatDropsASettingIsReported(t *testing.T) {
	app, _, user, def := stageEditFixture(t)
	app.LLM = &stubLLM{reply: `{"name":"Research","stages":[
		{"name":"plan","kind":"worker","prompt":"break it up",
		 "output":[{"name":"queries","type":"list","desc":"q"}]},
		{"name":"dig","kind":"fanout","fan_over":"plan.queries","prompt":"research {item}",
		 "tools":["web_search"]},
		{"name":"answer","kind":"worker","prompt":"from {stage:dig}"}]}`}

	w := reviseP(t, app, user, def.ID, `{"description":"tidy it up"}`)
	if w.Code != 200 {
		t.Fatalf("revise: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Changed []string `json:"changed"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	joined := strings.Join(out.Changed, "; ")
	if !strings.Contains(joined, "plan (wiring)") {
		t.Errorf("dropping plan's pinned model should be reported: %q", joined)
	}
	// Prose is untouched, so it is NOT reported as an instructions
	// change — the two are distinguished because they mean different
	// things to somebody deciding whether to undo.
	if strings.Contains(joined, "plan (instructions)") {
		t.Errorf("the prompt did not change: %q", joined)
	}
}
