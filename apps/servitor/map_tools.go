// map_tools.go — asking questions OF the accumulated map, instead of reading it.
//
// Every investigator prompt tells workers that link_entities builds "a topology
// you can traverse, not flat facts on one node." Until now nothing traversed it:
// scopedGraphBlock rendered every entity and edge into the prompt as a blob, and
// that was the only way to see it. Which works — right up until the map stops
// fitting, which is exactly when a map becomes worth having.
//
// These three tools are the traversal the prompts already promised. They matter
// most for the questions that are PATHS rather than lookups:
//
//	"what data sources does this function rely on?"      → neighbors, 2 hops
//	"when I change this setting, what gets called?"      → path, ui field → table
//
// The first time such a question is asked it still costs a full search-and-read
// traversal. What changes is that the hops get RECORDED as they are confirmed,
// so the second question — about that thing, or anything sharing a hop with it —
// starts here instead.
//
// THE MAP IS ADVISORY. It is derived from probes, it goes stale, and it can be
// wrong. Every tool below says so in its own description, because an answer whose
// last citation is "the map says so" is a regression against the discipline the
// rest of servitor enforces.
package servitor

import (
	"fmt"
	"sort"
	"strings"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	. "github.com/cmcoffee/gohort/core"
)

const (
	// mapMaxDepth bounds a neighbors walk. Past three hops on a well-recorded
	// map you are looking at most of it, which is what map_summary is for.
	mapMaxDepth = 3
	// mapMaxNodes caps one traversal's output.
	mapMaxNodes = 60
	// mapPasteEntityCap is the size past which the whole-graph prompt block is
	// replaced by a summary plus these tools. Below it, pasting is the cheapest
	// possible context and no tool call beats it.
	mapPasteEntityCap = 40
)

// mapScope resolves the graph scope for one appliance.
func mapScope(applianceID string) (*orchestrate.OrchestrateApp, orchestrate.AgentScope, bool) {
	orch := servitorOrch()
	if orch == nil || strings.TrimSpace(applianceID) == "" {
		return nil, orchestrate.AgentScope{}, false
	}
	scope := orchestrate.AgentScope{
		AgentID:   servitorInvestigatorAgentID,
		ScopeUser: applianceMemScope(applianceID),
	}
	// Reachability is checked here, once, so every tool below can treat a false
	// return as "we cannot read the map" and say so. Without it an unreadable
	// store answers every lookup with "not recorded", which reports not knowing
	// as knowing the thing is absent.
	if !orch.ScopedGraphAvailable(scope) {
		return nil, orchestrate.AgentScope{}, false
	}
	return orch, scope, true
}

// mapTools builds the traversal kit for one appliance. Available to the worker
// and the lead: the lead uses it to target a dispatch precisely, the worker to
// avoid re-deriving a hop somebody already confirmed.
func mapTools(applianceID string) []AgentToolDef {
	return []AgentToolDef{
		{
			Tool: Tool{
				Name:        "map_find",
				Description: "Look up one thing in the accumulated map by name — a service, file, table, package, route, host. Returns what it is, what is recorded about it, and how many connections it has. Use it to check whether something is already mapped before investigating it from scratch. The map is built from earlier probes: treat it as a starting point and verify anything load-bearing.",
				Parameters: map[string]ToolParam{
					"name": {Type: "string", Description: "The thing's name or a known alias, e.g. \"scheduler\", \"users table\", \"POST /api/settings\"."},
				},
				Required: []string{"name"},
			},
			Handler: func(args map[string]any) (string, error) {
				return mapFind(applianceID, strArg(args, "name"))
			},
		},
		{
			Tool: Tool{
				Name:        "map_neighbors",
				Description: "Show what one thing CONNECTS TO in the accumulated map, following recorded relationships outward. Answers \"what does this rely on\" and \"what uses this\" — both directions are returned, because when something breaks the second question is the one that matters. Use it before searching: a recorded connection costs one call, re-deriving it costs several. Verify anything you will state as fact.",
				Parameters: map[string]ToolParam{
					"name":     {Type: "string", Description: "The thing to start from, by name or alias."},
					"depth":    {Type: "integer", Description: fmt.Sprintf("How many hops to follow (default 1, max %d). Use 2 to see what your neighbours depend on in turn.", mapMaxDepth)},
					"relation": {Type: "string", Description: "Optional: follow only this relationship, e.g. \"calls\", \"reads\", \"imports\". Blank follows all of them."},
				},
				Required: []string{"name"},
			},
			Handler: func(args map[string]any) (string, error) {
				return mapNeighbors(applianceID, strArg(args, "name"),
					clampInt(repoIntArg(args, "depth"), 1, mapMaxDepth), strArg(args, "relation"))
			},
		},
		{
			Tool: Tool{
				Name:        "map_path",
				Description: "Find how one thing reaches another through recorded relationships — the shortest chain from A to B. This is the tool for a trace question (\"when I change this setting, what runs?\"): if the chain was mapped before, it comes back in one call. No path means nobody has recorded one YET, not that none exists — fall back to searching.",
				Parameters: map[string]ToolParam{
					"from": {Type: "string", Description: "Starting thing, by name or alias."},
					"to":   {Type: "string", Description: "Destination thing, by name or alias."},
				},
				Required: []string{"from", "to"},
			},
			Handler: func(args map[string]any) (string, error) {
				return mapPath(applianceID, strArg(args, "from"), strArg(args, "to"))
			},
		},
	}
}

// mapFind renders one entity and its connection count.
func mapFind(applianceID, name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("name is required")
	}
	orch, scope, ok := mapScope(applianceID)
	if !ok {
		return mapUnavailable, nil
	}
	e, found := orch.ScopedGraphEntity(scope, name)
	if !found {
		return fmt.Sprintf("%q is not in the map yet. That means nobody has recorded it — NOT that it does not exist. Search for it, and record what you find with link_entities so the next question starts here.", name), nil
	}
	out, in := orch.ScopedGraphEdges(scope, e.ID)
	var b strings.Builder
	b.WriteString(renderMapEntity(e))
	fmt.Fprintf(&b, "\nConnections: %d outgoing, %d incoming. Use map_neighbors to follow them.\n", len(out), len(in))
	return b.String(), nil
}

// mapNeighbors walks outward from an entity, breadth-first, in both directions.
func mapNeighbors(applianceID, name string, depth int, relation string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("name is required")
	}
	orch, scope, ok := mapScope(applianceID)
	if !ok {
		return mapUnavailable, nil
	}
	start, found := orch.ScopedGraphEntity(scope, name)
	if !found {
		return fmt.Sprintf("%q is not in the map yet, so it has no recorded connections. Search for it instead, and record what you find.", name), nil
	}
	rel := graphRelFilter(relation)

	type hop struct {
		id    string
		depth int
	}
	seen := map[string]bool{start.ID: true}
	queue := []hop{{start.ID, 0}}
	var lines []string
	truncated := false

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.depth >= depth {
			continue
		}
		out, in := orch.ScopedGraphEdges(scope, cur.id)
		for _, ed := range out {
			if !rel(ed.Rel) {
				continue
			}
			lines = append(lines, fmt.Sprintf("%s%s --%s--> %s%s",
				indent(cur.depth), mapName(orch, scope, ed.From), prettyRel(ed.Rel), mapName(orch, scope, ed.To), note(ed.Note)))
			if !seen[ed.To] {
				seen[ed.To] = true
				queue = append(queue, hop{ed.To, cur.depth + 1})
			}
		}
		for _, ed := range in {
			if !rel(ed.Rel) {
				continue
			}
			lines = append(lines, fmt.Sprintf("%s%s <--%s-- %s%s",
				indent(cur.depth), mapName(orch, scope, ed.To), prettyRel(ed.Rel), mapName(orch, scope, ed.From), note(ed.Note)))
			if !seen[ed.From] {
				seen[ed.From] = true
				queue = append(queue, hop{ed.From, cur.depth + 1})
			}
		}
		if len(lines) >= mapMaxNodes {
			truncated = true
			break
		}
	}

	if len(lines) == 0 {
		return fmt.Sprintf("%s is in the map but has no recorded connections%s. Nobody has traced it yet.",
			start.Name, relSuffix(relation)), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Connections from %s (%d hop(s)%s):\n\n", start.Name, depth, relSuffix(relation))
	for _, ln := range dedupeLines(lines) {
		b.WriteString(ln + "\n")
	}
	if truncated {
		fmt.Fprintf(&b, "\nTRUNCATED at %d connections — narrow with `relation`, or reduce `depth`.\n", mapMaxNodes)
	}
	b.WriteString("\nThese are RECORDED relationships from earlier probes, not a live read. Verify anything you will state as fact.\n")
	return b.String(), nil
}

// mapPath finds the shortest recorded chain between two entities, following
// edges in either direction — a trace crosses layers, and whether the recorder
// wrote "handler reads table" or "table read by handler" is an accident of
// phrasing that should not decide whether a path exists.
func mapPath(applianceID, from, to string) (string, error) {
	if strings.TrimSpace(from) == "" || strings.TrimSpace(to) == "" {
		return "", fmt.Errorf("both from and to are required")
	}
	orch, scope, ok := mapScope(applianceID)
	if !ok {
		return mapUnavailable, nil
	}
	src, okA := orch.ScopedGraphEntity(scope, from)
	dst, okB := orch.ScopedGraphEntity(scope, to)
	if !okA || !okB {
		missing := from
		if okA {
			missing = to
		}
		return fmt.Sprintf("%q is not in the map yet, so no recorded path can be found. Trace it by searching, and record each hop with link_entities.", missing), nil
	}
	if src.ID == dst.ID {
		return fmt.Sprintf("%s and %s are the same thing in the map.", src.Name, dst.Name), nil
	}

	type step struct{ prev, via string }
	trail := map[string]step{src.ID: {}}
	queue := []string{src.ID}
	found := false
	for len(queue) > 0 && !found && len(trail) < mapMaxNodes*4 {
		cur := queue[0]
		queue = queue[1:]
		out, in := orch.ScopedGraphEdges(scope, cur)
		next := func(id, via string) {
			if _, been := trail[id]; been {
				return
			}
			trail[id] = step{prev: cur, via: via}
			if id == dst.ID {
				found = true
				return
			}
			queue = append(queue, id)
		}
		for _, ed := range out {
			next(ed.To, prettyRel(ed.Rel))
			if found {
				break
			}
		}
		if found {
			break
		}
		for _, ed := range in {
			next(ed.From, "is "+prettyRel(ed.Rel)+" by")
			if found {
				break
			}
		}
	}
	if !found {
		return fmt.Sprintf("No recorded path from %s to %s. That means nobody has traced this chain YET — not that the two are unconnected. Trace it by searching, recording each hop with link_entities as you confirm it.", src.Name, dst.Name), nil
	}

	// Walk back from the destination.
	var chain []string
	for id := dst.ID; id != src.ID; {
		st := trail[id]
		chain = append([]string{fmt.Sprintf("  --%s--> %s", st.via, mapName(orch, scope, id))}, chain...)
		id = st.prev
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Recorded path from %s to %s:\n\n%s\n", src.Name, dst.Name, src.Name)
	for _, c := range chain {
		b.WriteString(c + "\n")
	}
	b.WriteString("\nThis is the SHORTEST recorded chain, not necessarily the only one, and it was assembled from earlier probes. Verify the hops that matter to your answer.\n")
	return b.String(), nil
}

// --- rendering helpers ---

const mapUnavailable = "The map is unavailable for this system (memory is not wired). Investigate by searching instead."

func renderMapEntity(e GraphEntity) string {
	var b strings.Builder
	b.WriteString(e.Name)
	if e.Kind != "" && e.Kind != "thing" {
		b.WriteString(" (" + e.Kind + ")")
	}
	b.WriteString("\n")
	if len(e.Aliases) > 0 {
		b.WriteString("Also known as: " + strings.Join(e.Aliases, ", ") + "\n")
	}
	if len(e.Attrs) > 0 {
		keys := make([]string, 0, len(e.Attrs))
		for k := range e.Attrs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "- %s: %s\n", k, e.Attrs[k])
		}
	}
	return b.String()
}

// mapName resolves an entity ID to its display name, falling back to the ID so
// an edge pointing at a deleted entity still reads as something.
func mapName(orch *orchestrate.OrchestrateApp, scope orchestrate.AgentScope, id string) string {
	if e, ok := orch.ScopedGraphEntityByID(scope, id); ok && strings.TrimSpace(e.Name) != "" {
		return e.Name
	}
	return id
}

func prettyRel(rel string) string { return strings.ReplaceAll(rel, "_", " ") }

func note(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return "  (" + s + ")"
}

func indent(depth int) string { return strings.Repeat("  ", depth) }

func relSuffix(relation string) string {
	if strings.TrimSpace(relation) == "" {
		return ""
	}
	return ", following only \"" + strings.TrimSpace(relation) + "\""
}

// graphRelFilter builds the relation predicate. An empty filter follows
// everything; a supplied one matches loosely, since a caller types "calls" and
// the recorder wrote "calls_into".
func graphRelFilter(relation string) func(string) bool {
	want := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(relation, " ", "_")))
	if want == "" {
		return func(string) bool { return true }
	}
	return func(rel string) bool {
		r := strings.ToLower(rel)
		return r == want || strings.Contains(r, want) || strings.Contains(want, r)
	}
}

// dedupeLines drops repeats while keeping order. A bidirectional walk reaches
// the same edge from both ends, and printing it twice reads as two facts.
func dedupeLines(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// mapIsLarge reports whether the appliance's map has outgrown pasting into the
// prompt. Below the cap the whole graph is the cheapest context there is; above
// it, the block becomes a summary and the tools do the rest.
func mapIsLarge(applianceID string) (large bool, entities, edges int) {
	orch, scope, ok := mapScope(applianceID)
	if !ok {
		return false, 0, 0
	}
	entities, edges = orch.ScopedGraphCounts(scope)
	return entities > mapPasteEntityCap, entities, edges
}

// scopedGraphPromptBlock is what the prompts render: the whole map while it
// fits, and a summary plus a pointer at the traversal tools once it does not.
func scopedGraphPromptBlock(a Appliance) string {
	large, ents, edges := mapIsLarge(a.ID)
	if !large {
		return scopedGraphBlock(a)
	}
	return fmt.Sprintf("This system's map has %d recorded things and %d relationships — too many to list here. "+
		"Use `map_find` to look one up, `map_neighbors` to see what it connects to, and `map_path` to find how one thing reaches another. "+
		"It is built from earlier probes, so treat it as a starting point and verify anything load-bearing.\n", ents, edges)
}
