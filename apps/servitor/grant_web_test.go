// Connecting an agent to a machine. The failures worth pinning are the ones
// where the UI grants more than the operator meant, or silently takes away what
// they had already granted.
package servitor

import "testing"

// Access is set on the APPLIANCE, from the chat page's Access button, and these
// are the endpoints that modal talks to. There is no server-rendered form for
// it any more: Manage is gone, because the toolbar already creates and edits
// appliances and a second page for the same thing is a second place to look.
func TestGrantEndpointConnectsWithoutPermitting(t *testing.T) {
	udb := grantStore(t)
	g := SaveCommandGrant(udb, "wren", "lab-box", nil)
	if len(g.Categories) != 0 {
		t.Errorf("connecting must not permit anything, got %v", g.Categories)
	}
	// Enough for the agent to ASK about the machine...
	if !applianceEnabledForAgent(udb, "wren", "lab-box") {
		t.Error("a connected agent should be able to request capabilities")
	}
	// ...and not enough to run anything without approval.
	if ok, _ := autoRunAllowed(udb, "wren", "lab-box", AllRiskCategories[0]); ok {
		t.Error("connecting must not grant auto-run")
	}
}

func TestDisconnectRemovesAccess(t *testing.T) {
	udb := grantStore(t)
	SaveCommandGrant(udb, "wren", "lab-box", nil)
	DeleteCommandGrant(udb, "wren", "lab-box")
	if applianceEnabledForAgent(udb, "wren", "lab-box") {
		t.Error("a disconnected agent must lose access to the machine")
	}
}
// The grantable-targets picker hands the modal "agent:<id>" values, but the
// runtime dispatches with the bare id. A grant saved under the wrapped form
// used to be unfindable by the very run it was written for — the connection
// looked made and did nothing.
func TestPickerWrappedAgentIDConnectsTheBareAgent(t *testing.T) {
	udb := grantStore(t)
	g := SaveCommandGrant(udb, "agent:wren", "lab-box", nil)
	if g.AgentID != "wren" {
		t.Errorf("stored agent id should be bare, got %q", g.AgentID)
	}
	if !applianceEnabledForAgent(udb, "wren", "lab-box") {
		t.Error("a connection saved from the picker must reach the dispatching agent")
	}
	// The exec gate resolves with the bare id too; the wrapped save must not
	// leave it falling through to the user default.
	if _, scope := ResolveCommandGrant(udb, "wren", "lab-box"); scope != ScopeAgentAppliance {
		t.Errorf("bare-id lookup should hit the grant, resolved via %q", scope)
	}
	// And removing through the modal (which may still hold either form) works.
	DeleteCommandGrant(udb, "agent:wren", "lab-box")
	if applianceEnabledForAgent(udb, "wren", "lab-box") {
		t.Error("disconnecting with the wrapped id must remove the connection")
	}
}

// A record written before ids were normalized sits under a doubled key. It must
// still answer lookups, get re-keyed on first touch, and stay deletable.
func TestLegacyWrappedGrantMigratesOnLookup(t *testing.T) {
	udb := grantStore(t)
	legacy := CommandGrant{AgentID: "agent:wren", ApplianceID: "lab-box"}
	udb.Set(commandGrantsTable, legacyCommandGrantKey("wren", "lab-box"), legacy)
	if !applianceEnabledForAgent(udb, "wren", "lab-box") {
		t.Fatal("a legacy grant must still connect the agent")
	}
	// First touch re-keyed it: the modern key now answers directly.
	if g, ok := loadCommandGrant(udb, "wren", "lab-box"); !ok || g.AgentID != "wren" {
		t.Errorf("migration should leave a bare-id record under the modern key, got %+v ok=%v", g, ok)
	}
	var gone CommandGrant
	if udb.Get(commandGrantsTable, legacyCommandGrantKey("wren", "lab-box"), &gone) {
		t.Error("the legacy key should be removed after migration")
	}
	DeleteCommandGrant(udb, "wren", "lab-box")
	if applianceEnabledForAgent(udb, "wren", "lab-box") {
		t.Error("a migrated grant must be deletable")
	}
}

// A category name that is not one this build knows must not become a permission
// by arriving in the request body.
func TestOnlyKnownCategoriesAreStored(t *testing.T) {
	udb := grantStore(t)
	real := string(AllRiskCategories[0])
	SaveCommandGrant(udb, "wren", "lab-box", []string{real, "not_a_category"})
	set, _ := ResolveCommandGrant(udb, "wren", "lab-box")
	if !set[AllRiskCategories[0]] {
		t.Error("the real category should be stored")
	}
	if len(set) != 1 {
		t.Errorf("an unknown category must not be stored, got %v", set)
	}
}
