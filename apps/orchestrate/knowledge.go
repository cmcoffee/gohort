// Per-agent knowledge store — a semantic-search layer above the
// one-line memory notes. The orchestrator and worker can deposit
// findings ("I researched X and learned Y") and recall them on
// future turns when a semantically similar question arrives.
//
// Backing store is the shared core/vector_store (embeddings + cosine
// search + substring fallback). Each (user, agent) pair gets its own
// source tag — "orchestrate:<user>:<agent_id>" — so retrieval can
// filter to the active agent without leaking across users or agents.
//
// Lifetimes:
//
//   - knowledge_save tool deposits a chunk explicitly when the worker
//     decides something is worth keeping.
//   - consolidation.go also auto-ingests the synthesis at turn close,
//     so the store grows even when the LLM forgets to call the tool.
//   - runPlan pre-search injects semantically relevant hits into the
//     orchestrator's prompt before it builds the plan.
//
// Embedding failures fall back to substring search (same pattern the
// vector store uses elsewhere), so the store is useful even when the
// embedding model is unavailable.

package orchestrate

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	RegisterTunable(TunableSpec{Key: "tune_knowledge_ingest_timeout", Category: "Timeouts", Label: "Knowledge ingest timeout", Help: "Caps any embedding round-trip during knowledge ingest/search.", Kind: KindSeconds, Default: 45, Min: 5, Max: 300})
}

// knowledgeIngestTimeout caps any embedding round-trip — long stalls
// would hang the consolidation goroutine or the user-facing tool call.
func knowledgeIngestTimeout() time.Duration { return TuneDuration("tune_knowledge_ingest_timeout") }

// knowledgeTopicsTable stores the per-(user, agent) accumulator of
// topics the classifier has seen so future classifications can prefer
// existing slugs over coining new ones. One row per (user, agentID),
// value is AgentTopics. Lives in the app DB so the chunk store and
// the topic accumulator can be inspected together.
const knowledgeTopicsTable = "orchestrate_knowledge_topics"

// AgentTopics is the on-disk record. Sorted on write so callers can
// iterate stably without re-sorting.
type AgentTopics struct {
	Topics []string `json:"topics"`
}

// generalTopic is the fallback slug when classification returns nothing
// usable. Matches Answer's convention so a deployment with both
// surfaces in flight produces compatible namespacing.
const generalTopic = "general"

// knowledgeSource returns the per-(user, agent, topic) namespace tag
// used as the EmbeddedChunk.Source field. Empty topic collapses to
// the per-(user, agent) bucket — used as the wider fallback during
// search when topic-scoped retrieval misses.
func knowledgeSource(user, agentID, topic string) string {
	base := "orchestrate:" + user + ":" + agentID
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return base
	}
	return base + ":" + topic
}

// normalizeTopic produces a snake_case [a-z0-9_.] slug usable as a
// stable namespace component. Empty or garbage inputs collapse to
// generalTopic. Mirrors Answer's classifyTopic post-processing so
// the two surfaces produce compatible namespaces.
func normalizeTopic(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	t = strings.NewReplacer(" ", "_", "-", "_", "/", "_").Replace(t)
	cleaned := make([]byte, 0, len(t))
	for i := 0; i < len(t); i++ {
		c := t[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '.' {
			cleaned = append(cleaned, c)
		}
	}
	t = string(cleaned)
	if t == "" {
		return generalTopic
	}
	return t
}

// listAgentTopics returns the topics this (user, agent) has accumulated
// so far. Caller-callable from the classifier prompt to nudge the LLM
// toward reusing existing slugs instead of inventing near-duplicates.
func listAgentTopics(db Database, user, agentID string) []string {
	if db == nil {
		return nil
	}
	key := user + ":" + agentID
	var rec AgentTopics
	if !db.Get(knowledgeTopicsTable, key, &rec) {
		return nil
	}
	return rec.Topics
}

// recordAgentTopic ensures the given topic is in the (user, agent)
// accumulator. Idempotent — does nothing when the topic is already
// present. Skipped silently when storage is unavailable.
func recordAgentTopic(db Database, user, agentID, topic string) {
	if db == nil || topic == "" {
		return
	}
	key := user + ":" + agentID
	var rec AgentTopics
	db.Get(knowledgeTopicsTable, key, &rec)
	for _, t := range rec.Topics {
		if t == topic {
			return
		}
	}
	rec.Topics = append(rec.Topics, topic)
	sort.Strings(rec.Topics)
	db.Set(knowledgeTopicsTable, key, rec)
}

// handleAgentKnowledge serves GET (count) and DELETE (wipe) for the
// per-(user, agent) vector knowledge bucket. The DELETE is the
// "nuclear" wipe — removes uploads AND auto-inferred chunks. The UI
// no longer surfaces this button directly; the Knowledge modal calls
// handleAgentKnowledgeAutoInferredWipe instead (which preserves
// uploads). Kept around for admin scripts and any legacy callers.
//
//	GET    /api/agents/{id}/knowledge → {chunks: N}
//	DELETE /api/agents/{id}/knowledge → {removed: N}
func (T *OrchestrateApp) handleAgentKnowledge(w http.ResponseWriter, r *http.Request, user, agentID string) {
	if T.DB == nil {
		http.Error(w, "DB not initialized", http.StatusInternalServerError)
		return
	}
	// Chunks live in the dedicated VectorDB now (consolidated shared
	// store, scoped by Source tag); metadata still lives in T.DB.
	switch r.Method {
	case http.MethodGet:
		n := countAgentKnowledgeChunks(VectorDB, user, agentID)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int{"chunks": n})
	case http.MethodDelete:
		removed := WipeChunksBySourcePrefix(VectorDB, agentKnowledgePrefix(user, agentID))
		Log("[orchestrate.knowledge] user=%q wiped %d chunk(s) for agent=%s",
			user, removed, agentID)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int{"removed": removed})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleAgentKnowledgeAutoInferredWipe deletes only auto-inferred
// chunks — synthesis ingest, closer findings, knowledge_save calls.
// Distinguishes by ReportID prefix:
//
//	orch-know-*    → auto-inferred (target of this wipe)
//	orch-upload-*  → user-uploaded documents (PRESERVED)
//	agent-shared-* → admin-curated shared KB (PRESERVED — different
//	                  source namespace anyway, but defensive)
//
// This is the "reset the noisy chunks but keep my carefully-curated
// uploads" button. Uploads have their own per-doc Remove for targeted
// removal.
//
//	DELETE /api/agents/{id}/knowledge/auto-inferred → {removed: N}
//	GET    same path → {chunks: N} (count of auto-inferred chunks only)
func (T *OrchestrateApp) handleAgentKnowledgeAutoInferredWipe(w http.ResponseWriter, r *http.Request, user, agentID string) {
	if T.DB == nil {
		http.Error(w, "DB not initialized", http.StatusInternalServerError)
		return
	}
	prefix := agentKnowledgePrefix(user, agentID)
	scope := func(c EmbeddedChunk) bool {
		if !strings.HasPrefix(c.Source, prefix) {
			return false
		}
		// Only auto-inferred reportIDs. Uploads + (defensive) shared
		// reportIDs are skipped.
		return strings.HasPrefix(c.ReportID, "orch-know-")
	}
	switch r.Method {
	case http.MethodGet:
		n := 0
		for _, key := range VectorDB.Keys(EmbeddedChunks) {
			var c EmbeddedChunk
			if !VectorDB.Get(EmbeddedChunks, key, &c) {
				continue
			}
			if scope(c) {
				n++
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int{"chunks": n})
	case http.MethodDelete:
		removed := 0
		for _, key := range VectorDB.Keys(EmbeddedChunks) {
			var c EmbeddedChunk
			if !VectorDB.Get(EmbeddedChunks, key, &c) {
				continue
			}
			if scope(c) {
				VectorDB.Unset(EmbeddedChunks, key)
				removed++
			}
		}
		invalidateChunkCacheIfPossible()
		Log("[orchestrate.knowledge] user=%q wiped %d auto-inferred chunk(s) for agent=%s (uploads preserved)",
			user, removed, agentID)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int{"removed": removed})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleAgentKnowledgeScaffoldCollection creates an empty Document
// Collection seeded from the agent's own name + description and
// attaches it to the agent. Plain reuse — name becomes "<agent>
// Knowledge", description copies the agent's verbatim. No LLM
// authoring; the user (or Builder, on request) refines later via
// the standard Knowledge surface.
//
//	POST /api/agents/{id}/knowledge/scaffold-collection
//	→ {id, name, description}
func (T *OrchestrateApp) handleAgentKnowledgeScaffoldCollection(w http.ResponseWriter, r *http.Request, user string, udb Database, agentID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a, ok := loadAgent(udb, agentID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if a.Owner != user && a.Owner != seedOwner {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	agentName := strings.TrimSpace(a.Name)
	if agentName == "" {
		http.Error(w, "agent has no name", http.StatusBadRequest)
		return
	}
	c := Collection{
		ID:          UUIDv4(),
		Owner:       user,
		Name:        agentName + " Knowledge",
		Description: strings.TrimSpace(a.Description),
		Created:     time.Now(),
	}
	saveCollection(udb, c)

	already := false
	for _, cid := range a.AttachedCollections {
		if cid == c.ID {
			already = true
			break
		}
	}
	if !already {
		a.AttachedCollections = append(a.AttachedCollections, c.ID)
		if _, serr := saveAgent(udb, a); serr != nil {
			Log("[orchestrate.knowledge] scaffold-collection: saved collection but failed to attach to agent=%s: %v", agentID, serr)
		}
	}
	Log("[orchestrate.knowledge] user=%q scaffolded collection %q (id=%s) for agent=%s", user, c.Name, c.ID, agentID)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"id":          c.ID,
		"name":        c.Name,
		"description": c.Description,
	})
}

// handleAgentInferredList returns the agent's Reference Memory entries
// (derived chunks — chunkProvenance=="derived"). Used by the Memory
// modal to render the prune-able list. Read-only listing; deletion
// flows through handleAgentInferredDelete per-entry.
//
//	GET /api/agents/{id}/inferred → {items: [{id, topic, content, source_doc}]}
//
// Capped at maxInferredList entries to keep the modal load tractable;
// users with hundreds of derived chunks get the most recent slice
// (sorted by ReportID prefix which is timestamp-ish).
func (T *OrchestrateApp) handleAgentInferredList(w http.ResponseWriter, r *http.Request, user, agentID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if T.DB == nil {
		http.Error(w, "DB not initialized", http.StatusInternalServerError)
		return
	}
	prefix := agentKnowledgePrefix(user, agentID)
	type row struct {
		ID        string `json:"id"`
		Topic     string `json:"topic,omitempty"`
		Content   string `json:"content"`
		SourceDoc string `json:"source_doc,omitempty"`
	}
	items := make([]row, 0, 32)
	for _, key := range VectorDB.Keys(EmbeddedChunks) {
		var c EmbeddedChunk
		if !VectorDB.Get(EmbeddedChunks, key, &c) {
			continue
		}
		if !strings.HasPrefix(c.Source, prefix) && c.Source != prefix {
			continue
		}
		if chunkProvenance(c.Source, c.ReportID) != "derived" {
			continue
		}
		items = append(items, row{
			ID:        c.ID,
			Topic:     c.Section,
			Content:   c.Text,
			SourceDoc: chunkDocName(c.Section),
		})
		if len(items) >= maxInferredList {
			break
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
}

// handleAgentInferredDelete removes one Reference Memory chunk by id.
// Scoped to the (user, agent) namespace + derived provenance — the
// chunk's source prefix and ReportID must both match, defending
// against a misrouted DELETE wiping curated content.
//
//	DELETE /api/agents/{id}/inferred/{chunk_id} → 204
func (T *OrchestrateApp) handleAgentInferredDelete(w http.ResponseWriter, r *http.Request, user, agentID, chunkID string) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if T.DB == nil {
		http.Error(w, "DB not initialized", http.StatusInternalServerError)
		return
	}
	if chunkID == "" {
		http.Error(w, "chunk_id required", http.StatusBadRequest)
		return
	}
	prefix := agentKnowledgePrefix(user, agentID)
	found := false
	for _, key := range VectorDB.Keys(EmbeddedChunks) {
		var c EmbeddedChunk
		if !VectorDB.Get(EmbeddedChunks, key, &c) {
			continue
		}
		if c.ID != chunkID {
			continue
		}
		// Scope checks — defend against deleting curated content via
		// a misrouted chunk id.
		if !strings.HasPrefix(c.Source, prefix) && c.Source != prefix {
			http.Error(w, "chunk not owned by this agent", http.StatusForbidden)
			return
		}
		if chunkProvenance(c.Source, c.ReportID) != "derived" {
			http.Error(w, "chunk is not a derived Reference Memory entry", http.StatusForbidden)
			return
		}
		VectorDB.Unset(EmbeddedChunks, key)
		found = true
		break
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	invalidateChunkCacheIfPossible()
	Log("[orchestrate.memory] user=%q deleted Reference Memory chunk %q for agent=%s", user, chunkID, agentID)
	w.WriteHeader(http.StatusNoContent)
}

// maxInferredList caps how many derived chunks the Memory modal
// requests in one shot. Plenty for spot-pruning; users with hundreds
// of chunks should reach for the corpus-wide wipe in the Knowledge
// modal instead.
const maxInferredList = 200

// invalidateChunkCacheIfPossible best-effort cache invalidation
// after a wipe. Core's invalidateChunkCache is private — we call it
// transitively via WipeChunksBySourcePrefix for the nuclear wipe but
// the targeted wipe above bypasses that, so the cache could go
// stale. Use the public no-op for now; if/when search results look
// stale right after auto-inferred wipes, expose Core.InvalidateChunkCache.
func invalidateChunkCacheIfPossible() {
	// Intentional no-op — chunk cache will refresh on TTL expiry.
	// Read-after-write inconsistency window is short and the impact
	// is minor (one stale search). Promote to a real invalidation
	// when it actually bites.
}

// chunkProvenance classifies a chunk by its source + reportID into
// one of three buckets the user (and the LLM via search results)
// cares about distinguishing:
//
//	"uploaded" — files the user uploaded via the Knowledge button
//	             (orch-upload-*) or autofill-fetched docs in a
//	             collection (autofill-*)
//	"shared"   — admin-curated docs on the agent (agent-shared:* source)
//	             or chunks under any collection source (always authoritative
//	             because they came from a deliberate ingest)
//	"derived"  — anything else: synthesis auto-ingests, closer findings,
//	             LLM knowledge_save calls. The LLM's own reasoning
//	             captured at end-of-turn — useful but lower priority
//	             than authoritative material.
func chunkProvenance(source, reportID string) string {
	switch {
	case strings.HasPrefix(source, "agent-shared:"):
		return "shared"
	case strings.HasPrefix(source, "collection:"):
		return "shared" // collections are admin/user-curated content too
	case strings.HasPrefix(reportID, "orch-upload-"):
		return "uploaded"
	case strings.HasPrefix(reportID, "autofill-"):
		return "uploaded"
	}
	return "derived"
}

// chunkDocName derives the clean document name from a chunk's
// section header. IngestReport stores sections as "## <doc name>"
// (often appended with " (part N/M)" when embedding splits a large
// chunk into pieces). Strip both the markdown prefix and any part
// suffix so the LLM sees a stable "this is doc X" identity across
// all chunks of the same document.
//
// Two chunks of the same document share their derived doc name even
// when their raw sections differ (one might be "K8s API (part 1/3)"
// and another "K8s API (part 2/3)"); both resolve to "K8s API".
func chunkDocName(section string) string {
	s := strings.TrimSpace(section)
	s = strings.TrimPrefix(s, "## ")
	s = strings.TrimPrefix(s, "# ")
	return stripChunkPartSuffix(s)
}

// stripChunkPartSuffix removes "(part N)" or "(part N/M)" suffixes
// appended by the chunker's sub-split fallback in vector_store.go
// (embedWithSplitFallback). When a single chunk exceeds the embedder's
// batch size, it gets recursively halved and each piece stored as a
// separate row with section "<orig> (part i/N)". Sections-listing
// callers group by ReportID and want the base name for display, not
// one of the (part X) variants.
func stripChunkPartSuffix(section string) string {
	s := strings.TrimSpace(section)
	// Walk from the end; strip repeated "(part X/Y)" or "(part X)"
	// tails in case sub-split fired multiple times.
	for {
		idx := strings.LastIndex(s, " (part ")
		if idx < 0 {
			return s
		}
		tail := s[idx+len(" (part "):]
		if !strings.HasSuffix(tail, ")") {
			return s
		}
		inner := tail[:len(tail)-1] // drop trailing ')'
		// Accept "N" or "N/M" with digits only.
		looksValid := true
		seenSlash := false
		for i := 0; i < len(inner); i++ {
			c := inner[i]
			if c >= '0' && c <= '9' {
				continue
			}
			if c == '/' && !seenSlash {
				seenSlash = true
				continue
			}
			looksValid = false
			break
		}
		if !looksValid || inner == "" {
			return s
		}
		s = strings.TrimSpace(s[:idx])
	}
}

// agentKnowledgePrefix returns the source-prefix that matches every
// chunk this (user, agent) pair has ingested. Used by the per-agent
// "wipe knowledge" affordance to scope deletion without touching
// other agents or users.
func agentKnowledgePrefix(user, agentID string) string {
	return "orchestrate:" + user + ":" + agentID
}

// isPDFUpload reports whether the upload should be treated as a PDF
// for paged-ingest purposes. Checks mime first (authoritative when
// the browser sets it), falls back to filename extension. Used by
// the upload handlers to pick IngestPagedReport vs IngestReport so
// PDF chunks carry "page N" locators for citation.
func isPDFUpload(mime, name string) bool {
	if strings.Contains(strings.ToLower(mime), "pdf") {
		return true
	}
	return strings.HasSuffix(strings.ToLower(name), ".pdf")
}

// handleAgentKnowledgeUpload accepts a single document upload and
// ingests it into the agent's knowledge corpus under topic="attachments".
// Mirrors the document-extraction path the chat-send flow runs, but
// driven by an explicit "manage knowledge" affordance instead of the
// in-conversation paperclip. Body shape matches the chat-send document
// payload so the client can reuse its existing file-read code:
//
//	POST /api/agents/{id}/knowledge/upload
//	{name, mime_type, data: base64}
//	→ {id: reportID, chunks: N, name}
func (T *OrchestrateApp) handleAgentKnowledgeUpload(w http.ResponseWriter, r *http.Request, user, agentID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if T.DB == nil {
		http.Error(w, "DB not initialized", http.StatusInternalServerError)
		return
	}
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	a, found := loadAgent(udb, agentID)
	if !found || (a.Owner != user && a.Owner != seedOwner) {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Name     string `json:"name"`
		MimeType string `json:"mime_type"`
		Data     string `json:"data"` // base64
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	raw, err := base64.StdEncoding.DecodeString(body.Data)
	if err != nil {
		http.Error(w, "data: not valid base64", http.StatusBadRequest)
		return
	}
	text, err := ExtractDocument(r.Context(), DocumentAttachment{
		Name:     name,
		MimeType: body.MimeType,
		Data:     raw,
	})
	if err != nil {
		http.Error(w, "extract failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	text = strings.TrimSpace(text)
	const minUploadChars = 200 // matches the chat-paperclip threshold (runner.go minIngestChars)
	if len(text) < minUploadChars {
		http.Error(w, fmt.Sprintf("extracted text too short (%d chars) — minimum is %d", len(text), minUploadChars), http.StatusBadRequest)
		return
	}
	// Use a stable reportID per upload — so the list / delete endpoints
	// can address one document at a time. The framework's IngestReport
	// replaces all chunks under the same reportID on re-ingest, so this
	// ID is also the "edit/replace" handle.
	reportID := fmt.Sprintf("orch-upload-%s-%s-%d", agentID, user, time.Now().UnixNano())
	doc := "## " + name + "\n\n" + text
	// PDFs go through the paged ingest so each chunk carries a
	// "page N" locator for citation. pdftotext emits form-feed (\f)
	// between pages, and IngestPagedReport splits on that. Other
	// formats (DOCX, Markdown, plain text) have no useful per-page
	// locator, so they go through the flat IngestReport path.
	if isPDFUpload(body.MimeType, name) {
		IngestPagedReport(r.Context(), VectorDB, knowledgeSource(user, agentID, "attachments"), reportID, doc)
	} else {
		IngestReport(r.Context(), VectorDB, knowledgeSource(user, agentID, "attachments"), reportID, doc)
	}
	// recordAgentTopic writes per-agent METADATA (topic list); stays on T.DB.
	recordAgentTopic(T.DB, user, agentID, "attachments")
	chunks := countReportChunks(VectorDB, reportID)
	Log("[orchestrate.knowledge] user=%q agent=%s uploaded %q (%d chars → %d chunks) reportID=%s",
		user, agentID, name, len(text), chunks, reportID)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":     reportID,
		"name":   name,
		"chunks": chunks,
	})
}

// handleAgentKnowledgeSources lists each USER-UPLOADED document
// (grouped by reportID) under this agent's corpus. Returns one entry
// per reportID with the source name, chunk count, and most-recent
// date.
//
// Only returns uploads (orch-upload-* reportIDs). Derived chunks
// (synthesis auto-ingests, closer findings, knowledge_save calls) are
// EXCLUDED so the "Your documents" modal section shows only what the
// user deliberately put in — the agent's own ramblings live separately
// under the "Auto-inferred knowledge" section.
//
//	GET /api/agents/{id}/knowledge/sources
//	→ {sources: [{id, name, chunks, latest}]}
func (T *OrchestrateApp) handleAgentKnowledgeSources(w http.ResponseWriter, r *http.Request, user, agentID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if T.DB == nil {
		http.Error(w, "DB not initialized", http.StatusInternalServerError)
		return
	}
	prefix := agentKnowledgePrefix(user, agentID)
	type group struct {
		id     string
		name   string
		chunks int
		latest string
	}
	groups := map[string]*group{}
	for _, key := range VectorDB.Keys(EmbeddedChunks) {
		var c EmbeddedChunk
		if !VectorDB.Get(EmbeddedChunks, key, &c) {
			continue
		}
		if !strings.HasPrefix(c.Source, prefix) {
			continue
		}
		// Skip derived chunks — they appear in the modal's separate
		// "Auto-inferred knowledge" section, not under "Your documents."
		if !strings.HasPrefix(c.ReportID, "orch-upload-") {
			continue
		}
		g, ok := groups[c.ReportID]
		if !ok {
			g = &group{id: c.ReportID, name: c.Section}
			groups[c.ReportID] = g
		}
		g.chunks++
		if c.Date > g.latest {
			g.latest = c.Date
		}
		// Prefer the document Title (topic/question stamped at ingest);
		// fall back to the SHORTEST section for legacy chunks with no
		// Title (the first chunk's section is the document title
		// "## <name>"; later chunks may be subsection titles within it).
		if c.Title != "" {
			g.name = c.Title
		} else if c.Section != "" && (g.name == "" || len(c.Section) < len(g.name)) {
			g.name = c.Section
		}
	}
	type sourceOut struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Chunks int    `json:"chunks"`
		Latest string `json:"latest,omitempty"`
	}
	out := make([]sourceOut, 0, len(groups))
	for _, g := range groups {
		out = append(out, sourceOut{ID: g.id, Name: g.name, Chunks: g.chunks, Latest: g.latest})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Latest > out[j].Latest })
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"sources": out})
}

// handleAgentKnowledgeSourceDelete removes every chunk for one
// reportID, scoped to the (user, agent) namespace so a caller can't
// nuke a reportID from a different agent.
//
//	DELETE /api/agents/{id}/knowledge/sources/{reportID}
//	→ {removed: N}
func (T *OrchestrateApp) handleAgentKnowledgeSourceDelete(w http.ResponseWriter, r *http.Request, user, agentID, reportID string) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if T.DB == nil {
		http.Error(w, "DB not initialized", http.StatusInternalServerError)
		return
	}
	if reportID == "" {
		http.Error(w, "reportID required", http.StatusBadRequest)
		return
	}
	prefix := agentKnowledgePrefix(user, agentID)
	removed := 0
	for _, key := range VectorDB.Keys(EmbeddedChunks) {
		var c EmbeddedChunk
		if !VectorDB.Get(EmbeddedChunks, key, &c) {
			continue
		}
		if c.ReportID != reportID {
			continue
		}
		if !strings.HasPrefix(c.Source, prefix) {
			continue // other agent's chunk with same ID — refuse cross-scope delete
		}
		VectorDB.Unset(EmbeddedChunks, key)
		removed++
	}
	Log("[orchestrate.knowledge] user=%q agent=%s removed %d chunk(s) for source=%s",
		user, agentID, removed, reportID)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int{"removed": removed})
}

// countReportChunks is a small helper for the upload endpoint to
// report how many chunks the just-ingested document produced.
func countReportChunks(db Database, reportID string) int {
	if db == nil || reportID == "" {
		return 0
	}
	n := 0
	for _, key := range db.Keys(EmbeddedChunks) {
		var c EmbeddedChunk
		if !db.Get(EmbeddedChunks, key, &c) {
			continue
		}
		if c.ReportID == reportID {
			n++
		}
	}
	return n
}

// countAgentKnowledgeChunks returns how many EmbeddedChunks belong to
// the (user, agent) namespace. Cheap-ish single-pass scan used by the
// Memory modal to render the current chunk count next to the wipe
// button. Pulls from the framework's app-wide knowledge bucket so
// counts match what the runner's auto-inject sees.
func countAgentKnowledgeChunks(appDB Database, user, agentID string) int {
	if appDB == nil || user == "" || agentID == "" {
		return 0
	}
	prefix := agentKnowledgePrefix(user, agentID)
	n := 0
	for _, key := range appDB.Keys(EmbeddedChunks) {
		var c EmbeddedChunk
		if !appDB.Get(EmbeddedChunks, key, &c) {
			continue
		}
		if strings.HasPrefix(c.Source, prefix) {
			n++
		}
	}
	return n
}

// ingestAgentKnowledge persists a free-form note under the agent's
// namespace. IngestReport handles chunking + embedding; we just stamp
// a unique reportID so re-ingests don't replace earlier entries.
// Title is prepended as an "## " heading so SplitReportIntoChunks
// labels the section correctly when the body has no headings of its
// own. Empty topic falls back to the agent-wide bucket — but callers
// should normally pass a normalized topic so retrieval can be sharp.
func ingestAgentKnowledge(ctx context.Context, db Database, user, agentID, topic, title, body string) {
	if db == nil || user == "" || agentID == "" {
		return
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Note"
	}
	// Each ingest gets a fresh reportID so prior entries persist —
	// IngestReport replaces all chunks under the same reportID.
	reportID := fmt.Sprintf("orch-know-%s-%s-%d", agentID, user, time.Now().UnixNano())
	doc := "## " + title + "\n\n" + body
	// Chunks land in the dedicated shared vector store; recordAgentTopic
	// writes per-agent metadata which stays on the app's own DB.
	IngestReport(ctx, VectorDB, knowledgeSource(user, agentID, topic), reportID, doc)
	if topic != "" {
		recordAgentTopic(db, user, agentID, topic)
	}
}

// searchAgentKnowledge returns up to k semantically relevant chunks
// stored under any source the calling agent is allowed to see:
//   - the agent's own per-(user, agent) corpus (topic-scoped + agent-wide)
//   - each collection directly attached to the agent ("collection:<id>")
//   - each collection attached to an ACTIVE skill ("collection:<id>")
//   - deployment-scope collections (when the agent has no explicit attachments)
//
// Unlike the old cascade-by-source path that early-returned on the
// first source with hits, this does a UNION search: one vector pass
// across the entire chunk table, filtered to the allowed-source set,
// ranked by relevance. The most relevant chunk might live in a
// skill-attached PDF even when the agent's own corpus has marginally-
// relevant chunks — we want the vector model picking, not source
// priority.
//
// activeSkills passes the SkillRecords selected by the classifier
// this turn; their AttachedCollections feed the allow predicate.
// Pass nil/empty when no skills are active (Builder, classifier off).
// Skill SelfTraining corpus is intentionally NOT a thing — that path
// compounded drift and was removed. Admin-curated collections only.
// ChunkScope filters chunks by provenance during a vector search.
// Lets knowledge_search (curated-only) and memory_search (derived-
// only) share one ranking pass — the predicate runs before scoring
// so each tool gets a focused result set without re-querying the
// vector index.
type ChunkScope int

const (
	// ChunkScopeAll returns both curated (user/admin-provided uploads,
	// shared KB, collections, skill self-training) AND derived (LLM-
	// produced via memory_save, synthesis auto-ingest). Used by the
	// turn-start auto-injection when the agent has both layers on.
	ChunkScopeAll ChunkScope = iota
	// ChunkScopeCuratedOnly drops derived chunks. The semantic that
	// powers knowledge_search (Knowledge layer is uploaded files only)
	// and the per-agent DisableInferred path — "only return the user's
	// authoritative content, none of the LLM's own derivations."
	ChunkScopeCuratedOnly
	// ChunkScopeDerivedOnly drops curated chunks. Powers memory_search
	// — "what has the agent learned that I haven't told it?"
	ChunkScopeDerivedOnly
)

// baseUser, when set and different from user, adds the TEMPLATE owner's corpus as
// a second searched layer: a scoped instance (runtimeUser != the agent's owner —
// e.g. a per-appliance servitor agent) retrieves the template's general/base
// knowledge ALONGSIDE what it gathered itself. Empty/equal ⇒ instance-only (the
// normal single-scope behavior).
func searchAgentKnowledge(ctx context.Context, db Database, user, baseUser, agentID, topic, query string, k int, activeSkills []SkillRecord, agentAttachedCollections []string, scope ChunkScope) []SearchHit {
	// db is kept in the signature for caller compatibility and acts as
	// the system-readiness gate. The chunk search itself runs against
	// VectorDB now (the dedicated shared store, partitioned by Source
	// tag) — the agent's own corpus AND any deployment-scoped collection
	// chunks all live there, so the prior two-pass-merge against RootDB
	// is no longer needed (pass 1 covers everything the allow predicate
	// matches).
	_ = db
	if VectorDB == nil || strings.TrimSpace(query) == "" || k <= 0 {
		return nil
	}
	// The agent's per-(user, agent) corpus uses a PREFIX (sources
	// like orchestrate:<user>:<agent>, plus topic suffixes like
	// orchestrate:<user>:<agent>:attachments, ...:kubernetes, etc.).
	// An exact-match map can't capture that — we'd miss every
	// uploaded document (those live under :attachments) unless the
	// turn's classified topic happens to equal "attachments". Use a
	// PREDICATE: prefix match for agent corpus, exact match for the
	// rest (shared KB, skill corpora, collection corpora). Optionally
	// scope the results by provenance (curated vs derived) for the
	// Knowledge / Memory split.
	agentPrefix := knowledgeSource(user, agentID, "")
	// Topic scoping: when the caller names a SPECIFIC topic (not the general /
	// agent-wide fallback), narrow the agent's OWN corpus to that topic's bucket
	// for precision — but ALWAYS keep uploaded documents (the :attachments bucket)
	// in scope, since uploads are cross-topic reference material the user added on
	// purpose (see the ingest path at knowledgeSource(..., "attachments")). A
	// general or empty topic keeps the original agent-wide prefix span. The
	// template/base layer and attached collections below are left wide — they are
	// not partitioned by this turn's topic.
	topicScoped := topic != "" && topic != generalTopic
	agentTopicSource, attachmentsSource := "", ""
	if topicScoped {
		agentTopicSource = knowledgeSource(user, agentID, topic)
		attachmentsSource = knowledgeSource(user, agentID, "attachments")
	}
	matchesAgentCorpus := func(src string) bool {
		if topicScoped {
			return src == agentTopicSource || strings.HasPrefix(src, agentTopicSource+":") ||
				src == attachmentsSource || strings.HasPrefix(src, attachmentsSource+":") ||
				src == agentPrefix
		}
		return src == agentPrefix || strings.HasPrefix(src, agentPrefix+":")
	}
	// Template/base layer (enabler #1 of agent-as-template): a SCOPED instance
	// (runtimeUser != the agent's owner) also searches the template owner's corpus,
	// so a per-appliance instance inherits the template's base knowledge on top of
	// its own. Empty when the run isn't scoped (baseUser unset or == user).
	basePrefix := ""
	if baseUser != "" && baseUser != user {
		basePrefix = knowledgeSource(baseUser, agentID, "")
	}
	// Per-agent shared KB was deprecated in favor of attached
	// collections — admins curate corpus by minting a Collection and
	// attaching it to the agent (which makes the corpus reusable
	// across agents instead of stranded on one). Any chunks still
	// living under agentSharedSource() from before the move are no
	// longer reachable from RAG; the admin can re-ingest them as a
	// collection if they still matter.
	exact := make(map[string]bool, len(agentAttachedCollections)+4)
	// Agent-attached collections are always in scope.
	for _, cid := range agentAttachedCollections {
		if cid = strings.TrimSpace(cid); cid != "" {
			exact[collectionSource(cid)] = true
		}
	}
	// Active skills contribute their AttachedCollections — when the
	// classifier picks a skill this turn, its admin-curated reference
	// material becomes searchable alongside the agent's own corpus.
	// When the skill isn't active, its collections stay out of scope,
	// so heavy reference docs don't leak into unrelated turns. This
	// is the "skills as behavior + corpus packets" path that pairs
	// with the new pull-only retrieval model (no auto-inject =
	// no contamination cost for ride-along docs).
	for _, sk := range activeSkills {
		for _, cid := range sk.AttachedCollections {
			if cid = strings.TrimSpace(cid); cid != "" {
				exact[collectionSource(cid)] = true
			}
		}
	}
	// Deployment-scoped collections auto-attach when the agent
	// has no curated AttachedCollections (the "default = open"
	// rule from the design discussion: empty list = "give me the
	// defaults"; explicit list = "I picked these on purpose,
	// leave me alone"). Agents wanting strict isolation set an
	// explicit AttachedCollections list — even if it's just their
	// one curated corpus — and the deployment defaults skip them.
	if len(agentAttachedCollections) == 0 {
		for _, c := range ListCollections(nil, "") { // empty user → deployment only
			if IsDeploymentScope(c) {
				exact[collectionSource(c.ID)] = true
			}
		}
	}
	allow := func(c EmbeddedChunk) bool {
		// Source allow-list (prefix for agent corpus, exact for the rest).
		inAllowed := false
		if matchesAgentCorpus(c.Source) {
			inAllowed = true
		} else if basePrefix != "" && (c.Source == basePrefix || strings.HasPrefix(c.Source, basePrefix+":")) {
			inAllowed = true // template/base layer for a scoped instance
		} else if exact[c.Source] {
			inAllowed = true
		}
		if !inAllowed {
			return false
		}
		// Provenance gate: "derived" = LLM-generated (memory_save,
		// synthesis auto-ingest, closer findings); anything else is
		// curated (uploads, shared KB, collections, skill self-
		// training). ChunkScopeAll skips the gate entirely.
		switch scope {
		case ChunkScopeCuratedOnly:
			if chunkProvenance(c.Source, c.ReportID) == "derived" {
				return false
			}
		case ChunkScopeDerivedOnly:
			if chunkProvenance(c.Source, c.ReportID) != "derived" {
				return false
			}
		}
		return true
	}

	cfg := GetEmbeddingConfig()
	var vec []float32
	if cfg.Enabled {
		if v, err := Embed(ctx, query); err == nil && len(v) > 0 {
			vec = v
		}
	}
	// Filter BEFORE ranking. At scale (many users / collections /
	// skill corpora), an unfiltered top-N gets dominated by chunks
	// the calling agent isn't allowed to see, and post-filtering
	// returns empty even when the agent has perfectly relevant
	// chunks in its corpus.
	//
	// Single pass over VectorDB — agent corpus AND deployment-scoped
	// collection chunks both live there now, so the allow predicate
	// (agentPrefix OR exact[c.Source]) covers everything in one ranked
	// search. The prior RootDB second-pass + merge dance is gone.
	// Hybrid recall: vector (semantic) + keyword (lexical), merged — so an exact
	// term the embedding semantically near-misses still surfaces. Hybrid also
	// covers the no-embedding case (keyword only), replacing the old fallback.
	hits := HybridSearchByPredicate(VectorDB, allow, query, vec, k)
	// Operator-set recall floor (admin tunable; default 0 = off): drop hits
	// below the cosine threshold so a high min-score deployment trades recall
	// for precision. No-op at 0, preserving the prior "return top-k" behavior.
	if ms := RecallMinScore(); ms > 0 {
		kept := hits[:0]
		for _, h := range hits {
			if float64(h.Score) >= ms {
				kept = append(kept, h)
			}
		}
		hits = kept
	}
	return hits
}

// mergeHitsByScore combines two hit slices, dedups by chunk ID,
// sorts by Score descending, and caps at k. Used to merge per-app
// + RootDB query results when deployment collections are in scope.
// mergeHitsByScore delegates to the lifted core primitive; kept as a
// package-local alias so existing call sites read unchanged.
func mergeHitsByScore(a, b []SearchHit, k int) []SearchHit {
	return MergeHitsByScore(a, b, k)
}

func filterHitsBySource(hits []SearchHit, source string, k int) []SearchHit {
	out := make([]SearchHit, 0, k)
	for _, h := range hits {
		if h.Source != source {
			continue
		}
		out = append(out, h)
		if len(out) >= k {
			break
		}
	}
	return out
}

// knowledgeSearchExcerptMaxChars caps how much chunk body
// knowledge_search returns per hit. The hit list is a preview pane:
// LLM scans the excerpts + source attributions, picks which document
// to read fully via fetch_knowledge_doc, and only then sees the
// surrounding body. Mirrors web_search → fetch_url ergonomics; cuts
// per-turn tool-result tokens by ~10x on multi-hit searches.
const knowledgeSearchExcerptMaxChars = 300

// knowledgeSearchExcerpt trims a chunk body to a preview suitable for
// the knowledge_search result list. Truncates at a word boundary +
// ellipsis when over the cap; returns the body unchanged when already
// short enough.
func knowledgeSearchExcerpt(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= knowledgeSearchExcerptMaxChars {
		return text
	}
	cut := text[:knowledgeSearchExcerptMaxChars]
	if idx := strings.LastIndex(cut, " "); idx > knowledgeSearchExcerptMaxChars/2 {
		cut = cut[:idx]
	}
	return strings.TrimRight(cut, " \t\n") + "…"
}

// manualSearchMinScore is the floor applied to knowledge_search and
// memory_search calls. Vector search returns top-K regardless of
// score, so weak matches (a chunk that shares surface terms but is
// tangentially related) would otherwise leak into the response. Qwen-
// class models treat anything the tool returns as trusted context and
// incorporate it without sanity-checking the score — the concrete
// failure was an LVM-shrink query pulling an Nvidia GPU article (low
// score, shared "Linux"/"reduce" surface terms) which then got woven
// into a downstream question to a tech-guru agent.
const manualSearchMinScore = 0.35

// memorySaveDedupThreshold is the cosine floor above which a memory_save is
// treated as a near-duplicate of an existing derived finding and skipped. Every
// save mints a fresh reportID (chunks never overwrite), so without this gate a
// re-saved finding proliferates near-identical chunks that then crowd each other
// in memory_search. Mirrors the fact layer's semantic dedup (factDedupSimThreshold
// = 0.90); set conservatively so only clear duplicates are dropped, not merely
// related material worth keeping separately.
const memorySaveDedupThreshold = 0.90

// --- Tools the LLM can call directly --------------------------------------

// memoryToolDef builds the grouped tool for Reference Memory.
// One LLM-facing entry (memory) with an action discriminator
// covers save / search / forget / help — same shape phantom's
// knowledge tool uses. Earlier revisions exposed three separate
// tools (memory_save / memory_search / memory_forget); they all
// operated on the same conceptual store and shared most params, so
// the grouped form reads cleaner in the catalog and the action
// keyword carries the semantic load.
//
// Closure-bound to the chatTurn so it picks up (user, agent_id)
// without an extra ToolSession round-trip. Writes go to Reference
// Memory only — the Knowledge layer (uploaded files) is admin-
// managed read-only; Explicit Memory (always-in-prompt) is the
// store_fact tool's job.
func (t *chatTurn) memoryToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "memory",
			Description: "Reference Memory for THIS agent — your own vector-searchable scratchpad of findings worth recalling later. Pull-only: nothing auto-injects, you call this tool when the current question reminds you of something you previously figured out. Sibling tools: `knowledge_search` (read-only over uploaded files — authoritative source-of-truth), `store_fact` (Explicit Memory — short notes pre-injected on every turn).\n\n**action=\"save\"** — persist a finding. Use for COMPLICATED REFERENCE MATERIAL you MAY need later (API specs, website navigation, config recipes, working approaches, document-derived technical details). Right examples: \"Acme API uses cursor-based pagination with `after` token; max page size 100\", \"To install ffmpeg with libx264 on Ubuntu, ...\". Paragraph-length self-contained findings work best. **Use store_fact instead** when the note shapes how you respond to ANY future question (user preferences, identity facts). DO NOT save findings already retrieved via memory(action=\"search\") OR knowledge_search this turn — saving the same content again compounds noise. Required: `content`. Optional: `topic`, `subject`.\n\n**action=\"search\"** — semantic search over saved findings. Returns up to k JSON entries ranked by relevance. Reference Memory is your own derived material and may have drifted, so verify against curated sources (knowledge_search) when accuracy matters. Required: `query`. Optional: `topic`, `k`.\n\n**action=\"forget\"** — delete chunks. Two modes: pass `id` (a mem_id from a prior search) to surgically drop ONE entry — the safe path; or pass `query` (+ optional `topic`, `k`) to delete top-matching chunks — useful for bulk cleanup but a loose query nukes more than intended. When both are present, `id` wins. Operates ONLY on derived chunks; curated Knowledge is admin-managed.\n\n**action=\"help\"** — return this spec.\n\nFindings are namespaced by `topic` (snake_case slug like `acme_api`, `nginx_config`) — YOU pick the slug. Look at the \"Known topics\" block in your system prompt and reuse when applicable; mint a new snake_case slug when the subject genuinely doesn't fit. Omit `topic` on save to drop into `general`; omit on search/forget to span all topics.",
			Parameters: map[string]ToolParam{
				"action":  {Type: "string", Description: "Which operation: save | search | forget | help."},
				"topic":   {Type: "string", Description: "Snake_case topic slug. (save) reuse one from the \"Known topics\" block or mint a new one; omit for `general`. (search, forget) scope query to one bucket; omit to span all."},
				"subject": {Type: "string", Description: "(save) Short heading for THIS specific finding. Example: \"Acme API rotates session tokens every 24h\". Optional; defaults to the topic slug."},
				"content": {Type: "string", Description: "(save) The finding itself — several sentences to a paragraph. Self-contained: include enough context that it'll make sense without seeing this conversation."},
				"query":   {Type: "string", Description: "(search, forget) Natural-language search query. The user's current question often works well, possibly trimmed to the gist. (forget) Be specific — a loose query wipes more than you intended."},
				"k":       {Type: "number", Description: "(search default 5, cap 20; forget default 3, cap 10) Max hits to return / delete."},
				"id":      {Type: "string", Description: "(forget) The mem_id from a memory(action=\"search\") hit. Deletes exactly that chunk; preferred when you know which entry to drop."},
			},
			Required: []string{"action"},
			// CapWrite covers save + forget; search reads, but the
			// combined tool needs the broader cap so privacy filters
			// don't strip it from agents allowed to write.
			Caps: []Capability{CapWrite},
		},
		Handler: func(args map[string]any) (string, error) {
			action := strings.TrimSpace(stringArg(args, "action"))
			switch action {
			case "", "help":
				return memoryHelpText(), nil
			case "save":
				return t.memorySave(args)
			case "search":
				return t.memorySearch(args)
			case "forget":
				return t.memoryForget(args)
			default:
				return "", fmt.Errorf("unknown action %q. valid: save, search, forget, help", action)
			}
		},
	}
}

// memoryHelpText returns the same spec the description carries.
// Kept as a separate function so the help action returns plain
// markdown without re-quoting the description body.
func memoryHelpText() string {
	return `memory — usage:

  action="save"   — persist a finding to Reference Memory.
                    Required: content. Optional: topic, subject.
  action="search" — semantic search over saved findings.
                    Required: query. Optional: topic, k.
  action="forget" — delete chunks. Pass id (surgical, one entry)
                    OR query (+ optional topic, k for bulk).
  action="help"   — show this spec.

Findings live under snake_case topic slugs; reuse from the
"Known topics" block when applicable, or omit topic to span all
(search/forget) / land in "general" (save).
`
}

// memorySave is the save-action implementation. Extracted so the
// grouped memoryToolDef can route through it cleanly.
func (t *chatTurn) memorySave(args map[string]any) (string, error) {
	topic := normalizeTopic(stringArg(args, "topic"))
	content := strings.TrimSpace(stringArg(args, "content"))
	if content == "" {
		return "", errors.New("content is required")
	}
	subject := strings.TrimSpace(stringArg(args, "subject"))
	if subject == "" {
		subject = strings.TrimSpace(stringArg(args, "title"))
	}
	if subject == "" {
		subject = topic
	}
	ctx, cancel := context.WithTimeout(context.Background(), knowledgeIngestTimeout())
	defer cancel()
	// Save-time dedup: skip when a near-identical finding is already in this
	// agent's derived memory, so re-saving the same material doesn't proliferate
	// chunks that later crowd each other in memory_search. Mirrors the fact
	// layer's semantic dedup. Best-effort — any embed/search failure falls
	// through to a normal save rather than dropping the content. Scoped to the
	// agent's OWN derived chunks (orch-know-*), across all topics, so a
	// duplicate saved under a different topic slug is still caught.
	if VectorDB != nil {
		if cfg := GetEmbeddingConfig(); cfg.Enabled {
			if vec, err := Embed(ctx, content); err == nil && len(vec) > 0 {
				agentPrefix := knowledgeSource(t.user, t.agent.ID, "")
				allow := func(c EmbeddedChunk) bool {
					if c.Source != agentPrefix && !strings.HasPrefix(c.Source, agentPrefix+":") {
						return false
					}
					return strings.HasPrefix(c.ReportID, "orch-know-") // derived (memory_save) only
				}
				if hits := SearchChunksByPredicate(VectorDB, allow, vec, 1); len(hits) > 0 && float64(hits[0].Score) >= memorySaveDedupThreshold {
					return fmt.Sprintf("Already saved (deduped): a near-identical finding is already in Memory (%.0f%% match). Skipping to avoid duplicate chunks — retrieve the existing one via memory(action=\"search\").",
						hits[0].Score*100), nil
				}
			}
		}
	}
	ingestAgentKnowledge(ctx, t.app.DB, t.user, t.agent.ID, topic, subject, content)
	return fmt.Sprintf("Saved %d chars under topic %q in Memory. Future similar questions can retrieve this via memory(action=\"search\").",
		len(content), topic), nil
}

// searchKnowledgeToolDef builds the read-side tool over the
// Knowledge layer — read-only authoritative content the user/admin
// uploaded (PDFs, shared KB, collections, skill self-training).
// The LLM never writes to Knowledge; it only reads. Derived chunks
// from memory_save / synthesis ingest live in Reference Memory and
// have their own memory_search tool. Closure-bound to the chatTurn.
func (t *chatTurn) searchKnowledgeToolDef() AgentToolDef {
	return t.knowledgeToolDefScoped(t.skillsActive)
}

// knowledgeToolDefScoped is searchKnowledgeToolDef parameterized by which
// skills' AttachedCollections widen the search scope. The main turn passes
// t.skillsActive (empty now that skills aren't in-context); a use_expert
// worker passes []SkillRecord{expert} so the expert can search its OWN
// attached corpus.
func (t *chatTurn) knowledgeToolDefScoped(scopeSkills []SkillRecord) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "knowledge_search",
			Description: "Search this agent's knowledge corpus (uploaded docs, attached collections, skill self-training). Returns hits with excerpts, source_doc, section, and a doc_id — pass the doc_id to `fetch_knowledge_doc` when an excerpt isn't enough. Below-floor matches are filtered; a 'no strong matches' result means the corpus has nothing confident, not license to speculate. Topic-scoped by default; pass explicit `topic` to query a different bucket. Distinct from `memory_search` (your own prior derived findings) and `list_facts` (Explicit Memory entries already in your prompt).",
			Parameters: map[string]ToolParam{
				"query": {
					Type:        "string",
					Description: "Natural-language search query. The user's current question often works well, possibly trimmed to the gist.",
				},
				"topic": {
					Type:        "string",
					Description: "Optional snake_case topic slug to scope the search. Omit to search across all topics (broadest). Pass an existing slug from the \"Known topics\" block in your system prompt to narrow to a single subject area when you know which one applies.",
				},
				"k": {
					Type:        "number",
					Description: "Max number of hits to return (default 5, cap 20). Leave default unless you specifically want a wider net.",
				},
			},
			Required: []string{"query"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			query := strings.TrimSpace(stringArg(args, "query"))
			if query == "" {
				return "", errors.New("query is required")
			}
			k := KnowledgeTopK()
			if v, ok := args["k"].(float64); ok && v > 0 {
				k = int(v)
				if maxK := KnowledgeMaxK(); k > maxK {
					k = maxK
				}
			}
			// Topic scoping: the LLM picks the slug via the topic= arg
			// (referencing the "Known topics" block in its prompt).
			// Empty or garbage collapses to generalTopic — agent-wide
			// retrieval without a per-subject filter.
			topic := normalizeTopic(stringArg(args, "topic"))
			ctx, cancel := context.WithTimeout(context.Background(), knowledgeIngestTimeout())
			defer cancel()
			hits := searchAgentKnowledge(ctx, t.app.DB, t.user, t.ownerUser, t.agent.ID, topic, query, k, scopeSkills, t.agent.AttachedCollections, ChunkScopeCuratedOnly)
			rawHits := len(hits)
			filtered := hits[:0]
			for _, h := range hits {
				if h.Score < manualSearchMinScore {
					continue
				}
				filtered = append(filtered, h)
			}
			hits = filtered
			dropped := rawHits - len(hits)
			if len(hits) == 0 {
				if dropped > 0 {
					return fmt.Sprintf("No strong matches. Vector search returned %d chunk(s) but ALL scored below the relevance floor (%.2f) — they would have pulled tangentially-related content (different topic, surface-word matches only) into your context. Do NOT speculate from absent results. Either rephrase the query, widen by passing topic=\"\", or proceed without prior context and acknowledge that the Knowledge layer didn't have a confident answer.", dropped, manualSearchMinScore), nil
				}
				return "No matching curated content. The Knowledge layer (uploads, shared KB, collections) has nothing on that — try memory_search for the agent's own derived findings, or proceed without prior context.", nil
			}
			// Mirror web_search's shape: "N. Title\n   URL\n   Snippet"
			// blocks separated by blank lines. Plain text, no markdown
			// ornament — the chat surface renders it the same way it
			// renders web_search results, no escape-sequence noise.
			// Score is dropped (the LLM trusts the ranking the way it
			// trusts web_search's ordering); provenance only surfaces
			// when "derived" (uploaded/shared is the expected default).
			var b strings.Builder
			for i, h := range hits {
				if i > 0 {
					b.WriteString("\n\n")
				}
				// Prefer the stamped document Title (the debate topic /
				// research question) — it tells the LLM what the hit is
				// ABOUT. Fall back to the section-derived name for legacy
				// chunks with no Title.
				docName := strings.TrimSpace(h.Title)
				if docName == "" {
					docName = chunkDocName(h.Section)
				}
				if docName == "" {
					docName = "(unnamed document)"
				}
				section := strings.TrimSpace(strings.TrimPrefix(h.Section, "## "))
				// Line 1: "N. <source_doc> — <section> (locator) [kind]"
				fmt.Fprintf(&b, "%d. %s", i+1, docName)
				if section != "" && section != docName {
					fmt.Fprintf(&b, " — %s", section)
				}
				if h.Locator != "" {
					fmt.Fprintf(&b, " (%s)", h.Locator)
				}
				if h.Kind != "" {
					fmt.Fprintf(&b, " [%s]", h.Kind)
				}
				if chunkProvenance(h.Source, h.ReportID) == "derived" {
					b.WriteString(" [derived]")
				}
				b.WriteString("\n")
				// Line 2: doc_id (web_search's URL slot).
				fmt.Fprintf(&b, "   doc_id: %s\n", h.ReportID)
				// Line 3: excerpt.
				fmt.Fprintf(&b, "   %s", knowledgeSearchExcerpt(h.Text))
			}
			return b.String(), nil
		},
	}
}

// fetchKnowledgeDocDefaultMax caps the output of fetch_knowledge_doc
// when the LLM doesn't specify max_chars. ~10k chars ≈ 2.5k tokens —
// enough to read a typical article body without blowing the round's
// context budget. The hard ceiling is fetchKnowledgeDocCap.
const fetchKnowledgeDocDefaultMax = 10000

// fetchKnowledgeDocCap is the absolute maximum chars one
// fetch_knowledge_doc call can return. Caller-supplied max_chars
// gets clamped to this; for genuinely massive docs the LLM has to
// use knowledge_search to find the right section rather than
// re-reading the whole thing.
const fetchKnowledgeDocCap = 30000

// fetchKnowledgeDocToolDef builds the drill-down tool that
// reconstructs a document body from its chunks. The LLM passes a
// doc_id obtained from a knowledge_search hit; the handler walks the
// vector store for every chunk with that ReportID, gates on the same
// allow predicate as searchAgentKnowledge (so an agent can only
// fetch docs from its own corpus + attached collections), and returns
// the chunks joined in section order. Truncates at max_chars with a
// pointer back to knowledge_search for finer-grained retrieval.
func (t *chatTurn) fetchKnowledgeDocToolDef() AgentToolDef {
	return t.fetchKnowledgeDocScoped(t.skillsActive)
}

// fetchKnowledgeDocScoped is fetchKnowledgeDocToolDef parameterized by
// which skills' AttachedCollections are in the allow-scope. The main
// fetch passes t.skillsActive (empty); a skill_knowledge_fetch_doc call
// passes []SkillRecord{skill} so a doc_id surfaced by that skill's
// collection actually resolves here (the predicate must agree with the
// search scope).
func (t *chatTurn) fetchKnowledgeDocScoped(scopeSkills []SkillRecord) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "fetch_knowledge_doc",
			Description: "Read the full body of a document by doc_id (from a knowledge_search hit). Returns the doc text with section headers, capped at max_chars (default 10000, ceiling 30000). Gated to your accessible corpus.",
			Parameters: map[string]ToolParam{
				"doc_id": {
					Type:        "string",
					Description: "doc_id from a knowledge_search hit.",
				},
				"max_chars": {
					Type:        "number",
					Description: "Optional. Max characters returned (default 10000, ceiling 30000).",
				},
			},
			Required: []string{"doc_id"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			docID := strings.TrimSpace(stringArg(args, "doc_id"))
			if docID == "" {
				return "", errors.New("doc_id is required")
			}
			maxChars := fetchKnowledgeDocDefaultMax
			if v, ok := args["max_chars"].(float64); ok && v > 0 {
				maxChars = int(v)
				if maxChars > fetchKnowledgeDocCap {
					maxChars = fetchKnowledgeDocCap
				}
			}
			// Build the same allow predicate as searchAgentKnowledge so
			// fetch can never reach across into another agent's or
			// another user's corpus. Agent-attached collections are
			// always in scope; active skill collections AND deployment-
			// scope collections also feed in so a doc_id returned from
			// knowledge_search via either path actually resolves here.
			agentPrefix := knowledgeSource(t.user, t.agent.ID, "")
			exact := make(map[string]bool, len(t.agent.AttachedCollections)+4)
			for _, cid := range t.agent.AttachedCollections {
				if cid = strings.TrimSpace(cid); cid != "" {
					exact[collectionSource(cid)] = true
				}
			}
			// Active skills contribute their AttachedCollections —
			// mirrors searchAgentKnowledge so a doc_id surfaced via
			// a skill's collection resolves here too. Without this,
			// knowledge_search returns a hit from a skill collection,
			// the LLM tries fetch_knowledge_doc(doc_id), and gets
			// "not found" because the predicate disagrees with the
			// search scope. Same skill scope the search used.
			for _, sk := range scopeSkills {
				for _, cid := range sk.AttachedCollections {
					if cid = strings.TrimSpace(cid); cid != "" {
						exact[collectionSource(cid)] = true
					}
				}
			}
			if len(t.agent.AttachedCollections) == 0 {
				for _, c := range ListCollections(nil, "") {
					if IsDeploymentScope(c) {
						exact[collectionSource(c.ID)] = true
					}
				}
			}
			allowSource := func(src string) bool {
				if src == agentPrefix || strings.HasPrefix(src, agentPrefix+":") {
					return true
				}
				return exact[src]
			}
			if VectorDB == nil {
				return "", errors.New("vector store unavailable")
			}
			var chunks []EmbeddedChunk
			for _, key := range VectorDB.Keys(EmbeddedChunks) {
				var c EmbeddedChunk
				if !VectorDB.Get(EmbeddedChunks, key, &c) {
					continue
				}
				if c.ReportID != docID {
					continue
				}
				if !allowSource(c.Source) {
					continue
				}
				chunks = append(chunks, c)
			}
			if len(chunks) == 0 {
				return fmt.Sprintf("No document found with doc_id=%q in your accessible knowledge corpus. The doc_id either doesn't exist, has been deleted, or belongs to a corpus you can't access. If you got the doc_id from a recent knowledge_search call and the document was deleted between turns, re-run the search.", docID), nil
			}
			// Order by Section (alphabetical groups same-section parts
			// together; (part 1)/(part 2) suffixes preserve order
			// within a section), then ID for stable tiebreak. Not perfect
			// for docs whose section order was meaningful, but better
			// than DB-key order (which is effectively random for UUIDs).
			sort.Slice(chunks, func(i, j int) bool {
				if chunks[i].Section != chunks[j].Section {
					return chunks[i].Section < chunks[j].Section
				}
				return chunks[i].ID < chunks[j].ID
			})
			docName := chunkDocName(chunks[0].Section)
			if docName == "" {
				docName = "(unnamed document)"
			}
			var b strings.Builder
			fmt.Fprintf(&b, "# %s\n\n", docName)
			lastSection := ""
			for _, c := range chunks {
				section := strings.TrimSpace(strings.TrimPrefix(c.Section, "## "))
				// Skip the section header on chunks whose Section
				// equals the doc title (e.g. single-section docs where
				// chunkDocName(Section) and Section are the same string).
				if section != lastSection && section != "" && section != docName {
					fmt.Fprintf(&b, "## %s\n\n", section)
					lastSection = section
				}
				if c.Kind != "" {
					fmt.Fprintf(&b, "*[%s]* ", c.Kind)
				}
				b.WriteString(strings.TrimSpace(c.Text))
				b.WriteString("\n\n")
			}
			out := strings.TrimSpace(b.String())
			if len(out) > maxChars {
				truncated := out[:maxChars]
				// Cut at a paragraph boundary when possible so the
				// truncation doesn't land mid-sentence.
				if idx := strings.LastIndex(truncated, "\n\n"); idx > maxChars/2 {
					truncated = truncated[:idx]
				}
				out = truncated + fmt.Sprintf("\n\n[…truncated; full document is %d chars. Pass max_chars=%d to fetch more, or use knowledge_search with a tighter query to find the specific section you need.]", len(out), fetchKnowledgeDocCap)
			}
			return out, nil
		},
	}
}

// memorySearch is the search-action implementation. Same ranking
// pass + topic scoping as knowledge_search but filters to derived
// chunks only. Returns a numbered text block with mem_ids so the
// LLM can pass them to memory(action="forget", id=...) for
// surgical cleanup.
func (t *chatTurn) memorySearch(args map[string]any) (string, error) {
	query := strings.TrimSpace(stringArg(args, "query"))
	if query == "" {
		return "", errors.New("query is required")
	}
	k := ReferenceRecallK()
	if v, ok := args["k"].(float64); ok && v > 0 {
		k = int(v)
		if maxK := KnowledgeMaxK(); k > maxK {
			k = maxK
		}
	}
	topic := normalizeTopic(stringArg(args, "topic"))
	ctx, cancel := context.WithTimeout(context.Background(), knowledgeIngestTimeout())
	defer cancel()
	hits := searchAgentKnowledge(ctx, t.app.DB, t.user, t.ownerUser, t.agent.ID, topic, query, k, t.skillsActive, t.agent.AttachedCollections, ChunkScopeDerivedOnly)
	rawHits := len(hits)
	filtered := hits[:0]
	for _, h := range hits {
		if h.Score < manualSearchMinScore {
			continue
		}
		filtered = append(filtered, h)
	}
	hits = filtered
	dropped := rawHits - len(hits)
	if len(hits) == 0 {
		if dropped > 0 {
			return fmt.Sprintf("No strong matches. Vector search returned %d derived chunk(s) but ALL scored below the relevance floor (%.2f) — they would have been tangentially related. Do NOT speculate from absent results. Rephrase the query, widen by passing topic=\"\", or proceed without prior context.", dropped, manualSearchMinScore), nil
		}
		return "No matching derived recollections. The Memory layer is empty for this query — try knowledge_search for curated content, or proceed without prior context and call memory(action=\"save\") after you investigate.", nil
	}
	var b strings.Builder
	for i, h := range hits {
		if i > 0 {
			b.WriteString("\n\n")
		}
		topic := strings.TrimSpace(strings.TrimPrefix(h.Section, "## "))
		if topic == "" {
			topic = "(no topic)"
		}
		fmt.Fprintf(&b, "%d. %s", i+1, topic)
		if h.Date != "" {
			if dt, err := time.Parse(time.RFC3339, h.Date); err == nil {
				fmt.Fprintf(&b, " (%s)", dt.Format("2006-01-02"))
			}
		}
		b.WriteString("\n")
		fmt.Fprintf(&b, "   mem_id: %s\n   ", h.ID)
		// Excerpt (not the full chunk) — mirrors knowledge_search so recall doesn't
		// dump whole documents into the context window every time it fires.
		b.WriteString(strings.ReplaceAll(knowledgeSearchExcerpt(h.Text), "\n", "\n   "))
	}
	out := b.String()
	// Reference → graph bridge: fold in the structured relationships for any known
	// entity these passages name, mirroring the graph → Reference bridge recall_about
	// runs in the other direction — so one recall returns unstructured + structured
	// together. Scan the full hit text (not just the rendered excerpt) for coverage.
	var scan strings.Builder
	for _, h := range hits {
		scan.WriteString(h.Text)
		scan.WriteByte('\n')
	}
	if g := t.passageRelatedGraph(scan.String()); g != "" {
		out += "\n\n" + g
	}
	return out, nil
}

// memoryForget is the forget-action implementation. Two modes:
// surgical (id=...) and bulk (query=...). Caps at maxForgetK so a
// single fuzzy query can't clear the whole index — admin per-agent
// wipe in the Memory modal is the path for bulk reset.
func (t *chatTurn) memoryForget(args map[string]any) (string, error) {
	explicitID := strings.TrimSpace(stringArg(args, "id"))
	query := strings.TrimSpace(stringArg(args, "query"))
	if explicitID == "" && query == "" {
		return "", errors.New("either id or query is required")
	}

	// ID-mode: targeted delete of one chunk. Same allow predicate
	// as searchAgentKnowledge so an LLM can't pass a mem_id it
	// found in another context and reach across into someone
	// else's memory store.
	if explicitID != "" {
		if VectorDB == nil {
			return "", errors.New("vector store unavailable")
		}
		var c EmbeddedChunk
		if !VectorDB.Get(EmbeddedChunks, explicitID, &c) {
			return fmt.Sprintf("No chunk with mem_id=%q in your accessible memory — it may have already been deleted, or the id belongs to a corpus you can't access.", explicitID), nil
		}
		agentPrefix := knowledgeSource(t.user, t.agent.ID, "")
		exact := make(map[string]bool, len(t.agent.AttachedCollections)+4)
		for _, cid := range t.agent.AttachedCollections {
			if cid = strings.TrimSpace(cid); cid != "" {
				exact[collectionSource(cid)] = true
			}
		}
		if len(t.agent.AttachedCollections) == 0 {
			for _, col := range ListCollections(nil, "") {
				if IsDeploymentScope(col) {
					exact[collectionSource(col.ID)] = true
				}
			}
		}
		inScope := c.Source == agentPrefix || strings.HasPrefix(c.Source, agentPrefix+":") || exact[c.Source]
		if !inScope {
			return fmt.Sprintf("No chunk with mem_id=%q in your accessible memory — it may have already been deleted, or the id belongs to a corpus you can't access.", explicitID), nil
		}
		// Only derived chunks are LLM-deletable. Curated content
		// (uploads, shared KB) is admin-managed.
		if chunkProvenance(c.Source, c.ReportID) != "derived" {
			return fmt.Sprintf("mem_id=%q points at curated content (uploaded doc / shared KB / collection), not a memory entry. memory(action=\"forget\") only deletes derived chunks. Curated content is admin-managed.", explicitID), nil
		}
		topic := strings.TrimSpace(strings.TrimPrefix(c.Section, "## "))
		// Chunks live in VectorDB (a separate, possibly relocated store), NOT
		// t.app.DB — deleting against the main DB is a silent no-op that leaves
		// the chunk searchable. Delete where the chunk actually lives.
		DeleteChunksByIDs(VectorDB, []string{explicitID})
		Log("[orchestrate.memory.forget] user=%q agent=%q deleted by id=%s topic=%q",
			t.user, t.agent.ID, explicitID, topic)
		return fmt.Sprintf("Deleted 1 memory entry — mem_id=%s, topic=%q.", explicitID, topic), nil
	}

	// Query-mode: vector-search delete.
	k := 3
	if v, ok := args["k"].(float64); ok && v > 0 {
		k = int(v)
		if k > maxForgetK {
			k = maxForgetK
		}
	}
	topic := normalizeTopic(stringArg(args, "topic"))
	ctx, cancel := context.WithTimeout(context.Background(), knowledgeIngestTimeout())
	defer cancel()
	hits := searchAgentKnowledge(ctx, t.app.DB, t.user, t.ownerUser, t.agent.ID, topic, query, k, t.skillsActive, t.agent.AttachedCollections, ChunkScopeDerivedOnly)
	// Apply the same relevance floor memory_search / knowledge_search use, so a
	// loose forget query can't delete tangentially-related chunks it wouldn't
	// even surface. Filtering here (not just checking len==0) keeps forget's
	// precision aligned with search.
	kept := hits[:0]
	for _, h := range hits {
		if h.Score < manualSearchMinScore {
			continue
		}
		kept = append(kept, h)
	}
	hits = kept
	if len(hits) == 0 {
		return "No matching derived chunks to forget — Reference Memory has nothing close enough to that query (above the relevance floor) under this agent.", nil
	}
	ids := make([]string, 0, len(hits))
	var b strings.Builder
	noun := "entries"
	if len(hits) == 1 {
		noun = "entry"
	}
	fmt.Fprintf(&b, "Deleted %d memory %s:\n", len(hits), noun)
	for i, h := range hits {
		if h.ID == "" {
			continue
		}
		ids = append(ids, h.ID)
		topicLabel := strings.TrimSpace(strings.TrimPrefix(h.Section, "## "))
		fmt.Fprintf(&b, "%d. %s — mem_id=%s\n   %s\n", i+1, topicLabel, h.ID, knowledgeSearchExcerpt(h.Text))
		Log("[orchestrate.memory.forget] user=%q agent=%q deleted chunk id=%s topic=%q score=%.3f",
			t.user, t.agent.ID, h.ID, h.Section, h.Score)
	}
	// Chunks live in VectorDB, not t.app.DB (see id-mode note above).
	DeleteChunksByIDs(VectorDB, ids)
	return strings.TrimRight(b.String(), "\n"), nil
}

// maxForgetK caps how many chunks one knowledge_forget call can
// delete. Tight on purpose — a loose query that returns many matches
// shouldn't be able to wipe the agent's index in one fell swoop. If
// the agent needs to clear everything under a topic, the admin
// per-agent wipe button is the right path.
const maxForgetK = 10
