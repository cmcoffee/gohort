// MachineDef → WorkflowGraph. The adapter half of docs/workflow-graph.md.
//
// The only file that knows both what a phase is and what a graph node
// is. Everything drawing-related lives in workflow_graph.go and stays
// ignorant of machines.

package core

import (
	"strconv"
	"strings"
)

// Graph compiles a machine into its picture.
// routingTargets returns the declared destinations of a phase's routing
// field, or nil when it declares none.
func routingTargets(p MachinePhase) []string { return p.RoutingChoices() }

func (d MachineDef) Graph() WorkflowGraph {
	g := WorkflowGraph{Title: d.Name, Entry: d.StartPhase()}
	start := d.StartPhase()

	for _, p := range d.Phases {
		kind := NodeStep
		if p.Resident {
			kind = NodeRest
		}
		if p.Name == start {
			kind = NodeEntry
		}
		g.Nodes = append(g.Nodes, WorkflowNode{
			ID: p.ID(), Label: p.Name, Note: p.Desc, Kind: kind, Tags: phaseTags(p),
		})
	}

	for _, p := range d.Phases {
		switch {
		case p.NextFrom != "":
			// DECLARED targets are drawn exactly. Undeclared, the target
			// is a run-time value and the honest drawing is an edge to
			// every phase it could pick — guessing a subset from the
			// field's prose would draw a graph the machine does not have,
			// which is the specific failure a picture is meant to
			// prevent. Declaring them is what turns that fan-out into the
			// shape somebody actually built.
			targets := d.Phases
			if declared := routingTargets(p); len(declared) > 0 {
				targets = targets[:0:0]
				for _, name := range declared {
					if t, ok := d.Phase(strings.TrimSpace(name)); ok {
						targets = append(targets, t)
					}
				}
			}
			for _, t := range targets {
				if t.Name == p.Name {
					continue
				}
				e := WorkflowEdge{
					From: p.Name, To: t.Name, Style: EdgeDashed,
					Label: "?", Note: "routed at run time by " + p.Name + "." + p.NextFrom,
				}
				// The static fallback usually names one of the same
				// targets. Two edges between one pair land on identical
				// curves with their labels stacked on top of each other,
				// so the two facts merge into one arrow that states both.
				if t.Name == p.Next {
					e.Label = "? · fallback"
					e.Note += "; also where it goes when " + p.NextFrom + " names no phase that exists"
				}
				g.Edges = append(g.Edges, e)
			}
			if p.Next != "" {
				// Was the fallback already drawn as one of the targets?
				// The old test was "does this phase exist", which held
				// only because EVERY phase was drawn. With declared
				// targets the fallback is often not among them, and that
				// arrow is the one saying where a bad choice lands.
				alreadyDrawn := false
				for _, t := range targets {
					if t.Name == p.Next {
						alreadyDrawn = true
						break
					}
				}
				if !alreadyDrawn {
					g.Edges = append(g.Edges, WorkflowEdge{
						From: p.Name, To: p.Next, Style: EdgeSolid, Label: "fallback",
						Note: "taken when " + p.NextFrom + " names no phase that exists",
					})
				}
			}
		case p.Next != "":
			label, note := "", ""
			if p.Resident {
				// A resident phase with a Next gets exactly one turn and
				// then hands off. That is a materially different thing
				// from a transient handoff and the picture should say so.
				label, note = "after one turn", "this phase replies once, then control moves on"
			}
			g.Edges = append(g.Edges, WorkflowEdge{From: p.Name, To: p.Next, Style: EdgeSolid, Label: label, Note: note})
		case p.Resident:
			// Stays. Drawn as a self-loop because "the conversation lives
			// here" is the most important fact about a resident phase and
			// an absence of arrows does not communicate it.
			g.Edges = append(g.Edges, WorkflowEdge{
				From: p.Name, To: p.Name, Style: EdgeSolid, Label: "stays",
				Note: "user turns land here until something moves them",
			})
		}

		if strings.TrimSpace(p.Guard) != "" {
			target := strings.TrimSpace(p.GuardTo)
			if target == "" {
				target = start
			}
			if _, ok := d.Phase(target); ok && target != p.Name {
				g.Edges = append(g.Edges, WorkflowEdge{
					From: p.Name, To: target, Style: EdgeBack, Label: "guard",
					Note: "guard: " + p.Guard,
				})
			}
		}
	}

	if len(d.Phases) > 1 {
		g.Legend = append(g.Legend,
			"Any phase can move to any other with change_phase — not drawn, because it connects everything to everything.")
	}
	g.Legend = append(g.Legend,
		"Solid: an unconditional handoff. Dashed: decided at run time. Dotted: a guard sending the conversation back.")
	return g
}

// ID is the graph handle for a phase. Phase names are already unique and
// dot-free, so they are their own identity — this exists so the adapter
// reads as a mapping rather than assuming the two vocabularies are the
// same thing forever.
func (p MachinePhase) ID() string { return p.Name }

// phaseTags renders the small annotations under a node: the settings
// that change how a phase behaves and would otherwise be invisible.
func phaseTags(p MachinePhase) []string {
	var tags []string
	if m := strings.ToLower(strings.TrimSpace(p.Model)); m == "lead" || m == "worker" {
		tags = append(tags, m)
	}
	switch strings.ToLower(strings.TrimSpace(p.Think)) {
	case "on":
		tags = append(tags, "thinks")
	case "off":
		tags = append(tags, "no thinking")
	}
	if len(p.Tools) > 0 {
		tags = append(tags, strconv.Itoa(len(p.Tools))+" tools")
	}
	if len(p.Output) > 0 {
		names := make([]string, 0, len(p.Output))
		for _, f := range p.Output {
			names = append(names, f.Name)
		}
		tags = append(tags, "→ "+strings.Join(names, ", "))
	}
	if strings.TrimSpace(p.Guard) != "" {
		tags = append(tags, "guarded")
	}
	return tags
}

// Overlay turns a session's cursor into the marks a live graph carries:
// where the conversation is now, and which edges it has actually taken.
func (c MachineCursor) Overlay() *WorkflowOverlay {
	o := &WorkflowOverlay{Current: c.Phase, Fired: map[string]int{}}
	for _, hop := range c.Log {
		o.Fired[EdgeKey(hop.From, hop.To)]++
	}
	return o
}
