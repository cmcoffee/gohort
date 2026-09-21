package orchestrate

// Copy session, from the restricted /agents/<slug> surface, as somebody who
// does not own the agent.
//
// Reported live as "404 page not found (url: api/sessions/<id>/export)". The
// export resolved the agent with "is this record in YOUR store, or is it a
// seed" — which is false for every recipient of a shared agent and everybody on
// a published one, so the button was dead for exactly the people that surface
// exists for.
//
// The session is the authorization and always was: it is loaded from the
// CALLER's store keyed by this agent, so a session you can export is one you
// ran.

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestASharedAgentsSessionCanBeExported(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	base := orchestrateBaseDB

	// alice owns it and shares it with bob.
	rec := AgentRecord{ID: "a1", Owner: "alice", Name: "Runbooks", OrchestratorPrompt: "help",
		AllowedUsers: []string{"bob"}}
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, ok := app.agentForSessionExport(UserDB(base, "bob"), "bob", "a1"); !ok {
		t.Fatal("a recipient cannot resolve the agent behind their own session, so Copy session 404s")
	}
	// The owner still resolves it, by the first tier.
	if a, ok := app.agentForSessionExport(udb, "alice", "a1"); !ok || a.Name != "Runbooks" {
		t.Fatalf("the owner lost their own agent: %+v", a)
	}
	// Somebody with no claim on it resolves nothing.
	if _, ok := app.agentForSessionExport(UserDB(base, "dana"), "dana", "a1"); ok {
		t.Error("an unshared user resolved an agent that is not theirs and was never shared")
	}
}
