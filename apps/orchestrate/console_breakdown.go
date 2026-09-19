package orchestrate

// The shape of the work: every schedule that is part of something larger, drawn
// under the thing it is part of.
//
// A third question, and its own page for that reason. The Scheduler answers
// "what will happen on its own", ordered by when. Goals answers "what am I
// still waiting on", ordered by what needs the owner first. Neither can also
// answer "how does this decompose" without giving up its own ordering: a tree
// puts a child next to its parent, and both of those pages deliberately put
// rows next to other rows for a different reason.
//
// Read-only, like Goals, and for the same reason: every row here is editable
// one page over, and a second set of the same buttons is a second set of rules
// for one record. The one thing this page adds is the arrangement.

import (
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// breakdownNode is one schedule in the forest.
type breakdownNode struct {
	ref      string // "<surface>:<id>", how children name it
	kind     string
	rowID    string // what a console action targets, which for recurring is NOT the ref's id
	name     string
	detail   string
	state    string
	children []*breakdownNode
}

// collectBreakdown reads every schedule this user owns into a forest.
//
// Ownership is the read: each surface is listed from the user's own records, so
// there is nothing to filter out afterwards.
func collectBreakdown(user string) (roots []*breakdownNode, byRef map[string]*breakdownNode) {
	byRef = map[string]*breakdownNode{}
	parentOf := map[string]string{}

	add := func(n *breakdownNode, parent string) {
		if n.ref == "" {
			return
		}
		byRef[n.ref] = n
		if parent = strings.TrimSpace(parent); parent != "" {
			parentOf[n.ref] = parent
		}
	}
	for _, sa := range ListStandingAgents(RootDB, user) {
		add(&breakdownNode{
			ref: taskParentRef(schedKindStanding, sa.Name), kind: schedKindStanding, rowID: sa.Name,
			name: sa.Name, detail: truncateObs(sa.Mission, 90), state: objectiveStateLabel(standingObjective(sa)),
		}, sa.Parent)
	}
	for _, rt := range listAgentRecurringTasks(user, "") {
		p := rt.Payload
		uid := recurringTaskUID(p)
		if uid == "" {
			continue // nothing stable to hang a link on; see recurringTaskUID
		}
		add(&breakdownNode{
			ref: taskParentRef(schedKindRecurring, uid), kind: schedKindRecurring, rowID: rt.TaskID,
			name: recurringName(p), detail: recurringDetail(p), state: objectiveStateLabel(p.objective()),
		}, p.Parent)
	}
	for _, m := range ListEventMonitors(RootDB, user) {
		add(&breakdownNode{
			ref: taskParentRef(schedKindMonitor, m.Name), kind: schedKindMonitor, rowID: m.Name,
			name: m.Name, detail: m.Kind, state: objectiveStateLabel(monitorObjective(m)),
		}, m.Parent)
	}

	// Link up. A parent that does not resolve leaves its child a ROOT rather
	// than dropping it: the work still exists, and a page about the shape of
	// the work that silently omits half of it is worse than one that shows a
	// flat row.
	for ref, parent := range parentOf {
		if p, ok := byRef[parent]; ok {
			p.children = append(p.children, byRef[ref])
		}
	}
	for ref, n := range byRef {
		if parent, has := parentOf[ref]; !has || byRef[parent] == nil {
			roots = append(roots, n)
		}
	}
	sortBreakdown(roots)
	return roots, byRef
}

// sortBreakdown orders siblings by name, recursively. Alphabetical because the
// page is answering a structural question: a list that reorders itself by next
// fire time is one you cannot find the same row in twice.
func sortBreakdown(nodes []*breakdownNode) {
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].name < nodes[j].name })
	for _, n := range nodes {
		sortBreakdown(n.children)
	}
}

// handleConsoleBreakdown serves the forest, depth-first, with each row carrying
// how deep it sits.
//
// Only the parts of the tree that ARE a tree. A schedule with no parent and no
// children is already on the Scheduler, and including it here would make this
// page the Scheduler again with indentation, which answers nothing new.
func (T *OrchestrateApp) handleConsoleBreakdown(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	_ = udb
	roots, _ := collectBreakdown(user)
	type row struct {
		Name   string `json:"Name"`
		Detail string `json:"Detail,omitempty"`
		State  string `json:"State,omitempty"`
		Kind   string `json:"Kind,omitempty"`
		ID     string `json:"_id"`
		RowKnd string `json:"_kind"`
		Depth  int    `json:"_depth,omitempty"`
		Notes  bool   `json:"_notes,omitempty"`
	}
	out := []row{}
	var walk func(n *breakdownNode, depth int)
	walk = func(n *breakdownNode, depth int) {
		out = append(out, row{
			Name: n.name, Detail: n.detail, State: n.state, Kind: breakdownKindLabel(n.kind),
			ID: n.rowID, RowKnd: n.kind, Depth: depth, Notes: true,
		})
		for _, c := range n.children {
			walk(c, depth+1)
		}
	}
	for _, rootNode := range roots {
		if len(rootNode.children) == 0 {
			continue // not part of anything and holding nothing: see above
		}
		walk(rootNode, 0)
	}
	writeJSON(w, out)
}

// breakdownKindLabel names the surface in the reader's terms. The page mixes
// all three, so a row that does not say which it is leaves the reader to infer
// it from a cadence string.
func breakdownKindLabel(kind string) string {
	switch kind {
	case schedKindStanding:
		return "Scheduled agent"
	case schedKindRecurring:
		return "Recurring task"
	case schedKindMonitor:
		return "Event monitor"
	}
	return kind
}
