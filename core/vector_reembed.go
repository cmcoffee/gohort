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

// ReembedUnvectoredChunks walks the chunk store and re-embeds every row that
// has text but no vector, writing the vector and the CURRENT model name back
// in place. Returns the number of rows repaired.
//
// Rewriting Model matters as much as writing Vector: chunkVectorComparable
// gates a chunk on c.Model matching the configured model, so a row repaired
// under a new model while still carrying the old model string would score as
// though it were never fixed.
func ReembedUnvectoredChunks(ctx context.Context, db Database) int {
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

	keys := db.Keys(EmbeddedChunks)
	var scanned, candidates, fixed, failed, streak int
	started := time.Now()

	for _, key := range keys {
		if ctx.Err() != nil {
			Log("[vector-reembed] cancelled after %d repaired", fixed)
			break
		}
		var c EmbeddedChunk
		if !db.Get(EmbeddedChunks, key, &c) {
			continue
		}
		scanned++
		if len(c.Vector) > 0 || c.Text == "" {
			continue
		}
		candidates++

		// Same prompt shape as ingest (embedWithSplitFallbackDepth), so a
		// repaired row lands in the same space as one embedded first time.
		ectx, cancel := context.WithTimeout(ctx, reembedChunkTimeout)
		v, err := Embed(ectx, c.Section+"\n\n"+c.Text)
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
		c.Model = cfg.Model
		db.Set(EmbeddedChunks, key, c)
		fixed++
	}

	if fixed > 0 {
		// The chunk cache holds the pre-repair rows; without this the new
		// vectors don't reach search until something else invalidates it.
		invalidateChunkCacheFor(db)
	}
	if candidates == 0 {
		Log("[vector-reembed] scanned %d chunk(s); none are missing a vector", scanned)
		return 0
	}
	Log("[vector-reembed] scanned %d chunk(s), %d missing a vector: %d repaired, %d still failing, %.1fs",
		scanned, candidates, fixed, failed, time.Since(started).Seconds())
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
	RegisterMaintenanceFunc(
		"reembed_unvectored_chunks",
		"Re-embed chunks missing a vector",
		"Repairs the \"Empty (embed failed)\" count above. Re-embeds every indexed chunk "+
			"that has text but no vector — the rows left behind when the embedding endpoint "+
			"was unreachable at ingest time, which keyword search can still find but semantic "+
			"search cannot see. Rewrites each repaired row with the CURRENT embedding model. "+
			"Stops early if the endpoint is still down; safe to re-run.",
		func(ctx context.Context) int {
			return ReembedUnvectoredChunks(ctx, vectorRepairDB())
		},
	)
}
