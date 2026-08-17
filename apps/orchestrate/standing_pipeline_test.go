package orchestrate

// A schedule whose target is a pipeline.
//
// Before this, "run the research pipeline nightly" needed a wrapper agent
// whose entire job was to decide to call run_<pipeline> — a model call
// spent reaching a foregone conclusion, and an agent in somebody's fleet
// that was pure plumbing.

import (
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestAScheduleNamesExactlyOneTarget(t *testing.T) {
	// Both is refused rather than resolved by precedence: whichever the
	// runner checked first would be the one that ran, forever, and which
	// one depends on the order of an if.
	both := StandingAgent{Name: "n", Owner: "u", AgentID: "ag", PipelineID: "pl"}
	if err := both.ValidateTarget(); err == nil {
		t.Error("naming an agent AND a pipeline should be refused")
	} else if !strings.Contains(err.Error(), "not both") {
		t.Errorf("the refusal should say why: %v", err)
	}
	// Neither is refused too: a schedule that fires nothing still fires,
	// recording an attention entry every time — a job that exists to fail.
	if err := (StandingAgent{Name: "n", Owner: "u"}).ValidateTarget(); err == nil {
		t.Error("a schedule with no target should be refused")
	}
	for _, ok := range []StandingAgent{
		{Name: "n", Owner: "u", AgentID: "ag"},
		{Name: "n", Owner: "u", PipelineID: "pl"},
	} {
		if err := ok.ValidateTarget(); err != nil {
			t.Errorf("%+v should be valid: %v", ok, err)
		}
	}
	if !(StandingAgent{PipelineID: "pl"}).TargetsPipeline() {
		t.Error("a pipeline schedule should say so")
	}
	if (StandingAgent{AgentID: "ag"}).TargetsPipeline() {
		t.Error("an agent schedule is not a pipeline one")
	}
}

// A target that is gone should be visible where somebody is looking at
// the schedule, not discovered when it fires. The pipeline case was
// unchecked — not a false positive (agentExists answers true for an
// empty id, deliberately) but a missing check, which is the quieter half
// of the same problem.
func TestAScheduleWhosePipelineIsGoneReportsIt(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	// standingAgentDependencyError resolves through the global RootDB, the
	// same way agentExists does — point it at this test's store.
	prev := RootDB
	RootDB = app.DB
	t.Cleanup(func() { RootDB = prev })

	def := SavePipelineDef(udb, PipelineDef{Owner: user, Name: "Nightly",
		Stages: []PipelineStage{{Name: "s", Kind: StageWorker, Prompt: "p"}}})

	live := StandingAgent{Name: "nightly", Owner: user, PipelineID: def.ID}
	if got := standingAgentDependencyError(live); got != "" {
		t.Errorf("a schedule pointing at a stored pipeline is healthy: %q", got)
	}
	gone := StandingAgent{Name: "nightly", Owner: user, PipelineID: "pl-vanished"}
	got := standingAgentDependencyError(gone)
	if !strings.Contains(got, "pipeline was deleted") || !strings.Contains(got, "pl-vanished") {
		t.Errorf("a deleted pipeline should be reported by id: %q", got)
	}
	// And an agent schedule still reports the agent, not the pipeline.
	if got := standingAgentDependencyError(StandingAgent{Owner: user, AgentID: "ag-vanished"}); !strings.Contains(got, "agent was deleted") {
		t.Errorf("the agent case should be unchanged: %q", got)
	}
}

// The console has to offer the right KIND of target, and accept only
// that kind. A relink picker listing agents for a schedule that runs a
// pipeline is worse than no picker: every choice in it gets refused.
func TestRelinkOffersAndAcceptsTheRightTargetKind(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	prev := RootDB
	RootDB = app.DB
	t.Cleanup(func() { RootDB = prev })

	good := SavePipelineDef(udb, PipelineDef{Owner: user, Name: "Nightly",
		Stages: []PipelineStage{{Name: "s", Kind: StageWorker, Prompt: "p"}}})
	agent, err := saveAgent(udb, AgentRecord{Owner: user, Name: "Wren", OrchestratorPrompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	SaveStandingAgent(RootDB, StandingAgent{
		Name: "nightly", Owner: user, PipelineID: "pl-gone", Broken: true,
		BrokenReason: "its target pipeline was deleted (id pl-gone)"})
	SaveStandingAgent(RootDB, StandingAgent{
		Name: "morning", Owner: user, AgentID: "ag-gone", Broken: true})

	// The picker, asked about the PIPELINE schedule, offers pipelines.
	r := httptest.NewRequest("GET", "/api/console/agent-options?row=nightly", nil)
	w := httptest.NewRecorder()
	app.handleConsoleAgentOptions(w, asUser(r, user))
	body := w.Body.String()
	if !strings.Contains(body, good.ID) || !strings.Contains(body, "Nightly") {
		t.Errorf("the pipeline schedule's picker should list pipelines:\n%s", body)
	}
	if strings.Contains(body, agent.ID) {
		t.Errorf("it must not offer agents — every one would be refused:\n%s", body)
	}
	// Asked about the AGENT schedule, it offers agents, exactly as before.
	r = httptest.NewRequest("GET", "/api/console/agent-options?row=morning", nil)
	w = httptest.NewRecorder()
	app.handleConsoleAgentOptions(w, asUser(r, user))
	if !strings.Contains(w.Body.String(), agent.ID) {
		t.Errorf("the agent schedule's picker should still list agents:\n%s", w.Body.String())
	}

	// Relink accepts a pipeline for the pipeline schedule, and clears broken.
	r = httptest.NewRequest("POST", "/api/console/agents/relink?id=nightly&value="+good.ID, nil)
	w = httptest.NewRecorder()
	app.handleConsoleAgentRelink(w, asUser(r, user))
	if w.Code != 204 {
		t.Fatalf("relink: %d %s", w.Code, w.Body.String())
	}
	sa, _ := GetStandingAgent(RootDB, user, "nightly")
	if sa.PipelineID != good.ID || sa.Broken {
		t.Errorf("the schedule was not repointed: %+v", sa)
	}
	// And it must NOT have grown an agent target alongside it — the one
	// state ValidateTarget exists to refuse.
	if sa.AgentID != "" {
		t.Errorf("relink set an agent on a pipeline schedule: %q", sa.AgentID)
	}
	if err := sa.ValidateTarget(); err != nil {
		t.Errorf("the repointed schedule is invalid: %v", err)
	}

	// An agent id offered to a pipeline schedule is refused rather than
	// stored — this is what would have happened on every pick from the
	// old picker.
	r = httptest.NewRequest("POST", "/api/console/agents/relink?id=nightly&value="+agent.ID, nil)
	w = httptest.NewRecorder()
	app.handleConsoleAgentRelink(w, asUser(r, user))
	if w.Code != 400 {
		t.Errorf("an agent is not a pipeline: %d %s", w.Code, w.Body.String())
	}

	// The schedules rail row says WHAT it runs.
	r = httptest.NewRequest("GET", "/api/schedules", nil)
	w = httptest.NewRecorder()
	app.handleSchedules(w, asUser(r, user))
	if !strings.Contains(w.Body.String(), "pipeline · Nightly") {
		t.Errorf("the row should name the pipeline it fires:\n%s", w.Body.String())
	}
}
