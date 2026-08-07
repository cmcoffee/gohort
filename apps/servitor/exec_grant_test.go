// The exec path asking the grant records who it is acting for.
//
// The records were keyed by (agent, appliance) and nothing consulted them: a
// set of per-agent permissions sat in the database while every command resolved
// against one flat per-user list. These pin the wiring, and — just as
// important — that a human at the console is unaffected by any of it.
package servitor

import (
	"context"
	"testing"
)

// The console has no acting agent, so it resolves to the user's own settings —
// the behaviour that existed before grants did.
func TestConsoleFallsBackToTheUserDefault(t *testing.T) {
	udb := grantStore(t)
	cat := AllRiskCategories[0]
	saveAllowedCategories(udb, map[string]bool{string(cat): true})

	ok, scope := autoRunAllowed(udb, "", "lab-box", cat)
	if !ok {
		t.Error("the operator's own allowance should still auto-run at the console")
	}
	if scope != ScopeUserDefault {
		t.Errorf("scope = %q, want the user default", scope)
	}
}

// An agent with its own grant on this appliance is answered by that record,
// not by the user's blanket settings.
func TestAgentGrantOnThisApplianceWins(t *testing.T) {
	udb := grantStore(t)
	permitted, withheld := AllRiskCategories[0], AllRiskCategories[1]
	saveAllowedCategories(udb, map[string]bool{string(withheld): true})
	SaveCommandGrant(udb, "wren", "lab-box", []string{string(permitted)})

	if ok, scope := autoRunAllowed(udb, "wren", "lab-box", permitted); !ok || scope != ScopeAgentAppliance {
		t.Errorf("the agent's own grant should decide: ok=%v scope=%q", ok, scope)
	}
	// And the user's blanket allowance does NOT leak through to the agent — a
	// narrower grant REPLACES the broader one, or an agent could never be given
	// less than its owner has.
	if ok, _ := autoRunAllowed(udb, "wren", "lab-box", withheld); ok {
		t.Error("the user's own allowance must not widen an agent's grant")
	}
}

// REMOVED WITH THE WILDCARD. This proved an agent-wide grant covered a box it
// did not name — which is exactly the standing decision about not-yet-existing
// machines that the wildcard was taken out for. What replaces it is the
// complement: a grant reaches the machine it names and no other.
func TestAGrantReachesOnlyTheMachineItNames(t *testing.T) {
	udb := grantStore(t)
	cat := AllRiskCategories[0]
	SaveCommandGrant(udb, "wren", "lab-box", []string{string(cat)})

	if ok, scope := autoRunAllowed(udb, "wren", "lab-box", cat); !ok || scope != ScopeAgentAppliance {
		t.Errorf("the named machine should be covered: ok=%v scope=%q", ok, scope)
	}
	// Another box falls through to the owner's own settings, which permit
	// nothing here — never to the grant held elsewhere.
	if ok, _ := autoRunAllowed(udb, "wren", "prod-box", cat); ok {
		t.Error("a grant must not reach a machine it does not name")
	}
}

// Present-but-empty is a decision, and the exec path has to honour it: this
// agent runs nothing here without asking, even though the owner auto-runs it.
func TestEmptyGrantStopsAutoRunForThatAgent(t *testing.T) {
	udb := grantStore(t)
	cat := AllRiskCategories[0]
	saveAllowedCategories(udb, map[string]bool{string(cat): true})
	SaveCommandGrant(udb, "wren", "prod-box", nil)

	if ok, _ := autoRunAllowed(udb, "wren", "prod-box", cat); ok {
		t.Error("an empty grant must stop auto-run rather than falling through")
	}
	// The same agent elsewhere still falls through to the owner's settings.
	if ok, scope := autoRunAllowed(udb, "wren", "lab-box", cat); !ok || scope != ScopeUserDefault {
		t.Errorf("absence should fall through, not deny: ok=%v scope=%q", ok, scope)
	}
}

// The acting agent rides the context and is never inferred from anything a
// caller supplies.
func TestActingAgentRoundTrip(t *testing.T) {
	if got := ActingAgent(context.Background()); got != "" {
		t.Errorf("a bare context has no acting agent, got %q", got)
	}
	ctx := WithActingAgent(context.Background(), "  wren  ")
	if got := ActingAgent(ctx); got != "wren" {
		t.Errorf("acting agent = %q, want the trimmed id", got)
	}
	// An empty id must not install a value that later reads as an agent.
	if got := ActingAgent(WithActingAgent(context.Background(), "   ")); got != "" {
		t.Errorf("an empty id is the console, got %q", got)
	}
}
