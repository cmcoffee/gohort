package orchestrate

import (
	"encoding/json"
	"strings"
	"testing"
)

// agents(list/get) read what the turn may dispatch to, not the owner's whole
// store. On a shared agent that store is the owner's, so whoever chatted it
// could list every agent and read any one in full past every dispatch limit.
// Only Builder, on the owner's own run, reads the whole fleet and the records;
// anyone else reads who a target is and what it can call.
func TestAgentsListAndGetFollowTheDispatchFleet(t *testing.T) {
	turn := reachTurn(t, AgentRecord{ID: "a1", Name: "Lead", DispatchMode: dispatchOnly, AllowedDispatchTargets: []string{"a2"}},
		AgentRecord{ID: "a2", Name: "Researcher", OrchestratorPrompt: "researcher prompt"},
		AgentRecord{ID: "a3", Name: "Payroll", OrchestratorPrompt: "the payroll secrets"})
	list, err := turn.agentsListAction()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(list, "Researcher") || strings.Contains(list, "Payroll") {
		t.Errorf("list must hold only what Lead may dispatch to: %s", list)
	}
	if _, err := turn.agentsGetAction(map[string]any{"agent": "Payroll", "full": true}); err == nil {
		t.Error("an agent outside the dispatch fleet must read as not found")
	}
	got, err := turn.agentsGetAction(map[string]any{"agent": "Researcher", "full": true})
	if err != nil {
		t.Fatal(err)
	}
	var rec map[string]any
	_ = json.Unmarshal([]byte(got), &rec)
	if strings.Contains(got, "researcher prompt") || rec["name"] != "Researcher" {
		t.Errorf("an agent that is not authoring reads who a target is, not its prompt: %s", got)
	}
}
