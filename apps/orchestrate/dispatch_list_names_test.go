package orchestrate

// A dispatch list is read by agent id. The agent tool could write names into
// one, which meant nothing: an "except" list left the agent it named
// reachable. Names are pinned to ids as they are written, and an except list
// written before that excludes by name too.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestAnExceptListByNameStillExcludes(t *testing.T) {
	turn, _ := newRunGateTurn(t, dispatchExcept)
	critic, err := saveAgent(turn.udb, AgentRecord{Name: "Critic", Owner: "u", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	turn.agent.AllowedDispatchTargets = []string{"critic"}
	if _, _, err := turn.agentsRunGate(map[string]any{"agent": critic.ID, "message": "review"}); err == nil || !strings.Contains(err.Error(), "block list") {
		t.Errorf("an agent an except list names was reached: %v", err)
	}
	if dispatchReachable(turn.agent, critic) {
		t.Error("the reach listing calls an excluded agent reachable")
	}
	if (dispatchAuthority{AgentID: turn.agent.ID, Mode: dispatchExcept, Targets: []string{"Critic"}}).allows(critic) {
		t.Error("an originator's except list did not bind its sub-run")
	}
	if _, err := saveAgent(turn.udb, AgentRecord{Name: "Editor", Owner: "u", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := turn.agentsRunGate(map[string]any{"agent": "Editor", "message": "edit"}); err != nil {
		t.Errorf("an agent the except list does not name was refused: %v", err)
	}
}

func TestAgentNamesInADispatchListArePinnedToIds(t *testing.T) {
	udb := UserDB(&DBase{Store: kvlite.MemStore()}, "u")
	critic, _ := saveAgent(udb, AgentRecord{Name: "Critic", Owner: "u", OrchestratorPrompt: "p"})
	got, err := pinDispatchAgentNames(udb, "u", []string{"critic", critic.ID, "nightly-digest"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != critic.ID+","+critic.ID+",nightly-digest" {
		t.Errorf("pinned list: %v", got)
	}
	saveAgent(udb, AgentRecord{Name: "Critic", Owner: "u", OrchestratorPrompt: "p"})
	if _, err := pinDispatchAgentNames(udb, "u", []string{"Critic"}); err == nil {
		t.Error("a name two agents share was pinned to one of them")
	}
}
