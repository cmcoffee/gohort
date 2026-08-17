package orchestrate

// The rehearsal panel. What it can show, and — the part that matters —
// what it cannot.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A rehearsal must not read as evidence for the one thing it cannot
// test. change_phase is decided INSIDE a resident step's turn, and a dry
// run does not run the step it lands in — so a machine whose arms are
// kept apart by exits_to rehearses identically whether the bound is
// working or missing entirely.
func TestTheRehearsalSaysWhatItCannotShow(t *testing.T) {
	plain := MachineDef{Name: "P", Start: "a", Phases: []MachinePhase{
		{Name: "a", Prompt: "x", Next: "b"}, {Name: "b", Prompt: "y", Resident: true}}}
	if c := tryCaveat(plain); strings.Contains(c, "change_phase") {
		t.Errorf("a machine that bounds nothing should not be warned about a bound:\n%s", c)
	}
	bounded := MachineDef{Name: "B", Start: "a", Phases: []MachinePhase{
		{Name: "a", Prompt: "x", Next: "left"},
		{Name: "left", Prompt: "y", Resident: true, ExitsTo: []string{"a"}}}}
	c := tryCaveat(bounded)
	if !strings.Contains(c, "change_phase") || !strings.Contains(c, "exits_to") {
		t.Errorf("staying put would read as a passing result:\n%s", c)
	}
	// And it still says what the rehearsal DOES cover, or the reader
	// concludes the whole thing proves nothing.
	if !strings.Contains(c, "Guards ARE judged") {
		t.Errorf("the caveat should say what is exercised too:\n%s", c)
	}
	if !strings.Contains(c, "no tools") {
		t.Errorf("the constant limits should survive:\n%s", c)
	}
}
