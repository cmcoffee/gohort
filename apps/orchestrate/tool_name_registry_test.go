package orchestrate

// The registry is reconciled on READ rather than written on create/delete,
// because the tool table has nine write sites and hooking each is how one gets
// missed. These pin that the reconciliation actually remembers and retires.

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func regDB(t *testing.T) Database {
	t.Helper()
	return &DBase{Store: kvlite.MemStore()}
}

func TestANameSeenOnceIsRetiredWhenItGoesAway(t *testing.T) {
	db := regDB(t)
	// First sweep: the tool is live, so nothing is retired.
	if got := observeToolNames(db, "u", map[string]bool{"scan_bundle": true}); got["scan_bundle"] {
		t.Fatal("a live tool must not be reported as retired")
	}
	// It is deleted. The next sweep sees it missing from the pool and knows it
	// used to be there.
	if got := observeToolNames(db, "u", map[string]bool{}); !got["scan_bundle"] {
		t.Error("a name seen before and gone now is retired")
	}
}

// A name nobody ever had is not retired — it is just a word.
func TestAnUnseenNameIsNeverRetired(t *testing.T) {
	db := regDB(t)
	observeToolNames(db, "u", map[string]bool{"scan_bundle": true})
	if got := observeToolNames(db, "u", map[string]bool{}); got["parse_config"] {
		t.Error("a function name that was never a tool must never be retired")
	}
}

// Per user: one owner's deletions are not another's.
func TestTheRegistryIsPerUser(t *testing.T) {
	db := regDB(t)
	observeToolNames(db, "alice", map[string]bool{"scan_bundle": true})
	if got := observeToolNames(db, "bob", map[string]bool{}); got["scan_bundle"] {
		t.Error("bob never had that tool, so it cannot have retired for him")
	}
}

// Removed framework tools never appear in any pool, so the reconciler could
// never notice them — they are declared, and that is the one class the
// mechanism cannot self-discover.
func TestRemovedFrameworkToolsAreRetiredWithoutHavingBeenSeen(t *testing.T) {
	db := regDB(t)
	got := observeToolNames(db, "u", map[string]bool{})
	for _, want := range []string{"store_fact", "knowledge_search", "memory"} {
		if !got[want] {
			t.Errorf("%q was removed with the unified surface and should be retired", want)
		}
	}
}

// A framework name that came BACK (or a user minting one themselves) is live
// again and must stop being reported.
func TestAFrameworkNameThatExistsAgainIsNotRetired(t *testing.T) {
	db := regDB(t)
	got := observeToolNames(db, "u", map[string]bool{"store_fact": true})
	if got["store_fact"] {
		t.Error("a name present in the pool is live, whatever the static list says")
	}
}

// Nil db: the declared retirements still work, so a caller without storage
// degrades to the static list rather than to nothing.
func TestNoStorageStillReportsDeclaredRetirements(t *testing.T) {
	if got := observeToolNames(nil, "u", map[string]bool{}); !got["store_fact"] {
		t.Error("the static list should not depend on a database")
	}
}
