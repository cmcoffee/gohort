package core

// The machine's picture. What is drawn is a claim about what the
// machine can do, so an omission reads as a fact.

import (
	"strings"
	"testing"
)

// exits_to is a fact about SHAPE — it exists to stop a conversation
// crossing from one arm of a split into the other — so a picture that
// leaves it out is missing the thing it was drawn for. Worse, the
// legend's blanket "any phase can move to any other" reads as a
// statement that the bound is not there.
func TestTheGraphDrawsTheExitsAPhaseAllows(t *testing.T) {
	d := MachineDef{Name: "M", Start: "triage", Phases: []MachinePhase{
		{Name: "triage", Prompt: "route", Choices: []string{"left", "right"}},
		{Name: "left", Prompt: "dig", Next: "left_answer"},
		{Name: "right", Prompt: "look", Next: "right_answer"},
		// Each arm's landing settles there and may only go back to its
		// own arm — never across to the other side's.
		{Name: "left_answer", Prompt: "reply", Resident: true, ExitsTo: []string{"left", "triage"}},
		{Name: "right_answer", Prompt: "reply", Resident: true, ExitsTo: []string{"right"}},
	}}
	g := d.Graph()

	has := func(from, to, label string) bool {
		for _, e := range g.Edges {
			if e.From == from && e.To == to && e.Label == label {
				return true
			}
		}
		return false
	}
	if !has("left_answer", "left", "may exit") || !has("left_answer", "triage", "may exit") {
		t.Errorf("the allowed exits are not drawn:\n%+v", g.Edges)
	}
	if has("left_answer", "right", "may exit") || has("right_answer", "left", "may exit") {
		t.Error("an exit nobody allowed was drawn — the whole point is that the arms do not cross")
	}
	// Bounded phases are named in the legend, and the blanket claim is
	// not made about them.
	legend := strings.Join(g.Legend, " ")
	if strings.Contains(legend, "Any phase can move to any other") {
		t.Errorf("the legend still says the bound does not exist:\n%s", legend)
	}
	if !strings.Contains(legend, "left_answer") || !strings.Contains(legend, "right_answer") {
		t.Errorf("the legend should say which phases are bounded:\n%s", legend)
	}

	// A machine that bounds nothing keeps the blanket claim, which is
	// true of it.
	plain := MachineDef{Name: "P", Start: "a", Phases: []MachinePhase{
		{Name: "a", Prompt: "x", Next: "b"}, {Name: "b", Prompt: "y", Resident: true}}}
	if !strings.Contains(strings.Join(plain.Graph().Legend, " "), "Any phase can move to any other") {
		t.Error("an unbounded machine should still say change_phase goes anywhere")
	}

	// The guard's arrow already says where a guard sends it; a second
	// arrow to the same place would stack two labels on one curve.
	guarded := MachineDef{Name: "G", Start: "a", Phases: []MachinePhase{
		{Name: "a", Prompt: "x", Next: "b"},
		{Name: "b", Prompt: "y", Resident: true, Guard: "they moved on", GuardTo: "a", ExitsTo: []string{"a"}}}}
	n := 0
	for _, e := range guarded.Graph().Edges {
		if e.From == "b" && e.To == "a" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("expected one arrow from b to a, got %d", n)
	}

	// An exit naming a step that is gone is checklist work, not a drawn
	// arrow to nowhere.
	broken := MachineDef{Name: "B", Start: "a", Phases: []MachinePhase{
		{Name: "a", Prompt: "x", Resident: true, ExitsTo: []string{"vanished"}}}}
	for _, e := range broken.Graph().Edges {
		if e.To == "vanished" {
			t.Error("an exit to a step that does not exist should not be drawn")
		}
	}
}

// Two more things a box was hiding.
func TestTheGraphSaysWhenAStepIsNotRunByTheAgent(t *testing.T) {
	d := MachineDef{Name: "M", Start: "dig", Phases: []MachinePhase{
		{Name: "dig", Prompt: "look", Next: "answer", Agent: "ag-7f3c"},
		{Name: "answer", Prompt: "reply", Resident: true},
	}}
	tags := strings.Join(d.Graph().Nodes[0].Tags, " ")
	if !strings.Contains(tags, "delegated") {
		t.Errorf("a step run by another agent looks identical to one the agent runs itself: %q", tags)
	}
	// The id is not shown: an id in a picture is noise.
	if strings.Contains(tags, "ag-7f3c") {
		t.Errorf("the raw reference should not be drawn: %q", tags)
	}
}

// exits_to bounds change_phase, which happens during a turn. A step that
// passes on never holds one, so it bounds nothing there — reported, and
// not drawn, because a drawn arrow would argue with the report.
func TestAnExitOnAStepThatCannotHoldATurnIsReportedNotDrawn(t *testing.T) {
	d := MachineDef{Name: "M", Start: "route", Phases: []MachinePhase{
		{Name: "route", Prompt: "decide", Next: "answer", ExitsTo: []string{"answer"}},
		{Name: "answer", Prompt: "reply", Resident: true},
	}}
	for _, e := range d.Graph().Edges {
		if e.Label == "may exit" {
			t.Errorf("an inert exit was drawn: %+v", e)
		}
	}
	found := false
	for _, p := range d.Problems() {
		if strings.Contains(p, "exits_to is only valid") {
			found = true
		}
	}
	if !found {
		t.Errorf("inert config with nothing saying so:\n%s", strings.Join(d.Problems(), "\n"))
	}
}
