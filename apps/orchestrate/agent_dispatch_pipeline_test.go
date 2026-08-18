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

// The dispatch policy governs pipelines as well as agents. It used not to, so
// "only these two targets" quietly meant "only these two, plus every pipeline
// the owner has" — and a pipeline is a delegation channel with a tool catalog
// of its own, not a lesser thing than an agent.
func TestDispatchOnlyGovernsPipelines(t *testing.T) {
	var allowed, other PipelineDef
	turn := newPipelineDispatchTurn(t, func(udb Database) []string {
		allowed = savePipeline(t, udb, "Allowed", false)
		other = savePipeline(t, udb, "Not Allowed", false)
		return nil
	})
	turn.agent.DispatchMode = dispatchOnly
	turn.agent.AllowedDispatchTargets = []string{allowed.ID}

	if got, err := turn.dispatchablePipeline("Allowed"); err != nil || got.ID != allowed.ID {
		t.Fatalf("a listed pipeline must be reachable; got %q, %v", got.Name, err)
	}
	err := mustRefuse(t, turn, "Not Allowed")
	// Named-and-refused is a different answer from never-existed. Conflating
	// them teaches the model to hunt for a spelling that works.
	if !strings.Contains(err.Error(), "not on this agent's dispatch target list") {
		t.Errorf("refusal should name the policy, not look like a typo; got: %v", err)
	}
	if strings.Contains(err.Error(), "you can run") {
		t.Errorf("a policy refusal must not read as an unknown name; got: %v", err)
	}
	// And what is advertised matches what the gate will accept.
	names := turn.dispatchablePipelineNames(maxAdvertisedPipelines)
	if len(names) != 1 || names[0] != "Allowed" {
		t.Errorf("advertised %v, want only the listed pipeline", names)
	}
	_ = other
}

// The picker writes ids, but a list edited through the agent tool or by hand
// carries the name the author knows the pipeline by. A grant that silently
// means nothing is worse than no grant at all.
func TestDispatchOnlyMatchesPipelineByName(t *testing.T) {
	turn := newPipelineDispatchTurn(t, func(udb Database) []string {
		savePipeline(t, udb, "Nightly Report", false)
		return nil
	})
	turn.agent.DispatchMode = dispatchOnly
	turn.agent.AllowedDispatchTargets = []string{"nightly report"} // case-insensitive
	if _, err := turn.dispatchablePipeline("Nightly Report"); err != nil {
		t.Fatalf("a pipeline named on the list must be reachable: %v", err)
	}
}

// Except mode is where an operator expresses a denial, and it has to reach
// pipelines for the same reason Only does.
func TestDispatchExceptBlocksListedPipeline(t *testing.T) {
	var blocked PipelineDef
	turn := newPipelineDispatchTurn(t, func(udb Database) []string {
		blocked = savePipeline(t, udb, "Blocked", false)
		savePipeline(t, udb, "Fine", false)
		return nil
	})
	turn.agent.DispatchMode = dispatchExcept
	turn.agent.AllowedDispatchTargets = []string{blocked.ID}
	mustRefuse(t, turn, "Blocked")
	if _, err := turn.dispatchablePipeline("Fine"); err != nil {
		t.Fatalf("an unlisted pipeline must stay reachable in Except mode: %v", err)
	}
}

// Transitive authority: everything else judges the IMMEDIATE caller, which
// makes an allowlist a one-hop gate — A(allow:[B]) → B → any pipeline would
// reach what A itself could not. Authority must never grow along a chain.
func TestTransitivePipelineAuthority(t *testing.T) {
	turn := newPipelineDispatchTurn(t, func(udb Database) []string {
		savePipeline(t, udb, "Payroll", false)
		return nil
	})
	// The immediate caller is unrestricted; the agent it runs on behalf of
	// is not.
	turn.dispatchOrigin = &dispatchAuthority{
		AgentID: "originator", AgentName: "Originator",
		Mode: dispatchOnly, Targets: []string{"some-other-agent"},
	}
	_, _, err := turn.pipelineDispatchGate(map[string]any{"pipeline": "Payroll", "message": "go"})
	if err == nil {
		t.Fatal("a delegated turn reached a pipeline its originator could not")
	}
	if !strings.Contains(err.Error(), "Originator") {
		t.Errorf("refusal should name whose authority bounds this; got: %v", err)
	}
	// Named by the originator, it goes through.
	turn.dispatchOrigin.Targets = []string{"Payroll"}
	if _, _, err := turn.pipelineDispatchGate(map[string]any{"pipeline": "Payroll", "message": "go"}); err != nil {
		t.Fatalf("a pipeline the originator permits must run: %v", err)
	}
}

func TestDispatchAuthorityAllowsPipeline(t *testing.T) {
	def := PipelineDef{ID: "p1", Name: "Nightly"}
	cases := []struct {
		mode    string
		targets []string
		want    bool
	}{
		{dispatchAll, nil, true},
		{dispatchNone, nil, false},
		{dispatchOnly, []string{"p1"}, true},
		{dispatchOnly, []string{"Nightly"}, true},
		{dispatchOnly, []string{"other"}, false},
		{dispatchExcept, []string{"p1"}, false},
		{dispatchExcept, []string{"other"}, true},
	}
	for _, tc := range cases {
		got := dispatchAuthority{Mode: tc.mode, Targets: tc.targets}.allowsPipeline(def)
		if got != tc.want {
			t.Errorf("mode=%s targets=%v → %v, want %v", tc.mode, tc.targets, got, tc.want)
		}
	}
}

// The deleted-target self-heal reads "no listed AGENT still exists" as "this
// allowlist is stale, fall back to Allow all". Once the list can hold
// pipelines that reading became an escalation: allow exactly one pipeline and
// no agents, and the whole fleet would have been handed over.
func TestPipelineOnlyListDoesNotLookStale(t *testing.T) {
	var def PipelineDef
	turn := newPipelineDispatchTurn(t, func(udb Database) []string {
		def = savePipeline(t, udb, "Solo", false)
		return nil
	})
	turn.agent.DispatchMode = dispatchOnly
	turn.agent.AllowedDispatchTargets = []string{def.ID}
	if !turn.dispatchListNamesAPipeline() {
		t.Fatal("a list naming a live pipeline must not read as an emptied allowlist")
	}
	turn.agent.AllowedDispatchTargets = []string{"deleted-agent-id"}
	if turn.dispatchListNamesAPipeline() {
		t.Error("a list naming nothing that exists must still read as stale")
	}
}

// mustRefuse asserts a pipeline is not reachable and returns the refusal.
func mustRefuse(t *testing.T, turn *chatTurn, ref string) error {
	t.Helper()
	_, err := turn.dispatchablePipeline(ref)
	if err == nil {
		t.Fatalf("pipeline %q should not have been reachable", ref)
	}
	return err
}
