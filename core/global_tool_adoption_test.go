package core

import (
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// TestGlobalToolAdoption covers the opt-in adoption store: adopt adds, adopt
// again is idempotent, unadopt removes, isolation is per-user, and Merge unions
// (the migration's grandfather path) without clobbering existing adoptions.
func TestGlobalToolAdoption(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })

	// Empty to start.
	if got := LoadAdoptedGlobalTools(db, "alice"); len(got) != 0 {
		t.Fatalf("fresh user must have no adoptions; got %v", got)
	}

	// Adopt is idempotent; unadopt removes.
	if err := SetGlobalToolAdopted(db, "alice", "weather", true); err != nil {
		t.Fatal(err)
	}
	if err := SetGlobalToolAdopted(db, "alice", "weather", true); err != nil {
		t.Fatal(err)
	}
	if err := SetGlobalToolAdopted(db, "alice", "jira", true); err != nil {
		t.Fatal(err)
	}
	a := LoadAdoptedGlobalTools(db, "alice")
	if !a["weather"] || !a["jira"] || len(a) != 2 {
		t.Fatalf("alice should have adopted weather+jira once each; got %v", a)
	}
	if err := SetGlobalToolAdopted(db, "alice", "weather", false); err != nil {
		t.Fatal(err)
	}
	a = LoadAdoptedGlobalTools(db, "alice")
	if a["weather"] || !a["jira"] {
		t.Fatalf("unadopt should drop only weather; got %v", a)
	}

	// Per-user isolation: bob is unaffected by alice.
	if b := LoadAdoptedGlobalTools(db, "bob"); len(b) != 0 {
		t.Fatalf("bob must not see alice's adoptions; got %v", b)
	}

	// Merge unions the migration's grandfathered names without dropping jira.
	MergeAdoptedGlobalTools(db, "alice", []string{"weather", "pager"})
	a = LoadAdoptedGlobalTools(db, "alice")
	if !a["jira"] || !a["weather"] || !a["pager"] || len(a) != 3 {
		t.Fatalf("merge must union with existing; got %v", a)
	}
}
