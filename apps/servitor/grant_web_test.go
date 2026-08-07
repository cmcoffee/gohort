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
