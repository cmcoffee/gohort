package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
	"github.com/cmcoffee/snugforge/kvlite"
)

// An agent another app makes for a person is theirs, under a name none of
// their agents already answers to, and Sources attach to it as to any other.
func TestAnAgentMadeByAnotherAppGetsAFreeName(t *testing.T) {
	prev := orchestrateBaseDB
	orchestrateBaseDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { orchestrateBaseDB = prev })
	if _, err := saveAgent(agentStore("alice"), AgentRecord{Name: "Cluster", Owner: "alice", OrchestratorPrompt: "one"}); err != nil {
		t.Fatal(err)
	}
	id, name, err := createUserAgent("alice", appagents.UserAgent{Name: "cluster", Prompt: "Investigate.", MemoryMode: "agent"})
	if err != nil || name != "cluster 2" {
		t.Fatalf("made %q: %v", name, err)
	}
	a, ok := loadAgent(agentStore("alice"), id)
	if !ok || a.Owner != "alice" || a.OrchestratorPrompt != "Investigate." || a.MemoryMode != "agent" {
		t.Fatalf("stored %+v", a)
	}
	if err := setAgentSource("alice", id, ReferenceSelection{Kind: "system", ItemID: "s1"}, true); err != nil {
		t.Fatal(err)
	}
	if got := agentSourcesFor("alice", id); len(got) != 1 {
		t.Errorf("sources %v", got)
	}
	if _, _, err := createUserAgent("alice", appagents.UserAgent{Name: "  "}); err == nil {
		t.Error("an agent with no name was made")
	}
}
