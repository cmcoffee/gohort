package orchestrate

// Authoring the stages inside a stage. Bodies were reachable only from
// the pipeline tool, an import, or a revise; these are the paths that
// make them editable on the page.

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func bodyFixture(t *testing.T) (*OrchestrateApp, Database, string, PipelineDef) {
	t.Helper()
	app, udb, user := newTestOrchestrate(t)
	def := SavePipelineDef(udb, PipelineDef{
		Owner: user, Name: "Research",
		Stages: []PipelineStage{
			{Name: "plan", Prompt: "plan {input}", Output: []PipelineField{{Name: "queries", Type: FieldList}}},
			{Name: "dig", Kind: StageFanout, FanOver: "plan.queries", Body: []PipelineStage{
				{Name: "look", Prompt: "look at {item}"},
			}},
		},
	})
	return app, udb, user, def
}

func post(t *testing.T, app *OrchestrateApp, udb Database, user string, def PipelineDef, query, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/pipelines/"+def.ID+"/stages"+query, strings.NewReader(body))
	w := httptest.NewRecorder()
	app.handlePipelineStages(w, asUser(r, user), udb, user, def)
	return w
}

// A path addresses a body stage, and it is unambiguous because a stage
// name may not contain a dot.
func TestABodyStageIsAddressedByPath(t *testing.T) {
	app, udb, user, def := bodyFixture(t)

	r := httptest.NewRequest("GET", "/api/pipelines/"+def.ID+"/stages?name=dig.look", nil)
	w := httptest.NewRecorder()
	app.handlePipelineStages(w, asUser(r, user), udb, user, def)
	if w.Code != 200 {
		t.Fatalf("want the body stage, got %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["name"] != "look" {
		t.Errorf("the record should be the body stage's own: %v", got)
	}
}

func TestEditingABodyStageSavesInPlace(t *testing.T) {
	app, udb, user, def := bodyFixture(t)
	if w := post(t, app, udb, user, def, "?name=dig.look",
		`{"prompt":"read {item} carefully"}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	saved, _ := LoadPipelineDef(udb, user, def.ID)
	if got := saved.Stages[1].Body[0].Prompt; got != "read {item} carefully" {
		t.Errorf("the edit did not land in the body: %q", got)
	}
	if len(saved.Stages[1].Body) != 1 {
		t.Errorf("editing should not add a stage: %d", len(saved.Stages[1].Body))
	}
}

func TestAddingAStepToABody(t *testing.T) {
	app, udb, user, def := bodyFixture(t)
	if w := post(t, app, udb, user, def, "?parent=dig",
		`{"name":"judge","kind":"worker","prompt":"judge {stage:look}"}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	saved, _ := LoadPipelineDef(udb, user, def.ID)
	body := saved.Stages[1].Body
	if len(body) != 2 || body[1].Name != "judge" {
		t.Fatalf("the step should land at the end of the body: %+v", body)
	}
	// And a step may read a SIBLING, which is the thing a body is for.
	if body[1].Prompt != "judge {stage:look}" {
		t.Errorf("the prompt should survive: %q", body[1].Prompt)
	}
}

// A rename rewrites what its siblings call it, and nothing else: nothing
// outside the body may name a body stage.
func TestRenamingABodyStageRewritesItsSiblings(t *testing.T) {
	app, udb, user, def := bodyFixture(t)
	post(t, app, udb, user, def, "?parent=dig", `{"name":"judge","prompt":"judge {stage:look}"}`)
	saved, _ := LoadPipelineDef(udb, user, def.ID)

	if w := post(t, app, udb, user, saved, "?name=dig.look", `{"name":"scan"}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	saved, _ = LoadPipelineDef(udb, user, def.ID)
	body := saved.Stages[1].Body
	if body[0].Name != "scan" {
		t.Errorf("the rename did not land: %q", body[0].Name)
	}
	if !strings.Contains(body[1].Prompt, "{stage:scan}") {
		t.Errorf("a sibling's reference should have been rewritten: %q", body[1].Prompt)
	}
}

// Refused by a SIBLING that reads it, which is the only thing that can.
func TestRemovingABodyStageASiblingReadsIsRefused(t *testing.T) {
	app, udb, user, def := bodyFixture(t)
	post(t, app, udb, user, def, "?parent=dig", `{"name":"judge","prompt":"judge {stage:look}"}`)
	saved, _ := LoadPipelineDef(udb, user, def.ID)

	r := httptest.NewRequest("DELETE", "/api/pipelines/"+saved.ID+"/stages?name=dig.look", nil)
	w := httptest.NewRecorder()
	app.handlePipelineStages(w, asUser(r, user), udb, saved.Owner, saved)
	if w.Code == 200 {
		t.Fatal("removing a step another step reads should be refused")
	}
	if !strings.Contains(w.Body.String(), "judge") {
		t.Errorf("the refusal should name who reads it: %s", w.Body.String())
	}

	// And one nothing reads goes.
	r = httptest.NewRequest("DELETE", "/api/pipelines/"+saved.ID+"/stages?name=dig.judge", nil)
	w = httptest.NewRecorder()
	app.handlePipelineStages(w, asUser(r, user), udb, saved.Owner, saved)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	after, _ := LoadPipelineDef(udb, user, def.ID)
	if len(after.Stages[1].Body) != 1 {
		t.Errorf("the step should be gone: %+v", after.Stages[1].Body)
	}
}

// A stage with no body has nothing to add a step to, and says so rather
// than growing one.
func TestAddingAStepToAStageThatHasNoBodyIsRefused(t *testing.T) {
	app, udb, user, def := bodyFixture(t)
	w := post(t, app, udb, user, def, "?parent=plan", `{"name":"nope","prompt":"x"}`)
	if w.Code == 200 {
		t.Fatal("a worker stage has no body")
	}
	if !strings.Contains(w.Body.String(), "no body") {
		t.Errorf("the refusal should say why: %s", w.Body.String())
	}
}

// The kinds a body may not be are not offered, because a choice that gets
// refused on save arrives after somebody wrote the prompt for it.
func TestABodyStageIsNotOfferedTheKindsItCannotBe(t *testing.T) {
	var labels []string
	for _, o := range bodyKindOptions() {
		labels = append(labels, o.Value)
	}
	joined := strings.Join(labels, ",")
	for _, forbidden := range []string{"loop", "fanout"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("bodies do not nest, so %q should not be offered: %v", forbidden, labels)
		}
	}
	for _, want := range []string{"worker", "agent", "machine"} {
		if !strings.Contains(joined, want) {
			t.Errorf("a body step should be able to be %q: %v", want, labels)
		}
	}
}
