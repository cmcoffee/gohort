package orchestrate

// Cross-app hooks must read the store AGENTS LIVE IN, not the store the caller
// happens to hold.
//
// The MCP server and the account page each pass their own per-app store,
// because that is the store they have. Agents live in orchestrate's. Every one
// of these hooks was therefore reading an empty bucket: a user's own agent was
// invisible to list_agents AND unreachable via ask_agent, however its
// "Reachable over MCP" toggle was set, while in-code seeds and app agents —
// which need no user record — came through and made the surface look alive.

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestCrossAppHooksIgnoreTheCallersStore(t *testing.T) {
	orchStore := &DBase{Store: kvlite.MemStore()}
	callerStore := &DBase{Store: kvlite.MemStore()} // e.g. the MCP server's own

	// An agent the user owns and has exposed, saved where orchestrate keeps it.
	const user = "alice@example.com"
	udb := UserDB(orchStore, user)
	if _, err := saveAgent(udb, AgentRecord{
		ID: "wren-1", Name: "Wren", Owner: user, MCPExposed: true,
		Description:        "General assistant",
		OrchestratorPrompt: "You are Wren.",
	}); err != nil {
		t.Fatal(err)
	}

	app := &OrchestrateApp{}
	app.DB = orchStore
	app.registerExternalAgentHooks()

	// Called the way an external app calls it: with ITS store, not orchestrate's.
	got := ListExternalReachableAgentsFn(callerStore, user, nil)
	var found bool
	for _, a := range got {
		if a.ID == "wren-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the user's exposed agent is missing when the caller passes its own store; got %+v", got)
	}

	// And the resolver has to agree, or the list shows what a call refuses.
	if id, ok := ResolveExternalAgentFn(callerStore, user, "Wren", nil); !ok || id != "wren-1" {
		t.Errorf("resolver returned (%q, %v) for a listed agent", id, ok)
	}
}
