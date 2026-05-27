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

// knowledgeIngestTimeout caps any embedding round-trip — long stalls
// would hang the consolidation goroutine or the user-facing tool call.
const knowledgeIngestTimeout = 45 * time.Second

// classifyTopicTimeout caps the topic-classification LLM call.
// Classification is on the user-facing critical path (runPlan can't
// start until the topic is known), so a generous-but-bounded cap
// keeps a stuck LLM from holding up the turn.
const classifyTopicTimeout = 20 * time.Second

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

// classifyTopicForQuestion asks the worker LLM to label the question
// with a short snake_case topic slug. Existing topics for this
// (user, agent) are surfaced as a hint so the classifier prefers
// reusing them — keeps "ssh_keys" from drifting into "sshkey",
// "ssh_key", "ssh-keys" across turns. Errors fall back to
// generalTopic so the turn proceeds with a usable namespace.
func classifyTopicForQuestion(ctx context.Context, app *OrchestrateApp, db Database, user, agentID, question string) string {
	if app == nil {
		return generalTopic
	}
	existing := listAgentTopics(db, user, agentID)
	hint := ""
	if len(existing) > 0 {
		hint = "\n\nExisting topics you may want to reuse: " + strings.Join(existing, ", ")
	}
	sys := "Pick a short topic slug (snake_case, 1-3 words) that best categorizes the user's question for knowledge-base storage. Reuse existing topics when applicable. Reply with ONLY the slug, no explanation." + hint
	cctx, cancel := context.WithTimeout(ctx, classifyTopicTimeout)
	defer cancel()
	// Thinking OFF: picking a snake_case slug is a deterministic
	// classification, not a reasoning task. Without this the worker
	// burns its full thinking budget (~10s, up toward classifyTopicTimeout)
	// deliberating over a 1-3 word label — same waste the ack avoids.
	resp, err := app.WorkerChat(cctx,
		[]Message{{Role: "user", Content: question}},
		WithSystemPrompt(sys), WithMaxTokens(20), WithThink(false),
	)
	if err != nil || resp == nil {
		return generalTopic
	}
	return normalizeTopic(resp.Content)
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
	// Important: use T.DB directly, NOT T.DB.Bucket("knowledge").
	// The orchestrate runner writes chunks via ingestKnowledgeForTurn
	// using `t.app.DB` (which IS T.DB) without sub-bucketing, so
	// reads must match the same scope to find the data.
	switch r.Method {
	case http.MethodGet:
		n := countAgentKnowledgeChunks(T.DB, user, agentID)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int{"chunks": n})
	case http.MethodDelete:
		removed := WipeChunksBySourcePrefix(T.DB, agentKnowledgePrefix(user, agentID))
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
		for _, key := range T.DB.Keys(EmbeddedChunks) {
			var c EmbeddedChunk
			if !T.DB.Get(EmbeddedChunks, key, &c) {
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
		for _, key := range T.DB.Keys(EmbeddedChunks) {
			var c EmbeddedChunk
			if !T.DB.Get(EmbeddedChunks, key, &c) {
				continue
			}
			if scope(c) {
				T.DB.Unset(EmbeddedChunks, key)
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
	for _, key := range T.DB.Keys(EmbeddedChunks) {
		var c EmbeddedChunk
		if !T.DB.Get(EmbeddedChunks, key, &c) {
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
	for _, key := range T.DB.Keys(EmbeddedChunks) {
		var c EmbeddedChunk
		if !T.DB.Get(EmbeddedChunks, key, &c) {
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
		T.DB.Unset(EmbeddedChunks, key)
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
//   "uploaded" — files the user uploaded via the Knowledge button
//                (orch-upload-*) or autofill-fetched docs in a
//                collection (autofill-*)
//   "shared"   — admin-curated docs on the agent (agent-shared:* source)
//                or chunks under any collection source (always authoritative
//                because they came from a deliberate ingest)
//   "derived"  — anything else: synthesis auto-ingests, closer findings,
//                LLM knowledge_save calls. The LLM's own reasoning
//                captured at end-of-turn — useful but lower priority
//                than authoritative material.
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
		IngestPagedReport(r.Context(), T.DB, knowledgeSource(user, agentID, "attachments"), reportID, doc)
	} else {
		IngestReport(r.Context(), T.DB, knowledgeSource(user, agentID, "attachments"), reportID, doc)
	}
	recordAgentTopic(T.DB, user, agentID, "attachments")
	chunks := countReportChunks(T.DB, reportID)
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
	for _, key := range T.DB.Keys(EmbeddedChunks) {
		var c EmbeddedChunk
		if !T.DB.Get(EmbeddedChunks, key, &c) {
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
		// Prefer the SHORTEST section as the display name — the first
		// chunk's section is the document title ("## <name>"); later
		// chunks may be subsection titles within it.
		if c.Section != "" && (g.name == "" || len(c.Section) < len(g.name)) {
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
	for _, key := range T.DB.Keys(EmbeddedChunks) {
		var c EmbeddedChunk
		if !T.DB.Get(EmbeddedChunks, key, &c) {
			continue
		}
		if c.ReportID != reportID {
			continue
		}
		if !strings.HasPrefix(c.Source, prefix) {
			continue // other agent's chunk with same ID — refuse cross-scope delete
		}
		T.DB.Unset(EmbeddedChunks, key)
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
	IngestReport(ctx, db, knowledgeSource(user, agentID, topic), reportID, doc)
	if topic != "" {
		recordAgentTopic(db, user, agentID, topic)
	}
}

// searchAgentKnowledge returns up to k semantically relevant chunks
// stored under any source the calling agent is allowed to see:
//   - the agent's own per-(user, agent) corpus (topic-scoped + agent-wide)
//   - each active skill's self-training corpus ("skill:<id>")
//   - each collection attached to those active skills ("collection:<id>")
//   - each collection directly attached to the agent ("collection:<id>")
//
// Unlike the old cascade-by-source path that early-returned on the
// first source with hits, this does a UNION search: one vector pass
// across the entire chunk table, filtered to the allowed-source set,
// ranked by relevance. This is the right shape for skill collections
// because the most relevant chunk might live in a skill's reference
// PDF even when the agent's own corpus has marginally-relevant
// chunks — we want the vector model picking, not source priority.
//
// activeSkills passes the SkillRecords selected by the classifier
// this turn; the function reads each one's ID + AttachedCollections.
// Pass nil/empty when no skills are active (Builder, classifier off).
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

func searchAgentKnowledge(ctx context.Context, db Database, user, agentID, topic, query string, k int, activeSkills []SkillRecord, agentAttachedCollections []string, scope ChunkScope) []SearchHit {
	if db == nil || strings.TrimSpace(query) == "" || k <= 0 {
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
	// Per-agent shared KB was deprecated in favor of attached
	// collections — admins curate corpus by minting a Collection and
	// attaching it to the agent (which makes the corpus reusable
	// across agents instead of stranded on one). Any chunks still
	// living under agentSharedSource() from before the move are no
	// longer reachable from RAG; the admin can re-ingest them as a
	// collection if they still matter.
	exact := make(map[string]bool, len(agentAttachedCollections)+4)
	// Agent-attached collections are always in scope. Skills no
	// longer contribute corpus to the RAG predicate.
	for _, cid := range agentAttachedCollections {
		if cid = strings.TrimSpace(cid); cid != "" {
			exact[collectionSource(cid)] = true
		}
	}
	// Deployment-scoped collections auto-attach when the agent
	// has no curated AttachedCollections (the "default = open"
	// rule from the design discussion: empty list = "give me the
	// defaults"; explicit list = "I picked these on purpose,
	// leave me alone"). Agents wanting strict isolation set an
	// explicit AttachedCollections list — even if it's just their
	// one curated corpus — and the deployment defaults skip them.
	var deploymentSources []string
	if len(agentAttachedCollections) == 0 {
		for _, c := range ListCollections(nil, "") { // empty user → deployment only
			if IsDeploymentScope(c) {
				src := collectionSource(c.ID)
				exact[src] = true
				deploymentSources = append(deploymentSources, src)
			}
		}
	}
	_ = activeSkills // kept in signature for callers; no corpus contribution today
	allow := func(c EmbeddedChunk) bool {
		// Source allow-list (prefix for agent corpus, exact for the rest).
		inAllowed := false
		if c.Source == agentPrefix || strings.HasPrefix(c.Source, agentPrefix+":") {
			inAllowed = true
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
	var hits []SearchHit
	if len(vec) > 0 {
		hits = SearchChunksByPredicate(db, allow, vec, k)
	} else {
		hits = SearchChunksSubstringByPredicate(db, allow, query, k)
	}
	// Deployment-collection chunks live in RootDB (research /
	// debate / answer ingest write there via knowledge.KnowledgeDB =
	// RootDB). When deployment collections are in scope this turn,
	// run a second pass against RootDB so those chunks join the
	// result set. Merged + re-sorted by score, capped at k.
	if len(deploymentSources) > 0 && RootDB != nil && db != RootDB {
		deployAllow := func(c EmbeddedChunk) bool {
			// Only deployment collection sources; the agent's per-
			// user predicate above already covered everything else.
			for _, src := range deploymentSources {
				if c.Source == src {
					// Reuse the provenance gate from the main allow.
					return allow(c)
				}
			}
			return false
		}
		var rootHits []SearchHit
		if len(vec) > 0 {
			rootHits = SearchChunksByPredicate(RootDB, deployAllow, vec, k)
		} else {
			rootHits = SearchChunksSubstringByPredicate(RootDB, deployAllow, query, k)
		}
		if len(rootHits) > 0 {
			hits = mergeHitsByScore(hits, rootHits, k)
		}
	}
	return hits
}

// mergeHitsByScore combines two hit slices, dedups by chunk ID,
// sorts by Score descending, and caps at k. Used to merge per-app
// + RootDB query results when deployment collections are in scope.
func mergeHitsByScore(a, b []SearchHit, k int) []SearchHit {
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
	for _, h := range a {
		if seen[h.ID] {
			continue
		}
		seen[h.ID] = true
		merged = append(merged, h)
	}
	for _, h := range b {
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

// autoInjectMinScore is the cosine-similarity floor for chunks that
// get auto-injected into the orchestrator's prompt at turn start.
// Vector search returns top-K matches regardless of relevance score,
// so weak matches (a chunk from a prior session that's only
// tangentially related) would otherwise leak into the prompt and pull
// the LLM toward old context — "the system thought I was running a
// query from an earlier query." 0.45 is the sweet spot from
// observation: 0.5+ rejects too many genuinely-relevant chunks (esp.
// short prior summaries where the embedding signal is thin); 0.4 lets
// noise through. Manual searches via knowledge_search / memory_search
// DON'T apply this floor — the LLM asked for them, so it can judge
// relevance itself.
const autoInjectMinScore = 0.45

// renderKnowledgePromptSection formats a slice of search hits as a
// markdown block suitable for prepending to the orchestrator's user
// prompt. Chunks are grouped by document (by ReportID) so the LLM can
// tell when hits come from different documents — variants, versions,
// or potentially-conflicting sources — versus the same document split
// into multiple chunks. Without this grouping, two variants of K8s
// API docs from different versions would visually merge into one
// undifferentiated knowledge blob.
//
// Empty input → empty string (callers can concatenate without
// branching).
func renderKnowledgePromptSection(hits []SearchHit) string {
	if len(hits) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Possibly-relevant background\n\n")
	b.WriteString("Auto-retrieved from your own corpus (uploaded docs, attached skill documents, prior findings) based on vector similarity to the current message. **Treat as background context, NOT as established current state.** Apply ONLY if the chunks clearly match what's being asked right now — vector search can surface tangentially-related material from past sessions that has nothing to do with the current intent. If the user's question seems unrelated to these chunks, ignore them entirely and answer the actual question. **Recency: each document below shows its captured-on date. Prefer recent content when it conflicts with older content; cite the as-of date when freshness matters (\"per the docs as of 2024-03…\").** When chunks from DIFFERENT documents DO match and disagree on a specific point, surface the conflict (\"Doc A says X but Doc B says Y\") rather than averaging. If a chunk conflicts with what the user said in THIS turn, the user wins.\n\n")
	// Group hits by ReportID (document identity), preserving first-seen
	// order so the highest-scoring document leads.
	type docGroup struct {
		name   string
		hits   []SearchHit
	}
	groupsByID := map[string]*docGroup{}
	var order []string
	for _, h := range hits {
		key := h.ReportID
		if key == "" {
			key = h.ID // fallback: unique per chunk so an empty reportID doesn't merge unrelated chunks
		}
		g, ok := groupsByID[key]
		if !ok {
			g = &docGroup{name: chunkDocName(h.Section)}
			groupsByID[key] = g
			order = append(order, key)
		}
		g.hits = append(g.hits, h)
	}
	for _, key := range order {
		g := groupsByID[key]
		name := g.name
		if name == "" {
			name = "(unnamed document)"
		}
		// Pull the captured-on date from the first hit in this group
		// (all chunks of a document share an ingestion timestamp).
		// Format as YYYY-MM-DD for the prompt; skip silently when
		// missing (legacy chunks pre-Date field).
		dateSuffix := ""
		if len(g.hits) > 0 && g.hits[0].Date != "" {
			if t, err := time.Parse(time.RFC3339, g.hits[0].Date); err == nil {
				dateSuffix = "  *(captured " + t.Format("2006-01-02") + ")*"
			}
		}
		fmt.Fprintf(&b, "### From document: %s%s\n\n", name, dateSuffix)
		for _, h := range g.hits {
			// Provenance tag — when the chunk came from a tagged
			// HTML region (comments, related-link rail, author bio),
			// surface that so the LLM frames the hit appropriately
			// ("one commenter noted…" vs "the doc says…") rather
			// than treating it as authoritative.
			if h.Kind != "" {
				fmt.Fprintf(&b, "*[%s]* ", h.Kind)
			}
			b.WriteString(strings.TrimSpace(h.Text))
			b.WriteString("\n\n")
		}
	}
	b.WriteString("---\n\n")
	return b.String()
}

// --- Tools the LLM can call directly --------------------------------------

// saveMemoryToolDef builds the AgentToolDef that lets the
// orchestrator / worker explicitly record a derived finding into the
// Reference Memory layer (vector-grown, searchable via memory_search).
// Closure-bound to the chatTurn so it picks up (user, agent_id)
// without an extra ToolSession round-trip.
//
// Writes to Reference Memory only. The Knowledge layer (uploaded
// files) is admin-managed read-only; Explicit Memory (always-in-
// prompt) is the store_fact tool's job.
func (t *chatTurn) saveMemoryToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "memory_save",
			Description: "Persist COMPLICATED REFERENCE MATERIAL you MAY need later — API specs, website navigation instructions, configuration recipes, working approaches you figured out, document-derived technical details. Saved into your Reference Memory (vector-searchable), retrieved on-demand via `memory_search`. **Pull-only**: these findings do NOT auto-inject into future prompts; you have to explicitly search for them when a future question triggers your recall.\n\n**Use memory_save when**: the finding is non-trivial to re-derive, specific enough to be useful for a similar future question, AND would clutter your prompt if always-in-context. Right examples: \"Acme API uses cursor-based pagination with `after` token; max page size 100\", \"To configure Nginx rate-limiting per-IP, use the limit_req_zone directive in the http block\", \"To install ffmpeg with libx264 on Ubuntu, ...\". Paragraph-length self-contained findings work best.\n\n**Use store_fact instead when**: the note shapes how you respond to ANY future question (user preferences, recurring constraints, identity facts). Those are always-in-prompt; memory_save is searchable reference.\n\nFindings are namespaced by `topic` (snake_case slug like `acme_api`, `nginx_config`). Defaults to the turn's classified topic.\n\nDO NOT save findings already retrieved via memory_search OR knowledge_search this turn — saving the same content again compounds noise. Save ONLY information genuinely NEW: facts from web_search / fetch_url / browse_page / external sources OR from an uploaded document.",
			Parameters: map[string]ToolParam{
				"topic": {
					Type:        "string",
					Description: "Snake_case topic slug (1-3 words). Reuse an existing slug when possible to keep retrieval sharp. Leave blank to inherit the turn's classified topic.",
				},
				"subject": {
					Type:        "string",
					Description: "Short heading for THIS specific finding — what the chunk's about. Example: \"Acme API rotates session tokens every 24h\". Optional; defaults to the topic slug.",
				},
				"content": {
					Type:        "string",
					Description: "The finding itself. Several sentences to a paragraph; include enough context that it'll make sense without seeing this conversation. No need to repeat the subject.",
				},
			},
			Required: []string{"content"},
			Caps:     []Capability{CapWrite},
		},
		Handler: func(args map[string]any) (string, error) {
			topic := normalizeTopic(stringArg(args, "topic"))
			content := strings.TrimSpace(stringArg(args, "content"))
			if content == "" {
				return "", errors.New("content is required")
			}
			// Default to the turn's classified topic when the LLM omitted
			// or sent something that collapsed to the fallback. Keeps
			// namespacing sharp even when the worker skips the arg.
			if rt := t.resolveTopic(); topic == generalTopic && rt != "" {
				topic = rt
			}
			subject := strings.TrimSpace(stringArg(args, "subject"))
			if subject == "" {
				subject = strings.TrimSpace(stringArg(args, "title"))
			}
			if subject == "" {
				subject = topic
			}
			ctx, cancel := context.WithTimeout(context.Background(), knowledgeIngestTimeout)
			defer cancel()
			ingestAgentKnowledge(ctx, t.app.DB, t.user, t.agent.ID, topic, subject, content)
			return fmt.Sprintf("Saved %d chars under topic %q in Memory. Future similar questions can retrieve this via memory_search or get it auto-injected.",
				len(content), topic), nil
		},
	}
}

// searchKnowledgeToolDef builds the read-side tool over the
// Knowledge layer — read-only authoritative content the user/admin
// uploaded (PDFs, shared KB, collections, skill self-training).
// The LLM never writes to Knowledge; it only reads. Derived chunks
// from memory_save / synthesis ingest live in Reference Memory and
// have their own memory_search tool. Closure-bound to the chatTurn.
func (t *chatTurn) searchKnowledgeToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "knowledge_search",
			Description: "Search THIS agent's Knowledge layer — the read-only authoritative content the user/admin provided: uploaded documents, shared KB, collections, skill self-training. Returns up to k JSON entries ranked by relevance. Topic-scoped by default: the framework searches within the turn's classified topic first and falls back to the agent's full corpus if nothing matches. Pass an explicit `topic` to query a different bucket. Call when the user asks something that should have a definite answer in the uploaded material.\n\nKnowledge is read-only — you cannot write to it. To save your own findings for later, use `memory_save` (Reference Memory, vector-searchable) or `store_fact` (Explicit Memory, always-in-prompt). Distinct from `memory_search` (your own prior derived findings) and `list_facts` (your pre-injected Explicit Memory entries).",
			Parameters: map[string]ToolParam{
				"query": {
					Type:        "string",
					Description: "Natural-language search query. The user's current question often works well, possibly trimmed to the gist.",
				},
				"topic": {
					Type:        "string",
					Description: "Optional snake_case topic slug to scope the search. Defaults to the turn's classified topic (which is usually what you want). Supply explicitly to look in a different subject area.",
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
			k := 5
			if v, ok := args["k"].(float64); ok && v > 0 {
				k = int(v)
				if k > 20 {
					k = 20
				}
			}
			// Topic scoping: explicit arg wins; else default to the
			// turn's classified topic so the search is sharp. Pass
			// empty string to deliberately broaden to agent-wide.
			topic := normalizeTopic(stringArg(args, "topic"))
			if topic == generalTopic {
				topic = t.resolveTopic()
			}
			ctx, cancel := context.WithTimeout(context.Background(), knowledgeIngestTimeout)
			defer cancel()
			hits := searchAgentKnowledge(ctx, t.app.DB, t.user, t.agent.ID, topic, query, k, t.skillsActive, t.agent.AttachedCollections, ChunkScopeCuratedOnly)
			if len(hits) == 0 {
				return "No matching curated content. The Knowledge layer (uploads, shared KB, collections) has nothing on that — try memory_search for the agent's own derived findings, or proceed without prior context.", nil
			}
			payload := make([]map[string]any, 0, len(hits))
			for _, h := range hits {
				payload = append(payload, map[string]any{
					"topic":      h.Section,
					"content":    h.Text,
					"score":      h.Score,
					// Provenance lets the LLM weight chunks by trustworthiness:
					//   "uploaded" — user-curated source (PDF, doc, autofill)
					//   "shared"   — admin-curated reference KB
					//   "derived"  — LLM's own prior synthesis (use cautiously;
					//                may compound errors if cited as ground truth)
					"provenance": chunkProvenance(h.Source, h.ReportID),
					// source_doc is the clean document name this chunk
					// came from — same across all chunks of the same
					// document. Lets the LLM tell when two chunks are
					// from DIFFERENT documents (variants, conflicts)
					// vs. the same one.
					"source_doc": chunkDocName(h.Section),
					// date is the captured-on / ingested-at timestamp
					// (RFC3339). LLM should cite it when freshness
					// matters and prefer newer content on conflicts.
					"date": h.Date,
					// locator is the sub-document citation pointer when
					// the source supports one (e.g. "page 12" for PDFs).
					// Empty for sources without a meaningful locator
					// (plain text, single-page notes, derived chunks).
					// LLM should cite it inline when present.
					"locator": h.Locator,
				})
			}
			out, err := json.Marshal(payload)
			if err != nil {
				return "", err
			}
			return string(out), nil
		},
	}
}

// searchMemoryToolDef builds the read-side tool over the derived
// Memory layer — the LLM's own accumulated recollections from
// memory_save / synthesis auto-ingest. Same ranking pass + topic
// scoping as knowledge_search but filters to derived chunks only.
// Closure-bound to the chatTurn.
func (t *chatTurn) searchMemoryToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "memory_search",
			Description: "Search THIS agent's Reference Memory — your accumulated recollections from prior turns (memory_save findings, synthesis auto-ingest). Returns up to k JSON entries ranked by relevance. Topic-scoped by default. **PULL-ONLY**: Reference Memory is no longer auto-injected into your prompt — call this tool explicitly when the current question reminds you of something you previously figured out (an API quirk, a working approach, a prior synthesis). Treat results as fuzzier than knowledge_search — Reference Memory is derived and may have drifted, so verify against curated sources when accuracy matters.\n\nDistinct from `knowledge_search` (read-only over uploaded files — authoritative source-of-truth content, AUTO-injected when relevant) and `list_facts` (your Explicit Memory entries, always pre-injected into your system prompt).",
			Parameters: map[string]ToolParam{
				"query": {
					Type:        "string",
					Description: "Natural-language search query. The user's current question often works well, possibly trimmed to the gist.",
				},
				"topic": {
					Type:        "string",
					Description: "Optional snake_case topic slug to scope the search. Defaults to the turn's classified topic (which is usually what you want). Supply explicitly to look in a different subject area.",
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
			k := 5
			if v, ok := args["k"].(float64); ok && v > 0 {
				k = int(v)
				if k > 20 {
					k = 20
				}
			}
			topic := normalizeTopic(stringArg(args, "topic"))
			if topic == generalTopic {
				topic = t.resolveTopic()
			}
			ctx, cancel := context.WithTimeout(context.Background(), knowledgeIngestTimeout)
			defer cancel()
			hits := searchAgentKnowledge(ctx, t.app.DB, t.user, t.agent.ID, topic, query, k, t.skillsActive, t.agent.AttachedCollections, ChunkScopeDerivedOnly)
			if len(hits) == 0 {
				return "No matching derived recollections. The Memory layer is empty for this query — try knowledge_search for curated content, or proceed without prior context and call memory_save after you investigate.", nil
			}
			payload := make([]map[string]any, 0, len(hits))
			for _, h := range hits {
				payload = append(payload, map[string]any{
					"topic":      h.Section,
					"content":    h.Text,
					"score":      h.Score,
					"provenance": chunkProvenance(h.Source, h.ReportID),
					"source_doc": chunkDocName(h.Section),
					"date":       h.Date,
					"locator":    h.Locator,
				})
			}
			out, err := json.Marshal(payload)
			if err != nil {
				return "", err
			}
			return string(out), nil
		},
	}
}

// forgetMemoryToolDef builds the AgentToolDef that lets the agent
// delete its own derived chunks when they go stale or stop being
// relevant. Operates over the Memory layer only — curated Knowledge
// content (uploads, shared KB) is admin-managed and has no in-LLM
// delete surface. Same scoping as memory_search (per-user,
// per-agent, optional topic). Capped at maxForgetK matches per call
// so a single fuzzy query can't clear the whole index — if the
// agent really wants to wipe everything under a topic, the right
// path is the
// admin per-agent wipe button in the Memory modal.
func (t *chatTurn) forgetMemoryToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "memory_forget",
			Description: "Delete chunks from THIS agent's Memory layer that are no longer relevant (outdated recollections, retracted claims, schema changes that invalidate prior findings). Searches like memory_search but DELETES matches instead of returning them. Capped at k matches per call (default 3, max 10) so a loose query can't clear the index. Use sparingly and with specific queries — broad queries delete more than you intended.\n\nOperates ONLY on the Memory layer (LLM-derived chunks via memory_save / synthesis ingest). Curated Knowledge content (uploads, shared KB) is admin-managed and cannot be deleted from inside the agent — that's the admin per-agent wipe button in the Memory modal. Distinct from store_fact (where overwriting a key is the normal update path); this is for vector chunks where the only update path is delete-and-re-save.",
			Parameters: map[string]ToolParam{
				"query": {
					Type:        "string",
					Description: "Natural-language description of WHAT TO FORGET. The system searches with this and deletes the top matches. Be specific — \"Acme API session token rotation policy from before 2025\" deletes the stale claim cleanly; \"Acme\" deletes anything tangentially related and probably wipes context you wanted to keep.",
				},
				"topic": {
					Type:        "string",
					Description: "Optional snake_case topic slug to scope the deletion. Defaults to the turn's classified topic. Pass an explicit topic when forgetting cross-topic.",
				},
				"k": {
					Type:        "number",
					Description: "Max matches to delete (default 3, cap 10). Lower is safer — if the first call deleted too little, you can call again.",
				},
			},
			Required: []string{"query"},
			Caps:     []Capability{CapWrite},
		},
		Handler: func(args map[string]any) (string, error) {
			query := strings.TrimSpace(stringArg(args, "query"))
			if query == "" {
				return "", errors.New("query is required")
			}
			k := 3
			if v, ok := args["k"].(float64); ok && v > 0 {
				k = int(v)
				if k > maxForgetK {
					k = maxForgetK
				}
			}
			topic := normalizeTopic(stringArg(args, "topic"))
			if topic == generalTopic {
				topic = t.resolveTopic()
			}
			ctx, cancel := context.WithTimeout(context.Background(), knowledgeIngestTimeout)
			defer cancel()
			hits := searchAgentKnowledge(ctx, t.app.DB, t.user, t.agent.ID, topic, query, k, t.skillsActive, t.agent.AttachedCollections, ChunkScopeDerivedOnly)
			if len(hits) == 0 {
				return "No matching derived chunks to forget — Reference Memory has nothing close enough to that query under this agent.", nil
			}
			ids := make([]string, 0, len(hits))
			deleted := make([]map[string]any, 0, len(hits))
			for _, h := range hits {
				if h.ID == "" {
					continue
				}
				ids = append(ids, h.ID)
				deleted = append(deleted, map[string]any{
					"id":      h.ID,
					"topic":   h.Section,
					"snippet": truncate(h.Text, 200),
					"score":   h.Score,
				})
				Log("[orchestrate.knowledge_forget] user=%q agent=%q deleted chunk id=%s topic=%q score=%.3f",
					t.user, t.agent.ID, h.ID, h.Section, h.Score)
			}
			DeleteChunksByIDs(t.app.DB, ids)
			payload := map[string]any{
				"deleted": deleted,
				"count":   len(deleted),
			}
			out, err := json.Marshal(payload)
			if err != nil {
				return "", err
			}
			return string(out), nil
		},
	}
}

// maxForgetK caps how many chunks one knowledge_forget call can
// delete. Tight on purpose — a loose query that returns many matches
// shouldn't be able to wipe the agent's index in one fell swoop. If
// the agent needs to clear everything under a topic, the admin
// per-agent wipe button is the right path.
const maxForgetK = 10
