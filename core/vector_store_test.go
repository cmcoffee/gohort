package core

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/kvlite"
)

// TestDBHandleInterning: Bucket/Sub must return pointer-identical handles for
// the same (parent, name) — that identity is what the chunk cache (and any
// future per-Database cache) keys on.
func TestDBHandleInterning(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	if root.Bucket("orchestrate") != root.Bucket("orchestrate") {
		t.Fatal("Bucket must intern: same parent+name should be the identical handle")
	}
	if root.Sub("user:alice") != root.Sub("user:alice") {
		t.Fatal("Sub must intern: same parent+name should be the identical handle")
	}
	if root.Sub("x") == root.Bucket("x") {
		t.Fatal("Sub and Bucket of the same name are different namespaces — must not share a handle")
	}
	// Chains: the interning composes level by level.
	a := root.Bucket("orchestrate").Sub("user:alice")
	b := root.Bucket("orchestrate").Sub("user:alice")
	if a != b {
		t.Fatal("chained Bucket().Sub() must resolve to the identical handle")
	}
	// Reads/writes flow through interned handles as before.
	a.Set("t", "k", "v")
	var got string
	if !b.Get("t", "k", &got) || got != "v" {
		t.Fatal("interned handles must address the same underlying namespace")
	}
}

// countingDB wraps a Database and counts Keys() calls — a rebuild of the chunk
// cache is exactly one Keys(EmbeddedChunks) walk, so the count exposes whether
// snapshots persist across interleaved reads of DIFFERENT databases.
type countingDB struct {
	Database
	keysCalls int
}

func (c *countingDB) Keys(table string) []string {
	c.keysCalls++
	return c.Database.Keys(table)
}

// TryKeys counts too, and must: the rebuild reads through the error-returning
// form so a failed listing is never cached as an empty corpus, and a counter
// that only watched Keys would report zero rebuilds however many happened.
func (c *countingDB) TryKeys(table string) ([]string, error) {
	c.keysCalls++
	return c.Database.TryKeys(table)
}

func TestChunkCache_NoThrashAcrossDatabases(t *testing.T) {
	invalidateChunkCache()
	t.Cleanup(invalidateChunkCache)
	a := &countingDB{Database: &DBase{Store: kvlite.MemStore()}}
	b := &countingDB{Database: &DBase{Store: kvlite.MemStore()}}
	a.Set(EmbeddedChunks, "1", EmbeddedChunk{ID: "1", Source: "s", Text: "alpha"})
	b.Set(EmbeddedChunks, "2", EmbeddedChunk{ID: "2", Source: "s", Text: "beta"})
	a.keysCalls, b.keysCalls = 0, 0

	// Interleave reads — the exact pattern of one recall touching VectorDB then
	// the per-user store. Each db must build ONCE, not on every alternation.
	for i := 0; i < 4; i++ {
		if got := snapshotChunks(a); len(got) != 1 || got[0].Text != "alpha" {
			t.Fatalf("db a snapshot wrong: %+v", got)
		}
		if got := snapshotChunks(b); len(got) != 1 || got[0].Text != "beta" {
			t.Fatalf("db b snapshot wrong: %+v", got)
		}
	}
	if a.keysCalls != 1 || b.keysCalls != 1 {
		t.Fatalf("interleaved reads must not thrash: got %d/%d rebuilds, want 1/1", a.keysCalls, b.keysCalls)
	}

	// Invalidation drops both snapshots; next read per db rebuilds once.
	invalidateChunkCache()
	snapshotChunks(a)
	snapshotChunks(b)
	if a.keysCalls != 2 || b.keysCalls != 2 {
		t.Fatalf("post-invalidate reads must rebuild once each: got %d/%d, want 2/2", a.keysCalls, b.keysCalls)
	}
}

func TestChunkCache_EvictsLRUAtCap(t *testing.T) {
	invalidateChunkCache()
	t.Cleanup(invalidateChunkCache)
	dbs := make([]*countingDB, chunkCacheMaxDBs+1)
	for i := range dbs {
		dbs[i] = &countingDB{Database: &DBase{Store: kvlite.MemStore()}}
		dbs[i].Set(EmbeddedChunks, "k", EmbeddedChunk{ID: fmt.Sprintf("c%d", i)})
		snapshotChunks(dbs[i])
	}
	// dbs[0] was least-recently-used and must have been evicted; re-reading it
	// rebuilds (2nd Keys call), while the most recent stays cached.
	last := dbs[len(dbs)-1]
	snapshotChunks(dbs[0])
	snapshotChunks(last)
	if dbs[0].keysCalls != 2 {
		t.Fatalf("LRU db should have been evicted and rebuilt: %d Keys calls", dbs[0].keysCalls)
	}
	if last.keysCalls != 1 {
		t.Fatalf("recently-used db must stay cached: %d Keys calls", last.keysCalls)
	}
}

// TestKeywordSearch_IDFWeighting: matching only a corpus-common term must not
// clear the 0.35 similarity floor (the tangential-leak case), while matching
// the rare identifier must; a single-term query keeps its 0.85 full score.
func TestKeywordSearch_IDFWeighting(t *testing.T) {
	invalidateChunkCache()
	t.Cleanup(invalidateChunkCache)
	db := &DBase{Store: kvlite.MemStore()}
	// "firewall" appears everywhere (common); "opnsense" in one chunk (rare).
	for i := 0; i < 9; i++ {
		db.Set(EmbeddedChunks, fmt.Sprintf("common-%d", i), EmbeddedChunk{
			ID: fmt.Sprintf("common-%d", i), Source: "s",
			Text: fmt.Sprintf("generic firewall discussion number %d", i)})
	}
	db.Set(EmbeddedChunks, "rare", EmbeddedChunk{
		ID: "rare", Source: "s", Text: "opnsense rule configuration guide"})

	allow := func(EmbeddedChunk) bool { return true }
	hits := SearchChunksKeywordByPredicate(db, allow, "opnsense firewall setup", 20)
	if len(hits) == 0 || hits[0].ID != "rare" {
		t.Fatalf("rare-identifier chunk must rank first: %+v", hits)
	}
	if hits[0].Score < 0.35 {
		t.Fatalf("rare-term match must clear the similarity floor, got %.3f", hits[0].Score)
	}
	for _, h := range hits[1:] {
		if h.Score >= 0.35 {
			t.Fatalf("common-term-only chunk must NOT clear the floor (tangential leak): %s scored %.3f", h.ID, h.Score)
		}
	}

	// Single-term identifier query: full coverage keeps the 0.85 ceiling.
	hits = SearchChunksKeywordByPredicate(db, allow, "opnsense", 5)
	if len(hits) != 1 || hits[0].ID != "rare" {
		t.Fatalf("single-term query: %+v", hits)
	}
	if hits[0].Score < 0.84 || hits[0].Score > 0.86 {
		t.Fatalf("full single-term coverage should score ~0.85, got %.3f", hits[0].Score)
	}
}

// The first recall after a restart paid for the whole corpus while somebody
// waited — and paid it over the recall budget, so the hints were discarded.
// Warming means the snapshot is already there when the first query arrives.
func TestWarmChunkCacheFillsItBeforeTheFirstQuery(t *testing.T) {
	invalidateChunkCache()
	db := &DBase{Store: kvlite.MemStore()}
	for i := 0; i < 25; i++ {
		db.Set(EmbeddedChunks, fmt.Sprintf("c%d", i), EmbeddedChunk{
			ID: fmt.Sprintf("c%d", i), Source: "s", Vector: []float32{1, 0},
		})
	}

	WarmChunkCache(db)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		chunkCache.mu.RLock()
		e := chunkCache.entries[db]
		chunkCache.mu.RUnlock()
		if e != nil {
			if len(e.chunks) != 25 {
				t.Fatalf("warmed with %d chunks, want 25", len(e.chunks))
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("cache never warmed")
}

// Nothing waits on it and nothing breaks without a store.
func TestWarmChunkCacheToleratesNoStore(t *testing.T) { WarmChunkCache(nil) }

// A write to one corpus must not throw away another's snapshot. Dropping every
// entry meant one knowledge save charged the next message a full rebuild of
// every OTHER cached store — a recall that is slow "sometimes" for no visible
// reason, and one that warming cannot fix because it recurs on every write.
func TestInvalidateSpareTheStoresThatDidNotChange(t *testing.T) {
	invalidateChunkCache()
	a := &DBase{Store: kvlite.MemStore()}
	b := &DBase{Store: kvlite.MemStore()}
	a.Set(EmbeddedChunks, "a1", EmbeddedChunk{ID: "a1", Source: "a", Vector: []float32{1, 0}})
	b.Set(EmbeddedChunks, "b1", EmbeddedChunk{ID: "b1", Source: "b", Vector: []float32{0, 1}})

	snapshotChunks(a)
	snapshotChunks(b)
	if len(chunkCache.entries) != 2 {
		t.Fatalf("expected both cached, got %d", len(chunkCache.entries))
	}

	invalidateChunkCacheFor(a)
	if _, still := chunkCache.entries[a]; still {
		t.Fatal("the written store kept a stale snapshot")
	}
	if _, kept := chunkCache.entries[b]; !kept {
		t.Fatal("an untouched store lost its snapshot and will rebuild for nothing")
	}
}

// Scoping must not hide a write from the store that took it.
func TestAWriteIsVisibleToTheStoreThatTookIt(t *testing.T) {
	invalidateChunkCache()
	db := &DBase{Store: kvlite.MemStore()}
	db.Set(EmbeddedChunks, "c1", EmbeddedChunk{ID: "c1", Source: "s", Vector: []float32{1, 0}})
	if got := len(snapshotChunks(db)); got != 1 {
		t.Fatalf("warm snapshot has %d chunks, want 1", got)
	}

	db.Set(EmbeddedChunks, "c2", EmbeddedChunk{ID: "c2", Source: "s", Vector: []float32{0, 1}})
	invalidateChunkCacheFor(db)
	if got := len(snapshotChunks(db)); got != 2 {
		t.Fatalf("the new chunk never reached search: %d chunks, want 2", got)
	}
}

// A caller that cannot name what it changed still gets the whole cache dropped.
func TestNilTargetDropsEverything(t *testing.T) {
	invalidateChunkCache()
	db := &DBase{Store: kvlite.MemStore()}
	db.Set(EmbeddedChunks, "x", EmbeddedChunk{ID: "x", Source: "s", Vector: []float32{1, 0}})
	snapshotChunks(db)
	invalidateChunkCacheFor(nil)
	if len(chunkCache.entries) != 0 {
		t.Fatalf("nil target left %d entries cached", len(chunkCache.entries))
	}
}

// Every maintenance action the deployment ships must actually be in the
// registry after package init.
//
// This exists because one silently wasn't. The source-hook engine moved to a
// leaf package and kept registering its cache sweep through a hook that core's
// init assigns — but a leaf initializes BEFORE the package importing it, so the
// hook was still nil, the nil-guard returned quietly, and the action
// disappeared from the admin surface. Nothing failed; it was simply gone.

func TestShippedMaintenanceActionsAreRegistered(t *testing.T) {
	want := map[string]bool{
		"sweep_expired_caches":         false,
		"survey_workspace_bound_tools": false,
		"survey_workspace_usage":       false,
		"survey_reapable_artifacts":    false,
		"reap_workspace_artifacts":     false,
	}
	for _, m := range ListMaintenanceFuncs() {
		if _, ok := want[m.Key]; ok {
			want[m.Key] = true
		}
		if m.Label == "" || m.Desc == "" {
			t.Errorf("maintenance %q has no label or description — the admin surface renders both", m.Key)
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("maintenance action %q is not registered; if it moved to a leaf package, register it from core — a leaf's init runs first and a hook assigned in core's init is still nil there", key)
		}
	}
}

// A page opened mid-run found an idle row, and a second press ran the same
// pass alongside the first. Most passes never report progress, so "running"
// could not be read off the progress line: it has to be a fact of its own, and
// the second start has to wait on the first rather than walk the store again.
func TestAMaintenancePassRunsOnceAndIsVisibleWhileItRuns(t *testing.T) {
	saved := maintenanceFuncs
	t.Cleanup(func() { maintenanceFuncs = saved })

	const key = "test_silent_pass"
	release := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	RegisterMaintenanceFunc("Housekeeping", key, "Silent pass", "Says nothing while it runs.",
		func(ctx context.Context) int {
			mu.Lock()
			calls++
			mu.Unlock()
			<-release
			return 7
		})

	results := make(chan int, 2)
	go func() { results <- RunMaintenanceFunc(context.Background(), key) }()

	// Silent, and still visibly running: that is what a page arriving now
	// rejoins on.
	deadline := time.Now().Add(2 * time.Second)
	for MaintenanceProgress(key) == "" {
		if time.Now().After(deadline) {
			t.Fatal("a running pass that has said nothing reads as not running")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := MaintenanceProgress(key); !strings.HasPrefix(got, "running") {
		t.Errorf("a silent running pass reads %q", got)
	}

	// The second press, while the first is in flight.
	go func() { results <- RunMaintenanceFunc(context.Background(), key) }()
	time.Sleep(50 * time.Millisecond)
	close(release)
	for i := 0; i < 2; i++ {
		select {
		case n := <-results:
			if n != 7 {
				t.Errorf("a start returned %d, want the pass's own 7", n)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("a start never returned")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("the pass ran %d times for two overlapping starts", calls)
	}
	if got := MaintenanceProgress(key); got != "" {
		t.Errorf("a finished pass still reads as running: %q", got)
	}
	if got := MaintenanceOutcome(key); !strings.Contains(got, "7 record(s)") {
		t.Errorf("the outcome is %q", got)
	}
}

// A pass that panics must still release its key, or the row reads as running
// for the life of the process and every later press waits forever.
func TestAPanickingMaintenancePassReleasesItsKey(t *testing.T) {
	saved := maintenanceFuncs
	t.Cleanup(func() { maintenanceFuncs = saved })

	const key = "test_panicking_pass"
	RegisterMaintenanceFunc("Housekeeping", key, "Panicking pass", "Falls over.",
		func(ctx context.Context) int { panic("boom") })
	func() {
		defer func() { _ = recover() }()
		RunMaintenanceFunc(context.Background(), key)
	}()
	if got := MaintenanceProgress(key); got != "" {
		t.Errorf("a pass that panicked still reads as running: %q", got)
	}
	if got := MaintenanceOutcome(key); got != "" {
		t.Errorf("a pass that panicked claimed an outcome: %q", got)
	}
}

// A running pass can be stopped: it ends at its next check of its context,
// and how it ended reads "stopped", not "finished". Nothing running, nothing
// to stop.
func TestAMaintenancePassCanBeStopped(t *testing.T) {
	saved := maintenanceFuncs
	t.Cleanup(func() { maintenanceFuncs = saved })

	const key = "test_stoppable_pass"
	if CancelMaintenance(key) {
		t.Fatal("stopped a pass that was not running")
	}
	RegisterMaintenanceFunc("Housekeeping", key, "Long pass", "Walks until told to stop.",
		func(ctx context.Context) int {
			n := 0
			for ctx.Err() == nil {
				n++
				ReportMaintenanceProgress(ctx, "walking")
				time.Sleep(5 * time.Millisecond)
			}
			ReportMaintenanceOutcome(ctx, "walked a while")
			return n
		})
	done := make(chan int, 1)
	go func() { done <- RunMaintenanceFunc(context.Background(), key) }()
	deadline := time.Now().Add(2 * time.Second)
	for MaintenanceProgress(key) != "walking" {
		if time.Now().After(deadline) {
			t.Fatal("the pass never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !CancelMaintenance(key) {
		t.Fatal("could not stop a running pass")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the pass did not stop")
	}
	if got := MaintenanceOutcome(key); got != "stopped: walked a while" {
		t.Fatalf("a stopped pass ended reading %q", got)
	}
}

// TestSearchRanksOnRelevanceNotAge pins the no-decay design (see the note in
// vector_store.go): a strong-match OLD chunk must outrank a weaker FRESH one.
// The removed 180d sort-key decay buried a year-old authoritative upload at
// ~x0.25, letting fresh derived chatter push it out of the candidate pool.
func TestSearchRanksOnRelevanceNotAge(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	oldDate := time.Now().AddDate(-2, 0, 0).Format(time.RFC3339)
	db.Set(EmbeddedChunks, "old-strong", EmbeddedChunk{
		ID: "old-strong", Source: "kb", ReportID: "r1", Section: "## Policy",
		Text: "the authoritative policy", Vector: []float32{1, 0}, Date: oldDate,
	})
	db.Set(EmbeddedChunks, "fresh-weak", EmbeddedChunk{
		ID: "fresh-weak", Source: "kb", ReportID: "r2", Section: "## Chatter",
		Text: "recent tangential note", Vector: []float32{0.7, 0.72}, Date: time.Now().Format(time.RFC3339),
	})

	hits := SearchChunks(db, []float32{1, 0}, 2)
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits, got %d", len(hits))
	}
	if hits[0].ID != "old-strong" {
		t.Fatalf("stronger old match must rank first, got %q (score %.2f) over %q",
			hits[0].ID, hits[0].Score, hits[1].ID)
	}
}

// The hybrid merge must keep the STRONGER signal for a chunk both halves
// found. It used to keep whichever copy came first (the vector one), so a
// chunk with cosine 0.30 and a keyword score of 0.80 left the merge at 0.30
// and was dropped by the 0.35 relevance floor.
func TestHybridMergeKeepsTheHigherScore(t *testing.T) {
	vec := []SearchHit{{ID: "x", Score: 0.30}, {ID: "y", Score: 0.50}}
	kw := []SearchHit{{ID: "x", Score: 0.80}}
	m := MergeHitsByScore(vec, kw, 5)
	if len(m) != 2 {
		t.Fatalf("expected 2 merged hits, got %d", len(m))
	}
	if m[0].ID != "x" || m[0].Score != 0.80 {
		t.Fatalf("x must carry its keyword score 0.80 and rank first, got %q at %.2f", m[0].ID, m[0].Score)
	}
	if kept := aboveFloor(m); len(kept) != 2 {
		t.Fatalf("both hits clear the floor once the higher score is kept, got %d", len(kept))
	}
	// Order of the arguments must not matter.
	m = MergeHitsByScore(kw, vec, 5)
	if m[0].ID != "x" || m[0].Score != 0.80 {
		t.Fatalf("reversed args: got %q at %.2f", m[0].ID, m[0].Score)
	}
}

// Stamped chunks reassemble in ingest order, whatever their headings.
func TestAssemblyFollowsTheOrdStamp(t *testing.T) {
	var chunks []EmbeddedChunk
	for i, s := range []string{"Overview", "Background", "Conclusion"} {
		chunks = append(chunks, EmbeddedChunk{ID: UUIDv4(), Title: "Report", Section: s, Text: s + " body", Ord: i + 1})
	}
	// Hand them over shuffled.
	chunks[0], chunks[2] = chunks[2], chunks[0]
	out := AssembleChunkDoc(chunks, 0)
	want := []string{"# Report", "## Overview", "## Background", "## Conclusion"}
	last := -1
	for _, w := range want {
		idx := strings.Index(out, w)
		if idx < 0 || idx < last {
			t.Fatalf("expected %q in document order, got:\n%s", w, out)
		}
		last = idx
	}
}

// Legacy rows (no Ord) fall back to a natural sort: "(part 10)" after
// "(part 2)", and nested "(part N/M)" splits ordered numerically too.
func TestLegacyAssemblyOrdersPartsNumerically(t *testing.T) {
	var chunks []EmbeddedChunk
	for _, s := range []string{"Guide (part 10)", "Guide (part 2)", "Guide (part 1)", "Guide (part 11)", "Guide (part 2) (part 2/2)", "Guide (part 2) (part 1/2)"} {
		chunks = append(chunks, EmbeddedChunk{ID: UUIDv4(), Section: s, Text: s})
	}
	SortChunksForAssembly(chunks)
	var got []string
	for _, c := range chunks {
		got = append(got, c.Section)
	}
	want := "Guide (part 1) | Guide (part 2) | Guide (part 2) (part 1/2) | Guide (part 2) (part 2/2) | Guide (part 10) | Guide (part 11)"
	if strings.Join(got, " | ") != want {
		t.Fatalf("legacy order wrong:\n got %s\nwant %s", strings.Join(got, " | "), want)
	}
}

// One legacy row in a document switches the whole set to the fallback, so a
// stamped chunk never compares its Ord against a 0.
func TestMixedStampAndLegacyUsesFallback(t *testing.T) {
	chunks := []EmbeddedChunk{
		{ID: "b", Section: "Zeta", Ord: 1},
		{ID: "a", Section: "Alpha", Ord: 0},
	}
	SortChunksForAssembly(chunks)
	if chunks[0].Section != "Alpha" {
		t.Fatalf("mixed set must natural-sort by section, got %q first", chunks[0].Section)
	}
}

// Ingest stamps every row, in order, including the sub-pieces the embed
// fallback produces — so a re-read of an unstructured upload with more than
// nine parts comes back in the order it was written.
func TestIngestStampsOrdInDocumentOrder(t *testing.T) {
	// Another test in the package may leave a fake embedding endpoint
	// configured; this one is about the stamp, not the vector.
	prev := GetEmbeddingConfig()
	defer SetEmbeddingConfig(prev)
	SetEmbeddingConfig(EmbeddingConfig{})
	db := &DBase{Store: kvlite.MemStore()}
	var b strings.Builder
	for i := 1; i <= 12; i++ {
		b.WriteString("## Section ")
		b.WriteString(strings.Repeat("x", i)) // distinct headings that do not sort numerically
		b.WriteString("\n\npara ")
		b.WriteString(strings.Repeat("y", i))
		b.WriteString("\n\n")
	}
	IngestReportTitled(context.Background(), db, "collection:t", "doc-1", "Twelve", b.String(), "")
	chunks := ChunksWhere(db, func(c EmbeddedChunk) bool { return c.ReportID == "doc-1" })
	if len(chunks) != 12 {
		t.Fatalf("expected 12 chunks, got %d", len(chunks))
	}
	SortChunksForAssembly(chunks)
	for i, c := range chunks {
		if c.Ord != i+1 {
			t.Fatalf("chunk %d has Ord %d", i, c.Ord)
		}
		if !strings.HasSuffix(c.Section, strings.Repeat("x", i+1)) {
			t.Fatalf("chunk %d out of order: %q", i, c.Section)
		}
	}
	if got := AssembleChunkDoc(chunks, 0); !strings.HasPrefix(got, "# Twelve\n\n## Section x\n") {
		t.Fatalf("assembled doc must open with the title then the first section, got:\n%.80s", got)
	}
}

// The keyword and substring scans see the parent document's Title, so a
// query that names the document finds chunks whose own text never does.
func TestLexicalSearchMatchesTheTitle(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	db.Set(EmbeddedChunks, "c1", EmbeddedChunk{
		ID: "c1", Source: "collection:t", ReportID: "r1", Title: "OPNsense firewall guide",
		Section: "## Overview", Text: "This document covers installation and rule ordering.",
	})
	db.Set(EmbeddedChunks, "c2", EmbeddedChunk{
		ID: "c2", Source: "collection:t", ReportID: "r2", Title: "Bread recipes",
		Section: "## Overview", Text: "Knead, prove, bake.",
	})
	all := func(EmbeddedChunk) bool { return true }
	hits := SearchChunksKeywordByPredicate(db, all, "opnsense", 5)
	if len(hits) != 1 || hits[0].ID != "c1" {
		t.Fatalf("keyword search must match the title, got %+v", hits)
	}
	hits = SearchChunksSubstringByPredicate(db, all, "opnsense", 5)
	if len(hits) != 1 || hits[0].ID != "c1" {
		t.Fatalf("substring search must match the title, got %+v", hits)
	}
}

// The embed prompt carries the title once, and not when it merely repeats
// the section (an upload's first section IS its name).
func TestEmbedHeaderCarriesTheTitleOnce(t *testing.T) {
	if got := embedHeader("Guide", "## Overview"); got != "Guide\n## Overview" {
		t.Fatalf("got %q", got)
	}
	if got := embedHeader("", "## Overview"); got != "## Overview" {
		t.Fatalf("empty title must leave the section alone, got %q", got)
	}
	if got := embedHeader("Overview", "Overview"); got != "Overview" {
		t.Fatalf("title equal to section must not repeat, got %q", got)
	}
}

// One hit shape for every app: the title, section, page locator and
// provenance kind all reach the model, and the doc_id line appears only
// for a caller that has a fetch tool to pass it to.
func TestHitFormatCarriesEverythingTheStoreStamps(t *testing.T) {
	hits := []SearchHit{
		{ReportID: "r1", Title: "OPNsense guide", Section: "## Rules (part 2)", Text: "first para\nsecond para", Locator: "page 12", Kind: "user_comment"},
		{ReportID: "r2", Section: "## Bread", Text: strings.Repeat("word ", 100)},
	}
	full := HitFormat{}.Render(hits)
	for _, want := range []string{
		"1. OPNsense guide - Rules (page 12) [user_comment]\n   first para\n   second para",
		"\n\n2. Bread\n   word word",
	} {
		if !strings.Contains(full, want) {
			t.Fatalf("missing %q in:\n%s", want, full)
		}
	}
	if strings.Contains(full, "doc_id") {
		t.Fatalf("no doc_id line without DocIDs:\n%s", full)
	}

	pane := HitFormat{Excerpt: 40, DocIDs: true, Tag: func(h SearchHit) string {
		if h.ReportID == "r2" {
			return "derived"
		}
		return ""
	}}.Render(hits)
	for _, want := range []string{
		"1. OPNsense guide - Rules (page 12) [user_comment]\n   doc_id: r1\n   section: Rules\n   first para",
		"2. Bread [derived]\n   doc_id: r2\n   word word",
	} {
		if !strings.Contains(pane, want) {
			t.Fatalf("missing %q in:\n%s", want, pane)
		}
	}
	if !strings.HasSuffix(pane, "…") || strings.Count(pane, "word") > 10 {
		t.Fatalf("excerpt must cut at the cap with an ellipsis:\n%s", pane)
	}
	// A title-less, section-only hit does not repeat itself as its own section.
	if strings.Contains(pane, "section: Bread") {
		t.Fatalf("section line must be omitted when it is the document name:\n%s", pane)
	}
	if (HitFormat{}).Render(nil) != "" {
		t.Fatal("empty input must render empty")
	}
}

// A long document's extra passages are demoted behind another document's
// first one, and then fill the slots that remain.
func TestDiversifyPromotesASecondDocument(t *testing.T) {
	hits := []SearchHit{
		{ID: "a1", ReportID: "A", Score: 0.90},
		{ID: "a2", ReportID: "A", Score: 0.85},
		{ID: "a3", ReportID: "A", Score: 0.80},
		{ID: "a4", ReportID: "A", Score: 0.75},
		{ID: "b1", ReportID: "B", Score: 0.50},
		{ID: "a5", ReportID: "A", Score: 0.70},
	}
	got := diversifyHits(hits, 2, 4)
	want := "a1 a2 b1 a3"
	if ids(got) != want {
		t.Fatalf("got %q, want %q", ids(got), want)
	}
}

// A corpus of one document still returns k passages from it — diversity
// changes order, never the count.
func TestDiversifyKeepsASingleDocumentWhole(t *testing.T) {
	var hits []SearchHit
	for i := 0; i < 8; i++ {
		hits = append(hits, SearchHit{ID: string(rune('a' + i)), ReportID: "A", Score: float32(0.9) - float32(i)*0.05})
	}
	got := diversifyHits(hits, 2, 5)
	if ids(got) != "a b c d e" {
		t.Fatalf("got %q", ids(got))
	}
}

// A passage not worth reading is never promoted over one that is: the
// callers' floor would drop it and the slot with it.
func TestDiversifyDoesNotPromoteBelowTheFloor(t *testing.T) {
	hits := []SearchHit{
		{ID: "a1", ReportID: "A", Score: 0.90},
		{ID: "a2", ReportID: "A", Score: 0.85},
		{ID: "a3", ReportID: "A", Score: 0.80},
		{ID: "b1", ReportID: "B", Score: 0.20},
	}
	if got := ids(diversifyHits(hits, 2, 3)); got != "a1 a2 a3" {
		t.Fatalf("got %q", got)
	}
	// Off means plain truncation.
	if got := ids(diversifyHits(hits, 0, 3)); got != "a1 a2 a3" {
		t.Fatalf("perDoc=0: got %q", got)
	}
	// Hits with no document are never capped.
	loose := []SearchHit{{ID: "x", Score: 0.9}, {ID: "y", Score: 0.8}, {ID: "z", Score: 0.7}}
	if got := ids(diversifyHits(loose, 1, 3)); got != "x y z" {
		t.Fatalf("no ReportID: got %q", got)
	}
}

// End to end through the keyword half: a guide with six matching passages
// and a second document with one — k=3 must show the second document.
func TestHybridSearchShowsTheSecondDocument(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	for i := 0; i < 6; i++ {
		id := "a" + string(rune('0'+i))
		db.Set(EmbeddedChunks, id, EmbeddedChunk{ID: id, Source: "collection:t", ReportID: "guide",
			Section: "## Part", Text: "OPNsense failover configuration step " + strings.Repeat("detail ", i)})
	}
	db.Set(EmbeddedChunks, "b0", EmbeddedChunk{ID: "b0", Source: "collection:t", ReportID: "note",
		Section: "## Note", Text: "OPNsense failover configuration summary"})
	if got := recallPerDocMax(); got != 2 {
		t.Fatalf("default per-doc cap should be 2, got %d", got)
	}
	hits := HybridSearchByPredicate(db, func(EmbeddedChunk) bool { return true }, "opnsense failover configuration", nil, 3)
	if len(hits) != 3 {
		t.Fatalf("expected 3 hits, got %d", len(hits))
	}
	docs := map[string]bool{}
	for _, h := range hits {
		docs[h.ReportID] = true
	}
	if !docs["note"] {
		t.Fatalf("the second document must be in the top 3, got %q", ids(hits))
	}
}

func ids(hits []SearchHit) string {
	var out []string
	for _, h := range hits {
		out = append(out, h.ID)
	}
	return strings.Join(out, " ")
}

// Two-letter identifiers are query terms; short terms match whole words
// only; longer terms keep substring matching; short fillers stay out.
func TestKeywordTermsKeepShortIdentifiers(t *testing.T) {
	got := strings.Join(keywordTerms("Is it safe to shrink an LVM volume in Go on S3 vs a VM?"), " ")
	if got != "safe shrink lvm volume go s3 vm" {
		t.Fatalf("terms = %q", got)
	}
	if terms := keywordTerms("c r"); len(terms) != 0 {
		t.Fatalf("single letters must be dropped, got %v", terms)
	}
}

func TestShortTermsMatchWholeWordsOnly(t *testing.T) {
	cases := []struct {
		text, term string
		want       bool
	}{
		{"written in go, not rust", "go", true},
		{"a good idea from long ago", "go", false},
		{"upload to the s3 bucket", "s3", true},
		{"the cat sat.", "cat", true},
		{"filed under a category", "cat", false},
		{"kubernetes (k8s) networking", "k8s", true},
		{"my_go_module", "go", false},
		{"both firewalls failed", "firewall", true}, // >3 chars: substring
		{"go", "go", true},
		{"não go", "go", true},
	}
	for _, c := range cases {
		if got := termMatches(c.text, c.term); got != c.want {
			t.Errorf("termMatches(%q, %q) = %v, want %v", c.text, c.term, got, c.want)
		}
	}
}

// End to end: a query naming a two-letter identifier finds the chunk that
// has it as a word and not the one that merely contains the letters.
func TestKeywordSearchFindsATwoLetterIdentifier(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	db.Set(EmbeddedChunks, "go", EmbeddedChunk{ID: "go", Source: "collection:t", ReportID: "r1",
		Section: "## Toolchain", Text: "The service is written in Go and built with make."})
	db.Set(EmbeddedChunks, "ago", EmbeddedChunk{ID: "ago", Source: "collection:t", ReportID: "r2",
		Section: "## History", Text: "Long ago the good old gopher logo was chosen."})
	hits := SearchChunksKeywordByPredicate(db, func(EmbeddedChunk) bool { return true }, "go service", 5)
	if len(hits) == 0 || hits[0].ID != "go" {
		t.Fatalf("expected the Go chunk first, got %q", ids(hits))
	}
	for _, h := range hits {
		if h.ID == "ago" {
			t.Fatalf("\"go\" must not match inside \"ago\"/\"good\"/\"gopher\": %q", ids(hits))
		}
	}
}

// A section a user titled "Sources" is content in an uploaded document and
// a bibliography in a synthesized report. The document paths keep it; the
// report path drops it.
func TestSourcesSectionSurvivesADocumentIngest(t *testing.T) {
	prev := GetEmbeddingConfig()
	defer SetEmbeddingConfig(prev)
	SetEmbeddingConfig(EmbeddingConfig{})
	db := &DBase{Store: kvlite.MemStore()}
	doc := "## Data guide\n\nHow the warehouse is fed.\n\n## Sources\n\nThe CRM export and the billing feed, nightly.\n"
	sections := func(reportID string) string {
		chunks := ChunksWhere(db, func(c EmbeddedChunk) bool { return c.ReportID == reportID })
		SortChunksForAssembly(chunks)
		var out []string
		for _, c := range chunks {
			out = append(out, c.Section)
		}
		return strings.Join(out, " | ")
	}
	IngestReport(context.Background(), db, "collection:t", "upload", doc)
	if got := sections("upload"); got != "Data guide | Sources" {
		t.Fatalf("upload must keep its Sources section, got %q", got)
	}
	IngestPagedReport(context.Background(), db, "collection:t", "pdf", doc)
	if got := sections("pdf"); got != "Data guide | Sources" {
		t.Fatalf("PDF upload must keep its Sources section, got %q", got)
	}
	IngestReportTitled(context.Background(), db, "collection:t", "report", "Data guide", doc, "research")
	if got := sections("report"); got != "Data guide" {
		t.Fatalf("a synthesized report must drop its bibliography, got %q", got)
	}
	if err := IngestRecallSpan(context.Background(), db, "history:x", "span", "chat", "## Sources\n\nsee the two links above\n", ""); err != nil {
		t.Fatalf("a transcript span that is only a Sources heading must still archive: %v", err)
	}
}

// A delete through the store invalidates the read cache, so the next search
// cannot return the row. The hand-rolled deletes this replaced did not.
func TestDeleteChunksWhereIsVisibleToTheNextSearch(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	for _, id := range []string{"keep", "drop"} {
		db.Set(EmbeddedChunks, id, EmbeddedChunk{ID: id, Source: "collection:t", ReportID: id, Section: "## S", Text: "opnsense " + id})
	}
	all := func(EmbeddedChunk) bool { return true }
	if n := len(SearchChunksKeywordByPredicate(db, all, "opnsense", 5)); n != 2 {
		t.Fatalf("warm-up: expected 2 hits, got %d", n)
	}
	if n := DeleteChunksWhere(db, func(c EmbeddedChunk) bool { return c.ReportID == "drop" }); n != 1 {
		t.Fatalf("expected 1 removed, got %d", n)
	}
	hits := SearchChunksKeywordByPredicate(db, all, "opnsense", 5)
	if len(hits) != 1 || hits[0].ID != "keep" {
		t.Fatalf("deleted chunk still served from the cache: %q", ids(hits))
	}
	if n := DeleteChunksWhere(db, func(c EmbeddedChunk) bool { return false }); n != 0 {
		t.Fatalf("nothing matched, got %d", n)
	}
}
