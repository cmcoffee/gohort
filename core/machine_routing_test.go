package core

// Declared routing targets. One declaration replaces a hand-written list
// in three places: the instruction the model reads, the arrows the
// diagram draws, and what the validator can check.

import (
	"strings"
	"testing"
)

func routingFixture(targets []string) MachineDef {
	return MachineDef{
		Name: "Investigation", Start: "triage",
		Phases: []MachinePhase{
			{Name: "triage", Desc: "Is there something to explain?", Prompt: "Decide.",
				NextFrom: "next_phase", Next: "answer",
				Output: []PipelineField{{Name: "next_phase", Type: "string", Desc: "where to go", Enum: targets}}},
			{Name: "hunch", Desc: "Form one hypothesis.", Prompt: "Commit.", Next: "answer"},
			{Name: "answer", Desc: "Answer it.", Prompt: "Reply.", Resident: true},
		},
	}
}

func TestRoutingInstructionIsGeneratedFromTheDeclaration(t *testing.T) {
	def := routingFixture([]string{"hunch", "answer"})
	tri, _ := def.Phase("triage")
	block := def.PhaseBlock(tri, MachineState{}, PhaseVars{})

	if !strings.Contains(block, `Put exactly one of these in "next_phase"`) {
		t.Fatalf("no generated routing instruction:\n%s", block)
	}
	// Each choice is explained in the TARGET's own words, so one
	// description serves the phase and every router that can reach it.
	if !strings.Contains(block, "- hunch: Form one hypothesis.") {
		t.Errorf("a choice should carry the target phase's own description:\n%s", block)
	}
	if !strings.Contains(block, "- answer: Answer it.") {
		t.Errorf("missing the second choice:\n%s", block)
	}
	// The fallback is stated, because a model choosing badly should know
	// what happens rather than discovering it.
	if !strings.Contains(block, "If none of them fits, answer is used.") {
		t.Errorf("the fallback should be stated:\n%s", block)
	}

	// Undeclared targets keep the old behaviour: nothing generated, the
	// author's prose still governs.
	plain := routingFixture(nil)
	p, _ := plain.Phase("triage")
	if strings.Contains(plain.PhaseBlock(p, MachineState{}, PhaseVars{}), "Where this goes next") {
		t.Error("a phase declaring no targets should not get a generated block")
	}
}

// A target that is not a phase is a save-time error. The whole point of
// declaring is that this stops being a silent run-time fallback.
func TestUnknownTargetIsRefusedAtSave(t *testing.T) {
	def := routingFixture([]string{"hunch", "typo_phase"})
	err := def.Validate()
	if err == nil {
		t.Fatal("a target naming no phase should be refused")
	}
	if !strings.Contains(err.Error(), "typo_phase") || !strings.Contains(err.Error(), "not a step") {
		t.Errorf("the refusal should name the offender: %v", err)
	}
	if strings.Contains(err.Error(), "\"hunch\"") {
		t.Errorf("a valid target should not be reported: %v", err)
	}
	if err := routingFixture([]string{"hunch", "answer"}).Validate(); err != nil {
		t.Errorf("valid targets should save: %v", err)
	}
}

// The diagram draws what was declared, instead of an arrow to everything.
func TestDiagramDrawsOnlyDeclaredTargets(t *testing.T) {
	declared := routingFixture([]string{"hunch"}).Graph()
	var from int
	for _, e := range declared.Edges {
		if e.From == "triage" {
			from++
			if e.To != "hunch" && e.To != "answer" {
				t.Errorf("drew an edge to %q, which was never declared", e.To)
			}
		}
	}
	// hunch (declared) plus answer (the static fallback) — not every
	// phase in the machine.
	if from != 2 {
		t.Errorf("expected 2 edges out of triage, got %d", from)
	}

	// Undeclared, the honest drawing is still every possible target.
	open := routingFixture(nil).Graph()
	var openFrom int
	for _, e := range open.Edges {
		if e.From == "triage" {
			openFrom++
		}
	}
	if openFrom != 2 {
		t.Errorf("with nothing declared, every other phase should be reachable: %d", openFrom)
	}
}

// The contract the model receives states the set, and a reply outside it
// is rejected where it can still be repaired.
func TestContractStatesAndEnforcesTheSet(t *testing.T) {
	decl := []PipelineField{{Name: "next_phase", Type: FieldString, Required: true,
		Desc: "where to go", Enum: []string{"hunch", "answer"}}}

	contract := renderOutputContract(decl)
	if !strings.Contains(contract, "exactly one of: hunch, answer") {
		t.Errorf("the contract should state the allowed values:\n%s", contract)
	}
	if _, err := decodeStageOutput(`{"next_phase":"elsewhere"}`, decl); err == nil {
		t.Error("a value outside the declared set should be refused")
	}
	if _, err := decodeStageOutput(`{"next_phase":"hunch"}`, decl); err != nil {
		t.Errorf("a declared value should be accepted: %v", err)
	}
	// Capitalisation is a choice made correctly, not an error.
	if _, err := decodeStageOutput(`{"next_phase":"Answer"}`, decl); err != nil {
		t.Errorf("case should not fail a correct choice: %v", err)
	}
}

// A field's description is where the step's real instruction lives, so a
// multi-line one has to survive into the contract as a block. Crushed
// onto one line after a colon it stops looking like a directive — to a
// reader and to the model.
func TestMultiLineFieldInstructionRendersAsABlock(t *testing.T) {
	decl := []PipelineField{
		{Name: "hypothesis", Type: FieldString, Required: true,
			Desc: "The single best explanation, stated so it could be wrong.\nNot three ranked possibilities: one, committed to.\nHedging here reads as thoroughness and costs the next step its target."},
		{Name: "asked", Type: FieldString, Desc: "what they actually want to know"},
	}
	got := renderOutputContract(decl)

	// Every line of the instruction survives, on its own line.
	for _, line := range []string{
		"The single best explanation, stated so it could be wrong.",
		"Not three ranked possibilities: one, committed to.",
		"Hedging here reads as thoroughness and costs the next step its target.",
	} {
		if !strings.Contains(got, "\n  "+line) {
			t.Errorf("instruction line missing or not indented:\n%q\nin:\n%s", line, got)
		}
	}
	// A one-line description still reads inline — a block for three words
	// would be worse than the colon.
	if !strings.Contains(got, `"asked" (string, optional): what they actually want to know`) {
		t.Errorf("a short description should stay on its line:\n%s", got)
	}
	// And the field it belongs to is still identifiable above it.
	if !strings.Contains(got, `- "hypothesis" (string, required)`) {
		t.Errorf("the field header was lost:\n%s", got)
	}
}

// A step that DECIDES has to be drawn deciding. The graph's branch for
// this was gated on next_from — the hand-wired mechanism — so a machine
// written the recommended way (choices) drew as a straight line to its
// fallback, and the split, which is the one thing a picture of a
// decision exists to show, was missing from every one of them.
func TestTheDiagramDrawsASplit(t *testing.T) {
	def := MachineDef{Name: "m", Start: "triage", Phases: []MachinePhase{
		{Name: "triage", Prompt: "decide", Choices: []string{"dig", "answer"}, Next: "answer"},
		{Name: "dig", Prompt: "look", Next: "answer"},
		{Name: "answer", Prompt: "reply", Resident: true},
	}}
	var out []string
	for _, e := range def.Graph().Edges {
		if e.From == "triage" {
			out = append(out, e.To+"/"+e.Style+"/"+e.Label)
		}
	}
	if len(out) != 2 {
		t.Fatalf("a step choosing between two should leave by two arrows, got %v", out)
	}
	// Both are run-time choices, so both are dashed; the one that is
	// also the static fallback says so rather than being drawn twice.
	joined := strings.Join(out, " ")
	if !strings.Contains(joined, "dig/dashed/?") {
		t.Errorf("the chosen-at-run-time arrow is missing: %v", out)
	}
	if !strings.Contains(joined, "answer/dashed/? · fallback") {
		t.Errorf("the fallback should be one arrow stating both facts: %v", out)
	}
	// And the note names the field the decision actually lands in, which
	// for a choices step is the one the framework declares.
	for _, e := range def.Graph().Edges {
		if e.From == "triage" && !strings.Contains(e.Note, "triage.next_step") {
			t.Errorf("the arrow should name where the decision is written: %q", e.Note)
		}
	}
}

// What reordering does to the picture, pinned because the answer
// surprised somebody: the layout ranks steps by FLOW (distance from the
// entry over forward edges), so the list order only decides which side
// same-depth steps sit on. Moving a step in a chain changes nothing in
// the map, and that is correct — a picture of a machine that rearranged
// itself by list order would be a picture of the list, not the machine.
func TestReorderingOnlySwapsStepsAtTheSameDepth(t *testing.T) {
	build := func(order ...string) MachineDef {
		by := map[string]MachinePhase{
			"triage": {Name: "triage", Prompt: "p", Choices: []string{"dig", "answer"}, Next: "answer"},
			"dig":    {Name: "dig", Prompt: "p", Next: "answer"},
			"answer": {Name: "answer", Prompt: "p", Resident: true},
		}
		d := MachineDef{Name: "m", Start: "triage"}
		for _, n := range order {
			d.Phases = append(d.Phases, by[n])
		}
		return d
	}
	xOf := func(d MachineDef, want string) int {
		pos, _ := d.Graph().layout()
		return pos[want].X
	}

	// dig and answer are both one hop from triage: same depth, so the
	// list decides which is on the left.
	a, b := build("triage", "dig", "answer"), build("triage", "answer", "dig")
	if xOf(a, "dig") >= xOf(a, "answer") {
		t.Fatalf("declared first should sit left: dig=%d answer=%d", xOf(a, "dig"), xOf(a, "answer"))
	}
	if xOf(b, "dig") <= xOf(b, "answer") {
		t.Errorf("reordering should swap same-depth steps: dig=%d answer=%d", xOf(b, "dig"), xOf(b, "answer"))
	}

	// A chain has one step per depth, so no reordering can move
	// anything: every step is placed by what leads to it.
	chain := func(order ...string) MachineDef {
		by := map[string]MachinePhase{
			"one":   {Name: "one", Prompt: "p", Next: "two"},
			"two":   {Name: "two", Prompt: "p", Next: "three"},
			"three": {Name: "three", Prompt: "p", Resident: true},
		}
		d := MachineDef{Name: "m", Start: "one"}
		for _, n := range order {
			d.Phases = append(d.Phases, by[n])
		}
		return d
	}
	first, shuffled := chain("one", "two", "three"), chain("three", "two", "one")
	for _, name := range []string{"one", "two", "three"} {
		p1, _ := first.Graph().layout()
		p2, _ := shuffled.Graph().layout()
		if p1[name] != p2[name] {
			t.Errorf("%s moved in a chain, where the arrows fix every position: %v vs %v", name, p1[name], p2[name])
		}
	}
}

// A branch has to LOOK like a branch two steps down. Ordering each row
// by declaration alone put a step wherever its author happened to write
// it, so the right arm's second step could sit under the left arm with
// the arrows crossing — and nothing on screen said the fix was to
// reorder a list.
func TestAnArmStaysUnderItsOwnBranch(t *testing.T) {
	build := func(tail ...MachinePhase) MachineDef {
		d := MachineDef{Name: "m", Start: "triage", Phases: []MachinePhase{
			{Name: "triage", Prompt: "p", Choices: []string{"left", "right"}, Next: "right"},
			{Name: "left", Prompt: "p", Next: "left_two"},
			{Name: "right", Prompt: "p", Next: "right_two"},
		}}
		d.Phases = append(d.Phases, tail...)
		return d
	}
	leftTwo := MachinePhase{Name: "left_two", Prompt: "p", Resident: true}
	rightTwo := MachinePhase{Name: "right_two", Prompt: "p", Resident: true}

	// Whichever order the second row is DECLARED in, each step lands
	// under the step that leads to it.
	for _, def := range []MachineDef{build(leftTwo, rightTwo), build(rightTwo, leftTwo)} {
		pos, _ := def.Graph().layout()
		if pos["left_two"].X != pos["left"].X {
			t.Errorf("left_two should sit under left: %d vs %d", pos["left_two"].X, pos["left"].X)
		}
		if pos["right_two"].X != pos["right"].X {
			t.Errorf("right_two should sit under right: %d vs %d", pos["right_two"].X, pos["right"].X)
		}
	}

	// And where there is a genuine TIE — the two arms of one split, both
	// reached from the same step — the author's order still decides
	// which is on the left. That is what the ↑↓ buttons act on.
	a := MachineDef{Name: "m", Start: "triage", Phases: []MachinePhase{
		{Name: "triage", Prompt: "p", Choices: []string{"left", "right"}, Next: "right"},
		{Name: "left", Prompt: "p", Resident: true},
		{Name: "right", Prompt: "p", Resident: true},
	}}
	b := a
	b.Phases = []MachinePhase{a.Phases[0], a.Phases[2], a.Phases[1]}
	pa, _ := a.Graph().layout()
	pb, _ := b.Graph().layout()
	if !(pa["left"].X < pa["right"].X && pb["right"].X < pb["left"].X) {
		t.Errorf("siblings of one split should follow the declared order: %v %v", pa, pb)
	}
}
