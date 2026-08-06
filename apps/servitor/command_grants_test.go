package servitor

// Grant resolution decides whether a risky command runs without asking, so the
// cases that matter are the ones where a wrong answer runs something nobody
// approved — or refuses something that was approved and then looks broken.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func grantStore(t *testing.T) Database {
	t.Helper()
	return &DBase{Store: kvlite.MemStore()}
}

func TestMostSpecificGrantWinsAndReplaces(t *testing.T) {
	udb := grantStore(t)
	// The user default allows two categories everywhere.
	saveAllowedCategories(udb, map[string]bool{
		string(AllRiskCategories[0]): true,
		string(AllRiskCategories[1]): true,
	})
	// This agent, anywhere: only the first.
	SaveCommandGrant(udb, "wren", anyAppliance, []string{string(AllRiskCategories[0])})
	// This agent on THIS box: only the second.
	SaveCommandGrant(udb, "wren", "lab-box", []string{string(AllRiskCategories[1])})

	set, scope := ResolveCommandGrant(udb, "wren", "lab-box")
	if scope != ScopeAgentAppliance {
		t.Fatalf("scope = %q, want the agent+appliance record", scope)
	}
	if set[AllRiskCategories[0]] {
		t.Error("a narrower grant must REPLACE the broader one, not add to it — an agent could never be given less otherwise")
	}
	if !set[AllRiskCategories[1]] {
		t.Error("the specific grant's own category is missing")
	}

	// Another appliance falls back to the agent-wide grant, not the user default.
	set, scope = ResolveCommandGrant(udb, "wren", "prod-box")
	if scope != ScopeAgentAny || !set[AllRiskCategories[0]] || set[AllRiskCategories[1]] {
		t.Errorf("agent-wide fallback wrong: scope=%q set=%v", scope, set)
	}

	// An agent with no grants at all gets the user default, exactly as before.
	set, scope = ResolveCommandGrant(udb, "other", "lab-box")
	if scope != ScopeUserDefault || !set[AllRiskCategories[0]] || !set[AllRiskCategories[1]] {
		t.Errorf("unknown agent should inherit the user default: scope=%q set=%v", scope, set)
	}
}

// The distinction the whole scheme rests on: a grant that exists and permits
// nothing is a decision, and must not fall through to something broader.
func TestEmptyGrantDeniesRatherThanFallingThrough(t *testing.T) {
	udb := grantStore(t)
	saveAllowedCategories(udb, map[string]bool{string(AllRiskCategories[0]): true})
	SaveCommandGrant(udb, "wren", "prod-box", nil) // present, permits nothing

	set, scope := ResolveCommandGrant(udb, "wren", "prod-box")
	if scope != ScopeAgentAppliance {
		t.Fatalf("scope = %q, want the empty record to answer", scope)
	}
	if len(set) != 0 {
		t.Errorf("an empty grant permitted %v — every risky command must prompt", set)
	}
	// Deleting it is different: now it falls through.
	DeleteCommandGrant(udb, "wren", "prod-box")
	set, scope = ResolveCommandGrant(udb, "wren", "prod-box")
	if scope != ScopeUserDefault || !set[AllRiskCategories[0]] {
		t.Errorf("after delete the lookup should fall through: scope=%q set=%v", scope, set)
	}
}

func TestGrantsIgnoreUnknownCategoriesAndCase(t *testing.T) {
	udb := grantStore(t)
	real := string(AllRiskCategories[0])
	SaveCommandGrant(udb, "Wren", "Lab-Box", []string{strings.ToUpper(real), "not_a_category"})

	// Case-insensitive on both key parts, so a grant saved from the UI and a
	// lookup during a run cannot miss each other.
	set, scope := ResolveCommandGrant(udb, "wren", "lab-box")
	if scope != ScopeAgentAppliance {
		t.Fatalf("case-normalized key did not match: scope=%q", scope)
	}
	if !set[AllRiskCategories[0]] {
		t.Error("a category given in the wrong case was dropped")
	}
	if len(set) != 1 {
		t.Errorf("an unrecognized category survived into the set: %v", set)
	}
}

func TestListGrantsIsStableAndScoped(t *testing.T) {
	udb := grantStore(t)
	SaveCommandGrant(udb, "b-agent", "box", nil)
	SaveCommandGrant(udb, "a-agent", "box", nil)
	SaveCommandGrant(udb, "a-agent", anyAppliance, nil)
	got := ListCommandGrants(udb)
	if len(got) != 3 {
		t.Fatalf("listed %d grant(s), want 3", len(got))
	}
	if got[0].AgentID != "a-agent" || got[2].AgentID != "b-agent" {
		t.Errorf("unstable order: %+v", got)
	}
	// A grant with no agent is not writable — it would apply to everything.
	SaveCommandGrant(udb, "", "box", nil)
	if len(ListCommandGrants(udb)) != 3 {
		t.Error("an agent-less grant was stored")
	}
}
