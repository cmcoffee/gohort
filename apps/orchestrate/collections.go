// Document collections — user-owned named buckets of uploaded files,
// attachable to N agents for shared RAG. Distinct from the per-(user,
// agent) Knowledge corpus exposed in the Memory modal: collections are
// curated containers the user manages explicitly, the per-agent
// corpus is the agent's own accumulated knowledge from synthesis,
// closer findings, and per-agent uploads.
//
// Storage:
//   - Collection records live in the caller's per-user udb under
//     collectionsTable. Per-user scope is enforced by udb-isolation;
//     loadCollection refuses cross-user reads via the Owner check.
//   - Document chunks live in the app-wide EmbeddedChunks table with
//     Source = "collection:<collectionID>". The runner can search them
//     by source-prefix without knowing which user owns the collection.
//   - Each upload gets its own ReportID so per-document delete works
//     the same way the per-agent path does.

package orchestrate

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// (Collection data layer moved to core/collections.go — Collection
// struct + scope constants + Load/List/Save/Delete + CollectionSource
// all live there now. HTTP handlers, autofill, and suggest endpoints
// stay below; they're orchestrate-specific use sites.)
//
// Local thin wrappers keep the existing call sites here ergonomic
// (lowercase names that match the old style) without forcing every
// reference inside this file to use the package-qualified form.
// Phantom + admin + other apps already use the exported names from
// core directly.
func collectionSource(id string) string                            { return CollectionSource(id) }
func loadCollection(udb Database, u, id string) (Collection, bool) { return LoadCollection(udb, u, id) }
func listCollections(udb Database, u string) []Collection          { return ListCollections(udb, u) }
func saveCollection(udb Database, c Collection)                    { SaveCollection(udb, c) }
func deleteCollection(udb, appDB Database, u, id string) int {
	return DeleteCollection(udb, appDB, u, id)
}

// Aliases for the old unexported constant names used inside this
// file (HTTP handler reads, etc.). External callers should use
// the exported core.CollectionsTable / core.GlobalCollectionsTable.
const (
	collectionsTable       = CollectionsTable
	globalCollectionsTable = GlobalCollectionsTable
)

// collectionDB returns the database where a collection's vector chunks
// live: the dedicated shared vector store (VectorDB), for both user-
// and deployment-scoped collections. Partitioning is by the
// collectionSource(c.ID) tag, not by which physical store — so the
// scope argument no longer selects the handle. (Collection metadata
// still lives in T.DB / RootDB; this resolver is the chunk store only.)
// Every chunk-touching site in this file routes through here so the
// read / write store stays consistent.
func (T *OrchestrateApp) collectionDB(c Collection) Database {
	return VectorDB
}

// --- HTTP handlers --------------------------------------------------------

// handleCollections serves the collections-list endpoint.
//
//	GET  /api/collections        → [{id, name, description, chunks, documents, updated}]
//	POST /api/collections        → create (body: {name, description})
func (T *OrchestrateApp) handleCollections(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		cols := listCollections(udb, user)
		type out struct {
			Collection
			Documents int `json:"documents"`
			Chunks    int `json:"chunks"`
		}
		// Single pass over EmbeddedChunks instead of one walk per
		// collection. The old shape (collectionStats called per
		// collection) was O(N * M) for N collections + M chunks —
		// 10 collections × 5000 chunks = 50K reads + decryption
		// per page load. Walk once, bucket by collection ID.
		type stats struct{ docs, chunks int }
		statsByID := make(map[string]*stats, len(cols))
		seenReportPerCollection := make(map[string]map[string]bool, len(cols))
		hasDeploymentCol := false
		for _, c := range cols {
			statsByID[c.ID] = &stats{}
			seenReportPerCollection[c.ID] = map[string]bool{}
			if IsDeploymentScope(c) {
				hasDeploymentCol = true
			}
		}
		// countChunks walks one DB and credits stats for any collection
		// chunk whose ID is in the statsByID map.
		countChunks := func(d Database) {
			for _, key := range d.Keys(EmbeddedChunks) {
				var ch EmbeddedChunk
				if !d.Get(EmbeddedChunks, key, &ch) {
					continue
				}
				// Source format: collection:<id> (exact, no suffixes
				// today — collection chunks don't carry topic dims).
				const prefix = "collection:"
				if !strings.HasPrefix(ch.Source, prefix) {
					continue
				}
				id := strings.TrimPrefix(ch.Source, prefix)
				s, ok := statsByID[id]
				if !ok {
					continue // chunk from a collection the user doesn't own
				}
				s.chunks++
				seen := seenReportPerCollection[id]
				if !seen[ch.ReportID] {
					seen[ch.ReportID] = true
					s.docs++
				}
			}
		}
		// All collection chunks — user-scoped and deployment-scoped
		// alike — live in the dedicated shared vector store now,
		// partitioned by Source tag. One walk suffices.
		_ = hasDeploymentCol
		countChunks(VectorDB)
		results := make([]out, 0, len(cols))
		for _, c := range cols {
			s := statsByID[c.ID]
			results = append(results, out{
				Collection: c,
				Documents:  s.docs,
				Chunks:     s.chunks,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"collections": results})
	case http.MethodPost:
		var body struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(body.Name)
		if name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		c := Collection{
			ID:          UUIDv4(),
			Owner:       user,
			Name:        name,
			Description: strings.TrimSpace(body.Description),
			Created:     time.Now(),
		}
		c.WhenToUse = GenerateWhenToUse("collection", c.Name, c.Description)
		saveCollection(udb, c)
		Log("[orchestrate.collections] user=%q created collection %q (id=%s)", user, name, c.ID)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(c)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleCollectionOne serves per-collection routes:
//
//	GET    /api/collections/{id}                          → record + stats
//	PATCH  /api/collections/{id}                          → rename / update description
//	DELETE /api/collections/{id}                          → delete + wipe chunks
//	POST   /api/collections/{id}/upload                   → upload doc
//	GET    /api/collections/{id}/sources                  → list docs (reportID grouping)
//	DELETE /api/collections/{id}/sources/{reportID}       → delete doc
//	GET    /api/collections/{id}/search?q=...&k=N         → semantic search
func (T *OrchestrateApp) handleCollectionOne(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/collections/")
	if rest == "" {
		http.NotFound(w, r)
		return
	}
	var id, action string
	if slash := strings.IndexByte(rest, '/'); slash >= 0 {
		id = rest[:slash]
		action = rest[slash+1:]
	} else {
		id = rest
	}
	if id == "" {
		http.NotFound(w, r)
		return
	}
	c, found := loadCollection(udb, user, id)
	if !found {
		http.NotFound(w, r)
		return
	}

	switch {
	case action == "":
		switch r.Method {
		case http.MethodGet:
			docs, chunks := collectionStats(VectorDB, c.ID)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":                   c.ID,
				"owner":                c.Owner,
				"name":                 c.Name,
				"description":          c.Description,
				"created":              c.Created,
				"updated":              c.Updated,
				"documents":            docs,
				"chunks":               chunks,
				"filter_rules":         c.FilterRules,
				"classify_on_autofill": c.ClassifyOnAutofill,
			})
		case http.MethodPatch:
			var body struct {
				Name               *string `json:"name"`
				Description        *string `json:"description"`
				FilterRules        *string `json:"filter_rules"`
				ClassifyOnAutofill *bool   `json:"classify_on_autofill"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if body.Name != nil {
				if name := strings.TrimSpace(*body.Name); name != "" {
					c.Name = name
				}
			}
			if body.Description != nil {
				newDesc := strings.TrimSpace(*body.Description)
				if newDesc != c.Description {
					c.Description = newDesc
					c.WhenToUse = GenerateWhenToUse("collection", c.Name, c.Description)
				}
			}
			if body.FilterRules != nil {
				c.FilterRules = strings.TrimSpace(*body.FilterRules)
			}
			if body.ClassifyOnAutofill != nil {
				c.ClassifyOnAutofill = *body.ClassifyOnAutofill
			}
			// Backfill a missing cue on touch (legacy collections); a
			// description change above already regenerated it.
			if strings.TrimSpace(c.WhenToUse) == "" {
				c.WhenToUse = GenerateWhenToUse("collection", c.Name, c.Description)
			}
			saveCollection(udb, c)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(c)
		case http.MethodDelete:
			removed := deleteCollection(udb, T.DB, user, c.ID)
			Log("[orchestrate.collections] user=%q deleted collection %q (id=%s, %d chunks removed)", user, c.Name, c.ID, removed)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]int{"removed": removed})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case action == "upload":
		T.handleCollectionUpload(w, r, user, c)
	case action == "sources":
		T.handleCollectionSources(w, r, c)
	case strings.HasPrefix(action, "sources/"):
		reportID := strings.TrimPrefix(action, "sources/")
		T.handleCollectionSourceDelete(w, r, c, reportID)
	case action == "search":
		T.handleCollectionSearch(w, r, c)
	case action == "autofill":
		T.handleCollectionAutofill(w, r, c)
	case action == "suggest-description":
		T.handleCollectionSuggestDescription(w, r, udb, user, c)
	default:
		http.NotFound(w, r)
	}
}

// handleCollectionUpload extracts + ingests a document under the
// collection's source prefix. Mirrors handleAgentKnowledgeUpload but
// writes to collection:<id> instead of orchestrate:<user>:<agent>:attachments.
func (T *OrchestrateApp) handleCollectionUpload(w http.ResponseWriter, r *http.Request, user string, c Collection) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Name     string `json:"name"`
		MimeType string `json:"mime_type"`
		Data     string `json:"data"`
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
	const minUploadChars = 200
	if len(text) < minUploadChars {
		http.Error(w, fmt.Sprintf("extracted text too short (%d chars) — minimum is %d", len(text), minUploadChars), http.StatusBadRequest)
		return
	}
	reportID := fmt.Sprintf("collection-%s-%d", c.ID, time.Now().UnixNano())
	doc := "## " + name + "\n\n" + text
	chunkDB := T.collectionDB(c)
	IngestReport(r.Context(), chunkDB, collectionSource(c.ID), reportID, doc)
	// Bump the collection's updated timestamp so the list reorders.
	if udb, ok := requireUDB(w, r, T.DB); ok {
		fresh, found := loadCollection(udb, user, c.ID)
		if found {
			saveCollection(udb, fresh)
		}
	}
	chunks := countReportChunks(chunkDB, reportID)
	Log("[orchestrate.collections] user=%q uploaded %q to %q (%d chars → %d chunks, reportID=%s)",
		user, name, c.Name, len(text), chunks, reportID)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":     reportID,
		"name":   name,
		"chunks": chunks,
	})
}

// handleCollectionSources lists documents in the collection, grouped
// by reportID. Same shape as handleAgentKnowledgeSources.
func (T *OrchestrateApp) handleCollectionSources(w http.ResponseWriter, r *http.Request, c Collection) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	prefix := collectionSource(c.ID)
	type group struct {
		id     string
		name   string
		chunks int
		latest string
	}
	groups := map[string]*group{}
	chunkDB := T.collectionDB(c)
	for _, key := range chunkDB.Keys(EmbeddedChunks) {
		var ch EmbeddedChunk
		if !chunkDB.Get(EmbeddedChunks, key, &ch) {
			continue
		}
		if !strings.HasPrefix(ch.Source, prefix) {
			continue
		}
		g, ok := groups[ch.ReportID]
		if !ok {
			g = &group{id: ch.ReportID, name: ch.Section}
			groups[ch.ReportID] = g
		}
		g.chunks++
		if ch.Date > g.latest {
			g.latest = ch.Date
		}
		// Prefer the document Title (the topic/question stamped at ingest)
		// — it says what the doc is ABOUT. Title is uniform across a
		// report's chunks, so this is stable. Fall back to the shortest
		// section heading only for legacy chunks with no Title.
		if ch.Title != "" {
			g.name = ch.Title
		} else if sect := stripChunkPartSuffix(ch.Section); sect != "" && (g.name == "" || len(sect) < len(g.name)) {
			g.name = sect
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

// handleCollectionSourceDelete removes a single document's chunks.
func (T *OrchestrateApp) handleCollectionSourceDelete(w http.ResponseWriter, r *http.Request, c Collection, reportID string) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if reportID == "" {
		http.Error(w, "reportID required", http.StatusBadRequest)
		return
	}
	prefix := collectionSource(c.ID)
	removed := 0
	chunkDB := T.collectionDB(c)
	for _, key := range chunkDB.Keys(EmbeddedChunks) {
		var ch EmbeddedChunk
		if !chunkDB.Get(EmbeddedChunks, key, &ch) {
			continue
		}
		if ch.ReportID != reportID {
			continue
		}
		if !strings.HasPrefix(ch.Source, prefix) {
			continue
		}
		chunkDB.Unset(EmbeddedChunks, key)
		removed++
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int{"removed": removed})
}

// handleCollectionSearch runs a vector-similarity search over the
// collection's chunks. Returns ranked hits with section + text preview
// so a user can verify what's indexed without going through an agent.
func (T *OrchestrateApp) handleCollectionSearch(w http.ResponseWriter, r *http.Request, c Collection) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		http.Error(w, "q required", http.StatusBadRequest)
		return
	}
	k := 6
	if kStr := r.URL.Query().Get("k"); kStr != "" {
		if n, err := strconvAtoi(kStr); err == nil && n > 0 && n <= 20 {
			k = n
		}
	}
	cfg := GetEmbeddingConfig()
	var hits []SearchHit
	chunkDB := T.collectionDB(c)
	if cfg.Enabled {
		vec, err := Embed(r.Context(), q)
		if err == nil {
			hits = SearchChunksBySource(chunkDB, collectionSource(c.ID), vec, k)
		}
	}
	// Fallback: simple substring scan if embeddings unavailable.
	if len(hits) == 0 {
		hits = substringHitsBySource(chunkDB, collectionSource(c.ID), q, k)
	}
	type hitOut struct {
		Section string  `json:"section"`
		Text    string  `json:"text"`
		Score   float64 `json:"score,omitempty"`
	}
	out := make([]hitOut, 0, len(hits))
	for _, h := range hits {
		text := h.Text
		if len(text) > 600 {
			text = text[:600] + "…"
		}
		out = append(out, hitOut{Section: h.Section, Text: text, Score: float64(h.Score)})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"hits": out})
}

// substringHitsBySource is the embedding-disabled fallback for
// handleCollectionSearch. Returns up to k chunks whose text contains
// the query (case-insensitive), in arbitrary order.
func substringHitsBySource(db Database, sourcePrefix, query string, k int) []SearchHit {
	q := strings.ToLower(query)
	var out []SearchHit
	for _, key := range db.Keys(EmbeddedChunks) {
		var ch EmbeddedChunk
		if !db.Get(EmbeddedChunks, key, &ch) {
			continue
		}
		if !strings.HasPrefix(ch.Source, sourcePrefix) {
			continue
		}
		if strings.Contains(strings.ToLower(ch.Section+"\n"+ch.Text), q) {
			out = append(out, SearchHit{
				Section: ch.Section,
				Text:    ch.Text,
				Source:  ch.Source,
				Score:   0,
			})
			if len(out) >= k {
				return out
			}
		}
	}
	return out
}

// --- helpers --------------------------------------------------------------

// collectionStats returns (documents, chunks) for a collection.
func collectionStats(db Database, id string) (docs, chunks int) {
	if db == nil || id == "" {
		return 0, 0
	}
	prefix := collectionSource(id)
	seenReports := map[string]bool{}
	for _, key := range db.Keys(EmbeddedChunks) {
		var ch EmbeddedChunk
		if !db.Get(EmbeddedChunks, key, &ch) {
			continue
		}
		if !strings.HasPrefix(ch.Source, prefix) {
			continue
		}
		chunks++
		if !seenReports[ch.ReportID] {
			seenReports[ch.ReportID] = true
			docs++
		}
	}
	return docs, chunks
}

// handleSkillsList exposes the calling user's skills as a thin list
// (id + name + description) so the Documents drill-in picker can
// render checkboxes without fetching the full classifier-relevant
// fields (embedding, instructions). Lives on the orchestrate side
// because skills are stored in the per-user udb and Documents is a
// thin shell over this app's APIs.
func (T *OrchestrateApp) handleSkillsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	type out struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
		Disabled    bool   `json:"disabled,omitempty"`
	}
	var list []out
	for _, sk := range LoadSkills(udb, user) {
		list = append(list, out{
			ID:          sk.ID,
			Name:        sk.Name,
			Description: sk.Description,
			Disabled:    sk.Disabled,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"skills": list})
}

// requireUDB is a small helper that re-runs RequireUser to get udb
// after the route has already extracted user. Used by handlers that
// receive a Collection (loaded via udb) and need to re-touch udb for
// related updates. Returns (nil, false) when the session is gone.
func requireUDB(w http.ResponseWriter, r *http.Request, db Database) (Database, bool) {
	_, udb, ok := RequireUser(w, r, db)
	return udb, ok
}

// strconvAtoi is a tiny strconv.Atoi-equivalent to avoid the import.
func strconvAtoi(s string) (int, error) {
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a number")
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// --- Autofill ---------------------------------------------------------------

const (
	autofillMaxDocs    = 50
	autofillMaxQueries = 20               // hard ceiling; effective count scales with MaxDocs
	autofillFetchLimit = 50 * 1024 * 1024 // 50 MB per file
	autofillTimeout    = 10 * time.Minute
	autofillPerFetch   = 30 * time.Second
)

// queriesForMaxDocs picks how many search queries to run for a
// requested ingest count. Roughly one query per ~4 desired docs
// (web_search returns ~8 URLs per query; ~50% survive dedup +
// source-quality filtering), floored at 4 and ceilinged by
// autofillMaxQueries. Without scaling, a 50-doc request would
// stall at ~15 candidates from the fixed 5 queries.
func queriesForMaxDocs(maxDocs int) int {
	n := maxDocs / 4
	if n < 4 {
		n = 4
	}
	if n > autofillMaxQueries {
		n = autofillMaxQueries
	}
	return n
}

// handleCollectionAutofill seeds the collection from the web. Generates
// a handful of search queries via worker LLM from the collection's
// name + description (override with explicit queries in the body),
// runs each against the registered web_search tool, fetches the top
// candidates, extracts text, ingests into the collection. Skips URLs
// already in c.IngestedURLs so repeated clicks don't duplicate.
//
// Note: this is the broad-capture pipeline — quality is shallow
// (filters weak URL patterns but doesn't evaluate content). A
// vetting/cleanup pass will be a separate feature later. For now,
// trust the URL filter and rely on per-doc Remove for cruft.
//
//	POST /api/collections/{id}/autofill
//	body (all optional):
//	  {queries: []string, max_docs: int}
//	→ {added, skipped, failed, queries_used, results}
func (T *OrchestrateApp) handleCollectionAutofill(w http.ResponseWriter, r *http.Request, c Collection) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	_ = user
	var body struct {
		Queries []string `json:"queries"`
		MaxDocs int      `json:"max_docs"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.MaxDocs <= 0 || body.MaxDocs > autofillMaxDocs {
		body.MaxDocs = autofillMaxDocs
	}

	if !WebSearchAvailable() {
		http.Error(w, "web search is not configured for this deployment", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), autofillTimeout)
	defer cancel()

	// Step 1: queries. Use what the caller provided; otherwise ask the
	// worker LLM to invent a batch from the collection name + description.
	// Query count scales with MaxDocs — small ingest = a handful of queries,
	// large ingest = up to autofillMaxQueries — so a 50-doc request actually
	// has enough candidate URLs to fill from.
	wanted := queriesForMaxDocs(body.MaxDocs)
	queries := normalizeQueries(body.Queries)
	if len(queries) == 0 {
		queries = generateAutofillQueries(ctx, T, c, wanted)
	}
	if len(queries) == 0 {
		http.Error(w, "could not derive search queries — try filling in the collection description, or pass queries explicitly", http.StatusBadRequest)
		return
	}
	if len(queries) > wanted {
		queries = queries[:wanted]
	}

	// Step 2: search + collect candidate URLs.
	// Dedup key is the *normalized* URL (see normalizeIngestURL) so
	// minor variants — trailing slash, http vs https, utm_*/fbclid
	// tracking params, fragments — all collapse to the same entry.
	// Verbatim string compare alone was producing duplicate ingests
	// when the same page came back through different referrers.
	alreadyIngested := map[string]bool{}
	for _, u := range c.IngestedURLs {
		alreadyIngested[normalizeIngestURL(u)] = true
	}
	type candidate struct {
		URL   string
		Title string
	}
	var candidates []candidate
	seen := map[string]bool{}
	for _, q := range queries {
		raw := WebSearch(q)
		if raw == "" {
			continue
		}
		// Parse the search results properly: blank-line-separated
		// blocks of "N. Title\n   URL\n   Snippet". Pair title with
		// URL so the ingested document has a meaningful name instead
		// of falling back to nameFromURL (which often degrades to
		// "document" for path-less URLs).
		blocks := strings.Split(raw, "\n\n")
		for _, block := range blocks {
			lines := strings.Split(strings.TrimSpace(block), "\n")
			if len(lines) < 2 {
				continue
			}
			title := strings.TrimSpace(lines[0])
			// Strip the "N. " numbering prefix websearch uses.
			if dot := strings.Index(title, ". "); dot >= 0 && dot < 4 {
				title = strings.TrimSpace(title[dot+2:])
			}
			var u string
			for _, ln := range lines[1:] {
				ln = strings.TrimSpace(ln)
				if strings.HasPrefix(ln, "http://") || strings.HasPrefix(ln, "https://") {
					u = ln
					break
				}
			}
			if u == "" {
				continue
			}
			nu := normalizeIngestURL(u)
			if alreadyIngested[nu] || seen[nu] {
				continue
			}
			if !looksLikeUsefulSourceURL(u) {
				continue
			}
			// Apply the research/debate source-quality filter — drops
			// vendor blogs, advocacy outlets, press-release wires,
			// navigation pages, and the rest of the curated blocklist
			// in core/filters.go. Keeps collection corpora aligned
			// with what the Research agent considers citation-worthy.
			if IsWeakSource(u) {
				continue
			}
			seen[nu] = true
			candidates = append(candidates, candidate{URL: u, Title: title})
		}
	}
	// Prefer PDF/docx URLs first — they extract cleanly.
	sort.SliceStable(candidates, func(i, j int) bool {
		return urlExtractScore(candidates[i].URL) > urlExtractScore(candidates[j].URL)
	})
	if len(candidates) > body.MaxDocs*2 {
		candidates = candidates[:body.MaxDocs*2] // overscan for failures
	}

	// Step 3: fetch + extract + ingest.
	added := 0
	skipped := len(alreadyIngested)
	failed := 0
	type summaryItem struct {
		URL    string `json:"url"`
		Status string `json:"status"`
		Name   string `json:"name,omitempty"`
		Chunks int    `json:"chunks,omitempty"`
		Err    string `json:"err,omitempty"`
	}
	results := make([]summaryItem, 0, len(candidates))
	for _, cand := range candidates {
		if added >= body.MaxDocs {
			break
		}
		// Static fetch + extract, with a headless-browser fallback for
		// JS-only / soft-blocked pages.
		name, text, raw, mime, gerr := fetchAndExtractForIngest(ctx, cand.URL)
		// Prefer the search-result title — it's human-curated and
		// way more readable than nameFromURL's basename fallback.
		if t := strings.TrimSpace(cand.Title); t != "" {
			name = t
		}
		if gerr != nil {
			failed++
			results = append(results, summaryItem{URL: cand.URL, Status: "unusable", Name: name, Err: gerr.Error()})
			continue
		}
		// Reject extracted blobs above a reasonable doc-text cap.
		// Cap is on EXTRACTED text (post-parse) — HTML pages get
		// stripped of markup first, so what we measure here is the
		// actual readable content, not raw bytes. 200 KB of plain
		// text is enough for the biggest legitimate single-doc
		// reference material (long RFCs, dense API specs); anything
		// bigger is almost certainly a junk page with infinite
		// scroll, mailing-list digest, or content farm.
		const maxAutofillDocChars = 200 * 1024
		if len(text) > maxAutofillDocChars {
			failed++
			results = append(results, summaryItem{URL: cand.URL, Status: "too_large", Name: name, Err: fmt.Sprintf("%d chars > %d cap (likely junk page or aggregator)", len(text), maxAutofillDocChars)})
			Log("[orchestrate.autofill] dropping %q: %d chars > %d cap", cand.URL, len(text), maxAutofillDocChars)
			continue
		}
		// LLM judge pass — opt-in per collection. Reads the
		// collection's description + FilterRules + the candidate
		// text and decides keep / drop, optionally returning a
		// cleaned version with residual noise stripped. Cheap
		// (non-thinking worker, one call per doc) and catches
		// off-topic or low-signal pages that pass the heuristic
		// HTML filter but don't fit the collection's purpose.
		if c.ClassifyOnAutofill && T.LLM != nil {
			verdict, jerr := judgeAutofillCandidate(ctx, T, c, cand.URL, name, text)
			if jerr != nil {
				// Judge failure shouldn't block the pipeline —
				// fall through and ingest. Log for visibility.
				Log("[orchestrate.autofill] judge failed for %q: %v — ingesting without classification", cand.URL, jerr)
			} else if !verdict.Keep {
				failed++
				reason := strings.TrimSpace(verdict.Reason)
				if reason == "" {
					reason = "(judge declined to keep this doc)"
				}
				results = append(results, summaryItem{URL: cand.URL, Status: "judge_dropped", Name: name, Err: reason})
				Log("[orchestrate.autofill] judge DROP %q: %s", cand.URL, reason)
				continue
			} else if cleaned := strings.TrimSpace(verdict.CleanedText); cleaned != "" && len(cleaned) >= 200 {
				// Judge returned a cleaned-up version — use that
				// instead of the raw extract. Common when the
				// page has residual noise the heuristic filter
				// missed (signup boxes, navigation cruft, "in
				// this article" preambles).
				text = cleaned
			}
		}
		baseReportID := fmt.Sprintf("autofill-%s-%d", c.ID, time.Now().UnixNano())
		mainDoc := "## " + name + "\n\n" + text
		chunkDB := T.collectionDB(c)
		IngestReport(ctx, chunkDB, collectionSource(c.ID), baseReportID, mainDoc)
		chunks := countReportChunks(chunkDB, baseReportID)
		// Also ingest any kind-tagged regions (comments,
		// related-link rails, author bios) under a sibling
		// reportID per kind so the chunker boundaries stay
		// clean. Chunks carry Kind so consumers can frame
		// hits appropriately at retrieval time.
		if len(raw) > 0 && (strings.HasPrefix(strings.ToLower(mime), "text/html") || strings.HasSuffix(strings.ToLower(name), ".html") || strings.HasSuffix(strings.ToLower(name), ".htm")) {
			if buckets, _ := ExtractHTMLByKind(raw); len(buckets) > 0 {
				for kind, kindText := range buckets {
					if kind == "" || len(kindText) < 100 {
						continue // default bucket already ingested via ExtractDocument; skip tiny tagged regions
					}
					kindReportID := baseReportID + "-" + kind
					kindDoc := "## " + name + " (" + kind + ")\n\n" + kindText
					IngestReportTagged(ctx, chunkDB, collectionSource(c.ID), kindReportID, kindDoc, kind)
					chunks += countReportChunks(chunkDB, kindReportID)
				}
			}
		}
		results = append(results, summaryItem{URL: cand.URL, Status: "added", Name: name, Chunks: chunks})
		c.IngestedURLs = append(c.IngestedURLs, cand.URL)
		added++
		// Persist after EVERY ingest, not just at the end. A mid-run
		// timeout / panic / context-cancel previously left chunks in
		// the DB but the URL never recorded in c.IngestedURLs, so the
		// next autofill re-ingested everything as duplicates. Cheap
		// to save — the collection record is small.
		saveCollection(udb, c)
	}

	Log("[orchestrate.autofill] collection=%s queries=%d added=%d failed=%d (max=%d)",
		c.ID, len(queries), added, failed, body.MaxDocs)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"added":        added,
		"skipped":      skipped,
		"failed":       failed,
		"queries_used": queries,
		"results":      results,
	})
}

// handleCollectionDraftDescription drafts a description BEFORE the
// collection exists — used by the "+ New collection" modal so the
// user can preview an AI-drafted description while they're still
// filling out the create form. Same prompting as the post-create
// suggest, just without a Collection record (uses provided name +
// skill_ids directly).
//
//	POST /api/collections/draft-description
//	body: {name, description?, skill_ids?: []string}
//	→ {description: "..."}
func (T *OrchestrateApp) handleCollectionDraftDescription(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var body struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		SkillIDs    []string `json:"skill_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}

	sys := `You write descriptions for RAG document collections. A description has three jobs:
1. Tell future-you (or another admin) what this collection is FOR.
2. Steer the Auto-fill feature's web-search queries — the more specific the description, the better the queries.
3. Stay short — 1-3 sentences, ~200 chars max.

Output ONLY the description text. No headings, no commentary, no quotes.

Bias toward CONCRETE nouns and named sources: "Official Kubernetes API reference, kubectl command guide, and CNCF operator best practices" reads way better than "Things about kubernetes." When in doubt, list specific types of source material the collection should contain.`

	// Append the selected skills' instructions as a domain primer.
	var expertise []string
	if len(body.SkillIDs) > 0 {
		want := map[string]bool{}
		for _, sid := range body.SkillIDs {
			want[sid] = true
		}
		for _, sk := range LoadSkills(udb, user) {
			if !want[sk.ID] {
				continue
			}
			if instr := strings.TrimSpace(sk.Instructions); instr != "" {
				expertise = append(expertise, "### "+sk.Name+"\n"+instr)
			}
		}
	}
	if len(expertise) > 0 {
		sys += "\n\n--- Domain expertise (apply when drafting) ---\n" + strings.Join(expertise, "\n\n")
	}

	prompt := "Collection name: " + name
	if cur := strings.TrimSpace(body.Description); cur != "" {
		prompt += "\nCurrent description (revise / improve): " + cur
	}
	prompt += "\n\nDescription:"

	// No artificial timeout — the worker LLM's own client timeout
	// + the HTTP request context (browser-navigate cancel) govern.
	// The previous 30s cap fired before thinking-mode workers
	// could finish even simple suggestions on a cold prompt cache.
	resp, err := T.WorkerChat(r.Context(),
		[]Message{{Role: "user", Content: prompt}},
		WithSystemPrompt(sys), WithMaxTokens(200),
	)
	if err != nil || resp == nil {
		http.Error(w, "worker LLM unavailable: "+errStr(err), http.StatusServiceUnavailable)
		return
	}
	out := strings.TrimSpace(resp.Content)
	out = strings.Trim(out, "\"'")
	if out == "" {
		http.Error(w, "model returned empty description", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"description": out})
}

// handleCollectionSuggestDescription drafts a description for the
// collection using the worker LLM. Optionally takes a skill_id —
// when provided, the skill's instructions get prepended to the system
// prompt so the draft reads like that domain's expert wrote it
// (e.g. a "Kubernetes Helper" skill produces a k8s-savvy description,
// not a generic one).
//
//	POST /api/collections/{id}/suggest-description
//	body (optional): {skill_id: "..."}
//	→ {description: "..."}
func (T *OrchestrateApp) handleCollectionSuggestDescription(w http.ResponseWriter, r *http.Request, udb Database, user string, c Collection) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		// Mode picks the expertise source:
		//   ""         → default (treat as "attached")
		//   "attached" → union every skill currently attached to this collection
		//   "none"     → vanilla worker LLM, no expertise primer
		//   "skill"    → single named skill, identified by SkillID
		Mode    string `json:"mode"`
		SkillID string `json:"skill_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	body.SkillID = strings.TrimSpace(body.SkillID)
	if body.Mode == "" {
		body.Mode = "attached"
	}

	// Sample existing doc titles (if any) so the suggestion is
	// informed by what's already in the collection. Cap at a handful
	// so the prompt stays terse.
	var titles []string
	chunkDB := T.collectionDB(c)
	for _, key := range chunkDB.Keys(EmbeddedChunks) {
		var ch EmbeddedChunk
		if !chunkDB.Get(EmbeddedChunks, key, &ch) {
			continue
		}
		if !strings.HasPrefix(ch.Source, collectionSource(c.ID)) {
			continue
		}
		t := strings.TrimSpace(strings.TrimPrefix(ch.Section, "## "))
		if t == "" {
			continue
		}
		seen := false
		for _, existing := range titles {
			if existing == t {
				seen = true
				break
			}
		}
		if !seen {
			titles = append(titles, t)
			if len(titles) >= 8 {
				break
			}
		}
	}

	// Base system prompt.
	sys := `You write descriptions for RAG document collections. A description has three jobs:
1. Tell future-you (or another admin) what this collection is FOR.
2. Steer the Auto-fill feature's web-search queries — the more specific the description, the better the queries.
3. Stay short — 1-3 sentences, ~200 chars max.

Output ONLY the description text. No headings, no commentary, no quotes.

Bias toward CONCRETE nouns and named sources: "Official Kubernetes API reference, kubectl command guide, and CNCF operator best practices" reads way better than "Things about kubernetes." When in doubt, list specific types of source material the collection should contain.`

	// Optional skill-lens enhancement: when mode="skill" + skill_id,
	// append that skill's instructions to the system prompt so the
	// model drafts with that skill's voice. The old "attached" mode
	// (union all skills attached to this collection) is gone because
	// skills no longer attach to collections.
	if body.Mode == "skill" && body.SkillID != "" {
		for _, sk := range LoadSkills(udb, user) {
			if sk.ID == body.SkillID {
				if instr := strings.TrimSpace(sk.Instructions); instr != "" {
					sys += "\n\n--- Domain expertise (apply when drafting) ---\n### " + sk.Name + "\n" + instr
				}
				break
			}
		}
	}

	prompt := "Collection name: " + c.Name
	if cur := strings.TrimSpace(c.Description); cur != "" {
		prompt += "\nCurrent description (revise / improve): " + cur
	}
	if len(titles) > 0 {
		prompt += "\nDocuments already in this collection:\n- " + strings.Join(titles, "\n- ")
	}
	prompt += "\n\nDescription:"

	// No artificial timeout — the worker LLM's own client timeout
	// + the HTTP request context (browser-navigate cancel) govern.
	// The previous 30s cap fired before thinking-mode workers
	// could finish even simple suggestions on a cold prompt cache.
	resp, err := T.WorkerChat(r.Context(),
		[]Message{{Role: "user", Content: prompt}},
		WithSystemPrompt(sys), WithMaxTokens(200),
	)
	if err != nil || resp == nil {
		http.Error(w, "worker LLM unavailable: "+errStr(err), http.StatusServiceUnavailable)
		return
	}
	out := strings.TrimSpace(resp.Content)
	// Strip leading/trailing quote marks that some models add despite
	// the prompt's "no quotes" instruction.
	out = strings.Trim(out, "\"'")
	if out == "" {
		http.Error(w, "model returned empty description", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"description": out})
}

func errStr(err error) string {
	if err == nil {
		return "(nil)"
	}
	return err.Error()
}

// generateAutofillQueries asks the worker LLM for 3-5 diverse search
// queries based on the collection's name + description. Returns empty
// when the LLM is unreachable, the description is too thin, or the
// model produces garbage.
// autofillJudgeVerdict is what judgeAutofillCandidate returns
// after the LLM classification pass. Keep gates ingest; Reason
// surfaces in the autofill summary so the user sees WHY a page
// got dropped (off-topic, marketing, etc.); CleanedText, when
// non-empty, replaces the raw extract before ingest (lets the
// judge strip residual noise the heuristic filter missed).
type autofillJudgeVerdict struct {
	Keep        bool   `json:"keep"`
	Reason      string `json:"reason"`
	CleanedText string `json:"cleaned_text"`
}

// judgeAutofillCandidate runs a single non-thinking worker LLM
// call to classify an autofilled candidate against the collection's
// purpose + FilterRules. Cheap by design — one call per doc, JSON
// output, ~45s timeout. Returns the verdict or an error (caller
// should fall through to ingest on error rather than block).
func judgeAutofillCandidate(ctx context.Context, app *OrchestrateApp, c Collection, candURL, candName, candText string) (*autofillJudgeVerdict, error) {
	if app.LLM == nil {
		return nil, fmt.Errorf("worker LLM not configured")
	}
	// Cap candidate text in the prompt — the judge doesn't need
	// the whole doc to decide keep/drop; first 4KB carries the
	// signal (title, intro, first sections). For cleaned_text the
	// judge can return up to ~maxAutofillDocChars worth — we don't
	// cap the output beyond the LLM's own context limits.
	sample := candText
	if len(sample) > 4000 {
		sample = sample[:4000] + "\n...[truncated for judge prompt; full text would be ingested if kept]"
	}
	rulesBlock := strings.TrimSpace(c.FilterRules)
	if rulesBlock == "" {
		rulesBlock = "(none — judge purely against the collection's description)"
	}
	desc := strings.TrimSpace(c.Description)
	if desc == "" {
		desc = "(no description — judge against the collection name only)"
	}
	sys := `You are a relevance / quality judge for a knowledge collection. Decide whether a fetched web page is worth ingesting based on the collection's purpose + filter rules.

Output ONLY a JSON object: {"keep": bool, "reason": "<short one-line>", "cleaned_text": "<optional, see below>"}

Decision criteria:
- KEEP when the page clearly fits the collection's purpose AND meets the filter rules
- DROP when off-topic, low-signal (marketing, listicles, content farms), or violates an explicit filter rule
- Reason: one line, specific. "Vendor blog post — filter rules exclude blogs" beats "doesn't fit."

cleaned_text (optional): when the page is worth keeping BUT has noise (signup boxes, navigation cruft, "in this article" preambles, footer disclaimers) that the heuristic HTML filter missed, return a CLEANED version with just the meaty content. Leave EMPTY when the raw text is already clean. Don't paraphrase or summarize — pass through the original text minus the noise.`
	prompt := fmt.Sprintf(
		"## Collection\nName: %s\nPurpose: %s\n\n## Filter rules\n%s\n\n## Candidate\nURL: %s\nTitle: %s\n\nText (sampled):\n%s\n\n## Your verdict (JSON only)",
		c.Name, desc, rulesBlock, candURL, candName, sample,
	)
	jctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	noThink := false
	resp, err := app.LLM.Chat(jctx, []Message{{Role: "user", Content: prompt}},
		WithSystemPrompt(sys),
		WithJSONMode(),
		WithRouteKey("app.orchestrate.autofill.judge"),
		WithThink(noThink),
	)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("empty response")
	}
	raw := strings.TrimSpace(resp.Content)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	var v autofillJudgeVerdict
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, fmt.Errorf("parse: %v (payload=%.200s)", err, raw)
	}
	return &v, nil
}

func generateAutofillQueries(ctx context.Context, app *OrchestrateApp, c Collection, want int) []string {
	desc := strings.TrimSpace(c.Description)
	name := strings.TrimSpace(c.Name)
	if desc == "" && name == "" {
		return nil
	}
	if want < 3 {
		want = 3
	}
	year := time.Now().Year()
	sys := fmt.Sprintf(`You generate web-search queries to seed a document collection with reference material.

The current year is %d. Many topics have annual/yearly editions (rulebooks,
regulations, tax forms, style guides, standards). When the collection name
or description suggests a versioned/annual publication, EXPLICITLY include
"%d" (or "%d edition", "latest") in queries — otherwise search engines tend
to surface older PDFs that have accumulated more inbound links.

Output: %d queries, one per line, no numbering or bullets, no quotes. Each query should be:
- terse (2-7 words)
- diverse (different angles on the topic, not synonyms of each other)
- biased toward the PRIMARY / AUTHORITATIVE source, NOT secondary commentary. For laws & statutes, target the official code TEXT — "California Penal Code 459.5 full text", "site:leginfo.legislature.ca.gov penal code theft" — not law-firm blogs that paraphrase it. Same shape for regulations (the agency's own text), technical standards (the standards body), official forms, and product docs ("kubernetes official documentation", not "kubernetes basics"). The skill that uses this corpus will CITE from it, so a paraphrase is worse than the source itself.
- biased toward fetchable PDF/HTML docs (e.g. "rfc 9110 pdf", "kubectl cheat sheet pdf")
- year-tagged when the topic is an annual/versioned publication

If the user provided filter rules below, treat them as hard constraints —
their "keep" notes should bias query phrasing, their "skip" notes should
make you avoid query shapes that would surface that content.

Skip news articles, opinions, tutorials, and explainer/commentary sites that merely SUMMARIZE the source (a law-firm article ABOUT a statute is not the statute). Aim for the canonical primary reference itself.`, year, year, year, want)
	rulesBlock := strings.TrimSpace(c.FilterRules)
	if rulesBlock == "" {
		rulesBlock = "(none)"
	}
	prompt := "Collection name: " + name + "\nCollection description: " + desc + "\nFilter rules:\n" + rulesBlock + "\n\nQueries:"
	// Sub-timeout scales with the number of queries requested — the
	// worker has to think through diversity + year-tagging + filter
	// rules for each one. The old 30s cap killed generation early
	// for any wanted > ~6. Use the parent ctx's deadline as the
	// hard ceiling (autofillTimeout, currently 10m) and fall back
	// to a generous per-query budget otherwise.
	perQuery := 8 * time.Second
	budget := 30*time.Second + time.Duration(want)*perQuery
	if budget > autofillTimeout/2 {
		budget = autofillTimeout / 2
	}
	cctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	maxTok := 60 + want*40
	resp, err := app.WorkerChat(cctx,
		[]Message{{Role: "user", Content: prompt}},
		WithSystemPrompt(sys), WithMaxTokens(maxTok),
	)
	if err != nil || resp == nil {
		Debug("[autofill] query generation failed: %v", err)
		return nil
	}
	return normalizeQueries(strings.Split(resp.Content, "\n"))
}

func normalizeQueries(raw []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(raw))
	for _, q := range raw {
		q = strings.TrimSpace(q)
		// Strip common LLM list-markers
		q = strings.TrimLeft(q, "-*0123456789.) ")
		q = strings.Trim(q, "\"'")
		if q == "" || len(q) < 3 {
			continue
		}
		k := strings.ToLower(q)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, q)
	}
	return out
}

// looksLikeUsefulSourceURL filters out junk before we spend an HTTP
// fetch on it. The websearch package has a fuller isLowValueURL but
// it's not exported — re-implement the must-haves inline.
func looksLikeUsefulSourceURL(u string) bool {
	lower := strings.ToLower(u)
	// Block aggregator / SERP / search-result-page URLs.
	for _, bad := range []string{
		"google.com/search", "duckduckgo.com/", "bing.com/search",
		"reddit.com/r/", "twitter.com/", "x.com/", "facebook.com/",
		"linkedin.com/", "youtube.com/watch",
		"/login", "/signup", "/subscribe", "/contact",
	} {
		if strings.Contains(lower, bad) {
			return false
		}
	}
	return true
}

// normalizeIngestURL collapses minor URL variants so the same page
// fetched via different referrer chains dedups cleanly. NOT a full
// canonicalizer — just the cheap, safe normalizations:
//   - lowercase scheme + host
//   - strip fragment (#...)
//   - strip trailing slash from path (but keep "/" itself)
//   - drop common tracking query params (utm_*, fbclid, gclid, mc_*,
//     _ga, ref, ref_src) that don't change the resource served
//
// Used as the dedup key in c.IngestedURLs; the original URL is what
// we actually fetch + store as the document source.
func normalizeIngestURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return raw
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	if len(u.Path) > 1 {
		u.Path = strings.TrimRight(u.Path, "/")
	}
	if u.RawQuery != "" {
		q := u.Query()
		for k := range q {
			lk := strings.ToLower(k)
			if strings.HasPrefix(lk, "utm_") ||
				strings.HasPrefix(lk, "mc_") ||
				lk == "fbclid" || lk == "gclid" || lk == "yclid" ||
				lk == "_ga" || lk == "_gl" ||
				lk == "ref" || lk == "ref_src" || lk == "ref_url" {
				q.Del(k)
			}
		}
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// urlExtractScore biases candidate ordering toward URLs that are more
// likely to extract cleanly. PDFs > docx > known-doc-domains > others.
func urlExtractScore(u string) int {
	lower := strings.ToLower(u)
	score := 0
	if strings.HasSuffix(lower, ".pdf") || strings.Contains(lower, ".pdf?") {
		score += 100
	}
	if strings.HasSuffix(lower, ".docx") || strings.HasSuffix(lower, ".doc") {
		score += 80
	}
	for _, doc := range []string{
		"/docs/", "/documentation/", "/reference/", "/manual/",
		"/api-docs/", "/api/docs/", "/guides/", "/spec/",
	} {
		if strings.Contains(lower, doc) {
			score += 30
			break
		}
	}
	for _, dom := range []string{
		"datatracker.ietf.org", "rfc-editor.org",
		"kubernetes.io", "docs.aws.amazon.com",
		"learn.microsoft.com", "developer.mozilla.org",
		"docs.python.org", "go.dev/doc",
	} {
		if strings.Contains(lower, dom) {
			score += 20
			break
		}
	}
	// Authority TLDs — government and academic sources tend to be
	// primary / peer-reviewed. Boost so a .gov or .edu URL outranks
	// a generic .com when both look like documentation.
	if strings.Contains(lower, ".gov/") || strings.HasSuffix(lower, ".gov") {
		score += 25
	}
	if strings.Contains(lower, ".edu/") || strings.HasSuffix(lower, ".edu") {
		score += 20
	}
	if strings.Contains(lower, ".mil/") || strings.HasSuffix(lower, ".mil") {
		score += 15
	}
	// "Official" subdomain conventions for vendor product docs.
	if strings.HasPrefix(lower, "https://docs.") || strings.HasPrefix(lower, "http://docs.") {
		score += 15
	}
	if strings.HasPrefix(lower, "https://developer.") || strings.HasPrefix(lower, "http://developer.") {
		score += 10
	}
	if strings.HasPrefix(lower, "https://help.") || strings.HasPrefix(lower, "http://help.") {
		score += 8
	}
	// Standards / specification organizations.
	for _, std := range []string{
		"w3.org", "iso.org", "ieee.org", "nist.gov",
		"iana.org", "icann.org", "unicode.org",
		"khronos.org", "ecma-international.org",
	} {
		if strings.Contains(lower, std) {
			score += 15
			break
		}
	}
	// Year-in-URL recency: when a URL embeds a year, prefer the
	// current year over older ones. Versioned/annual publications
	// (rulebooks, regulations, tax forms) usually include the year
	// in the filename or path — without this, older years often
	// outrank the current edition simply because they've had more
	// time to accumulate inbound links.
	currentYear := time.Now().Year()
	for offset := 0; offset <= 6; offset++ {
		year := currentYear - offset
		token := strconv.Itoa(year)
		if !strings.Contains(lower, token) {
			continue
		}
		// Linear decay: current year +40, last year +30, then -10/year.
		bonus := 40 - offset*10
		if bonus < 0 {
			bonus = 0
		}
		score += bonus
		break
	}
	return score
}

// nameFromURL derives a filename for the document name we present to
// the LLM at extraction time. Last path segment when usable, falls
// back to the hostname.
func nameFromURL(u, mime string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return "document"
	}
	base := strings.TrimSuffix(parsed.Path, "/")
	if idx := strings.LastIndexByte(base, '/'); idx >= 0 {
		base = base[idx+1:]
	}
	if base == "" {
		base = parsed.Hostname()
	}
	// If the URL extension doesn't match the actual mime, swap in a
	// sensible one so ExtractDocument routes correctly.
	if mime != "" && !strings.Contains(base, ".") {
		switch {
		case strings.Contains(mime, "pdf"):
			base += ".pdf"
		case strings.Contains(mime, "wordprocessingml"):
			base += ".docx"
		case strings.Contains(mime, "text/"):
			base += ".txt"
		case strings.Contains(mime, "html"):
			base += ".html"
		}
	}
	if base == "" {
		base = "document"
	}
	return base
}

// fetchAutofillURL pulls one candidate URL, returning bytes + mime.
// Applies the same SSRF guard the rest of the network tools use:
// refuse private / loopback / linklocal hosts.
func fetchAutofillURL(ctx context.Context, u string) ([]byte, string, error) {
	parsed, err := url.Parse(u)
	if err != nil {
		return nil, "", fmt.Errorf("bad url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, "", fmt.Errorf("unsupported scheme %q", parsed.Scheme)
	}
	host := parsed.Hostname()
	if host == "" {
		return nil, "", fmt.Errorf("missing host")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			return nil, "", fmt.Errorf("refusing non-public host: %s", host)
		}
	}
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".local") || strings.HasSuffix(lower, ".internal") {
		return nil, "", fmt.Errorf("refusing non-public host: %s", host)
	}
	fctx, cancel := context.WithTimeout(ctx, autofillPerFetch)
	defer cancel()
	req, err := http.NewRequestWithContext(fctx, "GET", u, nil)
	if err != nil {
		return nil, "", err
	}
	// Polite UA so servers don't 403 us as a default "Go-http-client".
	req.Header.Set("User-Agent", "gohort-autofill/1.0 (+https://github.com/cmcoffee/gohort)")
	// Bounded client (shared with fetch_url) — ties connect + time-to-
	// first-byte to the configured Network Timeouts so a dead URL fails
	// fast instead of stalling autofill for the full autofillPerFetch
	// window. The 30s fctx above stays the overall body cap.
	resp, err := NewBoundedHTTPClient().Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	limited := io.LimitReader(resp.Body, autofillFetchLimit+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, "", err
	}
	if int64(len(raw)) > autofillFetchLimit {
		return nil, "", fmt.Errorf("file exceeds %d-byte cap", autofillFetchLimit)
	}
	return raw, resp.Header.Get("Content-Type"), nil
}

// ingestMinChars is the floor below which a static fetch is treated as
// "didn't really get the page" — JS-only skeletons, soft blocks, and
// interstitials all extract to near-nothing. Below this we retry through
// the headless browser.
const ingestMinChars = 200

// ingestBrowserMaxChars bounds how much rendered text the browser
// fallback returns. Generous (full statute / spec pages run long); the
// caller's own doc-size cap still applies downstream.
const ingestBrowserMaxChars = 1 * 1024 * 1024

// fetchAndExtractForIngest pulls a URL and returns a display name plus
// extracted text ready for ingestion. It tries the cheap static HTTP
// path first; when that extracts too little (JS-only page / soft block /
// empty body) it falls back to the headless browser (BrowserFetchFunc),
// which runs JS and ships normal browser headers — the same recovery
// path fetch_url uses. Returns an error only when BOTH paths fail to
// produce usable text.
//
// raw/mime carry the original static-fetch bytes for callers that want to
// post-process the HTML (e.g. kind-tagged region extraction); they are
// nil/"" when the browser fallback supplied the text (rendered text, not
// source HTML).
func fetchAndExtractForIngest(ctx context.Context, u string) (name, text string, raw []byte, mime string, err error) {
	raw, mime, ferr := fetchAutofillURL(ctx, u)
	if ferr == nil {
		name = nameFromURL(u, mime)
		if t, eerr := ExtractDocument(ctx, DocumentAttachment{Name: name, MimeType: mime, Data: raw}); eerr == nil {
			text = strings.TrimSpace(t)
		}
	}
	if len(text) >= ingestMinChars {
		return name, text, raw, mime, nil
	}
	// Static path got a JS-only skeleton / soft block / nothing. Retry
	// through the headless browser; keep whichever extraction is richer.
	if BrowserFetchFunc != nil {
		if rendered, berr := BrowserFetchFunc(u, ingestBrowserMaxChars); berr == nil {
			if rendered = strings.TrimSpace(rendered); len(rendered) > len(text) {
				if name == "" {
					name = nameFromURL(u, "text/html")
				}
				// Rendered text, not source HTML — drop raw/mime so
				// callers don't try to kind-extract plain text.
				return name, rendered, nil, "", nil
			}
		} else {
			Debug("[orchestrate.collections] browser fallback failed for %s: %v", u, berr)
		}
	}
	if len(text) >= ingestMinChars {
		return name, text, raw, mime, nil
	}
	if ferr != nil {
		return "", "", nil, "", fmt.Errorf("fetch failed for %s: %w", u, ferr)
	}
	return "", "", nil, "", fmt.Errorf("extracted only %d chars from %s — JS-only or blocked even via headless browser; try a direct text/PDF URL", len(text), u)
}
