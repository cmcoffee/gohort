// Scoped graph-memory primitive — the structured complement to the flat
// factstore. Where MemoryFact holds freeform notes recalled by similarity,
// the graph holds ENTITIES (nodes: person / org / project / place / thing)
// and EDGES (relationships between them), so an agent can answer "who is X
// and how do they connect to Y" from structure instead of fuzzy passages.
//
// It is the 4th memory layer alongside store_fact (flat always-in-prompt
// facts), knowledge/RAG (hybrid vector+keyword passages), and the cortex
// (time-ordered narrative). What it adds that the others can't: alias-based
// entity consolidation and supersession (re-point an edge, no stale chunk
// left behind).
//
// v1 behavior (per the settled design):
//   - Scope: per-namespace ("agent:<id>"), passed a user-scoped UserDB —
//     so it's per-(user, agent) end to end, same as the factstore.
//   - Supersession: DELETE-ON-UPDATE. Re-pointing a single-valued relation
//     (works_at, lives_in) removes the prior value; multi-valued relations
//     (knows, member_of) coexist. The caller picks per link via `replace`.
//   - Timestamps: BOTH entities AND edges carry Created + Updated from day
//     one (edges get Updated even though delete-on-update doesn't read it
//     yet), so the future versioned/"as-of" upgrade is a non-breaking
//     additive switch — no schema migration, no backfill.
//
// Storage: two kvlite tables. Entities in GraphEntityTable keyed
// "<namespace>/<entityID>"; edges in GraphEdgeTable keyed
// "<namespace>/<from>|<rel>|<to>" so a prefix scan on "<namespace>/<from>|"
// returns a node's outbound edges cheaply. Inbound lookups scan-and-filter
// (a personal graph is small); add a reverse index when it grows.

package core

import (
	"sort"
	"strings"
	"time"
)

// Graph table names. Shared across apps (like MemoryFactsTable) so a future
// cross-app "everything I know" surface can enumerate; the namespace field
// keeps app/agent data partitioned.
const (
	GraphEntityTable = "core_graph_entity"
	GraphEdgeTable   = "core_graph_edge"
)

// Graph-bounds tunables — the graph's parity with the fact store's hard cap.
// Auto-extraction populates the graph continuously, so without a bound a
// long-lived agent's namespace grows forever (and every name/alias probe and
// recall_about scan walks it).
const (
	// TunableGraphEntityCap: max live entities per namespace; past it the
	// least-recently-updated entities are evicted (their edges go with them).
	// 0 disables.
	TunableGraphEntityCap = "tune_graph_entity_cap"
	// TunableGraphEdgeCap: max LIVE edges per namespace; past it the
	// least-recently-updated live edges are evicted. Tombstoned (retired)
	// edges are bounded separately by tombstone retention. 0 disables.
	TunableGraphEdgeCap = "tune_graph_edge_cap"
)

func init() {
	RegisterTunable(TunableSpec{Key: TunableGraphEntityCap, Category: "Limits",
		Label: "Graph entity cap per agent (0 = off)",
		Help:  "Max entities in one agent's memory graph. Past the cap, the least-recently-updated entities are evicted (edges included) so auto-extraction can't grow the graph without bound.",
		Kind:  KindInt, Default: 500, Min: 0, Max: 10000})
	RegisterTunable(TunableSpec{Key: TunableGraphEdgeCap, Category: "Limits",
		Label: "Graph edge cap per agent (0 = off)",
		Help:  "Max live relationships in one agent's memory graph. Past the cap, the least-recently-updated edges are evicted.",
		Kind:  KindInt, Default: 2000, Min: 0, Max: 50000})
}

// GraphEntity is one node: a thing the agent knows about. Identity is the
// slug ID ("person:robin-vale"); Aliases are the case-insensitive merge
// keys, so "Robin", "@robin", and "Robin Vale" resolve to one node. Attrs
// holds non-relational facts (email, title) that don't warrant their own node.
type GraphEntity struct {
	Namespace string            `json:"namespace"`
	ID        string            `json:"id"`   // slug: "<kind>:<name-slug>"
	Kind      string            `json:"kind"` // person | org | project | place | thing
	Name      string            `json:"name"`
	Aliases   []string          `json:"aliases,omitempty"`
	Attrs     map[string]string `json:"attrs,omitempty"`
	Created   time.Time         `json:"created"`
	Updated   time.Time         `json:"updated"`
	// MemoryProvenance is reserved: the graph layer has no hygiene pass (sweep /
	// eviction / merge) yet, so these fields sit unset. Embedded now so a future
	// graph-tombstone or entity-staleness pass inherits the same vocabulary as the
	// fact store rather than inventing a second one. Zero value = live, unknown
	// origin, never decays; all fields omitempty, so unused adds nothing to the wire.
	MemoryProvenance
}

// GraphEdge is one relationship: From --[Rel]--> To. From/To are entity IDs.
// Updated is recorded from v1 (unused by delete-on-update) so the versioned
// upgrade can set a validity window instead of dropping, without migration.
type GraphEdge struct {
	Namespace string    `json:"namespace"`
	From      string    `json:"from"` // entity ID
	Rel       string    `json:"rel"`  // verb slug: works_at, reports_to, knows
	To        string    `json:"to"`   // entity ID
	Note      string    `json:"note,omitempty"`
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated"`
	// MemoryProvenance carries retirement: correcting a single-valued relation via
	// LinkGraphEdge(replace=true) tombstones the old edge (Reason=RetireSuperseded,
	// Successor = the new target entity) as a validity window instead of
	// hard-dropping it, so recall_about can show the past relationship ("previously
	// worked at X"). Zero value = live; retired edges are filtered from live scans.
	// The origin fields (Source/Volatility/AsOf) are unused for edges.
	MemoryProvenance
}

// graphEntityKey / graphEdgeKey assemble the kvlite keys. Slash-delimited
// namespace prefix for clean per-namespace scans; pipe-delimited edge triple
// (entity IDs and rel slugs never contain a pipe).
func graphEntityKey(namespace, id string) string {
	return namespace + "/" + id
}

func graphEdgeKey(namespace, from, rel, to string) string {
	return namespace + "/" + from + "|" + rel + "|" + to
}

// graphSlug lowercases a label and reduces it to [a-z0-9-], collapsing runs
// of other characters to a single dash. Used for entity-ID name slugs.
func graphSlug(s string) string {
	return graphReduce(s, '-')
}

// graphRel slugs a relationship verb to [a-z0-9_] (underscore-joined), so
// "works at" → "works_at" and the pipe-delimited edge key stays unambiguous.
func graphRel(s string) string {
	return graphReduce(s, '_')
}

func graphReduce(s string, sep byte) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	pendingSep := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if pendingSep && b.Len() > 0 {
				b.WriteByte(sep)
			}
			pendingSep = false
			b.WriteRune(r)
			continue
		}
		pendingSep = true
	}
	return b.String()
}

// graphKindOrDefault normalizes a kind, defaulting to "thing".
func graphKindOrDefault(kind string) string {
	k := graphSlug(kind)
	if k == "" {
		return "thing"
	}
	return k
}

// FindGraphEntity resolves an entity in a namespace by its name or any alias,
// case-insensitively. Returns the entity and true on a match. The lookup an
// agent uses to answer "what do I know about X" and the merge probe upserts
// use to consolidate aliases onto one node.
func FindGraphEntity(db Database, namespace, nameOrAlias string) (GraphEntity, bool) {
	namespace = strings.TrimSpace(namespace)
	if db == nil || namespace == "" {
		return GraphEntity{}, false
	}
	return matchGraphEntity(ListGraphEntities(db, namespace), nameOrAlias)
}

// matchGraphEntity resolves a name/alias against an already-loaded entity
// list. Split from FindGraphEntity so multi-probe callers (UpsertGraphEntity
// resolving name + every alias) scan the namespace ONCE instead of once per
// probe — the O(probes × N) hot spot auto-extraction multiplied.
func matchGraphEntity(ents []GraphEntity, nameOrAlias string) (GraphEntity, bool) {
	want := strings.ToLower(strings.TrimSpace(nameOrAlias))
	if want == "" {
		return GraphEntity{}, false
	}
	for _, e := range ents {
		if strings.ToLower(strings.TrimSpace(e.Name)) == want || strings.EqualFold(e.ID, nameOrAlias) {
			return e, true
		}
		for _, a := range e.Aliases {
			if strings.ToLower(strings.TrimSpace(a)) == want {
				return e, true
			}
		}
	}
	return GraphEntity{}, false
}

// getGraphEntity fetches one entity by ID (no alias resolution).
func getGraphEntity(db Database, namespace, id string) (GraphEntity, bool) {
	var e GraphEntity
	if db == nil || namespace == "" || id == "" {
		return GraphEntity{}, false
	}
	if db.Get(GraphEntityTable, graphEntityKey(namespace, id), &e) {
		return e, true
	}
	return GraphEntity{}, false
}

// GetGraphEntity fetches one entity by its exact ID — an O(1) key lookup, unlike
// FindGraphEntity which resolves by name/alias with a full-namespace scan. Use
// this when you already hold the ID (e.g. resolving an edge endpoint to its name):
// passing an ID to FindGraphEntity is both slower AND wrong, since it matches
// names/aliases, not IDs.
func GetGraphEntity(db Database, namespace, id string) (GraphEntity, bool) {
	return getGraphEntity(db, namespace, id)
}

// ListGraphEntities returns every entity in a namespace (prefix scan),
// sorted by name. Empty when nothing's stored or the db handle is nil.
func ListGraphEntities(db Database, namespace string) []GraphEntity {
	namespace = strings.TrimSpace(namespace)
	if db == nil || namespace == "" {
		return nil
	}
	prefix := namespace + "/"
	var out []GraphEntity
	for _, k := range db.Keys(GraphEntityTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		var e GraphEntity
		if db.Get(GraphEntityTable, k, &e) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// graphMinMentionLen is the shortest entity name/alias eligible for substring
// mention-matching. Terms shorter than this ("Ed", "Al", "@x") over-match common
// letter runs, so the cross-layer bridges skip them — the same floor the fact and
// passage bridges have always used inline.
const graphMinMentionLen = 3

// GraphEntityMentionedIn reports whether the entity's canonical name or any alias
// (each at least graphMinMentionLen chars) appears as a case-insensitive substring
// of text. It is the shared predicate behind the cross-layer memory bridges: "is
// this blob of text — a fact note, a recalled passage — about this entity?"
// Name-as-join-key, no stored cross-link to maintain.
func GraphEntityMentionedIn(e GraphEntity, text string) bool {
	return graphEntityMentionedInLower(e, strings.ToLower(text))
}

// graphEntityMentionedInLower is the hot-path variant taking already-lowercased
// text, so a scan over many entities lowercases the (possibly large) haystack once.
func graphEntityMentionedInLower(e GraphEntity, low string) bool {
	if strings.TrimSpace(low) == "" {
		return false
	}
	if term := strings.ToLower(strings.TrimSpace(e.Name)); len(term) >= graphMinMentionLen && strings.Contains(low, term) {
		return true
	}
	for _, a := range e.Aliases {
		if term := strings.ToLower(strings.TrimSpace(a)); len(term) >= graphMinMentionLen && strings.Contains(low, term) {
			return true
		}
	}
	return false
}

// GraphEntitiesMentionedIn returns the namespace's entities that text names (per
// GraphEntityMentionedIn), in ListGraphEntities order, capped at limit (limit <= 0
// means no cap). The dual of GraphEntityMentionedIn: given text from another memory
// layer, which graph entities is it about — the join the Reference → graph and
// Explicit → graph bridges walk. Name-as-join-key; no stored cross-link.
func GraphEntitiesMentionedIn(db Database, namespace, text string, limit int) []GraphEntity {
	if db == nil || strings.TrimSpace(text) == "" {
		return nil
	}
	low := strings.ToLower(text)
	var out []GraphEntity
	for _, e := range ListGraphEntities(db, namespace) {
		if graphEntityMentionedInLower(e, low) {
			out = append(out, e)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out
}

// UpsertGraphEntity creates or MERGES an entity by name/alias within a
// namespace. If an existing node matches the name or any supplied alias
// (case-insensitive), the new aliases + attrs are folded into it and Updated
// is bumped; otherwise a fresh node is created with a "<kind>:<slug>" ID
// (suffixed -2, -3… on slug collision with a different node). Returns the
// resolved entity and whether it was newly created.
func UpsertGraphEntity(db Database, namespace, kind, name string, aliases []string, attrs map[string]string) (GraphEntity, bool) {
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)
	if db == nil || namespace == "" || name == "" {
		return GraphEntity{}, false
	}
	now := time.Now()
	kind = graphKindOrDefault(kind)

	// Resolve against existing nodes by name first, then any alias — so a
	// later mention under a different alias lands on the same node. One
	// namespace scan serves every probe.
	ents := ListGraphEntities(db, namespace)
	probes := append([]string{name}, aliases...)
	for _, p := range probes {
		if e, ok := matchGraphEntity(ents, p); ok {
			changed := mergeGraphEntity(&e, name, aliases, attrs)
			if changed {
				e.Updated = now
				db.Set(GraphEntityTable, graphEntityKey(namespace, e.ID), e)
			}
			return e, false
		}
	}

	// New node — derive a unique slug ID.
	slug := graphSlug(name)
	if slug == "" {
		slug = UUIDv4()
	}
	id := kind + ":" + slug
	for n := 2; ; n++ {
		if _, exists := getGraphEntity(db, namespace, id); !exists {
			break
		}
		id = kind + ":" + slug + "-" + itoa(n)
	}
	e := GraphEntity{
		Namespace: namespace,
		ID:        id,
		Kind:      kind,
		Name:      name,
		Aliases:   dedupeFold(aliases, name),
		Attrs:     copyAttrs(attrs),
		Created:   now,
		Updated:   now,
	}
	db.Set(GraphEntityTable, graphEntityKey(namespace, id), e)
	// A NEW node is the only event that can push the namespace past the
	// entity cap (merges don't grow it), so the bound is enforced here.
	enforceGraphEntityCap(db, namespace, append(ents, e))
	return e, true
}

// enforceGraphEntityCap evicts least-recently-updated entities until the
// namespace holds at most TunableGraphEntityCap. Eviction is a hard delete
// (DeleteGraphEntity drops the node's edges with it): the graph deliberately
// has no retirement for entities — a crowded-out node earns a log line, not a
// tombstone, because recall_about on a deleted node reads as a clean miss
// rather than resurfacing stale relationships. ents is the caller's
// already-loaded live list (post-insert) so no second scan is needed. No-op
// when the cap is 0 (disabled).
func enforceGraphEntityCap(db Database, namespace string, ents []GraphEntity) {
	limit := TuneInt(TunableGraphEntityCap)
	if limit <= 0 || len(ents) <= limit {
		return
	}
	sort.Slice(ents, func(i, j int) bool {
		return graphStamp(ents[i].Updated, ents[i].Created).Before(graphStamp(ents[j].Updated, ents[j].Created))
	})
	evicted := 0
	for i := 0; i < len(ents)-limit; i++ {
		if DeleteGraphEntity(db, namespace, ents[i].ID) {
			evicted++
		}
	}
	if evicted > 0 {
		Log("[graphstore] entity cap evicted %d least-recently-updated node(s) from %s (limit %d)", evicted, namespace, limit)
	}
}

// enforceGraphEdgeCap evicts least-recently-updated LIVE edges until the
// namespace holds at most TunableGraphEdgeCap. Retired edges are exempt here
// (tombstone retention bounds them). Hard delete, logged — same rationale as
// the entity cap. No-op when the cap is 0.
func enforceGraphEdgeCap(db Database, namespace string) {
	limit := TuneInt(TunableGraphEdgeCap)
	if limit <= 0 {
		return
	}
	edges := scanGraphEdges(db, namespace, func(e GraphEdge) bool { return !e.Retired() })
	if len(edges) <= limit {
		return
	}
	sort.Slice(edges, func(i, j int) bool {
		return graphStamp(edges[i].Updated, edges[i].Created).Before(graphStamp(edges[j].Updated, edges[j].Created))
	})
	evicted := 0
	for i := 0; i < len(edges)-limit; i++ {
		e := edges[i]
		db.Unset(GraphEdgeTable, graphEdgeKey(namespace, e.From, e.Rel, e.To))
		evicted++
	}
	if evicted > 0 {
		Log("[graphstore] edge cap evicted %d least-recently-updated edge(s) from %s (limit %d)", evicted, namespace, limit)
	}
}

// graphStamp is the LRU ordering stamp: Updated, falling back to Created for
// rows written before Updated existed.
func graphStamp(updated, created time.Time) time.Time {
	if !updated.IsZero() {
		return updated
	}
	return created
}

// mergeGraphEntity folds new aliases + attrs into an existing node. Returns
// true if anything changed. The canonical Name is left alone (first writer
// names it); the incoming name is kept as an alias if it differs.
func mergeGraphEntity(e *GraphEntity, name string, aliases []string, attrs map[string]string) bool {
	changed := false
	add := func(a string) {
		a = strings.TrimSpace(a)
		if a == "" || strings.EqualFold(a, e.Name) {
			return
		}
		for _, existing := range e.Aliases {
			if strings.EqualFold(existing, a) {
				return
			}
		}
		e.Aliases = append(e.Aliases, a)
		changed = true
	}
	add(name)
	for _, a := range aliases {
		add(a)
	}
	for k, v := range attrs {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if e.Attrs == nil {
			e.Attrs = map[string]string{}
		}
		if e.Attrs[k] != v {
			e.Attrs[k] = v
			changed = true
		}
	}
	return changed
}

// LinkGraphEdge records From --[rel]--> To within a namespace, creating the
// edge or bumping Updated if the exact triple already exists. When replace is
// true (a single-valued relation being corrected), any OTHER edges with the
// same (From, rel) are removed first — delete-on-update. When false, the edge
// coexists with siblings (multi-valued relations like "knows"). From/To are
// entity IDs; rel is slugged. Returns the stored edge.
// LinkGraphEdge records a live edge with no provenance stamp (the hand-curated
// path, link_entities). LinkGraphEdgeP is the variant that marks origin.
func LinkGraphEdge(db Database, namespace, from, rel, to, note string, replace bool) GraphEdge {
	return LinkGraphEdgeP(db, namespace, from, rel, to, note, replace, MemoryProvenance{})
}

// LinkGraphEdgeP is LinkGraphEdge with an ORIGIN provenance stamp (Source /
// Volatility / AsOf) copied onto the edge — only the origin fields, never
// retirement, so a fresh edge is always born live. Auto-extraction uses it with
// Source=MemSourceObserved so machine-extracted edges can be told apart from the
// hand-curated ones (link_entities) for later review or bulk pruning.
func LinkGraphEdgeP(db Database, namespace, from, rel, to, note string, replace bool, prov MemoryProvenance) GraphEdge {
	namespace = strings.TrimSpace(namespace)
	from = strings.TrimSpace(from)
	to = strings.TrimSpace(to)
	rel = graphRel(rel)
	now := time.Now()
	if db == nil || namespace == "" || from == "" || to == "" || rel == "" {
		return GraphEdge{}
	}
	key := graphEdgeKey(namespace, from, rel, to)
	if replace {
		// Correcting a single-valued relation: tombstone the OTHER values for this
		// (From, rel) as superseded (a validity window) instead of hard-dropping,
		// so recall_about can show the past relationship. The new triple's own key
		// is skipped — it's (re)written live below. Spent tombstones past the
		// retention window are pruned here to keep the lineage bounded.
		prefix := namespace + "/" + from + "|" + rel + "|"
		retDays := TuneInt(TunableFactTombstoneDays)
		cutoff := now.AddDate(0, 0, -retDays)
		for _, k := range db.Keys(GraphEdgeTable) {
			if !strings.HasPrefix(k, prefix) || k == key {
				continue
			}
			var old GraphEdge
			if !db.Get(GraphEdgeTable, k, &old) {
				continue
			}
			if old.Retired() {
				if retDays <= 0 || old.RetiredAt.Before(cutoff) {
					db.Unset(GraphEdgeTable, k) // spent tombstone
				}
				continue
			}
			old.Reason = RetireSuperseded
			old.RetiredAt = now
			old.Successor = to // the entity that replaced this relationship's target
			old.Updated = now
			db.Set(GraphEdgeTable, k, old)
		}
	}
	edge := GraphEdge{Namespace: namespace, From: from, Rel: rel, To: to, Note: strings.TrimSpace(note), Created: now, Updated: now}
	// Stamp ONLY the origin fields — a newly linked edge is always live, so
	// retirement fields on prov (if any) are deliberately ignored here.
	edge.Source = prov.Source
	edge.Volatility = prov.Volatility
	edge.AsOf = prov.AsOf
	// Preserve Created if the exact triple already existed (this is an update).
	var prior GraphEdge
	isNew := true
	if db.Get(GraphEdgeTable, key, &prior) && !prior.Created.IsZero() {
		edge.Created = prior.Created
		isNew = false
		// A re-link must not DOWNGRADE what's already known: an extraction
		// pass re-observing a hand-curated edge was overwriting its origin to
		// observed (losing the trust distinction eviction/pruning keys on) and
		// clobbering its Note with "". Keep the prior Note when the caller
		// brings none, and the prior Source when it outranks the incoming one;
		// AsOf still takes the incoming stamp (re-observation IS a
		// re-confirmation).
		if edge.Note == "" {
			edge.Note = prior.Note
		}
		if sourceTrust(prior.Source) > sourceTrust(edge.Source) {
			edge.Source = prior.Source
		}
	}
	db.Set(GraphEdgeTable, key, edge)
	// Only a NEW triple can push the namespace past the edge cap — rewrites
	// and revivals reuse their key.
	if isNew {
		enforceGraphEdgeCap(db, namespace)
	}
	return edge
}

// GraphEdgesFrom returns a node's outbound edges (prefix scan).
func GraphEdgesFrom(db Database, namespace, from string) []GraphEdge {
	return scanGraphEdges(db, namespace, func(e GraphEdge) bool { return e.From == from })
}

// GraphEdgesTo returns a node's inbound edges (full scan + filter; the graph
// is small in v1, so no reverse index yet).
func GraphEdgesTo(db Database, namespace, to string) []GraphEdge {
	return scanGraphEdges(db, namespace, func(e GraphEdge) bool { return e.To == to })
}

func scanGraphEdges(db Database, namespace string, keep func(GraphEdge) bool) []GraphEdge {
	namespace = strings.TrimSpace(namespace)
	if db == nil || namespace == "" {
		return nil
	}
	prefix := namespace + "/"
	var out []GraphEdge
	for _, k := range db.Keys(GraphEdgeTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		var e GraphEdge
		if db.Get(GraphEdgeTable, k, &e) && !e.Retired() && keep(e) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rel != out[j].Rel {
			return out[i].Rel < out[j].Rel
		}
		return out[i].To < out[j].To
	})
	return out
}

// DeleteGraphEntity removes an entity and EVERY edge touching it (inbound or
// outbound), so the graph can't be left with dangling edges. Returns false if
// the entity didn't exist.
func DeleteGraphEntity(db Database, namespace, id string) bool {
	namespace = strings.TrimSpace(namespace)
	id = strings.TrimSpace(id)
	if db == nil || namespace == "" || id == "" {
		return false
	}
	if _, ok := getGraphEntity(db, namespace, id); !ok {
		return false
	}
	db.Unset(GraphEntityTable, graphEntityKey(namespace, id))
	prefix := namespace + "/"
	for _, k := range db.Keys(GraphEdgeTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		var e GraphEdge
		if db.Get(GraphEdgeTable, k, &e) && (e.From == id || e.To == id) {
			db.Unset(GraphEdgeTable, k)
		}
	}
	return true
}

// DeleteGraphEdge removes one edge by (from, rel, to). rel is slugged so a
// display-form verb ("works at") resolves to the stored key. Returns false
// when no such edge exists.
func DeleteGraphEdge(db Database, namespace, from, rel, to string) bool {
	namespace = strings.TrimSpace(namespace)
	from = strings.TrimSpace(from)
	to = strings.TrimSpace(to)
	rel = graphRel(rel)
	if db == nil || namespace == "" || from == "" || rel == "" || to == "" {
		return false
	}
	key := graphEdgeKey(namespace, from, rel, to)
	if !db.Get(GraphEdgeTable, key, &GraphEdge{}) {
		return false
	}
	db.Unset(GraphEdgeTable, key)
	return true
}

// DeleteGraphEntityAttr removes one attribute key from an entity (so a single
// false key/value can be pruned without dropping the whole node). Returns
// false if the entity or key didn't exist.
func DeleteGraphEntityAttr(db Database, namespace, id, key string) bool {
	namespace = strings.TrimSpace(namespace)
	id = strings.TrimSpace(id)
	key = strings.TrimSpace(key)
	if db == nil || namespace == "" || id == "" || key == "" {
		return false
	}
	e, ok := getGraphEntity(db, namespace, id)
	if !ok {
		return false
	}
	if _, exists := e.Attrs[key]; !exists {
		return false
	}
	delete(e.Attrs, key)
	if len(e.Attrs) == 0 {
		e.Attrs = nil
	}
	e.Updated = time.Now()
	db.Set(GraphEntityTable, graphEntityKey(namespace, id), e)
	return true
}

// DeleteGraphEntityAlias removes one alias from an entity (case-insensitive).
// The canonical Name is never touched here. Returns false if not found.
func DeleteGraphEntityAlias(db Database, namespace, id, alias string) bool {
	namespace = strings.TrimSpace(namespace)
	id = strings.TrimSpace(id)
	alias = strings.TrimSpace(alias)
	if db == nil || namespace == "" || id == "" || alias == "" {
		return false
	}
	e, ok := getGraphEntity(db, namespace, id)
	if !ok {
		return false
	}
	var out []string
	removed := false
	for _, a := range e.Aliases {
		if strings.EqualFold(a, alias) {
			removed = true
			continue
		}
		out = append(out, a)
	}
	if !removed {
		return false
	}
	e.Aliases = out
	e.Updated = time.Now()
	db.Set(GraphEntityTable, graphEntityKey(namespace, id), e)
	return true
}

// GraphCounts returns the number of entities and edges in a namespace — for
// the introspect "Graph: N entities, M edges" line.
func GraphCounts(db Database, namespace string) (entities, edges int) {
	namespace = strings.TrimSpace(namespace)
	if db == nil || namespace == "" {
		return 0, 0
	}
	prefix := namespace + "/"
	for _, k := range db.Keys(GraphEntityTable) {
		if strings.HasPrefix(k, prefix) {
			entities++
		}
	}
	for _, k := range db.Keys(GraphEdgeTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		var e GraphEdge // decode to skip retired (tombstoned) edges from the live count
		if db.Get(GraphEdgeTable, k, &e) && !e.Retired() {
			edges++
		}
	}
	return entities, edges
}

// WipeGraphNamespace removes every entity and edge — live and retired — under
// the namespace. The scope-teardown primitive, paired with
// WipeMemoryFactNamespace: an agent delete (or seed revert) drops the whole
// graph its extraction passes populated, so recreating an agent with the same
// ID never resurrects the old one's relationships. Returns rows removed.
func WipeGraphNamespace(db Database, namespace string) (entities, edges int) {
	namespace = strings.TrimSpace(namespace)
	if db == nil || namespace == "" {
		return 0, 0
	}
	prefix := namespace + "/"
	for _, k := range db.Keys(GraphEntityTable) {
		if strings.HasPrefix(k, prefix) {
			db.Unset(GraphEntityTable, k)
			entities++
		}
	}
	for _, k := range db.Keys(GraphEdgeTable) {
		if strings.HasPrefix(k, prefix) {
			db.Unset(GraphEdgeTable, k)
			edges++
		}
	}
	return entities, edges
}

// RetiredGraphEdgesFrom returns a node's superseded outbound edges — past
// relationships tombstoned by LinkGraphEdge(replace=true) — most-recently retired
// first, for recall surfaces that show history ("previously worked at X"). Empty
// when none.
func RetiredGraphEdgesFrom(db Database, namespace, from string) []GraphEdge {
	namespace = strings.TrimSpace(namespace)
	from = strings.TrimSpace(from)
	if db == nil || namespace == "" || from == "" {
		return nil
	}
	prefix := namespace + "/"
	var out []GraphEdge
	for _, k := range db.Keys(GraphEdgeTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		var e GraphEdge
		if db.Get(GraphEdgeTable, k, &e) && e.Retired() && e.From == from {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RetiredAt.After(out[j].RetiredAt) })
	return out
}

// --- small local helpers (no strconv dependency, matching factstore style) ---

func dedupeFold(in []string, exclude string) []string {
	var out []string
	for _, a := range in {
		a = strings.TrimSpace(a)
		if a == "" || strings.EqualFold(a, exclude) {
			continue
		}
		dup := false
		for _, o := range out {
			if strings.EqualFold(o, a) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, a)
		}
	}
	return out
}

func copyAttrs(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if k = strings.TrimSpace(k); k != "" {
			out[k] = v
		}
	}
	return out
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}
