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
	// allowlist — but Allow none stays absolute.
	if !hasBuilder(AgentRecord{ID: "a1", Owner: "someuser", AllowBuilderDispatch: true,
		DispatchMode: dispatchOnly, AllowedDispatchTargets: []string{"someone-else"}}) {
		t.Error("the grant should hold under allowlist mode")
	}
	if hasBuilder(AgentRecord{ID: "a1", Owner: "someuser", AllowBuilderDispatch: true, DispatchMode: dispatchNone}) {
		t.Error("Allow none must stay absolute, grant or not")
	}
}
