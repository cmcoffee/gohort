package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// harness: a caller agent + a sub-agent it owns, saved in one user store, with
// a chatTurn wired the way agentsRunAction expects for a fleet lookup.
func newRunGateTurn(t *testing.T, callerMode string) (*chatTurn, AgentRecord) {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	caller, err := saveAgent(udb, AgentRecord{Name: "Moltbook", Owner: "u", DispatchMode: callerMode, OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatalf("save caller: %v", err)
	}
	target, err := saveAgent(udb, AgentRecord{Name: "Comedian", Owner: "u", OwnedBy: caller.ID, OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatalf("save target: %v", err)
	}
	return &chatTurn{user: "u", udb: udb, agent: caller}, target
}

// TestDispatchNoneBlocksOwnedSubAgent pins the "Allow none is absolute" rule.
// Before the fix, the ownership carve-out ran FIRST, so a dispatch-disabled
// agent could still dispatch its own sub-agents without limit — observed as a
// Comedian storm (100+ dispatches in one autonomous turn) from an agent whose
// dispatch policy the user had set to Allow none.
func TestDispatchNoneBlocksOwnedSubAgent(t *testing.T) {
	turn, _ := newRunGateTurn(t, dispatchNone)
	out, err := turn.agentsRunAction(map[string]any{"agent": "Comedian", "message": "tell a joke"})
	if err == nil {
		t.Fatalf("dispatch-none caller reached its owned sub-agent; out=%q", out)
	}
	if !strings.Contains(err.Error(), "Allow NONE") {
		t.Fatalf("refusal should name the Allow NONE policy; got: %v", err)
	}
}

// TestPermissionBlockRefusesAgentsRun pins tool/shell symmetry for the
// Permissions-pane delegation policy: a target Blocked there must be
// unreachable through agents(run) too, not just through the Operator's
// delegate tool. Before the fix agents(run) never consulted the policy, so a
// blocked target kept getting dispatched by every standing-cycle fire.
func TestPermissionBlockRefusesAgentsRun(t *testing.T) {
	turn, target := newRunGateTurn(t, dispatchAll)
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	SetDelegationPolicy(RootDB, "u", target.Name, PolicyBlock)

	out, err := turn.agentsRunAction(map[string]any{"agent": "Comedian", "message": "tell a joke"})
	if err == nil {
		t.Fatalf("permission-blocked target was dispatched anyway; out=%q", out)
	}
	if !strings.Contains(err.Error(), "BLOCKED in the user's permission settings") {
		t.Fatalf("refusal should name the permission block; got: %v", err)
	}
}

// TestAgentsToolIsSerialFirePerBatch pins the batching mode of the agents
// tool. The loop executes batched tool calls in parallel goroutines, but
// agentsRunAction mutates unsynchronized per-turn state: dispatchDepth (plain
// int — concurrent siblings read as recursion, giving spurious "depth limit
// exceeded" at true depth 1) and agentDispatchCounts (plain map — racy
// read-modify-write lets a large batch pass the per-turn cap before any
// increment lands). Serial-fire keeps every dispatch's bookkeeping visible to
// the next call in the batch.
func TestAgentsToolIsSerialFirePerBatch(t *testing.T) {
	turn, _ := newRunGateTurn(t, dispatchAll)
	for _, allowRun := range []bool{true, false} {
		if td := turn.agentsGroupedToolDef(allowRun); !td.SerialFirePerBatch {
			t.Fatalf("agents tool (allowRun=%v) must be SerialFirePerBatch — parallel batch calls race on dispatchDepth and agentDispatchCounts", allowRun)
		}
	}
}

// TestDispatchCapVerdictIsAnErrorNotAFencedResult pins the delivery channel of
// the per-turn dispatch cap. As a normal result the verdict rode through
// fenceAgentsOutput and reached the model inside the untrusted-content banner
// — a STOP instruction the fence itself tells the model to ignore, and one the
// loop's failure-streak machinery never counted. It must surface as an error.
func TestDispatchCapVerdictIsAnErrorNotAFencedResult(t *testing.T) {
	turn, target := newRunGateTurn(t, dispatchAll)
	// Pre-burn the total-dispatch budget so the cap decision blocks before any
	// real sub-dispatch is attempted.
	turn.agentDispatchCounts = map[string]int{"total\x00" + target.ID: maxTotalTargetDispatch}
	out, err := turn.agentsRunAction(map[string]any{"agent": "Comedian", "message": "tell a joke"})
	if err == nil {
		t.Fatalf("cap verdict came back as a normal result (would be fenced): %q", out)
	}
	if !strings.HasPrefix(err.Error(), "STOP —") {
		t.Fatalf("cap error must carry the STOP verdict for the loop guards; got: %v", err)
	}
	if strings.Contains(out+err.Error(), "UNTRUSTED EXTERNAL CONTENT") {
		t.Fatal("cap verdict must never be wrapped in the untrusted-content fence")
	}
}
