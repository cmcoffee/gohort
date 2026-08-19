package orchestrate

// The rehearsal panel. What it can show, and — the part that matters —
// what it cannot.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// --- the door for a machine that RUNS ----------------------------------

// A conversational machine has nowhere for a run to land, and the refusal
// says which of the two doors this machine has.
func TestRunRefusesAConversationalMachine(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	def := SaveMachineDef(udb, MachineDef{Owner: user, Name: "Chatty", Start: "talk",
		Phases: []MachinePhase{{Name: "talk", Prompt: "hi", Resident: true}}})

	r := httptest.NewRequest("POST", "/api/machines/"+def.ID+"/run", strings.NewReader(`{"input":"go"}`))
	w := httptest.NewRecorder()
	app.handleMachineRun(w, asUser(r, user), udb, user, def)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Try it") {
		t.Errorf("the refusal should name the door this machine DOES have: %s", w.Body.String())
	}
}

// A machine with outstanding problems says so where somebody can act on
// it, rather than failing three layers down with the same information.
func TestRunReportsTheChecklistRatherThanFailing(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	// Unattended, but nothing finishes it: every step hands on.
	def := SaveMachineDef(udb, MachineDef{Owner: user, Name: "Spin", Start: "a", Unattended: true,
		Phases: []MachinePhase{
			{Name: "a", Prompt: "x", Next: "b"},
			{Name: "b", Prompt: "y", Next: "a"},
		}})

	r := httptest.NewRequest("POST", "/api/machines/"+def.ID+"/run", strings.NewReader(`{"input":"go"}`))
	w := httptest.NewRecorder()
	app.handleMachineRun(w, asUser(r, user), udb, user, def)

	if w.Code != 200 {
		t.Fatalf("want a reported blockage, got %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["blocked"] != true {
		t.Errorf("a machine that cannot run should be reported as blocked: %v", got)
	}
	if list, _ := got["checklist"].([]any); len(list) == 0 {
		t.Errorf("the blockage should carry the checklist that explains it: %v", got)
	}
}

// The door exists only where it can work.
func TestRunSectionOnlyForMachinesThatRun(t *testing.T) {
	runs := MachineDef{Name: "Nightly", Unattended: true,
		Phases: []MachinePhase{{Name: "write", Prompt: "w"}}}
	if unattendedRunSection(runs).Title != "Run it" {
		t.Error("a machine that runs should offer the door")
	}
	converses := MachineDef{Name: "Chatty",
		Phases: []MachinePhase{{Name: "talk", Prompt: "hi", Resident: true}}}
	if s := unattendedRunSection(converses); s.Title != "" || s.Body != nil {
		t.Errorf("a conversation should have no run door at all, got %+v", s)
	}
}
