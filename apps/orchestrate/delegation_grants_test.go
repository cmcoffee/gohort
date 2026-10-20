package orchestrate

// One rule for an agent handing work to another, by either tool: no decision
// runs it, Ask queues it, Block refuses it. And a decision is found under the
// agent's id, so a grant does not stop matching when the model types the name
// another way.

import (
	"context"
	"errors"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestAGrantFindsItsAgentHoweverItWasNamed(t *testing.T) {
	depStores(t, "u")
	comedian := AgentRecord{ID: "c1", Name: "Comedian"}

	if _, ok := delegationDecisionFor("u", "wren", comedian, "Comedian"); ok {
		t.Fatal("a decision was found where none was made")
	}
	// Written under the name as the model once typed it.
	SetDelegationPolicy(RootDB, "u", "wren", "comedian", PolicyAllow)
	if p, ok := delegationDecisionFor("u", "wren", comedian, "Comedian"); !ok || p != PolicyAllow {
		t.Errorf("a grant under another spelling of the name stopped matching: %q %v", p, ok)
	}
	// The id is the agent itself, so it outranks a name in the same scope.
	SetDelegationPolicy(RootDB, "u", "wren", "c1", PolicyAsk)
	if p, _ := delegationDecisionFor("u", "wren", comedian, "Comedian"); p != PolicyAsk {
		t.Errorf("a name outranked the id: %q", p)
	}
	// The requester's own decision outranks the all-agents row.
	SetDelegationPolicy(RootDB, "u", "", "c1", PolicyBlock)
	if p, _ := delegationDecisionFor("u", "wren", comedian, ""); p != PolicyAsk {
		t.Errorf("the all-agents row outranked the agent's own decision: %q", p)
	}
	if p, _ := delegationDecisionFor("u", "other", comedian, ""); p != PolicyBlock {
		t.Errorf("another agent did not get the all-agents row: %q", p)
	}

	// A new grant is written under the id.
	udb := UserDB(orchestrateBaseDB, "u")
	saved, err := saveAgent(udb, AgentRecord{Name: "Critic", Owner: "u", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if got := delegationGrantKey("u", "critic"); got != saved.ID {
		t.Errorf("a grant for %q would be written under %q, not its id %q", "critic", got, saved.ID)
	}
}

// delegate runs where agents(run) would, queues on Ask, refuses on Block, and
// with no turn to check against runs only on an explicit Allow.
func TestDelegateFollowsTheSameRuleAsAgentsRun(t *testing.T) {
	depStores(t, "u")
	var ran []string
	RegisterStandingRunner(func(ctx context.Context, sa StandingAgent) StandingRunResult {
		ran = append(ran, sa.AgentID)
		return StandingRunResult{Status: RunOK, Summary: "a joke", Raw: "a joke " + strings.Repeat("that goes on ", 40) + "THE PUNCHLINE"}
	})
	t.Cleanup(func() { RegisterStandingRunner(nil) })
	udb := UserDB(orchestrateBaseDB, "u")
	comedian, err := saveAgent(udb, AgentRecord{Name: "Comedian", Owner: "u", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	sess := &ToolSession{Username: "u", ChatSessionID: "s1"}
	var delegate AgentToolDef
	for _, td := range operatorManagementTools(sess, "wren") {
		if td.Tool.Name == "delegate" {
			delegate = td
		}
	}
	call := func(ctx context.Context) string {
		out, err := delegate.Handler(ctx, map[string]any{"agent": "Comedian", "brief": "a joke about the heat"})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	gated := context.WithValue(context.Background(), delegateGateKey{}, delegateGate(func(agent, brief string) (AgentRecord, error) {
		return comedian, nil
	}))

	if out := call(gated); !strings.Contains(out, "Delegated to \"Comedian\"") || len(ran) != 1 || ran[0] != comedian.ID {
		t.Errorf("with nothing decided, a checked delegation should run, by id: %s (ran %v)", out, ran)
	} else if !strings.Contains(out, "THE PUNCHLINE") || !strings.Contains(out, agentResultFencePrefix+"Comedian.") {
		t.Errorf("the delegator should get the whole result, fenced, not the feed's 280-character line: %s", out)
	}
	if out := call(context.Background()); !strings.Contains(out, "Queued") {
		t.Errorf("with no turn to check against, an undecided delegation ran: %s", out)
	}
	SetDelegationPolicy(RootDB, "u", "wren", comedian.ID, PolicyAsk)
	if out := call(gated); !strings.Contains(out, "Queued") {
		t.Errorf("an agent set to Ask ran without asking: %s", out)
	}
	SetDelegationPolicy(RootDB, "u", "wren", comedian.ID, PolicyBlock)
	if out := call(gated); !strings.Contains(out, "blocked") {
		t.Errorf("an agent set to Block was not refused: %s", out)
	}
	SetDelegationPolicy(RootDB, "u", "wren", "comedian", PolicyAllow)
	RemoveDelegationPolicy(RootDB, "u", "wren", comedian.ID)
	if out := call(context.Background()); !strings.Contains(out, "Delegated to") {
		t.Errorf("an allowed agent named another way was queued again: %s", out)
	}
	// What the turn's checks refuse is refused, and says why.
	refusing := context.WithValue(context.Background(), delegateGateKey{}, delegateGate(func(agent, brief string) (AgentRecord, error) {
		return AgentRecord{}, errors.New("agents(run): this agent's dispatch policy is Allow NONE")
	}))
	if out := call(refusing); !strings.HasPrefix(out, "Not delegated: this agent's dispatch policy") {
		t.Errorf("a delegation the dispatch checks refuse was not refused: %s", out)
	}
}

// agents(run) honours Ask too, by queueing what delegate would have queued.
func TestAgentsRunAsksWhenTheUserSaidAsk(t *testing.T) {
	depStores(t, "u")
	turn, _ := newRunGateTurn(t, "")
	critic, err := saveAgent(turn.udb, AgentRecord{Name: "Critic", Owner: "u", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"agent": "critic", "message": "review this"}
	if _, queued := turn.queueAskedDispatch(context.Background(), args); queued {
		t.Error("an agent nobody set to Ask was queued")
	}
	SetDelegationPolicy(RootDB, "u", turn.agent.ID, critic.ID, PolicyAsk)
	out, queued := turn.queueAskedDispatch(context.Background(), args)
	if !queued || !strings.Contains(out, "Ask") {
		t.Fatalf("an agent set to Ask was not queued: %v %s", queued, out)
	}
	auths := ListAuthorizations(RootDB, "u")
	if len(auths) != 1 || auths[0].Agent != "Critic" || auths[0].FromAgent != turn.agent.ID || auths[0].Brief != "review this" {
		t.Errorf("the queued request: %+v", auths)
	}
	// Its own sub-agent stays the parent's to call, as with Block.
	SetDelegationPolicy(RootDB, "u", turn.agent.ID, "Comedian", PolicyAsk)
	if _, queued := turn.queueAskedDispatch(context.Background(), map[string]any{"agent": "Comedian", "message": "a joke"}); queued {
		t.Error("a parent was made to ask before calling its own sub-agent")
	}
}

// The Permissions pane's Allow for a pairing approves a narrowed agent's
// handoff, as the card's Always allow does: it was stored where the edge never
// looked, so the owner was asked again for a handoff they had allowed. Only
// the pairing's own row counts: an every-agent Allow was no decision about
// this caller, and an Ask is not a yes.
func TestAnAllowedPairingApprovesANarrowedAgentsHandoff(t *testing.T) {
	depStores(t, "u")
	udb := UserDB(orchestrateBaseDB, "u")
	comedian, err := saveAgent(udb, AgentRecord{Name: "Comedian", Owner: "u", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	turn := &chatTurn{user: "u", udb: udb, agent: AgentRecord{ID: "wiwee", Name: "WiWee", AllowedTools: []string{"web_search"}}}
	if turn.dispatchEdgeApproved(comedian) {
		t.Fatal("a narrowed agent's handoff was approved with no decision")
	}
	SetDelegationPolicy(RootDB, "u", "", comedian.ID, PolicyAllow)
	if turn.dispatchEdgeApproved(comedian) {
		t.Error("the every-agent Allow approved a narrowed agent's handoff")
	}
	SetDelegationPolicy(RootDB, "u", "wiwee", comedian.ID, PolicyAsk)
	if turn.dispatchEdgeApproved(comedian) {
		t.Error("an Ask for the pairing approved it")
	}
	// An Allow written under the name, before grants were keyed by id.
	RemoveDelegationPolicy(RootDB, "u", "wiwee", comedian.ID)
	SetDelegationPolicy(RootDB, "u", "wiwee", "comedian", PolicyAllow)
	if !turn.dispatchEdgeApproved(comedian) {
		t.Fatal("the pairing's Allow did not approve the handoff")
	}
	if how := edgeApproval(udb, "u", turn.agent, comedian); how != edgeGranted {
		t.Errorf("Security & Access would read the pairing as %q, not granted", how)
	}
}

// Deciding a pairing again leaves one row for it: a row keyed by the agent's
// name, from before grants were keyed by id, is dropped when the id's is
// written, and removing the pairing removes both. Until it is rewritten the
// list shows one row for the pair, the id's, the one the gate reads first.
func TestAPairingIsOneRow(t *testing.T) {
	depStores(t, "u")
	udb := UserDB(orchestrateBaseDB, "u")
	comedian, err := saveAgent(udb, AgentRecord{Name: "Comedian", Owner: "u", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	rows := func(scope string) []string {
		var out []string
		for _, e := range ListDelegationPolicies(RootDB, "u") {
			if e.Scope == scope {
				out = append(out, e.Target+"="+e.Policy)
			}
		}
		return out
	}
	SetDelegationPolicy(RootDB, "u", "wiwee", "Comedian", PolicyAllow)
	SetDelegationPolicy(RootDB, "u", "other", "Comedian", PolicyBlock)
	SetDelegationPolicy(RootDB, "u", "wiwee", comedian.ID, PolicyAllow)

	resolve := func(key string) string {
		if rec, ok := findAgentByNameOrID(udb, "u", key); ok {
			return rec.ID
		}
		return ""
	}
	SetDelegationPolicy(RootDB, "u", "wiwee", "gone-agent", PolicyAsk)
	shown := map[string]int{}
	for _, e := range collapseDelegationAliases(ListDelegationPolicies(RootDB, "u"), resolve) {
		shown[e.Scope+"/"+e.Target]++
	}
	if shown["wiwee/"+comedian.ID] != 1 || shown["wiwee/Comedian"] != 0 || shown["other/Comedian"] != 1 || shown["wiwee/gone-agent"] != 1 {
		t.Fatalf("the list shows %v", shown)
	}

	setDelegationDecision("u", "wiwee", "Comedian", PolicyAllow)
	if got := strings.Join(rows("wiwee"), ","); got != comedian.ID+"=allow,gone-agent=ask" {
		t.Fatalf("after approving again, WiWee's rows are %s", got)
	}
	if got := strings.Join(rows("other"), ","); got != "Comedian=block" {
		t.Errorf("another agent's row changed: %s", got)
	}
	SetDelegationPolicy(RootDB, "u", "wiwee", "comedian", PolicyAllow) // an alias again
	removeDelegationDecision("u", "wiwee", comedian.ID)
	if got := strings.Join(rows("wiwee"), ","); got != "gone-agent=ask" {
		t.Fatalf("removing the pairing left %s", got)
	}
}
