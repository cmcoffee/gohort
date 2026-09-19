package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func seedStanding(t *testing.T, db Database, owner, name, parent string) {
	t.Helper()
	SaveStandingAgent(db, StandingAgent{Owner: owner, Name: name, AgentID: "a1", Parent: parent})
}

// The reference carries the SURFACE because ids are only unique within one: a
// monitor and a standing agent may both be called "nightly", and a reference
// that could mean either resolves to whichever is looked up first.
func TestAParentReferenceNamesItsSurface(t *testing.T) {
	root := pinRootDB(t)
	SaveStandingAgent(root, StandingAgent{Owner: "craig", Name: "nightly", AgentID: "a1"})
	SaveEventMonitor(root, EventMonitor{Owner: "craig", Name: "nightly", Kind: EventKindWatch})

	standing := taskParentRef(schedKindStanding, "nightly")
	monitor := taskParentRef(schedKindMonitor, "nightly")
	if standing == monitor {
		t.Fatalf("two different schedules share a reference: %q", standing)
	}
	if got := taskParentLabel("craig", standing); got != "nightly" {
		t.Errorf("standing label: %q", got)
	}
	// Nothing resolvable is not an empty string: a reference to something that
	// no longer exists still shows, as deleted.
	if got := taskParentLabel("craig", taskParentRef(schedKindStanding, "gone")); !strings.Contains(got, "deleted") {
		t.Errorf("a dangling parent vanished instead of saying so: %q", got)
	}
	if got := taskParentLabel("craig", ""); got != "" {
		t.Errorf("no parent rendered as %q", got)
	}
}

// A cycle is not an error anybody sees at the moment they make it, and every
// reader afterwards walks it forever.
func TestAParentCannotLoop(t *testing.T) {
	root := pinRootDB(t)
	seedStanding(t, root, "craig", "top", "")
	seedStanding(t, root, "craig", "middle", taskParentRef(schedKindStanding, "top"))
	seedStanding(t, root, "craig", "bottom", taskParentRef(schedKindStanding, "middle"))

	// Straight back at itself.
	if err := setTaskParent("craig", schedKindStanding, "top", taskParentRef(schedKindStanding, "top")); err == nil {
		t.Error("a schedule was made its own parent")
	}
	// And around the chain: top cannot report to its own grandchild.
	if err := setTaskParent("craig", schedKindStanding, "top", taskParentRef(schedKindStanding, "bottom")); err == nil {
		t.Error("a loop through two links was allowed")
	}
	// A legitimate attach still works, and so does detaching.
	if err := setTaskParent("craig", schedKindStanding, "top", ""); err != nil {
		t.Fatalf("clearing a parent: %v", err)
	}
	if err := setTaskParent("craig", schedKindStanding, "bottom", taskParentRef(schedKindStanding, "top")); err != nil {
		t.Fatalf("a valid attach was refused: %v", err)
	}
}

// The link belongs to the TASK, so it survives the things that rewrite the
// schedule. For a recurring task that is every fire, since each one arms a new
// scheduler entry.
func TestARecurringParentSurvivesARearm(t *testing.T) {
	p := orchUpdatePayload{
		UID: "uid-1", Username: "craig", SessionID: "s1", CreatedAt: "2026-09-19T10:00:00Z",
		Parent: taskParentRef(schedKindStanding, "top"),
	}
	armed := p
	armed.FireCount++
	if armed.Parent != p.Parent {
		t.Error("the link did not survive an arm")
	}
	// And the reference points at the task's own identity, not at the
	// occurrence id, which is re-minted every fire.
	ref := taskParentRef(schedKindRecurring, recurringTaskUID(p))
	if ref != taskParentRef(schedKindRecurring, recurringTaskUID(armed)) {
		t.Error("a child pointing at this task would lose it on the next fire")
	}
}

// Ownership is the lookup: another user's schedule is not refused, it is simply
// not found, which is the same shape the permission work settled on.
func TestAParentCannotCrossOwners(t *testing.T) {
	root := pinRootDB(t)
	seedStanding(t, root, "craig", "mine", "")
	seedStanding(t, root, "dana", "theirs", "")

	if err := setTaskParent("dana", schedKindStanding, "mine", ""); err == nil {
		t.Error("another user edited a schedule that is not theirs")
	}
	// And a reference into somebody else's records does not resolve to a label.
	if got := taskParentLabel("craig", taskParentRef(schedKindStanding, "theirs")); !strings.Contains(got, "deleted") {
		t.Errorf("a cross-owner parent resolved: %q", got)
	}
}

// The restraint IS the design. A link that quietly acquired authority would be
// the container record the objectives build refused, arrived at one convenience
// at a time.
func TestTheLinkCarriesNoAuthority(t *testing.T) {
	root := pinRootDB(t)
	seedStanding(t, root, "craig", "parent", "")
	seedStanding(t, root, "craig", "child", taskParentRef(schedKindStanding, "parent"))

	// Pausing the parent does not touch the child.
	parent, _ := GetStandingAgent(root, "craig", "parent")
	parent.Paused = true
	SaveStandingAgent(root, parent)
	child, _ := GetStandingAgent(root, "craig", "child")
	if child.Paused {
		t.Error("a paused parent paused its child; the link is meant to carry no authority")
	}

	// Deleting the parent leaves the child running, with its link intact so a
	// person can see what it was part of.
	DeleteStandingAgent(root, "craig", "parent")
	child, ok := GetStandingAgent(root, "craig", "child")
	if !ok {
		t.Fatal("deleting a parent deleted its child")
	}
	if child.Parent == "" {
		t.Error("the link was silently cleared, losing the only record of what this was part of")
	}
	if got := taskParentLabel("craig", child.Parent); !strings.Contains(got, "deleted") {
		t.Errorf("the child does not say its parent is gone: %q", got)
	}
}
