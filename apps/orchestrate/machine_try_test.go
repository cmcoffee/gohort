package orchestrate

// The rehearsal panel. What it can show, and — the part that matters —
// what it cannot.

import (
	"context"
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

	r := httptest.NewRequest("POST", "/api/machines/"+def.ID+"/runs/stream", strings.NewReader(`{"input":"go"}`))
	w := httptest.NewRecorder()
	app.handleMachineRuns(w, asUser(r, user), udb, user, def, "stream")

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

	r := httptest.NewRequest("POST", "/api/machines/"+def.ID+"/runs/stream", strings.NewReader(`{"input":"go"}`))
	w := httptest.NewRecorder()
	app.handleMachineRuns(w, asUser(r, user), udb, user, def, "stream")

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want a refusal naming the problem, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "will not run yet") {
		t.Errorf("the refusal should carry the first problem: %s", w.Body.String())
	}
}

// Past runs are listed even for a machine that could not start a new one:
// the history is the record of what it did, not a control.
func TestRunSessionsListIsServedForAnyMachine(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	def := SaveMachineDef(udb, MachineDef{Owner: user, Name: "Chatty", Start: "talk",
		Phases: []MachinePhase{{Name: "talk", Prompt: "hi", Resident: true}}})

	r := httptest.NewRequest("GET", "/api/machines/"+def.ID+"/runs/sessions", nil)
	w := httptest.NewRecorder()
	app.handleMachineRuns(w, asUser(r, user), udb, user, def, "sessions")
	if w.Code != 200 {
		t.Fatalf("want an empty list, got %d: %s", w.Code, w.Body.String())
	}
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("a machine with no runs should list none, got %s", w.Body.String())
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

// A run narrates itself: one block per step, the framework's own
// decisions as status. Without that the panel is a spinner that returns
// everything at once, which is the thing streaming exists to fix.
func TestAStreamedRunEmitsABlockPerStep(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	def := SaveMachineDef(udb, MachineDef{Owner: user, Name: "Nightly", Start: "gather", Unattended: true,
		Phases: []MachinePhase{
			{Name: "gather", Desc: "Collect what changed.", Prompt: "gather", Next: "write"},
			{Name: "write", Desc: "Write it up.", Prompt: "write"},
		}})

	var kinds, titles []string
	sink := func(ev PipelineEvent) {
		kinds = append(kinds, ev.Kind)
		if ev.Kind == "block" {
			titles = append(titles, ev.Title)
		}
	}
	// No LLM in a test binary, so the run fails at its first step. What is
	// asserted is the NARRATION: the step opened a block and closed it,
	// which is what the panel renders.
	_, _ = app.runMachineStreaming(context.Background(), def, user, "go", sink)

	if len(titles) == 0 || !strings.Contains(titles[0], "gather") {
		t.Fatalf("the first step should open a block named for itself: %v", titles)
	}
	if !strings.Contains(titles[0], "Collect what changed") {
		t.Errorf("the block title should carry the step's own description: %q", titles[0])
	}
	var opened, closed int
	for _, k := range kinds {
		switch k {
		case "block":
			opened++
		case "block_done":
			closed++
		}
	}
	if opened == 0 || opened != closed {
		t.Errorf("every block opened must be closed: %d opened, %d closed (%v)", opened, closed, kinds)
	}
}
