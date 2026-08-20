package core

// Tests for session-resident phase machines: the validator, the turn
// driver, state templating, and the pinned system block. No LLM and no
// store — the driver takes a PhaseRunner callback, so the whole walk is
// exercised against canned replies.

import (
	"context"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// --- fixtures ---------------------------------------------------------

// triageMachine is the canonical shape the design exists to serve:
// decompose once, route once, then sit in a resident phase.
func triageMachine() MachineDef {
	return MachineDef{
		Name:  "triage",
		Start: "decompose",
		Phases: []MachinePhase{
			{
				Name: "decompose", Desc: "Split the question up.",
				Prompt: "Break down: {input}", Next: "route",
				Output: []PipelineField{{Name: "parts", Type: FieldList}},
			},
			{
				Name: "route", Desc: "Pick a lane.",
				Prompt: "Given {state:decompose.parts}, pick a phase.",
				Output: []PipelineField{{Name: "target", Type: FieldString}},
				// Route dynamically; fall back to answer when it can't.
				NextFrom: "target", Next: "answer",
			},
			{Name: "answer", Desc: "Reply directly.", Resident: true, Prompt: "Answer from {state:decompose.parts}."},
			{Name: "deep", Desc: "Long-form work.", Resident: true, Prompt: "Take your time."},
		},
	}
}

// scriptedRunner returns a PhaseRunner serving canned replies by phase
// name, plus the per-phase call log so a test can assert what actually
// ran (the whole point of the resident/transient split is that turn 2
// runs nothing).
func scriptedRunner(replies map[string]string) (PhaseRunner, *[]string, *[]string) {
	var calls []string
	var prompts []string
	run := func(_ context.Context, ph MachinePhase, prompt string) (string, error) {
		calls = append(calls, ph.Name)
		prompts = append(prompts, prompt)
		return replies[ph.Name], nil
	}
	return run, &calls, &prompts
}

// collectNotes returns a breadcrumb sink plus the slugs it recorded.
func collectNotes() (func(string, string), *[]string, *[]string) {
	var kinds, details []string
	return func(kind, detail string) {
		kinds = append(kinds, kind)
		details = append(details, detail)
	}, &kinds, &details
}

func hasNote(kinds []string, want string) bool {
	for _, k := range kinds {
		if k == want {
			return true
		}
	}
	return false
}

// --- Validate ---------------------------------------------------------

func TestMachineValidate_CanonicalShapePasses(t *testing.T) {
	if err := triageMachine().Validate(); err != nil {
		t.Fatalf("canonical machine should validate, got: %v", err)
	}
}

func TestMachineValidate_Rejects(t *testing.T) {
	cases := map[string]struct {
		def  MachineDef
		want string
	}{
		"machine has no steps": {
			MachineDef{Name: "empty"},
			"machine has no steps",
		},
		"no resident phase": {
			MachineDef{Phases: []MachinePhase{
				{Name: "a", Prompt: "x", Next: "b"},
				{Name: "b", Prompt: "y", Next: "a"},
			}},
			"no step waits for the person",
		},
		"duplicate name": {
			MachineDef{Phases: []MachinePhase{
				{Name: "a", Prompt: "x", Resident: true},
				{Name: "a", Prompt: "y", Resident: true},
			}},
			"duplicate step name",
		},
		"dotted name": {
			MachineDef{Phases: []MachinePhase{{Name: "a.b", Prompt: "x", Resident: true}}},
			"may not contain a dot",
		},
		"unknown start": {
			MachineDef{Start: "nope", Phases: []MachinePhase{{Name: "a", Prompt: "x", Resident: true}}},
			"start names unknown step",
		},
		"unknown next": {
			MachineDef{Phases: []MachinePhase{
				{Name: "a", Prompt: "x", Next: "ghost"},
				{Name: "b", Prompt: "y", Resident: true},
			}},
			"next names unknown step",
		},
		"transient dead end": {
			MachineDef{Phases: []MachinePhase{
				{Name: "a", Prompt: "x"},
				{Name: "b", Prompt: "y", Resident: true},
			}},
			"passes on but goes nowhere",
		},
		"next_from undeclared": {
			MachineDef{Phases: []MachinePhase{
				{Name: "a", Prompt: "x", NextFrom: "target", Next: "b"},
				{Name: "b", Prompt: "y", Resident: true},
			}},
			"not one of this step's declared output fields",
		},
		"next_from wrong type": {
			MachineDef{Phases: []MachinePhase{
				{Name: "a", Prompt: "x", NextFrom: "target", Next: "b",
					Output: []PipelineField{{Name: "target", Type: FieldList}}},
				{Name: "b", Prompt: "y", Resident: true},
			}},
			"must be a string field holding a step name",
		},
		"guard on transient phase": {
			MachineDef{Phases: []MachinePhase{
				{Name: "a", Prompt: "x", Next: "b", Guard: "is this still the same job?"},
				{Name: "b", Prompt: "y", Resident: true},
			}},
			"guard is only valid on a step the conversation waits in",
		},
		"guard_to without guard": {
			MachineDef{Phases: []MachinePhase{
				{Name: "a", Prompt: "x", Resident: true, GuardTo: "a"},
			}},
			"there is no guard to trip it",
		},
		"output on resident phase": {
			MachineDef{Phases: []MachinePhase{
				{Name: "a", Prompt: "x", Resident: true,
					Output: []PipelineField{{Name: "reply", Type: FieldString}}},
			}},
			"output is not valid on a step the conversation waits in",
		},
		"next_from on resident phase": {
			MachineDef{Phases: []MachinePhase{
				{Name: "a", Prompt: "x", Resident: true, NextFrom: "target"},
			}},
			"next_from is not valid on a step the conversation waits in",
		},
		"turn-local token in resident prompt": {
			MachineDef{Phases: []MachinePhase{
				{Name: "a", Prompt: "answer {input} well", Resident: true},
			}},
			"is not available in a step the conversation waits in",
		},
		"unknown state phase ref": {
			MachineDef{Phases: []MachinePhase{
				{Name: "a", Prompt: "see {state:ghost.x}", Resident: true},
			}},
			"references unknown step",
		},
		"unknown state field ref": {
			MachineDef{Phases: []MachinePhase{
				{Name: "scan", Prompt: "x", Next: "a",
					Output: []PipelineField{{Name: "notes", Type: FieldString}}},
				{Name: "a", Prompt: "see {state:scan.missing}", Resident: true},
			}},
			"references a field step scan does not declare",
		},
		"keep names unknown step": {
			MachineDef{Phases: []MachinePhase{
				{Name: "a", Prompt: "x", Resident: true, Keep: []string{"ghost"}},
			}},
			"keep names unknown step",
		},
		"bad model": {
			MachineDef{Phases: []MachinePhase{
				{Name: "a", Prompt: "x", Resident: true, Model: "turbo"},
			}},
			"model must be",
		},
		"transient cycle never reaches a reply": {
			MachineDef{Start: "a", Phases: []MachinePhase{
				{Name: "a", Prompt: "x", Next: "b"},
				{Name: "b", Prompt: "y", Next: "a"},
				{Name: "c", Prompt: "z", Resident: true},
			}},
			"loop back on themselves",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := tc.def.Validate()
			if err == nil {
				t.Fatalf("expected a problem containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("expected a problem containing %q, got: %v", tc.want, err)
			}
		})
	}
}

func TestMachineValidate_ReportsEveryIndependentProblem(t *testing.T) {
	// A machine is authored as one object, so its mistakes arrive as a
	// set. Returning them one at a time turns one fix into three
	// round-trips — the same reasoning as validateStageList.
	def := MachineDef{Phases: []MachinePhase{
		{Name: "a", Prompt: "x", Next: "ghost", Model: "turbo"},
		{Name: "b", Prompt: "y", Resident: true, Keep: []string{"nope"}},
	}}
	err := def.Validate()
	if err == nil {
		t.Fatal("expected problems")
	}
	msg := err.Error()
	if !strings.Contains(msg, "3 problems") {
		t.Errorf("expected all three problems in one error, got: %v", err)
	}
	for _, want := range []string{"next names unknown step", "model must be", "keep names unknown step"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in: %v", want, err)
		}
	}
}

func TestMachineValidate_RouterWithDynamicTargetIsNotACycle(t *testing.T) {
	// The cycle walk stops at any phase carrying next_from: a router's
	// target is a run-time value and may well BE the resident phase.
	// Calling that a cycle would reject the most useful machine there is.
	def := MachineDef{Start: "route", Phases: []MachinePhase{
		{Name: "route", Prompt: "x", NextFrom: "target", Next: "reply",
			Output: []PipelineField{{Name: "target", Type: FieldString}}},
		{Name: "reply", Prompt: "y", Resident: true},
	}}
	if err := def.Validate(); err != nil {
		t.Fatalf("router machine should validate, got: %v", err)
	}
}

// --- the turn driver --------------------------------------------------

func TestAdvanceMachine_FirstTurnWalksToTheResidentPhase(t *testing.T) {
	def := triageMachine()
	run, calls, prompts := scriptedRunner(map[string]string{
		"decompose": `{"parts":["cost","timeline"]}`,
		"route":     `{"target":"deep"}`,
	})
	note, _, _ := collectNotes()
	cur := &MachineCursor{}

	ph, err := (&AppCore{}).AdvanceMachine(context.Background(), def, cur, MachineTurn{Input: "how much and how long?"}, run, note)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if ph.Name != "deep" {
		t.Fatalf("expected to land on deep (the routed target), got %s", ph.Name)
	}
	if got := strings.Join(*calls, ","); got != "decompose,route" {
		t.Errorf("expected the two transient phases to run and the resident one NOT to, got: %s", got)
	}
	if cur.Phase != "deep" {
		t.Errorf("cursor should be parked on deep, got %s", cur.Phase)
	}
	// The blackboard carries both results forward.
	if parts, ok := cur.State["decompose"].Fields["parts"].([]any); !ok || len(parts) != 2 {
		t.Errorf("decompose fields did not land: %#v", cur.State["decompose"])
	}
	// The user's message reached the first phase, and the first phase's
	// decoded field reached the second.
	if !strings.Contains((*prompts)[0], "how much and how long?") {
		t.Errorf("{input} did not resolve: %s", (*prompts)[0])
	}
	if !strings.Contains((*prompts)[1], "cost") {
		t.Errorf("{state:decompose.parts} did not resolve into the router prompt: %s", (*prompts)[1])
	}
}

func TestAdvanceMachine_LaterTurnsRunNothing(t *testing.T) {
	// The whole point: once parked in a resident phase, a turn makes no
	// extra LLM calls at all.
	def := triageMachine()
	run, calls, _ := scriptedRunner(nil)
	note, _, _ := collectNotes()
	cur := &MachineCursor{Phase: "answer", State: MachineState{
		"decompose": {Fields: map[string]any{"parts": []any{"cost"}}},
	}}

	ph, err := (&AppCore{}).AdvanceMachine(context.Background(), def, cur, MachineTurn{Input: "follow-up question"}, run, note)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if ph.Name != "answer" {
		t.Fatalf("expected to resume in answer, got %s", ph.Name)
	}
	if len(*calls) != 0 {
		t.Errorf("a resumed resident turn must make no phase calls, got %v", *calls)
	}
}

func TestAdvanceMachine_UnknownRouteFallsBackAndLeavesABreadcrumb(t *testing.T) {
	def := triageMachine()
	run, _, _ := scriptedRunner(map[string]string{
		"decompose": `{"parts":["a"]}`,
		"route":     `{"target":"nonsense"}`,
	})
	note, kinds, details := collectNotes()
	cur := &MachineCursor{}

	ph, err := (&AppCore{}).AdvanceMachine(context.Background(), def, cur, MachineTurn{Input: "q"}, run, note)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if ph.Name != "answer" {
		t.Fatalf("expected the static fallback (answer), got %s", ph.Name)
	}
	if !hasNote(*kinds, "machine_route_fallback") {
		t.Fatalf("a routing fallback must leave a breadcrumb, got %v", *kinds)
	}
	if !strings.Contains(strings.Join(*details, " "), "nonsense") {
		t.Errorf("the breadcrumb should name the phase that was asked for: %v", *details)
	}
}

func TestAdvanceMachine_DeadEndRoutesToTheFirstResidentPhase(t *testing.T) {
	// A router with no static fallback whose choice doesn't resolve.
	// Validate can't catch this (next is optional), so the driver has to
	// land the turn somewhere a reply can come from.
	def := MachineDef{Start: "route", Phases: []MachinePhase{
		{Name: "route", Prompt: "x", NextFrom: "target",
			Output: []PipelineField{{Name: "target", Type: FieldString}}},
		{Name: "reply", Prompt: "y", Resident: true},
	}}
	if err := def.Validate(); err != nil {
		t.Fatalf("fixture should validate: %v", err)
	}
	run, _, _ := scriptedRunner(map[string]string{"route": `{"target":"ghost"}`})
	note, kinds, _ := collectNotes()
	cur := &MachineCursor{}

	ph, err := (&AppCore{}).AdvanceMachine(context.Background(), def, cur, MachineTurn{Input: "q"}, run, note)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if ph.Name != "reply" {
		t.Fatalf("expected the first resident phase, got %s", ph.Name)
	}
	if !hasNote(*kinds, "machine_dead_end") {
		t.Errorf("expected a dead-end breadcrumb, got %v", *kinds)
	}
}

func TestAdvanceMachine_TransitionCapStopsADynamicCycle(t *testing.T) {
	// Two transient phases routing at each other. Validate passes (the
	// targets are run-time values), so the cap is the only thing between
	// this and a turn that never replies.
	def := MachineDef{Start: "ping", Phases: []MachinePhase{
		{Name: "ping", Prompt: "x", NextFrom: "target", Next: "reply",
			Output: []PipelineField{{Name: "target", Type: FieldString}}},
		{Name: "pong", Prompt: "y", NextFrom: "target", Next: "reply",
			Output: []PipelineField{{Name: "target", Type: FieldString}}},
		{Name: "reply", Prompt: "z", Resident: true},
	}}
	if err := def.Validate(); err != nil {
		t.Fatalf("fixture should validate: %v", err)
	}
	run, calls, _ := scriptedRunner(map[string]string{
		"ping": `{"target":"pong"}`,
		"pong": `{"target":"ping"}`,
	})
	note, kinds, _ := collectNotes()
	cur := &MachineCursor{}

	ph, err := (&AppCore{}).AdvanceMachine(context.Background(), def, cur, MachineTurn{Input: "q"}, run, note)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if len(*calls) != MaxPhaseTransitions {
		t.Errorf("expected the walk to stop after %d phase calls, made %d (%v)", MaxPhaseTransitions, len(*calls), *calls)
	}
	if !hasNote(*kinds, "machine_transition_cap") {
		t.Errorf("hitting the cap must leave a breadcrumb, got %v", *kinds)
	}
	if ph.Name != "ping" && ph.Name != "pong" {
		t.Errorf("expected to reply from where the walk stopped, got %s", ph.Name)
	}
}

func TestAdvanceMachine_ResumeHealsAStalePhaseAndKeepsState(t *testing.T) {
	// The machine was edited under a live session. Keep the thing,
	// surface the break, never silently discard what the session
	// already established.
	def := triageMachine()
	run, _, _ := scriptedRunner(map[string]string{
		"decompose": `{"parts":["a"]}`,
		"route":     `{"target":"answer"}`,
	})
	note, kinds, _ := collectNotes()
	cur := &MachineCursor{Phase: "retired_phase", State: MachineState{
		"earlier": {Text: "something the session established"},
	}}

	if _, err := (&AppCore{}).AdvanceMachine(context.Background(), def, cur, MachineTurn{Input: "q"}, run, note); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if !hasNote(*kinds, "machine_phase_reset") {
		t.Errorf("a healed cursor must leave a breadcrumb, got %v", *kinds)
	}
	if _, kept := cur.State["earlier"]; !kept {
		t.Error("resuming must keep the state the session already established")
	}
}

func TestAdvanceMachine_PhaseErrorFailsTheTurn(t *testing.T) {
	// A declared output that never parses, even after the repair retry.
	def := triageMachine()
	run := func(_ context.Context, ph MachinePhase, _ string) (string, error) {
		return "not json at all", nil
	}
	note, _, _ := collectNotes()
	cur := &MachineCursor{}

	if _, err := (&AppCore{}).AdvanceMachine(context.Background(), def, cur, MachineTurn{Input: "q"}, run, note); err == nil {
		t.Fatal("expected an error when a phase can't produce its declared shape")
	} else if !strings.Contains(err.Error(), "decompose") {
		t.Errorf("the error should name the phase that failed, got: %v", err)
	}
}

// --- resident handoff + state trimming --------------------------------

func TestCompleteTurn_ResidentPhaseWithNextHandsOffAfterOneTurn(t *testing.T) {
	def := MachineDef{Start: "intake", Phases: []MachinePhase{
		{Name: "intake", Prompt: "Ask what they need.", Resident: true, Next: "work"},
		{Name: "work", Prompt: "Do it.", Resident: true},
	}}
	if err := def.Validate(); err != nil {
		t.Fatalf("fixture should validate: %v", err)
	}
	note, kinds, _ := collectNotes()
	cur := &MachineCursor{Phase: "intake"}

	ph, _ := def.Phase("intake")
	def.CompleteTurn(cur, ph, note)
	if cur.Phase != "work" {
		t.Fatalf("expected the one-beat phase to hand off, cursor is %s", cur.Phase)
	}
	if !hasNote(*kinds, "machine_phase_advance") {
		t.Errorf("expected an advance breadcrumb, got %v", *kinds)
	}
	// And a resident phase writes nothing to the blackboard — its
	// product is the conversation, which is already in history.
	if len(cur.State) != 0 {
		t.Errorf("a resident turn must not write state, got %#v", cur.State)
	}
}

func TestCompleteTurn_StayingPutIsTheDefault(t *testing.T) {
	def := triageMachine()
	cur := &MachineCursor{Phase: "answer"}
	ph, _ := def.Phase("answer")
	def.CompleteTurn(cur, ph, nil)
	if cur.Phase != "answer" {
		t.Errorf("a resident phase with no next must stay put, got %s", cur.Phase)
	}
}

func TestMoveTo_KeepTrimsOnlyOnReEntry(t *testing.T) {
	def := MachineDef{Start: "scan", Phases: []MachinePhase{
		{Name: "scan", Prompt: "x", Next: "plan",
			Output: []PipelineField{{Name: "notes", Type: FieldString}}},
		{Name: "plan", Prompt: "y", NextFrom: "to", Next: "reply", Keep: []string{"scan"},
			Output: []PipelineField{{Name: "to", Type: FieldString}}},
		{Name: "reply", Prompt: "z", Resident: true, Next: "plan"},
	}}
	if err := def.Validate(); err != nil {
		t.Fatalf("fixture should validate: %v", err)
	}
	run, _, _ := scriptedRunner(map[string]string{
		"scan": `{"notes":"gathered"}`,
		"plan": `{"to":"reply"}`,
	})
	note, kinds, _ := collectNotes()
	cur := &MachineCursor{}
	app := &AppCore{}

	// Turn 1: scan → plan → reply.
	if _, err := app.AdvanceMachine(context.Background(), def, cur, MachineTurn{Input: "q"}, run, note); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if _, ok := cur.State["plan"]; !ok {
		t.Fatal("plan should have run on turn 1")
	}
	if hasNote(*kinds, "machine_state_trim") {
		t.Error("a first pass through a phase must not trim: nothing has been re-entered yet")
	}

	// The resident phase hands back to plan, which is now a RE-ENTRY:
	// keep names scan, so plan's own earlier result is dropped.
	ph, _ := def.Phase("reply")
	def.CompleteTurn(cur, ph, note)
	if !hasNote(*kinds, "machine_state_trim") {
		t.Errorf("expected a trim breadcrumb on re-entry, got %v", *kinds)
	}
	if _, kept := cur.State["scan"]; !kept {
		t.Error("keep listed scan, so scan's findings must survive")
	}
	if _, gone := cur.State["plan"]; gone {
		t.Error("plan was not in keep, so its earlier result should have been dropped")
	}
}

func TestMoveTo_ResumingTheSamePhaseNeverTrims(t *testing.T) {
	// A machine with a keep list would otherwise shed state on every
	// ordinary turn spent sitting still.
	def := MachineDef{Start: "reply", Phases: []MachinePhase{
		{Name: "reply", Prompt: "z", Resident: true, Keep: []string{"nothing_kept"}},
		{Name: "nothing_kept", Prompt: "x", Next: "reply"},
	}}
	cur := &MachineCursor{Phase: "reply", State: MachineState{"scan": {Text: "keep me"}}}
	ph, _ := def.Phase("reply")
	cur.moveTo("reply", ph, "test", nil, def.accumulatorNames())
	if _, kept := cur.State["scan"]; !kept {
		t.Error("resuming the phase the cursor is already in is not a transition and must not trim")
	}
}

// --- the re-entry guard -----------------------------------------------

// guardedMachine parks in a resident phase that judges each new turn.
func guardedMachine() MachineDef {
	return MachineDef{
		Name: "guarded", Start: "answer",
		Phases: []MachinePhase{
			{Name: "decompose", Desc: "Split the question up.", Prompt: "Break down: {input}", Next: "answer",
				Output: []PipelineField{{Name: "parts", Type: FieldList}}},
			{Name: "answer", Desc: "Reply directly.", Resident: true, Prompt: "Answer.",
				Guard: "the user has moved on to a different subject entirely", GuardTo: "decompose"},
		},
	}
}

func TestGuard_StayIsTheDefaultAndCostsNoTransition(t *testing.T) {
	def := guardedMachine()
	run, calls, _ := scriptedRunner(map[string]string{"guard:answer": `{"stay":true}`})
	note, kinds, _ := collectNotes()
	cur := &MachineCursor{Phase: "answer"}

	ph, err := (&AppCore{}).AdvanceMachine(context.Background(), def, cur, MachineTurn{Input: "and what about the timeline?"}, run, note)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if ph.Name != "answer" || cur.Phase != "answer" {
		t.Fatalf("a stay verdict must leave the cursor alone, got %s / %s", ph.Name, cur.Phase)
	}
	if len(*calls) != 1 || (*calls)[0] != "guard:answer" {
		t.Errorf("expected exactly one guard call and no phase runs, got %v", *calls)
	}
	if hasNote(*kinds, "machine_guard_tripped") {
		t.Error("staying should not report a transition")
	}
}

func TestGuard_TripMovesAndRunsTheTargetChain(t *testing.T) {
	def := guardedMachine()
	run, calls, _ := scriptedRunner(map[string]string{
		"guard:answer": `{"stay":false,"to":"decompose","why":"different subject"}`,
		"decompose":    `{"parts":["new","question"]}`,
	})
	note, kinds, details := collectNotes()
	cur := &MachineCursor{Phase: "answer"}

	ph, err := (&AppCore{}).AdvanceMachine(context.Background(), def, cur, MachineTurn{Input: "actually, unrelated: how do I renew a passport?"}, run, note)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	// Tripped into decompose, which is transient, so the walk carried on
	// and landed back in answer with fresh state.
	if ph.Name != "answer" {
		t.Fatalf("expected the walk to continue to a resident phase, got %s", ph.Name)
	}
	if got := strings.Join(*calls, ","); got != "guard:answer,decompose" {
		t.Errorf("expected the guard then the target phase, got %s", got)
	}
	if parts, ok := cur.State["decompose"].Fields["parts"].([]any); !ok || len(parts) != 2 {
		t.Errorf("the re-entered phase should have written fresh state, got %#v", cur.State["decompose"])
	}
	if !hasNote(*kinds, "machine_guard_tripped") {
		t.Fatalf("a trip must leave a breadcrumb, got %v", *kinds)
	}
	if !strings.Contains(strings.Join(*details, " "), "different subject") {
		t.Errorf("the breadcrumb should carry the guard's reason: %v", *details)
	}
}

func TestGuard_FailsOpen(t *testing.T) {
	// Every uncertain path stays put: the cost of a wrong stay is one
	// turn answered from a slightly stale phase, while the cost of a
	// wrong move is throwing away the state the conversation is built on.
	cases := map[string]string{
		"unparseable verdict":   "I think maybe they moved on?",
		"move with no target":   `{"stay":false,"why":"moved on"}`,
		"move to unknown phase": `{"stay":false,"to":"nowhere","why":"moved on"}`,
	}
	for name, verdict := range cases {
		t.Run(name, func(t *testing.T) {
			// GuardTo empty so an unresolved target has no author fallback
			// to land on either.
			def := guardedMachine()
			def.Phases[1].GuardTo = ""
			def.Start = "answer"
			run, _, _ := scriptedRunner(map[string]string{"guard:answer": verdict})
			note, kinds, _ := collectNotes()
			cur := &MachineCursor{Phase: "answer"}

			ph, err := (&AppCore{}).AdvanceMachine(context.Background(), def, cur, MachineTurn{Input: "hello again"}, run, note)
			if err != nil {
				t.Fatalf("a failing guard must not fail the turn: %v", err)
			}
			if ph.Name != "answer" || cur.Phase != "answer" {
				t.Errorf("expected to stay put, got %s", ph.Name)
			}
			if !hasNote(*kinds, "machine_guard_failed") && !hasNote(*kinds, "machine_guard_unresolved") {
				t.Errorf("a guard that could not be honored must say so, got %v", *kinds)
			}
		})
	}
}

func TestGuard_UnresolvedTargetFallsBackToGuardTo(t *testing.T) {
	// The common small-model failure: fires correctly, then can't name
	// where to go. The author's GuardTo is what catches it.
	def := guardedMachine()
	run, calls, _ := scriptedRunner(map[string]string{
		"guard:answer": `{"stay":false,"why":"totally different"}`,
		"decompose":    `{"parts":["a"]}`,
	})
	note, kinds, _ := collectNotes()
	cur := &MachineCursor{Phase: "answer"}

	if _, err := (&AppCore{}).AdvanceMachine(context.Background(), def, cur, MachineTurn{Input: "new subject"}, run, note); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if !hasNote(*kinds, "machine_guard_tripped") {
		t.Fatalf("expected the guard to trip via guard_to, got %v", *kinds)
	}
	if got := strings.Join(*calls, ","); got != "guard:answer,decompose" {
		t.Errorf("expected the fallback target to run, got %s", got)
	}
}

func TestGuard_OnlyRunsOnAResumedResidentPhase(t *testing.T) {
	// Nothing to re-decide about a phase the walk reached one line ago:
	// guarding it would ask a model whether to undo the routing decision
	// it just made.
	def := guardedMachine()
	def.Start = "decompose"
	run, calls, _ := scriptedRunner(map[string]string{"decompose": `{"parts":["a"]}`})
	note, _, _ := collectNotes()
	cur := &MachineCursor{} // fresh session

	ph, err := (&AppCore{}).AdvanceMachine(context.Background(), def, cur, MachineTurn{Input: "first message"}, run, note)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if ph.Name != "answer" {
		t.Fatalf("expected to walk into answer, got %s", ph.Name)
	}
	for _, c := range *calls {
		if strings.HasPrefix(c, "guard:") {
			t.Errorf("the guard must not run on a phase the walk just entered, calls: %v", *calls)
		}
	}
}

func TestGuard_NotConsultedWithoutOne(t *testing.T) {
	def := triageMachine() // no guards anywhere
	run, calls, _ := scriptedRunner(nil)
	note, _, _ := collectNotes()
	cur := &MachineCursor{Phase: "answer"}

	if _, err := (&AppCore{}).AdvanceMachine(context.Background(), def, cur, MachineTurn{Input: "follow-up"}, run, note); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if len(*calls) != 0 {
		t.Errorf("a phase with no guard must cost nothing on resume, got %v", *calls)
	}
}

// --- mid-turn phase change --------------------------------------------

func TestChangePhase_MovesAndWalksToAReply(t *testing.T) {
	def := guardedMachine()
	run, calls, _ := scriptedRunner(map[string]string{"decompose": `{"parts":["x"]}`})
	note, kinds, _ := collectNotes()
	cur := &MachineCursor{Phase: "answer", State: MachineState{}}

	ph, err := (&AppCore{}).ChangePhase(context.Background(), def, cur, "decompose", MachineTurn{Input: "user pivoted"}, run, note)
	if err != nil {
		t.Fatalf("change: %v", err)
	}
	if ph.Name != "answer" {
		t.Fatalf("a move to a transient phase should walk on to a reply, got %s", ph.Name)
	}
	if got := strings.Join(*calls, ","); got != "decompose" {
		t.Errorf("expected the target phase to run, got %s", got)
	}
	if !hasNote(*kinds, "machine_phase_changed") {
		t.Errorf("a mid-turn move must leave a breadcrumb, got %v", *kinds)
	}
}

func TestChangePhase_RejectsAnUnknownPhase(t *testing.T) {
	def := guardedMachine()
	run, _, _ := scriptedRunner(nil)
	cur := &MachineCursor{Phase: "answer"}

	if _, err := (&AppCore{}).ChangePhase(context.Background(), def, cur, "nowhere", MachineTurn{Input: "x"}, run, nil); err == nil {
		t.Fatal("expected an error naming the unknown phase")
	}
	if cur.Phase != "answer" {
		t.Errorf("a rejected move must not touch the cursor, got %s", cur.Phase)
	}
}

func TestChangePhase_MovingToTheCurrentPhaseIsANoOp(t *testing.T) {
	def := guardedMachine()
	run, calls, _ := scriptedRunner(nil)
	cur := &MachineCursor{Phase: "answer"}

	ph, err := (&AppCore{}).ChangePhase(context.Background(), def, cur, "answer", MachineTurn{Input: "x"}, run, nil)
	if err != nil {
		t.Fatalf("change: %v", err)
	}
	if ph.Name != "answer" || len(*calls) != 0 {
		t.Errorf("moving to where you already are should do nothing, got %s / %v", ph.Name, *calls)
	}
}

// --- templating + the pinned block ------------------------------------

func TestResolvePhaseTemplate(t *testing.T) {
	st := MachineState{
		"route":     {Text: "raw reply", Fields: map[string]any{"target": "deep", "confidence": 0.75}},
		"decompose": {Fields: map[string]any{"parts": []any{"a", "b"}}},
	}
	got := ResolvePhaseTemplate(
		"in={input} first={original_input} prev={prev} whole={state:route} one={state:route.target} n={state:route.confidence} list={state:decompose.parts} miss={state:ghost}",
		PhaseVars{MachineTurn: MachineTurn{Input: "USER"}, Prev: "PREV", Opening: "FIRST"}, st)

	for _, want := range []string{"in=USER", "first=FIRST", "prev=PREV", "whole=raw reply", "one=deep", "n=0.75", `list=["a","b"]`} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in: %s", want, got)
		}
	}
	// An unresolvable reference stays visible rather than blanking, so a
	// mistake degrades to a prompt artifact instead of silent context loss.
	if !strings.Contains(got, "miss={state:ghost}") {
		t.Errorf("unknown refs should be left untouched: %s", got)
	}
}

func TestPhaseBlock_PinsEarlierFindingsAndIsByteStable(t *testing.T) {
	def := triageMachine()
	st := MachineState{
		"decompose": {Fields: map[string]any{"parts": []any{"cost", "timeline"}}},
		"route":     {Fields: map[string]any{"target": "deep"}},
	}
	ph, _ := def.Phase("deep")
	got := def.PhaseBlock(ph, st, PhaseVars{})

	if !strings.Contains(got, "Current phase: deep") {
		t.Errorf("block should name the current phase: %s", got)
	}
	if !strings.Contains(got, "cost") || !strings.Contains(got, "timeline") {
		t.Errorf("block should pin what decompose established: %s", got)
	}
	// The phase's own prior result is left out: handing a phase its own
	// last answer invites it to repeat it.
	if strings.Contains(got, "### deep") {
		t.Errorf("block should not include the current phase's own result: %s", got)
	}
	// Byte-stability is a cache requirement, not a nicety: this block
	// sits in the cacheable system prefix. Map iteration order must not
	// reach it.
	for i := 0; i < 20; i++ {
		if again := def.PhaseBlock(ph, st, PhaseVars{}); again != got {
			t.Fatalf("PhaseBlock is not byte-stable across renders:\n%s\n---\n%s", got, again)
		}
	}
}

func TestPhaseBlock_EmptyStateRendersJustTheDirective(t *testing.T) {
	def := triageMachine()
	ph, _ := def.Phase("answer")
	got := def.PhaseBlock(ph, nil, PhaseVars{})
	if strings.Contains(got, "Established earlier") {
		t.Errorf("nothing established yet, so the section should be absent: %s", got)
	}
	if !strings.Contains(got, "Current phase: answer") {
		t.Errorf("the directive should still render: %s", got)
	}
}

// --- host-facing helpers ----------------------------------------------

// Empty means inherit, the marker means nothing, and both rules are the
// pipeline's rules too — one vocabulary across the two authoring surfaces.
func TestNoToolsMarkerBeatsAnEmptyList(t *testing.T) {
	catalog := []AgentToolDef{{Tool: Tool{Name: "read_file"}}, {Tool: Tool{Name: "web_search"}}}

	if got := PhaseTools(MachinePhase{}, catalog); len(got) != len(catalog) {
		t.Errorf("an empty list inherits the catalog, got %d", len(got))
	}
	if got := PhaseTools(MachinePhase{Tools: []string{NoToolsMarker}}, catalog); got != nil {
		t.Errorf("the marker means none at all, got %+v", got)
	}
	// Left in two states by an author who ticked both: the reading that
	// grants less is the safe one.
	if got := PhaseTools(MachinePhase{Tools: []string{"read_file", NoToolsMarker}}, catalog); got != nil {
		t.Errorf("the marker wins over anything else in the list, got %+v", got)
	}
	if got := resolveStageTools([]string{NoToolsMarker}, catalog); got != nil {
		t.Errorf("a pipeline stage reads the marker the same way, got %+v", got)
	}
}

func TestPhaseToolsAndTier(t *testing.T) {
	catalog := []AgentToolDef{
		{Tool: Tool{Name: "web_search"}},
		{Tool: Tool{Name: "knowledge_search"}},
	}
	if got := PhaseTools(MachinePhase{}, catalog); len(got) != 2 {
		t.Errorf("an empty tool list inherits the whole catalog, got %d", len(got))
	}
	got := PhaseTools(MachinePhase{Tools: []string{"knowledge_search"}}, catalog)
	if len(got) != 1 || got[0].Tool.Name != "knowledge_search" {
		t.Errorf("a phase should narrow the catalog to what it named, got %#v", got)
	}
	if PhaseTier(MachinePhase{}) != TierUnset {
		t.Error("no model set must follow the agent's own routing")
	}
	if PhaseTier(MachinePhase{Model: "lead"}) != LEAD || PhaseTier(MachinePhase{Model: "worker"}) != WORKER {
		t.Error("model should map onto the loop's tier override")
	}
}

// --- storage ----------------------------------------------------------

func TestMachineDef_ThinkOffSurvivesTheStore(t *testing.T) {
	// The regression this guards: kvlite encodes with gob, gob omits
	// zero values, and a *bool pointing at FALSE decodes back as nil.
	// Think is a tri-state STRING for exactly that reason — switch it
	// back to a pointer and "off" silently becomes "inherit" on every
	// read, which is the one setting an author reaches for to make a
	// phase cheap.
	db := &DBase{Store: kvlite.MemStore()}
	saved := SaveMachineDef(db, MachineDef{
		Name: "cheap", Owner: "u", Start: "classify",
		Phases: []MachinePhase{
			{Name: "classify", Prompt: "x", Next: "reply", Think: "off",
				Output: []PipelineField{{Name: "kind", Type: FieldString}}},
			{Name: "reply", Prompt: "y", Resident: true},
		},
	})
	got, ok := LoadMachineDef(db, "u", saved.ID)
	if !ok {
		t.Fatal("machine should load back")
	}
	ph, _ := got.Phase("classify")
	if ph.Think != "off" {
		t.Fatalf("think=off did not survive the store, got %q", ph.Think)
	}
	if PhaseThink(ph, true) {
		t.Error("a phase that says off must turn reasoning off even when the agent says on")
	}
	// And inherit really inherits, in both directions.
	reply, _ := got.Phase("reply")
	if !PhaseThink(reply, true) || PhaseThink(reply, false) {
		t.Error("an empty think must leave the caller's value alone")
	}
}

func TestMachineDefRoundTripAndExport(t *testing.T) {
	def := triageMachine()
	def.Owner = "alice"

	exported := ExportMachine(def)
	if exported.Owner != "" || !exported.Created.IsZero() {
		t.Errorf("export must strip storage/identity metadata, got %#v", exported)
	}
	if exported.ID != def.ID {
		t.Error("the ID travels so an agent+machine bundle keeps its wiring")
	}
	if len(exported.Phases) != len(def.Phases) {
		t.Error("export should carry the recipe intact")
	}
}

// A phase that narrows the catalog must SAY so. Without it the narrowing is
// invisible from inside the turn — an earlier phase's successful calls are
// still in the history, so a name that stops resolving reads as a typo and the
// model burns rounds retrying spellings.
func TestPhaseBlockNamesToolScope(t *testing.T) {
	blk := MachineDef{Name: "triage"}.PhaseBlock(
		MachinePhase{Name: "gather", Tools: []string{"web_search", "fetch_url"}},
		MachineState{}, PhaseVars{})
	for _, want := range []string{"web_search", "fetch_url", "out of scope HERE"} {
		if !strings.Contains(blk, want) {
			t.Errorf("phase block missing %q:\n%s", want, blk)
		}
	}
	// It must NOT claim the list is everything the model may use. A host
	// keeps things past the narrowing that the list does not name — the
	// workflow controls always, and whatever the agent's attachments
	// granted — so a block that said "anything else is out of scope"
	// talked the model out of tools it could see and was entitled to use.
	if strings.Contains(blk, "Anything else is OUT OF SCOPE") {
		t.Errorf("the block should scope by the CATALOG, not by the list alone:\n%s", blk)
	}

	// The explicit "nothing" says so in words. Printing the marker would
	// read as a tool called __none__ and get called.
	none := MachineDef{Name: "triage"}.PhaseBlock(
		MachinePhase{Name: "decide", Tools: []string{NoToolsMarker}},
		MachineState{}, PhaseVars{})
	if !strings.Contains(none, "reaches no tools") || strings.Contains(none, NoToolsMarker) {
		t.Errorf("a no-tools phase should say so plainly:\n%s", none)
	}
	// A phase that names no tools inherits everything, so saying anything
	// about scope there would be false.
	open := MachineDef{Name: "triage"}.PhaseBlock(
		MachinePhase{Name: "gather"}, MachineState{}, PhaseVars{})
	if strings.Contains(open, "Tools in this phase") {
		t.Errorf("unscoped phase claimed a tool scope:\n%s", open)
	}
}
