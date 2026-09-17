package core

// Repair pass for chunks that were stored WITHOUT a vector.
//
// Ingest never rejects a chunk whose embed call failed — IngestReportTitled
// writes the row with an empty Vector so the text is still there for keyword
// search. That is the right call at ingest time (losing the document would be
// worse than losing its vector), but nothing ever went back for those rows.
// An embedder that was down for an afternoon left every document ingested in
// that window permanently invisible to semantic search, findable only by exact
// term. The admin Vector Index panel has always shown the count; until now
// there was nothing to do about it.

import (
	"context"
	"fmt"
	"time"
)

// reembedFailStreak is how many consecutive embed failures end the pass.
//
// The failure this repairs is usually "the embedder was down", and the operator
// most likely to press the button is one who hasn't noticed it still is. With no
// breaker, a 10k-row backlog against a dead endpoint is 10k requests that each
// wait out the 60s client ceiling — days of pointless retry that also looks
// identical, from the admin UI, to a pass that is merely slow. Five in a row is
// enough to distinguish a dead endpoint from an individual chunk the embedder
// dislikes (an oversized one, which fails and is skipped without ending the run).
const reembedFailStreak = 5

// reembedChunkTimeout bounds a single chunk's embed. The shared client ceiling
// is 60s, which is a sane bulk-ingest budget but far too long here: this pass
// walks an unbounded backlog, so a slow endpoint has to fail fast enough that
// the breaker can trip while the operator is still watching.
const reembedChunkTimeout = 20 * time.Second

// reembedReportEvery is how often the pass says where it is. Often enough
// that a watching operator sees movement, rare enough that the report is not
// the work.
const reembedReportEvery = 2 * time.Second

// ReembedUnvectoredChunks re-embeds every row that has text but no vector.
// Returns the number of rows repaired.
func ReembedUnvectoredChunks(ctx context.Context, db Database) int {
	return reembedChunks(ctx, db, "missing a vector", func(c EmbeddedChunk, _ string) bool {
		return len(c.Vector) == 0
	})
}

// ReembedStaleChunks re-embeds every row whose vector is not in the CURRENT
// embedding space — stamped with a different model, a different document
// prefix, or (legacy rows) no stamp at all — plus any row with no vector.
// This is the pass to run after changing the embedding model or setting a
// document prefix: until it runs, those rows are skipped by semantic search
// (chunkVectorComparable) or, for the unstamped ones, compared across spaces.
func ReembedStaleChunks(ctx context.Context, db Database) int {
	return reembedChunks(ctx, db, "outside the current embedding space", func(c EmbeddedChunk, space string) bool {
		return len(c.Vector) == 0 || c.Model != space
	})
}

// ReembedAllChunks re-embeds every row that has text, current or not. The
// pass for a change the stamp cannot see: the same model name served by a
// different endpoint or build, which is a different space with the same name.
func ReembedAllChunks(ctx context.Context, db Database) int {
	return reembedChunks(ctx, db, "in the store", func(EmbeddedChunk, string) bool { return true })
}

// reembedChunks walks the chunk store and re-embeds every row with text that
// want accepts, writing the vector and the CURRENT space stamp back in place.
// what names the selection in the log. Returns the number of rows repaired.
//
// Rewriting Model matters as much as writing Vector: chunkVectorComparable
// gates a chunk on c.Model matching the configured space, so a row repaired
// under a new model while still carrying the old stamp would score as though
// it were never fixed.
//
// Walks kvlite by key rather than the cache snapshot on purpose: a row
// re-ingested while the pass runs is re-read fresh, where a snapshot would
// write its pre-ingest text back over the new one.
func reembedChunks(ctx context.Context, db Database, what string, want func(c EmbeddedChunk, space string) bool) int {
	if db == nil {
		return 0
	}
	cfg := GetEmbeddingConfig()
	if !cfg.Enabled {
		Log("[vector-reembed] embeddings are disabled — nothing to do")
		return 0
	}
	if cfg.Endpoint == "" {
		Log("[vector-reembed] no embedding endpoint configured — nothing to do")
		return 0
	}
	space := cfg.spaceStamp()

	keys := db.Keys(EmbeddedChunks)
	var scanned, candidates, fixed, failed, streak int
	started := time.Now()
	// This pass embeds one chunk at a time and a full store takes minutes, so
	// it says where it is. Reported on a tick rather than per chunk: the
	// admin panel reads the latest line, and writing one per row would be
	// lock traffic nobody sees.
	ReportMaintenanceProgress(ctx, fmt.Sprintf("starting — %d chunk(s) to check", len(keys)))
	lastReport := time.Now()

	for _, key := range keys {
		if ctx.Err() != nil {
			Log("[vector-reembed] cancelled after %d repaired", fixed)
			break
		}
		if time.Since(lastReport) >= reembedReportEvery {
			ReportMaintenanceProgress(ctx, fmt.Sprintf("%d of %d chunk(s) checked · %d re-embedded · %s elapsed",
				scanned, len(keys), fixed, time.Since(started).Round(time.Second)))
			lastReport = time.Now()
		}
		var c EmbeddedChunk
		if !db.Get(EmbeddedChunks, key, &c) {
			continue
		}
		scanned++
		if c.Text == "" || !want(c, space) {
			continue
		}
		candidates++

		// Same prompt shape as ingest (embedWithSplitFallbackDepth), so a
		// repaired row lands in the same space as one embedded first time.
		ectx, cancel := context.WithTimeout(ctx, reembedChunkTimeout)
		v, err := embedDocumentWith(ectx, cfg, embedHeader(c.Title, c.Section)+"\n\n"+c.Text)
		cancel()
		if err != nil || len(v) == 0 {
			failed++
			streak++
			Debug("[vector-reembed] %s/%s section %q failed: %v", c.Source, c.ReportID, c.Section, err)
			if streak >= reembedFailStreak {
				Log("[vector-reembed] stopping — %d consecutive failures (endpoint likely down: %s). Repaired %d before the streak; re-run once the embedder is back.",
					streak, cfg.Endpoint, fixed)
				break
			}
			continue
		}
		streak = 0
		c.Vector = v
		c.Model = space
		db.Set(EmbeddedChunks, key, c)
		fixed++
	}

	if fixed > 0 {
		// The chunk cache holds the pre-repair rows; without this the new
		// vectors don't reach search until something else invalidates it.
		invalidateChunkCacheFor(db)
	}
	if candidates == 0 {
		Log("[vector-reembed] scanned %d chunk(s); none %s", scanned, what)
		return 0
	}
	Log("[vector-reembed] scanned %d chunk(s), %d %s: %d repaired, %d still failing, %.1fs",
		scanned, candidates, what, fixed, failed, time.Since(started).Seconds())
	return fixed
}

// vectorRepairDB picks the store the chunks actually live in, matching the
// admin stats endpoint: the dedicated vector store when it's split out, the
// main database when it isn't.
func vectorRepairDB() Database {
	if VectorDB != nil {
		return VectorDB
	}
	return RootDB
}

func init() {
	// One button repairs both counts above: a chunk with no vector and a chunk
	// whose vector is from another space are the same problem to the operator
	// (semantic search cannot see it) and the same fix. The missing-vector
	// pass remains callable for tests and tooling; it does not need a button.
	RegisterMaintenanceFunc("Vector index",
		"reembed_stale_chunks",
		"Repair the vector index",
		"Re-embeds every chunk semantic search cannot see: the \"Empty (embed failed)\" rows "+
			"left when the embedder was down at ingest, and the \"In another embedding space\" rows "+
			"whose vector was made under a different model or document prefix (or has no stamp). "+
			"Rewrites each with the CURRENT space. One embed call per chunk, so a large store takes "+
			"a while; stops early if the endpoint is down; safe to re-run, and a second run finds "+
			"nothing to do.",
		func(ctx context.Context) int {
			return ReembedStaleChunks(ctx, vectorRepairDB())
		},
	)
	RegisterMaintenanceFunc("Vector index",
		"reembed_all_chunks",
		"Re-embed EVERY chunk",
		"Re-embeds every indexed chunk, current or not. Only for a change the stamp cannot see — "+
			"the same model name now served by a different endpoint, build or quantization, which is "+
			"a different space with the same name. Otherwise use Repair, which skips what is already "+
			"right. One embed call per chunk; stops early if the endpoint is down; safe to re-run.",
		func(ctx context.Context) int {
			return ReembedAllChunks(ctx, vectorRepairDB())
		},
	)
}
