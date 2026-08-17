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

// Reachability is the attachment set, and a pipeline resolves by name or id
// the way an agent target does.
func TestDispatchablePipelineResolvesAttachedByNameAndID(t *testing.T) {
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

// A global pipeline reaches every agent that has not denied it — the same rule
// the attached-tool catalog uses, which is why both read it from one place.
func TestDispatchablePipelineHonorsGlobalAndOptOut(t *testing.T) {
	var def PipelineDef
	turn := newPipelineDispatchTurn(t, func(udb Database) []string {
		def = savePipeline(t, udb, "Shared", true)
		return nil
	})
	if _, err := turn.dispatchablePipeline("Shared"); err != nil {
		t.Fatalf("a global pipeline should be reachable without attachment: %v", err)
	}
	turn.agent.DisabledPipelines = []string{def.ID}
	if _, err := turn.dispatchablePipeline("Shared"); err == nil {
		t.Error("an opted-out global pipeline should not be reachable")
	}
}

// "Not attached" and "does not exist" are different problems with different
// fixes: an agent told a pipeline is missing goes off to author a replacement,
// where the actionable ask is for the person to attach the one that exists.
func TestUnattachedPipelineSaysSoRatherThanMissing(t *testing.T) {
	turn := newPipelineDispatchTurn(t, func(udb Database) []string {
		savePipeline(t, udb, "Private Workflow", false)
		return nil
	})
	_, err := turn.dispatchablePipeline("Private Workflow")
	if err == nil {
		t.Fatal("an unattached pipeline should not be dispatchable")
	}
	if !strings.Contains(err.Error(), "not attached") {
		t.Errorf("refusal should distinguish unattached from missing; got: %v", err)
	}
	if _, err := turn.dispatchablePipeline("No Such Thing"); err == nil {
		t.Error("a pipeline that does not exist should be refused")
	} else if strings.Contains(err.Error(), "not attached") {
		t.Errorf("a missing pipeline should not be reported as unattached; got: %v", err)
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
