package core

// The pipeline's picture. A pipeline reads as a list, which is exactly
// wrong for the three shapes that make one worth having: a fanout is
// one stage that becomes twelve, a branch is two futures where the list
// shows one line, and a loop is a stage list that runs again.

import (
	"strings"
	"testing"
)

func graphHas(g WorkflowGraph, from, to, label string) bool {
	for _, e := range g.Edges {
		if e.From == from && e.To == to && (label == "" || e.Label == label) {
			return true
		}
	}
	return false
}

func nodeTags(g WorkflowGraph, id string) string {
	for _, n := range g.Nodes {
		if n.ID == id {
			return strings.Join(n.Tags, " ")
		}
	}
	return "<no such node>"
}

func TestAPipelineDrawsItsShape(t *testing.T) {
	d := PipelineDef{Name: "research", Stages: []PipelineStage{
		{Name: "plan", Kind: StageWorker, Prompt: "p",
			Output: []PipelineField{{Name: "queries", Type: FieldList, Desc: "q"}}},
		{Name: "dig", Kind: StageFanout, FanOver: "plan.queries", Prompt: "{item}",
			Tools: []string{"web_search", "fetch_url"}},
		{Name: "answer", Kind: StageWorker, Prompt: "synthesize", Model: "lead"},
	}}
	g := d.Graph()

	if g.Entry != "plan" {
		t.Errorf("a pipeline begins at its first stage, got %q", g.Entry)
	}
	if !graphHas(g, "plan", "dig", "") || !graphHas(g, "dig", "answer", "") {
		t.Errorf("the run order is not drawn: %+v", g.Edges)
	}
	// The first and last boxes are not ordinary steps: one is where it
	// starts, the other is the result.
	kinds := map[string]string{}
	for _, n := range g.Nodes {
		kinds[n.ID] = n.Kind
	}
	if kinds["plan"] != NodeEntry || kinds["answer"] != NodeExit || kinds["dig"] != NodeStep {
		t.Errorf("node kinds: %v", kinds)
	}
	// The multiplier is the whole point of a fanout box.
	if !strings.Contains(nodeTags(g, "dig"), "× each plan.queries") {
		t.Errorf("a fanout should say it is many calls: %q", nodeTags(g, "dig"))
	}
	if !strings.Contains(nodeTags(g, "dig"), "2 tools") {
		t.Errorf("and what it can reach: %q", nodeTags(g, "dig"))
	}
	if !strings.Contains(nodeTags(g, "plan"), "→ queries") {
		t.Errorf("a declared contract should be visible: %q", nodeTags(g, "plan"))
	}
	if !strings.Contains(nodeTags(g, "answer"), "lead") {
		t.Errorf("a pinned tier should be visible: %q", nodeTags(g, "answer"))
	}
	// A fanout cannot be drawn as N boxes, so the legend says what the
	// one box means.
	if !strings.Contains(strings.Join(g.Legend, " "), "ONE box and many calls") {
		t.Errorf("legend: %v", g.Legend)
	}
	// And it renders.
	if svg := g.SVG(nil); !strings.HasPrefix(svg, "<svg ") {
		t.Error("the picture does not render")
	}
}

// A branch is two futures. Drawing only the jump shows a pipeline that
// always jumps; drawing only the fall-through hides the jump entirely.
func TestABranchDrawsBothFutures(t *testing.T) {
	d := PipelineDef{Name: "p", Stages: []PipelineStage{
		{Name: "screen", Kind: StageWorker, Prompt: "judge",
			Output: []PipelineField{{Name: "reject", Type: FieldBool, Desc: "r"}}},
		{Name: "gate", Kind: StageBranch, When: "screen.reject", SkipTo: "sorry"},
		{Name: "work", Kind: StageWorker, Prompt: "do it"},
		{Name: "sorry", Kind: StageWorker, Prompt: "explain"},
	}}
	g := d.Graph()
	if !graphHas(g, "gate", "sorry", "when screen.reject") {
		t.Errorf("the jump is not drawn: %+v", g.Edges)
	}
	if !graphHas(g, "gate", "work", "otherwise") {
		t.Errorf("the fall-through is not drawn: %+v", g.Edges)
	}
	// A branch with nowhere to jump ENDS the pipeline, which is not an
	// arrow — so it is a sentence.
	ends := PipelineDef{Name: "p", Stages: []PipelineStage{
		{Name: "screen", Kind: StageWorker, Prompt: "judge",
			Output: []PipelineField{{Name: "reject", Type: FieldBool, Desc: "r"}}},
		{Name: "gate", Kind: StageBranch, When: "screen.reject"},
		{Name: "work", Kind: StageWorker, Prompt: "do it"},
	}}
	legend := strings.Join(ends.Graph().Legend, " ")
	if !strings.Contains(legend, "ENDS the pipeline") || !strings.Contains(legend, `"gate"`) {
		t.Errorf("legend should say the branch ends it: %s", legend)
	}
}

// A loop is a stage list that runs again, and a flat rendering of it is
// a lie about how many times anything happens. The body is drawn,
// because the body is where the work is.
func TestALoopIsDrawnAsAHubWithItsBody(t *testing.T) {
	d := PipelineDef{Name: "p", Stages: []PipelineStage{
		{Name: "draft", Kind: StageWorker, Prompt: "write"},
		{Name: "round", Kind: StageLoop, Count: 5, Until: "critic.ok", Body: []PipelineStage{
			{Name: "critic", Kind: StageWorker, Prompt: "judge",
				Output: []PipelineField{{Name: "ok", Type: FieldBool, Desc: "o"}}},
			{Name: "revise", Kind: StageWorker, Prompt: "fix"},
		}},
		{Name: "answer", Kind: StageWorker, Prompt: "done"},
	}}
	g := d.Graph()

	if !graphHas(g, "round", "round › critic", "each pass") {
		t.Errorf("the body is not entered: %+v", g.Edges)
	}
	if !graphHas(g, "round › critic", "round › revise", "") {
		t.Error("the body's own order is not drawn")
	}
	if !graphHas(g, "round › revise", "round", "again") {
		t.Error("nothing goes round again — the loop reads as a straight line")
	}
	if !graphHas(g, "round", "answer", "when it stops") {
		t.Error("the loop never leaves")
	}
	// Exactly one arrow from the loop to what follows it: the body's
	// last stage must not ALSO flow onward, or the picture claims the
	// pipeline continues from inside the loop.
	n := 0
	for _, e := range g.Edges {
		if e.From == "round" && e.To == "answer" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("expected one arrow out of the loop, got %d", n)
	}
	// Body ids are scoped: two loops may each hold a "critic" and they
	// are not the same box.
	if nodeTags(g, "round › critic") == "<no such node>" {
		t.Error("body stages should be their own nodes")
	}
	if !strings.Contains(nodeTags(g, "round"), "loop ×5") {
		t.Errorf("the ceiling should be visible: %q", nodeTags(g, "round"))
	}
	// What stops it is hover text on the arrows, in the author's terms.
	for _, e := range g.Edges {
		if e.From == "round › revise" && e.To == "round" {
			if !strings.Contains(e.Note, "critic.ok") || !strings.Contains(e.Note, "5 passes") {
				t.Errorf("the stop condition is not on the arrow: %q", e.Note)
			}
		}
	}
	if svg := g.SVG(nil); !strings.HasPrefix(svg, "<svg ") {
		t.Error("a pipeline with a loop does not render")
	}
}

// An empty pipeline draws nothing rather than a broken picture.
func TestAnEmptyPipelineDrawsNothing(t *testing.T) {
	g := PipelineDef{Name: "p"}.Graph()
	if len(g.Nodes) != 0 || len(g.Edges) != 0 || g.Entry != "" {
		t.Errorf("expected an empty graph, got %+v", g)
	}
}
