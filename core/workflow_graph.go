// A picture of a workflow — shared by machines and pipelines.
//
// The renderer knows nothing about either. It takes a WorkflowGraph of
// nodes and edges, and each def supplies a small adapter that produces
// one (MachineDef.Graph, and PipelineDef.Graph when that lands). That
// split is the whole design: a machine-shaped renderer would be cheap to
// write today and expensive to un-write the first time a pipeline wanted
// the same picture, which is the leak core/ui has already paid for twice.
//
// Server-side SVG rather than a JS graph library: deterministic output
// (the same def renders byte-identically, so a diff of two renders is
// signal), unit-testable in Go, no dependency, and nothing to verify in
// a browser beyond "the image appeared".
//
// See docs/workflow-graph.md.

package core

import (
	"sort"
	"strconv"
	"strings"
)

// Node kinds. The renderer needs to know which nodes HOLD and which are
// passed through, and deliberately not why: a machine's resident phase
// and a pipeline's terminal stage are drawn heavier for the same reason,
// without either vocabulary reaching this file.
const (
	NodeEntry = "entry" // where a run or session begins
	NodeStep  = "step"  // passed through
	NodeRest  = "rest"  // holds — a turn can end here
	NodeExit  = "exit"  // terminal
)

// Edge styles.
const (
	EdgeSolid  = "solid"  // an unconditional handoff
	EdgeDashed = "dashed" // conditional / decided at run time
	EdgeBack   = "back"   // returns to an earlier node
)

// WorkflowNode is one box.
type WorkflowNode struct {
	ID    string   `json:"id"`
	Label string   `json:"label"`
	Note  string   `json:"note,omitempty"` // one line under the label
	Kind  string   `json:"kind,omitempty"`
	Tags  []string `json:"tags,omitempty"` // small annotations: "lead", "guarded"

	// Href, when set, wraps the node in a link. The graph stays a data
	// structure about shape — WHERE a node links is the caller's
	// knowledge (an editor's section anchor, a detail page), so the
	// adapter that builds the graph does not set this; the surface that
	// renders it does.
	Href string `json:"href,omitempty"`
}

// WorkflowEdge is one arrow.
type WorkflowEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label,omitempty"`
	Style string `json:"style,omitempty"`
	Note  string `json:"note,omitempty"` // hover text
}

// WorkflowGraph is what a def compiles to for drawing.
type WorkflowGraph struct {
	Title string         `json:"title,omitempty"`
	Entry string         `json:"entry,omitempty"`
	Nodes []WorkflowNode `json:"nodes"`
	Edges []WorkflowEdge `json:"edges"`
	// Legend holds statements that are true of the whole graph but
	// cannot be drawn. The canonical one: a machine's change_phase
	// connects every node to every other, so rendering it produces a
	// complete graph and destroys the picture. It is a sentence, not
	// forty arrows.
	Legend []string `json:"legend,omitempty"`
}

// WorkflowOverlay marks a graph with what a live run has actually done.
//
// This is what makes the picture a debugging surface rather than
// documentation. The structure alone says what COULD happen, which is
// already in the def; the overlay says what did, and how often, which is
// otherwise only readable as a chronological wall of sentences.
type WorkflowOverlay struct {
	Current string         // node the run is sitting in
	Fired   map[string]int // edge key (see EdgeKey) → times taken
}

// EdgeKey identifies one edge for overlay lookup.
func EdgeKey(from, to string) string { return from + "\x00" + to }

// Layout constants. Fixed rather than measured: a server has no font
// metrics, and a stable box size is what keeps the output deterministic.
const (
	gNodeW = 210
	gNodeH = 58
	gHGap  = 26
	// gVGap is the row PITCH (top of one row to top of the next), so the
	// visible gap between rows is gVGap−gNodeH. At the old 66 that gap
	// was 8px: a forward hop rendered as a stub shorter than its own
	// 10px arrowhead, and an edge label had no lane of its own. 96
	// leaves 38px — an arrow that reads as an arrow.
	gVGap   = 96
	gPad    = 20
	gCharW  = 6.1 // approximate advance at the label font size, for truncation
	gLabelC = 30  // characters before a label is truncated
	gNoteC  = 40
)

// SVG renders the graph. The result is a complete <svg> document, safe
// to inject inline (every dynamic string is XML-escaped and no attribute
// takes user text), which is what lets the page's CSS variables theme it.
//
// Colors reference CSS variables with literal fallbacks, so the same
// image reads correctly inline in a themed page AND standalone in a tab.
func (g WorkflowGraph) SVG(overlay *WorkflowOverlay) string {
	pos, ranks := g.layout()
	width := gPad*2 + maxRankWidth(g, ranks)
	height := gPad*2 + len(ranks)*gNodeH + maxInt(len(ranks)-1, 0)*(gVGap-gNodeH)
	if height < gNodeH+gPad*2 {
		height = gNodeH + gPad*2
	}
	// Back edges bow out to the right; give them room rather than
	// letting them clip at the viewBox edge.
	if g.hasBackEdge(pos) {
		width += 46
	}

	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ` +
		strconv.Itoa(width) + ` ` + strconv.Itoa(height) + `" width="` + strconv.Itoa(width) + `" height="` + strconv.Itoa(height) +
		`" role="img" aria-label="` + xmlEscape(chooseStr(g.Title, "workflow")) + `" font-family="system-ui,-apple-system,Segoe UI,sans-serif">`)
	b.WriteString(svgDefs())

	// Edges first so boxes sit on top of the arrows.
	for _, e := range g.Edges {
		from, okF := pos[e.From]
		to, okT := pos[e.To]
		if !okF || !okT {
			continue // an edge to a node that isn't drawn; adapters shouldn't, but don't crash
		}
		b.WriteString(g.edgeSVG(e, from, to, overlay))
	}
	for _, n := range g.Nodes {
		p, ok := pos[n.ID]
		if !ok {
			continue
		}
		b.WriteString(nodeSVG(n, p, overlay))
	}
	b.WriteString(`</svg>`)
	return b.String()
}

// point is a node's top-left corner.
type point struct{ X, Y int }

// layout ranks nodes by distance from the entry and spreads each rank
// across a row.
//
// Layered rather than force-directed because determinism outranks
// beauty here: the same def must produce the same picture every time or
// a golden test is worthless and a visual diff is noise. Rows rather
// than columns because phase names are words, and words are wider than
// they are tall.
func (g WorkflowGraph) layout() (map[string]point, [][]string) {
	rank := map[string]int{}
	order := map[string]int{}
	for i, n := range g.Nodes {
		order[n.ID] = i
	}
	// BFS from the entry over FORWARD edges only. A back edge is defined
	// as one that lands on an already-ranked node, which is exactly the
	// cycle-breaking this needs: whichever path reaches a node first owns
	// its rank, and declaration order makes "first" deterministic.
	start := g.Entry
	if start == "" && len(g.Nodes) > 0 {
		start = g.Nodes[0].ID
	}
	queue := []string{}
	if start != "" {
		rank[start] = 0
		queue = append(queue, start)
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		// Deterministic expansion: edges in declared order.
		for _, e := range g.Edges {
			if e.From != cur || e.To == cur {
				continue
			}
			if _, seen := rank[e.To]; seen {
				continue
			}
			rank[e.To] = rank[cur] + 1
			queue = append(queue, e.To)
		}
	}
	// Anything unreachable still gets drawn, in a row of its own below
	// the reachable graph. An orphan phase is a real authoring mistake
	// and hiding it would make the picture lie about the def.
	maxRank := 0
	for _, r := range rank {
		if r > maxRank {
			maxRank = r
		}
	}
	for _, n := range g.Nodes {
		if _, ok := rank[n.ID]; !ok {
			rank[n.ID] = maxRank + 1
		}
	}

	rows := map[int][]string{}
	top := 0
	for _, n := range g.Nodes {
		r := rank[n.ID]
		rows[r] = append(rows[r], n.ID)
		if r > top {
			top = r
		}
	}
	ranks := make([][]string, top+1)
	for r := 0; r <= top; r++ {
		ids := rows[r]
		sort.SliceStable(ids, func(i, j int) bool { return order[ids[i]] < order[ids[j]] })
		ranks[r] = ids
	}

	widest := 0
	for _, ids := range ranks {
		if w := rowWidth(len(ids)); w > widest {
			widest = w
		}
	}
	pos := map[string]point{}
	for r, ids := range ranks {
		rw := rowWidth(len(ids))
		x0 := gPad + (widest-rw)/2
		for i, id := range ids {
			pos[id] = point{X: x0 + i*(gNodeW+gHGap), Y: gPad + r*gVGap}
		}
	}
	return pos, ranks
}

func rowWidth(n int) int {
	if n <= 0 {
		return 0
	}
	return n*gNodeW + (n-1)*gHGap
}

func maxRankWidth(g WorkflowGraph, ranks [][]string) int {
	widest := 0
	for _, ids := range ranks {
		if w := rowWidth(len(ids)); w > widest {
			widest = w
		}
	}
	return widest
}

func (g WorkflowGraph) hasBackEdge(pos map[string]point) bool {
	for _, e := range g.Edges {
		f, okF := pos[e.From]
		t, okT := pos[e.To]
		if okF && okT && (e.From == e.To || t.Y <= f.Y) {
			return true
		}
	}
	return false
}

// nodeSVG draws one box.
func nodeSVG(n WorkflowNode, p point, overlay *WorkflowOverlay) string {
	current := overlay != nil && overlay.Current == n.ID
	// A node that HOLDS is drawn heavier than one passed through: that
	// is the single most useful thing to see at a glance, because it is
	// where a conversation can actually be sitting.
	stroke := "var(--border,#d4d4d8)"
	strokeW := "1"
	fill := "var(--surface,#ffffff)"
	switch n.Kind {
	case NodeRest, NodeExit, NodeEntry:
		strokeW = "2"
	}
	if n.Kind == NodeEntry {
		stroke = "var(--accent,#6366f1)"
	}
	if current {
		stroke = "var(--accent,#6366f1)"
		strokeW = "3"
		fill = "var(--accent-soft,rgba(99,102,241,0.10))"
	}

	var b strings.Builder
	// Tagged with the node's own id so a surface rendering this INLINE
	// can mark which one the reader is on — a map is worth much more
	// when it says "you are here". Inert in a standalone document.
	b.WriteString(`<g data-node="` + xmlEscape(n.ID) + `">`)
	b.WriteString(`<rect x="` + strconv.Itoa(p.X) + `" y="` + strconv.Itoa(p.Y) + `" width="` + strconv.Itoa(gNodeW) +
		`" height="` + strconv.Itoa(gNodeH) + `" rx="7" fill="` + fill + `" stroke="` + stroke + `" stroke-width="` + strokeW + `"/>`)
	if n.Note != "" || len(n.Tags) > 0 {
		b.WriteString(`<title>` + xmlEscape(n.Label+tagSuffix(n.Tags)+noteSuffix(n.Note)) + `</title>`)
	}
	tx := p.X + 12
	b.WriteString(`<text x="` + strconv.Itoa(tx) + `" y="` + strconv.Itoa(p.Y+22) +
		`" font-size="12.5" font-weight="600" fill="var(--text,#18181b)">` +
		xmlEscape(gTrunc(n.Label, gLabelC)) + `</text>`)
	if n.Note != "" {
		b.WriteString(`<text x="` + strconv.Itoa(tx) + `" y="` + strconv.Itoa(p.Y+38) +
			`" font-size="10.5" fill="var(--text-mute,#71717a)">` +
			xmlEscape(gTrunc(n.Note, gNoteC)) + `</text>`)
	}
	if len(n.Tags) > 0 {
		b.WriteString(`<text x="` + strconv.Itoa(tx) + `" y="` + strconv.Itoa(p.Y+51) +
			`" font-size="9.5" fill="var(--text-mute,#71717a)">` +
			xmlEscape(gTrunc(strings.Join(n.Tags, " · "), gNoteC)) + `</text>`)
	}
	b.WriteString(`</g>`)
	if n.Href != "" {
		// A plain SVG2 <a>: works inline in a page and in a standalone
		// SVG document alike, and inherits nothing surprising. The
		// cursor style is what tells a person the box is a door.
		return `<a href="` + xmlEscape(n.Href) + `" style="cursor:pointer">` + b.String() + `</a>`
	}
	return b.String()
}

// edgeSVG draws one arrow, choosing a route from the two nodes'
// positions rather than from what the adapter called it: a "back" edge
// is whatever actually points upward once laid out.
func (g WorkflowGraph) edgeSVG(e WorkflowEdge, from, to point, overlay *WorkflowOverlay) string {
	fired := 0
	if overlay != nil {
		fired = overlay.Fired[EdgeKey(e.From, e.To)]
	}
	stroke := "var(--border-strong,#a1a1aa)"
	width := "1.4"
	opacity := "1"
	if e.Style == EdgeDashed || e.Style == EdgeBack {
		opacity = "0.75"
	}
	if overlay != nil {
		// With an overlay in play, edges that have never fired recede so
		// the path the conversation actually took is what reads first.
		if fired > 0 {
			stroke = "var(--accent,#6366f1)"
			width = "2.2"
			opacity = "1"
		} else {
			opacity = "0.3"
		}
	}
	dash := ""
	if e.Style == EdgeDashed {
		dash = ` stroke-dasharray="5 4"`
	} else if e.Style == EdgeBack {
		dash = ` stroke-dasharray="2 4"`
	}
	marker := "url(#wf-arrow)"
	if fired > 0 {
		marker = "url(#wf-arrow-hot)"
	}

	var d string
	var lx, ly int
	switch {
	case e.From == e.To:
		// "Stays here" is drawn as a badge INSIDE the node's top-right
		// corner rather than a loop hanging off its side.
		//
		// A loop bowing outward collides with whatever shares the row —
		// on the canonical machine it drew straight through the
		// neighbouring phase — and a self-loop carries no routing
		// information that needs space. It is a property of the node.
		cx := from.X + gNodeW - 17
		cy := from.Y + 15
		d = "M" + strconv.Itoa(cx-6) + " " + strconv.Itoa(cy+3) +
			" A 6 6 0 1 1 " + strconv.Itoa(cx+2) + " " + strconv.Itoa(cy+5)
		lx, ly = 0, 0
	case to.Y > from.Y:
		// Forward: bottom of one to top of the next.
		x1, y1 := from.X+gNodeW/2, from.Y+gNodeH
		x2, y2 := to.X+gNodeW/2, to.Y
		mid := (y1 + y2) / 2
		d = "M" + strconv.Itoa(x1) + " " + strconv.Itoa(y1) +
			" C" + strconv.Itoa(x1) + " " + strconv.Itoa(mid) + " " + strconv.Itoa(x2) + " " + strconv.Itoa(mid) + " " + strconv.Itoa(x2) + " " + strconv.Itoa(y2)
		lx, ly = (x1+x2)/2+6, mid
	default:
		// Backward or sideways: bow out to the right, into the target's
		// right edge. Kept off the node column so it never crosses a box.
		x1, y1 := from.X+gNodeW, from.Y+gNodeH/2
		x2, y2 := to.X+gNodeW, to.Y+gNodeH/2
		bow := maxInt(x1, x2) + 38
		d = "M" + strconv.Itoa(x1) + " " + strconv.Itoa(y1) +
			" C" + strconv.Itoa(bow) + " " + strconv.Itoa(y1) + " " + strconv.Itoa(bow) + " " + strconv.Itoa(y2) + " " + strconv.Itoa(x2) + " " + strconv.Itoa(y2)
		// Nudged off the rank boundary. A forward edge between the same
		// two rows puts its label at exactly this midpoint, and on a
		// machine where a guard returns across the same gap the two
		// landed on top of each other — legible in neither direction.
		lx, ly = bow-4, (y1+y2)/2+13
	}

	var b strings.Builder
	b.WriteString(`<path d="` + d + `" fill="none" stroke="` + stroke + `" stroke-width="` + width +
		`" opacity="` + opacity + `"` + dash + ` marker-end="` + marker + `"/>`)
	if title := edgeTitle(e, fired); title != "" {
		b.WriteString(`<title>` + xmlEscape(title) + `</title>`)
	}
	label := e.Label
	if fired > 1 {
		label = chooseStr(label, "taken") + " ×" + strconv.Itoa(fired)
	}
	if e.From == e.To {
		label = "" // the badge speaks for itself; its <title> carries the detail
	}
	if label != "" {
		b.WriteString(`<text x="` + strconv.Itoa(lx) + `" y="` + strconv.Itoa(ly) +
			`" font-size="9.5" fill="var(--text-mute,#71717a)" opacity="` + opacity + `">` +
			xmlEscape(gTrunc(label, 22)) + `</text>`)
	}
	return b.String()
}

func edgeTitle(e WorkflowEdge, fired int) string {
	parts := []string{}
	if e.Note != "" {
		parts = append(parts, e.Note)
	}
	switch {
	case fired == 1:
		parts = append(parts, "taken once in this conversation")
	case fired > 1:
		parts = append(parts, "taken "+strconv.Itoa(fired)+" times in this conversation")
	}
	return strings.Join(parts, " — ")
}

func svgDefs() string {
	return `<defs>` +
		`<marker id="wf-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">` +
		`<path d="M0 0 L10 5 L0 10 z" fill="var(--border-strong,#a1a1aa)"/></marker>` +
		`<marker id="wf-arrow-hot" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse">` +
		`<path d="M0 0 L10 5 L0 10 z" fill="var(--accent,#6366f1)"/></marker>` +
		`</defs>`
}

// --- small helpers ----------------------------------------------------

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func tagSuffix(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	return " (" + strings.Join(tags, ", ") + ")"
}

func noteSuffix(note string) string {
	if note == "" {
		return ""
	}
	return " — " + note
}

// gTrunc caps a label by RUNE count, so a multi-byte name is cut where it
// looks cut rather than mid-character.
//
// Distinct from agent_loop's truncateRunes, which appends a whole sentence
// explaining itself to a model. This one has 210 pixels to work with.
func gTrunc(s string, max int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max < 2 {
		return string(r[:max])
	}
	return strings.TrimRight(string(r[:max-1]), " ") + "…"
}

// xmlEscape makes a string safe as SVG text content.
//
// Every dynamic string in the output goes through here. Phase names and
// descriptions are authored by users and by models, the result is meant
// to be injected inline so the page's theme reaches it, and inline
// injection of unescaped text is how a picture becomes an XSS vector.
func xmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return r.Replace(s)
}
