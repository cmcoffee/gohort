package orchestrate

// Pipelines, on the page where things are kept.
//
// Everything the HTTP layer served — list, export, import, delete — was
// reachable from nothing: pipelines were authored in chat and attached
// from a picker, so "what do I have, and is any of it live" had no
// answer outside asking an agent.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func pipelinePageFixture(t *testing.T) (*OrchestrateApp, Database, string, PipelineDef) {
	t.Helper()
	app, udb, user := newTestOrchestrate(t)
	def := SavePipelineDef(udb, PipelineDef{Owner: user, Name: "Research",
		Description: "Decompose, dig, synthesize.",
		Stages: []PipelineStage{
			{Name: "plan", Kind: StageWorker, Prompt: "break it up",
				Output: []PipelineField{{Name: "queries", Type: FieldList, Desc: "searches"}}},
			{Name: "dig", Kind: StageFanout, FanOver: "plan.queries", Prompt: "research {item}",
				Tools: []string{"web_search"}},
			{Name: "answer", Kind: StageWorker, Prompt: "synthesize {stage:dig}", Model: "lead"},
		}})
	return app, udb, user, def
}

func TestThePipelineListSaysWhatEachOneIsAndWhoCanCallIt(t *testing.T) {
	app, udb, user, def := pipelinePageFixture(t)
	// An agent that can call it, and one that cannot.
	if _, err := saveAgent(udb, AgentRecord{Owner: user, Name: "Wren",
		OrchestratorPrompt: "hi", AttachedPipelines: []string{def.ID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := saveAgent(udb, AgentRecord{Owner: user, Name: "Idle", OrchestratorPrompt: "hi"}); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest("GET", "/orchestrate/api/pipelines", nil)
	w := httptest.NewRecorder()
	app.handlePipelines(w, asUser(r, user))
	body := w.Body.String()

	// An unattached pipeline is inert — a tool no agent can call — so
	// this is the single most useful fact about one.
	if !strings.Contains(body, `"used_by_text":"Wren"`) {
		t.Errorf("the row should say who can call it:\n%s", body)
	}
	// What it is MADE of, so a list of names is not the only thing to
	// go on.
	if !strings.Contains(body, "2 worker") || !strings.Contains(body, "1 fanout") {
		t.Errorf("the row should say what kinds of stages it has:\n%s", body)
	}
	if !strings.Contains(body, `"edit_url":"/orchestrate/pipeline?id=`+def.ID+`"`) {
		t.Errorf("the row should open the pipeline:\n%s", body)
	}
	if !strings.Contains(body, `"stage_names":["plan","dig","answer"]`) {
		t.Errorf("and name its stages in order:\n%s", body)
	}
}

func TestThePipelinePageReadsInTheOrderItRuns(t *testing.T) {
	app, _, user, def := pipelinePageFixture(t)
	r := httptest.NewRequest("GET", "/orchestrate/pipeline?id="+def.ID, nil)
	w := httptest.NewRecorder()
	app.handlePipelinePage(w, asUser(r, user))
	if w.Code != 200 {
		t.Fatalf("the page does not render: %d", w.Code)
	}
	body := w.Body.String()

	// One section per stage, numbered, in run order — the rail is the
	// stage list.
	for _, want := range []string{"1. plan", "2. dig", "3. answer"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page is missing %q", want)
		}
	}
	// Each says what KIND it is, in the spec's own vocabulary.
	if !strings.Contains(body, "Runs once per element of plan.queries") {
		t.Error("a fanout should say what it fans over")
	}
	if !strings.Contains(body, "A worker step") {
		t.Error("a plain stage should say so")
	}
	// And what it carries that a prompt does not show.
	for _, want := range []string{"tools: web_search", "returns: queries", "model: lead"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not show %q", want)
		}
	}
	// Name and description are editable, because the description is what
	// an agent reads when deciding whether to call it.
	if !strings.Contains(body, `"field":"description"`) || !strings.Contains(body, `"method":"PUT"`) {
		t.Error("the pipeline's own fields should be editable")
	}
	// Export is here, next to the thing being exported.
	if !strings.Contains(body, "/api/pipelines/"+def.ID+"/export") {
		t.Error("no export on the page")
	}
	// A pipeline nobody can reach is a 404, not somebody else's.
	r = httptest.NewRequest("GET", "/orchestrate/pipeline?id=nope", nil)
	w = httptest.NewRecorder()
	app.handlePipelinePage(w, asUser(r, user))
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for an unknown pipeline, got %d", w.Code)
	}
}

// The advice the tool reports has a home in the UI too, phrased the same
// way the machine editor phrases it.
func TestThePipelinePageShowsWhatIsWorthALook(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	def := SavePipelineDef(udb, PipelineDef{Owner: user, Name: "Bad",
		Stages: []PipelineStage{
			{Name: "plan", Kind: StageWorker, Prompt: "plan it. Respond only with valid JSON.",
				Output: []PipelineField{{Name: "queries", Type: FieldList, Desc: "q"}}},
			{Name: "answer", Kind: StageWorker, Prompt: "answer {stage:plan.queries}"},
		}})
	r := httptest.NewRequest("GET", "/orchestrate/pipeline?id="+def.ID, nil)
	w := httptest.NewRecorder()
	app.handlePipelinePage(w, asUser(r, user))
	body := w.Body.String()
	if !strings.Contains(body, "Worth a look") || !strings.Contains(body, "stage plan") {
		t.Errorf("the finding the tool reports should be on the page too:\n%s", body[:min(len(body), 400)])
	}
	// And it must not read as breakage.
	if !strings.Contains(body, "Nothing here refuses a save") {
		t.Error("advice should be phrased as a suggestion")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
