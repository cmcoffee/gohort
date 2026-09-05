package core

import "testing"

// A live entry may name a second place for its owner to go — the conversation
// the work is running in — beside the place everyone else may look. The
// substitution is the owner's alone, and an entry with no owner has none.
func TestLiveEntryOwnerDestination(t *testing.T) {
	base := LiveEntry{ID: "r1", Owner: "craig", URL: "/monitor?run=r1", OwnerURL: "/orchestrate/?agent=a1&session=s1"}

	e := base
	e.applyOwnerDestination("craig")
	if e.URL != "/orchestrate/?agent=a1&session=s1" {
		t.Errorf("the owner goes to the conversation, got %q", e.URL)
	}
	for _, viewer := range []string{"dana", ""} {
		e = base
		e.applyOwnerDestination(viewer)
		if e.URL != "/monitor?run=r1" {
			t.Errorf("viewer %q must keep the shared destination, got %q", viewer, e.URL)
		}
	}
	e = base
	e.Owner = ""
	e.applyOwnerDestination("craig")
	if e.URL != "/monitor?run=r1" {
		t.Errorf("an entry with no owner has no owner's destination, got %q", e.URL)
	}
	e = base
	e.OwnerURL = ""
	e.applyOwnerDestination("craig")
	if e.URL != "/monitor?run=r1" {
		t.Errorf("no OwnerURL means the owner goes where everyone goes, got %q", e.URL)
	}
}
