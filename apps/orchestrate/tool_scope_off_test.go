package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Turning a tool off for an agent that does not carry it is already done, not
// an error to show ("not bundled on agent Critic").
func TestTurningOffAnAgentThatLacksTheToolIsANoOp(t *testing.T) {
	prev := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prev })
	db := RootDB
	udb := agentUserDB(db, "alice")
	for _, id := range []string{"a1", "a2"} {
		if _, err := saveAgent(udb, AgentRecord{ID: id, Name: "Agent " + id, Owner: "alice", OrchestratorPrompt: "p"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := bundleAgentToolByID(udb, "alice", "a1", TempTool{Name: "make_event", Description: "d", CommandTemplate: "echo"}); err != nil {
		t.Fatal(err)
	}
	if err := setToolScope(db, "alice", "make_event", "a2", false); err != nil {
		t.Errorf("off for an agent without it: %v", err)
	}
	if p, ok := UserToolByName(db, "alice", "make_event"); !ok || !p.ScopedToAgent("a1") {
		t.Error("the no-op touched the agent that does carry it")
	}
}
