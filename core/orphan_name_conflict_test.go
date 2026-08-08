// A tool name may live in exactly one home. AdminPersistTempTool has always
// enforced that between the pending queue and the active pool, and the orphan
// pool was simply left out of the rule — so deleting the last agent that
// carried a tool, then re-creating the tool under the same name, produced two
// rows the admin UI rendered side by side, and left the stale copy
// re-homeable on top of the live one.
package core

import (
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/kvlite"
)

func TestPersistingANameClearsItsOrphan(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prev := RootDB
	RootDB = db
	defer func() { RootDB = prev }()

	const user, name = "craig@example.com", "get_top_stories"

	// The agent carrying the tool was deleted, so the definition went here.
	AddOrphanedTempTools(db, user, []OrphanedTempTool{{
		Tool:            TempTool{Name: name, Description: "news (old copy)"},
		FormerAgentName: "Wren",
		OrphanedAt:      time.Now(),
	}})

	// The user re-creates it. That IS the resolution of the orphan.
	if err := AdminPersistTempTool(db, user, TempTool{Name: name, Description: "news"}); err != nil {
		t.Fatalf("persist: %v", err)
	}

	if got := LoadOrphanedTempTools(db, user); len(got) != 0 {
		t.Errorf("the orphan survived alongside the live tool: %+v", got)
	}
	live, ok := UserToolByName(db, user, name)
	if !ok {
		t.Fatal("the live tool is missing")
	}
	if live.Tool.Description != "news" {
		t.Errorf("the stale copy won: %q", live.Tool.Description)
	}
}

// The clear is inlined under the already-held tempToolPersistMu, which is not
// reentrant — the naive call to the exported RemoveOrphanedTempTool would
// hang forever rather than fail.
func TestPersistWithAnOrphanDoesNotDeadlock(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prev := RootDB
	RootDB = db
	defer func() { RootDB = prev }()

	const user, name = "craig@example.com", "get_top_stories"
	AddOrphanedTempTools(db, user, []OrphanedTempTool{{Tool: TempTool{Name: name}}})

	done := make(chan error, 1)
	go func() { done <- AdminPersistTempTool(db, user, TempTool{Name: name}) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("persist returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("AdminPersistTempTool deadlocked clearing the orphan under its own lock")
	}
}

// Unrelated orphans are not collateral damage.
func TestPersistLeavesOtherOrphansAlone(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prev := RootDB
	RootDB = db
	defer func() { RootDB = prev }()

	const user = "craig@example.com"
	AddOrphanedTempTools(db, user, []OrphanedTempTool{
		{Tool: TempTool{Name: "get_top_stories"}},
		{Tool: TempTool{Name: "check_surf_report"}},
	})

	if err := AdminPersistTempTool(db, user, TempTool{Name: "get_top_stories"}); err != nil {
		t.Fatalf("persist: %v", err)
	}

	got := LoadOrphanedTempTools(db, user)
	if len(got) != 1 || got[0].Tool.Name != "check_surf_report" {
		t.Errorf("orphan pool = %+v, want only check_surf_report", got)
	}
}
