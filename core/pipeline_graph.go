// PipelineDef → WorkflowGraph. The pipeline half of the adapter pair
// workflow_graph.go was designed for; machine_graph.go is the other.
//
// A pipeline reads as a list, which is why it went this long without a
// picture — and a list is exactly wrong for the three shapes that make
// pipelines worth having. A fanout is one stage that becomes twelve. A
// branch is two futures where the list shows one line. A loop is a
// stage list that runs again, and a flat rendering of it is a lie about
// how many times anything happens.
//
// Body stages are drawn, not summarised. The body is where a loop's
// work is, and a node saying "3 stages inside" is a picture of a box.

package core

import (
	"strconv"
	"strings"
)

// Graph compiles a pipeline into its picture.
func (d PipelineDef) Graph() WorkflowGraph {
	g := WorkflowGraph{Title: d.Name}
	if len(d.Stages) == 0 {
		return g
	}
	g.Entry = strings.TrimSpace(d.Stages[0].Name)

	real := map[string]bool{}
	for _, s := range d.Stages {
		if n := strings.TrimSpace(s.Name); n != "" {
			real[n] = true
		}
	}

	for i, s := range d.Stages {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			continue
		}
		kind := NodeStep
		switch {
		case i == 0:
			kind = NodeEntry
		case i == len(d.Stages)-1:
			// The last stage's output IS the pipeline's result. Drawn
			// heavier for the same reason a machine's resident phase is:
			// it is where the thing ends up.
			kind = NodeExit
		}
		g.Nodes = append(g.Nodes, WorkflowNode{
			ID: name, Label: name, Kind: kind, Tags: stageTags(s),
		})

		next := ""
		if i+1 < len(d.Stages) {
			next = strings.TrimSpace(d.Stages[i+1].Name)
		}
		g.Edges = append(g.Edges, stageEdges(s, name, next, real)...)
		g.Nodes, g.Edges = appendLoopBody(g.Nodes, g.Edges, s, name, next)
	}

	g.Legend = append(g.Legend, pipelineLegend(d)...)
	g.Legend = append(g.Legend,
		"Solid: the next stage. Dashed: a branch decided at run time. Dotted: a loop going round again.")
	return g
}

// stageEdges is what leaves ONE stage.
func stageEdges(s PipelineStage, name, next string, real map[string]bool) []WorkflowEdge {
	var out []WorkflowEdge
	if strings.TrimSpace(string(s.Kind)) == string(StageBranch) {
		// A branch is two futures. The taken arm is dashed because it is
		// decided at run time; the untaken one falls through to the next
		// stage exactly like any other, and drawing only the jump would
		// show a pipeline that always jumps.
		cond := strings.TrimSpace(s.When)
		if to := strings.TrimSpace(s.SkipTo); to != "" && real[to] {
			out = append(out, WorkflowEdge{From: name, To: to, Style: EdgeDashed,
				Label: "when " + cond,
				Note:  "skips ahead when " + cond + " is true"})
		}
		if next != "" {
			out = append(out, WorkflowEdge{From: name, To: next, Style: EdgeSolid,
				Label: "otherwise",
				Note:  "taken when " + cond + " is false"})
		}
		return out
	}
	// A loop's own arrow onward is drawn with its body, so the loop node
	// does not get two edges to the same place.
	if strings.TrimSpace(string(s.Kind)) == string(StageLoop) && len(s.Body) > 0 {
		return out
	}
	if next != "" {
		out = append(out, WorkflowEdge{From: name, To: next, Style: EdgeSolid})
	}
	return out
}

// appendLoopBody draws a loop as a hub: into the body, along it, back
// round, and on.
//
// Body names are scoped to the loop (Validate keeps them out of the
// caller's scope), so their node IDs are prefixed — two loops may each
// have a stage called "critique" and they are not the same node.
func appendLoopBody(nodes []WorkflowNode, edges []WorkflowEdge, s PipelineStage, name, next string) ([]WorkflowNode, []WorkflowEdge) {
	if strings.TrimSpace(string(s.Kind)) != string(StageLoop) || len(s.Body) == 0 {
		return nodes, edges
	}
	id := func(inner string) string { return name + " › " + strings.TrimSpace(inner) }

	var first, last string
	for _, b := range s.Body {
		inner := strings.TrimSpace(b.Name)
		if inner == "" {
			continue
		}
		nodes = append(nodes, WorkflowNode{
			ID: id(inner), Label: inner, Kind: NodeStep, Tags: stageTags(b),
		})
		if first == "" {
			first = id(inner)
		} else {
			edges = append(edges, WorkflowEdge{From: last, To: id(inner), Style: EdgeSolid})
		}
		last = id(inner)
	}
	if first == "" {
		return nodes, edges
	}
	edges = append(edges, WorkflowEdge{From: name, To: first, Style: EdgeSolid,
		Label: "each pass", Note: "the body runs once per pass, each pass seeing the last"})
	edges = append(edges, WorkflowEdge{From: last, To: name, Style: EdgeBack,
		Label: "again", Note: loopStopNote(s)})
	if next != "" {
		edges = append(edges, WorkflowEdge{From: name, To: next, Style: EdgeSolid,
			Label: "when it stops", Note: loopStopNote(s)})
	}
	return nodes, edges
}

// loopStopNote says what ends a loop, in the author's own terms.
func loopStopNote(s PipelineStage) string {
	parts := []string{"up to " + strconv.Itoa(s.Count) + " passes"}
	if u := strings.TrimSpace(s.Until); u != "" {
		parts = append(parts, "stopping early when "+u)
	}
	return strings.Join(parts, ", ")
}

// stageTags renders the annotations that change what a stage DOES and
// would otherwise be invisible in a box with a name in it.
func stageTags(s PipelineStage) []string {
	var tags []string
	switch PipelineStageKind(strings.TrimSpace(string(s.Kind))) {
	case StageAgent:
		tags = append(tags, "delegated")
	case StageFanout:
		over := strings.TrimSpace(s.FanOver)
		if over == "" {
			over = "a list"
		}
		// The multiplier is the whole point: this box is one stage in
		// the list and N calls at run time.
		tags = append(tags, "× each "+over)
	case StageLoop:
		tags = append(tags, "loop ×"+strconv.Itoa(s.Count))
	case StageBranch:
		tags = append(tags, "no model call")
	case StageTool:
		tags = append(tags, "tool: "+chFirstNonEmpty(s.Tool, "(unnamed)"))
	}
	if m := strings.ToLower(strings.TrimSpace(s.Model)); m == "lead" || m == "worker" {
		tags = append(tags, m)
	}
	if s.Think != nil {
		if *s.Think {
			tags = append(tags, "thinks")
		} else {
			tags = append(tags, "no thinking")
		}
	}
	if len(s.Tools) > 0 {
		tags = append(tags, strconv.Itoa(len(s.Tools))+" tools")
	}
	if out := s.ModelOutput(); len(out) > 0 {
		names := make([]string, 0, len(out))
		for _, f := range out {
			names = append(names, f.Name)
		}
		tags = append(tags, "→ "+strings.Join(names, ", "))
	}
	return tags
}

// pipelineLegend states what is true of the whole picture and cannot be
// drawn on it.
func pipelineLegend(d PipelineDef) []string {
	var out []string
	fanouts := 0
	for _, s := range d.Stages {
		if strings.TrimSpace(string(s.Kind)) == string(StageFanout) {
			fanouts++
		}
	}
	if fanouts > 0 {
		out = append(out,
			"A fanout is ONE box and many calls: it runs its prompt once per element of the list it fans over, in parallel, then collects them into one block.")
	}
	for _, s := range d.Stages {
		if strings.TrimSpace(string(s.Kind)) == string(StageBranch) && strings.TrimSpace(s.SkipTo) == "" {
			out = append(out, "Branch "+strconv.Quote(strings.TrimSpace(s.Name))+
				" ENDS the pipeline when it fires — its arrow onward is the case where it does not.")
		}
	}
	return out
}

// chFirstNonEmpty returns a when it has content, else b.
func chFirstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
