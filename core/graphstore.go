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
	want := strings.ToLower(strings.TrimSpace(nameOrAlias))
	if db == nil || namespace == "" || want == "" {
		return GraphEntity{}, false
	}
	for _, e := range ListGraphEntities(db, namespace) {
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
	// later mention under a different alias lands on the same node.
	probes := append([]string{name}, aliases...)
	for _, p := range probes {
		if e, ok := FindGraphEntity(db, namespace, p); ok {
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
	return e, true
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
func LinkGraphEdge(db Database, namespace, from, rel, to, note string, replace bool) GraphEdge {
	namespace = strings.TrimSpace(namespace)
	from = strings.TrimSpace(from)
	to = strings.TrimSpace(to)
	rel = graphRel(rel)
	now := time.Now()
	if db == nil || namespace == "" || from == "" || to == "" || rel == "" {
		return GraphEdge{}
	}
	if replace {
		// Drop existing values for this (From, rel) pair before writing the
		// new one. Prefix scan over "<namespace>/<from>|<rel>|".
		prefix := namespace + "/" + from + "|" + rel + "|"
		for _, k := range db.Keys(GraphEdgeTable) {
			if strings.HasPrefix(k, prefix) {
				db.Unset(GraphEdgeTable, k)
			}
		}
	}
	key := graphEdgeKey(namespace, from, rel, to)
	edge := GraphEdge{Namespace: namespace, From: from, Rel: rel, To: to, Note: strings.TrimSpace(note), Created: now, Updated: now}
	// Preserve Created if the exact triple already existed (this is an update).
	var prior GraphEdge
	if db.Get(GraphEdgeTable, key, &prior) && !prior.Created.IsZero() {
		edge.Created = prior.Created
	}
	db.Set(GraphEdgeTable, key, edge)
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
		if db.Get(GraphEdgeTable, k, &e) && keep(e) {
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
		if strings.HasPrefix(k, prefix) {
			edges++
		}
	}
	return entities, edges
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
