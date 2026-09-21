// Two authoring traps found by reading one exported Builder session: a reach
// spelling the framework teaches and then refuses, and a cross-check that only
// ran in one direction.
package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// "all" is printed back by update_phase and offered by the schema enum, while
// the stored value for "inherit everything" is "". Only update_phase used to
// translate, so a whole-machine update carrying the word the framework had just
// printed died on core validation — and a whole-machine update is
// all-or-nothing, so one unmeant field cost the entire save.
func TestReachAllIsTheSayableSpellingEverywhere(t *testing.T) {
	if got := normalizeReach("all"); got != ReachAll {
		t.Errorf("normalizeReach(\"all\") = %q, want the stored empty spelling", got)
	}
	for _, in := range []string{"ALL", "  All  "} {
		if got := normalizeReach(in); got != ReachAll {
			t.Errorf("normalizeReach(%q) = %q — the enum is matched case-insensitively everywhere else", in, got)
		}
	}
	for _, in := range []string{"read", "none", ""} {
		if got := normalizeReach(in); got != in {
			t.Errorf("normalizeReach(%q) = %q — the real values must pass through untouched", in, got)
		}
	}
	// A wrong word still has to fail: normalizing is not the same as accepting.
	if got := normalizeReach("everything"); got == ReachAll {
		t.Error("only \"all\" is the sayable spelling; anything else must reach the validator and be refused")
	}
}

// The parse path is what actually bit, so assert on it rather than only on the
// helper: a phase written with reach "all" must survive a whole-machine save.
func TestWholeMachineSaveAcceptsReachAll(t *testing.T) {
	phases, err := parseMachinePhases([]any{
		map[string]any{"name": "investigate", "prompt": "look", "reach": "all", "next": "answer"},
		map[string]any{"name": "answer", "prompt": "reply", "resident": true},
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if phases[0].Reach != ReachAll {
		t.Fatalf("reach %q survived the parse; core refuses anything but \"\", \"read\", \"none\"", phases[0].Reach)
	}
	def := MachineDef{ID: "m1", Name: "m", Owner: "u", Start: "investigate", Phases: phases}
	if probs := def.Problems(); len(probs) > 0 {
		t.Errorf("a machine written with the word the framework prints must be runnable: %v", probs)
	}
}
