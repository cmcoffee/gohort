package core

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cmcoffee/gohort/core/sourcehooks"
)

// DESIGN NOTE — no temporal decay in this store. An earlier version decayed
// every chunk's sort key by age (180d half-life), which violated the
// corpus-exempt principle: a year-old AUTHORITATIVE upload sorted at ~×0.25
// and fell out of the candidate pool behind fresh derived chatter, and
// self-saved findings then got decayed a SECOND time by the app layer's
// recency re-rank. Recency is a memory-layer concern, not a corpus concern:
// callers that want it apply MemoryProvenance.RecencyMultiplier to the
// layers where age means drift (see rerankFindingsByRecency in
// apps/orchestrate), governed by the tune_recency_weight tunable. Curated
// knowledge, collections, skill corpora, and conversation history rank on
// relevance alone — don't re-add a blanket decay here.

// chunkCache holds snapshots of every EmbeddedChunk row, one per Database,
// so SearchChunks/SearchChunksSubstring can scan a Go slice instead of
// re-deserializing every chunk from kvlite per query. Lazy-loaded on first
// read; invalidated (whole cache) on every IngestReport / DeleteReportChunks
// so the next read rebuilds.
//
// It is a MAP keyed by the Database handle, not a single slot: one recall
// touches both the shared VectorDB (findings/knowledge) and the per-user
// store (history archive) back-to-back, and a single-slot cache thrashed —
// two full N×kvlite-get rebuilds per recall, brutal when the store sits on
// NFS. Handle interning (core/database.go dbHandles) is what makes the key
// stable: UserDB/Bucket/Sub chains now return pointer-identical handles.
// Capped at a handful of Databases, LRU-evicted, since each snapshot holds a
// full chunk corpus in memory.
//
// At gohort scale (thousands of chunks, low write rate, interactive reads)
// invalidate-on-write + lazy-rebuild is the right trade: the cost of a write
// is 1 extra db.Keys walk per cached Database on the next read, and reads
// drop from N×gob-decode to a single slice walk.
const chunkCacheMaxDBs = 8

var chunkCache struct {
	mu      sync.RWMutex
	entries map[Database]*chunkCacheEntry
	tick    uint64 // monotonic use-counter driving LRU eviction
}

type chunkCacheEntry struct {
	chunks   []EmbeddedChunk
	lastUsed uint64 // chunkCache.tick at last snapshot; guarded by chunkCache.mu
}

// snapshotChunks returns the cached chunk slice for db, rebuilding from
// kvlite on a miss. The returned slice is owned by the cache — callers must
// NOT mutate it.
func snapshotChunks(db Database) []EmbeddedChunk {
	chunkCache.mu.RLock()
	e := chunkCache.entries[db]
	chunkCache.mu.RUnlock()
	if e == nil {
		return rebuildChunkCache(db)
	}
	// Bump recency under the write lock (cheap; the map hit above is the hot
	// path and stays under the read lock).
	chunkCache.mu.Lock()
	if cur := chunkCache.entries[db]; cur != nil { // may have been invalidated since
		chunkCache.tick++
		cur.lastUsed = chunkCache.tick
		chunks := cur.chunks
		chunkCache.mu.Unlock()
		return chunks
	}
	chunkCache.mu.Unlock()
	return rebuildChunkCache(db)
}

// rebuildChunkCache loads db's full chunk table and installs it in the cache,
// evicting the least-recently-used snapshot when the cache is at capacity.
func rebuildChunkCache(db Database) []EmbeddedChunk {
	chunkCache.mu.Lock()
	defer chunkCache.mu.Unlock()
	if e := chunkCache.entries[db]; e != nil { // raced with another rebuilder
		chunkCache.tick++
		e.lastUsed = chunkCache.tick
		return e.chunks
	}
	var chunks []EmbeddedChunk
	for _, key := range db.Keys(EmbeddedChunks) {
		var c EmbeddedChunk
		if db.Get(EmbeddedChunks, key, &c) {
			chunks = append(chunks, c)
		}
	}
	if chunkCache.entries == nil {
		chunkCache.entries = make(map[Database]*chunkCacheEntry, chunkCacheMaxDBs)
	}
	for len(chunkCache.entries) >= chunkCacheMaxDBs {
		var oldest Database
		var oldestUsed uint64 = ^uint64(0)
		for d, e := range chunkCache.entries {
			if e.lastUsed < oldestUsed {
				oldest, oldestUsed = d, e.lastUsed
			}
		}
		delete(chunkCache.entries, oldest)
	}
	chunkCache.tick++
	chunkCache.entries[db] = &chunkCacheEntry{chunks: chunks, lastUsed: chunkCache.tick}
	return chunks
}

// ChunksForSource returns every stored chunk whose Source exactly matches —
// used to read out one namespace's knowledge (e.g. to copy a phantom chat's
// per-chat knowledge onto a migrated agent). Returns a fresh slice.
func ChunksForSource(db Database, source string) []EmbeddedChunk {
	if db == nil || source == "" {
		return nil
	}
	var out []EmbeddedChunk
	for _, c := range snapshotChunks(db) {
		if c.Source == source {
			out = append(out, c)
		}
	}
	return out
}

// invalidateChunkCache drops every cached snapshot so the next read per
// Database rebuilds. Whole-cache invalidation keeps the write sites simple
// (they don't all know which handle a row was written through); writes are
// rare relative to reads, so the occasional multi-DB rebuild is fine.
func invalidateChunkCache() {
	chunkCache.mu.Lock()
	chunkCache.entries = nil
	chunkCache.mu.Unlock()
}

// invalidateChunkCacheFor drops ONE Database's snapshot, which is all a write
// to that Database can invalidate.
//
// Dropping every entry was the original behavior and it is a rebuild of every
// other cached corpus, charged to whoever sends the next message — the same
// shape as the cold-start cost fixed by WarmChunkCache, and one that warming
// cannot help because it recurs on every write. Invisible on a deployment
// whose writes are rare; on one that saves knowledge steadily it is a recall
// that is slow "sometimes", for no reason anybody can see.
//
// Scoping is CORRECT and not merely cheaper: a snapshot is built by walking
// db.Keys(EmbeddedChunks) on one handle, so it contains that handle's rows and
// nothing else, and a write through a different handle cannot appear in it.
// Handle interning (core/database.go dbHandles) is what makes that safe — the
// same logical store reached twice is the same pointer, so a write and a read
// of one corpus always agree on the key.
//
// A nil db means the caller could not name what it changed; the safe answer
// there is still all of them.
func invalidateChunkCacheFor(db Database) {
	if db == nil {
		invalidateChunkCache()
		return
	}
	chunkCache.mu.Lock()
	delete(chunkCache.entries, db)
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
	// Ord is the chunk's 1-based position within its parent document, in the
	// order the ingest produced it. It exists because nothing else records
	// that order: IDs are random UUIDs, every chunk of a report shares one
	// Date, and section headings sort alphabetically — so a reassembled
	// document read Background, Conclusion, Overview, and a long unstructured
	// upload read part 1, 10, 11, 2. Zero on rows ingested before the field
	// existed; SortChunksForAssembly falls back to a natural sort for those.
	Ord int `json:"ord,omitempty"`
	// MemoryProvenance is reserved: the vector layer has no retirement pass yet, so
	// these fields sit unset. Embedded now so a future chunk-staleness or
	// supersession pass inherits the same vocabulary as the fact store. Zero value
	// = live, unknown origin, never decays; all fields omitempty. Note the existing
	// Date string above is a separate, pre-existing ingestion stamp; a later pass
	// can migrate it into the typed AsOf.
	MemoryProvenance
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

// The embed chunk-size limit caps how large a single chunk can be before it
// gets sub-split. Sized conservatively for 512-token embedding-server batch
// limits. The 4-chars/token rule of thumb breaks down on dense technical
// content (PDFs with tables, code blocks, RFC-style citation blocks,
// math-heavy text) where the ratio dips to ~2 chars/token — so a 1800-char
// chunk could be 900 tokens, well past the 512 cap on a constrained embedding
// server. The default (DefaultChunkChars, 1000) targets ~250-500 tokens across
// content densities, with embedWithRetry below handling the rare overflow.
// Operator-tunable via ChunkChars() (admin Site Settings); applies to NEW
// ingestions only — existing chunks keep the size they were split at.

// SplitReportIntoChunks splits a synthesized report's body at `## section`
// boundaries. The opening (everything before the first `##`) becomes
// one chunk labeled "Overview"; every subsequent `## Header` becomes a
// chunk labeled with that header. The "## Sources" section is dropped —
// in a synthesized report it's a bibliography, not semantic content.
// Empty or all-whitespace sections are skipped. Documents ingested as
// written go through splitReport with the drop off; see there.
//
// Oversized sections (> the chunk-size limit) are sub-split at paragraph
// boundaries — a single overlong section can't blow past the
// embedder's batch limit. Sub-chunks inherit the section name with a
// " (part N)" suffix so the retrieval payload still attributes the
// content to its parent heading.
func SplitReportIntoChunks(report string) []struct{ Section, Text string } {
	return splitReport(report, true)
}

// splitReport is the chunker behind SplitReportIntoChunks, with the
// bibliography drop as a choice. dropBibliography is for a synthesized
// REPORT (research, debate, a dispatched agent's delivery), whose trailing
// "## Sources" is a list of links the pipeline wrote and nobody searches
// for. A DOCUMENT ingested as written — an upload, a fetched page, a saved
// finding — keeps every section, because a section a user titled "Sources"
// is content: a guide's list of data sources, a chapter on sourcing. The
// drop used to apply to both, and an upload lost that section silently.
func splitReport(report string, dropBibliography bool) []struct{ Section, Text string } {
	report = strings.TrimSpace(report)
	if report == "" {
		return nil
	}
	cc := ChunkChars() // operator-tunable chunk-size limit; read once per ingestion
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
		// Drop a report's bibliography — not semantic content to search over.
		if dropBibliography && strings.EqualFold(section, "Sources") {
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
		parts := splitOnParagraphsCap(text, cc)
		multi := len(parts) > 1 // single-chunk sections stay un-suffixed
		for i, part := range parts {
			name := section
			if len(part) > 0 && i > 0 {
				name = section + fmt.Sprintf(" (part %d)", i+1)
			}
			if i == 0 && len(part) > 0 && multi {
				name = section + " (part 1)"
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
				// Back the cut off any multi-byte rune it lands inside, so a
				// chunk never ends mid-character. cap is large (chunk-chars), so
				// the fallback to a raw byte cut only trips for a pathological
				// cap smaller than a single rune.
				cut := cap
				for cut > 0 && !utf8.RuneStart(p[cut]) {
					cut--
				}
				if cut == 0 {
					cut = cap
				}
				out = append(out, p[:cut])
				p = p[cut:]
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

// IngestReport chunks the given DOCUMENT as written, embeds each chunk, and
// stores the results in the vector store tagged with the given source label
// (app-provided origin tag — the app decides what string to pass). Every
// section is kept, a "## Sources" one included; a synthesized report whose
// Sources is a bibliography goes through IngestReportTitled, which drops it.
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
	ingestReport(ctx, db, source, reportID, "", report, kind, false)
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
		invalidateChunkCacheFor(db)
	}
	return updated
}

// IngestDocument ingests a DOCUMENT as written — an upload, a pasted note, a
// fetched page — with its human name stamped as the Title on every chunk, so
// a hit is labelled by what the document is rather than by its first
// heading. Every section is kept, a "## Sources" one included. The reportID
// is the document's handle: ingesting again under the same one REPLACES it,
// which is what lets a pasted note be updated in place. Returns the number
// of chunk rows stored.
func IngestDocument(ctx context.Context, db Database, source, reportID, title, body string) int {
	return ingestReport(ctx, db, source, reportID, title, body, "", false)
}

// IngestReportTitled ingests a synthesized REPORT — a debate verdict, a
// research synthesis, a dispatched agent's delivery — with a document Title,
// the human-meaningful name of the parent record (the debate topic, the
// research question) stamped onto every chunk. Without it, browsers and
// recall can only show a chunk's section heading ("Verdict", "Executive
// Summary"), which is meaningless without knowing what document it came
// from. Being a report, its trailing "## Sources" bibliography is dropped
// (see splitReport); a document ingested as written goes through
// IngestReport / IngestReportTagged / IngestPagedReport instead.
//
// Returns the number of chunk rows stored (0 = nothing indexed). Rows are
// stored even when their embedding fails — an unvectored chunk still serves
// keyword search — so 0 means the ingest itself didn't happen, not that the
// embedder was down. Callers that must not lose the content (the compaction
// archive) treat 0-for-non-empty-input as a failure.
func IngestReportTitled(ctx context.Context, db Database, source, reportID, title, report, kind string) int {
	return ingestReport(ctx, db, source, reportID, title, report, kind, true)
}

// ingestReport is the one ingest behind the public entry points; isReport
// selects the bibliography drop (see splitReport).
func ingestReport(ctx context.Context, db Database, source, reportID, title, report, kind string, isReport bool) int {
	if db == nil || reportID == "" {
		return 0
	}
	// Defense-in-depth: servitor handles SSH credentials, system facts,
	// and other sensitive per-appliance data that must never be indexed
	// into the deployment-wide knowledge store. Refuse the ingest if a
	// caller ever wires it up by mistake.
	if source == "servitor" {
		Debug("[vector] refusing to ingest source=servitor (sensitive data — must stay in app)")
		return 0
	}
	// Remove any existing chunks for this report — re-ingestion on
	// resynth should replace, not duplicate.
	DeleteReportChunks(db, reportID)

	chunks := splitReport(report, isReport)
	if len(chunks) == 0 {
		Debug("[vector] no chunks extracted for %s/%s", source, reportID)
		return 0
	}
	cfg := GetEmbeddingConfig()
	now := time.Now().Format(time.RFC3339)
	var embedded, empty, split, ord int
	for _, c := range chunks {
		// embedWithSplitFallback handles the case where a single chunk,
		// even after the chunker's defensive cap, still exceeds the
		// embedder's per-call token limit. The fallback recursively
		// halves the text until each piece embeds successfully OR
		// returns empty for pieces that fail for other reasons.
		pieces := embedWithSplitFallback(ctx, cfg, embedHeader(title, c.Section), c.Text)
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
			ord++
			row := EmbeddedChunk{
				ID:       UUIDv4(),
				Source:   source,
				ReportID: reportID,
				Title:    title,
				Section:  sect,
				Text:     p.Text,
				Vector:   p.Vector,
				Model:    cfg.spaceStamp(),
				Date:     now,
				Kind:     kind,
				Ord:      ord,
			}
			db.Set(EmbeddedChunks, row.ID, row)
		}
	}
	invalidateChunkCacheFor(db)
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
	return embedded + empty
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
	var totalChunks, embedded, empty, split, ord int
	for i, pageText := range pages {
		pageText = strings.TrimSpace(pageText)
		if pageText == "" {
			continue
		}
		pageNum := i + 1
		locator := fmt.Sprintf("page %d", pageNum)
		chunks := splitReport(pageText, false) // a document, as written
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
				ord++
				row := EmbeddedChunk{
					ID:       UUIDv4(),
					Source:   source,
					ReportID: reportID,
					Section:  sect,
					Text:     p.Text,
					Vector:   p.Vector,
					Model:    cfg.spaceStamp(),
					Date:     now,
					Locator:  locator,
					Ord:      ord,
				}
				db.Set(EmbeddedChunks, row.ID, row)
			}
		}
	}
	invalidateChunkCacheFor(db)
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

// embedHeader is the context line prefixed to a chunk's text when it is
// embedded: the parent document's title and the section heading. The title
// is there because a chunk's own words rarely name the document they belong
// to — the "Overview" section of a firewall guide need not say "firewall" —
// and without it a query about the document lands on nothing. Empty title
// leaves the header as the bare section, so pre-Title ingests embed as they
// always did.
func embedHeader(title, section string) string {
	title = strings.TrimSpace(title)
	if title == "" || strings.EqualFold(title, strings.TrimSpace(section)) {
		return section
	}
	return title + "\n" + section
}

// embedWithSplitFallback embeds a chunk's text, falling back to
// recursive half-splitting when the embedder rejects the input as too
// large. Returns one piece per successful (or final-failed) embed call.
// On non-size errors (network, decode, server outage) the function
// stops splitting and returns a single piece with an empty vector so
// the row still lands in the index with its raw text (recoverable via
// re-embed later). header is the context prefix (see embedHeader) —
// it rides on every piece but is never stored as chunk text.
func embedWithSplitFallback(ctx context.Context, cfg EmbeddingConfig, header, text string) []embedPiece {
	return embedWithSplitFallbackDepth(ctx, cfg, header, text, 0)
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
	v, err := embedDocumentWith(ctx, cfg, prompt)
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
	invalidateChunkCacheFor(db)
}

// DeleteChunksWhere removes every chunk keep accepts and invalidates the
// read cache when anything went. Returns the number removed. THE way to
// delete chunks by any rule — a report, a source prefix, a scope, one id.
//
// It reads the cached snapshot rather than walking kvlite, so a delete costs
// a slice scan plus one Unset per victim instead of a Keys walk and a gob
// decode per row in the store (brutal on NFS, and every hand-rolled delete
// paid it). And it invalidates, which is the part the hand-rolled ones got
// wrong: four of them Unset rows and never told the cache, and the helper
// they called instead was a no-op whose comment assumed a TTL this cache
// does not have. A deleted chunk kept surfacing in search until some
// unrelated ingest happened to invalidate.
//
// Rows are addressed by their ID, which every writer uses as the kvlite key.
func DeleteChunksWhere(db Database, keep func(c EmbeddedChunk) bool) int {
	if db == nil || keep == nil {
		return 0
	}
	removed := 0
	for _, c := range snapshotChunks(db) { // read-only; owned by the cache
		if !keep(c) {
			continue
		}
		db.Unset(EmbeddedChunks, c.ID)
		removed++
	}
	if removed > 0 {
		invalidateChunkCacheFor(db)
	}
	return removed
}

// DeleteReportChunks removes every chunk belonging to the given report.
// Called on re-ingestion (before re-insert) and on record deletion
// (cleanup). Silent no-op on nil DB.
func DeleteReportChunks(db Database, reportID string) {
	if db == nil || reportID == "" {
		return
	}
	DeleteChunksWhere(db, func(c EmbeddedChunk) bool { return c.ReportID == reportID })
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
	invalidateChunkCacheFor(db)
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
	return DeleteChunksWhere(db, func(c EmbeddedChunk) bool { return strings.HasPrefix(c.Source, prefix) })
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
	invalidateChunkCacheFor(VectorDB)
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

	// EmptyBySource narrows Empty to WHERE the gap is. A bare "Empty: 412"
	// says an outage happened but not what it cost; the same 412 spread over
	// every source is a config problem, while 412 concentrated in one is one
	// import worth re-running. Rendered the same way, for the same reason.
	EmptyBySource     map[string]int `json:"empty_by_source"`
	EmptyBySourceText string         `json:"empty_by_source_text"`
	// Stale counts chunks that HAVE a vector but not in the current
	// embedding space: stamped with another model or document prefix, or
	// not stamped at all. Semantic search skips the stamped ones and
	// compares the unstamped ones across spaces; the stale re-embed pass
	// is the repair. Zero when no model name is configured, since then
	// there is no space to be outside of.
	Stale             int            `json:"stale"`
	StaleBySource     map[string]int `json:"stale_by_source"`
	StaleBySourceText string         `json:"stale_by_source_text"`
}

// VectorStats walks the EmbeddedChunks table once and summarizes how
// many chunks are stored, how many have real vectors vs fell back to
// empty (because embed was down at ingest time), and the breakdown per
// source. Intended for admin-panel visibility — not hot-path.
func VectorStats(db Database) VectorIndexStats {
	stats := VectorIndexStats{BySource: map[string]int{}, EmptyBySource: map[string]int{}, StaleBySource: map[string]int{}}
	if db == nil {
		return stats
	}
	space := currentEmbedModel()
	for _, c := range snapshotChunks(db) {
		stats.Total++
		src := c.Source
		if src == "" {
			src = "(unspecified)"
		}
		if len(c.Vector) > 0 {
			stats.Embedded++
			if space != "" && c.Model != space {
				stats.Stale++
				stats.StaleBySource[src]++
			}
		} else {
			stats.Empty++
			stats.EmptyBySource[src]++
		}
		stats.BySource[src]++
	}
	stats.BySourceText = formatSourceCounts(stats.BySource)
	stats.EmptyBySourceText = formatSourceCounts(stats.EmptyBySource)
	stats.StaleBySourceText = formatSourceCounts(stats.StaleBySource)
	return stats
}

// formatSourceCounts renders a source→count map as a stable, source-sorted
// "src=N, src2=M" string. Empty map renders empty, not "0" — the caller
// decides what "nothing here" should read as.
func formatSourceCounts(m map[string]int) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

// MaintenanceFunc is a named one-shot repair function registered by a
// private package at init time. The admin UI can trigger any registered
// function by key. Returns the number of records modified.
type MaintenanceFunc struct {
	// Group is the admin section the button appears under. Declared by the
	// registrant, never inferred from the key: "Vector index" (repairs of
	// the search index, shown beside its stats), "Reclaim space" (dry runs
	// and the deletes that act on them), "Reports" (read-only surveys), or
	// "Housekeeping" (everything else). An unknown group lands under
	// Housekeeping rather than vanishing.
	Group string
	Key   string
	Label string
	Desc  string
	Run   func(ctx context.Context) int
}

var maintenanceFuncs []MaintenanceFunc

// RegisterMaintenanceFunc registers a named maintenance function for the
// admin panel, under the given group (see MaintenanceFunc.Group). Called
// from package init() functions.
func RegisterMaintenanceFunc(group, key, label, desc string, fn func(ctx context.Context) int) {
	maintenanceFuncs = append(maintenanceFuncs, MaintenanceFunc{Group: group, Key: key, Label: label, Desc: desc, Run: fn})
}

// ListMaintenanceFuncs returns metadata for all registered maintenance funcs.
func ListMaintenanceFuncs() []struct{ Group, Key, Label, Desc string } {
	out := make([]struct{ Group, Key, Label, Desc string }, len(maintenanceFuncs))
	for i, m := range maintenanceFuncs {
		out[i] = struct{ Group, Key, Label, Desc string }{Group: m.Group, Key: m.Key, Label: m.Label, Desc: m.Desc}
	}
	return out
}

// RunMaintenanceFunc runs the maintenance function matching key. Returns -1 if
// not found.
func RunMaintenanceFunc(ctx context.Context, key string) int {
	for _, m := range maintenanceFuncs {
		if m.Key == key {
			return m.Run(ctx)
		}
	}
	return -1
}

// ChunksWhere returns every chunk the predicate keeps, served from the
// in-process snapshot cache — the read-path replacement for raw Keys+Get
// table walks (N random kvlite gets per call, the NFS cold-read pathology the
// chunk cache exists to kill). Returned values are copies of cache rows;
// mutate freely, but write back through the normal Set/Delete paths so the
// cache invalidates.
func ChunksWhere(db Database, keep func(c EmbeddedChunk) bool) []EmbeddedChunk {
	if db == nil || keep == nil {
		return nil
	}
	chunks := snapshotChunks(db)
	var out []EmbeddedChunk
	for i := range chunks {
		if keep(chunks[i]) {
			out = append(out, chunks[i])
		}
	}
	return out
}

// chunkVectorComparable reports whether a chunk's cached vector may be
// cosine-compared against a query embedded in the CURRENT space: dimensions
// must match, and a chunk stamped with a DIFFERENT embedding model is skipped
// rather than compared across spaces (a same-dimension model swap otherwise
// produces silently-garbage similarity; re-ingest re-embeds the chunk).
// Chunks with an empty Model (single-model backends, legacy rows) and
// deployments with no configured model name are grandfathered — a
// same-endpoint model swap there is undetectable, see EmbedVersion.
//
// model is the current embedding model, read ONCE by the caller through
// currentEmbedModel before its scan. This function used to read it itself,
// which put a config read — a lock, a peer-registry lookup and the
// once-warnings around it — inside a loop that runs once per chunk in the
// store, on every query, for a value that cannot change mid-scan.
func chunkVectorComparable(c *EmbeddedChunk, query []float32, model string) bool {
	if len(c.Vector) != len(query) {
		return false
	}
	return c.Model == "" || model == "" || c.Model == model
}

// currentEmbedModel is the space stamp a scan compares chunk stamps against
// (EmbeddingConfig.spaceStamp of the resolved config, so a peer-served
// embedder reports the model the peer advertises, plus any document prefix).
func currentEmbedModel() string {
	return GetEmbeddingConfig().spaceStamp()
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
	model := currentEmbedModel()
	var all []SearchHit
	for i := range chunks {
		c := &chunks[i]
		if !chunkVectorComparable(c, query, model) {
			continue
		}
		s := Cosine(query, c.Vector)
		if s <= 0 {
			continue
		}
		all = append(all, SearchHit{
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
		})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Score > all[j].Score })
	if k > len(all) {
		k = len(all)
	}
	return all[:k]
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
	model := currentEmbedModel()
	var all []SearchHit
	for i := range chunks {
		c := &chunks[i]
		if !allow(*c) {
			continue
		}
		if !chunkVectorComparable(c, query, model) {
			continue
		}
		s := Cosine(query, c.Vector)
		if s <= 0 {
			continue
		}
		all = append(all, SearchHit{
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
		})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Score > all[j].Score })
	if k > len(all) {
		k = len(all)
	}
	return all[:k]
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
		if !strings.Contains(strings.ToLower(lexicalText(c)), q) {
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

// lexicalText is the text a chunk exposes to the keyword and substring
// scans: the parent document's title, the section heading, and the body.
// The title is included for the same reason embedHeader includes it — the
// name of a document is the one thing its chunks are least likely to
// repeat, and a query that names the document ("the OPNsense guide")
// matched nothing while the scan looked only at section and body.
func lexicalText(c *EmbeddedChunk) string {
	return c.Title + "\n" + c.Section + "\n" + c.Text
}

// keywordMinChars is the shortest query token the keyword half keeps. Two,
// not three: the identifiers this half exists to catch are often two
// characters — Go, S3, an IP, a VM, a DB — and a three-character floor
// dropped exactly those from a query about them, leaving the embedding to
// find "go" on its own, which it does not. Single characters stay out: "c"
// and "r" are languages, but one letter matches too much of any text.
const keywordMinChars = 2

// keywordWholeWordMax is the longest term matched as a whole word rather
// than a substring. Short terms are substrings of too many longer words —
// "go" is in "good" and "ago", "ram" in "program", "cat" in "category" —
// so up to this length a term must stand on its own between non-word
// characters. Longer terms keep substring matching, which doubles as cheap
// prefix stemming ("firewall" finds "firewalls"). The cost is that "sql"
// no longer finds "mysql"; a reader who means that can say it.
const keywordWholeWordMax = 3

// keywordShortStopwords are the function words admitting two-letter tokens
// lets through. The shared sourcehooks.Stopwords list is short on purpose —
// it normalizes cache keys, where dropping a real word is worse than keeping
// a filler — and every two-letter word it lacks was previously excluded by
// the length floor. This list keeps that exclusion for the fillers only.
var keywordShortStopwords = map[string]bool{
	"am": true, "be": true, "do": true, "he": true, "if": true, "it": true,
	"me": true, "my": true, "no": true, "so": true, "up": true, "us": true,
	"we": true, "vs": true,
}

// keywordTerms tokenizes a query into distinct content terms for lexical
// matching: lowercased, split on non-alphanumerics, stopwords + single
// characters dropped, deduped. Empty when the query is all stopwords/
// punctuation.
func keywordTerms(query string) []string {
	fields := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	seen := map[string]bool{}
	var out []string
	for _, f := range fields {
		if len(f) < keywordMinChars || sourcehooks.Stopwords[f] || keywordShortStopwords[f] || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// termMatches reports whether lowercased chunk text contains the query
// term: as a whole word when the term is short (see keywordWholeWordMax),
// as a substring otherwise.
func termMatches(lt, term string) bool {
	if len(term) > keywordWholeWordMax {
		return strings.Contains(lt, term)
	}
	return containsWholeWord(lt, term)
}

// containsWholeWord reports whether s contains w with a non-word character
// (or the text's edge) on BOTH sides. Distinct from containsWord in
// machine_def.go, which checks only the leading edge — enough for an
// advisory that should err toward firing, wrong here, where "go" at the
// start of "good" is exactly the match to refuse. A byte outside ASCII
// counts as a word character, since it is a piece of a letter.
func containsWholeWord(s, w string) bool {
	joins := func(b byte) bool { return isWordByte(b) || b >= 0x80 }
	for start := 0; ; {
		i := strings.Index(s[start:], w)
		if i < 0 {
			return false
		}
		i += start
		end := i + len(w)
		if (i == 0 || !joins(s[i-1])) && (end == len(s) || !joins(s[end])) {
			return true
		}
		start = i + 1
	}
}

// SearchChunksKeywordByPredicate ranks chunks by LEXICAL overlap with the query
// — the keyword half of hybrid search, so an exact term the embedding glosses
// over (a product name, an acronym, an identifier like "OPNsense") still
// surfaces. Coverage is IDF-WEIGHTED over the allowed chunk set: matching a
// term that appears in few chunks (the identifier this function exists for)
// carries most of the query's weight, while matching only a term half the
// corpus contains carries almost none. Score = 0.85 × weighted coverage, so a
// full-coverage hit still lands at 0.85 (cosine-comparable, same ceiling as
// before) but the scale is HONEST from zero: the old form (0.35 + 0.5·cov)
// started every one-term hit above the 0.35 similarity floors, so the floors
// filtered nothing on the keyword half and tangential single-common-term hits
// leaked through. Now a hit passes a 0.35 floor only when the matched terms
// carry ≥~41% of the query's IDF mass. Nil when the query has no usable terms.
func SearchChunksKeywordByPredicate(db Database, allow func(c EmbeddedChunk) bool, query string, k int) []SearchHit {
	if db == nil || allow == nil || k <= 0 {
		return nil
	}
	terms := keywordTerms(query)
	if len(terms) == 0 {
		return nil
	}
	chunks := snapshotChunks(db)
	// Pass 1: per-chunk term matches + document frequency per term, one text
	// scan per chunk. Only candidates that matched something are kept.
	type cand struct {
		idx     int
		matched []bool
	}
	df := make([]int, len(terms))
	var cands []cand
	allowed := 0
	for i := range chunks {
		c := &chunks[i]
		if !allow(*c) {
			continue
		}
		allowed++
		lt := strings.ToLower(lexicalText(c))
		var matched []bool
		any := false
		for j, t := range terms {
			if termMatches(lt, t) {
				if matched == nil {
					matched = make([]bool, len(terms))
				}
				matched[j] = true
				df[j]++
				any = true
			}
		}
		if any {
			cands = append(cands, cand{idx: i, matched: matched})
		}
	}
	if len(cands) == 0 {
		return nil
	}
	// Pass 2: IDF weights from the observed frequencies, then score each
	// candidate by the fraction of total query weight its matches carry.
	// Terms NO allowed chunk contains are excluded from the denominator:
	// they can't discriminate between candidates, and (being rarest) they'd
	// otherwise carry the largest weight — one filler word or typo in the
	// query would dilute every real hit under the similarity floor,
	// including the lone rare-identifier match this search exists to catch.
	idf := make([]float64, len(terms))
	var totalW float64
	for j := range terms {
		if df[j] == 0 {
			continue
		}
		idf[j] = math.Log(1 + float64(allowed)/float64(1+df[j]))
		totalW += idf[j]
	}
	if totalW <= 0 {
		return nil
	}
	// idfMax is the weight a term appearing in exactly ONE allowed chunk
	// carries: the most discriminating any matched term can be here.
	//
	// It exists because coverage alone is a RATIO, and a ratio cannot tell a
	// precise query from a vague one. A single-term query matches 100% of its
	// own weight whatever that term is, so "firewall" against a corpus where
	// nine chunks in ten say "firewall" scored 0.85 — a perfect match, on the
	// least informative word available, for every one of them. That is the
	// shape of "search returns everything": not a ranking failure, a scale
	// that reports agreement with the query instead of evidence about the
	// corpus.
	//
	// Specificity is the second factor: how rare the matched terms actually
	// are, measured against that ceiling and independent of what else the
	// query contained. A rare identifier keeps the full score; a ubiquitous
	// word is discounted toward zero no matter how completely it matched.
	idfMax := math.Log(1 + float64(allowed)/2)
	all := make([]SearchHit, 0, len(cands))
	for _, cd := range cands {
		var w float64
		matchedN := 0
		for j, m := range cd.matched {
			if m {
				w += idf[j]
				matchedN++
			}
		}
		spec := 1.0
		if matchedN > 0 && idfMax > 0 {
			spec = (w / float64(matchedN)) / idfMax
			if spec > 1 {
				spec = 1
			}
		}
		c := &chunks[cd.idx]
		s := float32(0.85 * (w / totalW) * spec)
		all = append(all, SearchHit{
			ID: c.ID, Source: c.Source, ReportID: c.ReportID, Title: c.Title,
			Section: c.Section, Text: c.Text, Score: s,
			Locator: c.Locator, Date: c.Date, Kind: c.Kind,
		})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Score > all[j].Score })
	if k > len(all) {
		k = len(all)
	}
	return all[:k]
}

// HybridSearchByPredicate fuses semantic (vector) and lexical (keyword) recall:
// it runs both and merges by score, so a chunk strong in EITHER signal makes the
// top-k. This catches the failure mode of pure vector search — an exact term
// (identifier, acronym, product name) the embedding semantically near-misses —
// without giving up semantic recall. Falls back to keyword-only when no
// embedding is available (vec empty). queryText is the raw query (keyword
// extraction); vec is its embedding.
//
// The result is diversified by document (diversifyHits): each half is asked
// for a wider pool than k so that a second relevant document has a chance to
// be in it at all — with a pool of exactly k, one long document's chunks fill
// the pool and there is nothing to diversify with.
func HybridSearchByPredicate(db Database, allow func(c EmbeddedChunk) bool, queryText string, vec []float32, k int) []SearchHit {
	if db == nil || allow == nil || k <= 0 {
		return nil
	}
	perDoc := recallPerDocMax()
	pool := k
	if perDoc > 0 {
		pool = k * diversifyPoolFactor
	}
	var hits []SearchHit
	if len(vec) == 0 {
		hits = SearchChunksKeywordByPredicate(db, allow, queryText, pool)
	} else {
		hits = MergeHitsByScore(
			SearchChunksByPredicate(db, allow, vec, pool),
			SearchChunksKeywordByPredicate(db, allow, queryText, pool),
			pool)
	}
	return diversifyHits(hits, perDoc, k)
}

// diversifyPoolFactor is how many times k the candidate pool is when
// diversifying. The primitives sort every scored candidate before slicing,
// so a wider slice costs nothing; four is enough that a second document with
// one strong passage is in the pool behind a first document's top dozen.
const diversifyPoolFactor = 4

// diversifyHits re-ranks score-sorted hits so that no document holds more
// than perDoc of the leading slots while another document still has a
// passage worth reading, then truncates to k.
//
// It exists because top-k by score is top-k by CHUNK, and a long document
// that matches a query matches it in several places: six slots, six passages
// from the one guide, and the other guide that answered the question in a
// single paragraph was never shown. Diversity here is a matter of ORDER, not
// exclusion — a document's extra passages are demoted behind other documents'
// first ones and then fill whatever slots remain, so a corpus of one document
// still returns k passages from it.
//
// A passage from a new document is promoted ahead of a demoted extra only
// when it clears RelevanceFloor. The callers apply that floor (or
// their own) AFTER this, and a below-floor passage promoted into the top-k
// would be dropped there, costing the slot the demoted extra would have kept
// — so a passage that is not worth reading is never promoted over one that
// is. perDoc <= 0 disables the re-rank. Hits with no ReportID belong to no
// document and are never capped.
func diversifyHits(hits []SearchHit, perDoc, k int) []SearchHit {
	if k <= 0 {
		return nil
	}
	if perDoc <= 0 || len(hits) <= 1 {
		if len(hits) > k {
			hits = hits[:k]
		}
		return hits
	}
	seen := make(map[string]int, len(hits))
	lead := make([]SearchHit, 0, k)
	var extras []SearchHit
	for _, h := range hits {
		if h.ReportID == "" || (seen[h.ReportID] < perDoc && h.Score >= RelevanceFloor) {
			seen[h.ReportID]++
			lead = append(lead, h)
			continue
		}
		extras = append(extras, h)
	}
	out := append(lead, extras...)
	if len(out) > k {
		out = out[:k]
	}
	return out
}

// MergeHitsByScore unions two hit lists, dedups by chunk ID keeping the
// HIGHER score, sorts by descending score, and caps at k. Used to fuse the
// vector and keyword halves of a hybrid search, and to fold a second-store
// pass into the primary result set. Fast-paths when either side is empty.
//
// Keeping the higher score is the whole point of hybrid recall: a chunk is
// in both lists exactly when both signals found it, and the merge used to
// keep whichever copy came first — the vector one. A chunk with cosine 0.30
// and a keyword score of 0.80 (the exact identifier the keyword half exists
// to catch) came out at 0.30 and was then dropped by the 0.35 relevance
// floor, so the strongest kind of match was the one that vanished.
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
	at := make(map[string]int, len(a)+len(b)) // chunk ID → index in merged
	for _, h := range append(append([]SearchHit{}, a...), b...) {
		if i, ok := at[h.ID]; ok {
			if h.Score > merged[i].Score {
				merged[i] = h
			}
			continue
		}
		at[h.ID] = len(merged)
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
	model := currentEmbedModel()
	var all []SearchHit
	for i := range chunks {
		c := &chunks[i]
		if !allowed[c.Source] {
			continue
		}
		if !chunkVectorComparable(c, query, model) {
			continue
		}
		s := Cosine(query, c.Vector)
		if s <= 0 {
			continue
		}
		all = append(all, SearchHit{
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
		})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Score > all[j].Score })
	if k > len(all) {
		k = len(all)
	}
	return all[:k]
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
		if !strings.Contains(strings.ToLower(lexicalText(c)), q) {
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
	model := currentEmbedModel()
	var all []SearchHit
	for i := range chunks {
		c := &chunks[i]
		if sourcePrefix != "" && !strings.HasPrefix(c.Source, sourcePrefix) {
			continue
		}
		if !chunkVectorComparable(c, query, model) {
			continue
		}
		s := Cosine(query, c.Vector)
		if s <= 0 {
			continue
		}
		all = append(all, SearchHit{
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
		})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Score > all[j].Score })
	if k > len(all) {
		k = len(all)
	}
	return all[:k]
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
		if !strings.Contains(strings.ToLower(lexicalText(c)), q) {
			continue
		}
		out = append(out, SearchHit{
			ID:       c.ID,
			Source:   c.Source,
			ReportID: c.ReportID,
			Title:    c.Title,
			Section:  c.Section,
			Text:     c.Text,
			Kind:     c.Kind,
			Score:    0,
			Locator:  c.Locator,
			Date:     c.Date,
		})
		if len(out) >= k {
			break
		}
	}
	return out
}

// WarmChunkCache builds the chunk snapshot off the critical path.
//
// The cache is process memory, lazily built on first read, so the first recall
// after every restart pays for the whole corpus — and pays it while somebody is
// waiting. Measured on a live deployment: knowledge=4.229s and 3.615s on the
// first recall after a restart, 22-86ms on every one after it, same query, same
// 12 hits. Every slow recall in that log was a first-after-restart and no other
// one was slow.
//
// Worse than slow: that first recall runs under RecallHintTimeout, so it
// blew the budget and the turn was sent WITHOUT hints. The user waited four
// seconds for a result that was then discarded — the cost of the rebuild with
// none of its benefit.
//
// Asynchronous because nothing should wait on it. If a real query arrives first
// it rebuilds inline exactly as before and this becomes a no-op that finds the
// entry already there; the two race harmlessly, which rebuildChunkCache already
// handles (it re-checks the map under the write lock).
func WarmChunkCache(db Database) {
	if db == nil {
		return
	}
	go func() {
		started := time.Now()
		n := len(snapshotChunks(db))
		// Logged rather than silent: this is the number that explains a slow
		// first turn, and if it ever grows enough to matter again the evidence
		// should already be in the log.
		Debug("[vector] chunk cache warmed: %d chunks in %s", n, time.Since(started).Round(time.Millisecond))
	}()
}
