package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// harness: a caller agent in one user store, plus whatever pipelines a test
// wants reachable from it.
func newPipelineDispatchTurn(t *testing.T, attach func(udb Database) []string) *chatTurn {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	var attached []string
	if attach != nil {
		attached = attach(udb)
	}
	caller, err := saveAgent(udb, AgentRecord{
		Name: "Caller", Owner: "u", DispatchMode: dispatchAll,
		OrchestratorPrompt: "p", AttachedPipelines: attached,
	})
	if err != nil {
		t.Fatalf("save caller: %v", err)
	}
	return &chatTurn{user: "u", udb: udb, agent: caller, ctx: context.Background()}
}

func savePipeline(t *testing.T, udb Database, name string, global bool) PipelineDef {
	t.Helper()
	def := SavePipelineDef(udb, PipelineDef{
		Name: name, Owner: "u", Global: global,
		Stages: []PipelineStage{{Name: "only", Kind: StageWorker, Prompt: "do {input}"}},
	})
	if err := def.Validate(); err != nil {
		t.Fatalf("test pipeline %q is not runnable: %v", name, err)
	}
	return def
}

// Naming both targets is refused rather than resolved by precedence: whichever
// the handler checked first would be the one that ran forever, and which one
// depends on the order of an if.
func TestDispatchTargetIsExactlyOne(t *testing.T) {
	if err := validateDispatchTarget("Comedian", "Nightly"); err == nil {
		t.Error("naming an agent AND a pipeline should be refused")
	} else if !strings.Contains(err.Error(), "not both") {
		t.Errorf("refusal should say both were named; got: %v", err)
	}
	if err := validateDispatchTarget("", "  "); err == nil {
		t.Error("naming neither target should be refused")
	}
	if err := validateDispatchTarget("Comedian", ""); err != nil {
		t.Errorf("an agent alone is a valid target; got: %v", err)
	}
	if err := validateDispatchTarget("", "Nightly"); err != nil {
		t.Errorf("a pipeline alone is a valid target; got: %v", err)
	}
}

// A pipeline resolves by name or id, the way an agent target does — the model
// has names, so an id-only lookup would strand it.
func TestDispatchablePipelineResolvesByNameAndID(t *testing.T) {
	var def PipelineDef
	turn := newPipelineDispatchTurn(t, func(udb Database) []string {
		def = savePipeline(t, udb, "Nightly Report", false)
		return []string{def.ID}
	})
	for _, ref := range []string{"Nightly Report", "nightly report", def.ID} {
		got, err := turn.dispatchablePipeline(ref)
		if err != nil {
			t.Fatalf("resolving %q: %v", ref, err)
		}
		if got.ID != def.ID {
			t.Fatalf("resolving %q gave %q, want %q", ref, got.ID, def.ID)
		}
	}
}

// The point of dispatch: reaching a workflow that was never attached. Gating
// on attachment made dispatch and the run_<name> tool answer the same
// question, at which case dispatch added nothing the catalog did not already.
func TestUnattachedPipelineIsDispatchable(t *testing.T) {
	turn := newPipelineDispatchTurn(t, func(udb Database) []string {
		savePipeline(t, udb, "Never Attached", false)
		return nil
	})
	got, err := turn.dispatchablePipeline("Never Attached")
	if err != nil {
		t.Fatalf("an unattached pipeline should be dispatchable: %v", err)
	}
	if got.Name != "Never Attached" {
		t.Fatalf("resolved %q, want Never Attached", got.Name)
	}
	// A name that matches nothing still has to be refused, and the refusal
	// names what IS runnable so the model can correct itself in one round.
	_, err = turn.dispatchablePipeline("No Such Thing")
	if err == nil {
		t.Fatal("a pipeline that does not exist should be refused")
	}
	if !strings.Contains(err.Error(), "Never Attached") {
		t.Errorf("refusal should list what can be run; got: %v", err)
	}
}

// An absent grant and an expressed denial are different statements. Widening
// says a missing attachment no longer blocks; it must not overrule an operator
// who ticked "not this agent".
func TestDeniedPipelineStaysUnreachable(t *testing.T) {
	var def PipelineDef
	turn := newPipelineDispatchTurn(t, func(udb Database) []string {
		def = savePipeline(t, udb, "Off Limits", false)
		return nil
	})
	turn.agent.DisabledPipelines = []string{def.ID}
	if _, err := turn.dispatchablePipeline("Off Limits"); err == nil {
		t.Error("an explicitly denied pipeline should not be dispatchable")
	}
}

// "Allow none is absolute" — an agent switched off from delegating does not
// acquire a second channel just because the target is a pipeline rather than
// an agent.
func TestDispatchNoneRefusesPipelines(t *testing.T) {
	turn := newPipelineDispatchTurn(t, func(udb Database) []string {
		savePipeline(t, udb, "Nightly", false)
		return nil
	})
	turn.agent.DispatchMode = dispatchNone
	_, err := turn.dispatchablePipeline("Nightly")
	if err == nil {
		t.Fatal("a dispatch-none agent reached a pipeline")
	}
	if !strings.Contains(err.Error(), "Allow NONE") {
		t.Errorf("refusal should name the policy; got: %v", err)
	}
	if names := turn.dispatchablePipelineNames(5); len(names) != 0 {
		t.Errorf("a dispatch-none agent should be advertised no pipelines; got %v", names)
	}
}

// The reachable set is advertised in the tool description, because a pipeline
// that needs no attachment has nothing else in the catalog to announce it.
func TestReachablePipelinesAreAdvertisedAndBounded(t *testing.T) {
	turn := newPipelineDispatchTurn(t, func(udb Database) []string {
		savePipeline(t, udb, "Alpha", false)
		savePipeline(t, udb, "Beta", false)
		return nil
	})
	names := turn.dispatchablePipelineNames(maxAdvertisedPipelines)
	if len(names) != 2 {
		t.Fatalf("advertised %v, want both pipelines", names)
	}
	if got := turn.dispatchablePipelineNames(1); len(got) != 1 {
		t.Errorf("advertised list ignored its cap: %v", got)
	}
}

// A pipeline whose agent stage dispatches back into the same pipeline is a
// loop the depth counter catches only slowly — each hop resets per-turn depth,
// so it would iterate the cap's worth at every level.
func TestPipelineCannotReenterItself(t *testing.T) {
	var def PipelineDef
	turn := newPipelineDispatchTurn(t, func(udb Database) []string {
		def = savePipeline(t, udb, "Recursive", false)
		return []string{def.ID}
	})
	args := map[string]any{"pipeline": "Recursive", "message": "go"}
	if _, _, err := turn.pipelineDispatchGate(args); err != nil {
		t.Fatalf("first entry should be allowed: %v", err)
	}
	// Now run the gate as a stage agent would see it: the pipeline is already
	// in flight above this call, recorded on the context.
	turn.ctx = withDispatchedPipeline(turn.ctx, def.ID)
	_, _, err := turn.pipelineDispatchGate(args)
	if err == nil {
		t.Fatal("re-entering an in-flight pipeline should be refused")
	}
	if !strings.Contains(err.Error(), "already running above this call") {
		t.Errorf("refusal should name the cycle; got: %v", err)
	}
}

// The chain rides the context because a pipeline's agent stages are dispatched
// through RunAgentSync, which starts each sub-turn with an empty dispatchChain
// — a turn-local list would not survive the one hop that matters.
func TestDispatchedPipelinesNest(t *testing.T) {
	ctx := context.Background()
	if got := dispatchedPipelines(ctx); len(got) != 0 {
		t.Fatalf("a fresh context carries no pipelines; got %v", got)
	}
	ctx = withDispatchedPipeline(ctx, "a")
	inner := withDispatchedPipeline(ctx, "b")
	if got := dispatchedPipelines(inner); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("nested chain = %v, want [a b]", got)
	}
	// The outer context must not see what was added below it.
	if got := dispatchedPipelines(ctx); len(got) != 1 || got[0] != "a" {
		t.Fatalf("outer chain = %v, want [a] — a nested add leaked upward", got)
	}
}

// The per-turn dispatch counters are keyed by agent id. A pipeline id colliding
// with an agent id would spend one budget on two different targets.
func TestPipelineCapKeyIsNamespaced(t *testing.T) {
	if dispatchPipelineCapKey("x") == "x" {
		t.Error("a pipeline's cap key must not collide with the agent id namespace")
	}
}
