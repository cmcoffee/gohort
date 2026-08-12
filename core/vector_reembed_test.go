package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
		if f.Key == "reembed_unvectored_chunks" {
			if f.Label == "" || f.Desc == "" {
				t.Errorf("maintenance entry needs a label and description, got %+v", f)
			}
			return
		}
	}
	t.Fatal("reembed_unvectored_chunks is not registered — the admin panel has no way to run it")
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
