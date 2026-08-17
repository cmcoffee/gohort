package orchestrate

// The per-stage form's endpoint. What matters: a save MERGES, a rename
// rewrites the references that would otherwise refuse the save, and a
// control that is hidden is not holding a value.

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func stageEditFixture(t *testing.T) (*OrchestrateApp, Database, string, PipelineDef) {
	t.Helper()
	app, udb, user := newTestOrchestrate(t)
	def := SavePipelineDef(udb, PipelineDef{Owner: user, Name: "Research",
		Stages: []PipelineStage{
			{Name: "plan", Kind: StageWorker, Prompt: "break it up", Model: "lead",
				Output: []PipelineField{{Name: "queries", Type: FieldList, Desc: "q"}}},
			{Name: "dig", Kind: StageFanout, FanOver: "plan.queries", Prompt: "research {item}",
				Tools: []string{"web_search"}},
			{Name: "answer", Kind: StageWorker, Prompt: "from {stage:dig}"},
		}})
	return app, udb, user, def
}

func postStage(t *testing.T, app *OrchestrateApp, user, id, name, body string) *httptest.ResponseRecorder {
	t.Helper()
	url := "/api/pipelines/" + id + "/stages"
	if name != "" {
		url += "?name=" + name
	}
	r := httptest.NewRequest("POST", url, strings.NewReader(body))
	w := httptest.NewRecorder()
	app.handlePipelineOne(w, asUser(r, user))
	return w
}

func TestAStageSaveMergesRatherThanReplaces(t *testing.T) {
	app, udb, user, def := stageEditFixture(t)

	// The panel sends one field. Everything else must survive.
	w := postStage(t, app, user, def.ID, "dig", `{"prompt":"research {item} thoroughly"}`)
	if w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	stored, _ := LoadPipelineDef(udb, user, def.ID)
	dig := stored.Stages[1]
	if dig.Prompt != "research {item} thoroughly" {
		t.Errorf("the edit did not land: %q", dig.Prompt)
	}
	if dig.FanOver != "plan.queries" || len(dig.Tools) != 1 || dig.Kind != StageFanout {
		t.Errorf("a partial save clobbered what it never showed: %+v", dig)
	}

	// GET returns the flat record the form binds to.
	r := httptest.NewRequest("GET", "/api/pipelines/"+def.ID+"/stages?name=plan", nil)
	rec := httptest.NewRecorder()
	app.handlePipelineOne(rec, asUser(r, user))
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["model"] != "lead" || got["kind"] != "worker" {
		t.Errorf("the record the form loads is wrong: %v", got)
	}

	// A form still addressing a stage that is gone must not resurrect it.
	w = postStage(t, app, user, def.ID, "vanished", `{"prompt":"x"}`)
	if w.Code != 404 {
		t.Errorf("a stale form should 404, got %d", w.Code)
	}
	if len(ListPipelineDefs(udb, user)) != 1 {
		t.Error("and must not create anything")
	}
}

// Renaming through the form has to rewrite the references, or the
// validator refuses the save for a reason the author did not cause —
// and the author cannot fix it, because the reference is in another
// stage's prompt.
func TestRenamingAStageThroughTheFormKeepsThePipelineRunnable(t *testing.T) {
	app, udb, user, def := stageEditFixture(t)

	w := postStage(t, app, user, def.ID, "plan", `{"name":"decompose"}`)
	if w.Code != 200 {
		t.Fatalf("a rename should succeed: %d %s", w.Code, w.Body.String())
	}
	stored, _ := LoadPipelineDef(udb, user, def.ID)
	if stored.Stages[0].Name != "decompose" {
		t.Fatalf("not renamed: %q", stored.Stages[0].Name)
	}
	if stored.Stages[1].FanOver != "decompose.queries" {
		t.Errorf("the reference was not rewritten: %q", stored.Stages[1].FanOver)
	}
	if err := stored.Validate(); err != nil {
		t.Fatalf("the saved pipeline does not run: %v", err)
	}

	// Renaming onto an existing name would point its references
	// somewhere else.
	w = postStage(t, app, user, def.ID, "dig", `{"name":"answer"}`)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "already exists") {
		t.Errorf("a colliding rename should refuse: %d %s", w.Code, w.Body.String())
	}
}

// Removal refuses while another stage reads it, and says which — the
// message is the fix, because a pipeline's references live in prose.
func TestRemovingAStageThroughTheFormSaysWhatIsInTheWay(t *testing.T) {
	app, udb, user, def := stageEditFixture(t)

	r := httptest.NewRequest("DELETE", "/api/pipelines/"+def.ID+"/stages?name=plan", nil)
	w := httptest.NewRecorder()
	app.handlePipelineOne(w, asUser(r, user))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "dig (fan_over)") {
		t.Errorf("should refuse and name the reader: %d %s", w.Code, w.Body.String())
	}

	r = httptest.NewRequest("DELETE", "/api/pipelines/"+def.ID+"/stages?name=answer", nil)
	w = httptest.NewRecorder()
	app.handlePipelineOne(w, asUser(r, user))
	if w.Code != 200 {
		t.Fatalf("an unread stage should remove: %d %s", w.Code, w.Body.String())
	}
	stored, _ := LoadPipelineDef(udb, user, def.ID)
	if len(stored.Stages) != 2 {
		t.Errorf("it did not go: %d stages", len(stored.Stages))
	}
}

// The rule the machine editor learned three times: a control that is
// hidden must not be holding a value. Here it is worse than a stale
// display — a pipeline's validator REFUSES what it cannot resolve, so a
// hidden fan_over on a stage that is no longer a fanout is a save
// nobody can make succeed.
func TestAStageKeepsControlsForWhatItStillHolds(t *testing.T) {
	_, _, _, def := stageEditFixture(t)
	// A stage that is a worker but still carries a fanout's field.
	odd := PipelineStage{Name: "dig", Kind: StageWorker, FanOver: "plan.queries",
		Tools: []string{"web_search"}}
	shown := map[string]string{}
	for _, f := range stageFormFields(def, odd, editorCatalog{}) {
		if f.Field != "" {
			shown[f.Field] = f.ShowWhen
		}
	}
	if got, ok := shown["fan_over"]; !ok || got != "" {
		t.Errorf("fan_over holds a value and hides behind %q", got)
	}
	if got := shown["tools"]; got != "" {
		t.Errorf("tools holds a value and hides behind %q", got)
	}
	// An EMPTY one still gets out of the way, or every stage shows every
	// control and they all read as the same thing.
	plain := PipelineStage{Name: "x", Kind: StageWorker, Prompt: "p"}
	for _, f := range stageFormFields(def, plain, editorCatalog{}) {
		switch f.Field {
		case "fan_over":
			if f.ShowWhen != "kind:fanout" {
				t.Errorf("an empty fan_over should be gated, got %q", f.ShowWhen)
			}
		case "when", "skip_to":
			if !strings.Contains(f.ShowWhen, "branch") {
				t.Errorf("%s should be gated to a branch, got %q", f.Field, f.ShowWhen)
			}
		}
	}
}

// The validator's own sentence is the reply, because it says what is
// wrong far better than "bad request" and the form shows it inline.
func TestARefusedStageEditSaysWhy(t *testing.T) {
	app, _, user, def := stageEditFixture(t)
	w := postStage(t, app, user, def.ID, "dig", `{"fan_over":"nowhere.at_all"}`)
	if w.Code != 400 {
		t.Fatalf("expected a refusal, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "nowhere") {
		t.Errorf("the reason should name the reference: %s", w.Body.String())
	}
}
