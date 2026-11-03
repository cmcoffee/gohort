package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/kvlite"
)

// stubEmbedServer stands in for the embedding backend. reply decides what each
// request gets, so a test can serve vectors, fail everything, or count calls.
func stubEmbedServer(t *testing.T, model string, reply func(n int32) (int, []float32)) (*httptest.Server, *int32) {
	t.Helper()
	return stubEmbedServerOf(t, model, func(n int32, _ []string) (int, []float32) { return reply(n) })
}

// stubEmbedServerOf is stubEmbedServer whose reply also sees the request's
// inputs, and which answers a vector for each input, as an embedder does.
func stubEmbedServerOf(t *testing.T, model string, reply func(n int32, inputs []string) (int, []float32)) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		status, vec := reply(n, req.Input)
		if status != 200 {
			http.Error(w, "embedder unavailable", status)
			return
		}
		vecs := make([][]float32, len(req.Input))
		for i := range vecs {
			vecs[i] = vec
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": vecs})
	}))
	t.Cleanup(srv.Close)
	useConnectorEmbedder(t, srv.URL, model)
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
	// Both in one request: the pass sends its chunks in batches.
	if *calls != 1 {
		t.Errorf("only the vectorless rows with text should be embedded, in one request; server saw %d calls", *calls)
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
	// The batch, refused, then one at a time until the breaker trips.
	if int(*calls) != reembedFailStreak+1 {
		t.Errorf("should stop after the batch and %d consecutive failures, made %d attempts against 10 rows", reembedFailStreak, *calls)
	}
}

// A single failing chunk (oversized, say) is not an outage: the pass skips it
// and keeps going, and the streak resets on the next success.
func TestReembedSkipsOneBadChunkAndContinues(t *testing.T) {
	// The embedder refuses any request carrying chunk b: the batch with it,
	// then b alone.
	_, calls := stubEmbedServerOf(t, "m", func(_ int32, inputs []string) (int, []float32) {
		for _, in := range inputs {
			if strings.Contains(in, "unembedded b") {
				return 500, nil
			}
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
	// The batch, then each candidate alone.
	if *calls != 4 {
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
	if fixed != 3 || *calls != 1 {
		t.Fatalf("expected old, unstamp and empty repaired (3) in one request, got fixed=%d calls=%d", fixed, *calls)
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
	if again := ReembedStaleChunks(context.Background(), db); again != 0 || *calls != 1 {
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

// upkeepWorld stands in for the two repair passes, counting their runs, and
// resets the upkeep state around the test.
func upkeepWorld(t *testing.T) (runs func() (cleanups, repairs int)) {
	t.Helper()
	var mu sync.Mutex
	cleanups, repairs := 0, 0
	saved := maintenanceFuncs
	maintenanceFuncs = append([]MaintenanceFunc{
		{Key: "vector_database_cleanup", Run: func(context.Context) int { mu.Lock(); cleanups++; mu.Unlock(); return 0 }},
		{Key: "reembed_stale_chunks", Run: func(context.Context) int { mu.Lock(); repairs++; mu.Unlock(); return 2 }},
	}, saved...)
	prevDelay := vectorRepairDelay
	vectorRepairDelay = 10 * time.Millisecond
	reset := func() {
		vectorUpkeep.mu.Lock()
		vectorUpkeep.lastDay, vectorUpkeep.pending = "", false
		vectorUpkeep.mu.Unlock()
	}
	reset()
	t.Cleanup(func() { maintenanceFuncs, vectorRepairDelay = saved, prevDelay; reset() })
	return func() (int, int) { mu.Lock(); defer mu.Unlock(); return cleanups, repairs }
}

// The nightly run is due once per deployment day, in its hour: 3am unless
// another is set.
func TestTheVectorUpkeepRunsOnceANight(t *testing.T) {
	upkeepWorld(t)
	day := time.Date(2026, 11, 3, 3, 10, 0, 0, time.Local)
	if dueNightlyVectorUpkeep(day.Add(-2 * time.Hour)) {
		t.Error("due outside its hour")
	}
	if !dueNightlyVectorUpkeep(day) {
		t.Fatal("not due in its hour")
	}
	if dueNightlyVectorUpkeep(day.Add(30 * time.Minute)) {
		t.Error("due twice in one night")
	}
	if !dueNightlyVectorUpkeep(day.Add(24 * time.Hour)) {
		t.Error("not due the next night")
	}
	tdb := &DBase{Store: kvlite.MemStore()}
	SetTunablesDB(tdb)
	t.Cleanup(func() { SetTunablesDB(nil) })
	tdb.Set(WebTable, TunableVectorUpkeepHour, float64(22))
	InvalidateTunables()
	if dueNightlyVectorUpkeep(day.Add(48 * time.Hour)) {
		t.Error("due at 3am with 22:00 chosen")
	}
	if !dueNightlyVectorUpkeep(day.Add(48*time.Hour + 19*time.Hour)) {
		t.Error("not due in the chosen hour")
	}
}

// A chunk stored with text and no vector wants a repair, and the next embed
// that succeeds sets one off, once, a little later: the cleanup then the
// repair, through the maintenance runner. Without an embedder set up, or with
// the upkeep switched off, nothing is set off.
func TestTheEmbedderComingBackRepairsWhatItMissed(t *testing.T) {
	runs := upkeepWorld(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"embedding":[0.1,0.2]}]}`))
	}))
	defer srv.Close()
	cfg := useConnectorEmbedder(t, srv.URL, "e5")
	ctx := context.Background()
	db := &DBase{Store: kvlite.MemStore()}

	// Every row has its vector, or no text to make one from: nothing wanted.
	putChunks(db, []EmbeddedChunk{{ID: "v1", Text: "fine", Vector: []float32{1}}, {ID: "v2", Text: "  "}})
	embedRaw(ctx, cfg, "x")
	time.Sleep(50 * time.Millisecond)
	if c, r := runs(); c+r != 0 {
		t.Fatalf("a repair ran with nothing to repair: %d %d", c, r)
	}
	// One without: the next good embed sets the repair off once.
	putChunks(db, []EmbeddedChunk{{ID: "e1", Text: "the embedder was down"}})
	embedRaw(ctx, cfg, "x")
	embedRaw(ctx, cfg, "y")
	deadline := time.Now().Add(2 * time.Second)
	for {
		if c, r := runs(); c == 1 && r == 1 {
			break
		}
		if time.Now().After(deadline) {
			c, r := runs()
			t.Fatalf("after the embedder answered: %d cleanup(s), %d repair(s), want 1 and 1", c, r)
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	embedRaw(ctx, cfg, "z")
	time.Sleep(50 * time.Millisecond)
	if c, r := runs(); c != 1 || r != 1 {
		t.Errorf("the repair ran again with nothing new wanting it: %d %d", c, r)
	}

	// Switched off: a row without a vector sets nothing off.
	tdb := &DBase{Store: kvlite.MemStore()}
	SetTunablesDB(tdb)
	t.Cleanup(func() { SetTunablesDB(nil) })
	tdb.Set(WebTable, TunableVectorUpkeep, float64(0))
	InvalidateTunables()
	if TuneBool(TunableVectorUpkeep) {
		t.Fatal("the switch did not take")
	}
	putChunks(db, []EmbeddedChunk{{ID: "e2", Text: "again"}})
	embedRaw(ctx, cfg, "x")
	time.Sleep(50 * time.Millisecond)
	if c, r := runs(); c != 1 || r != 1 {
		t.Errorf("switched off, a repair still ran: %d %d", c, r)
	}
	SetTunablesDB(nil)

	// No embedder set up: nothing to repair with, so nothing is wanted.
	vectorUpkeep.mu.Lock()
	vectorUpkeep.pending = false
	vectorUpkeep.mu.Unlock()
	off := cfg
	off.Enabled = false
	SetEmbeddingConfig(off)
	putChunks(db, []EmbeddedChunk{{ID: "e3", Text: "no embedder"}})
	vectorUpkeep.mu.Lock()
	wanted := vectorUpkeep.pending
	vectorUpkeep.mu.Unlock()
	if wanted {
		t.Error("a repair was wanted with no embedder to make it")
	}
}

// Saving an embedding config that moves stored vectors to another space starts
// the repair, and one that does not (or embeds nothing) starts nothing.
func TestAChangedEmbeddingSpaceStartsTheRepair(t *testing.T) {
	var started []string
	prev := startVectorUpkeep
	startVectorUpkeep = func(why string) { started = append(started, why) }
	t.Cleanup(func() { startVectorUpkeep = prev })

	was := EmbeddingConfig{Enabled: true, Model: "nomic-embed-text", Endpoint: "http://embed.example.test/v1"}
	for _, tc := range []struct {
		name string
		next EmbeddingConfig
		want bool
	}{
		{"the same space", was, false},
		{"another endpoint, the same model", EmbeddingConfig{Enabled: true, Model: "nomic-embed-text", Endpoint: "http://other.example.test/v1"}, false},
		{"another model", EmbeddingConfig{Enabled: true, Model: "bge-m3", Endpoint: was.Endpoint}, true},
		{"a document prefix", EmbeddingConfig{Enabled: true, Model: was.Model, Endpoint: was.Endpoint, DocPrefix: "search_document: "}, true},
		{"turned off", EmbeddingConfig{Model: "bge-m3"}, false},
	} {
		started = nil
		if got := RepairAfterSpaceChange(was, tc.next); got != tc.want || (len(started) == 1) != tc.want {
			t.Errorf("%s: reported %v, started %d, want %v", tc.name, got, len(started), tc.want)
		}
	}
}

// sizeLimitedEmbedServer is an embedder with a small batch, as a llama.cpp
// server is: an input over limit chars is refused for its size, the way
// that server words it.
func sizeLimitedEmbedServer(t *testing.T, limit int) *int32 {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		for _, in := range req.Input {
			if len(in) > limit {
				http.Error(w, "input (588 tokens) is too large to process. increase the physical batch size (current batch size: 512)", 500)
				return
			}
		}
		vecs := make([][]float32, len(req.Input))
		for i := range vecs {
			vecs[i] = []float32{0.5, 0.5}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": vecs})
	}))
	t.Cleanup(srv.Close)
	useConnectorEmbedder(t, srv.URL, "m")
	return &calls
}

// A section heading the splitter took from a run of code was longer than the
// embedder's batch, and it rides on every piece, so no split could make the
// chunk fit. The header sent is clipped; the stored section is not.
func TestALongHeadingStillEmbeds(t *testing.T) {
	sizeLimitedEmbedServer(t, 600)
	heading := "PYTORCH CODE " + strings.Repeat("model . train () ", 110)
	db := &DBase{Store: kvlite.MemStore()}
	db.Set(EmbeddedChunks, "c", EmbeddedChunk{ID: "c", Source: "kb", ReportID: "r1", Section: heading, Text: "optim . step ()"})

	if n := ReembedUnvectoredChunks(context.Background(), db); n != 1 {
		t.Fatalf("a chunk with a long heading must embed, repaired %d", n)
	}
	var c EmbeddedChunk
	db.Get(EmbeddedChunks, "c", &c)
	if len(c.Vector) == 0 || c.Section != heading {
		t.Fatalf("the row must carry a vector and keep its whole heading: vector %d, section %d chars", len(c.Vector), len(c.Section))
	}
	if got := embedHeader(strings.Repeat("t", 500), heading); len(got) > 2*embedHeaderPartChars+10 {
		t.Errorf("the header must be clipped, got %d chars", len(got))
	}
}

// Chunks refused for their size are not an outage: the embedder answered.
// Five in a row once ended a pass as "endpoint likely down" against a server
// that was up; each is skipped and the pass goes on.
func TestChunksTooLargeDoNotStopTheRepair(t *testing.T) {
	calls := sizeLimitedEmbedServer(t, 0)
	db := &DBase{Store: kvlite.MemStore()}
	ids := []string{"a", "b", "c", "d", "e", "f", "g"}
	for _, id := range ids {
		db.Set(EmbeddedChunks, id, EmbeddedChunk{ID: id, Source: "kb", Text: "short " + id})
	}

	if n := ReembedUnvectoredChunks(context.Background(), db); n != 0 {
		t.Fatalf("nothing fits this embedder, repaired %d", n)
	}
	// The batch, then every chunk alone: none of them is a step toward the breaker.
	if int(*calls) != 1+len(ids) {
		t.Errorf("every chunk must be tried, the embedder saw %d calls for %d chunks", *calls, len(ids))
	}
}

// The give-ups for size say so, which is what keeps them off the breaker.
func TestASizeGiveUpIsMarked(t *testing.T) {
	sizeLimitedEmbedServer(t, 0)
	pieces := embedWithSplitFallback(context.Background(), GetEmbeddingConfig(), "## S", "tiny")
	if len(pieces) != 1 || len(pieces[0].Vector) != 0 || !pieces[0].TooLarge {
		t.Fatalf("a chunk refused for its size must come back marked, got %+v", pieces)
	}
}

// Splitting stops at its depth cap, and a piece given up there is marked as
// refused for its size like the rest.
func TestTheSplitDepthCapIsASizeGiveUp(t *testing.T) {
	sizeLimitedEmbedServer(t, 0)
	text := strings.Repeat("a sentence of filler. ", 3000) // big enough to reach the cap
	pieces := embedWithSplitFallback(context.Background(), GetEmbeddingConfig(), "## S", text)
	for i, p := range pieces {
		if !p.TooLarge {
			t.Fatalf("piece %d of %d was not marked as refused for its size", i+1, len(pieces))
		}
	}
}

// A PDF's pages go through the same clipped header as every other ingest.
func TestAPagedIngestClipsALongHeading(t *testing.T) {
	sizeLimitedEmbedServer(t, 600)
	db := &DBase{Store: kvlite.MemStore()}
	heading := "PYTORCH CODE " + strings.Repeat("model . train () ", 110)
	IngestPagedReport(context.Background(), db, "collection:t", "pdf", "## "+heading+"\n\noptim . step ()\n")
	rows := ChunksWhere(db, func(c EmbeddedChunk) bool { return c.ReportID == "pdf" })
	if len(rows) != 1 || len(rows[0].Vector) == 0 {
		t.Fatalf("the page must embed despite its long heading, got %d row(s)", len(rows))
	}
}
