package core

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// chunkCache holds a snapshot of every EmbeddedChunk row from a single
// kvlite Database, so SearchChunks/SearchChunksSubstring can scan a Go
// slice instead of re-deserializing every chunk from kvlite per query.
// Lazy-loaded on first read; invalidated on every IngestReport /
// DeleteReportChunks so the next read rebuilds. Keyed by the Database
// pointer so a future multi-bucket setup just rebuilds when the bucket
// changes — no correctness risk, only the rebuild cost.
//
// At gohort scale (thousands of chunks, low write rate, interactive
// reads) invalidate-on-write + lazy-rebuild is the right trade: the
// cost of a write is 1 extra db.Keys walk on the next read, and reads
// drop from N×JSON-decode to a single slice walk.
var chunkCache struct {
	mu     sync.RWMutex
	db     Database
	chunks []EmbeddedChunk
	loaded bool
}

// loadChunkCache fully (re)builds the cache from db. Caller must hold
// chunkCache.mu for write.
func loadChunkCache(db Database) {
	chunkCache.db = db
	chunkCache.chunks = chunkCache.chunks[:0]
	for _, key := range db.Keys(EmbeddedChunks) {
		var c EmbeddedChunk
		if db.Get(EmbeddedChunks, key, &c) {
			chunkCache.chunks = append(chunkCache.chunks, c)
		}
	}
	chunkCache.loaded = true
}

// snapshotChunks returns the current cached chunk slice for db,
// rebuilding from kvlite if the cache is empty or pointed at a
// different Database. The returned slice is owned by the cache —
// callers must NOT mutate it.
func snapshotChunks(db Database) []EmbeddedChunk {
	chunkCache.mu.RLock()
	if chunkCache.loaded && chunkCache.db == db {
		out := chunkCache.chunks
		chunkCache.mu.RUnlock()
		return out
	}
	chunkCache.mu.RUnlock()

	chunkCache.mu.Lock()
	defer chunkCache.mu.Unlock()
	if !chunkCache.loaded || chunkCache.db != db {
		loadChunkCache(db)
	}
	return chunkCache.chunks
}

// invalidateChunkCache marks the cache stale so the next read rebuilds.
// Cheap: a single bool flip under lock.
func invalidateChunkCache() {
	chunkCache.mu.Lock()
	chunkCache.loaded = false
	chunkCache.mu.Unlock()
}

// EmbeddedChunk is a single row in the vector store. One report's
// text is split into multiple chunks (typically one per `## section`);
// each gets its own row with its own embedding. Source is the
// app-provided origin tag (e.g., the app name or record kind) so
// consumers can filter or group results by where the chunk came from.
type EmbeddedChunk struct {
	ID       string    `json:"id"`        // UUID, the kvlite key
	Source   string    `json:"source"`    // app-provided origin tag for this chunk
	ReportID string    `json:"report_id"` // parent record ID in that source's table
	Section  string    `json:"section"`   // section heading, e.g. "Executive Summary"
	Text     string    `json:"text"`      // the chunk content
	Vector   []float32 `json:"vector"`    // embedding
	Model    string    `json:"model"`     // embedding model used (for compatibility)
	Date     string    `json:"date"`      // ingestion timestamp
}

// SearchHit is one result from a semantic or keyword search.
type SearchHit struct {
	Source   string  `json:"source"`
	ReportID string  `json:"report_id"`
	Section  string  `json:"section"`
	Text     string  `json:"text"`
	Score    float32 `json:"score"`
}

// SplitReportIntoChunks splits a synthesized report's body at `## section`
// boundaries. The opening (everything before the first `##`) becomes
// one chunk labeled "Overview"; every subsequent `## Header` becomes a
// chunk labeled with that header. The "## Sources" section at the end
// is dropped — it's a bibliography, not semantic content. Empty or
// all-whitespace sections are skipped.
func SplitReportIntoChunks(report string) []struct{ Section, Text string } {
	report = strings.TrimSpace(report)
	if report == "" {
		return nil
	}
	var chunks []struct{ Section, Text string }
	lines := strings.Split(report, "\n")
	var curSection string
	var curBuf strings.Builder
	flush := func() {
		text := strings.TrimSpace(curBuf.String())
		section := curSection
		if section == "" {
			section = "Overview"
		}
		// Drop the bibliography — not semantic content to search over.
		if strings.EqualFold(section, "Sources") {
			curBuf.Reset()
			return
		}
		if text != "" {
			chunks = append(chunks, struct{ Section, Text string }{section, text})
		}
		curBuf.Reset()
	}
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "## ") {
			flush()
			curSection = strings.TrimSpace(strings.TrimPrefix(trim, "##"))
			continue
		}
		curBuf.WriteString(line)
		curBuf.WriteByte('\n')
	}
	flush()
	return chunks
}

// IngestReport chunks the given report, embeds each chunk, and stores
// the results in the vector store tagged with the given source label
// (app-provided origin tag — the app decides what string to pass).
// Any existing chunks for that reportID are replaced. Silent no-op when embeddings are disabled or the DB is
// nil. Errors from individual chunk embeddings are logged and skipped
// — a partial ingestion beats a failed one. Embeddings always run on
// the content, but chunks are also stored even when embedding fails
// (with an empty Vector) so the fallback substring search still works.
func IngestReport(ctx context.Context, db Database, source, reportID, report string) {
	if db == nil || reportID == "" {
		return
	}
	// Defense-in-depth: servitor handles SSH credentials, system facts,
	// and other sensitive per-appliance data that must never be indexed
	// into the deployment-wide knowledge store. Refuse the ingest if a
	// caller ever wires it up by mistake.
	if source == "servitor" {
		Debug("[vector] refusing to ingest source=servitor (sensitive data — must stay in app)")
		return
	}
	// Remove any existing chunks for this report — re-ingestion on
	// resynth should replace, not duplicate.
	DeleteReportChunks(db, reportID)

	chunks := SplitReportIntoChunks(report)
	if len(chunks) == 0 {
		Debug("[vector] no chunks extracted for %s/%s", source, reportID)
		return
	}
	cfg := GetEmbeddingConfig()
	now := time.Now().Format(time.RFC3339)
	var embedded, empty int
	for _, c := range chunks {
		var vec []float32
		if cfg.Enabled {
			v, err := Embed(ctx, c.Section+"\n\n"+c.Text)
			if err != nil {
				Debug("[vector] embed failed for %s/%s section %q: %s", source, reportID, c.Section, err)
			} else {
				vec = v
			}
		}
		if len(vec) > 0 {
			embedded++
		} else {
			empty++
		}
		row := EmbeddedChunk{
			ID:       UUIDv4(),
			Source:   source,
			ReportID: reportID,
			Section:  c.Section,
			Text:     c.Text,
			Vector:   vec,
			Model:    cfg.Model,
			Date:     now,
		}
		db.Set(EmbeddedChunks, row.ID, row)
	}
	invalidateChunkCache()
	Debug("[vector] ingested %s/%s: %d chunks (%d embedded, %d empty)", source, reportID, len(chunks), embedded, empty)
}

// DeleteReportChunks removes every chunk belonging to the given report.
// Called on re-ingestion (before re-insert) and on record deletion
// (cleanup). Silent no-op on nil DB.
func DeleteReportChunks(db Database, reportID string) {
	if db == nil || reportID == "" {
		return
	}
	removed := false
	for _, key := range db.Keys(EmbeddedChunks) {
		var c EmbeddedChunk
		if db.Get(EmbeddedChunks, key, &c) && c.ReportID == reportID {
			db.Unset(EmbeddedChunks, key)
			removed = true
		}
	}
	if removed {
		invalidateChunkCache()
	}
}

// VectorIndexStats is the shape returned by VectorStats and the
// /admin/api/vector-stats endpoint so the admin UI can display a
// quick snapshot of index health.
type VectorIndexStats struct {
	Total    int            `json:"total"`
	Embedded int            `json:"embedded"`
	Empty    int            `json:"empty"`
	BySource map[string]int `json:"by_source"`
}

// VectorStats walks the EmbeddedChunks table once and summarizes how
// many chunks are stored, how many have real vectors vs fell back to
// empty (because embed was down at ingest time), and the breakdown per
// source. Intended for admin-panel visibility — not hot-path.
func VectorStats(db Database) VectorIndexStats {
	stats := VectorIndexStats{BySource: map[string]int{}}
	if db == nil {
		return stats
	}
	for _, key := range db.Keys(EmbeddedChunks) {
		var c EmbeddedChunk
		if !db.Get(EmbeddedChunks, key, &c) {
			continue
		}
		stats.Total++
		if len(c.Vector) > 0 {
			stats.Embedded++
		} else {
			stats.Empty++
		}
		src := c.Source
		if src == "" {
			src = "unknown"
		}
		stats.BySource[src]++
	}
	return stats
}

// MaintenanceFunc is a named one-shot repair function registered by a
// private package at init time. The admin UI can trigger any registered
// function by key. Returns the number of records modified.
type MaintenanceFunc struct {
	Label string
	Desc  string
	Run   func(ctx context.Context) int
}

var maintenanceFuncs []MaintenanceFunc

// RegisterMaintenanceFunc registers a named maintenance function for the
// admin panel. Called from package init() functions.
func RegisterMaintenanceFunc(key, label, desc string, fn func(ctx context.Context) int) {
	maintenanceFuncs = append(maintenanceFuncs, MaintenanceFunc{Label: label, Desc: desc, Run: fn})
}

// ListMaintenanceFuncs returns metadata for all registered maintenance funcs.
func ListMaintenanceFuncs() []struct{ Key, Label, Desc string } {
	out := make([]struct{ Key, Label, Desc string }, len(maintenanceFuncs))
	for i, m := range maintenanceFuncs {
		out[i] = struct{ Key, Label, Desc string }{Key: m.Label, Label: m.Label, Desc: m.Desc}
	}
	return out
}

// RunMaintenanceFunc runs the maintenance function matching key (by Label).
// Returns -1 if not found.
func RunMaintenanceFunc(ctx context.Context, key string) int {
	for _, m := range maintenanceFuncs {
		if m.Label == key {
			return m.Run(ctx)
		}
	}
	return -1
}

// SearchChunks returns the top-K chunks by cosine similarity to the
// query vector. Backed by an in-process cache (chunkCache) so each
// query is a slice scan, not a kvlite re-deserialize. Skips chunks
// whose dimension doesn't match the query (embedding model mismatch).
//
// Scale notes: comfortable to ~50k chunks with the cache; consider a
// real ANN index (HNSW via coder/hnsw, chromem-go) above that.
func SearchChunks(db Database, query []float32, k int) []SearchHit {
	if db == nil || len(query) == 0 || k <= 0 {
		return nil
	}
	chunks := snapshotChunks(db)
	type scored struct {
		hit   SearchHit
		score float32
	}
	var all []scored
	for i := range chunks {
		c := &chunks[i]
		if len(c.Vector) != len(query) {
			continue
		}
		s := Cosine(query, c.Vector)
		if s <= 0 {
			continue
		}
		all = append(all, scored{
			hit: SearchHit{
				Source:   c.Source,
				ReportID: c.ReportID,
				Section:  c.Section,
				Text:     c.Text,
				Score:    s,
			},
			score: s,
		})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].score > all[j].score })
	if k > len(all) {
		k = len(all)
	}
	out := make([]SearchHit, k)
	for i := 0; i < k; i++ {
		out[i] = all[i].hit
	}
	return out
}

// SearchChunksSubstring does a case-insensitive substring match over
// stored chunks. Used as the fallback when embeddings are disabled or
// a query's embedding fails, so the unified search tool still returns
// something. Scores are primitive (1.0 for substring hit, 0.0 else)
// so the caller can still rank results. Returns up to k matches.
func SearchChunksSubstring(db Database, query string, k int) []SearchHit {
	if db == nil || query == "" || k <= 0 {
		return nil
	}
	q := strings.ToLower(query)
	chunks := snapshotChunks(db)
	var out []SearchHit
	for i := range chunks {
		c := &chunks[i]
		if !strings.Contains(strings.ToLower(c.Section+" "+c.Text), q) {
			continue
		}
		out = append(out, SearchHit{
			Source:   c.Source,
			ReportID: c.ReportID,
			Section:  c.Section,
			Text:     c.Text,
			Score:    1,
		})
		if len(out) >= k {
			break
		}
	}
	return out
}
