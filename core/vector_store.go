package core

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// chunkAgeHalflifeDays is the soft half-life for temporal-decay
// ranking: a chunk N days old has its raw cosine multiplied by
// exp(-N / halflife). At halflife days the effective score is halved;
// at 2x halflife it's a quarter. 180 days is a gentle decay — keeps
// year-old reference content alive while letting fresh chunks edge
// out stale ones on ties. Strong-match old chunks still surface for
// niche queries (the multiplier is a tiebreaker, not a filter).
const chunkAgeHalflifeDays = 180.0

// applyTemporalDecay multiplies a raw cosine score by an exponential
// decay factor based on chunk age. Chunks with an empty / unparseable
// Date pass through with no decay — defensive against legacy rows.
// Returns the adjusted score so the caller can sort + filter on the
// same field downstream consumers see.
func applyTemporalDecay(rawScore float32, date string) float32 {
	if date == "" {
		return rawScore
	}
	t, err := time.Parse(time.RFC3339, date)
	if err != nil {
		return rawScore
	}
	age := time.Since(t).Hours() / 24.0
	if age <= 0 {
		return rawScore
	}
	decay := math.Exp(-age / chunkAgeHalflifeDays)
	return rawScore * float32(decay)
}

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

// InvalidateChunkCache is the exported wrapper for callers outside the
// core package that bulk-modify EmbeddedChunks rows (e.g. one-shot
// maintenance migrations). Normal IngestReport / DeleteReportChunks
// paths already invalidate internally.
func InvalidateChunkCache() { invalidateChunkCache() }

// EmbeddedChunk is a single row in the vector store. One report's
// text is split into multiple chunks (typically one per `## section`);
// each gets its own row with its own embedding. Source is the
// app-provided origin tag (e.g., the app name or record kind) so
// consumers can filter or group results by where the chunk came from.
type EmbeddedChunk struct {
	ID       string    `json:"id"`                // UUID, the kvlite key
	Source   string    `json:"source"`            // app-provided origin tag for this chunk
	ReportID string    `json:"report_id"`         // parent record ID in that source's table
	Title    string    `json:"title,omitempty"`   // human-meaningful name of the PARENT document (e.g. the debate topic / research question) — the same for every chunk of one report. Lets browsers + recall label a chunk by what it's ABOUT, not just its section heading ("Verdict" → "<topic> — Verdict"). Empty on legacy chunks (fall back to Section).
	Section  string    `json:"section"`           // section heading, e.g. "Executive Summary"
	Text     string    `json:"text"`              // the chunk content
	Vector   []float32 `json:"vector"`            // embedding
	Model    string    `json:"model"`             // embedding model used (for compatibility)
	Date     string    `json:"date"`              // ingestion timestamp
	Locator  string    `json:"locator,omitempty"` // optional source pointer for citations — e.g. "page 12", "pages 4-5", "§ 3.2 Auth flow". Empty when the source has no meaningful sub-document locator (plain text, single-page note). Surfaced in SearchHit so the LLM can cite specifically.
	// Kind tags the provenance of this chunk so consumers can
	// frame it appropriately at retrieval time. Empty (default)
	// = authoritative content (article body, official docs). Set
	// at extraction time by the HTML extractor when the source
	// element's class/role indicates a non-authoritative kind:
	//   - "user_comment" — comments thread under an article
	//   - "related_link" — "you might also like" / related-posts rails
	//   - "author_bio"   — author byline / about-the-author blurb
	// Future kinds (LLM section classifier may add):
	//   - "opinion" / "editorial"
	// Knowledge agents are taught to cite these differently
	// ("one commenter noted…" vs "the doc says…").
	Kind string `json:"kind,omitempty"`
}

// SearchHit is one result from a semantic or keyword search. ID is the
// underlying EmbeddedChunk row key — exposed so callers that want to
// act on a hit (delete it, mark it stale, etc.) can address the row
// directly without a second scan.
type SearchHit struct {
	ID       string  `json:"id"`
	Source   string  `json:"source"`
	ReportID string  `json:"report_id"`
	Title    string  `json:"title,omitempty"` // mirrored from EmbeddedChunk.Title — the parent document's human name (debate topic / research question) so recall can say what a hit is ABOUT, not just its section.
	Section  string  `json:"section"`
	Text     string  `json:"text"`
	Score    float32 `json:"score"`
	Locator  string  `json:"locator,omitempty"` // mirrored from EmbeddedChunk.Locator — citation pointer (page number, section ref)
	Date     string  `json:"date,omitempty"`    // mirrored from EmbeddedChunk.Date — ingestion timestamp (RFC3339). Surfaced so the LLM can weight freshness and cite as-of date.
	Kind     string  `json:"kind,omitempty"`    // mirrored from EmbeddedChunk.Kind — provenance tag ("user_comment", "related_link", "author_bio") so consumers can frame the hit appropriately. Empty = authoritative.
}

// maxChunkChars caps how large a single chunk can be before it gets
// sub-split. Sized conservatively for 512-token embedding-server batch
// limits. The 4-chars/token rule of thumb breaks down on dense
// technical content (PDFs with tables, code blocks, RFC-style citation
// blocks, math-heavy text) where the ratio dips to ~2 chars/token —
// so a 1800-char chunk could be 900 tokens, well past the 512 cap on
// a constrained embedding server. 1000 chars targets ~250-500 tokens
// across content densities, with embedWithRetry below handling the
// rare overflow.
const maxChunkChars = 1000

// SplitReportIntoChunks splits a synthesized report's body at `## section`
// boundaries. The opening (everything before the first `##`) becomes
// one chunk labeled "Overview"; every subsequent `## Header` becomes a
// chunk labeled with that header. The "## Sources" section at the end
// is dropped — it's a bibliography, not semantic content. Empty or
// all-whitespace sections are skipped.
//
// Oversized sections (> maxChunkChars) are sub-split at paragraph
// boundaries — a single overlong section can't blow past the
// embedder's batch limit. Sub-chunks inherit the section name with a
// " (part N)" suffix so the retrieval payload still attributes the
// content to its parent heading.
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
		if text == "" {
			curBuf.Reset()
			return
		}
		// Sub-split if this section is too big for one embedding call.
		// splitOnParagraphsCap returns the original text as a single-
		// element slice when it fits, so the cheap path stays cheap.
		for i, part := range splitOnParagraphsCap(text, maxChunkChars) {
			name := section
			if len(part) > 0 && i > 0 {
				name = section + fmt.Sprintf(" (part %d)", i+1)
			}
			if i == 0 && len(part) > 0 {
				// Mark the first part too only when there's more than one
				// — single-chunk sections stay un-suffixed.
				if len(splitOnParagraphsCap(text, maxChunkChars)) > 1 {
					name = section + " (part 1)"
				}
			}
			chunks = append(chunks, struct{ Section, Text string }{name, part})
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

// splitOnParagraphsCap breaks text into chunks no longer than cap,
// preferring paragraph boundaries (blank lines). When a single
// paragraph itself exceeds cap, it gets hard-cut at the cap to
// preserve the invariant that no returned chunk is larger than cap.
// Returns [text] unchanged when text already fits.
func splitOnParagraphsCap(text string, cap int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if len(text) <= cap {
		return []string{text}
	}
	paragraphs := strings.Split(text, "\n\n")
	var out []string
	var cur strings.Builder
	for _, p := range paragraphs {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// One paragraph is itself too big — hard-cut it.
		if len(p) > cap {
			if cur.Len() > 0 {
				out = append(out, strings.TrimSpace(cur.String()))
				cur.Reset()
			}
			for len(p) > cap {
				out = append(out, p[:cap])
				p = p[cap:]
			}
			if p != "" {
				cur.WriteString(p)
				cur.WriteString("\n\n")
			}
			continue
		}
		// Would adding this paragraph overflow? Flush first.
		if cur.Len()+len(p)+2 > cap && cur.Len() > 0 {
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
		}
		cur.WriteString(p)
		cur.WriteString("\n\n")
	}
	if cur.Len() > 0 {
		out = append(out, strings.TrimSpace(cur.String()))
	}
	return out
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
	IngestReportTagged(ctx, db, source, reportID, report, "")
}

// IngestReportTagged is IngestReport with a Kind tag attached to
// every chunk it creates. Use for content with non-authoritative
// provenance (user comments, related-link rails, author bios) so
// consumers at retrieval time can frame the hits appropriately
// ("one commenter noted…" vs "the doc says…"). Pass kind="" for
// default authoritative (equivalent to IngestReport).
func IngestReportTagged(ctx context.Context, db Database, source, reportID, report, kind string) {
	IngestReportTitled(ctx, db, source, reportID, "", report, kind)
}

// BackfillChunkTitles stamps Title onto pre-existing chunks of the
// given Kind that have an empty Title, resolving each chunk's ReportID
// to a document title via resolve(). Debate/research chunks live in the
// deployment collection (Source = the collection), tagged by Kind
// ("debate"/"research") — so the filter is on Kind, and ReportID is the
// app record's ID. Idempotent: chunks that already have a Title are
// skipped, so a one-time guard isn't strictly required for correctness
// (only to avoid a needless full-table scan on every startup). resolve()
// returns "" for unknown IDs (those chunks are left as-is). Returns the
// number of chunks updated.
func BackfillChunkTitles(db Database, kind string, resolve func(reportID string) string) int {
	if db == nil || kind == "" || resolve == nil {
		return 0
	}
	cache := map[string]string{}
	updated := 0
	for _, key := range db.Keys(EmbeddedChunks) {
		var c EmbeddedChunk
		if !db.Get(EmbeddedChunks, key, &c) {
			continue
		}
		if c.Kind != kind || strings.TrimSpace(c.Title) != "" {
			continue
		}
		title, seen := cache[c.ReportID]
		if !seen {
			title = strings.TrimSpace(resolve(c.ReportID))
			cache[c.ReportID] = title
		}
		if title == "" {
			continue
		}
		c.Title = title
		db.Set(EmbeddedChunks, key, c)
		updated++
	}
	if updated > 0 {
		invalidateChunkCache()
	}
	return updated
}

// IngestReportTitled is IngestReportTagged plus a document Title — the
// human-meaningful name of the parent record (e.g. a debate topic or a
// research question) stamped onto every chunk. Without it, browsers and
// recall can only show a chunk's section heading ("Verdict", "Executive
// Summary"), which is meaningless without knowing what document it came
// from. Pass title="" for sources that have no distinct document name
// (equivalent to IngestReportTagged).
func IngestReportTitled(ctx context.Context, db Database, source, reportID, title, report, kind string) {
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
	var embedded, empty, split int
	for _, c := range chunks {
		// embedWithSplitFallback handles the case where a single chunk,
		// even after the chunker's defensive cap, still exceeds the
		// embedder's per-call token limit. The fallback recursively
		// halves the text until each piece embeds successfully OR
		// returns empty for pieces that fail for other reasons.
		pieces := embedWithSplitFallback(ctx, cfg, c.Section, c.Text)
		for i, p := range pieces {
			if len(p.Vector) > 0 {
				embedded++
			} else {
				empty++
			}
			sect := c.Section
			if len(pieces) > 1 {
				// Sub-chunks get a "(part i/N)" tag so search results
				// can show which slice of an oversized section matched.
				sect = fmt.Sprintf("%s (part %d/%d)", c.Section, i+1, len(pieces))
				split++
			}
			row := EmbeddedChunk{
				ID:       UUIDv4(),
				Source:   source,
				ReportID: reportID,
				Title:    title,
				Section:  sect,
				Text:     p.Text,
				Vector:   p.Vector,
				Model:    cfg.Model,
				Date:     now,
				Kind:     kind,
			}
			db.Set(EmbeddedChunks, row.ID, row)
		}
	}
	invalidateChunkCache()
	tagSuffix := ""
	if kind != "" {
		tagSuffix = " [kind=" + kind + "]"
	}
	if split > 0 {
		Debug("[vector] ingested %s/%s%s: %d chunks → %d rows (%d embedded, %d empty, %d sub-split rows from oversize chunks)",
			source, reportID, tagSuffix, len(chunks), embedded+empty, embedded, empty, split)
	} else {
		Debug("[vector] ingested %s/%s%s: %d chunks (%d embedded, %d empty)", source, reportID, tagSuffix, len(chunks), embedded, empty)
	}
}

// IngestPagedReport is the same as IngestReport but takes text where
// page boundaries are marked with form-feed (\f) — the natural output
// of `pdftotext` for PDFs. Each resulting chunk gets a Locator set to
// "page N" (the page it came from). Used by the KB upload path for
// PDFs so search results carry citable page numbers.
//
// Falls back to a single "page 1" locator if the input has no form-
// feeds (treats the whole document as one page). For non-PDF inputs
// the caller should use IngestReport directly — there's no useful
// per-page locator for plain text / DOCX / Markdown.
func IngestPagedReport(ctx context.Context, db Database, source, reportID, report string) {
	if db == nil || reportID == "" {
		return
	}
	if source == "servitor" {
		Debug("[vector] refusing to ingest source=servitor (sensitive data — must stay in app)")
		return
	}
	DeleteReportChunks(db, reportID)

	pages := strings.Split(report, "\f")
	cfg := GetEmbeddingConfig()
	now := time.Now().Format(time.RFC3339)
	var totalChunks, embedded, empty, split int
	for i, pageText := range pages {
		pageText = strings.TrimSpace(pageText)
		if pageText == "" {
			continue
		}
		pageNum := i + 1
		locator := fmt.Sprintf("page %d", pageNum)
		chunks := SplitReportIntoChunks(pageText)
		if len(chunks) == 0 {
			continue
		}
		totalChunks += len(chunks)
		for _, c := range chunks {
			pieces := embedWithSplitFallback(ctx, cfg, c.Section, c.Text)
			for j, p := range pieces {
				if len(p.Vector) > 0 {
					embedded++
				} else {
					empty++
				}
				sect := c.Section
				if len(pieces) > 1 {
					sect = fmt.Sprintf("%s (part %d/%d)", c.Section, j+1, len(pieces))
					split++
				}
				row := EmbeddedChunk{
					ID:       UUIDv4(),
					Source:   source,
					ReportID: reportID,
					Section:  sect,
					Text:     p.Text,
					Vector:   p.Vector,
					Model:    cfg.Model,
					Date:     now,
					Locator:  locator,
				}
				db.Set(EmbeddedChunks, row.ID, row)
			}
		}
	}
	invalidateChunkCache()
	if split > 0 {
		Debug("[vector] paged-ingested %s/%s: %d pages, %d chunks → %d rows (%d embedded, %d empty, %d sub-split rows from oversize chunks)",
			source, reportID, len(pages), totalChunks, embedded+empty, embedded, empty, split)
	} else {
		Debug("[vector] paged-ingested %s/%s: %d pages, %d chunks (%d embedded, %d empty)",
			source, reportID, len(pages), totalChunks, embedded, empty)
	}
}

// embedPiece is one (text, vector) result from embedWithSplitFallback.
type embedPiece struct {
	Text   string
	Vector []float32
}

// embedWithSplitFallback embeds a chunk's text, falling back to
// recursive half-splitting when the embedder rejects the input as too
// large. Returns one piece per successful (or final-failed) embed call.
// On non-size errors (network, decode, server outage) the function
// stops splitting and returns a single piece with an empty vector so
// the row still lands in the index with its raw text (recoverable via
// re-embed later).
func embedWithSplitFallback(ctx context.Context, cfg EmbeddingConfig, section, text string) []embedPiece {
	return embedWithSplitFallbackDepth(ctx, cfg, section, text, 0)
}

// embedWithSplitFallbackDepth is the recursive worker with an explicit
// depth counter. Hard cap prevents pathological inputs (a 300 KB
// scraped page that's not a useful document anyway) from producing
// thousands of sub-split rows when each level's embed call fails.
// 8 levels = up to 256 pieces from one chunk — plenty for any
// reasonable doc; anything beyond is junk we shouldn't be ingesting.
const maxSplitDepth = 8

func embedWithSplitFallbackDepth(ctx context.Context, cfg EmbeddingConfig, section, text string, depth int) []embedPiece {
	const minSplitChars = 200 // stop splitting below this — small chunks rarely fail for size
	if !cfg.Enabled {
		return []embedPiece{{Text: text}}
	}
	if depth >= maxSplitDepth {
		Debug("[vector] embed split depth cap %d reached for section %q (text %d chars) — storing raw", maxSplitDepth, section, len(text))
		return []embedPiece{{Text: text}}
	}
	prompt := section + "\n\n" + text
	v, err := Embed(ctx, prompt)
	if err == nil {
		return []embedPiece{{Text: text, Vector: v}}
	}
	// Only retry-with-split on size errors. Anything else (server
	// down, network timeout, auth fail) won't be fixed by smaller
	// input — bail with an empty vector.
	if !isEmbedSizeError(err) {
		Debug("[vector] embed failed (non-size) section %q: %s", section, err)
		return []embedPiece{{Text: text}}
	}
	if len(text) <= minSplitChars {
		Debug("[vector] embed too-large but text already small (%d chars), giving up: section %q", len(text), section)
		return []embedPiece{{Text: text}}
	}
	// Split at the nearest paragraph or sentence boundary near the
	// midpoint. Falls back to a hard mid-cut if nothing sensible.
	left, right := splitTextNearMid(text)
	if left == "" || right == "" {
		Debug("[vector] embed too-large but split produced empty halves: section %q", section)
		return []embedPiece{{Text: text}}
	}
	Debug("[vector] embed too-large for section %q (%d chars) — sub-splitting (depth=%d)", section, len(text), depth)
	out := embedWithSplitFallbackDepth(ctx, cfg, section, left, depth+1)
	out = append(out, embedWithSplitFallbackDepth(ctx, cfg, section, right, depth+1)...)
	return out
}

// isEmbedSizeError matches the error strings llama.cpp + Ollama
// produce when input exceeds the physical batch size. Conservative —
// only retries with split when the error clearly says size.
func isEmbedSizeError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "too large"):
		return true
	case strings.Contains(msg, "batch size"):
		return true
	case strings.Contains(msg, "context length"):
		return true
	case strings.Contains(msg, "exceeds"):
		return true
	}
	return false
}

// splitTextNearMid splits text at the paragraph/sentence boundary
// closest to the midpoint. Used by embedWithSplitFallback to make
// oversize-retry produce semantically-coherent halves rather than
// hard mid-cuts that bisect words.
func splitTextNearMid(text string) (string, string) {
	if len(text) < 4 {
		return text, ""
	}
	mid := len(text) / 2
	// Try paragraph break first (most natural).
	if idx := bestBoundaryNear(text, mid, "\n\n"); idx > 0 {
		return strings.TrimSpace(text[:idx]), strings.TrimSpace(text[idx:])
	}
	// Single newline.
	if idx := bestBoundaryNear(text, mid, "\n"); idx > 0 {
		return strings.TrimSpace(text[:idx]), strings.TrimSpace(text[idx:])
	}
	// Sentence boundary (period + space).
	if idx := bestBoundaryNear(text, mid, ". "); idx > 0 {
		return strings.TrimSpace(text[:idx+1]), strings.TrimSpace(text[idx+1:])
	}
	// Space — at least don't bisect a word.
	if idx := bestBoundaryNear(text, mid, " "); idx > 0 {
		return strings.TrimSpace(text[:idx]), strings.TrimSpace(text[idx:])
	}
	// Hard cut.
	return strings.TrimSpace(text[:mid]), strings.TrimSpace(text[mid:])
}

// bestBoundaryNear finds the occurrence of sep in text whose offset is
// closest to mid. Returns the index (start of sep occurrence) or -1.
func bestBoundaryNear(text string, mid int, sep string) int {
	best := -1
	bestDist := len(text)
	idx := 0
	for {
		next := strings.Index(text[idx:], sep)
		if next < 0 {
			break
		}
		pos := idx + next
		dist := pos - mid
		if dist < 0 {
			dist = -dist
		}
		if dist < bestDist {
			best = pos
			bestDist = dist
		}
		idx = pos + len(sep)
	}
	return best
}

// DeleteChunksByIDs removes the chunks with the given EmbeddedChunk
// IDs and invalidates the read cache. Used by surface-level "forget
// these specific hits" flows (e.g. knowledge_forget) that already
// resolved IDs via SearchChunks. Missing IDs are silently skipped —
// idempotent.
func DeleteChunksByIDs(db Database, ids []string) {
	if db == nil || len(ids) == 0 {
		return
	}
	for _, id := range ids {
		if id == "" {
			continue
		}
		db.Unset(EmbeddedChunks, id)
	}
	invalidateChunkCache()
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

// WipeVectorStore deletes every chunk in the EmbeddedChunks table.
// Nuclear option — used only by the admin "wipe all" affordance for
// global cleanup. Per-agent or per-source cleanup should use
// WipeChunksBySourcePrefix to scope the deletion.
//
// Returns the number of chunks removed.
func WipeVectorStore(db Database) int {
	if db == nil {
		return 0
	}
	keys := db.Keys(EmbeddedChunks)
	for _, k := range keys {
		db.Unset(EmbeddedChunks, k)
	}
	invalidateChunkCache()
	return len(keys)
}

// WipeChunksBySourcePrefix deletes every chunk whose Source begins
// with prefix. Used to clean up one agent's accumulated knowledge
// (prefix = "orchestrate:<user>:<agentID>") or one user's entire
// orchestrate footprint (prefix = "orchestrate:<user>:") without
// touching other users / apps.
//
// Returns the number of chunks removed. Cache is invalidated when
// anything was actually removed.
func WipeChunksBySourcePrefix(db Database, prefix string) int {
	if db == nil || prefix == "" {
		return 0
	}
	removed := 0
	for _, key := range db.Keys(EmbeddedChunks) {
		var c EmbeddedChunk
		if !db.Get(EmbeddedChunks, key, &c) {
			continue
		}
		if !strings.HasPrefix(c.Source, prefix) {
			continue
		}
		db.Unset(EmbeddedChunks, key)
		removed++
	}
	if removed > 0 {
		invalidateChunkCache()
	}
	return removed
}

// --- one-shot migration of legacy chunk stores into VectorDB ---

// legacyChunkSources are pre-split stores that historically held
// shared EmbeddedChunks rows (agent knowledge, collections, deployment
// KB). Registered by wiring code; folded into VectorDB once on the
// first boot after the split.
var legacyChunkSources []Database

// vectorMetaTable holds VectorDB-local bookkeeping (the migration
// marker). Lives in VectorDB itself so the "already migrated" fact
// travels with the file — relocate the vector store and the marker
// comes with it.
const vectorMetaTable = "vector_meta"

// RegisterLegacyChunkSource declares a store whose EmbeddedChunks rows
// should be folded into VectorDB by MigrateLegacyChunksToVectorDB.
// Wiring registers each app/bucket that historically wrote SHARED
// chunks. Stores that must stay isolated (e.g. phantom's personal
// corpus) deliberately do NOT register — they keep passing their own
// handle to the chunk functions. nil handles are ignored; registering
// the same store twice is harmless (the copy dedups by chunk ID).
func RegisterLegacyChunkSource(db Database) {
	if db != nil {
		legacyChunkSources = append(legacyChunkSources, db)
	}
}

// MigrateLegacyChunksToVectorDB copies every EmbeddedChunk from each
// registered legacy source into VectorDB exactly once. Idempotent: a
// marker in VectorDB short-circuits later boots, and within a run rows
// already present (by chunk ID = key) are skipped so a crashed prior
// run resumes cleanly. NON-DESTRUCTIVE — legacy rows are left in place
// so the split is rollback-safe; an operator drops them later once
// satisfied. No-op when VectorDB is unset or a source IS VectorDB (the
// co-located default, where there's nothing to move).
func MigrateLegacyChunksToVectorDB() {
	if VectorDB == nil || len(legacyChunkSources) == 0 {
		return
	}
	var marker string
	if VectorDB.Get(vectorMetaTable, "legacy_migrated", &marker) && marker != "" {
		return
	}
	// Announce up-front, BEFORE any Keys()/Get() call, so an operator
	// watching boot logs can tell the long pause on the first post-split
	// boot is the migration and not a hang. The per-row Get over NFS is
	// the dominant cost on big stores; counting source sizes (Keys walk)
	// is itself slow there, so we don't pre-scan — progress lines show
	// the rate as work proceeds.
	start := time.Now()
	Log("[vector] one-shot migration starting: %d legacy source(s) → VectorDB (non-destructive; legacy rows preserved). This may take minutes on large NFS-backed stores.", len(legacyChunkSources))
	copied, scanned := 0, 0
	for srcIdx, src := range legacyChunkSources {
		if src == nil || src == VectorDB {
			continue
		}
		keys := src.Keys(EmbeddedChunks)
		Log("[vector] migrating source %d/%d: %d chunk(s)", srcIdx+1, len(legacyChunkSources), len(keys))
		for i, key := range keys {
			var c EmbeddedChunk
			if !src.Get(EmbeddedChunks, key, &c) {
				continue
			}
			scanned++
			var existing EmbeddedChunk
			if VectorDB.Get(EmbeddedChunks, key, &existing) {
				continue // already migrated (resume-safe)
			}
			VectorDB.Set(EmbeddedChunks, key, c)
			copied++
			// Heartbeat every 500 chunks so progress is visible on
			// large stores without spamming smaller deployments.
			if (i+1)%500 == 0 {
				Log("[vector] migration progress: source %d/%d, %d/%d chunk(s) in this source (%d copied total, %.0fs elapsed)",
					srcIdx+1, len(legacyChunkSources), i+1, len(keys), copied, time.Since(start).Seconds())
			}
		}
	}
	VectorDB.Set(vectorMetaTable, "legacy_migrated", time.Now().Format(time.RFC3339))
	invalidateChunkCache()
	Log("[vector] migration complete: copied %d chunk(s), scanned %d, elapsed %.1fs; legacy rows left in place for rollback", copied, scanned, time.Since(start).Seconds())
}

// VectorIndexStats is the shape returned by VectorStats and the
// /admin/api/vector-stats endpoint so the admin UI can display a
// quick snapshot of index health.
type VectorIndexStats struct {
	Total    int            `json:"total"`
	Embedded int            `json:"embedded"`
	Empty    int            `json:"empty"`
	BySource map[string]int `json:"by_source"`
	// BySourceText is a stable, source-sorted "src=N, src2=M" rendering of
	// BySource. App-specific map formatting belongs server-side so the
	// generic declarative DisplayPanel can show the breakdown as a plain
	// labeled value instead of teaching the renderer about maps.
	BySourceText string `json:"by_source_text"`
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
	if len(stats.BySource) > 0 {
		keys := make([]string, 0, len(stats.BySource))
		for k := range stats.BySource {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%d", k, stats.BySource[k]))
		}
		stats.BySourceText = strings.Join(parts, ", ")
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
		// Temporal decay applies to the SORT KEY only — SearchHit.Score
		// stays as the raw cosine so callers with similarity-floor
		// filters (manualSearchMinScore, dedup thresholds) get
		// predictable behavior. Recent chunks edge out stale ones on
		// ties; strong-match old chunks still surface for niche queries.
		all = append(all, scored{
			hit: SearchHit{
				ID:       c.ID,
				Source:   c.Source,
				ReportID: c.ReportID,
				Title:    c.Title,
				Section:  c.Section,
				Text:     c.Text,
				Score:    s,
				Locator:  c.Locator,
				Date:     c.Date,
				Kind:     c.Kind,
			},
			score: applyTemporalDecay(s, c.Date),
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

// SearchChunksByPredicate is the most flexible filtered search —
// caller supplies an arbitrary `allow(chunk) bool` predicate that
// sees the full EmbeddedChunk and can filter on Source, ReportID,
// Date, or any other field. Used when the allowed-source set is a
// mix of exact matches (skill IDs, collection IDs) and prefix matches
// (per-(user, agent) corpus across every topic suffix), OR when a
// stricter rule applies (e.g. exclude derived chunks by ReportID
// prefix — the Force Clean mode's behavior).
//
// Pass the whole chunk (not just source) so callers can implement
// provenance-aware filters without re-reading the chunk after match.
func SearchChunksByPredicate(db Database, allow func(c EmbeddedChunk) bool, query []float32, k int) []SearchHit {
	if db == nil || allow == nil || len(query) == 0 || k <= 0 {
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
		if !allow(*c) {
			continue
		}
		if len(c.Vector) != len(query) {
			continue
		}
		s := Cosine(query, c.Vector)
		if s <= 0 {
			continue
		}
		// Temporal decay applies to the SORT KEY only — SearchHit.Score
		// stays as the raw cosine so callers with similarity-floor
		// filters (manualSearchMinScore, dedup thresholds) get
		// predictable behavior. Recent chunks edge out stale ones on
		// ties; strong-match old chunks still surface for niche queries.
		all = append(all, scored{
			hit: SearchHit{
				ID:       c.ID,
				Source:   c.Source,
				ReportID: c.ReportID,
				Title:    c.Title,
				Section:  c.Section,
				Text:     c.Text,
				Score:    s,
				Locator:  c.Locator,
				Date:     c.Date,
				Kind:     c.Kind,
			},
			score: applyTemporalDecay(s, c.Date),
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

// SearchChunksSubstringByPredicate is the substring fallback of
// SearchChunksByPredicate. Same predicate semantics — predicate sees
// the full EmbeddedChunk.
func SearchChunksSubstringByPredicate(db Database, allow func(c EmbeddedChunk) bool, query string, k int) []SearchHit {
	if db == nil || allow == nil || k <= 0 {
		return nil
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	chunks := snapshotChunks(db)
	out := make([]SearchHit, 0, k)
	for i := range chunks {
		c := &chunks[i]
		if !allow(*c) {
			continue
		}
		if !strings.Contains(strings.ToLower(c.Section+"\n"+c.Text), q) {
			continue
		}
		out = append(out, SearchHit{
			ID:       c.ID,
			Source:   c.Source,
			ReportID: c.ReportID,
			Title:    c.Title,
			Section:  c.Section,
			Text:     c.Text,
			Score:    0,
			Locator:  c.Locator,
			Date:     c.Date,
			Kind:     c.Kind,
		})
		if len(out) >= k {
			break
		}
	}
	return out
}

// MergeHitsByScore unions two hit lists, dedups by chunk ID, sorts by
// descending score, and caps at k. Used to fold a second-store search
// pass (e.g. deployment-scoped chunks in RootDB) into the primary
// result set. Fast-paths when either side is empty.
func MergeHitsByScore(a, b []SearchHit, k int) []SearchHit {
	if k <= 0 {
		return nil
	}
	if len(a) == 0 {
		if len(b) > k {
			return b[:k]
		}
		return b
	}
	if len(b) == 0 {
		if len(a) > k {
			return a[:k]
		}
		return a
	}
	merged := make([]SearchHit, 0, len(a)+len(b))
	seen := make(map[string]bool, len(a)+len(b))
	for _, h := range append(append([]SearchHit{}, a...), b...) {
		if seen[h.ID] {
			continue
		}
		seen[h.ID] = true
		merged = append(merged, h)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Score > merged[j].Score })
	if len(merged) > k {
		merged = merged[:k]
	}
	return merged
}

// SearchChunksInSources is the set-filtered cousin of SearchChunks:
// rank ONLY chunks whose Source is in the allowed set, then return
// top-k by similarity. Filtering BEFORE ranking is materially
// different from "rank everything, filter after" — at scale, the
// post-filter approach starves on the agent's own chunks because the
// top-N unfiltered candidates get dominated by other users / agents
// / collections. Pre-filter is required for union searches across
// per-(user, agent) corpus + skill corpora + collection chunks.
//
// Empty `allowed` returns nil (caller would have meant SearchChunks).
func SearchChunksInSources(db Database, allowed map[string]bool, query []float32, k int) []SearchHit {
	if db == nil || len(query) == 0 || k <= 0 || len(allowed) == 0 {
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
		if !allowed[c.Source] {
			continue
		}
		if len(c.Vector) != len(query) {
			continue
		}
		s := Cosine(query, c.Vector)
		if s <= 0 {
			continue
		}
		// Temporal decay applies to the SORT KEY only — SearchHit.Score
		// stays as the raw cosine so callers with similarity-floor
		// filters (manualSearchMinScore, dedup thresholds) get
		// predictable behavior. Recent chunks edge out stale ones on
		// ties; strong-match old chunks still surface for niche queries.
		all = append(all, scored{
			hit: SearchHit{
				ID:       c.ID,
				Source:   c.Source,
				ReportID: c.ReportID,
				Title:    c.Title,
				Section:  c.Section,
				Text:     c.Text,
				Score:    s,
				Locator:  c.Locator,
				Date:     c.Date,
				Kind:     c.Kind,
			},
			score: applyTemporalDecay(s, c.Date),
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

// SearchChunksSubstringInSources is the substring fallback for
// SearchChunksInSources — used when embeddings are disabled or fail.
// Same semantics: filter by source-set before scoring.
func SearchChunksSubstringInSources(db Database, allowed map[string]bool, query string, k int) []SearchHit {
	if db == nil || len(allowed) == 0 || k <= 0 {
		return nil
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	chunks := snapshotChunks(db)
	out := make([]SearchHit, 0, k)
	for i := range chunks {
		c := &chunks[i]
		if !allowed[c.Source] {
			continue
		}
		if !strings.Contains(strings.ToLower(c.Section+"\n"+c.Text), q) {
			continue
		}
		out = append(out, SearchHit{
			ID:       c.ID,
			Source:   c.Source,
			ReportID: c.ReportID,
			Title:    c.Title,
			Section:  c.Section,
			Text:     c.Text,
			Score:    0,
			Locator:  c.Locator,
			Date:     c.Date,
			Kind:     c.Kind,
		})
		if len(out) >= k {
			break
		}
	}
	return out
}

// SearchChunksBySource is the source-filtered cousin of SearchChunks:
// returns only hits whose Source begins with the given prefix.
// Skills use this to scope their corpus to "skill:<id>" — admin
// curation and agent knowledge stays out of the result set even when
// vectors overlap. Empty prefix matches all (same as SearchChunks).
func SearchChunksBySource(db Database, sourcePrefix string, query []float32, k int) []SearchHit {
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
		if sourcePrefix != "" && !strings.HasPrefix(c.Source, sourcePrefix) {
			continue
		}
		if len(c.Vector) != len(query) {
			continue
		}
		s := Cosine(query, c.Vector)
		if s <= 0 {
			continue
		}
		// Temporal decay applies to the SORT KEY only — SearchHit.Score
		// stays as the raw cosine so callers with similarity-floor
		// filters (manualSearchMinScore, dedup thresholds) get
		// predictable behavior. Recent chunks edge out stale ones on
		// ties; strong-match old chunks still surface for niche queries.
		all = append(all, scored{
			hit: SearchHit{
				ID:       c.ID,
				Source:   c.Source,
				ReportID: c.ReportID,
				Title:    c.Title,
				Section:  c.Section,
				Text:     c.Text,
				Score:    s,
				Locator:  c.Locator,
				Date:     c.Date,
				Kind:     c.Kind,
			},
			score: applyTemporalDecay(s, c.Date),
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

// CountChunksBySource returns the number of chunks whose Source has
// the given prefix. Cheap walk over the snapshot; used for skill
// corpus stats ("47 chunks in pickleball's knowledge").
func CountChunksBySource(db Database, sourcePrefix string) int {
	if db == nil || sourcePrefix == "" {
		return 0
	}
	chunks := snapshotChunks(db)
	n := 0
	for i := range chunks {
		if strings.HasPrefix(chunks[i].Source, sourcePrefix) {
			n++
		}
	}
	return n
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
			ID:       c.ID,
			Source:   c.Source,
			ReportID: c.ReportID,
			Title:    c.Title,
			Section:  c.Section,
			Text:     c.Text,
			Score:    1,
			Locator:  c.Locator,
			Date:     c.Date,
		})
		if len(out) >= k {
			break
		}
	}
	return out
}
