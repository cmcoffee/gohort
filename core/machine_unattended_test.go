package core

// A machine that RUNS rather than converses: no step waits for a person,
// the walk keeps going until one hands off nowhere, and that step's
// result is the answer. Same driver, same blackboard, same breadcrumbs as
// a turn; what changes is where it stops and what the stop means.

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// reportMachine is the shape this mode exists for: several steps of work
// and nobody waiting. It ends at "write", which hands off nowhere.
func reportMachine() MachineDef {
	return MachineDef{
		Name: "nightly", Start: "gather", Unattended: true,
		Phases: []MachinePhase{
			{Name: "gather", Desc: "Collect what changed.", Prompt: "Gather.", Next: "judge",
				Output: []PipelineField{{Name: "items", Type: FieldList}}},
			{Name: "judge", Desc: "Decide what matters.", Prompt: "Judge {state:gather.items}.", Next: "write"},
			{Name: "write", Desc: "Write it up.", Prompt: "Write it."},
		},
	}
}

func TestUnattendedRunWalksToTheTerminalStep(t *testing.T) {
	run, calls, _ := scriptedRunner(map[string]string{
		"gather": `{"items": ["a", "b"]}`,
		"judge":  "b matters",
		"write":  "The report.",
	})
	notes, _, _ := collectNotes()
	cur := &MachineCursor{}

	final, text, err := new(AppCore).RunUnattended(context.Background(), reportMachine(), cur,
		MachineTurn{Input: "go"}, run, notes)
	if err != nil {
		t.Fatalf("RunUnattended: %v", err)
	}
	if final.Name != "write" {
		t.Errorf("finished at %q, want the step that hands off nowhere", final.Name)
	}
	if text != "The report." {
		t.Errorf("result = %q, want the terminal step's own output", text)
	}
	// Every step ran, including the terminal one. This is the half of the
	// contract that differs from a turn: AdvanceMachine hands the resident
	// phase back UNRUN for the host to run as the reply.
	if got := strings.Join(*calls, ","); got != "gather,judge,write" {
		t.Errorf("calls = %q, want every step run once, in order", got)
	}
	// The blackboard is the run's working memory and belongs to the caller.
	if len(cur.State) != 3 {
		t.Errorf("state holds %d step(s), want all three", len(cur.State))
	}
	if items, ok := cur.State["gather"].Fields["items"].([]any); !ok || len(items) != 2 {
		t.Errorf("declared fields should land on the blackboard: %#v", cur.State["gather"].Fields)
	}
}

// The conversational cap is a courtesy to somebody watching a cursor.
// Applying it to a run would stop deep research at step four.
func TestUnattendedRunIsNotHeldToTheConversationalCap(t *testing.T) {
	// Eight steps, chained. More than MaxPhaseTransitions, far less than
	// the run ceiling.
	def := MachineDef{Name: "long", Start: "s1", Unattended: true}
	replies := map[string]string{}
	for i := 1; i <= 8; i++ {
		ph := MachinePhase{Name: "s" + strconv.Itoa(i), Prompt: "work"}
		if i < 8 {
			ph.Next = "s" + strconv.Itoa(i+1)
		}
		def.Phases = append(def.Phases, ph)
		replies[ph.Name] = "out" + strconv.Itoa(i)
	}
	run, calls, _ := scriptedRunner(replies)
	notes, _, _ := collectNotes()

	final, text, err := new(AppCore).RunUnattended(context.Background(), def, &MachineCursor{},
		MachineTurn{Input: "go"}, run, notes)
	if err != nil {
		t.Fatalf("RunUnattended: %v", err)
	}
	if len(*calls) != 8 || final.Name != "s8" || text != "out8" {
		t.Errorf("ran %d step(s), finished at %q with %q; want all eight", len(*calls), final.Name, text)
	}
}

// A cycle is caught by the ceiling, and the ceiling reports rather than
// returning a half-answer as though it were the answer.
func TestUnattendedRunCeilingIsAnErrorWithItsPartialResult(t *testing.T) {
	def := MachineDef{Name: "spin", Start: "a", Unattended: true, Phases: []MachinePhase{
		{Name: "a", Prompt: "loop", Next: "b"},
		{Name: "b", Prompt: "loop", Next: "a"},
	}}
	run, calls, _ := scriptedRunner(map[string]string{"a": "again", "b": "again"})
	notes, kinds, _ := collectNotes()

	_, text, err := new(AppCore).RunUnattended(context.Background(), def, &MachineCursor{},
		MachineTurn{Input: "go"}, run, notes)
	if err == nil {
		t.Fatal("a run that never finished should say so")
	}
	if text == "" {
		t.Error("the partial result should come back with the error, for a caller that would rather show something")
	}
	if len(*calls) != MaxUnattendedTransitions {
		t.Errorf("ran %d step(s), want the ceiling %d", len(*calls), MaxUnattendedTransitions)
	}
	if !hasNote(*kinds, "machine_run_cap") {
		t.Errorf("hitting the ceiling must leave a breadcrumb, got %v", *kinds)
	}
}

// A step that waits for a person, in a run with nobody there, is a step
// the walk enters and cannot leave. Validate reports it at save time;
// this is what happens when the flag was flipped afterwards.
func TestUnattendedRunRefusesAStepThatWaits(t *testing.T) {
	def := MachineDef{Name: "mixed", Start: "work", Unattended: true, Phases: []MachinePhase{
		{Name: "work", Prompt: "do", Next: "talk"},
		{Name: "talk", Prompt: "chat", Resident: true},
	}}
	run, _, _ := scriptedRunner(map[string]string{"work": "done"})
	notes, _, _ := collectNotes()

	_, _, err := new(AppCore).RunUnattended(context.Background(), def, &MachineCursor{},
		MachineTurn{Input: "go"}, run, notes)
	if err == nil || !strings.Contains(err.Error(), "talk") {
		t.Errorf("want an error naming the step that waits, got %v", err)
	}
}

// The mode is opt-in on the definition, not inferred from its shape.
func TestRunUnattendedRefusesAConversationalMachine(t *testing.T) {
	run, _, _ := scriptedRunner(nil)
	notes, _, _ := collectNotes()
	_, _, err := new(AppCore).RunUnattended(context.Background(), triageMachine(), &MachineCursor{},
		MachineTurn{Input: "hi"}, run, notes)
	if err == nil || !strings.Contains(err.Error(), "not marked unattended") {
		t.Errorf("want a refusal that names the reason, got %v", err)
	}
}

// A conversation is unchanged by any of this.
func TestConversationalWalkStillStopsAtTheResidentStep(t *testing.T) {
	run, calls, _ := scriptedRunner(map[string]string{
		"decompose": `{"parts": ["x"]}`,
		"route":     `{"target": "answer"}`,
	})
	notes, _, _ := collectNotes()
	cur := &MachineCursor{}

	ph, err := new(AppCore).AdvanceMachine(context.Background(), triageMachine(), cur,
		MachineTurn{Input: "hi"}, run, notes)
	if err != nil {
		t.Fatalf("AdvanceMachine: %v", err)
	}
	if ph.Name != "answer" {
		t.Errorf("stopped at %q, want the resident step", ph.Name)
	}
	// And it is handed back UNRUN: the host runs it as the reply.
	for _, c := range *calls {
		if c == "answer" {
			t.Error("the resident step must not be run by the walk")
		}
	}
}

// --- validation -------------------------------------------------------

func TestUnattendedValidationInvertsTheResidentRule(t *testing.T) {
	// A run with a step that waits.
	withResident := reportMachine()
	withResident.Phases = append(withResident.Phases, MachinePhase{Name: "chat", Prompt: "hi", Resident: true})
	if !anyProblem(withResident.Problems(), "no step may wait") {
		t.Errorf("a resident step in a run should be reported: %v", withResident.Problems())
	}

	// A run with nothing that finishes it.
	noEnd := reportMachine()
	noEnd.Phases[2].Next = "gather"
	if !anyProblem(noEnd.Problems(), "no step finishes it") {
		t.Errorf("a run with no terminal step should be reported: %v", noEnd.Problems())
	}

	// The ordinary run is clean.
	if probs := reportMachine().Problems(); len(probs) != 0 {
		t.Errorf("a well-formed run should have nothing outstanding: %v", probs)
	}

	// And a conversation still needs its resident step.
	conv := reportMachine()
	conv.Unattended = false
	if !anyProblem(conv.Problems(), "no step waits for the person") {
		t.Errorf("a conversation with no resident step should still be reported: %v", conv.Problems())
	}
}

func anyProblem(probs []string, want string) bool {
	for _, p := range probs {
		if strings.Contains(p, want) {
			return true
		}
	}
	return false
}
