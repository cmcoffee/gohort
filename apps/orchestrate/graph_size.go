package orchestrate

// How big a memory graph may grow. The store caps every graph (core/graphstore)
// and asks its owner whether one is Large; this answers for the graphs kept
// here: an agent's own, by its GraphSize, and a scoped graph (another app's,
// such as a system's map), by the size that app set on it.

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// GraphSizeLarge is the one size besides standard ("").
const GraphSizeLarge = "large"

// graphSizeTable holds a scoped graph's size, in the scope's own store, keyed
// by the graph's namespace.
const graphSizeTable = "graph_size"

func init() { RegisterGraphSizer(graphIsLarge) }

// graphIsLarge answers for a graph kept here. A scoped graph's own size wins;
// otherwise it is an agent's, sized by the agent. An agent run by someone it
// was shared with writes into their store, where it is not an agent of
// theirs, so that graph stays standard.
func graphIsLarge(db Database, namespace string) bool {
	id, ok := strings.CutPrefix(namespace, "agent:")
	if !ok || db == nil || id == "" {
		return false
	}
	var size string
	if db.Get(graphSizeTable, namespace, &size) {
		return size == GraphSizeLarge
	}
	a, ok := loadAgent(db, id)
	return ok && a.GraphSize == GraphSizeLarge
}

// SetScopedGraphSize sets how big a scoped graph may grow: large, or standard.
func (T *OrchestrateApp) SetScopedGraphSize(scope AgentScope, large bool) {
	if scope.ScopeUser == "" || scope.AgentID == "" {
		return
	}
	db := UserDB(T.DB, scope.ScopeUser)
	if db == nil {
		return
	}
	size := ""
	if large {
		size = GraphSizeLarge
	}
	ns := factsNamespace(scope.AgentID)
	var cur string
	if db.Get(graphSizeTable, ns, &cur) && cur == size {
		return
	}
	db.Set(graphSizeTable, ns, size)
}

// graphSizeArg reads a size as the agent tools take it: "large", or anything
// else for standard.
func graphSizeArg(v string) string {
	if strings.EqualFold(strings.TrimSpace(v), GraphSizeLarge) {
		return GraphSizeLarge
	}
	return ""
}

// graphSizeLabel is a size as the agent tools report it.
func graphSizeLabel(size string) string {
	if size == GraphSizeLarge {
		return GraphSizeLarge
	}
	return "standard"
}

// renderMapPromptSection tells an agent whose graph is Large that building a
// map is its job and that graph memory is where the map lives. The graph tools
// alone did not say so: their descriptions are about people and organisations,
// and an agent mapping a codebase reported what it found in replies and never
// filed it, until somebody reminded it. A graph does not age by date (only the
// cap crowds out what is least used), so it also says to remove a part that is
// gone and to correct a changed connection. Fixed text, so the prompt stays
// cacheable; empty without the graph tools (Explicit Memory off).
func renderMapPromptSection(agent AgentRecord) string {
	if agent.GraphSize != GraphSizeLarge || agent.DisableExplicit {
		return ""
	}
	return "## Your map\n\n" +
		"Your job includes building a map, and your graph memory is where it lives: the parts of what you are mapping (modules, services, databases, files that matter) and how they connect. " +
		"Record each connection with link_entities as soon as you find it, one relationship per call (\"api\" calls \"auth\"; \"orders\" writes to \"orders_db\"), with paths, versions and ports as attributes. " +
		"Before exploring a part, look it up with recall_about: what an earlier run mapped does not need finding again. " +
		"Keep it true as well as full: nothing in it expires, so when a part turns out to be gone, remove it with forget_graph, and when a connection has changed, record the new one with replace=true rather than adding a second. " +
		"The map is what carries over to the next conversation; a finding you only state in a reply is gone after it.\n\n"
}
