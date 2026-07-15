package core

import (
	"fmt"
	"testing"
	"time"
)

// seedGraphEntity writes a node directly with a controlled Updated stamp so
// LRU ordering is deterministic in tests.
func seedGraphEntity(db Database, ns, id string, updated time.Time) {
	db.Set(GraphEntityTable, graphEntityKey(ns, id), GraphEntity{
		Namespace: ns, ID: id, Kind: "thing", Name: id,
		Created: updated, Updated: updated,
	})
}

// TestGraphEntityCapEvictsLRU: past the cap, the least-recently-updated
// entities go — edges included — and the just-added node (newest) survives.
func TestGraphEntityCapEvictsLRU(t *testing.T) {
	db := memDB(t)
	ns := "agent:x"
	db.Set(WebTable, TunableGraphEntityCap, float64(3))
	SetTunablesDB(db)
	defer SetTunablesDB(nil)

	base := time.Now().Add(-10 * time.Hour)
	for i := 0; i < 3; i++ {
		seedGraphEntity(db, ns, fmt.Sprintf("thing:e%d", i), base.Add(time.Duration(i)*time.Hour))
	}
	LinkGraphEdge(db, ns, "thing:e0", "knows", "thing:e1", "", false)

	// 4th node crosses the cap → e0 (oldest) and its edge must go.
	if _, isNew := UpsertGraphEntity(db, ns, "thing", "fresh node", nil, nil); !isNew {
		t.Fatal("expected a new node")
	}
	ents := ListGraphEntities(db, ns)
	if len(ents) != 3 {
		t.Fatalf("expected 3 entities at the cap, got %d", len(ents))
	}
	for _, e := range ents {
		if e.ID == "thing:e0" {
			t.Fatal("LRU eviction should have dropped the oldest node e0")
		}
	}
	if _, edges := GraphCounts(db, ns); edges != 0 {
		t.Fatalf("evicted node's edge should be gone, %d edges remain", edges)
	}
}

// TestGraphEdgeCapEvictsLRU: past the edge cap, the least-recently-updated
// LIVE edges go; retired tombstones are exempt (retention bounds those).
func TestGraphEdgeCapEvictsLRU(t *testing.T) {
	db := memDB(t)
	ns := "agent:x"
	db.Set(WebTable, TunableGraphEdgeCap, float64(2))
	SetTunablesDB(db)
	defer SetTunablesDB(nil)

	base := time.Now().Add(-10 * time.Hour)
	for i := 0; i < 2; i++ {
		from, to := fmt.Sprintf("a%d", i), fmt.Sprintf("b%d", i)
		db.Set(GraphEdgeTable, graphEdgeKey(ns, from, "knows", to), GraphEdge{
			Namespace: ns, From: from, Rel: "knows", To: to,
			Created: base.Add(time.Duration(i) * time.Hour), Updated: base.Add(time.Duration(i) * time.Hour),
		})
	}
	db.Set(GraphEdgeTable, graphEdgeKey(ns, "old", "was_at", "place"), GraphEdge{
		Namespace: ns, From: "old", Rel: "was_at", To: "place", Created: base, Updated: base,
		MemoryProvenance: MemoryProvenance{Reason: RetireSuperseded, RetiredAt: base},
	})

	// A new live edge crosses the cap → the oldest live edge (a0) goes.
	LinkGraphEdge(db, ns, "c", "knows", "d", "", false)
	live := scanGraphEdges(db, ns, func(e GraphEdge) bool { return !e.Retired() })
	if len(live) != 2 {
		t.Fatalf("expected 2 live edges at the cap, got %d", len(live))
	}
	for _, e := range live {
		if e.From == "a0" {
			t.Fatal("LRU eviction should have dropped the oldest live edge a0")
		}
	}
	var tomb GraphEdge
	if !db.Get(GraphEdgeTable, graphEdgeKey(ns, "old", "was_at", "place"), &tomb) || !tomb.Retired() {
		t.Fatal("retired tombstone must be exempt from the live-edge cap")
	}
}

// TestRelinkPreservesCuratedEdge: a machine re-link (extraction re-observing a
// relationship) must not downgrade a hand-curated edge — the prior Note stays
// when the incoming one is empty, the higher-trust Source wins in both
// directions, and AsOf takes the incoming stamp (re-observation confirms).
func TestRelinkPreservesCuratedEdge(t *testing.T) {
	db := memDB(t)
	ns := "agent:x"
	old := time.Now().Add(-30 * 24 * time.Hour)
	LinkGraphEdgeP(db, ns, "person:robin", "works_at", "org:acme", "confirmed by user", false,
		MemoryProvenance{Source: MemSourceUserStated, AsOf: old})

	// Machine pass re-observes the same triple with no note.
	now := time.Now()
	LinkGraphEdgeP(db, ns, "person:robin", "works_at", "org:acme", "", false,
		MemoryProvenance{Source: MemSourceObserved, AsOf: now})

	edges := GraphEdgesFrom(db, ns, "person:robin")
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	e := edges[0]
	if e.Note != "confirmed by user" {
		t.Fatalf("re-link clobbered the curated note: %q", e.Note)
	}
	if e.Source != MemSourceUserStated {
		t.Fatalf("re-link downgraded the curated source: %d", e.Source)
	}
	if !e.AsOf.Equal(now) {
		t.Fatalf("re-observation should bump AsOf, got %v", e.AsOf)
	}

	// And the upgrade direction: a user confirming an observed edge wins.
	LinkGraphEdgeP(db, ns, "person:sam", "lives_in", "place:denver", "", false,
		MemoryProvenance{Source: MemSourceObserved, AsOf: old})
	LinkGraphEdgeP(db, ns, "person:sam", "lives_in", "place:denver", "user confirmed", false,
		MemoryProvenance{Source: MemSourceUserStated, AsOf: now})
	e = GraphEdgesFrom(db, ns, "person:sam")[0]
	if e.Source != MemSourceUserStated || e.Note != "user confirmed" {
		t.Fatalf("user confirmation should upgrade the edge, got source=%d note=%q", e.Source, e.Note)
	}
}
