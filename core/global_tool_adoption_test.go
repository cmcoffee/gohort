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

// TestGlobalToolAdoptACL covers the phase-5 adopt ACL: AllowedUsers on a Shared
// tool restricts who may see/adopt it, the field survives the kvlite/gob round
// trip, CanAdoptGlobalTool enforces the rule (open=all, restricted=members,
// unpublished=harmless, anon=never), and SetGlobalToolAdopted refuses an
// ACL-denied adopt while always permitting un-adopt.
func TestGlobalToolAdoptACL(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })

	// alice publishes two shared tools: "payroll" restricted to herself, and
	// "weather" open to everyone (empty AllowedUsers).
	db.Set(persistentTempToolsTable, "alice", []PersistentTempTool{
		{Tool: TempTool{Name: "payroll"}, Shared: true, AllowedUsers: []string{"alice"}},
		{Tool: TempTool{Name: "weather"}, Shared: true},
	})

	// AllowedUsers survives the kvlite/gob round-trip.
	var got []string
	for _, p := range LoadPersistentTempTools(db, "alice") {
		if p.Tool.Name == "payroll" {
			got = p.AllowedUsers
		}
	}
	if len(got) != 1 || got[0] != "alice" {
		t.Fatalf("AllowedUsers must persist through the pool round-trip; got %v", got)
	}

	// ACL predicate.
	if !CanAdoptGlobalTool(db, "alice", "payroll") {
		t.Fatal("alice is in payroll's ACL; must be permitted")
	}
	if CanAdoptGlobalTool(db, "bob", "payroll") {
		t.Fatal("bob is not in payroll's ACL; must be denied")
	}
	if !CanAdoptGlobalTool(db, "bob", "weather") {
		t.Fatal("weather is open (empty ACL); bob must be permitted")
	}
	if !CanAdoptGlobalTool(db, "carol", "unpublished") {
		t.Fatal("an unpublished name is harmless; must be permitted (existence != permission)")
	}
	if CanAdoptGlobalTool(db, "", "weather") {
		t.Fatal("anonymous must never be permitted")
	}

	// Adopt guard refuses an ACL-denied adopt but allows a permitted one.
	if err := SetGlobalToolAdopted(db, "bob", "payroll", true); err == nil {
		t.Fatal("SetGlobalToolAdopted must refuse an ACL-denied adopt")
	}
	if LoadAdoptedGlobalTools(db, "bob")["payroll"] {
		t.Fatal("a refused adopt must not persist")
	}
	if err := SetGlobalToolAdopted(db, "alice", "payroll", true); err != nil {
		t.Fatalf("alice is permitted; adopt should succeed: %v", err)
	}
	// Un-adopt is ALWAYS allowed, even for a user who could never adopt — a
	// tightened ACL must never strand an un-removable tool.
	if err := SetGlobalToolAdopted(db, "bob", "payroll", false); err != nil {
		t.Fatalf("un-adopt must always be allowed: %v", err)
	}
}

// TestSharingResolvesPromotionRequest pins the fix for a stale "Publish requested"
// badge: sharing a tool — by ANY path — fulfills a pending publish request, so the
// request queue and the owner's badge don't linger on an already-shared tool.
func TestSharingResolvesPromotionRequest(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })

	db.Set(persistentTempToolsTable, "alice", []PersistentTempTool{{Tool: TempTool{Name: "weather"}}})
	if err := CreatePromotionRequest(db, "alice", "tool", "weather", "please"); err != nil {
		t.Fatal(err)
	}
	if !PendingPromotion(db, "alice", "tool", "weather") {
		t.Fatal("precondition: request should be pending")
	}
	if err := SetPersistentTempToolShared(db, "alice", "weather", true); err != nil {
		t.Fatal(err)
	}
	if PendingPromotion(db, "alice", "tool", "weather") {
		t.Fatal("sharing the tool must fulfill (clear) the pending publish request")
	}
}

// TestSetPersistentTempToolAllowedUsers covers the admin adopt-ACL setter: it
// normalizes (trim/dedupe/sort), updates CanAdoptGlobalTool, errors on an unknown
// tool, and an empty list re-opens the tool to everyone.
func TestSetPersistentTempToolAllowedUsers(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })

	db.Set(persistentTempToolsTable, "alice", []PersistentTempTool{
		{Tool: TempTool{Name: "payroll"}, Shared: true},
	})

	// Unknown tool errors.
	if err := SetPersistentTempToolAllowedUsers(db, "alice", "nope", []string{"bob"}); err == nil {
		t.Fatal("setting ACL on an unknown tool must error")
	}

	// Set + normalize: duplicates, blanks, and whitespace collapse to a sorted set.
	if err := SetPersistentTempToolAllowedUsers(db, "alice", "payroll", []string{" carol ", "bob", "bob", ""}); err != nil {
		t.Fatal(err)
	}
	got, found := SharedToolAllowedUsers(db, "payroll")
	if !found || len(got) != 2 || got[0] != "bob" || got[1] != "carol" {
		t.Fatalf("ACL must be trimmed/deduped/sorted [bob carol]; got %v", got)
	}
	if !CanAdoptGlobalTool(db, "bob", "payroll") || CanAdoptGlobalTool(db, "dave", "payroll") {
		t.Fatal("ACL must admit bob and deny dave")
	}

	// Empty list re-opens the tool to everyone.
	if err := SetPersistentTempToolAllowedUsers(db, "alice", "payroll", nil); err != nil {
		t.Fatal(err)
	}
	if !CanAdoptGlobalTool(db, "dave", "payroll") {
		t.Fatal("an emptied ACL must re-open the tool to all users")
	}
}
