// tool_index.go — semantic search over tool descriptions.
//
// Solves the LLM catalog-saturation problem: when an agent has 40+
// tools, the LLM struggles to pick the right one for any given
// question, and most tools are irrelevant to most turns. Instead of
// admin-authored static groupings (the previous ToolGroup approach),
// the runtime embeds each tool's description once, then for each
// turn embeds the user's message and surfaces only the top-K
// closest-matching tools.
//
// Index lifecycle:
//
//   - First call lazily builds the index by walking
//     RegisteredChatTools() and embedding each Desc(). Subsequent
//     calls hit the cache.
//   - InvalidateToolIndex() forces a rebuild. Called when temp tools
//     register / deregister so the index stays in sync.
//   - Embedding failures don't fail the lookup — the affected tool
//     just doesn't surface from the classifier (it can still be
//     called directly when listed in agent.AllowedTools).
//
// Match cost per turn: one Embed() of the user message + cosine
// sweep across the index. Both cheap (microseconds for cosine; the
// embed model serves the query in tens of ms on local hardware).

package core

import (
	"context"
	"sort"
	"sync"
)

// ToolMatch is one classifier hit: a tool name + its cosine similarity
// to the user's query. Score is in [-1, 1]; in practice cosine on
// normalized embeddings lands in [0, 1] for any plausibly related
// pair. Threshold filtering is left to the caller.
type ToolMatch struct {
	Name        string
	Description string
	Score       float32
}

// toolIndexEntry is one row of the index. Description is cached
// so the classifier can return it without re-fetching the tool;
// callers building "Available tools" hints can use it directly.
type toolIndexEntry struct {
	Name        string
	Description string
	Embedding   []float32
}

var (
	toolIndexMu      sync.RWMutex
	toolIndexEntries []toolIndexEntry
	toolIndexBuilt   bool
)

// InvalidateToolIndex marks the index as stale so the next lookup
// rebuilds it. Call when the set of registered tools changes
// (temp-tool register / deregister, RegisterChatTool after startup).
// Cheap — just flips a flag; the rebuild itself runs lazily on the
// next classifier call.
func InvalidateToolIndex() {
	toolIndexMu.Lock()
	toolIndexBuilt = false
	toolIndexMu.Unlock()
}

// ensureToolIndex builds the index if it hasn't been built (or
// has been invalidated). Safe to call from any goroutine — the
// rebuild holds the write lock for its duration; concurrent callers
// queue then see the populated index.
func ensureToolIndex(ctx context.Context) {
	toolIndexMu.RLock()
	built := toolIndexBuilt
	toolIndexMu.RUnlock()
	if built {
		return
	}
	toolIndexMu.Lock()
	defer toolIndexMu.Unlock()
	// Re-check under the write lock — another goroutine may have
	// built it between our RUnlock and Lock.
	if toolIndexBuilt {
		return
	}
	cfg := GetEmbeddingConfig()
	if !cfg.Enabled {
		// No embedder configured. Mark as built (empty) so we don't
		// retry on every call; callers fall back to other selection
		// strategies.
		toolIndexEntries = nil
		toolIndexBuilt = true
		Log("[tool_index] embeddings disabled — classifier-trim is a no-op (worker catalog stays full size)")
		return
	}
	tools := RegisteredChatTools()
	entries := make([]toolIndexEntry, 0, len(tools))
	skippedFramework := 0
	skippedEmbed := 0
	for _, t := range tools {
		// Framework tools don't benefit from classifier surfacing —
		// they're either always-on or never user-facing. Skip to keep
		// the index focused on capability tools.
		if IsFrameworkTool(t) {
			skippedFramework++
			continue
		}
		desc := t.Desc()
		if desc == "" {
			continue
		}
		// Embed the description. Failure for one tool doesn't fail
		// the whole index — it just can't surface from classifier
		// hits this run.
		vec, err := Embed(ctx, t.Name()+": "+desc)
		if err != nil || len(vec) == 0 {
			Log("[tool_index] embed failed for %q: %v", t.Name(), err)
			skippedEmbed++
			continue
		}
		entries = append(entries, toolIndexEntry{
			Name:        t.Name(),
			Description: desc,
			Embedding:   vec,
		})
	}
	toolIndexEntries = entries
	toolIndexBuilt = true
	Log("[tool_index] built: %d tools indexed (skipped %d framework, %d embed-failed)", len(entries), skippedFramework, skippedEmbed)
}

// RelevantTools returns the top-K tools whose description embeddings
// most closely match the query, filtered by threshold. Empty query
// or unavailable embedder = empty slice (no error — caller can
// degrade gracefully). The cosine sweep is over a small index so
// the cost is microseconds; the embed of the query is what
// dominates (~10ms local).
//
// k caps the result count. threshold (cosine similarity, typically
// 0.30-0.45) filters out weak matches; pass 0 to disable filtering.
func RelevantTools(ctx context.Context, query string, k int, threshold float32) []ToolMatch {
	if query == "" || k <= 0 {
		return nil
	}
	ensureToolIndex(ctx)
	toolIndexMu.RLock()
	entries := toolIndexEntries
	toolIndexMu.RUnlock()
	if len(entries) == 0 {
		return nil
	}
	qvec, err := Embed(ctx, query)
	if err != nil || len(qvec) == 0 {
		Debug("[tool_index] query embed failed: %v", err)
		return nil
	}
	scored := make([]ToolMatch, 0, len(entries))
	for _, e := range entries {
		s := Cosine(qvec, e.Embedding)
		if s < threshold {
			continue
		}
		scored = append(scored, ToolMatch{
			Name:        e.Name,
			Description: e.Description,
			Score:       s,
		})
	}
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})
	if len(scored) > k {
		scored = scored[:k]
	}
	return scored
}

// LookupToolsByQuery is the public API for the LLM-callable
// find_tools tool. Same semantics as RelevantTools but returns a
// minimum of 1 result when ANY match exists above threshold/2 —
// when the user explicitly searches, we'd rather surface SOMETHING
// than report "no matches" on a barely-missed cutoff.
func LookupToolsByQuery(ctx context.Context, query string, k int) []ToolMatch {
	const threshold = 0.30
	hits := RelevantTools(ctx, query, k, threshold)
	if len(hits) > 0 {
		return hits
	}
	// Fallback pass with relaxed threshold so an explicit query
	// always returns something useful (or genuinely nothing if the
	// store has no tools indexed).
	return RelevantTools(ctx, query, k, threshold/2)
}
