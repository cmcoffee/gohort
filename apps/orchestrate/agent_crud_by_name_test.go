package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestAgentMutationToolsResolveByName: agents(action="get") finds an agent by
// name, so a model that reads "Moltbook Conversational Agent" and then calls
// update_agent with that same string must reach the same record. It did not:
// update_agent looked the id up directly, said "not found", and the turn went
// off narrating instead (seen live 2026-09-07). The three mutation tools now
// share the get path's name-or-id resolution.
func TestAgentMutationToolsResolveByName(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	const owner = "u"
	saved, err := saveAgent(db, AgentRecord{Name: "Moltbook Conversational Agent", Owner: owner, OrchestratorPrompt: "posts things"})
	if err != nil {
		t.Fatal(err)
	}
	sess := &ToolSession{Username: owner, DB: db}

	out, err := updateAgentTool{}.RunWithSession(map[string]any{"id": "Moltbook Conversational Agent", "description": "updated by name"}, sess)
	if err != nil {
		t.Fatalf("update_agent by name: %v", err)
	}
	if !strings.Contains(out, "AGENT_UPDATED") {
		t.Fatalf("update_agent did not report success: %s", out)
	}
	got, ok := loadAgent(db, saved.ID)
	if !ok || got.Description != "updated by name" {
		t.Fatalf("the update did not land on the named agent: ok=%v desc=%q", ok, got.Description)
	}

	out, err = cloneAgentTool{}.RunWithSession(map[string]any{"id": "Moltbook Conversational Agent", "name": "Moltbook Copy"}, sess)
	if err != nil {
		t.Fatalf("clone_agent by name: %v", err)
	}
	if !strings.Contains(out, "AGENT_CLONED") {
		t.Fatalf("clone_agent did not report success: %s", out)
	}

	if _, err := (deleteAgentTool{}).RunWithSession(map[string]any{"id": "Moltbook Copy"}, sess); err != nil {
		t.Fatalf("delete_agent by name: %v", err)
	}
	for _, a := range listAgents(db, owner) {
		if a.Name == "Moltbook Copy" {
			t.Fatal("delete_agent by name left the agent in place")
		}
	}

	// A name that is nobody's agent still fails, and says so.
	if _, err := (updateAgentTool{}).RunWithSession(map[string]any{"id": "No Such Agent"}, sess); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected a not-found error for an unknown name, got %v", err)
	}
}
