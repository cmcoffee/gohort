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
	if !ok || triage.NextFrom == "" {
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
