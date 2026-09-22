package orchestrate

// The page built to answer "what can this agent do" listed tools out of
// rec.AllowedTools, which is EMPTY on a default-pool agent because empty means
// "every catalog tool". So the commonest kind of agent showed no tools at all,
// on the one surface whose whole job is that question.

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The regression, stated as the thing that was wrong: an agent with an empty
// allowlist has the WHOLE catalog, and must not read as having nothing.
func TestADefaultPoolAgentResolvesItsWholeCatalog(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	rec := AgentRecord{ID: "a1", Name: "Wren", Owner: "alice", OrchestratorPrompt: "you are a helper"} // AllowedTools nil = default pool
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatal(err)
	}
	rows, err := app.resolvedAgentTools(context.Background(), udb, "alice", rec)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("a default-pool agent resolved to no tools, which is what the old AllowedTools read did")
	}
	// Sanity that these are real resolved names, not a placeholder.
	for _, r := range rows {
		if strings.TrimSpace(r.Name) == "" {
			t.Error("a resolved row has no name")
		}
	}
}

// A tool in the owner's pool can carry a flag; a framework tool has no record
// to hold one. The page has to say which, or it offers controls that do
// nothing and hides ones that would work.
func TestOnlyToolsWithARecordReadAsGovernable(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	if err := AdminPersistTempTool(udb, "alice", TempTool{
		Name: "access_row_tool", CommandTemplate: "echo hi", ConfirmInChat: true}); err != nil {
		t.Fatal(err)
	}
	rec := AgentRecord{ID: "a2", Name: "Wren", Owner: "alice", OrchestratorPrompt: "you are a helper"}
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatal(err)
	}
	rows, err := app.resolvedAgentTools(context.Background(), udb, "alice", rec)
	if err != nil {
		t.Fatal(err)
	}
	var mine, framework int
	for _, r := range rows {
		if r.Name == "access_row_tool" {
			mine++
			if !r.Governable {
				t.Error("a tool in the owner's pool does not read as governable, so its controls are hidden")
			}
			if !r.Asks {
				t.Error("the ask-before-every-call flag did not reach the row")
			}
			if r.Origin != "your tools" {
				t.Errorf("origin should say where it came from, got %q", r.Origin)
			}
		}
		if r.Origin == "framework" {
			framework++
			if r.Governable {
				t.Errorf("%q reads as governable but has no record to carry a flag", r.Name)
			}
		}
	}
	if mine == 0 {
		t.Error("the owner's own tool is missing from the agent's resolved catalog")
	}
	if framework == 0 {
		t.Error("no framework tools resolved, so the governable check proved nothing")
	}
}

// Governable rows sort first. They are what somebody opened this page to act
// on, and burying them under the framework catalog is how a control surface
// becomes a list nobody scrolls.
func TestTheRowsYouCanActOnComeFirst(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	if err := AdminPersistTempTool(udb, "alice", TempTool{
		Name: "zzz_last_alphabetically", CommandTemplate: "echo hi"}); err != nil {
		t.Fatal(err)
	}
	rec := AgentRecord{ID: "a3", Name: "Wren", Owner: "alice", OrchestratorPrompt: "you are a helper"}
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatal(err)
	}
	rows, err := app.resolvedAgentTools(context.Background(), udb, "alice", rec)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 2 || !rows[0].Governable {
		t.Fatalf("a tool the owner can act on is not first; got %+v", rows[0])
	}
	seenPlain := false
	for _, r := range rows {
		if !r.Governable {
			seenPlain = true
		} else if seenPlain {
			t.Errorf("%q is governable but sorts after a row that is not", r.Name)
		}
	}
}
