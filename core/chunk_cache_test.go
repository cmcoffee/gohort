package core

import (
	"fmt"
	"testing"

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
