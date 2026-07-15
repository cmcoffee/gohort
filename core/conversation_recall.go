// Conversation recall — the searchable counterpart to a rolling summary.
//
// An always-on conversation (a phantom chat, the Operator's thread) compacts
// older messages into a lossy narrative Summary for continuity. That summary
// can't hold a specific number, name, or exact thing said weeks ago — and it's
// a single point of failure (a biased or flooded summary drives every later
// turn). So as messages age out and fold into the summary, the caller ALSO
// folds the raw span into a per-conversation, private, SEARCHABLE store here.
// The agent reaches it on demand instead of trusting the summary.
//
// This is ONE mechanism with two surfaces: phantom (recall_conversation) and
// the Operator (recall_history / expand_history). Both route through these
// primitives so improvements — secret redaction, embedding-config gating,
// expand — land on both. Scoping is by `source`: a chunk Source tag unique to
// one conversation, so one contact's (or thread's) history never surfaces in
// another's recall.

package core

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// IngestRecallSpan folds a raw conversation span into the recall store under
// `source`. Lines that look like they carry a credential are redacted first —
// defense-in-depth so a secret typed into a chat doesn't land in the index.
// reportID should be unique per span (e.g. include the last message id) so
// successive folds accumulate distinct docs rather than overwriting. `kind`
// tags provenance ("phantom_convo", "lcm", …). Raw text, not distilled: the
// recall target is what was literally said, and the store is private to the
// conversation so there's no shared-corpus pollution risk.
//
// Returns an error when a non-empty span produced no stored chunks — callers
// that later DROP the raw messages on the assumption they're archived (the
// compaction cursor) must not advance past a failed archive. Nothing-to-do
// (nil db, empty span, fully-redacted body) is nil, not an error.
func IngestRecallSpan(ctx context.Context, db Database, source, reportID, title, body, kind string) error {
	if db == nil || strings.TrimSpace(reportID) == "" {
		return nil
	}
	body = RedactSecretLines(body)
	if strings.TrimSpace(body) == "" {
		return nil
	}
	if rows := IngestReportTitled(ctx, db, source, reportID, title, body, kind); rows == 0 {
		return fmt.Errorf("recall span %s: no chunks stored", reportID)
	}
	return nil
}

// RedactSecretLines replaces any line of s that contains a secret-shaped value
// (Bearer tokens, key=… / token: … forms) with a placeholder, leaving the rest
// intact. Line-granular so one leaked credential doesn't blank a whole span.
func RedactSecretLines(s string) string {
	if !ContainsLikelySecret(s) {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		if ContainsLikelySecret(ln) {
			lines[i] = "[redacted: possible secret omitted from recall index]"
		}
	}
	return strings.Join(lines, "\n")
}

// SearchRecall searches a conversation's recall store (scoped by source):
// semantic when embeddings are enabled (with substring fallback if it returns
// nothing), substring otherwise. Returns up to k hits.
func SearchRecall(db Database, source, query string, k int) []SearchHit {
	var vec []float32
	if GetEmbeddingConfig().Enabled && strings.TrimSpace(query) != "" {
		// Bounded like every other query-embed path (SearchMemoryFacts et al):
		// a stalled embed server must degrade recall to keyword-only, not hang
		// the turn indefinitely.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if v, err := Embed(ctx, query); err == nil && len(v) > 0 {
			vec = v
		}
	}
	return SearchRecallVec(db, source, query, vec, k)
}

// SearchRecallVec is SearchRecall with a caller-supplied query embedding, so a
// multi-layer caller embeds once for facts + chunks + history instead of one
// serial GPU round-trip per layer. Empty vec = keyword-only (hybrid handles it).
func SearchRecallVec(db Database, source, query string, vec []float32, k int) []SearchHit {
	if db == nil || strings.TrimSpace(source) == "" || strings.TrimSpace(query) == "" || k <= 0 {
		return nil
	}
	allow := func(c EmbeddedChunk) bool { return c.Source == source }
	// Hybrid: vector + keyword, merged — so an exact term the embedding misses
	// still surfaces. Hybrid handles the no-embedding case (keyword only).
	return HybridSearchByPredicate(db, allow, query, vec, k)
}

// FetchRecallSpanChunks returns every chunk for one (source, reportID) span so a
// caller can reassemble the full exchange (e.g. via AssembleChunkDoc) for an
// "expand" view.
func FetchRecallSpanChunks(db Database, source, reportID string) []EmbeddedChunk {
	if db == nil || source == "" || reportID == "" {
		return nil
	}
	return ChunksWhere(db, func(c EmbeddedChunk) bool {
		return c.Source == source && c.ReportID == reportID
	})
}
