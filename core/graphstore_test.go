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

// TestUpsertGraphEntityMerge: a second mention under a different alias lands
// on the SAME node (alias-based consolidation), folding aliases + attrs in.
func TestUpsertGraphEntityMerge(t *testing.T) {
	db := memDB(t)
	ns := "agent:1"

	e1, new1 := UpsertGraphEntity(db, ns, "person", "Robin", nil, map[string]string{"title": "VP"})
	if !new1 || e1.ID == "" {
		t.Fatalf("first upsert should create: new=%v id=%q", new1, e1.ID)
	}
	// Same person, different surface form + an alias + a new attr.
	e2, new2 := UpsertGraphEntity(db, ns, "person", "Robin", []string{"Robin Vale"}, map[string]string{"email": "robin@acme.com"})
	if new2 {
		t.Fatalf("second upsert should merge, not create new")
	}
	if e2.ID != e1.ID {
		t.Fatalf("merge landed on a different node: %q vs %q", e2.ID, e1.ID)
	}
	if e2.Attrs["title"] != "VP" || e2.Attrs["email"] != "robin@acme.com" {
		t.Fatalf("attrs not merged: %+v", e2.Attrs)
	}
	// Now look up by the alias — must resolve to the same node.
	got, ok := FindGraphEntity(db, ns, "robin vale")
	if !ok || got.ID != e1.ID {
		t.Fatalf("alias lookup failed: ok=%v id=%q", ok, got.ID)
	}
	if ents, _ := GraphCounts(db, ns); ents != 1 {
		t.Fatalf("expected 1 entity after merge, got %d", ents)
	}
}

// TestLinkGraphEdgeReplace: replace=true is delete-on-update for a single-
// valued relation; replace=false lets siblings coexist (multi-valued).
func TestLinkGraphEdgeReplace(t *testing.T) {
	db := memDB(t)
	ns := "agent:1"
	robin, _ := UpsertGraphEntity(db, ns, "person", "Robin", nil, nil)
	acme, _ := UpsertGraphEntity(db, ns, "org", "Acme", nil, nil)
	globex, _ := UpsertGraphEntity(db, ns, "org", "Globex", nil, nil)

	LinkGraphEdge(db, ns, robin.ID, "works at", acme.ID, "", false)
	// Single-valued correction — Acme should be gone.
	LinkGraphEdge(db, ns, robin.ID, "works at", globex.ID, "", true)
	out := GraphEdgesFrom(db, ns, robin.ID)
	if len(out) != 1 || out[0].To != globex.ID {
		t.Fatalf("replace should leave only Globex, got %+v", out)
	}

	// Multi-valued — two "knows" coexist.
	morgan, _ := UpsertGraphEntity(db, ns, "person", "Morgan", nil, nil)
	casey, _ := UpsertGraphEntity(db, ns, "person", "Casey", nil, nil)
	LinkGraphEdge(db, ns, robin.ID, "knows", morgan.ID, "", false)
	LinkGraphEdge(db, ns, robin.ID, "knows", casey.ID, "", false)
	knows := 0
	for _, e := range GraphEdgesFrom(db, ns, robin.ID) {
		if e.Rel == "knows" {
			knows++
		}
	}
	if knows != 2 {
		t.Fatalf("multi-valued knows should coexist, got %d", knows)
	}

	// Inbound lookup from Globex's side resolves back to Robin.
	in := GraphEdgesTo(db, ns, globex.ID)
	if len(in) != 1 || in[0].From != robin.ID {
		t.Fatalf("inbound edge lookup failed: %+v", in)
	}
}

// TestGraphEdgeSupersessionTombstone: a corrected single-valued relation keeps a
// validity-window tombstone (the past relationship) instead of hard-dropping,
// while the live view and counts show only the current value, and re-linking an
// old target revives it.
func TestGraphEdgeSupersessionTombstone(t *testing.T) {
	db := memDB(t)
	ns := "agent:1"
	robin, _ := UpsertGraphEntity(db, ns, "person", "Robin", nil, nil)
	acme, _ := UpsertGraphEntity(db, ns, "org", "Acme", nil, nil)
	globex, _ := UpsertGraphEntity(db, ns, "org", "Globex", nil, nil)

	LinkGraphEdge(db, ns, robin.ID, "works at", acme.ID, "", true)
	LinkGraphEdge(db, ns, robin.ID, "works at", globex.ID, "", true)

	live := GraphEdgesFrom(db, ns, robin.ID)
	if len(live) != 1 || live[0].To != globex.ID {
		t.Fatalf("live edges should be only Globex, got %+v", live)
	}
	past := RetiredGraphEdgesFrom(db, ns, robin.ID)
	if len(past) != 1 || past[0].To != acme.ID {
		t.Fatalf("expected one retired edge to Acme, got %+v", past)
	}
	if past[0].Reason != RetireSuperseded || past[0].Successor != globex.ID {
		t.Fatalf("retired edge should be RetireSuperseded with successor Globex, got %+v", past[0])
	}
	if _, edges := GraphCounts(db, ns); edges != 1 {
		t.Fatalf("GraphCounts should count only live edges, got %d", edges)
	}

	// Re-linking the old target revives it live; Globex becomes the retired one.
	LinkGraphEdge(db, ns, robin.ID, "works at", acme.ID, "", true)
	if live2 := GraphEdgesFrom(db, ns, robin.ID); len(live2) != 1 || live2[0].To != acme.ID {
		t.Fatalf("re-link should make Acme live again, got %+v", live2)
	}
	if p := RetiredGraphEdgesFrom(db, ns, robin.ID); len(p) != 1 || p[0].To != globex.ID {
		t.Fatalf("after re-linking Acme, Globex should be the retired one, got %+v", p)
	}
}

// TestGraphNamespaceIsolation: entities/edges in one agent's namespace are
// invisible to another's.
func TestGraphNamespaceIsolation(t *testing.T) {
	db := memDB(t)
	UpsertGraphEntity(db, "agent:1", "person", "Robin", nil, nil)
	if _, ok := FindGraphEntity(db, "agent:2", "Robin"); ok {
		t.Fatalf("entity leaked across namespaces")
	}
	if ents, _ := GraphCounts(db, "agent:2"); ents != 0 {
		t.Fatalf("namespace 2 should be empty, got %d", ents)
	}
}

// TestGraphEntityMentionedIn: the cross-layer join predicate matches on name and
// alias, case-insensitively, and skips terms under the min length to avoid
// over-matching.
func TestGraphEntityMentionedIn(t *testing.T) {
	db := memDB(t)
	ns := "agent:1"
	robin, _ := UpsertGraphEntity(db, ns, "person", "Robin Vale", []string{"RV", "the boss"}, nil)

	cases := []struct {
		text string
		want bool
	}{
		{"met with robin vale about the deploy", true}, // canonical name, lowercased
		{"ROBIN VALE approved it", true},               // haystack upper, term lower
		{"escalate to the boss first", true},           // multi-word alias
		{"rv rv rv", false},                            // "RV" is under the 3-char floor → skipped
		{"nothing relevant here", false},
		{"", false},
	}
	for _, c := range cases {
		if got := GraphEntityMentionedIn(robin, c.text); got != c.want {
			t.Errorf("GraphEntityMentionedIn(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

// TestGraphEntitiesMentionedIn: the dual scan returns which entities a blob names,
// respects the limit, and stays inside the namespace.
func TestGraphEntitiesMentionedIn(t *testing.T) {
	db := memDB(t)
	ns := "agent:1"
	UpsertGraphEntity(db, ns, "person", "Robin", nil, nil)
	UpsertGraphEntity(db, ns, "org", "Acme", nil, nil)
	UpsertGraphEntity(db, ns, "org", "Globex", nil, nil)
	// A different namespace must not leak into the scan.
	UpsertGraphEntity(db, "agent:2", "person", "Robin", nil, nil)

	text := "Robin works at Acme, not Globex."
	got := GraphEntitiesMentionedIn(db, ns, text, 0)
	if len(got) != 3 {
		t.Fatalf("expected all 3 named entities, got %d: %+v", len(got), got)
	}
	// Limit caps the result.
	if capped := GraphEntitiesMentionedIn(db, ns, text, 2); len(capped) != 2 {
		t.Fatalf("limit=2 should cap at 2, got %d", len(capped))
	}
	// Text naming nothing returns empty.
	if none := GraphEntitiesMentionedIn(db, ns, "unrelated text", 0); len(none) != 0 {
		t.Fatalf("expected no matches, got %d", len(none))
	}
}

// TestGraphRelSlug: relation verbs are slugged so the pipe-delimited key
// stays unambiguous and re-stating the same triple updates rather than dups.
func TestGraphRelSlug(t *testing.T) {
	db := memDB(t)
	ns := "agent:1"
	a, _ := UpsertGraphEntity(db, ns, "person", "A", nil, nil)
	b, _ := UpsertGraphEntity(db, ns, "person", "B", nil, nil)
	LinkGraphEdge(db, ns, a.ID, "Reports To", b.ID, "", false)
	LinkGraphEdge(db, ns, a.ID, "reports  to", b.ID, "", false) // same after slug
	out := GraphEdgesFrom(db, ns, a.ID)
	if len(out) != 1 {
		t.Fatalf("expected 1 edge after slug-collapsed restatement, got %d: %+v", len(out), out)
	}
	if out[0].Rel != "reports_to" {
		t.Fatalf("rel not slugged: %q", out[0].Rel)
	}
}
