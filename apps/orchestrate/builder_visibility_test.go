package orchestrate

// The visibility half of the Builder dispatch grant. The permission is
// worthless if the fleet catalog still filters Builder out — an agent that
// may call a target it can't see never thinks to call it — so this pins
// that listAgents actually surfaces the seed and that the catalog honors
// the grant in both directions.

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestListAgentsIncludesBuilderSeed(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	found := false
	for _, a := range listAgents(udb, "someuser") {
		if isBuilderAgent(a.ID) {
			found = true
		}
	}
	if !found {
		t.Fatal("listAgents does not surface the Builder seed — the fleet-catalog carve-out would be a silent no-op")
	}
}

func TestComputeDispatchableFleet_BuilderFollowsTheGrant(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	hasBuilder := func(agent AgentRecord) bool {
		turn := &chatTurn{agent: agent, udb: udb, user: "someuser"}
		for _, a := range turn.computeDispatchableFleet() {
			if isBuilderAgent(a.ID) {
				return true
			}
		}
		return false
	}
	if hasBuilder(AgentRecord{ID: "a1", Owner: "someuser"}) {
		t.Error("a plain agent must not see Builder in its fleet catalog")
	}
	if !hasBuilder(AgentRecord{ID: "a1", Owner: "someuser", AllowBuilderDispatch: true}) {
		t.Error("a granted agent must see Builder in its fleet catalog")
	}
	// The grant is explicit, so it shouldn't also have to be repeated in an
	// allowlist, and it holds under Allow none: Builder answers to its own
	// switch, not the fleet policy.
	if !hasBuilder(AgentRecord{ID: "a1", Owner: "someuser", AllowBuilderDispatch: true,
		DispatchMode: dispatchOnly, AllowedDispatchTargets: []string{"someone-else"}}) {
		t.Error("the grant should hold under allowlist mode")
	}
	if !hasBuilder(AgentRecord{ID: "a1", Owner: "someuser", AllowBuilderDispatch: true, DispatchMode: dispatchNone}) {
		t.Error("the explicit grant must hold under Allow none")
	}
	// Only the explicit grant: a Fleet controller's implicit access stops at
	// Allow none.
	if hasBuilder(AgentRecord{ID: "a1", Owner: "someuser", Fleet: true, DispatchMode: dispatchNone}) {
		t.Error("Fleet alone must not reach Builder under Allow none")
	}
}

// Under Allow none with the grant, Builder is the WHOLE catalog: nothing else
// in the fleet may be advertised, since the gate refuses all of it.
func TestAllowNoneWithGrantListsBuilderOnly(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	if _, err := saveAgent(udb, AgentRecord{Name: "Other", Owner: "u", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	turn := &chatTurn{agent: AgentRecord{ID: "a1", Owner: "u", AllowBuilderDispatch: true, DispatchMode: dispatchNone}, udb: udb, user: "u"}
	got := turn.computeDispatchableFleet()
	if len(got) != 1 || !isBuilderAgent(got[0].ID) {
		names := []string{}
		for _, a := range got {
			names = append(names, a.Name)
		}
		t.Fatalf("Allow none + grant must list Builder alone, got %v", names)
	}
}
