package orchestrate

// The declared-output editor.
//
// Declaring output is how a pipeline stops being a chain of prose — it
// is what makes fan_over-a-field, a loop's until and a branch's when
// possible — so leaving it to the tool meant the one thing that makes a
// stage useful to the NEXT stage was the one thing the page could not
// change.

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestAStagesDeclaredFieldsAreEditable(t *testing.T) {
	app, udb, user, def := stageEditFixture(t)

	// Add a field to a stage that had one, and change the existing one.
	body := `{"output":[
		{"kind":"asked","name":"queries","type":"list","desc":"what to search for","required":true},
		{"kind":"asked","name":"rationale","type":"string","desc":"why these"}
	]}`
	if w := postStage(t, app, user, def.ID, "plan", body); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	stored, _ := LoadPipelineDef(udb, user, def.ID)
	plan := stored.Stages[0]
	if len(plan.Output) != 2 {
		t.Fatalf("expected two fields, got %+v", plan.Output)
	}
	if plan.Output[0].Type != FieldList || !plan.Output[0].Required ||
		plan.Output[0].Desc != "what to search for" {
		t.Errorf("the edited field is wrong: %+v", plan.Output[0])
	}
	// And the pipeline still runs: dig fans over plan.queries, so the
	// field it reads had better still be a list of that name.
	if err := stored.Validate(); err != nil {
		t.Fatalf("editing output broke the pipeline: %v", err)
	}

	// A FILLED field is left out of what the model is asked for, holds
	// text whatever the row said, and carries no instruction.
	filled := `{"output":[
		{"kind":"asked","name":"queries","type":"list","desc":"what to search for"},
		{"kind":"filled","name":"asked_about","from":"{input}","type":"number","desc":"ignored","required":true}
	]}`
	if w := postStage(t, app, user, def.ID, "plan", filled); w.Code != 200 {
		t.Fatalf("save filled: %d %s", w.Code, w.Body.String())
	}
	stored, _ = LoadPipelineDef(udb, user, def.ID)
	got := stored.Stages[0].Output[1]
	if got.From != "{input}" {
		t.Errorf("the fill source did not land: %+v", got)
	}
	if got.Type != FieldString {
		t.Errorf("everything a variable carries is text, got %q", got.Type)
	}
	if len(stored.Stages[0].ModelOutput()) != 1 {
		t.Error("a filled field must not be asked of the model")
	}

	// The form LOADS what is stored, with the kind question answered
	// from what each field is rather than left blank.
	r := httptest.NewRequest("GET", "/api/pipelines/"+def.ID+"/stages?name=plan", nil)
	w := httptest.NewRecorder()
	app.handlePipelineOne(w, asUser(r, user))
	var rec map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &rec)
	rows, _ := rec["output"].([]any)
	if len(rows) != 2 {
		t.Fatalf("the form should load both fields: %v", rec["output"])
	}
	first, _ := rows[0].(map[string]any)
	second, _ := rows[1].(map[string]any)
	if first["kind"] != "asked" || second["kind"] != "filled" {
		t.Errorf("the kind should be answered from the field: %v / %v", first, second)
	}
}

// What the control does NOT edit must survive it. A form that silently
// deletes what it cannot show is the worst of the three possible
// behaviours — worse than refusing, and worse than showing it read-only.
func TestEditingOutputPreservesWhatTheControlCannotShow(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	def := SavePipelineDef(udb, PipelineDef{Owner: user, Name: "Routed",
		Stages: []PipelineStage{
			{Name: "triage", Kind: StageWorker, Prompt: "sort it", Output: []PipelineField{
				// An enum (a list inside a row) and a nested shape (a
				// shape inside a shape) — neither has a column.
				{Name: "lane", Type: FieldString, Desc: "which lane", Enum: []string{"logs", "docs"}},
				{Name: "detail", Type: FieldObject, Desc: "the pieces", Fields: []PipelineField{
					{Name: "where", Type: FieldString, Desc: "where to look"},
				}},
			}},
			{Name: "answer", Kind: StageWorker, Prompt: "from {stage:triage.lane}"},
		}})

	// Edit only the description of the first field.
	body := `{"output":[
		{"kind":"asked","name":"lane","type":"string","desc":"which lane, in one word"},
		{"kind":"asked","name":"detail","type":"object","desc":"the pieces"}
	]}`
	if w := postStage(t, app, user, def.ID, "triage", body); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	stored, _ := LoadPipelineDef(udb, user, def.ID)
	lane, detail := stored.Stages[0].Output[0], stored.Stages[0].Output[1]
	if lane.Desc != "which lane, in one word" {
		t.Errorf("the edit did not land: %q", lane.Desc)
	}
	if len(lane.Enum) != 2 {
		t.Errorf("the enum was dropped by a control that cannot show it: %v", lane.Enum)
	}
	if len(detail.Fields) != 1 || detail.Fields[0].Name != "where" {
		t.Errorf("the nested shape was dropped: %+v", detail.Fields)
	}

	// And the page says who owns those, rather than leaving them as an
	// unexplained absence.
	r := httptest.NewRequest("GET", "/orchestrate/pipeline?id="+def.ID, nil)
	w := httptest.NewRecorder()
	app.handlePipelinePage(w, asUser(r, user))
	page := w.Body.String()
	if !strings.Contains(page, "lane may only be logs | docs") {
		t.Error("an enum should still be visible somewhere")
	}
	if !strings.Contains(page, "owns the shapes that nest") {
		t.Error("the page should say why those are not editable here")
	}
}

// A filled field can only draw on values that exist EARLIER in the run,
// because stages run strictly in order and Validate refuses a forward
// reference — offering one would be offering a save that cannot succeed.
func TestFillOptionsOnlyOfferWhatHasAlreadyRun(t *testing.T) {
	def := PipelineDef{Name: "p", Stages: []PipelineStage{
		{Name: "plan", Kind: StageWorker, Prompt: "p", Output: []PipelineField{
			{Name: "queries", Type: FieldList, Desc: "what to search for"},
		}},
		{Name: "dig", Kind: StageWorker, Prompt: "look"},
		{Name: "answer", Kind: StageWorker, Prompt: "done", Output: []PipelineField{
			{Name: "verdict", Type: FieldString, Desc: "the answer"},
		}},
	}}
	var labels []string
	for _, o := range stageFillOptions(def, "dig") {
		labels = append(labels, o.Value)
	}
	joined := strings.Join(labels, " ")
	for _, want := range []string{"{input}", "{prev}", "{stage:plan}", "{stage:plan.queries}"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s from %v", want, labels)
		}
	}
	// Itself and everything after it are absent.
	for _, unwanted := range []string{"{stage:dig}", "{stage:answer}", "{stage:answer.verdict}"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("%s is not available to dig: %v", unwanted, labels)
		}
	}
	// The first stage can still be filled from the run's own input.
	if first := stageFillOptions(def, "plan"); len(first) != 3 {
		t.Errorf("the first stage should offer input and prev only: %+v", first)
	}
}
