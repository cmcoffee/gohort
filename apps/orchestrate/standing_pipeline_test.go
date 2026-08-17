package orchestrate

// A schedule whose target is a pipeline.
//
// Before this, "run the research pipeline nightly" needed a wrapper agent
// whose entire job was to decide to call run_<pipeline> — a model call
// spent reaching a foregone conclusion, and an agent in somebody's fleet
// that was pure plumbing.

import (
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
