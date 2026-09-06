package core

import (
	"context"
	"errors"
	"reflect"
	"strings"
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

// fakeChat builds a FactChatFunc that returns a canned reply (or error),
// so the deterministic half of supersession can be tested without a live
// model. The embedding band + real judge are exercised by the manual smoke
// test, not here.
func fakeChat(reply string, err error) FactChatFunc {
	return func(ctx context.Context, msgs []Message, opts ...ChatOption) (*Response, error) {
		if err != nil {
			return nil, err
		}
		return &Response{Content: reply}, nil
	}
}

// TestJudgeSupersedes pins the parse-and-select logic and every fail-safe
// path: a bad reply must yield NO supersession (the new fact is just added
// alongside), never a wrong one.
func TestJudgeSupersedes(t *testing.T) {
	cands := []MemoryFact{
		{ID: "a", Namespace: "ns", Note: "lives in Denver"},
		{ID: "b", Namespace: "ns", Note: "likes coffee"},
	}
	cases := []struct {
		name string
		chat FactChatFunc
		want []string // expected superseded IDs
	}{
		{"replace first", fakeChat("[1]", nil), []string{"a"}},
		{"replace both", fakeChat("[1,2]", nil), []string{"a", "b"}},
		{"replace none", fakeChat("[]", nil), nil},
		{"index out of range ignored", fakeChat("[5]", nil), nil},
		{"mixed valid + out of range", fakeChat("[2,9]", nil), []string{"b"}},
		{"malformed reply -> none", fakeChat("none of them apply", nil), nil},
		{"chat error -> none", fakeChat("", errors.New("boom")), nil},
		{"nil chat -> none", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := judgeSupersedes(tc.chat, "moved to Austin", cands)
			var gotIDs []string
			for _, f := range got {
				gotIDs = append(gotIDs, f.ID)
			}
			if !reflect.DeepEqual(gotIDs, tc.want) {
				t.Fatalf("got %v, want %v", gotIDs, tc.want)
			}
		})
	}
}

// TestListMemoryFactsFiltersSuperseded: a row marked SupersededAt is hidden
// from the central list (which feeds render, recall, dedup, and forget).
func TestListMemoryFactsFiltersSuperseded(t *testing.T) {
	db := memDB(t)
	ns := "agent:x"
	db.Set(MemoryFactsTable, factDBKey(ns, "a"), MemoryFact{
		Namespace: ns, ID: "a", Note: "active fact", Created: time.Now(),
	})
	db.Set(MemoryFactsTable, factDBKey(ns, "b"), MemoryFact{
		Namespace: ns, ID: "b", Note: "old fact", Created: time.Now().Add(-time.Hour),
		MemoryProvenance: MemoryProvenance{Reason: RetireSuperseded, RetiredAt: time.Now(), Successor: "a"},
	})
	got := ListMemoryFacts(db, ns)
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("expected only the active fact, got %+v", got)
	}
}

// TestSourcePopulation: the write policy's Source lands on the stored fact.
func TestSourcePopulation(t *testing.T) {
	db := memDB(t)
	ns := "agent:x"
	res := StoreMemoryFactP(db, ns, "the monthly budget is $800", FactWritePolicy{Source: MemSourceUserStated})
	if res.Reason != FactStored {
		t.Fatalf("expected FactStored, got %d", res.Reason)
	}
	if res.Fact.Source != MemSourceUserStated {
		t.Fatalf("Source not persisted: got %d", res.Fact.Source)
	}
	if got := ListMemoryFacts(db, ns); len(got) != 1 || got[0].Source != MemSourceUserStated {
		t.Fatalf("Source not readable back: %+v", got)
	}
}

// TestSourcedFactCorpus: only user_stated and retrieved facts are grounding
// sources; observed and inferred (ambiguous, possibly stale priors) are excluded.
func TestSourcedFactCorpus(t *testing.T) {
	facts := []MemoryFact{
		{Note: "budget is $800", MemoryProvenance: MemoryProvenance{Source: MemSourceUserStated}},
		{Note: "the 5090 costs $1600", MemoryProvenance: MemoryProvenance{Source: MemSourceObserved}},
		{Note: "API rate limit is 500/min", MemoryProvenance: MemoryProvenance{Source: MemSourceRetrieved}},
		{Note: "probably ships in Q3", MemoryProvenance: MemoryProvenance{Source: MemSourceInferred}},
	}
	corpus := SourcedFactCorpus(facts)
	if !strings.Contains(corpus, "budget is $800") || !strings.Contains(corpus, "500/min") {
		t.Errorf("user_stated + retrieved facts should be included: %q", corpus)
	}
	if strings.Contains(corpus, "1600") || strings.Contains(corpus, "Q3") {
		t.Errorf("observed/inferred facts must be excluded from grounds: %q", corpus)
	}
}

// TestClassifyVolatility: notes are classified by SUBJECT, not confidence, and
// default to stable unless a clear volatile/slow signal is present.
func TestClassifyVolatility(t *testing.T) {
	cases := []struct {
		note string
		want Volatility
	}{
		{"RTX 5090 price is $1999", VolVolatile},
		{"the latest version is 2.3.1", VolVolatile},
		{"team is currently ranked 4th", VolVolatile},
		{"the 5090 costs $1,600", VolVolatile},
		{"user works at Acme on the platform team", VolSlow},
		{"user lives in Denver", VolSlow},
		{"the CEO is Jane Roe", VolSlow},
		{"user prefers metric units", VolStable},
		{"water boils at 100C at sea level", VolStable},
		{"the project is named Atlas", VolStable},
		// A $-sign or "currently" inside a PREFERENCE is not a price quote /
		// live status — the bare tokens used to flag these volatile.
		{"user prefers products under $50", VolStable},
		{"user currently prefers dark mode for the dashboard", VolStable},
	}
	for _, c := range cases {
		if got := classifyVolatility(c.note); got != c.want {
			t.Errorf("classifyVolatility(%q) = %d, want %d", c.note, got, c.want)
		}
	}
}

// TestStaleness: a volatile fact ages past its half-life and goes stale past 2x;
// a stable fact never ages regardless of age.
func TestStaleness(t *testing.T) {
	db := memDB(t)
	db.Set(WebTable, TunableStaleVolatileDays, float64(3))
	SetTunablesDB(db)
	defer SetTunablesDB(nil)
	now := time.Now()
	mk := func(vol Volatility, ageDays int) MemoryProvenance {
		return MemoryProvenance{Volatility: vol, AsOf: now.AddDate(0, 0, -ageDays)}
	}
	if s := mk(VolVolatile, 1).Staleness(now); s != Fresh {
		t.Errorf("1-day volatile should be Fresh, got %d", s)
	}
	if s := mk(VolVolatile, 4).Staleness(now); s != Aging {
		t.Errorf("4-day volatile (half-life 3) should be Aging, got %d", s)
	}
	if s := mk(VolVolatile, 7).Staleness(now); s != Stale {
		t.Errorf("7-day volatile (>= 2x3) should be Stale, got %d", s)
	}
	if s := mk(VolStable, 999).Staleness(now); s != Fresh {
		t.Errorf("stable fact never ages, got %d", s)
	}
}

// TestFactProvenanceMarker + render: a volatile fact carries a STABLE absolute
// as-of marker in the always-in-prompt block; stable and legacy (no AsOf) facts
// render clean.
func TestFactProvenanceMarker(t *testing.T) {
	asof := time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC)
	vol := MemoryFact{Note: "price is $X", MemoryProvenance: MemoryProvenance{Volatility: VolVolatile, AsOf: asof}}
	if m := factProvenanceMarker(vol); !strings.Contains(m, "volatile, as of 2026-07-07") {
		t.Errorf("volatile marker missing absolute date: %q", m)
	}
	slow := MemoryFact{Note: "works at Acme", MemoryProvenance: MemoryProvenance{Volatility: VolSlow, AsOf: asof}}
	if m := factProvenanceMarker(slow); !strings.Contains(m, "may change, as of 2026-07-07") {
		t.Errorf("slow marker wrong: %q", m)
	}
	stable := MemoryFact{Note: "prefers metric", MemoryProvenance: MemoryProvenance{Volatility: VolStable, AsOf: asof}}
	if m := factProvenanceMarker(stable); m != "" {
		t.Errorf("stable fact should have no marker, got %q", m)
	}
	legacy := MemoryFact{Note: "price is $X", MemoryProvenance: MemoryProvenance{Volatility: VolVolatile}} // AsOf zero
	if m := factProvenanceMarker(legacy); m != "" {
		t.Errorf("legacy fact without AsOf should render clean, got %q", m)
	}
	// End-to-end through the render block.
	block := RenderMemoryFactsBlockWith([]MemoryFact{vol}, "", "")
	if !strings.Contains(block, "volatile, as of 2026-07-07") {
		t.Errorf("render block missing volatile marker:\n%s", block)
	}
}

// TestMigrateSupersededTombstones is the safety-critical path: a row written
// under the OLD supersession shape ({superseded_at, superseded_by}) must NOT
// resurrect into the live set after the field was removed from MemoryFact. Before
// migration it would (the dropped field decodes as Reason==RetireLive); the
// migration must convert it to a retirement tombstone so it stays hidden.
func TestMigrateSupersededTombstones(t *testing.T) {
	db := memDB(t)
	ns := "agent:x"
	// A legacy-shaped superseded row. kvlite uses gob, which keys on Go field
	// NAMES (json tags are ignored), so these names reproduce exactly what the old
	// MemoryFact wrote before SupersededAt/By moved into the envelope.
	type legacyFact struct {
		Namespace    string
		ID           string
		Note         string
		Created      time.Time
		SupersededAt time.Time
		SupersededBy string
	}
	db.Set(MemoryFactsTable, factDBKey(ns, "old"), legacyFact{
		Namespace: ns, ID: "old", Note: "lives in Denver",
		Created: time.Now().Add(-time.Hour), SupersededAt: time.Now(), SupersededBy: "new",
	})
	// Proves the resurrection risk is real: read as the new shape, the legacy row
	// looks live because superseded_at no longer maps to a field.
	if got := ListMemoryFacts(db, ns); len(got) != 1 {
		t.Fatalf("precondition: legacy superseded row should decode as live pre-migration, got %d", len(got))
	}
	migrateSupersededTombstones(db)
	if got := ListMemoryFacts(db, ns); len(got) != 0 {
		t.Fatalf("after migration the row must be filtered from live, got %+v", got)
	}
	retired := ListRetiredFacts(db, ns)
	if len(retired) != 1 || retired[0].Reason != RetireSuperseded || retired[0].Successor != "new" {
		t.Fatalf("expected one RetireSuperseded tombstone with successor 'new', got %+v", retired)
	}
	// Idempotent: a second pass converts nothing.
	migrateSupersededTombstones(db)
	if got := ListRetiredFacts(db, ns); len(got) != 1 {
		t.Fatalf("migration must be idempotent, got %d retired", len(got))
	}
}

// TestHardCapEvictsToTombstone: the hard cap must TOMBSTONE evicted facts, not
// hard-delete them — an evicted real fact was crowded out for space, so recall
// needs a record of it. Live count drops to the cap; the evicted ones become
// RetireEvicted tombstones with no successor.
func TestHardCapEvictsToTombstone(t *testing.T) {
	db := memDB(t)
	ns := "agent:x"
	db.Set(WebTable, TunableFactHardCap, float64(2))
	SetTunablesDB(db)
	defer SetTunablesDB(nil)
	base := time.Now().Add(-10 * time.Hour)
	for i := 0; i < 5; i++ {
		id := string(rune('a' + i))
		ts := base.Add(time.Duration(i) * time.Hour) // a<b<c<d<e by recency
		db.Set(MemoryFactsTable, factDBKey(ns, id), MemoryFact{
			Namespace: ns, ID: id, Note: "fact " + id, Created: ts, Updated: ts,
		})
	}
	enforceFactHardCap(db, ns)
	live := ListMemoryFacts(db, ns)
	if len(live) != 2 {
		t.Fatalf("expected 2 live facts at the cap, got %d: %+v", len(live), live)
	}
	retired := ListRetiredFacts(db, ns)
	if len(retired) != 3 {
		t.Fatalf("expected 3 evicted tombstones, got %d", len(retired))
	}
	for _, f := range retired {
		if f.Reason != RetireEvicted || f.Successor != "" {
			t.Fatalf("evicted fact should be RetireEvicted with no successor, got %+v", f)
		}
	}
	// The 3 oldest (a,b,c) were evicted; the 2 newest (d,e) survive.
	for _, f := range live {
		if f.ID == "a" || f.ID == "b" || f.ID == "c" {
			t.Fatalf("LRU eviction should keep the newest, but %q survived", f.ID)
		}
	}
}

// TestTombstoneRetentionPrunes: retired facts older than the retention window are
// permanently deleted so tombstones can't defeat the live cap; fresh ones stay. A
// window of 0 deletes every retired fact.
func TestTombstoneRetentionPrunes(t *testing.T) {
	db := memDB(t)
	ns := "agent:x"
	stale := MemoryFact{Namespace: ns, ID: "stale", Note: "old",
		MemoryProvenance: MemoryProvenance{Reason: RetireEvicted, RetiredAt: time.Now().AddDate(0, 0, -40)}}
	fresh := MemoryFact{Namespace: ns, ID: "fresh", Note: "recent",
		MemoryProvenance: MemoryProvenance{Reason: RetireEvicted, RetiredAt: time.Now()}}
	live := MemoryFact{Namespace: ns, ID: "live", Note: "current", Created: time.Now()}
	for _, f := range []MemoryFact{stale, fresh, live} {
		db.Set(MemoryFactsTable, factDBKey(ns, f.ID), f)
	}
	// 30-day window: the 40-day-old tombstone goes, the fresh one and the live fact stay.
	db.Set(WebTable, TunableFactTombstoneDays, float64(30))
	SetTunablesDB(db)
	defer SetTunablesDB(nil)
	enforceTombstoneRetention(db, ns)
	if _, ok := GetMemoryFactByID(db, ns, "stale"); ok {
		t.Fatal("40-day-old tombstone should have been pruned at a 30-day window")
	}
	if _, ok := GetMemoryFactByID(db, ns, "fresh"); !ok {
		t.Fatal("fresh tombstone must survive a 30-day window")
	}
	if _, ok := GetMemoryFactByID(db, ns, "live"); !ok {
		t.Fatal("retention must never touch live facts")
	}
	// Window 0: every remaining tombstone is deleted, live untouched.
	db.Set(WebTable, TunableFactTombstoneDays, float64(0))
	InvalidateTunables()
	enforceTombstoneRetention(db, ns)
	if len(ListRetiredFacts(db, ns)) != 0 {
		t.Fatal("a 0-day window must delete all tombstones")
	}
	if _, ok := GetMemoryFactByID(db, ns, "live"); !ok {
		t.Fatal("live fact must survive a 0-day tombstone window")
	}
}

// TestForgetByIndexSkipsSuperseded confirms the index the LLM sees (built
// from the active-only list) lines up with what forget removes — a
// superseded row must not shift the indices.
func TestForgetByIndexSkipsSuperseded(t *testing.T) {
	db := memDB(t)
	ns := "agent:x"
	db.Set(MemoryFactsTable, factDBKey(ns, "old"), MemoryFact{
		Namespace: ns, ID: "old", Note: "stale", Created: time.Now().Add(-2 * time.Hour),
		MemoryProvenance: MemoryProvenance{Reason: RetireSuperseded, RetiredAt: time.Now()},
	})
	db.Set(MemoryFactsTable, factDBKey(ns, "keep"), MemoryFact{
		Namespace: ns, ID: "keep", Note: "current", Created: time.Now(),
	})
	// Active list is just ["current"] at index 1. Forgetting 1 must remove
	// "keep", not the hidden superseded "old".
	removed, ok := ForgetMemoryFactByIndex(db, ns, 1)
	if !ok || removed.ID != "keep" {
		t.Fatalf("expected to remove 'keep', got ok=%v id=%q", ok, removed.ID)
	}
}

// TestForgetByIndexQuoted: the quote-verified forget must delete on an
// index+quote agreement, self-heal to a unique quote match when the list has
// shifted under the caller, and refuse (deleting nothing) when the quote is
// ambiguous or matches nothing.
func TestForgetByIndexQuoted(t *testing.T) {
	db := memDB(t)
	ns := "agent:x"
	seed := func(id, note string, age time.Duration) {
		db.Set(MemoryFactsTable, factDBKey(ns, id), MemoryFact{
			Namespace: ns, ID: id, Note: note, Created: time.Now().Add(-age),
		})
	}
	seed("a", "user prefers metric units", 3*time.Hour)
	seed("b", "production API needs JWT header", 2*time.Hour)
	seed("c", "user's dog is named Rex", 1*time.Hour)

	// Index and quote agree → that note goes.
	removed, reason, ok := ForgetMemoryFactByIndexQuoted(db, ns, 2, "JWT header")
	if !ok || removed.ID != "b" {
		t.Fatalf("agreeing index+quote should delete b: ok=%v id=%q reason=%q", ok, removed.ID, reason)
	}

	// The list shifted (b is gone; c is now index 2). A caller holding the
	// STALE index 3 with c's quote must still delete c, not error or misfire.
	removed, reason, ok = ForgetMemoryFactByIndexQuoted(db, ns, 3, "dog is named Rex")
	if !ok || removed.ID != "c" {
		t.Fatalf("unique quote should self-heal a stale index: ok=%v id=%q reason=%q", ok, removed.ID, reason)
	}

	// Quote matching nothing → refuse, delete nothing.
	if _, reason, ok = ForgetMemoryFactByIndexQuoted(db, ns, 1, "no such text"); ok || reason == "" {
		t.Fatalf("unmatched quote must refuse with a reason, got ok=%v reason=%q", ok, reason)
	}
	if got := ListMemoryFacts(db, ns); len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("refusals must not delete: %+v", got)
	}

	// Ambiguous quote across multiple notes → refuse.
	seed("d", "user prefers metric units for temperature too", time.Minute)
	if _, reason, ok = ForgetMemoryFactByIndexQuoted(db, ns, 5, "prefers metric"); ok || reason == "" {
		t.Fatalf("ambiguous quote must refuse, got ok=%v reason=%q", ok, reason)
	}
	if got := ListMemoryFacts(db, ns); len(got) != 2 {
		t.Fatalf("ambiguous refusal must not delete: %+v", got)
	}
}

// TestStoreMemoryFactNoChatStillDedups: the variadic chat arg is optional —
// callers that pass none keep the prior dedup-only behavior. Tier-1
// normalized match needs no embeddings, so this runs offline.
func TestStoreMemoryFactNoChatStillDedups(t *testing.T) {
	db := memDB(t)
	ns := "agent:x"
	if _, isNew, _ := StoreMemoryFact(db, ns, "User prefers metric units"); !isNew {
		t.Fatal("first store should report new")
	}
	if _, isNew, _ := StoreMemoryFact(db, ns, "  user prefers metric units. "); isNew {
		t.Fatal("normalized duplicate should not report new")
	}
	if got := ListMemoryFacts(db, ns); len(got) != 1 {
		t.Fatalf("expected 1 fact after dedup, got %d", len(got))
	}
}

func TestStoreMemoryFactStampsUpdated(t *testing.T) {
	db := memDB(t)
	ns := "agent:x"
	f, isNew, _ := StoreMemoryFact(db, ns, "Time zone is America/Los_Angeles")
	if !isNew {
		t.Fatal("first store should report new")
	}
	if f.Updated.IsZero() {
		t.Error("Updated should be stamped on a fresh fact")
	}
	if !f.Updated.Equal(f.Created) {
		t.Errorf("on a fresh fact Updated should equal Created: created=%v updated=%v", f.Created, f.Updated)
	}
	// The stamp round-trips through storage.
	got := ListMemoryFacts(db, ns)
	if len(got) != 1 || got[0].Updated.IsZero() {
		t.Fatalf("stored fact should carry Updated: %+v", got)
	}
}
