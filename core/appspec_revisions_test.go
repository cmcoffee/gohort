package core

// Tests for the app revision ring: what gets kept, what deliberately doesn't,
// how deep it goes, and that a restored revision is the real prior document
// rather than a reference to a spec that has since moved on.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/kvlite"
)

func revisionTestDB(t *testing.T) {
	t.Helper()
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
}

func savePage(t *testing.T, slug, page, reason string) AppSpec {
	t.Helper()
	spec, ok := LoadAppSpec("alice", slug)
	if !ok {
		spec = AppSpec{Slug: slug, Name: "Game", Owner: "alice", RecordKey: "id"}
	}
	spec.Page = json.RawMessage(page)
	return SaveAppSpecAs(spec, reason)
}

// TestSaveKeepsThePriorVersion — the whole point: after an edit, the thing it
// replaced is still reachable.
func TestSaveKeepsThePriorVersion(t *testing.T) {
	revisionTestDB(t)
	first := savePage(t, "game", `{"v":1}`, "create")
	savePage(t, "game", `{"v":2}`, "update")

	revs := ListAppRevisions("alice", "game")
	if len(revs) != 1 {
		t.Fatalf("expected one kept revision, got %d", len(revs))
	}
	if revs[0].Stamp != first.Updated {
		t.Errorf("kept revision stamp = %q, want the first save's %q", revs[0].Stamp, first.Updated)
	}
	if revs[0].Reason != "update" {
		t.Errorf("reason should name the edit that replaced it, got %q", revs[0].Reason)
	}
	back, ok := LoadAppRevision("alice", "game", "1")
	if !ok {
		t.Fatal("could not load the kept revision")
	}
	if string(back.Page) != `{"v":1}` {
		t.Errorf("restored page = %s, want the original", back.Page)
	}
	// The id is what an author copies out of a listing, so "#1" and a bare
	// timestamp both have to resolve to the same thing.
	if r, ok := FindAppRevision("alice", "game", "#1"); !ok || r.Seq != 1 {
		t.Error("a #-prefixed id should resolve")
	}
	if r, ok := FindAppRevision("alice", "game", first.Updated); !ok || r.Seq != 1 {
		t.Error("a timestamp should still resolve")
	}
}

// TestRevisionIdsAreStableAcrossSameSecondSaves — Updated has second
// resolution, so a burst of edits produces colliding timestamps. Ids must
// still address exactly one document each.
func TestRevisionIdsAreStableAcrossSameSecondSaves(t *testing.T) {
	revisionTestDB(t)
	for i := 1; i <= 4; i++ {
		savePage(t, "game", `{"v":`+itoaSmall(i)+`}`, "update")
	}
	revs := ListAppRevisions("alice", "game")
	if len(revs) != 3 {
		t.Fatalf("expected three kept revisions, got %d", len(revs))
	}
	seen := map[int]bool{}
	for _, r := range revs {
		if seen[r.Seq] {
			t.Fatalf("revision id %d was reused", r.Seq)
		}
		seen[r.Seq] = true
		spec, ok := LoadAppRevision("alice", "game", itoaSmall(r.Seq))
		if !ok {
			t.Fatalf("id %d did not resolve", r.Seq)
		}
		// Ids are assigned in save order, so #n holds version n.
		if want := `{"v":` + itoaSmall(r.Seq) + `}`; string(spec.Page) != want {
			t.Errorf("revision #%d holds %s, want %s", r.Seq, spec.Page, want)
		}
	}
}

// TestMetadataWritesAreNotRevisions — a disable/share/publish flip must not
// push a real version out of a six-deep ring.
func TestMetadataWritesAreNotRevisions(t *testing.T) {
	revisionTestDB(t)
	savePage(t, "game", `{"v":1}`, "create")
	savePage(t, "game", `{"v":2}`, "update")

	spec, _ := LoadAppSpec("alice", "game")
	spec.Disabled = true
	SaveAppSpecAs(spec, "disable")
	spec.PublicToken = "abc123"
	SaveAppSpecAs(spec, "publish")

	if revs := ListAppRevisions("alice", "game"); len(revs) != 1 {
		t.Errorf("metadata writes should not file revisions, ring has %d", len(revs))
	}
}

// TestNoHistorySuppressesTheSnapshot — the rollback path restores a good
// revision after a failed edit; filing the broken one as history would offer
// an author a version that never worked.
func TestNoHistorySuppressesTheSnapshot(t *testing.T) {
	revisionTestDB(t)
	savePage(t, "game", `{"v":1}`, "create")
	savePage(t, "game", `{"v":2}`, "update")
	before := len(ListAppRevisions("alice", "game"))

	savePage(t, "game", `{"v":1}`, AppSaveNoHistory)
	if after := len(ListAppRevisions("alice", "game")); after != before {
		t.Errorf("AppSaveNoHistory still filed a revision: %d → %d", before, after)
	}
}

// TestRingTrimsToDepthNewestFirst — old versions fall off the back, and the
// listing reads newest first because that is the one an author wants.
func TestRingTrimsToDepthNewestFirst(t *testing.T) {
	revisionTestDB(t)
	total := AppRevisionsKept + 3
	for i := 1; i <= total; i++ {
		savePage(t, "game", `{"v":`+itoaSmall(i)+`}`, "update")
	}
	revs := ListAppRevisions("alice", "game")
	if len(revs) != AppRevisionsKept {
		t.Fatalf("ring depth = %d, want %d", len(revs), AppRevisionsKept)
	}
	// Newest kept revision is the version saved immediately before the last.
	if revs[0].Seq != total-1 {
		t.Errorf("newest kept = #%d, want #%d", revs[0].Seq, total-1)
	}
	// The very first save has aged out, and its id is not recycled.
	if _, ok := FindAppRevision("alice", "game", "1"); ok {
		t.Error("the oldest version should have fallen out of the ring")
	}
}

// TestLoadRevisionDefaultsToMostRecent — revert with no stamp is the common
// case (undo what just happened).
func TestLoadRevisionDefaultsToMostRecent(t *testing.T) {
	revisionTestDB(t)
	savePage(t, "game", `{"v":1}`, "create")
	savePage(t, "game", `{"v":2}`, "update")
	savePage(t, "game", `{"v":3}`, "update")

	back, ok := LoadAppRevision("alice", "game", "")
	if !ok {
		t.Fatal("expected a default revision")
	}
	if string(back.Page) != `{"v":2}` {
		t.Errorf("default revert target = %s, want the version just replaced", back.Page)
	}
	if _, ok := LoadAppRevision("alice", "game", "2020-01-01T00:00:00Z"); ok {
		t.Error("an unknown stamp must not resolve to something arbitrary")
	}
}

// TestDeleteAppSpecDropsHistory — history is part of the app, not a separate
// artifact that outlives it.
func TestDeleteAppSpecDropsHistory(t *testing.T) {
	revisionTestDB(t)
	savePage(t, "game", `{"v":1}`, "create")
	savePage(t, "game", `{"v":2}`, "update")
	DeleteAppSpec("alice", "game")

	if revs := ListAppRevisions("alice", "game"); len(revs) != 0 {
		t.Errorf("deleting the app left %d revisions behind", len(revs))
	}
}

func TestAppRevisionAgeReadsPlainly(t *testing.T) {
	if got := AppRevisionAge("not a time"); got != "" {
		t.Errorf("unparsable stamp should render empty, got %q", got)
	}
	if got := AppRevisionAge(time.Now().UTC().Format(time.RFC3339)); got != "just now" {
		t.Errorf("a fresh stamp should read \"just now\", got %q", got)
	}
}
