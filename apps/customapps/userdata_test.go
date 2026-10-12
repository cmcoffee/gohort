package customapps

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A deleted account's apps move to someone else with their records and their
// sharing, or are deleted; nothing is left filed under the old name.
func TestADeletedAccountsAppsMoveOrGo(t *testing.T) {
	T := sharingTestApp(t)
	h := &appsUserData{app: T}
	SaveAppSpec(AppSpec{Slug: "tally", Name: "Tally", Owner: "bob", RecordKey: "id", SharedCollections: []string{"board"}})
	SaveAppSpec(AppSpec{Slug: "notes", Name: "Notes", Owner: "bob", RecordKey: "id"})
	SaveAppSpec(AppSpec{Slug: "notes", Name: "Alice's own notes", Owner: "alice", RecordKey: "id"})
	if err := T.setShared("bob", "tally", true); err != nil {
		t.Fatal(err)
	}
	bobBase := T.recordBase(AppSpec{Slug: "tally", Owner: "bob"}, "bob")
	bobBase.Set(recTable("tally"), "r1", map[string]any{"id": "r1", "n": 3})
	bobBase.Set(sharedTable("tally", "board"), "s1", map[string]any{"id": "s1", "by": "carol"})

	if s := h.Describe("bob"); s.Counts["apps"] != 2 || s.Counts["shared"] != 1 {
		t.Fatalf("describe: %+v", s.Counts)
	}
	if err := h.Reassign("bob", "alice"); err != nil {
		t.Fatal(err)
	}
	if len(listSpecs("bob")) != 0 {
		t.Error("bob still owns apps after they moved")
	}
	moved, ok := loadSpec("alice", "tally")
	if !ok || moved.Owner != "alice" || !moved.Shared {
		t.Fatalf("tally did not arrive as alice's shared app: %+v %v", moved, ok)
	}
	var rec map[string]any
	if !T.recordBase(moved, "alice").Get(recTable("tally"), "r1", &rec) {
		t.Error("bob's records did not travel with the app")
	}
	if !T.recordBase(moved, "alice").Get(sharedTable("tally", "board"), "s1", &rec) {
		t.Error("the app's shared collection, everyone's entries, stayed with bob")
	}
	if owner, _ := LookupSharedOwner(T.DB, sharedAppsIndex, "tally"); owner != "alice" {
		t.Errorf("the shared index still names %q", owner)
	}
	// Alice had her own "notes": bob's arrives beside it, hers untouched.
	if own, _ := loadSpec("alice", "notes"); own.Name != "Alice's own notes" {
		t.Error("the move overwrote the new owner's own app")
	}
	if got, ok := loadSpec("alice", "notes-2"); !ok || got.Name != "Notes" {
		t.Error("bob's notes should arrive under a free slug")
	}

	// Purge: carol's apps go, sharing included.
	SaveAppSpec(AppSpec{Slug: "poll", Name: "Poll", Owner: "carol"})
	T.setShared("carol", "poll", true)
	if err := h.Purge("carol"); err != nil {
		t.Fatal(err)
	}
	if len(listSpecs("carol")) != 0 {
		t.Error("purge left carol's apps")
	}
	if _, shared := LookupSharedOwner(T.DB, sharedAppsIndex, "poll"); shared {
		t.Error("purge left the app in the shared index")
	}
}
