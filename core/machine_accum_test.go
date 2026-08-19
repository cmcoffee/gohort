package core

// The working set: lists many phases contribute to. Phase-keyed state
// cannot hold one, which is why these exist; the tests that matter are the
// ones about surviving what would otherwise erase them.

import (
	"context"
	"strings"
	"testing"
)

// gatherMachine collects across three passes: two steps contribute to one
// list, and the run comes back through a step that prunes.
func gatherMachine() MachineDef {
	return MachineDef{
		Name: "collect", Start: "ask", Unattended: true,
		Phases: []MachinePhase{
			{Name: "ask", Prompt: "ask", Next: "dig",
				Output:      []PipelineField{{Name: "questions", Type: FieldList}},
				Accumulates: []MachineAccumulator{{Name: "answers", From: "questions"}}},
			{Name: "dig", Prompt: "dig",
				Output:      []PipelineField{{Name: "found", Type: FieldList}},
				Accumulates: []MachineAccumulator{{Name: "answers", From: "found"}}},
		},
	}
}

func TestManyStepsBuildOneList(t *testing.T) {
	run, _, _ := scriptedRunner(map[string]string{
		"ask": `{"questions": ["what fails", "who owns it"]}`,
		"dig": `{"found": ["it fails under load"]}`,
	})
	notes, _, _ := collectNotes()
	cur := &MachineCursor{}

	if _, _, err := new(AppCore).RunUnattended(context.Background(), gatherMachine(), cur,
		MachineTurn{Input: "go"}, run, notes); err != nil {
		t.Fatalf("run: %v", err)
	}
	items, _ := cur.State["answers"].Fields[AccumulatorItemsField].([]any)
	if len(items) != 3 {
		t.Fatalf("the list holds %d, want all three contributions: %#v", len(items), items)
	}
	if items[0] != "what fails" || items[2] != "it fails under load" {
		t.Errorf("order should follow the run: %#v", items)
	}
	if got, _ := cur.State["answers"].Fields[AccumulatorCountField].(int); got != 3 {
		t.Errorf("count = %d, want 3 — a prompt asks how many so far", got)
	}
	// A list field contributes its ELEMENTS: a step that produced two
	// answers added two things, not one thing shaped like two.
	if strings.Count(cur.State["answers"].Text, "\n") != 2 {
		t.Errorf("the rendering should be one line per entry:\n%s", cur.State["answers"].Text)
	}
	// And the step's own entry is untouched by any of this.
	if cur.State["ask"].Text == "" {
		t.Error("contributing to a list must not replace the step's own finding")
	}
}

// The failure this whole thing exists to prevent: a run that keeps coming
// back must not lose what it spent twenty phases collecting.
func TestKeepPrunesStepsAndNeverTheLists(t *testing.T) {
	def := gatherMachine()
	// dig re-enters ask, and ask keeps nothing.
	def.Phases[1].Next = "ask"
	def.Phases[0].Keep = []string{}
	cur := &MachineCursor{
		Phase: "dig",
		State: MachineState{
			// ask has run, which is what makes the move below a RE-ENTRY:
			// moveTo trims only when coming BACK to a phase.
			"ask":     {Text: "asked once already"},
			"scan":    {Text: "an earlier step's finding"},
			"answers": {Text: "1. kept", Fields: map[string]any{AccumulatorItemsField: []any{"kept"}}},
		},
	}
	ask, _ := def.Phase("ask")
	ask.Keep = []string{"nothing_named"}
	cur.moveTo("dig", ask, "re-entry", func(string, string) {}, def.accumulatorNames())

	if _, kept := cur.State["scan"]; kept {
		t.Error("a step finding not named in keep should still be pruned")
	}
	if _, kept := cur.State["answers"]; !kept {
		t.Fatal("the working set must survive re-entry — it exists BECAUSE the run comes back")
	}
}

func TestAccumulatorModes(t *testing.T) {
	st := MachineState{}
	def := MachineDef{}

	appendPh := MachinePhase{Name: "a", Accumulates: []MachineAccumulator{{Name: "l", From: "f"}}}
	def.accumulate(appendPh, map[string]any{"f": []any{"x", "y"}}, st, nil)
	def.accumulate(appendPh, map[string]any{"f": []any{"y"}}, st, nil)
	if got, _ := st["l"].Fields[AccumulatorCountField].(int); got != 3 {
		t.Errorf("append takes everything, duplicates included: %d", got)
	}

	st = MachineState{}
	unionPh := MachinePhase{Name: "a", Accumulates: []MachineAccumulator{{Name: "l", From: "f", Mode: AccumUnion}}}
	def.accumulate(unionPh, map[string]any{"f": []any{"x", "y"}}, st, nil)
	def.accumulate(unionPh, map[string]any{"f": []any{"y", "z"}}, st, nil)
	if got, _ := st["l"].Fields[AccumulatorCountField].(int); got != 3 {
		t.Errorf("union skips what is already there: %d", got)
	}

	// Union on a keyed field: two records for the same id are one entry.
	st = MachineState{}
	keyed := MachinePhase{Name: "a", Accumulates: []MachineAccumulator{{Name: "l", From: "f", Mode: AccumUnion, By: "id"}}}
	def.accumulate(keyed, map[string]any{"f": []any{map[string]any{"id": "1", "t": "first"}}}, st, nil)
	def.accumulate(keyed, map[string]any{"f": []any{map[string]any{"id": "1", "t": "again"}}}, st, nil)
	if got, _ := st["l"].Fields[AccumulatorCountField].(int); got != 1 {
		t.Errorf("a union keyed on id should hold one entry, got %d", got)
	}

	st = MachineState{}
	replacePh := MachinePhase{Name: "a", Accumulates: []MachineAccumulator{{Name: "l", From: "f", Mode: AccumReplace}}}
	def.accumulate(replacePh, map[string]any{"f": []any{"x", "y"}}, st, nil)
	def.accumulate(replacePh, map[string]any{"f": []any{"z"}}, st, nil)
	if got, _ := st["l"].Fields[AccumulatorCountField].(int); got != 1 {
		t.Errorf("replace is the whole list, got %d", got)
	}
}

// A step that answered without the field it was to contribute has not
// contributed nothing on purpose. Silence there is indistinguishable from
// a list that is genuinely empty.
func TestAMissingContributionLeavesABreadcrumb(t *testing.T) {
	notes, kinds, _ := collectNotes()
	st := MachineState{}
	ph := MachinePhase{Name: "a", Accumulates: []MachineAccumulator{{Name: "l", From: "f"}}}
	MachineDef{}.accumulate(ph, map[string]any{"other": "value"}, st, notes)
	if _, wrote := st["l"]; wrote {
		t.Error("nothing should be written when the field is absent")
	}
	if !hasNote(*kinds, "machine_accumulator_empty") {
		t.Errorf("want a breadcrumb, got %v", *kinds)
	}
}

// The lists are part of what a later step is working from.
func TestTheWorkingSetShowsInTheEstablishedBlock(t *testing.T) {
	def := gatherMachine()
	st := MachineState{
		"ask":     {Text: "asked"},
		"answers": {Text: "1. what fails\n2. it fails under load", Fields: map[string]any{AccumulatorCountField: 2}},
	}
	dig, _ := def.Phase("dig")
	block := def.establishedBlock(dig, st)
	if !strings.Contains(block, "answers") || !strings.Contains(block, "it fails under load") {
		t.Errorf("a later step cannot work from a list it is not shown:\n%s", block)
	}
	if !strings.Contains(block, "2 so far") {
		t.Errorf("the count belongs in the heading:\n%s", block)
	}
}

// --- validation -------------------------------------------------------

func TestAccumulatorValidation(t *testing.T) {
	probs := func(p MachinePhase) []string {
		d := MachineDef{Name: "m", Start: p.Name, Unattended: true, Phases: []MachinePhase{p}}
		return d.Problems()
	}
	has := func(ps []string, want string) bool {
		for _, p := range ps {
			if strings.Contains(p, want) {
				return true
			}
		}
		return false
	}

	// A field the step does not produce.
	p := MachinePhase{Name: "a", Prompt: "x",
		Output:      []PipelineField{{Name: "found", Type: FieldList}},
		Accumulates: []MachineAccumulator{{Name: "answers", From: "nope"}}}
	if !has(probs(p), "declares no such field") {
		t.Errorf("a step can only contribute what it produces: %v", probs(p))
	}

	// A list named after a step.
	p = MachinePhase{Name: "answers", Prompt: "x",
		Output:      []PipelineField{{Name: "found", Type: FieldList}},
		Accumulates: []MachineAccumulator{{Name: "answers", From: "found"}}}
	if !has(probs(p), "same name as a step") {
		t.Errorf("a collision on the blackboard should be reported: %v", probs(p))
	}

	// An unknown mode.
	p = MachinePhase{Name: "a", Prompt: "x",
		Output:      []PipelineField{{Name: "found", Type: FieldList}},
		Accumulates: []MachineAccumulator{{Name: "answers", From: "found", Mode: "merge"}}}
	if !has(probs(p), "is not one of append, replace, union") {
		t.Errorf("an unknown mode should be reported: %v", probs(p))
	}

	// And a well-formed one is clean, INCLUDING a prompt that reads the
	// list: {state:answers} has to resolve or nobody can use it.
	p = MachinePhase{Name: "a", Prompt: "so far: {state:answers} ({state:answers.count})",
		Output:      []PipelineField{{Name: "found", Type: FieldList}},
		Accumulates: []MachineAccumulator{{Name: "answers", From: "found"}}}
	if got := probs(p); len(got) != 0 {
		t.Errorf("a well-formed contribution should have nothing outstanding: %v", got)
	}
}
