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
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/gohort/core/textutil"
)

// reembedFailStreak is how many consecutive embed failures end the pass.
//
// The failure this repairs is usually "the embedder was down", and the operator
// most likely to press the button is one who hasn't noticed it still is. With no
// breaker, a 10k-row backlog against a dead endpoint is 10k requests that each
// wait out the 60s client ceiling — days of pointless retry that also looks
// identical, from the admin UI, to a pass that is merely slow. Five in a row is
// enough to tell a dead endpoint from an individual chunk the embedder
// dislikes. A chunk refused for its size is not counted at all: the embedder
// answered, and five such chunks in a row once ended a pass as "endpoint
// likely down" against a server that was up.
const reembedFailStreak = 5

// reembedChunkTimeout bounds a single chunk's embed. The shared client ceiling
// is 60s, which is a sane bulk-ingest budget but far too long here: this pass
// walks an unbounded backlog, so a slow endpoint has to fail fast enough that
// the breaker can trip while the operator is still watching.
const reembedChunkTimeout = 20 * time.Second

// reembedBatchSize and reembedBatchChars bound one request of the pass: as
// many chunks as a peer instance takes in one call (maxPeerEmbedInputs is 64),
// halved for headroom, and a text budget so a batch of long chunks stays a
// request any embedder accepts.
const (
	reembedBatchSize  = 32
	reembedBatchChars = 32000
)

// reembedBatchTimeout bounds one batch's request: a chunk's budget for the
// first few, since the embedder works through them in one go.
const reembedBatchTimeout = 90 * time.Second

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
		Log("[vector-reembed] embeddings are disabled: nothing to do")
		return 0
	}
	if cfg.Endpoint == "" {
		Log("[vector-reembed] no embedding endpoint configured: nothing to do")
		return 0
	}
	space := cfg.spaceStamp()

	keys := db.Keys(EmbeddedChunks)
	var scanned, candidates, fixed, failed, streak, split int
	started := time.Now()
	// A full store takes minutes even in batches, so
	// it says where it is. Reported on a tick rather than per chunk: the
	// admin panel reads the latest line, and writing one per row would be
	// lock traffic nobody sees.
	ReportMaintenanceProgress(ctx, fmt.Sprintf("starting: %d chunk(s) to check", len(keys)))
	lastReport := time.Now()

	url := embedPauseAddress(cfg)
	// waitOut waits out a pause the embedder asked for (a peer key's per
	// minute ceiling), with no chunk's deadline running and the wait on the
	// progress line. False means the pass was cancelled.
	waitOut := func() bool {
		d := media.PausedFor(url)
		if d <= 0 {
			return true
		}
		ReportMaintenanceProgress(ctx, fmt.Sprintf("%d of %d chunk(s) checked - %d re-embedded - the embedder is rate limiting, waiting %s",
			scanned, len(keys), fixed, d.Round(time.Second)))
		if media.WaitPause(ctx, url) != nil {
			return false
		}
		lastReport = time.Now()
		return true
	}
	type pending struct {
		key string
		c   EmbeddedChunk
	}
	// repairOne is one chunk through the path ingest uses, split fallback
	// included, and reports whether the pass has to stop: cancelled, or the
	// breaker tripped. A chunk refused for the rate, not itself, goes again
	// once the pause is over and is no step toward the breaker.
	repairOne := func(p pending) (stop bool) {
		for {
			if !waitOut() {
				return true
			}
			// The SAME embed path ingest uses, split fallback included. This
			// used to call the embedder once, which meant a chunk too large
			// for it could never be repaired: such rows exist precisely
			// because the embedder was unreachable at ingest (a non-size
			// error bails without splitting, storing the whole text raw).
			ectx, cancel := context.WithTimeout(ctx, reembedChunkTimeout)
			pieces := embedWithSplitFallback(ectx, cfg, embedHeader(p.c.Title, p.c.Section), p.c.Text)
			cancel()
			if len(pieces) == 0 || len(pieces[0].Vector) == 0 {
				if ctx.Err() != nil {
					return true
				}
				if media.PausedFor(url) > 0 {
					continue
				}
				failed++
				// Refused for its size: the embedder answered, so this is no
				// step toward "the endpoint is down".
				if len(pieces) > 0 && pieces[0].TooLarge {
					Log("[vector-reembed] %s/%s section %q is too large for %s even split; skipped",
						p.c.Source, p.c.ReportID, textutil.Clip(p.c.Section, 80), cfg.Endpoint)
					return false
				}
				streak++
				Debug("[vector-reembed] %s/%s section %q failed", p.c.Source, p.c.ReportID, p.c.Section)
				if streak >= reembedFailStreak {
					Log("[vector-reembed] stopping, %d consecutive failures (endpoint likely down: %s). Repaired %d before the streak; re-run once the embedder is back.",
						streak, cfg.Endpoint, fixed)
					ReportMaintenanceFailure(ctx, fmt.Sprintf("stopped after %d failures in a row (the embedder at %s is likely down); repaired %d before that", streak, cfg.Endpoint, fixed))
					return true
				}
				return false
			}
			streak = 0
			// One piece is the ordinary case and the row keeps its identity.
			// Several means the text only embeds in halves, so the row becomes
			// the rows ingest would have written: same parent, same position,
			// each tagged "(part i/N)", which SortChunksForAssembly orders
			// within the position they share.
			base := p.c.Section
			for i, pc := range pieces {
				row := p.c
				row.Vector, row.Model, row.Text = pc.Vector, space, pc.Text
				if len(pieces) > 1 {
					row.Section = fmt.Sprintf("%s (part %d/%d)", base, i+1, len(pieces))
				}
				if i == 0 {
					db.Set(EmbeddedChunks, p.key, row) // in place, keeping its ID
				} else {
					row.ID = UUIDv4()
					db.Set(EmbeddedChunks, row.ID, row)
					split++
				}
				fixed++
			}
			return false
		}
	}
	// Chunks go to the embedder reembedBatchSize at a time, one request for
	// the lot: a pass of one request a chunk took a 10k backlog through a
	// peer key at its 600 a minute. A batch the embedder will not take whole
	// (a backend that embeds one input a request, an oversized chunk, a short
	// answer) turns batching off for the rest of the pass, and its chunks and
	// every later one go one at a time, where splitting and the breaker work
	// as they always have.
	var batch []pending
	batchChars, batching := 0, true
	flush := func() (stop bool) {
		defer func() { batch, batchChars = nil, 0 }()
		for len(batch) > 0 && batching {
			if !waitOut() {
				return true
			}
			prompts := make([]string, len(batch))
			for i, p := range batch {
				prompts[i] = cfg.DocPrefix + embedHeader(p.c.Title, p.c.Section) + "\n\n" + p.c.Text
			}
			bctx, cancel := context.WithTimeout(ctx, reembedBatchTimeout)
			vecs, err := embedTexts(bctx, cfg, prompts)
			cancel()
			if err == nil {
				for i, p := range batch {
					row := p.c
					row.Vector, row.Model = vecs[i], space
					db.Set(EmbeddedChunks, p.key, row) // in place, keeping its ID
					fixed++
				}
				streak = 0
				return false
			}
			if ctx.Err() != nil {
				return true
			}
			if media.PausedFor(url) > 0 {
				continue // the same batch again once the pause is over
			}
			batching = false
			Log("[vector-reembed] %s would not embed %d chunk(s) in one request (%v): going one at a time", cfg.Endpoint, len(batch), err)
		}
		for _, p := range batch {
			if repairOne(p) {
				return true
			}
		}
		return false
	}

	stopped := false
	for _, key := range keys {
		if ctx.Err() != nil {
			stopped = true
			break
		}
		if time.Since(lastReport) >= reembedReportEvery {
			ReportMaintenanceProgress(ctx, fmt.Sprintf("%d of %d chunk(s) checked - %d re-embedded - %s elapsed",
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
		if !batching {
			if stopped = repairOne(pending{key, c}); stopped {
				break
			}
			continue
		}
		batch = append(batch, pending{key, c})
		batchChars += len(c.Text)
		if len(batch) >= reembedBatchSize || batchChars >= reembedBatchChars {
			if stopped = flush(); stopped {
				break
			}
		}
	}
	if !stopped && len(batch) > 0 {
		stopped = flush()
	}
	if ctx.Err() != nil {
		Log("[vector-reembed] cancelled after %d repaired", fixed)
	}

	if fixed > 0 {
		// The chunk cache holds the pre-repair rows; without this the new
		// vectors don't reach search until something else invalidates it.
		invalidateChunkCacheFor(db)
	}
	if candidates == 0 {
		ReportMaintenanceOutcome(ctx, fmt.Sprintf("%d chunk(s) checked - none %s", scanned, what))
		Log("[vector-reembed] scanned %d chunk(s); none %s", scanned, what)
		return 0
	}
	// The pass's own last word. Without it the row's final reading is whichever
	// two-second tick landed last, which can sit a few chunks short of the end
	// and read as though the pass stopped early — it does not: the loop is
	// sequential, each embed completes and its row is written before the next.
	outcome := fmt.Sprintf("%d chunk(s) checked - %d re-embedded", scanned, fixed)
	if split > 0 {
		outcome += fmt.Sprintf(" - %d oversized split into parts", split)
	}
	if failed > 0 {
		outcome += fmt.Sprintf(" - %d still failing", failed)
	}
	ReportMaintenanceOutcome(ctx, outcome+" - "+time.Since(started).Round(time.Second).String())
	Log("[vector-reembed] scanned %d chunk(s), %d %s: %d repaired, %d split, %d still failing, %.1fs",
		scanned, candidates, what, fixed, split, failed, time.Since(started).Seconds())
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

// RemoveUnusableChunks deletes every chunk with no TEXT.
//
// Such a row can never be repaired (there is nothing to embed) and can never
// be returned (nothing for keyword search to match, and a hit would carry an
// empty body), so it sat in the counts forever looking like a gap the repair
// pass was failing to close. It is not a gap; it is dead weight. Returns the
// number removed.
func RemoveUnusableChunks(ctx context.Context, db Database) int {
	if db == nil {
		return 0
	}
	// Once stopped, nothing more is chosen: the rest of the walk is over an
	// in-memory snapshot and ends at once.
	removed := DeleteChunksWhere(db, func(c EmbeddedChunk) bool {
		return ctx.Err() == nil && strings.TrimSpace(c.Text) == ""
	})
	if ctx.Err() != nil {
		ReportMaintenanceOutcome(ctx, fmt.Sprintf("stopped part way, %d unusable chunk(s) removed by then", removed))
		Log("[vector-reembed] stopped; removed %d chunk(s) with no text", removed)
		return removed
	}
	ReportMaintenanceOutcome(ctx, fmt.Sprintf("%d unusable chunk(s) removed", removed))
	Log("[vector-reembed] removed %d chunk(s) with no text", removed)
	return removed
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
		"Re-embeds every indexed chunk, current or not. Only for a change the stamp cannot see: "+
			"the same model name now served by a different endpoint, build or quantization, which is "+
			"a different space with the same name. Otherwise use Repair, which skips what is already "+
			"right. One embed call per chunk; stops early if the endpoint is down; safe to re-run.",
		func(ctx context.Context) int {
			return ReembedAllChunks(ctx, vectorRepairDB())
		},
	)
	RegisterMaintenanceFunc("Vector index",
		"vector_database_cleanup",
		"Vector database cleanup (DELETES)",
		"Removes rows the index cannot use: a chunk with no text. Nothing to embed, so Repair "+
			"cannot fix it; nothing to match, so search cannot return it. They are dead weight that "+
			"kept the counts above from reaching zero. Permanent, and safe: the documents they came "+
			"from are untouched and can be re-ingested.",
		func(ctx context.Context) int {
			return RemoveUnusableChunks(ctx, vectorRepairDB())
		},
	)
}

// --- upkeep: the vector repairs, run without anybody pressing them -----------
//
// Two passes keep the index whole: the cleanup (a chunk with no text, which no
// repair can fix) and the repair (a chunk search cannot see, stored while the
// embedder was down or embedded in another space). Both were buttons, so a
// chunk stored while the embedder was down stayed out of semantic search until
// somebody noticed and pressed Repair. They now run on their own, through the
// same runner as the buttons, so a run shows its progress, can be stopped, and
// a press while it runs joins it:
//
//   - nightly, once in the chosen hour of the deployment's day (3am unless
//     set), when the store is quiet. With nothing to fix each is a scan of the cached chunk
//     list.
//   - soon after the embedder answers again, when a write since the last run
//     stored a chunk with text and no vector: the chunks an outage left behind
//     are back in search in minutes, not by the next night.

// TunableVectorUpkeep switches the automatic runs off; the buttons stay.
// TunableVectorUpkeepHour is the hour of the deployment's day the nightly run
// starts.
const (
	TunableVectorUpkeep     = "tune_vector_upkeep"
	TunableVectorUpkeepHour = "tune_vector_upkeep_hour"
)

// vectorRepairDelay is how long after the embedder answers again the repair
// starts: long enough for a backend that just came back to settle, and for an
// ingest in flight to finish its own embeds first.
var vectorRepairDelay = 2 * time.Minute

var vectorUpkeep struct {
	mu      sync.Mutex
	lastDay string // the deployment day of the last nightly run
	pending bool   // a write stored a chunk with text and no vector
}

func init() {
	RegisterTunable(TunableSpec{Key: TunableVectorUpkeep, Category: "Retrieval", Label: "Repair the vector index on its own",
		Help: "Each night, and a few minutes after the embedder comes back from an outage, remove chunks with no text and re-embed the chunks search cannot see.",
		Kind: KindBool, Default: 1, Min: 0, Max: 1})
	RegisterTunable(TunableSpec{Key: TunableVectorUpkeepHour, Category: "Retrieval", Label: "Hour the vector index is repaired",
		Help: "The hour of the night (0-23, in the deployment's time zone) the nightly repair runs. Pick one when the box is quiet.",
		Kind: KindInt, Default: 3, Min: 0, Max: 23})
	// The reconciler ticker is where the framework already runs periodic
	// upkeep (image_reap, managed_workspace_cleanup); the work is the check.
	RegisterReconciler("vector_upkeep", func(ctx context.Context) error {
		if TuneBool(TunableVectorUpkeep) && dueNightlyVectorUpkeep(time.Now()) {
			go runVectorUpkeep("nightly")
		}
		return nil
	})
}

// dueNightlyVectorUpkeep reports whether now is in the upkeep hour of a
// deployment day the nightly run has not yet had, and claims the day when it
// is, so two reconciler passes in the hour cannot both run it.
func dueNightlyVectorUpkeep(now time.Time) bool {
	local := now.In(time.Local)
	if local.Hour() != TuneInt(TunableVectorUpkeepHour) {
		return false
	}
	day := local.Format("2006-01-02")
	vectorUpkeep.mu.Lock()
	defer vectorUpkeep.mu.Unlock()
	if vectorUpkeep.lastDay == day {
		return false
	}
	vectorUpkeep.lastDay = day
	return true
}

// noteChunksWithoutVectors marks a repair as wanted when rows hold text the
// embedder did not turn into a vector, with an embedder set up to repair them.
func noteChunksWithoutVectors(rows []EmbeddedChunk) {
	for _, r := range rows {
		if len(r.Vector) == 0 && strings.TrimSpace(r.Text) != "" {
			if !GetEmbeddingConfig().Enabled {
				return
			}
			vectorUpkeep.mu.Lock()
			vectorUpkeep.pending = true
			vectorUpkeep.mu.Unlock()
			return
		}
	}
}

// vectorEmbedderAnswered is told of every embed that succeeded. When a repair
// is wanted it sets one to start vectorRepairDelay later, and the want is
// spent; a run already going when it starts is joined, not doubled
// (RunMaintenanceFunc).
func vectorEmbedderAnswered() {
	vectorUpkeep.mu.Lock()
	if !vectorUpkeep.pending || !TuneBool(TunableVectorUpkeep) {
		vectorUpkeep.mu.Unlock()
		return
	}
	vectorUpkeep.pending = false
	vectorUpkeep.mu.Unlock()
	time.AfterFunc(vectorRepairDelay, func() {
		runVectorUpkeep("the embedder answered again after chunks were stored without vectors")
	})
}

// RepairAfterSpaceChange starts the vector repair when a saved embedding
// config put the stored vectors in another space: another model, another
// document prefix. Until then semantic search skipped every chunk made in the
// old one, and nothing said so but the counts in the Vector Index panel. It
// runs as the nightly repair does, through the maintenance runner (progress,
// Stop, a page arriving mid-run rejoins it), and only when the automatic
// repairs are on and the new config embeds at all. Reports whether it started.
func RepairAfterSpaceChange(prev, next EmbeddingConfig) bool {
	if !next.Enabled || !TuneBool(TunableVectorUpkeep) || prev.spaceStamp() == next.spaceStamp() {
		return false
	}
	// The built-in embedder still downloading would fail every chunk: its
	// download starts the repair when it ends (runBuiltinEmbedderDownload).
	if next.Provider == EmbeddingProviderBuiltin && !BuiltinEmbedderReady() {
		startBuiltinEmbedderDownload()
		return false
	}
	startVectorUpkeep("the embedding settings moved stored vectors to another space")
	return true
}

// startVectorUpkeep runs the upkeep in the background; tests replace it.
var startVectorUpkeep = func(why string) { go runVectorUpkeep(why) }

// runVectorUpkeep runs the cleanup, then the repair, through the maintenance
// runner, on a context of its own (it outlives whatever noticed it was due),
// and says what they did.
func runVectorUpkeep(why string) {
	ctx := context.WithoutCancel(AppContext())
	removed := RunMaintenanceFunc(ctx, "vector_database_cleanup")
	repaired := RunMaintenanceFunc(ctx, "reembed_stale_chunks")
	if removed <= 0 && repaired <= 0 {
		Log("[vector-upkeep] %s: the index is whole, nothing to do", why)
		return
	}
	Log("[vector-upkeep] %s: removed %d unusable chunk(s), re-embedded %d", why, max(removed, 0), max(repaired, 0))
}
