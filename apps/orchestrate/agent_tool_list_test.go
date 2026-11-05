package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// Emptying a tool list never widens an agent to every tool: only asking for
// it by name (allToolsMarker) does. A new agent with no list keeps every
// default tool, and nothing stores the marker itself.
func TestEmptyingAToolListNeverWidensIt(t *testing.T) {
	_, udb, owner := newTestOrchestrate(t)
	a, err := saveAgent(udb, AgentRecord{Name: "Fresh", Owner: owner, OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.AllowedTools) != 0 {
		t.Fatalf("a new agent with no list is not every default tool: %v", a.AllowedTools)
	}
	a.AllowedTools = []string{"web_search"}
	a, _ = saveAgent(udb, a)
	a.AllowedTools = nil
	a, _ = saveAgent(udb, a)
	if !isNoToolsSentinel(a.AllowedTools) {
		t.Fatalf("emptying a limited list widened the agent: %v", a.AllowedTools)
	}
	// From none, an empty list is still none.
	a.AllowedTools = []string{}
	a, _ = saveAgent(udb, a)
	if !isNoToolsSentinel(a.AllowedTools) {
		t.Fatalf("an empty list over none widened the agent: %v", a.AllowedTools)
	}
	// Asked for by name, every tool, stored as the empty list.
	a.AllowedTools = []string{allToolsMarker}
	a, _ = saveAgent(udb, a)
	if len(a.AllowedTools) != 0 {
		t.Fatalf("asking for every tool did not give every tool: %v", a.AllowedTools)
	}
	var raw AgentRecord
	udb.Get(agentsTable, a.ID, &raw)
	if len(raw.AllowedTools) != 0 {
		t.Errorf("the marker was stored: %v", raw.AllowedTools)
	}
}

// Builder's ["*"] on an agent with a limited list still means every tool.
func TestBuildersStarStillMeansEveryTool(t *testing.T) {
	_, udb, owner := newTestOrchestrate(t)
	a, _ := saveAgent(udb, AgentRecord{Name: "Ada", Owner: owner, OrchestratorPrompt: "p", AllowedTools: []string{"web_search"}})
	sess := &ToolSession{DB: udb, Username: owner}
	if _, err := (updateAgentTool{}).RunWithSession(map[string]any{"agent": a.ID, "allowed_tools": []any{"*"}}, sess); err != nil {
		t.Fatal(err)
	}
	if got, _ := loadAgent(udb, a.ID); len(got.AllowedTools) != 0 || isNoToolsSentinel(got.AllowedTools) {
		t.Errorf(`["*"] did not give every tool: %v`, got.AllowedTools)
	}
}

// A new agent's dispatch policy is written down when it is made, not left to
// a blank that reads as Allow all.
func TestANewAgentsDispatchPolicyIsWrittenDown(t *testing.T) {
	_, udb, owner := newTestOrchestrate(t)
	open, _ := saveAgent(udb, AgentRecord{Name: "Front desk", Owner: owner, OrchestratorPrompt: "p"})
	if open.DispatchMode != dispatchAll {
		t.Errorf("a new agent with no list: dispatch mode %q, want %q written down", open.DispatchMode, dispatchAll)
	}
	listed, _ := saveAgent(udb, AgentRecord{Name: "Triage", Owner: owner, OrchestratorPrompt: "p", AllowedDispatchTargets: []string{open.ID}})
	if listed.DispatchMode != dispatchOnly {
		t.Errorf("a new agent with a list: dispatch mode %q, want %q", listed.DispatchMode, dispatchOnly)
	}
}

// The Tools modal says "every box checked" as allToolsMarker, so ticking them
// all on a limited agent gives it every tool, and the modal's page script sends
// exactly that.
func TestTheToolsModalAsksForEveryToolByName(t *testing.T) {
	app, udb, owner := newTestOrchestrate(t)
	a, _ := saveAgent(udb, AgentRecord{Name: "Ada", Owner: owner, OrchestratorPrompt: "p", AllowedTools: []string{"web_search"}})
	w := writeAs(app, owner, "POST", "/api/agents?tools_modal=1", `{"id":"`+a.ID+`","name":"Ada","orchestrator_prompt":"p","allowed_tools":["__all__"]}`)
	if w.Code != 200 {
		t.Fatalf("modal save: %d %s", w.Code, w.Body.String())
	}
	if got, _ := loadAgent(udb, a.ID); len(got.AllowedTools) != 0 || isNoToolsSentinel(got.AllowedTools) {
		t.Errorf("ticking every box did not give every tool: %v", got.AllowedTools)
	}
	if js := readFile(t, "assets/web_assets.html"); !strings.Contains(js, `: ['__all__'];`) {
		t.Error("the Tools modal does not send __all__ for every box checked")
	}
}
