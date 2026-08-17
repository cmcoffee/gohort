package core

// Mechanical repair. The bar for anything in here is that it has
// exactly one right answer; the test that matters most is the last one,
// which pins the findings that must NOT be auto-corrected.

import (
	"strings"
	"testing"
)

func danglingMachine() MachineDef {
	return MachineDef{Name: "M", Start: "gone", Phases: []MachinePhase{
		{Name: "triage", Prompt: "p", Next: "testing", Choices: []string{"answer", "testing"},
			Output: []PipelineField{
				{Name: "next_step", Type: FieldString, Enum: []string{"answer", "testing"}},
				{Name: "asked", Type: FieldNumber, From: "{input}"},
			}},
		{Name: "answer", Prompt: "p", Resident: true, Guard: "g", GuardTo: "testing",
			Keep: []string{"triage", "testing"}, ExitsTo: []string{"testing"}},
	}}
}

func TestRepairDropsReferencesToStepsThatAreGone(t *testing.T) {
	d := danglingMachine()

	// Previewing changes nothing — the button is labelled from this.
	pre := d.Repairs(RepairAll)
	if len(pre) < 8 {
		t.Fatalf("expected every dangling reference, got %d:\n%s", len(pre), strings.Join(RepairLines(pre), "\n"))
	}
	if d.Phases[0].Next != "testing" || d.Start != "gone" || len(d.Phases[1].Keep) != 2 {
		t.Fatal("a preview must not write through to the definition")
	}

	got := d.Repair(RepairAll)
	if len(got) != len(pre) {
		t.Errorf("preview promised %d and apply did %d", len(pre), len(got))
	}
	switch {
	case d.Phases[0].Next != "":
		t.Error("next still points at a step that is gone")
	case len(d.Phases[0].Choices) != 1 || d.Phases[0].Choices[0] != "answer":
		t.Errorf("choices: %v", d.Phases[0].Choices)
	case len(d.Phases[0].Output[0].Enum) != 1:
		t.Errorf("routing targets: %v", d.Phases[0].Output[0].Enum)
	case d.Phases[0].Output[1].Type != FieldString:
		t.Errorf("a filled field holds text: %q", d.Phases[0].Output[1].Type)
	case d.Phases[1].GuardTo != "":
		t.Error("guard_to should fall back to the start")
	case len(d.Phases[1].Keep) != 1 || len(d.Phases[1].ExitsTo) != 0:
		t.Errorf("keep=%v exits_to=%v", d.Phases[1].Keep, d.Phases[1].ExitsTo)
	case d.Start != "triage":
		t.Errorf("start should be written down, not left implied: %q", d.Start)
	}

	// The point of the button: the findings it offered to settle are
	// settled. Anything mentioning a step that never existed is a
	// finding somebody cannot act on.
	for _, p := range append(d.Problems(), d.Advice()...) {
		if strings.Contains(p, "testing") || strings.Contains(p, "gone") {
			t.Errorf("still reported after repair: %s", p)
		}
	}
	if again := d.Repairs(RepairAll); len(again) != 0 {
		t.Errorf("repairing twice should find nothing:\n%s", strings.Join(RepairLines(again), "\n"))
	}
}

// Each panel fixes what it reports. A button in "worth a look" that
// silently rewrote the checklist's findings would be the same surprise
// as one that did nothing.
func TestRepairIsScopedToThePanelItSitsIn(t *testing.T) {
	d := danglingMachine()
	advice := d.Repairs(RepairAdvice)
	if len(advice) != 1 || !advice[0].Advice || !strings.Contains(advice[0].What, "asked") {
		t.Fatalf("advice scope: %+v", advice)
	}
	for _, r := range d.Repairs(RepairProblems) {
		if r.Advice {
			t.Errorf("problem scope carried advice: %s", r.What)
		}
	}
	// And applying one leaves the other's work to do.
	d.Repair(RepairAdvice)
	if d.Phases[0].Next != "testing" {
		t.Error("the advice button should not have touched the wiring")
	}
	if len(d.Repairs(RepairProblems)) == 0 {
		t.Error("the checklist's own repairs went missing")
	}
}

// The line this feature must not cross. Every finding here has two
// defensible answers, so offering to "fix" it would be picking one on
// the author's behalf and deleting work to do it.
func TestRepairRefusesTheJudgementCalls(t *testing.T) {
	d := MachineDef{Name: "M", Start: "one", Phases: []MachinePhase{
		// tools AND a delegate: drop either one, only the author knows.
		{Name: "one", Prompt: "search the logs", Next: "two",
			Tools: []string{"read_file"}, Agent: "analyst"},
		// routes by a field AND lists choices: the advice says keep one.
		{Name: "two", Prompt: "decide", NextFrom: "lane", Choices: []string{"three"},
			Output: []PipelineField{{Name: "lane", Type: FieldString, Enum: []string{"three"}}}},
		{Name: "three", Prompt: "reply as json object", Resident: true},
		// a duplicate name, which is a rename nobody can guess
		{Name: "three", Prompt: "reply", Resident: true},
	}}
	if rs := d.Repairs(RepairAll); len(rs) != 0 {
		t.Errorf("these are decisions, not corrections:\n%s", strings.Join(RepairLines(rs), "\n"))
	}
	// And they are all still reported, so nothing was quietly swallowed.
	if len(d.Problems())+len(d.Advice()) < 3 {
		t.Error("the findings should stand")
	}
}

// A machine with no steps has nothing to hold a reference to. Repair is
// not the place to invent one.
func TestRepairLeavesAnEmptyMachineAlone(t *testing.T) {
	d := MachineDef{Name: "M", Start: "nowhere"}
	if rs := d.Repair(RepairAll); len(rs) != 0 || d.Start != "nowhere" {
		t.Errorf("%+v start=%q", rs, d.Start)
	}
}

// The finding whose fix is prose. It is deliberately NOT a repair: which
// sentences are formatting instructions and which are the subject is a
// judgement ("return the JSON object's key" is content), so it gets a
// draft to react to instead of a silent edit.
func TestPromptRewritesAreTheProseFindings(t *testing.T) {
	d := MachineDef{Name: "M", Start: "decompose", Phases: []MachinePhase{
		{Name: "decompose", Prompt: "Break it down. Reply with valid JSON: {\"parts\": []}", Next: "decide",
			Output: []PipelineField{{Name: "parts", Type: FieldString, Desc: "the pieces"}}},
		// Same trigger words, but nothing declared — the framework is not
		// encoding anything, so the instruction is the author's own.
		{Name: "raw", Prompt: "explain this config as json", Next: "answer"},
		// Declares fields and never mentions a format.
		{Name: "decide", Prompt: "work out which lane", Next: "answer",
			Output: []PipelineField{{Name: "lane", Type: FieldString, Desc: "which"}}},
		{Name: "answer", Prompt: "reply", Resident: true},
	}}
	rw := d.PromptRewrites()
	if len(rw) != 1 || rw[0].Step != "decompose" {
		t.Fatalf("only a step that declares fields AND hand-rolls the format: %+v", rw)
	}
	// The brief is the finding itself, by value — the panel matches on it
	// to decide which line carries the button, and a second copy of the
	// sentence would put it on none of them.
	found := false
	for _, a := range d.Advice() {
		if a == rw[0].Why {
			found = true
		}
	}
	if !found {
		t.Errorf("the brief is not one of the findings:\n%s", rw[0].Why)
	}
	// And it stays out of the mechanical repairs.
	for _, r := range d.Repairs(RepairAll) {
		if strings.Contains(r.What, "JSON") {
			t.Errorf("prose is not a mechanical repair: %s", r.What)
		}
	}
}
