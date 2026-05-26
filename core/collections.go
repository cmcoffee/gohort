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
	"sort"
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
// live in RootDB.EmbeddedChunks so cross-app retrieval works
// without per-bucket fanout. Agents with empty AttachedCollections
// auto-include deployment collections in their knowledge_search
// scope; agents with curated AttachedCollections stay self-contained.
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

// IsDeploymentScope reports whether a collection record is in the
// deployment-wide pool. Empty string is treated as user-scoped for
// back-compat with records written before the Scope field existed.
func IsDeploymentScope(c Collection) bool {
	return c.Scope == CollectionScopeDeployment
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
