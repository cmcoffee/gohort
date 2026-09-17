package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// stubEmbedServer stands in for the embedding backend. reply decides what each
// request gets, so a test can serve vectors, fail everything, or count calls.
func stubEmbedServer(t *testing.T, model string, reply func(n int32) (int, []float32)) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		status, vec := reply(n)
		if status != 200 {
			http.Error(w, "embedder unavailable", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{vec}})
	}))
	t.Cleanup(srv.Close)

	prev := GetEmbeddingConfig()
	SetEmbeddingConfig(EmbeddingConfig{Endpoint: srv.URL, Model: model, Enabled: true})
	t.Cleanup(func() { SetEmbeddingConfig(prev) })
	return srv, &calls
}

// The repair pass exists because ingest stores a chunk with an empty vector
// when the embedder is down, and nothing ever went back for those rows — they
// stay findable by keyword and invisible to semantic search forever.
func TestReembedFillsMissingVectorsOnly(t *testing.T) {
	_, calls := stubEmbedServer(t, "new-model", func(int32) (int, []float32) {
		return 200, []float32{0.5, 0.5}
	})

	db := &DBase{Store: kvlite.MemStore()}
	db.Set(EmbeddedChunks, "healthy", EmbeddedChunk{
		ID: "healthy", Source: "kb", ReportID: "r1", Section: "## Kept",
		Text: "already embedded", Vector: []float32{1, 0}, Model: "old-model",
	})
	db.Set(EmbeddedChunks, "broken-1", EmbeddedChunk{
		ID: "broken-1", Source: "kb", ReportID: "r2", Section: "## Lost",
		Text: "ingested while the embedder was down", Model: "old-model",
	})
	db.Set(EmbeddedChunks, "broken-2", EmbeddedChunk{
		ID: "broken-2", Source: "uploads", ReportID: "r3", Section: "## Also lost",
		Text: "same outage, different source", Model: "old-model",
	})
	// No text to embed — must be skipped, not sent to the embedder.
	db.Set(EmbeddedChunks, "textless", EmbeddedChunk{
		ID: "textless", Source: "kb", ReportID: "r4", Section: "## Empty",
	})

	fixed := ReembedUnvectoredChunks(context.Background(), db)
	if fixed != 2 {
		t.Fatalf("expected 2 rows repaired, got %d", fixed)
	}
	if *calls != 2 {
		t.Errorf("only the vectorless rows with text should be embedded; server saw %d calls", *calls)
	}

	// A repaired row must carry the CURRENT model, or chunkVectorComparable
	// gates it out and the repair achieves nothing.
	for _, id := range []string{"broken-1", "broken-2"} {
		var c EmbeddedChunk
		if !db.Get(EmbeddedChunks, id, &c) {
			t.Fatalf("%s vanished", id)
		}
		if len(c.Vector) == 0 {
			t.Errorf("%s still has no vector", id)
		}
		if c.Model != "new-model" {
			t.Errorf("%s kept model %q; a repaired row must be stamped with the current model", id, c.Model)
		}
	}
	// The healthy row is left exactly as it was — including its stale model
	// string, which is a different problem with a different repair.
	var kept EmbeddedChunk
	db.Get(EmbeddedChunks, "healthy", &kept)
	if kept.Model != "old-model" || len(kept.Vector) != 2 || kept.Vector[0] != 1 {
		t.Errorf("an already-embedded row must not be touched, got %+v", kept)
	}
}

// The operator most likely to press this button is one who hasn't noticed the
// endpoint is still down. Without a breaker that's one 20s request per backlog
// row — so the pass must give up quickly instead of grinding for hours.
func TestReembedStopsWhenEndpointIsDown(t *testing.T) {
	_, calls := stubEmbedServer(t, "m", func(int32) (int, []float32) {
		return 500, nil
	})

	db := &DBase{Store: kvlite.MemStore()}
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		db.Set(EmbeddedChunks, id, EmbeddedChunk{ID: id, Source: "kb", Text: "unembedded " + id})
	}

	if fixed := ReembedUnvectoredChunks(context.Background(), db); fixed != 0 {
		t.Fatalf("nothing can be repaired against a dead endpoint, got %d", fixed)
	}
	if int(*calls) != reembedFailStreak {
		t.Errorf("should stop after %d consecutive failures, made %d attempts against 10 rows", reembedFailStreak, *calls)
	}
}

// A single failing chunk (oversized, say) is not an outage: the pass skips it
// and keeps going, and the streak resets on the next success.
func TestReembedSkipsOneBadChunkAndContinues(t *testing.T) {
	_, calls := stubEmbedServer(t, "m", func(n int32) (int, []float32) {
		if n == 1 {
			return 500, nil
		}
		return 200, []float32{1, 0}
	})

	db := &DBase{Store: kvlite.MemStore()}
	for _, id := range []string{"a", "b", "c"} {
		db.Set(EmbeddedChunks, id, EmbeddedChunk{ID: id, Source: "kb", Text: "unembedded " + id})
	}

	if fixed := ReembedUnvectoredChunks(context.Background(), db); fixed != 2 {
		t.Fatalf("one bad chunk should cost only itself; expected 2 repaired, got %d", fixed)
	}
	if *calls != 3 {
		t.Errorf("every candidate should be attempted, server saw %d calls", *calls)
	}
}

// Disabled embeddings is a configuration state, not a repairable fault — the
// pass must not fire a single request.
func TestReembedNoopWhenDisabled(t *testing.T) {
	_, calls := stubEmbedServer(t, "m", func(int32) (int, []float32) { return 200, []float32{1, 0} })
	cfg := GetEmbeddingConfig()
	cfg.Enabled = false
	SetEmbeddingConfig(cfg)

	db := &DBase{Store: kvlite.MemStore()}
	db.Set(EmbeddedChunks, "a", EmbeddedChunk{ID: "a", Source: "kb", Text: "unembedded"})

	if fixed := ReembedUnvectoredChunks(context.Background(), db); fixed != 0 {
		t.Fatalf("expected no repair with embeddings disabled, got %d", fixed)
	}
	if *calls != 0 {
		t.Errorf("disabled embeddings must not reach the endpoint, saw %d calls", *calls)
	}
}

// The pass is only reachable through the admin Maintenance list, so a
// registration that silently didn't happen would leave the repair unusable
// while every other test here still passed.
func TestReembedIsRegisteredAsMaintenance(t *testing.T) {
	for _, f := range ListMaintenanceFuncs() {
		if f.Key == "reembed_stale_chunks" {
			if f.Label == "" || f.Desc == "" || f.Group != "Vector index" {
				t.Errorf("maintenance entry needs a label, description and the Vector index group, got %+v", f)
			}
			return
		}
	}
	t.Fatal("reembed_stale_chunks is not registered — the admin panel has no way to repair the index")
}

// The stats panel is the only place an operator learns this happened, so the
// breakdown that tells them WHERE has to be right.
func TestVectorStatsReportsEmptyBySource(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	db.Set(EmbeddedChunks, "ok", EmbeddedChunk{ID: "ok", Source: "kb", Text: "t", Vector: []float32{1, 0}})
	db.Set(EmbeddedChunks, "bad1", EmbeddedChunk{ID: "bad1", Source: "kb", Text: "t"})
	db.Set(EmbeddedChunks, "bad2", EmbeddedChunk{ID: "bad2", Source: "uploads", Text: "t"})
	db.Set(EmbeddedChunks, "bad3", EmbeddedChunk{ID: "bad3", Source: "uploads", Text: "t"})

	stats := VectorStats(db)
	if stats.Total != 4 || stats.Embedded != 1 || stats.Empty != 3 {
		t.Fatalf("totals wrong: %+v", stats)
	}
	if stats.EmptyBySource["kb"] != 1 || stats.EmptyBySource["uploads"] != 2 {
		t.Errorf("empty-by-source wrong: %v", stats.EmptyBySource)
	}
	// Sorted and stable, so the panel doesn't reshuffle between refreshes.
	if stats.EmptyBySourceText != "kb=1, uploads=2" {
		t.Errorf("empty-by-source text = %q", stats.EmptyBySourceText)
	}
	// The healthy row must not appear in the empty breakdown at all.
	if _, ok := stats.EmptyBySource["(unspecified)"]; ok {
		t.Errorf("unexpected bucket in %v", stats.EmptyBySource)
	}
}

// After a model or document-prefix change, the stale pass re-embeds exactly
// the rows outside the current space — stamped otherwise, unstamped, or
// vectorless — and leaves rows already in it alone.
func TestReembedStaleTouchesOnlyRowsOutsideTheSpace(t *testing.T) {
	_, calls := stubEmbedServer(t, "new-model", func(int32) (int, []float32) {
		return 200, []float32{0.5, 0.5}
	})
	db := &DBase{Store: kvlite.MemStore()}
	rows := map[string]EmbeddedChunk{
		"current":  {ID: "current", Source: "kb", ReportID: "r1", Section: "## A", Text: "in the space", Vector: []float32{1, 0}, Model: "new-model"},
		"old":      {ID: "old", Source: "kb", ReportID: "r2", Section: "## B", Text: "other model", Vector: []float32{1, 0}, Model: "old-model"},
		"unstamp":  {ID: "unstamp", Source: "uploads", ReportID: "r3", Section: "## C", Text: "legacy row", Vector: []float32{1, 0}},
		"empty":    {ID: "empty", Source: "uploads", ReportID: "r4", Section: "## D", Text: "embed failed at ingest", Model: "new-model"},
		"textless": {ID: "textless", Source: "kb", ReportID: "r5", Section: "## E", Model: "old-model"},
	}
	for id, c := range rows {
		db.Set(EmbeddedChunks, id, c)
	}
	stats := VectorStats(db)
	if stats.Stale != 2 || stats.StaleBySource["kb"] != 1 || stats.StaleBySource["uploads"] != 1 {
		t.Fatalf("stats must count the stamped-otherwise and unstamped vectors as stale, got %+v", stats)
	}

	fixed := ReembedStaleChunks(context.Background(), db)
	if fixed != 3 || *calls != 3 {
		t.Fatalf("expected old, unstamp and empty repaired (3), got fixed=%d calls=%d", fixed, *calls)
	}
	for _, id := range []string{"old", "unstamp", "empty"} {
		var c EmbeddedChunk
		db.Get(EmbeddedChunks, id, &c)
		if c.Model != "new-model" || len(c.Vector) != 2 || c.Vector[0] != 0.5 {
			t.Errorf("%s not brought into the space: %+v", id, c)
		}
	}
	var kept EmbeddedChunk
	db.Get(EmbeddedChunks, "current", &kept)
	if kept.Vector[0] != 1 {
		t.Errorf("a row already in the space must not be re-embedded: %+v", kept)
	}
	if VectorStats(db).Stale != 0 {
		t.Errorf("nothing should be stale after the pass")
	}
	// A second run finds nothing to do.
	if again := ReembedStaleChunks(context.Background(), db); again != 0 || *calls != 3 {
		t.Errorf("second run must be a no-op, repaired %d with %d calls", again, *calls)
	}
	// The forced pass re-embeds the current row too.
	if all := ReembedAllChunks(context.Background(), db); all != 4 {
		t.Errorf("all-pass must re-embed every row with text, got %d", all)
	}
}

// A document prefix is part of the space, so setting one makes every vector
// stale until the pass runs — the case that motivated the pass.
func TestDocPrefixChangeMakesVectorsStale(t *testing.T) {
	prev := GetEmbeddingConfig()
	defer SetEmbeddingConfig(prev)
	SetEmbeddingConfig(EmbeddingConfig{Enabled: true, Endpoint: "http://x", Model: "m"})
	db := &DBase{Store: kvlite.MemStore()}
	db.Set(EmbeddedChunks, "c", EmbeddedChunk{ID: "c", Source: "kb", Text: "t", Vector: []float32{1}, Model: "m"})
	if VectorStats(db).Stale != 0 {
		t.Fatal("stamped with the current model, not stale")
	}
	SetEmbeddingConfig(EmbeddingConfig{Enabled: true, Endpoint: "http://x", Model: "m", DocPrefix: "passage: "})
	invalidateChunkCacheFor(db)
	if VectorStats(db).Stale != 1 {
		t.Fatal("a document prefix moves the space; the row must read as stale")
	}
}

func TestStalePassIsRegisteredAsMaintenance(t *testing.T) {
	var stale, all, unvectored bool
	for _, f := range ListMaintenanceFuncs() {
		stale = stale || f.Key == "reembed_stale_chunks"
		all = all || f.Key == "reembed_all_chunks"
		unvectored = unvectored || f.Key == "reembed_unvectored_chunks"
	}
	if !stale || !all {
		t.Fatalf("both passes must be on the maintenance list (stale=%v all=%v)", stale, all)
	}
	if unvectored {
		t.Fatal("the missing-vector pass is covered by Repair and must not be a third button")
	}
}

// A chunk with no TEXT is not a repairable gap, and counting it as one is why
// the missing-vector and stale counts never reached zero: the repair skips it
// (nothing to embed) and search cannot return it (nothing to match, and a hit
// would carry an empty body).
func TestTextlessChunksAreCountedApartAndRemovable(t *testing.T) {
	prev := GetEmbeddingConfig()
	defer SetEmbeddingConfig(prev)
	SetEmbeddingConfig(EmbeddingConfig{Enabled: true, Endpoint: "http://x", Model: "now"})
	db := &DBase{Store: kvlite.MemStore()}
	db.Set(EmbeddedChunks, "good", EmbeddedChunk{ID: "good", Source: "kb", Text: "real", Vector: []float32{1}, Model: "now"})
	db.Set(EmbeddedChunks, "repairable", EmbeddedChunk{ID: "repairable", Source: "kb", Text: "real", Model: "now"})
	db.Set(EmbeddedChunks, "stale", EmbeddedChunk{ID: "stale", Source: "kb", Text: "real", Vector: []float32{1}, Model: "old"})
	// The two dead kinds: no text with a vector, and no text without one.
	db.Set(EmbeddedChunks, "dead-stale", EmbeddedChunk{ID: "dead-stale", Source: "uploads", Vector: []float32{1}, Model: "old"})
	db.Set(EmbeddedChunks, "dead-empty", EmbeddedChunk{ID: "dead-empty", Source: "uploads"})

	st := VectorStats(db)
	if st.Unusable != 2 || st.UnusableBySource["uploads"] != 2 {
		t.Fatalf("both textless rows count as unusable, got %+v", st)
	}
	if st.Empty != 1 || st.Stale != 1 {
		t.Fatalf("a textless row must not inflate the repairable counts, got empty=%d stale=%d", st.Empty, st.Stale)
	}

	if n := RemoveUnusableChunks(context.Background(), db); n != 2 {
		t.Fatalf("expected both removed, got %d", n)
	}
	after := VectorStats(db)
	if after.Unusable != 0 || after.Total != 3 {
		t.Fatalf("after cleanup: %+v", after)
	}
	// The rows that carry text are untouched.
	var kept EmbeddedChunk
	if !db.Get(EmbeddedChunks, "stale", &kept) || kept.Text != "real" {
		t.Fatal("a repairable row must survive the cleanup")
	}
	if n := RemoveUnusableChunks(context.Background(), db); n != 0 {
		t.Fatalf("a second run has nothing to do, got %d", n)
	}
}

// A chunk too large for the embedder must be repairable. Such rows exist
// precisely because the embedder was unreachable at ingest — a non-size error
// bails without splitting and stores the whole text raw — so once it is back
// it answers "too large" every time. The repair used to embed once and give
// up, which left those rows in the missing-vector count forever.
func TestReembedSplitsAnOversizedChunk(t *testing.T) {
	var calls int32
	_, _ = stubEmbedServer(t, "m", func(n int32) (int, []float32) {
		atomic.StoreInt32(&calls, n)
		return 200, []float32{0.5, 0.5}
	})
	// The stub answers everything, so drive the split through the real size
	// path: a chunk whose text the embedder rejects until it is halved.
	db := &DBase{Store: kvlite.MemStore()}
	db.Set(EmbeddedChunks, "big", EmbeddedChunk{
		ID: "big", Source: "kb", ReportID: "r1", Title: "Doc", Section: "## Long",
		Text: strings.Repeat("paragraph of text.\n\n", 200), Ord: 3,
	})
	if n := ReembedUnvectoredChunks(context.Background(), db); n < 1 {
		t.Fatalf("the oversized row must be repaired, got %d", n)
	}
	rows := ChunksWhere(db, func(c EmbeddedChunk) bool { return c.ReportID == "r1" })
	for _, r := range rows {
		if len(r.Vector) == 0 {
			t.Fatalf("every resulting row must carry a vector: %+v", r)
		}
		if r.Ord != 3 {
			t.Errorf("a split part keeps the original's position, got Ord %d", r.Ord)
		}
	}
	// Parts of one position order by their part number, not by UUID.
	parts := []EmbeddedChunk{
		{ID: "zzz", Ord: 3, Section: "## Long (part 1/2)"},
		{ID: "aaa", Ord: 3, Section: "## Long (part 2/2)"},
	}
	SortChunksForAssembly(parts)
	if !strings.Contains(parts[0].Section, "part 1/2") {
		t.Fatalf("split parts must order by part number: %q then %q", parts[0].Section, parts[1].Section)
	}
}
