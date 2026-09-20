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
	"strconv"
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
		Description: "Auto-populated cross-cutting knowledge base: every research report, debate verdict, and answered question this deployment has produced. Searched via knowledge_search on any agent (auto-attached when the agent has no curated collections of its own).",
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
	// = deployment-wide (any user's agent can attach).
	//
	// NOTHING SETS THIS TO "deployment" EXCEPT THE FRAMEWORK. The only
	// deployment-scoped collection that exists is the auto-created
	// deployment-knowledge one (EnsureDeploymentKnowledgeCollection); the
	// create handler never reads a scope off the request, so a user cannot
	// mint one, and there is no admin endpoint that promotes an existing
	// collection either.
	//
	// This comment used to say deployment scope was "admin-authored only at
	// the HTTP layer" and that gating write access was "the admin endpoint's
	// job". There is no such endpoint. The sentence described a gate that had
	// never been built, which is worse than describing none: the next person to
	// add a write path reads it and believes the check is already somewhere
	// else. If promoting a collection is ever wanted, it goes through
	// core/promotion like tools, apps and agents do — the owner asks, an
	// administrator approves — and this comment says so instead.
	//
	// The data model itself does not gate writes, and that part was true:
	// SaveCollection routes on Scope alone, so whatever sets the field decides
	// the pool.
	Scope string `json:"scope,omitempty"`

	// AllowedUsers is the peer-share recipient set: which other users may
	// ATTACH and search this collection besides its Owner. Empty = private,
	// which is the default.
	//
	// The middle rung of the governance model, and the same ACL field
	// AgentRecord, SecureCredential and PersistentTempTool carry, so an owner
	// learns one control and an admin audits one shape. Distinct from Scope,
	// which is the top rung: these named people, not everybody.
	//
	// A recipient gets READ. They can attach it to their agents and search it;
	// they cannot add documents, rename it, or delete it. The owner's copy
	// stays the only copy, so a document added later is shared too and a
	// document removed is gone for everyone, which is what people mean by
	// sharing a folder.
	//
	// Searchable by a recipient because chunks live in the shared VectorDB
	// keyed by collection source, not in the owner's own store. On a
	// deployment with no VectorDB the legacy split stores apply and a shared
	// collection's chunks stay in the owner's base, where a recipient's search
	// cannot reach them; SharedCollectionsFor says so rather than returning a
	// collection that silently finds nothing.
	AllowedUsers []string  `json:"allowed_users,omitempty"`
	Created      time.Time `json:"created"`
	Updated      time.Time `json:"updated,omitempty"`
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
	// CuratorAgent names an agent that is IN CHARGE of this collection: it
	// keeps the corpus current, and is granted the four corpus actions bound
	// to this collection and nothing else.
	//
	// A field beside the rest rather than a record of its own, because this
	// does not describe a new thing, it describes this collection. One agent
	// may look after several collections and carries nothing per-collection
	// that a name cannot express.
	//
	// Empty is the normal case: a collection filled by hand, by upload or by
	// auto-fill has nobody in charge of it, and reading it is unaffected
	// either way.
	CuratorAgent string `json:"curator_agent,omitempty"`
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
	return searchCollectionsWith(ctx, globalCollectionsEnv(base), user, collectionIDs, query, k)
}

// collectionsEnv bundles the storage + embedding state the retrieval primitives
// read, so they run against injected state (an SDK consumer's AppCore) or the
// process globals (the server) through one seam. SDK Phase 1 — see
// docs/sdk-decoupling-scope.md.
type collectionsEnv struct {
	Base       Database        // user-scoped collections home
	VectorDB   Database        // dedicated vector store (all shared chunk I/O)
	Deployment Database        // legacy deployment-scoped chunk store (RootDB)
	Embed      EmbeddingConfig // embedding backend for query vectors
}

// globalCollectionsEnv wires retrieval to the process globals — the server's
// default. `base` is the collections home (pass CollectionsDB()).
func globalCollectionsEnv(base Database) collectionsEnv {
	return collectionsEnv{Base: base, VectorDB: VectorDB, Deployment: RootDB, Embed: GetEmbeddingConfig()}
}

// collEnv resolves an AppCore's injected retrieval state, falling back to the
// process globals for any field the caller left unset — so an SDK consumer
// overrides only what it needs and the server-embedded AppCore behaves exactly
// as before.
func (T *AppCore) collEnv() collectionsEnv {
	env := collectionsEnv{Base: T.DB, VectorDB: T.VectorDB, Deployment: RootDB, Embed: T.EmbedCfg}
	if env.Base == nil {
		env.Base = CollectionsDB()
	}
	if env.VectorDB == nil {
		env.VectorDB = VectorDB
	}
	if !env.Embed.Enabled && strings.TrimSpace(env.Embed.Endpoint) == "" {
		env.Embed = GetEmbeddingConfig()
	}
	return env
}

// SearchCollections is the instance-scoped RAG search: same as the package
// SearchCollections but over this agent's injected retrieval backend (VectorDB /
// EmbedCfg / DB), falling back to the globals for anything unset. The SDK entry
// point for retrieval.
func (T *AppCore) SearchCollections(ctx context.Context, user string, collectionIDs []string, query string, k int) []SearchHit {
	return searchCollectionsWith(ctx, T.collEnv(), user, collectionIDs, query, k)
}

// RelevanceFloor is the score below which a retrieved item is not worth
// showing: "related enough to be worth reading", not "identical". ONE number
// for every retrieval surface — collection search, knowledge_search and
// memory_search, the fact store, unified recall, the graph bridge — because
// they all rank on the same cosine against the same embedding backend, and
// three copies of it under three names (collectionSearchMinScore,
// factSearchMinScore, manualSearchMinScore) were three places for the next
// model change to be fixed in and two to be forgotten.
//
// It exists because the ranking primitives do not filter. The vector half
// drops only a non-positive cosine and the keyword half keeps any chunk
// containing any query term, so both hand back their top k whatever the scores
// are — and a collection with k passages or fewer therefore returned ALL of
// them for EVERY query, which reads as a search that does not search. The
// concrete failure on the tool side was an LVM-shrink query pulling an Nvidia
// GPU article (shared "Linux"/"reduce" surface terms) that a model then wove
// into a downstream question as if it were on topic.
//
// SearchChunksKeywordByPredicate is scaled around this number (its comment
// says so: matched terms must carry ~41% of the query's IDF mass to clear
// 0.35), and diversifyHits promotes a passage only when it clears it.
//
// On the vector half 0.35 is a COSINE, and what counts as unrelated moves with
// the embedding model. It is the knob to turn if recall goes thin after a
// model change — not the ranking. Well below the dedup threshold (0.90, "same
// fact"). A code constant rather than a tunable on purpose: see the note at
// the top of tunables.go on thresholds an operator cannot judge. The tunable
// RecallMinScore is a SECOND, operator-set floor on top of this one, off by
// default.
const RelevanceFloor = 0.35

// aboveFloor drops hits under RelevanceFloor.
//
// Applied AFTER the top-k truncation, which loses nothing: hits arrive sorted
// by descending score, so a passage below the floor can never have displaced
// one above it. Returning fewer than k — or none — is the point. "Nothing in
// the linked collections is relevant to this" is an answer, and the callers
// already say it in those words.
func aboveFloor(hits []SearchHit) []SearchHit {
	out := hits[:0:0]
	for _, h := range hits {
		if h.Score >= RelevanceFloor {
			out = append(out, h)
		}
	}
	return out
}

func searchCollectionsWith(ctx context.Context, env collectionsEnv, user string, collectionIDs []string, query string, k int) []SearchHit {
	if env.Base == nil || strings.TrimSpace(query) == "" || k <= 0 || len(collectionIDs) == 0 {
		return nil
	}
	metaDB := UserDB(env.Base, user) // nil when user=="" → LoadCollection reads deployment only
	// Resolve each ID to its chunk-source tag, routed to the store its
	// chunks actually live in. Unknown / not-visible IDs are skipped.
	baseSources := make(map[string]bool, len(collectionIDs)) // user-scoped → base root
	rootSources := make(map[string]bool, len(collectionIDs)) // deployment → Deployment store
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
	if env.Embed.Enabled {
		if v, err := EmbedWith(ctx, env.Embed, query); err == nil && len(v) > 0 {
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
	// Chunks for both user- and deployment-scoped collections live in the
	// dedicated VectorDB — all shared chunk I/O routes there, and new ingests
	// (autofill, research) write ONLY there. Search it for the union of every
	// resolved source. Fall back to the legacy split stores (base root for
	// user-scoped, Deployment for deployment) only when VectorDB is unset (early
	// init); the one-shot migration left those legacy rows in place.
	if env.VectorDB != nil {
		allSources := make(map[string]bool, len(baseSources)+len(rootSources))
		for s := range baseSources {
			allSources[s] = true
		}
		for s := range rootSources {
			allSources[s] = true
		}
		return aboveFloor(search(env.VectorDB, allSources))
	}
	hits := search(env.Base, baseSources)
	if len(rootSources) > 0 && env.Deployment != nil {
		hits = MergeHitsByScore(hits, search(env.Deployment, rootSources), k)
	}
	return aboveFloor(hits)
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
		return ChunksWhere(db, func(c EmbeddedChunk) bool { return c.ReportID == docID && sources[c.Source] })
	}
	// Chunks live in the dedicated VectorDB (see SearchCollections); read the
	// union there, falling back to the legacy split stores only pre-VectorDB.
	if VectorDB != nil {
		allSources := make(map[string]bool, len(baseSources)+len(rootSources))
		for s := range baseSources {
			allSources[s] = true
		}
		for s := range rootSources {
			allSources[s] = true
		}
		return AssembleChunkDoc(collect(VectorDB, allSources), maxChars)
	}
	chunks := collect(base, baseSources)
	chunks = append(chunks, collect(RootDB, rootSources)...)
	return AssembleChunkDoc(chunks, maxChars)
}

// SortChunksForAssembly puts one document's chunks back into document order.
//
// Chunks stamped with Ord (every ingest since the field existed) sort by it,
// which is the order the ingest produced them. Rows from before the stamp
// have nothing that records their order, so they get the best available
// approximation: section heading, then the numeric "(part N)" / "(part N/M)"
// suffixes compared as numbers, then ID. That still puts sections in
// alphabetical rather than document order — unrecoverable for those rows —
// but it stops "(part 10)" landing before "(part 2)", which is what made a
// long unstructured upload read as scrambled paragraphs. One legacy chunk in
// the set switches the whole set to the fallback, since an Ord of 0 has no
// position to compare against.
func SortChunksForAssembly(chunks []EmbeddedChunk) {
	stamped := true
	for i := range chunks {
		if chunks[i].Ord <= 0 {
			stamped = false
			break
		}
	}
	if stamped {
		sort.Slice(chunks, func(i, j int) bool {
			if chunks[i].Ord != chunks[j].Ord {
				return chunks[i].Ord < chunks[j].Ord
			}
			// Same position: the repair pass splits an oversized row into
			// parts that all inherit its Ord, so the part number orders them.
			// Falling straight to ID would scramble them, since an ID is a
			// UUID and carries no order at all.
			_, pi := chunkPartOrder(chunks[i].Section)
			_, pj := chunkPartOrder(chunks[j].Section)
			for n := 0; n < len(pi) && n < len(pj); n++ {
				if pi[n] != pj[n] {
					return pi[n] < pj[n]
				}
			}
			if len(pi) != len(pj) {
				return len(pi) < len(pj)
			}
			return chunks[i].ID < chunks[j].ID
		})
		return
	}
	sort.Slice(chunks, func(i, j int) bool {
		bi, pi := chunkPartOrder(chunks[i].Section)
		bj, pj := chunkPartOrder(chunks[j].Section)
		if bi != bj {
			return bi < bj
		}
		for n := 0; n < len(pi) && n < len(pj); n++ {
			if pi[n] != pj[n] {
				return pi[n] < pj[n]
			}
		}
		if len(pi) != len(pj) {
			return len(pi) < len(pj)
		}
		return chunks[i].ID < chunks[j].ID
	})
}

// chunkPartOrder splits a section heading into its base name and the
// sequence of part numbers the chunker appended, outermost first: "Guide
// (part 2) (part 1/3)" → ("Guide", [2, 1]). A trailing "(part …)" that is not
// digits (or digits "/" digits) is left on the base name. Headings with no
// suffix return a nil sequence, which sorts before any suffixed sibling.
func chunkPartOrder(section string) (string, []int) {
	s := strings.TrimSpace(strings.TrimPrefix(section, "## "))
	var parts []int
	for {
		idx := strings.LastIndex(s, " (part ")
		if idx < 0 || !strings.HasSuffix(s, ")") {
			break
		}
		inner := s[idx+len(" (part ") : len(s)-1]
		if slash := strings.IndexByte(inner, '/'); slash >= 0 {
			inner = inner[:slash]
		}
		n, err := strconv.Atoi(inner)
		if err != nil {
			break
		}
		parts = append([]int{n}, parts...)
		s = strings.TrimSpace(s[:idx])
	}
	return s, parts
}

// HitFormat renders search hits as the text a model reads. One shape for
// every app, because the chunk store stamps a Title, a Locator (the PDF
// page), a Kind (comment thread versus article body) and a document id on
// each hit, and every app that hand-rolled its own loop dropped most of
// them: a page citation and the "one commenter noted" framing were
// recorded at ingest and never reached the model that was asked to cite.
//
// Per hit:
//
//	N. <title> — <section> (<locator>) [<kind>] [<tag>]
//	   doc_id: <report id>          (DocIDs only)
//	   section: <section>           (DocIDs only, when it differs from the title)
//	   <text, continuation lines indented>
//
// Excerpt caps each hit's text at that many characters, cut at a word
// boundary with an ellipsis; 0 renders the whole chunk. A caller that also
// offers a fetch-by-id tool sets DocIDs so the model can read further; one
// that does not should leave it off, since a doc_id with nothing to pass it
// to is an invitation to call a tool that is not there. Tag adds one more
// bracketed marker per hit (an app's provenance label, say); nil adds none.
type HitFormat struct {
	Excerpt int
	DocIDs  bool
	Tag     func(SearchHit) string
}

// Render formats hits in rank order. Empty input renders as "".
func (f HitFormat) Render(hits []SearchHit) string {
	var b strings.Builder
	for i, h := range hits {
		if i > 0 {
			b.WriteString("\n\n")
		}
		docName := strings.TrimSpace(h.Title)
		section, _ := chunkPartOrder(h.Section)
		if docName == "" {
			docName = section
		}
		if docName == "" {
			docName = "(unnamed document)"
		}
		fmt.Fprintf(&b, "%d. %s", i+1, docName)
		if section != "" && section != docName {
			fmt.Fprintf(&b, " \u00b7 %s", section)
		}
		if h.Locator != "" {
			fmt.Fprintf(&b, " (%s)", h.Locator)
		}
		if h.Kind != "" {
			fmt.Fprintf(&b, " [%s]", h.Kind)
		}
		if f.Tag != nil {
			if tag := strings.TrimSpace(f.Tag(h)); tag != "" {
				fmt.Fprintf(&b, " [%s]", tag)
			}
		}
		b.WriteString("\n")
		if f.DocIDs {
			fmt.Fprintf(&b, "   doc_id: %s\n", h.ReportID)
			if section != "" && section != docName {
				fmt.Fprintf(&b, "   section: %s\n", section)
			}
		}
		b.WriteString("   ")
		b.WriteString(strings.ReplaceAll(excerptText(h.Text, f.Excerpt), "\n", "\n   "))
	}
	return b.String()
}

// excerptText trims text to max characters at a word boundary, with an
// ellipsis; max <= 0 or text already within it returns the trimmed text.
func excerptText(text string, max int) string {
	text = strings.TrimSpace(text)
	if max <= 0 || len(text) <= max {
		return text
	}
	cut := text[:max]
	if idx := strings.LastIndex(cut, " "); idx > max/2 {
		cut = cut[:idx]
	}
	return strings.TrimRight(cut, " \t\n") + "…"
}

// AssembleChunkDoc reconstructs a readable document from its embedded
// chunks: in document order (see SortChunksForAssembly), titled (prefers
// the stamped Title, else the first section heading), section headers
// de-duplicated, non-authoritative Kind tags inlined, and truncated to
// maxChars (default 10000) at a paragraph boundary. Returns "" for no
// chunks.
func AssembleChunkDoc(chunks []EmbeddedChunk, maxChars int) string {
	if len(chunks) == 0 {
		return ""
	}
	if maxChars <= 0 {
		maxChars = 10000
	}
	SortChunksForAssembly(chunks)
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
	// Shared WITH this user by somebody else. Looked up through the index and
	// then re-checked against the owner's record, so a stale index entry cannot
	// grant access the owner has taken away.
	if user != "" && RootDB != nil && VectorDB != nil {
		for _, ref := range ListPeerShares(RootDB, SharedCollectionsTable, user) {
			if ref.ID != id {
				continue
			}
			ownerDB := UserDB(CollectionsDB(), ref.Owner)
			if ownerDB == nil {
				continue
			}
			var c Collection
			if ownerDB.Get(CollectionsTable, id, &c) && c.Owner == ref.Owner && collectionSharedWith(c, user) {
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
	// Shared WITH this user, before the deployment-wide ones: somebody chose to
	// give them these, which is closer to their own than a corpus everybody
	// has.
	for _, c := range SharedCollectionsFor(user) {
		out = append(out, c)
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

// SharedCollectionsTable indexes peer shares: recipient -> (owner, collection).
const SharedCollectionsTable = "shared_collections"

// SharedCollectionsFor returns the collections other people have shared WITH
// this user, read from each owner's own store.
//
// Empty when there is no VectorDB. That is not a quiet failure: without it,
// chunks live in the owner's per-user store and a recipient's search reaches
// none of them, so offering the collection would be offering something that
// attaches cleanly and then never matches anything. A deployment in that state
// is mid-migration, and the honest answer is that peer sharing is not available
// yet rather than available and empty.
func SharedCollectionsFor(user string) []Collection {
	if RootDB == nil || VectorDB == nil || strings.TrimSpace(user) == "" {
		return nil
	}
	var out []Collection
	for _, ref := range ListPeerShares(RootDB, SharedCollectionsTable, user) {
		udb := UserDB(CollectionsDB(), ref.Owner)
		if udb == nil {
			continue
		}
		var c Collection
		if !udb.Get(CollectionsTable, ref.ID, &c) || c.ID == "" {
			continue
		}
		// Re-checked against the record rather than trusted from the index: the
		// list on the collection is what the owner edits, the index is derived,
		// and a derived thing that can outvote its source is how a revoked
		// share keeps working.
		if c.Owner != ref.Owner || !collectionSharedWith(c, user) {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func collectionSharedWith(c Collection, user string) bool {
	for _, u := range c.AllowedUsers {
		if u == user {
			return true
		}
	}
	return false
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
	// The peer-share index follows the record in the same write, so a share and
	// its lookup cannot disagree about who has access.
	if RootDB != nil && c.Owner != "" {
		SetPeerShareRecipients(RootDB, SharedCollectionsTable, c.Owner, c.ID, c.AllowedUsers)
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
	// A recipient resolves a shared collection through LoadCollection, so
	// without this a share would carry the right to destroy somebody else's
	// documents. Sharing gives READ: attach it, search it, nothing more.
	if !IsDeploymentScope(c) && c.Owner != "" && c.Owner != user {
		return 0
	}
	if IsDeploymentScope(c) {
		if RootDB != nil {
			RootDB.Unset(GlobalCollectionsTable, c.ID)
		}
	} else if udb != nil {
		udb.Unset(CollectionsTable, c.ID)
	}
	// The shares go with it: an index entry outliving its record points at
	// nothing, which reads to a recipient as access they lost.
	if RootDB != nil && c.Owner != "" {
		DropPeerShares(RootDB, SharedCollectionsTable, c.Owner, c.ID)
	}
	if appDB != nil {
		chunksRemoved = WipeChunksBySourcePrefix(appDB, CollectionSource(id))
	}
	return chunksRemoved
}
