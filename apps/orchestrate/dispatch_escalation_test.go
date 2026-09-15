package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func narrowedTurn(t *testing.T, caller AgentRecord) *chatTurn {
	t.Helper()
	return guardTurn(t, &wardenStubLLM{}, caller)
}

// An agent's AllowedTools list narrows what it does DIRECTLY and nothing else:
// the `agents` tool is appended by structure rather than by allowlist, and
// dispatch defaults to reaching any non-Hidden agent, which runs with its own
// catalog. So the first delegation out of a narrowed agent is a question.
func TestANarrowedAgentNeedsApprovalToDelegate(t *testing.T) {
	turn := narrowedTurn(t, AgentRecord{ID: "research", Name: "Research", AllowedTools: []string{"web_search"}})
	ops := AgentRecord{ID: "ops", Name: "Ops", AllowedTools: []string{"run_shell", "send_email"}}

	if turn.dispatchEdgeApproved(ops) {
		t.Fatal("an unapproved edge out of a narrowed agent must not be allowed silently")
	}
	// No SSE on this turn, so the confirmation fails closed — which is also the
	// unattended behaviour, and the refusal has to be actionable.
	err := turn.confirmDispatchEdge(ops)
	if err == nil {
		t.Fatal("with nobody to ask, the delegation must be refused")
	}
	for _, want := range []string{"Ops", "dispatch targets"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must say what to do about it (missing %q): %v", want, err)
		}
	}
}

// An agent the owner never restricted gains nothing by delegating, so gating it
// would be friction with no boundary behind it.
func TestAnUnrestrictedAgentIsNotGated(t *testing.T) {
	turn := narrowedTurn(t, AgentRecord{ID: "operator", Name: "Operator"})
	ops := AgentRecord{ID: "ops", Name: "Ops", AllowedTools: []string{"run_shell"}}
	if !turn.dispatchEdgeApproved(ops) {
		t.Error("an agent on the default pool must not need approval to delegate")
	}
	if err := turn.confirmDispatchEdge(ops); err != nil {
		t.Errorf("and must not be refused: %v", err)
	}
}

// An explicit link is the owner having already answered this question in the
// editor. The gate honours it and never writes to it: adding one entry to an
// empty dispatch list flips the mode from "all" to "only" and would narrow
// every other target as a side effect of approving one.
func TestAnExplicitLinkIsAlreadyApproval(t *testing.T) {
	turn := narrowedTurn(t, AgentRecord{
		ID: "research", Name: "Research",
		AllowedTools:           []string{"web_search"},
		AllowedDispatchTargets: []string{"ops"},
	})
	if !turn.dispatchEdgeApproved(AgentRecord{ID: "ops", Name: "Ops"}) {
		t.Error("a linked target must not prompt")
	}
	if !turn.dispatchEdgeApproved(AgentRecord{ID: "ops", Name: "Ops"}) {
		t.Error("and the check must not mutate anything on the way")
	}
	if turn.dispatchEdgeApproved(AgentRecord{ID: "other", Name: "Other"}) {
		t.Error("an unlinked target must still prompt")
	}
}

// A standing grant answers silently, and it is scoped to the CALLER: approving
// "Research → Ops" must not approve "Marketing → Ops".
func TestAStandingGrantIsPerEdge(t *testing.T) {
	research := AgentRecord{ID: "research", Name: "Research", AllowedTools: []string{"web_search"}}
	turn := narrowedTurn(t, research)
	ops := AgentRecord{ID: "ops", Name: "Ops"}

	saveToolGrant(turn.udb, ToolGrant{Scope: dispatchGrantScope("research"), Tool: "agents", Prefix: "ops", Label: "Research → Ops"})

	if !turn.dispatchEdgeApproved(ops) {
		t.Fatal("the standing grant did not answer the edge it was given for")
	}
	if turn.dispatchEdgeApproved(AgentRecord{ID: "billing", Name: "Billing"}) {
		t.Error("the grant covered a target it was not given for")
	}
	// A different caller reading the SAME store: a chatTurn carries a sync.Once
	// and must not be copied, so this is built rather than cloned.
	marketing := &chatTurn{
		app:   turn.app,
		agent: AgentRecord{ID: "marketing", Name: "Marketing", AllowedTools: []string{"web_search"}},
		user:  turn.user, udb: turn.udb, ctx: turn.ctx,
	}
	if marketing.dispatchEdgeApproved(ops) {
		t.Error("one agent's grant approved another agent's delegation")
	}
}

// The note is what makes the question answerable. It is best effort by design —
// the gate is the edge, so an understated note costs a vaguer question and
// never a silent bypass.
func TestTheQuestionSaysWhatIsBeingHandedOver(t *testing.T) {
	caller := AgentRecord{Name: "Research", AllowedTools: []string{"web_search"}}

	if note := dispatchEscalationNote(caller, AgentRecord{Name: "Ops"}); !strings.Contains(note, "full tool pool") {
		t.Errorf("the sharpest case must be said plainly: %q", note)
	}
	note := dispatchEscalationNote(caller, AgentRecord{Name: "Ops", AllowedTools: []string{"run_shell", "send_email", "web_search"}})
	if !strings.Contains(note, "run_shell") || !strings.Contains(note, "send_email") {
		t.Errorf("the note should name what is added: %q", note)
	}
	if strings.Contains(note, "web_search") {
		t.Errorf("and only what is ADDED — the caller already has web_search: %q", note)
	}
	// A question is a question, not a catalog.
	many := AgentRecord{Name: "Ops", AllowedTools: []string{"a_tool", "b_tool", "c_tool", "d_tool", "e_tool"}}
	if n := dispatchEscalationNote(caller, many); !strings.Contains(n, "2 more") {
		t.Errorf("a long list should be summarised: %q", n)
	}
	// Capability toolsets are appended after the allowlist, so an allowlist
	// comparison alone would miss the two that matter most.
	authored := dispatchEscalationNote(caller, AgentRecord{Name: "Builder", Author: true, AllowedTools: []string{"web_search"}})
	if !strings.Contains(authored, "author") {
		t.Errorf("authoring is a capability the allowlist never shows: %q", authored)
	}
	fleet := dispatchEscalationNote(caller, AgentRecord{Name: "Operator", Fleet: true, AllowedTools: []string{"web_search"}})
	if !strings.Contains(fleet, "fleet") {
		t.Errorf("fleet management is the other one: %q", fleet)
	}
}

// The no-tools sentinel is the most narrowed an agent gets, and the last one
// that should be able to delegate its way out.
func TestTheNoToolsSentinelCountsAsNarrowed(t *testing.T) {
	if !agentIsNarrowed(AgentRecord{AllowedTools: []string{noToolsSentinel}}) {
		t.Error("an agent given no tools at all is narrowed")
	}
	if agentIsNarrowed(AgentRecord{}) {
		t.Error("an agent with no list is on the default pool, not narrowed")
	}
}

// A recipe is not an identity, and most of what it does is already bounded by
// its caller: a worker stage's Tools list is an intersection with the inherited
// catalog, and a dispatched pipeline inherits none. What escalates is a stage
// that names an AGENT, which runs with that agent's own catalog and never
// passes the agent gate on the way in.
func TestOnlyARecipeThatReachesAgentsIsGated(t *testing.T) {
	plain := PipelineDef{ID: "p1", Name: "summarize", Stages: []PipelineStage{
		{Name: "draft", Kind: StageWorker, Tools: []string{"web_search"}},
		{Name: "polish", Kind: StageWorker},
	}}
	if pipelineReach(plain).escalates() {
		t.Error("a pipeline of worker stages reaches no further than its caller")
	}

	reaching := PipelineDef{ID: "p2", Name: "research", Stages: []PipelineStage{
		{Name: "gather", Kind: StageWorker},
		{Name: "ask", Kind: StageAgent, Agent: "Ops"},
	}}
	r := pipelineReach(reaching)
	if !r.escalates() || len(r.Agents) != 1 || r.Agents[0] != "Ops" {
		t.Fatalf("an agent stage is the escalation: %+v", r)
	}
	if !strings.Contains(r.note(), "Ops") {
		t.Errorf("the question must name what it runs: %q", r.note())
	}
}

// A fanout's or a loop's body dispatches exactly like a top-level stage, so a
// walk that stops at the top level would miss the one that matters.
func TestANestedBodyIsWalked(t *testing.T) {
	def := PipelineDef{ID: "p3", Name: "fan", Stages: []PipelineStage{
		{Name: "spread", Kind: StageFanout, Body: []PipelineStage{
			{Name: "each", Kind: StageAgent, Agent: "Ops"},
		}},
	}}
	if !pipelineReach(def).escalates() {
		t.Fatal("an agent stage inside a fanout body was missed")
	}
}

// A recipe that runs another recipe is not followed from here, so it counts as
// reaching something unknown rather than as reaching nothing.
func TestAnUnfollowedRecipeCountsAsReach(t *testing.T) {
	m := MachineDef{ID: "m1", Name: "investigate", Phases: []MachinePhase{
		{Name: "look", Pipeline: "some-pipeline"},
	}}
	r := machineReach(m)
	if !r.escalates() || !r.Nested {
		t.Fatalf("a nested recipe must count: %+v", r)
	}
	if !strings.Contains(r.note(), "other saved recipes") {
		t.Errorf("and say so: %q", r.note())
	}
	if !machineReach(MachineDef{Phases: []MachinePhase{{Name: "ask", Agent: "Ops"}}}).escalates() {
		t.Error("a phase naming an agent is the machine's version of an agent stage")
	}
}

func TestTheRecipeDoorUsesTheSameApprovals(t *testing.T) {
	turn := narrowedTurn(t, AgentRecord{ID: "research", Name: "Research", AllowedTools: []string{"web_search"}})
	reach := recipeReach{Agents: []string{"Ops"}}

	if turn.recipeEdgeApproved("p2", "research", reach) {
		t.Fatal("an unapproved recipe edge must not pass")
	}
	err := turn.confirmRecipeEdge("pipeline", "p2", "research", reach)
	if err == nil {
		t.Fatal("with nobody to ask, it must be refused")
	}
	if !strings.Contains(err.Error(), "research") {
		t.Errorf("the refusal must name the recipe: %v", err)
	}
	// The same grant store as the agent door, keyed on the recipe's id.
	saveToolGrant(turn.udb, ToolGrant{Scope: dispatchGrantScope("research"), Tool: "agents", Prefix: "p2"})
	if !turn.recipeEdgeApproved("p2", "research", reach) {
		t.Error("a standing grant must answer the recipe door too")
	}
	// And a recipe that reaches nothing is never gated, grant or no grant.
	if !turn.recipeEdgeApproved("p9", "harmless", recipeReach{}) {
		t.Error("a recipe that reaches no agent must not be gated")
	}
}
