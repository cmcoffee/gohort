package core

import (
	"sync"
	"testing"
	"time"
)

// TestSweepMergeDedupBackfillsSuccessor: when a sweep-merge's combined text
// DEDUPES against an existing live fact (instead of storing fresh), the merged
// sources' tombstones must still get a Successor pointer — to the live fact
// they were absorbed into — or recall can never follow the merge.
func TestSweepMergeDedupBackfillsSuccessor(t *testing.T) {
	db := memDB(t)
	ns := "agent:x"
	now := time.Now()
	seed := func(id, note string, age time.Duration) {
		db.Set(MemoryFactsTable, factDBKey(ns, id), MemoryFact{
			Namespace: ns, ID: id, Note: note, Created: now.Add(-age), Updated: now.Add(-age),
		})
	}
	seed("a", "user likes espresso", 3*time.Hour)
	seed("b", "user enjoys espresso drinks", 2*time.Hour)
	seed("c", "user drinks espresso daily", time.Hour)

	// Judge merges #1+#2 into text that tier-1 dedupes against live fact "c".
	sweepFacts(db, ns, fakeChat(`{"drop": [], "merge": [{"keep": "user drinks espresso daily", "replace": [1, 2]}]}`, nil))

	live := ListMemoryFacts(db, ns)
	if len(live) != 1 || live[0].ID != "c" {
		t.Fatalf("expected only fact c live, got %+v", live)
	}
	for _, id := range []string{"a", "b"} {
		f, ok := GetMemoryFactByID(db, ns, id)
		if !ok || f.Reason != RetireMerged {
			t.Fatalf("expected %s retired as merged, got %+v (ok=%v)", id, f, ok)
		}
		if f.Successor != "c" {
			t.Fatalf("expected %s Successor=c after dedup backfill, got %q", id, f.Successor)
		}
	}
}

// TestWipeMemoryFactNamespace: the scope-teardown wipe removes live rows AND
// tombstones for exactly the given namespace — no prefix bleed into a sibling
// namespace that happens to share leading characters.
func TestWipeMemoryFactNamespace(t *testing.T) {
	db := memDB(t)
	now := time.Now()
	db.Set(MemoryFactsTable, factDBKey("agent:x", "live"), MemoryFact{
		Namespace: "agent:x", ID: "live", Note: "live note", Created: now,
	})
	db.Set(MemoryFactsTable, factDBKey("agent:x", "dead"), MemoryFact{
		Namespace: "agent:x", ID: "dead", Note: "old note", Created: now,
		MemoryProvenance: MemoryProvenance{Reason: RetireSuperseded, RetiredAt: now, Successor: "live"},
	})
	db.Set(MemoryFactsTable, factDBKey("agent:xy", "other"), MemoryFact{
		Namespace: "agent:xy", ID: "other", Note: "sibling agent note", Created: now,
	})

	if n := WipeMemoryFactNamespace(db, "agent:x"); n != 2 {
		t.Fatalf("expected 2 rows wiped, got %d", n)
	}
	if got := ListMemoryFacts(db, "agent:x"); len(got) != 0 {
		t.Fatalf("live rows survived the wipe: %+v", got)
	}
	if got := ListRetiredFacts(db, "agent:x"); len(got) != 0 {
		t.Fatalf("tombstones survived the wipe: %+v", got)
	}
	if got := ListMemoryFacts(db, "agent:xy"); len(got) != 1 {
		t.Fatalf("sibling namespace was damaged: %+v", got)
	}
}

// TestWipeGraphNamespace: entities and edges under the namespace go; a
// sibling namespace is untouched.
func TestWipeGraphNamespace(t *testing.T) {
	db := memDB(t)
	UpsertGraphEntity(db, "agent:x", "person", "Robin", nil, nil)
	UpsertGraphEntity(db, "agent:x", "org", "Acme", nil, nil)
	LinkGraphEdge(db, "agent:x", "person:robin", "works_at", "org:acme", "", false)
	UpsertGraphEntity(db, "agent:y", "person", "Sam", nil, nil)

	ents, edges := WipeGraphNamespace(db, "agent:x")
	if ents != 2 || edges != 1 {
		t.Fatalf("expected 2 entities + 1 edge wiped, got %d + %d", ents, edges)
	}
	if e, ed := GraphCounts(db, "agent:x"); e != 0 || ed != 0 {
		t.Fatalf("graph survived the wipe: %d entities, %d edges", e, ed)
	}
	if e, _ := GraphCounts(db, "agent:y"); e != 1 {
		t.Fatalf("sibling namespace was damaged")
	}
}

// TestSearchRetiredFactsTermOverlap pins the embeddings-off tier: a natural-
// language question must find a retired note that shares its significant
// terms — the old full-query-substring test matched essentially never.
func TestSearchRetiredFactsTermOverlap(t *testing.T) {
	db := memDB(t)
	ns := "agent:x"
	now := time.Now()
	db.Set(MemoryFactsTable, factDBKey(ns, "job"), MemoryFact{
		Namespace: ns, ID: "job", Note: "works at Acme Corp", Created: now,
		MemoryProvenance: MemoryProvenance{Reason: RetireSuperseded, RetiredAt: now},
	})

	// "where does the user work" → significant terms {user, work}; "works at
	// Acme Corp" contains "work" — half the terms, so it matches.
	if got := SearchRetiredFacts(db, ns, "where does the user work", 3); len(got) != 1 {
		t.Fatalf("multi-word question found %d retired facts, want 1", len(got))
	}
	// Single distinctive term still matches by containment.
	if got := SearchRetiredFacts(db, ns, "acme", 3); len(got) != 1 {
		t.Fatalf("single-term query found %d retired facts, want 1", len(got))
	}
	// Unrelated query stays quiet.
	if got := SearchRetiredFacts(db, ns, "favorite color preference", 3); len(got) != 0 {
		t.Fatalf("unrelated query found %d retired facts, want 0", len(got))
	}
	// Stopword-only query can't match everything by accident.
	if got := SearchRetiredFacts(db, ns, "what is it about", 3); len(got) != 0 {
		t.Fatalf("stopword-only query found %d retired facts, want 0", len(got))
	}
}

// TestDuplicateSaveReconfirmsAsOf: a duplicate save from a live source is a
// re-verification — AsOf bumps (so a daily-re-confirmed volatile fact stops
// rendering STALE) while Updated stays put (it orders LRU eviction and tracks
// meaning changes). Anonymous writers (Source unknown — sweep merges,
// migrations) confirm nothing.
func TestDuplicateSaveReconfirmsAsOf(t *testing.T) {
	db := memDB(t)
	ns := "agent:x"
	old := time.Now().Add(-30 * 24 * time.Hour)
	db.Set(MemoryFactsTable, factDBKey(ns, "gpu"), MemoryFact{
		Namespace: ns, ID: "gpu", Note: "the 5090 costs $1,600", Created: old, Updated: old,
		MemoryProvenance: MemoryProvenance{AsOf: old, Volatility: VolVolatile, Source: MemSourceObserved},
	})

	// Sourced duplicate → AsOf bumps, Updated untouched.
	res := StoreMemoryFactP(db, ns, "The 5090 costs $1,600", FactWritePolicy{Source: MemSourceObserved})
	if res.Reason != FactDuplicate {
		t.Fatalf("expected FactDuplicate, got %d", res.Reason)
	}
	got, _ := GetMemoryFactByID(db, ns, "gpu")
	if time.Since(got.AsOf) > time.Minute {
		t.Fatalf("AsOf not bumped by sourced duplicate: %v", got.AsOf)
	}
	if !got.Updated.Equal(old) {
		t.Fatalf("Updated must not change on re-confirmation: %v", got.Updated)
	}
	if got.Staleness(time.Now()) != Fresh {
		t.Fatalf("re-confirmed volatile fact should read Fresh, got %v", got.Staleness(time.Now()))
	}

	// Anonymous duplicate (zero policy — the sweep-merge shape) → no bump.
	db.Set(MemoryFactsTable, factDBKey(ns, "gpu"), MemoryFact{
		Namespace: ns, ID: "gpu", Note: "the 5090 costs $1,600", Created: old, Updated: old,
		MemoryProvenance: MemoryProvenance{AsOf: old, Volatility: VolVolatile, Source: MemSourceObserved},
	})
	if res := StoreMemoryFactP(db, ns, "the 5090 costs $1,600", FactWritePolicy{}); res.Reason != FactDuplicate {
		t.Fatalf("expected FactDuplicate, got %d", res.Reason)
	}
	got, _ = GetMemoryFactByID(db, ns, "gpu")
	if !got.AsOf.Equal(old) {
		t.Fatalf("anonymous duplicate must not bump AsOf: %v", got.AsOf)
	}
}

// TestSweepMergeCarriesProvenance: the merged note is a rewording of its
// sources, so it inherits their envelope — max-trust Source (a user_stated
// component keeps its grounding license), EARLIEST AsOf (only as current as
// the least-recently-confirmed component), max Volatility — instead of the
// empty policy's Source=unknown / AsOf=now / re-classified volatility.
func TestSweepMergeCarriesProvenance(t *testing.T) {
	db := memDB(t)
	ns := "agent:x"
	now := time.Now()
	oldest := now.Add(-40 * 24 * time.Hour)
	db.Set(MemoryFactsTable, factDBKey(ns, "a"), MemoryFact{
		Namespace: ns, ID: "a", Note: "budget is $800 per month", Created: now.Add(-3 * time.Hour), Updated: now.Add(-3 * time.Hour),
		MemoryProvenance: MemoryProvenance{Source: MemSourceUserStated, AsOf: oldest, Volatility: VolSlow},
	})
	db.Set(MemoryFactsTable, factDBKey(ns, "b"), MemoryFact{
		Namespace: ns, ID: "b", Note: "monthly spend cap is eight hundred", Created: now.Add(-2 * time.Hour), Updated: now.Add(-2 * time.Hour),
		MemoryProvenance: MemoryProvenance{Source: MemSourceObserved, AsOf: now.Add(-10 * 24 * time.Hour), Volatility: VolVolatile},
	})

	sweepFacts(db, ns, fakeChat(`{"drop": [], "merge": [{"keep": "user's monthly budget cap is $800", "replace": [1, 2]}]}`, nil))

	live := ListMemoryFacts(db, ns)
	if len(live) != 1 {
		t.Fatalf("expected 1 merged fact, got %d", len(live))
	}
	m := live[0]
	if m.Source != MemSourceUserStated {
		t.Fatalf("merge laundered Source: got %d, want user_stated", m.Source)
	}
	if !m.AsOf.Equal(oldest) {
		t.Fatalf("merge should carry the EARLIEST source AsOf, got %v", m.AsOf)
	}
	if m.Volatility != VolVolatile {
		t.Fatalf("merge should carry max volatility, got %d", m.Volatility)
	}
}

// TestPruneExpiredFactTombstones: the startup pass removes expired tombstones
// across every namespace in one walk; fresh tombstones and live rows stay.
func TestPruneExpiredFactTombstones(t *testing.T) {
	db := memDB(t)
	now := time.Now()
	for _, ns := range []string{"agent:x", "agent:y"} {
		db.Set(MemoryFactsTable, factDBKey(ns, "stale"), MemoryFact{Namespace: ns, ID: "stale", Note: "old",
			MemoryProvenance: MemoryProvenance{Reason: RetireEvicted, RetiredAt: now.AddDate(0, 0, -40)}})
		db.Set(MemoryFactsTable, factDBKey(ns, "fresh"), MemoryFact{Namespace: ns, ID: "fresh", Note: "recent",
			MemoryProvenance: MemoryProvenance{Reason: RetireEvicted, RetiredAt: now}})
		db.Set(MemoryFactsTable, factDBKey(ns, "live"), MemoryFact{Namespace: ns, ID: "live", Note: "current", Created: now})
	}
	if pruned := PruneExpiredFactTombstones(db); pruned != 2 {
		t.Fatalf("expected 2 expired tombstones pruned (one per namespace), got %d", pruned)
	}
	for _, ns := range []string{"agent:x", "agent:y"} {
		if got := ListRetiredFacts(db, ns); len(got) != 1 || got[0].ID != "fresh" {
			t.Fatalf("%s: expected only the fresh tombstone to survive, got %+v", ns, got)
		}
		if got := ListMemoryFacts(db, ns); len(got) != 1 {
			t.Fatalf("%s: live row must survive the prune", ns)
		}
	}
}

// TestConcurrentSameSaveWritesOnce: the per-namespace save lock closes the
// read-check-write race — N concurrent saves of the same note (a tool call and
// an async fold landing together) must produce exactly one live fact.
func TestConcurrentSameSaveWritesOnce(t *testing.T) {
	db := memDB(t)
	ns := "agent:x"
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			StoreMemoryFactP(db, ns, "user prefers metric units", FactWritePolicy{Source: MemSourceObserved})
		}()
	}
	wg.Wait()
	if got := ListMemoryFacts(db, ns); len(got) != 1 {
		t.Fatalf("concurrent duplicate saves wrote %d facts, want 1", len(got))
	}
}

// TestSearchMemoryFactsVecUsesCallerVector: a caller-supplied query embedding
// drives the semantic ranking with NO embed call of its own — the single-
// embed-per-recall contract.
func TestSearchMemoryFactsVecUsesCallerVector(t *testing.T) {
	db := memDB(t)
	ns := "agent:x"
	now := time.Now()
	db.Set(MemoryFactsTable, factDBKey(ns, "hit"), MemoryFact{
		Namespace: ns, ID: "hit", Note: "deploy header is X-Auth", Created: now, Vector: []float32{1, 0},
	})
	db.Set(MemoryFactsTable, factDBKey(ns, "miss"), MemoryFact{
		Namespace: ns, ID: "miss", Note: "favorite tea is oolong", Created: now, Vector: []float32{0, 1},
	})
	// Enabled config with an unreachable endpoint: if the function tried to
	// embed for itself it would find nothing (Embed fails), so a semantic hit
	// proves the caller's vector was used.
	old := GetEmbeddingConfig()
	SetEmbeddingConfig(EmbeddingConfig{Enabled: true, Endpoint: "http://127.0.0.1:1"})
	defer SetEmbeddingConfig(old)

	got := SearchMemoryFactsVec(db, ns, "what's the deploy header?", []float32{1, 0})
	if len(got) != 1 || got[0].ID != "hit" {
		t.Fatalf("expected the vector-matched fact only, got %+v", got)
	}
}
