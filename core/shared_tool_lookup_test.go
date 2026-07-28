package core

import (
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// A deployment-wide shared tool is callable from every user's catalog but
// lives in its owner's pool. tool_def's lookups searched only the CALLER's
// pool, so a tool the model had just invoked answered "no tool named X" —
// which reads as "your tool vanished" rather than "you don't own this."
func TestFindSharedToolWithOwner(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prev := RootDB
	RootDB = db
	defer func() { RootDB = prev }()
	owner := "owner@example.com"
	other := "other@example.com"

	shared := PersistentTempTool{
		Tool:   TempTool{Name: "get_top_stories", Description: "news"},
		Shared: true,
	}
	private := PersistentTempTool{
		Tool: TempTool{Name: "private_helper"},
	}
	db.Set("persistent_temp_tools", owner, []PersistentTempTool{shared, private})

	got, gotOwner, ok := FindSharedToolWithOwner(db, "get_top_stories")
	if !ok {
		t.Fatal("shared tool not found")
	}
	if gotOwner != owner {
		t.Errorf("owner = %q, want %q", gotOwner, owner)
	}
	if got.Tool.Description != "news" {
		t.Errorf("returned the wrong record: %+v", got.Tool)
	}

	// An unshared tool must NOT surface — it isn't in anyone else's catalog,
	// so resolving it here would leak one user's pool into another's.
	if _, _, ok := FindSharedToolWithOwner(db, "private_helper"); ok {
		t.Error("an unshared tool leaked through the shared lookup")
	}
	if _, _, ok := FindSharedToolWithOwner(db, "nonexistent"); ok {
		t.Error("found a tool that does not exist")
	}
	if _, _, ok := FindSharedToolWithOwner(db, ""); ok {
		t.Error("empty name resolved")
	}

	// The lookup is deliberately owner-blind on the query side: any user
	// asking about a shared tool gets the same answer plus the owner, so the
	// caller can distinguish "yours, edit it" from "someone else's".
	_ = other
}

// The listing surface needs owners in one pass — the per-name lookup walks
// every user's pool, so calling it per row would be quadratic.
func TestSharedToolOwners(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prev := RootDB
	RootDB = db
	defer func() { RootDB = prev }()

	db.Set("persistent_temp_tools", "alice@example.com", []PersistentTempTool{
		{Tool: TempTool{Name: "get_top_stories"}, Shared: true},
		{Tool: TempTool{Name: "alice_private"}},
	})
	db.Set("persistent_temp_tools", "bob@example.com", []PersistentTempTool{
		{Tool: TempTool{Name: "bob_shared"}, Shared: true},
	})

	owners := SharedToolOwners(db)
	if owners["get_top_stories"] != "alice@example.com" {
		t.Errorf("get_top_stories owner = %q", owners["get_top_stories"])
	}
	if owners["bob_shared"] != "bob@example.com" {
		t.Errorf("bob_shared owner = %q", owners["bob_shared"])
	}
	// An unshared tool must not appear: the map drives a listing shown to
	// other users, so leaking a private name would be a disclosure.
	if _, ok := owners["alice_private"]; ok {
		t.Error("an unshared tool leaked into the shared-owner map")
	}
	if len(owners) != 2 {
		t.Errorf("map has %d entries, want 2: %v", len(owners), owners)
	}
}
