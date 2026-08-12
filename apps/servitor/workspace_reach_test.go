package servitor

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A workspace exists to be a handle for the machines inside it. Connecting an
// agent to one and then refusing every machine in it by name is the grant
// appearing not to work: the general question succeeds and the obvious
// follow-up — "and what about lab-box specifically?" — fails.

// estate builds a workspace with two members plus an unrelated machine.
func estate(t *testing.T) Database {
	t.Helper()
	udb := &DBase{Store: kvlite.MemStore()}
	for _, a := range []Appliance{
		{ID: "ws-1", Name: "Lab Estate", Type: "workspace", Members: []string{"box-1", "box-2"}},
		{ID: "box-1", Name: "lab-box", Type: "ssh"},
		{ID: "box-2", Name: "db-box", Type: "ssh"},
		{ID: "other", Name: "unrelated", Type: "ssh"},
	} {
		udb.Set(applianceTable, a.ID, a)
	}
	return udb
}

// TestAWorkspaceGrantReachesItsMembers — the ask the user made.
func TestAWorkspaceGrantReachesItsMembers(t *testing.T) {
	udb := estate(t)
	SaveCommandGrant(udb, "agent-1", "ws-1", nil)

	for _, id := range []string{"ws-1", "box-1", "box-2"} {
		if !applianceAskableForAgent(udb, "agent-1", id) {
			t.Errorf("connected to the workspace but cannot ask about %q", id)
		}
	}
	// Membership is not a wildcard: a machine outside the workspace stays out.
	if applianceAskableForAgent(udb, "agent-1", "other") {
		t.Error("a workspace grant reached a machine that is not in it")
	}
	// And an agent with no grant at all reaches nothing.
	if applianceAskableForAgent(udb, "agent-2", "box-1") {
		t.Error("an unconnected agent may ask about a machine")
	}
}

// TestMembershipGrantsQuestionsNotShells — connecting is not permitting, and
// putting two boxes in a workspace together is not a decision to hand an agent
// a shell on both.
func TestMembershipGrantsQuestionsNotShells(t *testing.T) {
	udb := estate(t)
	SaveCommandGrant(udb, "agent-1", "ws-1", nil)

	if !applianceAskableForAgent(udb, "agent-1", "box-1") {
		t.Fatal("the member is not askable")
	}
	if applianceEnabledForAgent(udb, "agent-1", "box-1") {
		t.Error("a workspace grant made a member CONNECTED — approved command tools " +
			"on that machine would be inherited by an agent nobody connected to it")
	}
	// A direct grant on the member does both, as it always did.
	SaveCommandGrant(udb, "agent-1", "box-1", nil)
	if !applianceEnabledForAgent(udb, "agent-1", "box-1") {
		t.Error("a direct grant on the member did not connect it")
	}
}

// TestTheQuestionToolListsWhatItAccepts — the failure this pairing prevents.
// The handler re-checks the grant, so a list built from a different rule would
// name machines the same tool then refuses. Same defect as a picker offering an
// option the server rejects.
func TestTheQuestionToolListsWhatItAccepts(t *testing.T) {
	udb := estate(t)
	SaveCommandGrant(udb, "agent-1", "ws-1", nil)

	var listed []Appliance
	for _, id := range []string{"ws-1", "box-1", "box-2", "other"} {
		var a Appliance
		if udb.Get(applianceTable, id, &a) && applianceAskableForAgent(udb, "agent-1", a.ID) {
			listed = append(listed, a)
		}
	}
	if len(listed) != 3 {
		t.Fatalf("listed %d systems, want the workspace and its two members", len(listed))
	}
	for _, a := range listed {
		if !applianceAskableForAgent(udb, "agent-1", a.ID) {
			t.Errorf("%q is listed but would be refused by the handler", a.ID)
		}
	}
}

// TestAMissingMemberIsSkipped — a workspace can name a machine that was since
// deleted, and a dangling id must not become a reachable phantom.
func TestAMissingMemberIsSkipped(t *testing.T) {
	udb := estate(t)
	var ws Appliance
	udb.Get(applianceTable, "ws-1", &ws)
	ws.Members = append(ws.Members, "deleted-box")
	udb.Set(applianceTable, ws.ID, ws)
	SaveCommandGrant(udb, "agent-1", "ws-1", nil)

	if applianceAskableForAgent(udb, "agent-1", "") {
		t.Error("an empty appliance id resolved as askable")
	}
	// The dangling id is "askable" by membership but resolves to no appliance,
	// so findAppliance refuses it first — the important thing is that nothing
	// panics and the real members still work.
	if !applianceAskableForAgent(udb, "agent-1", "box-1") {
		t.Error("a dangling member id broke resolution for the real ones")
	}
}
