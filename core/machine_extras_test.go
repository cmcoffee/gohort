package core

// The machine recipes shipped in extras/ are the first thing anyone
// pastes at /api/machines. A shipped example that 400s is worse than no
// example, so they are validated here rather than trusted.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtrasMachineRecipesValidate(t *testing.T) {
	paths, err := filepath.Glob("../extras/*.machine.json")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(paths) == 0 {
		t.Skip("no machine recipes in extras/")
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			var def MachineDef
			if err := json.Unmarshal(raw, &def); err != nil {
				t.Fatalf("not valid JSON: %v", err)
			}
			if err := def.Validate(); err != nil {
				t.Fatalf("does not validate:\n%v", err)
			}
			// A recipe with no description is a row in the picker that
			// says nothing about when to reach for it.
			if strings.TrimSpace(def.Description) == "" {
				t.Error("a shipped recipe should describe when to use it")
			}
			// And it should draw: the graph adapter is where a malformed
			// edge (a guard pointing nowhere, a router with no targets)
			// shows up as a picture nobody can read.
			if svg := def.Graph().SVG(nil); !strings.HasPrefix(svg, "<svg ") {
				t.Error("recipe does not render")
			}
		})
	}
}

// The investigation machine is the one with a real job (see
// docs/investigation.md). Its shape IS the design: a hypothesis is formed
// in one phase and TESTED in another, against evidence the first phase
// had to name. Collapse those and you get an agent that confirms its own
// hunch from the source that produced it — which reads as thorough and
// is circular.
func TestInvestigationRecipeSeparatesHunchFromVerification(t *testing.T) {
	raw, err := os.ReadFile("../extras/investigation.machine.json")
	if err != nil {
		t.Skip("recipe not present")
	}
	var def MachineDef
	if err := json.Unmarshal(raw, &def); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// The front router: not every turn has something to explain.
	triage, ok := def.Phase("triage")
	if !ok || triage.RoutesBy() == "" {
		t.Fatal("triage must route — a question with no observation skips the hypothesis")
	}
	if triage.Resident {
		t.Error("triage decides and hands off; it does not hold the conversation")
	}

	// The hypothesis phase must hand forward what would SETTLE it, not
	// just what it thinks. That is the field the verifying phase aims at,
	// and without it verification has no target and drifts back to
	// re-reading the observation.
	hunch, ok := def.Phase("hunch")
	if !ok {
		t.Fatal("no hunch phase")
	}
	declared := map[string]bool{}
	for _, f := range hunch.Output {
		declared[f.Name] = true
	}
	for _, want := range []string{"hypothesis", "confirms_if", "refutes_if", "look_where"} {
		if !declared[want] {
			t.Errorf("hunch must declare %q — verification needs a target, not an opinion", want)
		}
	}
	if hunch.Resident {
		t.Error("hunch must be transient: forming a hypothesis is not where a turn ends")
	}
	if hunch.Think != "on" {
		t.Error("committing to one explanation is judgment, not a transform")
	}

	// And verification has to be where the conversation lives, because
	// that is the part a person argues with.
	verify, ok := def.Phase("verify")
	if !ok || !verify.Resident {
		t.Fatal("verify must be the resident phase")
	}
	if verify.GuardTo != "triage" {
		t.Error("a new problem should re-triage rather than inherit this hypothesis")
	}
	// It must be told the honest third outcome exists. Supported and
	// refuted are easy; "the evidence does not settle it" is the one an
	// agent will otherwise round into a conclusion.
	if !strings.Contains(verify.Prompt, "UNSETTLED") {
		t.Error("verify must be able to report that the evidence settles nothing")
	}
	if !strings.Contains(verify.Prompt, "REFUTED") {
		t.Error("verify must be told to say so plainly when the hunch is wrong")
	}

	// The no-observation path exists and is its own resident phase.
	answer, ok := def.Phase("answer")
	if !ok || !answer.Resident {
		t.Fatal("answer must be a resident phase for questions with no observation")
	}
}

// The starter is what someone sees when they ask for a new machine. It
// must be a machine the server would accept — an editor that opens on a
// definition the save path rejects teaches the wrong lesson about the
// whole feature in the first ten seconds.
func TestStarterMachineValidatesAndTeachesTheShape(t *testing.T) {
	st := StarterMachine()
	if err := st.Validate(); err != nil {
		t.Fatalf("the starter does not validate:\n%v", err)
	}
	// It has to demonstrate the distinction, or it teaches nothing: one
	// phase that hands off, one the conversation lives in.
	var transient, resident int
	for _, p := range st.Phases {
		if p.Resident {
			resident++
			continue
		}
		transient++
		if p.Next == "" && p.NextFrom == "" {
			t.Errorf("starter phase %q is transient and hands off nowhere", p.Name)
		}
		if len(p.Output) == 0 {
			t.Errorf("starter phase %q should show what a handoff looks like", p.Name)
		}
	}
	if transient == 0 || resident == 0 {
		t.Errorf("the starter should show both kinds of phase, got %d transient %d resident", transient, resident)
	}
	// And it should draw, since the first thing someone may do is look at
	// it rather than read it.
	if svg := st.Graph().SVG(nil); !strings.HasPrefix(svg, "<svg ") {
		t.Error("the starter does not render")
	}
}

// Advice is for what is worth fixing but must not block a save. The rule
// that prompted it: a phase declaring fields AND telling the model to
// emit JSON — the author hand-rolling the mechanism the fields already
// are, in a prompt that then reads like a schema instead of a job.
func TestAdviceCatchesAHandRolledJSONPrompt(t *testing.T) {
	def := MachineDef{
		Name: "x", Start: "split",
		Phases: []MachinePhase{
			{Name: "split", Next: "answer",
				Prompt: "Analyze the user's input '{input}' and identify its core components. " +
					"Present them as a JSON object with two fields. Do not include any other text in your response.",
				Output: []PipelineField{{Name: "question_parts", Type: "list"}}},
			{Name: "answer", Prompt: "answer", Resident: true},
		},
	}
	adv := def.Advice()
	if len(adv) != 1 {
		t.Fatalf("expected one piece of advice, got %v", adv)
	}
	if !strings.Contains(adv[0], "declares") || !strings.Contains(adv[0], "say what to FIND") {
		t.Errorf("advice should explain what to do instead: %s", adv[0])
	}
	// It must NOT be a problem: a heuristic that reads wording has no
	// business refusing somebody's save.
	if err := def.Validate(); err != nil {
		t.Errorf("advice must not block a save: %v", err)
	}

	// A phase whose SUBJECT is JSON but which declares nothing is left
	// alone — the instruction is unusual there, not redundant.
	plain := MachineDef{Name: "y", Start: "read", Phases: []MachinePhase{
		{Name: "read", Resident: true, Prompt: "Explain this JSON object to the user in plain words."},
	}}
	if adv := plain.Advice(); len(adv) != 0 {
		t.Errorf("a phase declaring no fields should not be advised: %v", adv)
	}

	// And a well-written phase gets nothing.
	good := MachineDef{Name: "z", Start: "split", Phases: []MachinePhase{
		{Name: "split", Next: "answer", Prompt: "Work out what the person is actually asking, and say where it came from.",
			Output: []PipelineField{{Name: "asked", Type: "string"}}},
		{Name: "answer", Prompt: "answer", Resident: true},
	}}
	if adv := good.Advice(); len(adv) != 0 {
		t.Errorf("a clean phase should draw no advice: %v", adv)
	}
}
