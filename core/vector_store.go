package core

import (
	"context"
	"sort"
	"strings"
	"time"
)

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
	Debug("[vector] ingested %s/%s: %d chunks (%d embedded, %d empty)", source, reportID, len(chunks), embedded, empty)
}

// DeleteReportChunks removes every chunk belonging to the given report.
// Called on re-ingestion (before re-insert) and on record deletion
// (cleanup). Silent no-op on nil DB.
func DeleteReportChunks(db Database, reportID string) {
	if db == nil || reportID == "" {
		return
	}
	for _, key := range db.Keys(EmbeddedChunks) {
		var c EmbeddedChunk
		if db.Get(EmbeddedChunks, key, &c) && c.ReportID == reportID {
			db.Unset(EmbeddedChunks, key)
		}
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

// EmbeddingBackfiller describes a registered per-package backfill
// function. Packages that persist reports worth embedding register at
// init time; the admin UI calls RunAllEmbeddingBackfills to trigger
// them all. Decouples admin from the individual packages' record
// types.
type EmbeddingBackfiller struct {
	Label string
	Run   func(ctx context.Context) int // returns records newly ingested
}

var embeddingBackfillers []EmbeddingBackfiller

// RegisterEmbeddingBackfiller registers a per-package backfill helper.
// Called from package init() functions.
func RegisterEmbeddingBackfiller(label string, fn func(ctx context.Context) int) {
	embeddingBackfillers = append(embeddingBackfillers, EmbeddingBackfiller{Label: label, Run: fn})
}

// RunAllEmbeddingBackfills invokes every registered backfiller and
// returns a map of label → records-newly-ingested. Admin UI surfaces
// this so the operator sees the scope of the first-time embed.
func RunAllEmbeddingBackfills(ctx context.Context) map[string]int {
	out := make(map[string]int, len(embeddingBackfillers))
	for _, b := range embeddingBackfillers {
		out[b.Label] = b.Run(ctx)
	}
	return out
}

// BackfillMissing walks every record in the given history table and
// ingests any whose report text hasn't yet been chunked (detected by
// absence of any EmbeddedChunk row pointing at the report ID under
// the given source). Safe to run multiple times — records already
// ingested are skipped. Intended to be called from an admin endpoint
// after embeddings are first enabled or after switching models.
//
// reportsFor is a caller-supplied function that pulls a (reportID,
// reportText) pair for each record in the history table. Decouples
// this helper from the source package's record types.
//
// Returns the number of records newly ingested.
func BackfillMissing(ctx context.Context, db Database, source, historyTable string, reportsFor func(key string) (id, report string, ok bool)) int {
	if db == nil {
		return 0
	}
	// Build a set of (source, reportID) that already have AT LEAST ONE
	// chunk with a non-empty vector. Records whose chunks all have
	// empty vectors (because embedding was down when they were first
	// ingested) are NOT skipped — the backfill is the retry path.
	ingested := make(map[string]bool)
	embedReady := GetEmbeddingConfig().Enabled
	for _, key := range db.Keys(EmbeddedChunks) {
		var c EmbeddedChunk
		if !db.Get(EmbeddedChunks, key, &c) || c.Source != source {
			continue
		}
		// When embeddings are currently disabled, any existing chunk
		// counts as "ingested" (no point re-ingesting what we can't
		// embed). When embeddings ARE enabled, require a non-empty
		// vector — empty means a prior embed failed and this is a
		// retry opportunity.
		if !embedReady || len(c.Vector) > 0 {
			ingested[c.ReportID] = true
		}
	}
	var count, skipped int
	for _, key := range db.Keys(historyTable) {
		id, report, ok := reportsFor(key)
		if !ok || id == "" || strings.TrimSpace(report) == "" {
			continue
		}
		if ingested[id] {
			skipped++
			continue
		}
		IngestReport(ctx, db, source, id, report)
		count++
	}
	Debug("[vector] backfill %s: %d ingested, %d already-indexed skipped", source, count, skipped)
	return count
}

// SearchChunks returns the top-K chunks by cosine similarity to the
// query vector. Linear scan over all stored chunks — fine for the
// gohort scale (hundreds of reports → thousands of chunks at most,
// search completes in tens of milliseconds). Skips chunks whose
// dimension doesn't match the query (embedding model mismatch).
func SearchChunks(db Database, query []float32, k int) []SearchHit {
	if db == nil || len(query) == 0 || k <= 0 {
		return nil
	}
	type scored struct {
		hit   SearchHit
		score float32
	}
	var all []scored
	for _, key := range db.Keys(EmbeddedChunks) {
		var c EmbeddedChunk
		if !db.Get(EmbeddedChunks, key, &c) {
			continue
		}
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
	var out []SearchHit
	for _, key := range db.Keys(EmbeddedChunks) {
		var c EmbeddedChunk
		if !db.Get(EmbeddedChunks, key, &c) {
			continue
		}
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
