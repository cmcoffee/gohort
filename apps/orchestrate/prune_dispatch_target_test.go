package orchestrate

import (
	"strings"
	"testing"
)

// A deleted pipeline or machine stops being a grant on any agent's dispatch
// list. The prune read the agents from the wrong store (the root store's user
// scope, where no agent lives), so it found nothing and pruned nothing.
func TestADeletedTargetLeavesEveryDispatchList(t *testing.T) {
	_, udb := familyFixture(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "a-caller", Name: "Caller", Owner: "alice", OrchestratorPrompt: "p",
		DispatchMode: "only", AllowedDispatchTargets: []string{"pipe-1", "a-writer"}}); err != nil {
		t.Fatal(err)
	}
	pruneDispatchTarget("alice", "pipe-1")
	a, _ := loadAgent(udb, "a-caller")
	if strings.Join(a.AllowedDispatchTargets, ",") != "a-writer" {
		t.Errorf("dispatch list after the prune = %v, want only a-writer", a.AllowedDispatchTargets)
	}
}
