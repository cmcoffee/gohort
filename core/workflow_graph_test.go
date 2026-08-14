package core

// The workflow graph: the machine adapter, the layered layout, and the
// SVG renderer. All pure — a def in, a string out — which is the point
// of rendering server-side instead of handing a JS library some JSON.

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestMachineGraph_ShapeOfTheCanonicalMachine(t *testing.T) {
	g := guardedMachine().Graph()

	if g.Entry != "answer" {
		t.Errorf("entry should be the start phase, got %q", g.Entry)
	}
	byID := map[string]WorkflowNode{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}
	if len(byID) != 2 {
		t.Fatalf("expected one node per phase, got %d", len(byID))
	}
	// The start phase is resident here, and entry wins: where a session
	// begins is the more useful thing to see.
	if byID["answer"].Kind != NodeEntry {
		t.Errorf("the start phase should read as the entry, got %q", byID["answer"].Kind)
	}
	if byID["decompose"].Kind != NodeStep {
		t.Errorf("a transient phase is passed through, got %q", byID["decompose"].Kind)
	}
	if byID["decompose"].Note != "Split the question up." {
		t.Errorf("the phase description should ride along, got %q", byID["decompose"].Note)
	}
	// Tags surface the settings that change behaviour and are otherwise
	// invisible on a picture.
	tags := strings.Join(byID["answer"].Tags, " ")
	if !strings.Contains(tags, "guarded") {
		t.Errorf("a guarded phase should say so, got %v", byID["answer"].Tags)
	}
	if !strings.Contains(strings.Join(byID["decompose"].Tags, " "), "parts") {
		t.Errorf("a declared output should show on the node, got %v", byID["decompose"].Tags)
	}

	var forward, guard bool
	for _, e := range g.Edges {
		if e.From == "decompose" && e.To == "answer" && e.Style == EdgeSolid {
			forward = true
		}
		if e.From == "answer" && e.To == "decompose" && e.Style == EdgeBack {
			guard = true
		}
	}
	if !forward {
		t.Error("the transient handoff should be a solid edge")
	}
	if !guard {
		t.Error("the guard should be drawn as an edge back to guard_to")
	}
	// change_phase connects everything to everything, so it is a sentence
	// rather than an arrow.
	if len(g.Legend) == 0 || !strings.Contains(strings.Join(g.Legend, " "), "change_phase") {
		t.Errorf("the legend should carry what cannot be drawn, got %v", g.Legend)
	}
}

func TestMachineGraph_RouterDrawsEveryPossibleTarget(t *testing.T) {
	// next_from picks its target at run time. Drawing only a guessed
	// subset would show a graph the machine does not have, which is the
	// exact failure a picture is meant to prevent.
	g := triageMachine().Graph()
	targets := map[string]string{}
	for _, e := range g.Edges {
		if e.From == "route" {
			targets[e.To] = e.Style
		}
	}
	for _, want := range []string{"decompose", "answer", "deep"} {
		if _, ok := targets[want]; !ok {
			t.Errorf("the router should be able to reach %s", want)
		}
	}
	if targets["deep"] != EdgeDashed {
		t.Errorf("a run-time route should be dashed, got %q", targets["deep"])
	}
	// The static fallback usually names one of the same targets. Two
	// edges between one pair land on identical curves with their labels
	// stacked, so the two facts merge into one arrow that states both
	// (found by looking at the rendered output, not by reasoning about it).
	var merged int
	for _, e := range g.Edges {
		if e.From == "route" && e.To == "answer" {
			merged++
			if !strings.Contains(e.Label, "fallback") {
				t.Errorf("the merged edge should still say it is the fallback, got %q", e.Label)
			}
		}
	}
	if merged != 1 {
		t.Errorf("expected exactly one route→answer edge, got %d", merged)
	}
}

func TestMachineGraph_ResidentPhaseStaysOrHandsOff(t *testing.T) {
	// "The conversation lives here" is the most important fact about a
	// resident phase, and an absence of arrows does not say it.
	g := triageMachine().Graph()
	var loop bool
	for _, e := range g.Edges {
		if e.From == "answer" && e.To == "answer" {
			loop = true
		}
	}
	if !loop {
		t.Error("a resident phase with no next should draw a self-loop")
	}

	oneBeat := MachineDef{Name: "intake", Start: "intake", Phases: []MachinePhase{
		{Name: "intake", Resident: true, Prompt: "ask", Next: "work"},
		{Name: "work", Resident: true, Prompt: "do"},
	}}
	var handoff WorkflowEdge
	for _, e := range oneBeat.Graph().Edges {
		if e.From == "intake" && e.To == "work" {
			handoff = e
		}
	}
	if handoff.Label != "after one turn" {
		t.Errorf("a one-beat handoff is materially different from a transient one and should say so, got %q", handoff.Label)
	}
}

// --- layout -----------------------------------------------------------

func TestGraphLayout_RanksFromTheEntryAndBreaksCycles(t *testing.T) {
	// The guard makes this cyclic. A layered layout has to terminate and
	// produce one row per rank anyway.
	g := triageMachine().Graph()
	pos, ranks := g.layout()

	if len(pos) != len(g.Nodes) {
		t.Fatalf("every node needs a position, got %d for %d nodes", len(pos), len(g.Nodes))
	}
	if len(ranks) == 0 {
		t.Fatal("expected at least one rank")
	}
	if ranks[0][0] != "decompose" {
		t.Errorf("rank 0 should be the entry, got %v", ranks[0])
	}
	if pos["decompose"].Y >= pos["route"].Y {
		t.Error("a later phase should sit below an earlier one")
	}
	// answer and deep are both reachable from route in one hop, so they
	// share a rank and sit side by side.
	if pos["answer"].Y != pos["deep"].Y {
		t.Error("phases at the same distance from the entry should share a row")
	}
	if pos["answer"].X == pos["deep"].X {
		t.Error("nodes sharing a row must not overlap")
	}
}

func TestGraphLayout_OrphanPhaseIsStillDrawn(t *testing.T) {
	// An unreachable phase is a real authoring mistake. Hiding it would
	// make the picture lie about the def.
	def := MachineDef{Name: "orphan", Start: "a", Phases: []MachinePhase{
		{Name: "a", Resident: true, Prompt: "x"},
		{Name: "stranded", Prompt: "y", Next: "a"},
	}}
	g := def.Graph()
	pos, _ := g.layout()
	if _, ok := pos["stranded"]; !ok {
		t.Error("an unreachable phase must still be positioned")
	}
	if !strings.Contains(g.SVG(nil), "stranded") {
		t.Error("and drawn")
	}
}

func TestGraphLayout_IsDeterministic(t *testing.T) {
	// Determinism is the reason for a layered layout over anything
	// prettier: a golden test is worthless and a visual diff is noise
	// without it. Map iteration order is the thing being guarded here.
	def := triageMachine()
	want := def.Graph().SVG(nil)
	for i := 0; i < 25; i++ {
		if got := def.Graph().SVG(nil); got != want {
			t.Fatalf("render %d differs from the first", i)
		}
	}
}

// --- SVG --------------------------------------------------------------

func TestGraphSVG_StructureAndEscaping(t *testing.T) {
	def := MachineDef{Name: "escaping", Start: "a", Phases: []MachinePhase{
		{Name: "a", Resident: true, Prompt: "x", Desc: `Tom & Jerry's <script>alert(1)</script>`},
	}}
	svg := def.Graph().SVG(nil)

	if !strings.HasPrefix(svg, "<svg ") || !strings.HasSuffix(svg, "</svg>") {
		t.Error("expected a complete svg document")
	}
	if !strings.Contains(svg, `viewBox="0 0 `) {
		t.Error("a viewBox is what lets the picture scale in a modal")
	}
	// The result is injected inline so the page's theme reaches it, which
	// makes escaping load-bearing rather than cosmetic. Phase text is
	// authored by users and by models.
	if strings.Contains(svg, "<script>") {
		t.Fatal("unescaped markup reached the output — this is injected inline")
	}
	if !strings.Contains(svg, "&lt;script&gt;") || !strings.Contains(svg, "Tom &amp; Jerry") {
		t.Errorf("expected escaped text, got: %s", svg)
	}
	// Themed, not baked: the same image has to read on light and dark.
	if !strings.Contains(svg, "var(--text") || !strings.Contains(svg, "var(--surface") {
		t.Error("colors should reference CSS variables with fallbacks")
	}
}

// A guard returning across the same rank gap a forward edge crosses put
// both labels on one baseline, on top of each other. Found by rendering
// a real machine and reading the coordinates — the only way this class
// of fault shows up.
func TestGraphSVG_EdgeLabelsDoNotCollide(t *testing.T) {
	def := MachineDef{Name: "collide", Start: "triage", Phases: []MachinePhase{
		{Name: "triage", Prompt: "x", NextFrom: "to", Next: "answer",
			Output: []PipelineField{{Name: "to", Type: FieldString}}},
		{Name: "verify", Prompt: "y", Resident: true, Guard: "moved on", GuardTo: "triage"},
		{Name: "answer", Prompt: "z", Resident: true},
	}}
	svg := def.Graph().SVG(nil)

	type placed struct {
		x    int
		text string
	}
	re := regexp.MustCompile(`<text x="(\d+)" y="(\d+)" font-size="9.5"[^>]*>([^<]*)</text>`)
	byRow := map[int][]placed{}
	for _, m := range re.FindAllStringSubmatch(svg, -1) {
		x, _ := strconv.Atoi(m[1])
		y, _ := strconv.Atoi(m[2])
		byRow[y] = append(byRow[y], placed{x, m[3]})
	}
	for y, items := range byRow {
		sort.Slice(items, func(i, j int) bool { return items[i].x < items[j].x })
		for i := 1; i < len(items); i++ {
			// ~5px per character at this size, the same approximation the
			// renderer works in.
			if end := items[i-1].x + len(items[i-1].text)*5; end > items[i].x {
				t.Errorf("labels overlap on baseline y=%d: %q ends at %d, %q starts at %d",
					y, items[i-1].text, end, items[i].text, items[i].x)
			}
		}
	}
}

func TestGraphSVG_OverlayMarksWhereTheConversationHasBeen(t *testing.T) {
	def := triageMachine()
	plain := def.Graph().SVG(nil)

	cur := MachineCursor{Phase: "deep", Log: []PhaseHop{
		{From: "decompose", To: "route"},
		{From: "route", To: "deep"},
		{From: "deep", To: "decompose"},
		{From: "decompose", To: "route"},
	}}
	live := def.Graph().SVG(cur.Overlay())

	if live == plain {
		t.Fatal("an overlay should change the picture")
	}
	// A repeated edge carries its count: a guard that has tripped four
	// times is the shape of a resident phase scoped too narrowly.
	if !strings.Contains(live, "×2") {
		t.Errorf("expected a repeat count on the twice-taken edge:\n%s", live)
	}
	// The current node is marked, and untaken edges recede so the path
	// actually walked is what reads first.
	if !strings.Contains(live, `stroke-width="3"`) {
		t.Error("the current node should be marked")
	}
	if !strings.Contains(live, `opacity="0.3"`) {
		t.Error("edges that never fired should recede")
	}
	if !strings.Contains(live, "wf-arrow-hot") {
		t.Error("fired edges should use the highlighted arrowhead")
	}
}

func TestCursorOverlay_CountsEdgesNotHops(t *testing.T) {
	cur := MachineCursor{Phase: "answer", Log: []PhaseHop{
		{From: "a", To: "b"}, {From: "a", To: "b"}, {From: "b", To: "a"},
	}}
	o := cur.Overlay()
	if o.Current != "answer" {
		t.Errorf("current should come from the cursor, got %q", o.Current)
	}
	if o.Fired[EdgeKey("a", "b")] != 2 || o.Fired[EdgeKey("b", "a")] != 1 {
		t.Errorf("edge counts wrong: %v", o.Fired)
	}
}

func TestPhaseLog_RecordsEveryTransitionBounded(t *testing.T) {
	// The overlay reads this. Deriving it by parsing the breadcrumb
	// sentences would break silently the first time one was reworded.
	def := triageMachine()
	run, _, _ := scriptedRunner(map[string]string{
		"decompose": `{"parts":["a"]}`,
		"route":     `{"target":"deep"}`,
	})
	cur := &MachineCursor{}
	if _, err := (&AppCore{}).AdvanceMachine(nil, def, cur, "q", run, nil); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if len(cur.Log) != 2 {
		t.Fatalf("expected decompose→route and route→deep, got %+v", cur.Log)
	}
	if cur.Log[0].From != "decompose" || cur.Log[0].To != "route" {
		t.Errorf("first hop wrong: %+v", cur.Log[0])
	}
	if cur.Log[1].Why == "" {
		t.Error("a hop should record why it happened")
	}
	if cur.Log[0].At.IsZero() {
		t.Error("a hop should be timestamped")
	}

	// Bounded: a machine that has taken hundreds of transitions has a
	// design problem the last fifty will show just as well.
	cur.Log = nil
	ph, _ := def.Phase("route")
	for i := 0; i < maxPhaseLog+20; i++ {
		cur.moveTo("decompose", ph, "spin", nil)
		cur.Phase = "decompose"
	}
	if len(cur.Log) != maxPhaseLog {
		t.Errorf("log should cap at %d, got %d", maxPhaseLog, len(cur.Log))
	}
}
