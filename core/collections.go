// Document Collections — named, reusable corpora that any app can
// attach to its agents/skills/workers for RAG-backed retrieval.
//
// The data layer lives in core/ so apps don't have to import
// orchestrate just to reference a collection. HTTP handlers, the
// admin UI, autofill, and the suggest-description flow stay in
// apps/orchestrate/ — those are use-site concerns. The struct,
// storage, and lookup helpers are framework primitives.
//
// Scope:
//   - user-scoped (default): owned by one user, stored under the
//     user's per-user DB. Only that user's agents can attach.
//   - deployment-scoped: shared, stored under RootDB. Any user's
//     agents can attach. Authoring restriction lives at the
//     HTTP layer (admins-only POST/PATCH).
//
// Chunks always live in the shared EmbeddedChunks table, keyed by
// CollectionSource(id) — retrieval is identical regardless of
// scope; only the metadata routing differs.

package core

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// CollectionsTable is the per-user metadata table for user-scoped
// Document Collections. Lives in the user's per-user Database
// (returned by UserDB on the shared bucket).
const CollectionsTable = "orchestrate_collections"

// GlobalCollectionsTable holds deployment-scoped collections in
// RootDB. Any user's agent can attach a collection from this
// table; the chunks still live in the shared EmbeddedChunks store
// (keyed by source = "collection:<id>"), so retrieval is identical
// to a user-scoped collection. Only the metadata bucket differs.
const GlobalCollectionsTable = "orchestrate_collections_global"

// Collection scope constants. Empty Scope on a stored record is
// treated as user-scoped for back-compat with records written
// before the field existed.
const (
	CollectionScopeUser       = "user"
	CollectionScopeDeployment = "deployment"
)

// DeploymentKnowledgeCollectionID is the well-known ID for the
// auto-minted deployment-wide Collection that research, debate, and
// answer pipelines ingest into. Fixed ID (not generated) so any app
// can reference it without a lookup. Chunks for this collection
// live in the dedicated VectorDB (alongside every other shared chunk),
// partitioned by the collectionSource(ID) tag. Agents with empty
// AttachedCollections auto-include deployment collections in their
// knowledge_search scope; agents with curated AttachedCollections
// stay self-contained.
const DeploymentKnowledgeCollectionID = "deployment-knowledge"

// EnsureDeploymentKnowledgeCollection auto-mints the well-known
// deployment-wide knowledge collection if it doesn't already exist.
// Safe to call repeatedly — short-circuits when the record is
// already present. No-op when RootDB is unset (early-init paths;
// caller must run this after the database is open).
func EnsureDeploymentKnowledgeCollection() {
	if RootDB == nil {
		return
	}
	var existing Collection
	if RootDB.Get(GlobalCollectionsTable, DeploymentKnowledgeCollectionID, &existing) {
		return
	}
	now := time.Now()
	c := Collection{
		ID:          DeploymentKnowledgeCollectionID,
		Owner:       "", // deployment-scoped; no individual owner
		Name:        "Deployment Knowledge",
		Description: "Auto-populated cross-cutting knowledge base — every research report, debate verdict, and answered question this deployment has produced. Searched via knowledge_search on any agent (auto-attached when the agent has no curated collections of its own).",
		Scope:       CollectionScopeDeployment,
		Created:     now,
		Updated:     now,
	}
	RootDB.Set(GlobalCollectionsTable, c.ID, c)
}

// Collection is a named bucket of documents with RAG-attachable
// chunks. Owner + Scope together determine visibility:
//   - Scope="" or "user": only Owner's agents can attach.
//   - Scope="deployment": any user's agents can attach (admin-
//     authored at the HTTP layer).
type Collection struct {
	ID          string `json:"id"`
	Owner       string `json:"owner"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Scope controls who can attach this collection. Empty or
	// "user" = per-user (Owner has exclusive access); "deployment"
	// = deployment-wide (any user's agent can attach). Deployment
	// scope is admin-authored only at the HTTP layer; the data
	// model itself doesn't gate write access — that's the
	// admin endpoint's job.
	Scope   string    `json:"scope,omitempty"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated,omitempty"`
	// IngestedURLs tracks every URL that has been pulled into this
	// collection by the autofill flow. Used to dedupe across
	// repeated "Auto-fill from web" clicks — the second run won't
	// re-fetch URLs it already pulled. Not populated for manual
	// uploads. Carried on the record so it survives across
	// processes; not all consumers of Collection need to populate
	// it.
	IngestedURLs []string `json:"ingested_urls,omitempty"`
	// FilterRules — optional user-authored markdown describing
	// what this collection should and shouldn't accept. Used by
	// the autofill flow's query generator + LLM judge. Format is
	// freeform but bullet lists read best.
	FilterRules string `json:"filter_rules,omitempty"`
	// ClassifyOnAutofill enables the LLM judge pass during
	// autofill. When true, every fetched + extracted candidate
	// goes through a non-thinking worker call that decides
	// keep/drop. Default false. Autofill-specific; ignored by
	// other consumers of Collection.
	ClassifyOnAutofill bool `json:"classify_on_autofill,omitempty"`
}

// CollectionSource returns the chunk-source tag for a collection's
// embedded chunks. The runner uses this prefix to RAG-search a
// collection without needing the user identity.
func CollectionSource(id string) string {
	return "collection:" + id
}

// CollectionsBucket is the global.db bucket where Document Collections
// are homed. Collections were first built inside the orchestrate app,
// so their metadata + user-scoped chunks live in that bucket (the table
// name CollectionsTable = "orchestrate_collections" already encodes
// this). Any app that wants the shared collection pool must reach this
// bucket rather than its own per-app bucket — see CollectionsDB.
const CollectionsBucket = "orchestrate"

// CollectionsDB returns the base store under which Document Collections
// live, regardless of which app is asking:
//   - user-scoped metadata:  UserDB(CollectionsDB(), user)
//   - user-scoped chunks:     CollectionsDB() root (keyed by CollectionSource)
//   - deployment metadata + chunks: RootDB
//
// Apps must NOT use their own T.DB bucket for collections — that's
// app-private and won't see the shared pool. Returns nil before the
// database is open. Once the planned collections-app move lands, only
// this accessor (and CollectionsBucket) changes.
func CollectionsDB() Database {
	if RootDB == nil {
		return nil
	}
	return RootDB.Bucket(CollectionsBucket)
}

// IsDeploymentScope reports whether a collection record is in the
// deployment-wide pool. Empty string is treated as user-scoped for
// back-compat with records written before the Scope field existed.
func IsDeploymentScope(c Collection) bool {
	return c.Scope == CollectionScopeDeployment
}

// SearchCollections runs a RAG search over a FIXED set of Document
// Collections and returns the top-k chunks across them. This is the
// app-agnostic retrieval primitive behind any "attach reference
// corpora to my app" feature (codewriter, techwriter, …): no
// AgentRecord, no skills, no per-(user, agent) corpus — just "given
// these collection IDs and a query, hand me the most relevant chunks."
// searchAgentKnowledge in apps/orchestrate layers agent/skill/memory
// concerns ON TOP of this same idea; apps that aren't agents call here
// directly instead of dragging in that machinery.
//
// `base` is the collections home (pass CollectionsDB() — NOT the
// caller's own app bucket). Metadata is read from UserDB(base, user);
// each ID is resolved via LoadCollection so user-scoped and
// deployment-scoped IDs mix freely and access gating (ownership /
// scope) is enforced — an ID the caller can't see contributes nothing.
//
// Chunk storage mirrors the ingest paths: user-scoped chunks live at
// `base` root (keyed by CollectionSource), deployment-scoped chunks
// live in RootDB. Both are searched as needed and merged by score
// (capped at k); the merge dedups by chunk ID.
//
// Embeddings are used when configured; otherwise it degrades to a
// substring scan so the feature still works without an embedding
// backend. Collections are curated by definition, so there is no
// curated/derived provenance gate here.
func SearchCollections(ctx context.Context, base Database, user string, collectionIDs []string, query string, k int) []SearchHit {
	if base == nil || strings.TrimSpace(query) == "" || k <= 0 || len(collectionIDs) == 0 {
		return nil
	}
	metaDB := UserDB(base, user) // nil when user=="" → LoadCollection reads deployment only
	// Resolve each ID to its chunk-source tag, routed to the store its
	// chunks actually live in. Unknown / not-visible IDs are skipped.
	baseSources := make(map[string]bool, len(collectionIDs)) // user-scoped → base root
	rootSources := make(map[string]bool, len(collectionIDs)) // deployment → RootDB
	for _, cid := range collectionIDs {
		cid = strings.TrimSpace(cid)
		if cid == "" {
			continue
		}
		c, ok := LoadCollection(metaDB, user, cid)
		if !ok {
			continue
		}
		src := CollectionSource(c.ID)
		if IsDeploymentScope(c) {
			rootSources[src] = true
		} else {
			baseSources[src] = true
		}
	}
	if len(baseSources) == 0 && len(rootSources) == 0 {
		return nil
	}

	var vec []float32
	if GetEmbeddingConfig().Enabled {
		if v, err := Embed(ctx, query); err == nil && len(v) > 0 {
			vec = v
		}
	}
	search := func(db Database, sources map[string]bool) []SearchHit {
		if db == nil || len(sources) == 0 {
			return nil
		}
		allow := func(c EmbeddedChunk) bool { return sources[c.Source] }
		// Hybrid: vector + keyword, so an exact term the embedding misses still
		// surfaces (also covers the no-embedding case, keyword only).
		return HybridSearchByPredicate(db, allow, query, vec, k)
	}
	hits := search(base, baseSources)
	if len(rootSources) > 0 && RootDB != nil {
		hits = MergeHitsByScore(hits, search(RootDB, rootSources), k)
	}
	return hits
}

// FetchCollectionDoc assembles the full text of one document (by its
// report/doc id) from the given collections, scoped exactly like
// SearchCollections (user-scoped collections live in base, deployment ones
// in RootDB). Returns "" when the doc isn't in those collections. Orders
// chunks by section and caps at maxChars (default 10000 when <= 0). This is
// the fetch counterpart to SearchCollections — apps use it to back a
// "fetch full doc by id" tool scoped to a collection set.
func FetchCollectionDoc(base Database, user string, collectionIDs []string, docID string, maxChars int) string {
	docID = strings.TrimSpace(docID)
	if docID == "" || len(collectionIDs) == 0 {
		return ""
	}
	metaDB := UserDB(base, user)
	baseSources := make(map[string]bool, len(collectionIDs))
	rootSources := make(map[string]bool, len(collectionIDs))
	for _, cid := range collectionIDs {
		cid = strings.TrimSpace(cid)
		if cid == "" {
			continue
		}
		c, ok := LoadCollection(metaDB, user, cid)
		if !ok {
			continue
		}
		src := CollectionSource(c.ID)
		if IsDeploymentScope(c) {
			rootSources[src] = true
		} else {
			baseSources[src] = true
		}
	}
	collect := func(db Database, sources map[string]bool) []EmbeddedChunk {
		if db == nil || len(sources) == 0 {
			return nil
		}
		var out []EmbeddedChunk
		for _, key := range db.Keys(EmbeddedChunks) {
			var c EmbeddedChunk
			if !db.Get(EmbeddedChunks, key, &c) {
				continue
			}
			if c.ReportID != docID || !sources[c.Source] {
				continue
			}
			out = append(out, c)
		}
		return out
	}
	chunks := collect(base, baseSources)
	chunks = append(chunks, collect(RootDB, rootSources)...)
	return AssembleChunkDoc(chunks, maxChars)
}

// AssembleChunkDoc reconstructs a readable document from its embedded
// chunks: ordered by section, titled (prefers the stamped Title, else the
// first section heading), section headers de-duplicated, non-authoritative
// Kind tags inlined, and truncated to maxChars (default 10000) at a
// paragraph boundary. Returns "" for no chunks.
func AssembleChunkDoc(chunks []EmbeddedChunk, maxChars int) string {
	if len(chunks) == 0 {
		return ""
	}
	if maxChars <= 0 {
		maxChars = 10000
	}
	sort.Slice(chunks, func(i, j int) bool {
		if chunks[i].Section != chunks[j].Section {
			return chunks[i].Section < chunks[j].Section
		}
		return chunks[i].ID < chunks[j].ID
	})
	docName := strings.TrimSpace(chunks[0].Title)
	if docName == "" {
		docName = strings.TrimSpace(strings.TrimPrefix(chunks[0].Section, "## "))
	}
	if docName == "" {
		docName = "(unnamed document)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", docName)
	lastSection := ""
	for _, c := range chunks {
		section := strings.TrimSpace(strings.TrimPrefix(c.Section, "## "))
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
		if idx := strings.LastIndex(truncated, "\n\n"); idx > maxChars/2 {
			truncated = truncated[:idx]
		}
		out = truncated + fmt.Sprintf("\n\n[…truncated; full document is %d chars. Search with a tighter query to find the section you need.]", len(out))
	}
	return out
}

// LoadCollection reads one collection by ID. Looks in the user's
// per-user pool first, then falls back to the deployment-wide pool.
// Returns (record, false) when the collection doesn't exist OR is
// user-scoped + owned by someone else. Pass empty user to skip the
// per-user lookup and read deployment-scoped only.
func LoadCollection(udb Database, user, id string) (Collection, bool) {
	if id == "" {
		return Collection{}, false
	}
	if udb != nil && user != "" {
		var c Collection
		if udb.Get(CollectionsTable, id, &c) {
			if c.Owner == user && !IsDeploymentScope(c) {
				return c, true
			}
		}
	}
	if RootDB != nil {
		var c Collection
		if RootDB.Get(GlobalCollectionsTable, id, &c) {
			return c, true
		}
	}
	return Collection{}, false
}

// ListCollections returns every collection visible to user: the
// user's own per-user pool unioned with all deployment-scoped
// collections. Sorted by most-recently-updated first across both
// sources. Empty user returns deployment-scoped only.
func ListCollections(udb Database, user string) []Collection {
	var out []Collection
	if udb != nil && user != "" {
		for _, k := range udb.Keys(CollectionsTable) {
			var c Collection
			if !udb.Get(CollectionsTable, k, &c) {
				continue
			}
			if c.Owner != user {
				continue
			}
			if IsDeploymentScope(c) {
				continue // shouldn't be in per-user pool; skip defensively
			}
			out = append(out, c)
		}
	}
	if RootDB != nil {
		seen := make(map[string]bool, len(out))
		for _, c := range out {
			seen[c.ID] = true
		}
		for _, k := range RootDB.Keys(GlobalCollectionsTable) {
			var c Collection
			if !RootDB.Get(GlobalCollectionsTable, k, &c) {
				continue
			}
			if seen[c.ID] {
				continue
			}
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Updated.After(out[j].Updated)
	})
	return out
}

// SaveCollection writes the record to the right pool based on
// Scope. User-scoped goes to udb; deployment-scoped goes to RootDB
// under GlobalCollectionsTable. Updated timestamp stamped on write.
func SaveCollection(udb Database, c Collection) {
	c.Updated = time.Now()
	if IsDeploymentScope(c) {
		if RootDB != nil {
			RootDB.Set(GlobalCollectionsTable, c.ID, c)
		}
		return
	}
	if udb != nil {
		udb.Set(CollectionsTable, c.ID, c)
	}
}

// DeleteCollection removes the metadata record + every chunk
// under its source. Routes the metadata delete to the right pool
// based on the loaded record's scope. Returns the number of chunks
// vacuumed (0 when appDB is nil).
func DeleteCollection(udb, appDB Database, user, id string) (chunksRemoved int) {
	if id == "" {
		return 0
	}
	c, ok := LoadCollection(udb, user, id)
	if !ok {
		return 0
	}
	if IsDeploymentScope(c) {
		if RootDB != nil {
			RootDB.Unset(GlobalCollectionsTable, c.ID)
		}
	} else if udb != nil {
		udb.Unset(CollectionsTable, c.ID)
	}
	if appDB != nil {
		chunksRemoved = WipeChunksBySourcePrefix(appDB, CollectionSource(id))
	}
	return chunksRemoved
}
