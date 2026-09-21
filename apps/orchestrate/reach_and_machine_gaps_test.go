// Two authoring traps found by reading one exported Builder session: a reach
// spelling the framework teaches and then refuses, and a cross-check that only
// ran in one direction.
package orchestrate

import (
	"strings"
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

// Saving a MACHINE warned about agents that could not carry its phase tools.
// Saving an AGENT said nothing, though rewriting an allowlist is exactly how a
// step loses the tool it names.
func TestNarrowingAnAllowlistWarnsAboutTheAttachedMachine(t *testing.T) {
	udb, user := preflightFixture(t)
	// One name the agent holds (minted by an attached source, the same way the
	// attach preflight's own fixture grants one) and one it does not.
	withBundleSource(t)
	def := MachineDef{ID: "m1", Name: "acme_investigation", Owner: user, Start: "investigate",
		Phases: []MachinePhase{
			{Name: "investigate", Prompt: "look", Next: "answer",
				Tools: []string{"repo_search", "search_support_bundles"}},
			{Name: "answer", Prompt: "reply", Resident: true},
		}}
	if saved := SaveMachineDef(udb, def); saved.ID == "" {
		t.Fatal("machine did not save")
	}
	rec := &AgentRecord{ID: "a1", Name: "Wren", Owner: user, Machine: "m1",
		AttachedSources: []ReferenceSelection{{Kind: "testfiles", ItemID: "support_bundles"}}}
	sess := &ToolSession{Username: user, DB: udb}

	msg := attachedMachineGapsWarning(sess, rec)
	if !strings.Contains(msg, "repo_search") {
		t.Fatalf("the allowlist no longer carries a tool the machine's step names; that must be said at save time, got %q", msg)
	}
	if !strings.Contains(msg, "acme_investigation") || !strings.Contains(msg, "investigate") {
		t.Errorf("the warning must name the machine AND the step, or nobody can act on it: %q", msg)
	}
	// The tool it DOES still carry is not a gap, and saying so would make the
	// warning read as "this machine is broken" when one name is missing.
	if strings.Contains(msg, "search_support_bundles") {
		t.Errorf("a name the agent still holds must not appear as a gap: %q", msg)
	}
}

// A warning that fires on healthy configurations is one people learn to scroll
// past — including the time it was right.
func TestNoMachineWarningWhenTheAgentCarriesEverything(t *testing.T) {
	udb, user := preflightFixture(t)
	def := MachineDef{ID: "m2", Name: "quiet", Owner: user, Start: "s",
		Phases: []MachinePhase{{Name: "s", Prompt: "go", Resident: true}}}
	SaveMachineDef(udb, def)
	sess := &ToolSession{Username: user, DB: udb}
	if msg := attachedMachineGapsWarning(sess, &AgentRecord{ID: "a2", Owner: user, Machine: "m2"}); msg != "" {
		t.Errorf("a machine naming no tools can be missing none: %q", msg)
	}
	// No machine at all is the common case and must cost nothing.
	if msg := attachedMachineGapsWarning(sess, &AgentRecord{ID: "a3", Owner: user}); msg != "" {
		t.Errorf("an agent with no machine has nothing to check: %q", msg)
	}
	// And the nil-session path the existing create/update tests exercise.
	if msg := attachedMachineGapsWarning(nil, &AgentRecord{Machine: "m2"}); msg != "" {
		t.Errorf("no database, no opinion: %q", msg)
	}
}
