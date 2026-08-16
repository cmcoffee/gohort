package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// choosingMachine is the same triage shape written the short way: the
// router declares nothing and just names the steps it may choose
// between.
func choosingMachine() MachineDef {
	return MachineDef{
		Name:  "choosing",
		Start: "route",
		Phases: []MachinePhase{
			{
				Name: "route", Desc: "Pick a lane.",
				Prompt:  "Decide which way this goes.",
				Choices: []string{"answer", "deep"}, Next: "answer",
			},
			{Name: "answer", Desc: "Reply directly.", Resident: true, Prompt: "Answer."},
			{Name: "deep", Desc: "Long-form work.", Resident: true, Prompt: "Take your time."},
		},
	}
}

// The point of the whole thing: declaring the destinations is the entire
// routing decision. No field to invent, no field to point at, no list of
// legal values kept in sync by hand.
func TestAStepThatChoosesNeedsNoFieldDeclared(t *testing.T) {
	def := choosingMachine()
	if probs := def.Problems(); len(probs) > 0 {
		t.Fatalf("a machine that routes by choices should be valid: %v", probs)
	}
	ph, _ := def.Phase("route")

	decl := ph.DeclaredOutput()
	if len(decl) != 1 || decl[0].Name != BuiltinNextStep {
		t.Fatalf("the framework should declare %s for it, got %+v", BuiltinNextStep, decl)
	}
	if decl[0].Type != FieldString || !decl[0].Required {
		t.Errorf("the routing field must be a required string, got %+v", decl[0])
	}
	if strings.Join(decl[0].Enum, ",") != "answer,deep" {
		t.Errorf("the choices are the allowed values, got %v", decl[0].Enum)
	}

	// And it actually routes.
	if next, why := def.NextPhase(ph, map[string]any{BuiltinNextStep: "deep"}); next != "deep" {
		t.Errorf("expected deep, got %q (%s)", next, why)
	}
	// An unresolvable choice still falls back rather than stranding a turn.
	next, why := def.NextPhase(ph, map[string]any{BuiltinNextStep: "ghost"})
	if next != "answer" || why == "" {
		t.Errorf("an unknown choice should fall back with a breadcrumb, got %q / %q", next, why)
	}
}

// The instruction naming each destination is generated from the machine,
// so it cannot drift from the step names or their descriptions.
func TestTheChoicesWriteTheirOwnInstruction(t *testing.T) {
	def := choosingMachine()
	ph, _ := def.Phase("route")
	got := def.PhaseInstructions(ph, MachineState{}, PhaseVars{MachineTurn: MachineTurn{Input: "why is it slow?"}})

	for _, want := range []string{BuiltinNextStep, "answer", "deep"} {
		if !strings.Contains(got, want) {
			t.Errorf("the composed instruction never mentions %q:\n%s", want, got)
		}
	}
}

// The trap this closes: a prompt is a TEMPLATE, so one that never says
// {input} used to be sent without the person's message at all — a step
// answering confidently about nothing, which reads as the model ignoring
// its instructions.
func TestAStepThatNeverPlacesTheMessageStillGetsIt(t *testing.T) {
	def := choosingMachine()
	ph, _ := def.Phase("route")
	got := def.PhaseInstructions(ph, MachineState{}, PhaseVars{MachineTurn: MachineTurn{Input: "the disk filled up"}})
	if !strings.Contains(got, "the disk filled up") {
		t.Fatalf("a step whose prompt never places {input} must still receive it:\n%s", got)
	}

	// But an author who placed it keeps their phrasing, with no second copy.
	ph.Prompt = "Rewrite this in one line: {input}"
	got = def.PhaseInstructions(ph, MachineState{}, PhaseVars{MachineTurn: MachineTurn{Input: "the disk filled up"}})
	if n := strings.Count(got, "the disk filled up"); n != 1 {
		t.Errorf("expected the message exactly once when the prompt places it, got %d:\n%s", n, got)
	}
}

// {original_input} is what makes "does this answer what they actually
// asked?" possible on turn nine.
func TestTheOpeningMessageSurvivesLaterTurns(t *testing.T) {
	def := choosingMachine()
	def.Phases[0].Prompt = "They opened with: {original_input}\nNow they say: {input}"

	run, _, prompts := scriptedRunner(map[string]string{
		"route": `{"next_step":"answer"}`,
	})
	note, _, _ := collectNotes()
	cur := &MachineCursor{}

	if _, err := (&AppCore{}).AdvanceMachine(context.Background(), def, cur, MachineTurn{Input: "why is the export failing?"}, run, note); err != nil {
		t.Fatal(err)
	}
	if cur.Opening != "why is the export failing?" {
		t.Fatalf("the opening message should be recorded, got %q", cur.Opening)
	}

	// Turn two arrives somewhere else entirely; send it back through the
	// router the way a guard or change_phase would.
	cur.Phase = "route"
	if _, err := (&AppCore{}).AdvanceMachine(context.Background(), def, cur, MachineTurn{Input: "any update?"}, run, note); err != nil {
		t.Fatal(err)
	}
	last := (*prompts)[len(*prompts)-1]
	if !strings.Contains(last, "They opened with: why is the export failing?") {
		t.Errorf("{original_input} should still be the FIRST message:\n%s", last)
	}
	if !strings.Contains(last, "Now they say: any update?") {
		t.Errorf("{input} should be this turn's message:\n%s", last)
	}
	if cur.Opening != "why is the export failing?" {
		t.Errorf("the opening must not drift to the latest message, got %q", cur.Opening)
	}
}

// Every way of getting the routing wrong is a save-time answer, because
// the definition already knows all of them.
func TestChoicesAreCheckedWhereTheyAreDeclared(t *testing.T) {
	cases := []struct {
		name string
		def  MachineDef
		want string
	}{
		{
			name: "a choice that is not a step",
			def: MachineDef{Name: "m", Phases: []MachinePhase{
				{Name: "route", Prompt: "p", Choices: []string{"ghost"}, Next: "answer"},
				{Name: "answer", Prompt: "a", Resident: true},
			}},
			want: "not a step in this machine",
		},
		{
			name: "a step the conversation waits in cannot decide",
			def: MachineDef{Name: "m", Phases: []MachinePhase{
				{Name: "answer", Prompt: "a", Resident: true, Choices: []string{"answer"}},
			}},
			want: "cannot choose its next step by deciding",
		},
		{
			name: "both mechanisms at once",
			def: MachineDef{Name: "m", Phases: []MachinePhase{
				{Name: "route", Prompt: "p", NextFrom: "lane", Choices: []string{"answer"}, Next: "answer",
					Output: []PipelineField{{Name: "lane", Type: FieldString}}},
				{Name: "answer", Prompt: "a", Resident: true},
			}},
			want: "Keep one",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(strings.Join(c.def.Problems(), "\n"), c.want) {
				t.Errorf("expected a problem mentioning %q, got %v", c.want, c.def.Problems())
			}
		})
	}
}

// A built-in is a primitive: no name to choose, nothing to declare, the
// same meaning in every machine. So the vocabulary is one table, and
// every entry in it has to actually resolve — a variable that is
// documented and not implemented is worse than one that does not exist,
// because the prompt keeps the braces and nobody notices.
func TestEveryBuiltinVariableResolves(t *testing.T) {
	def := choosingMachine()
	def.Phases[0].Prompt = ""
	for _, v := range MachineVars() {
		def.Phases[0].Prompt += v.Ref + "\n"
	}
	ph, _ := def.Phase("route")

	got := def.PhaseInstructions(ph, MachineState{"answer": {Text: "an earlier finding"}}, PhaseVars{
		MachineTurn: MachineTurn{
			Input: "this turn", User: "craig", Agent: "Wren",
			Now: "Mon, January 2, 2026 at 3:04 PM PST",
		},
		Opening: "the opening message",
		Prev:    "the step before",
	})

	for _, v := range MachineVars() {
		if strings.Contains(got, v.Ref) {
			t.Errorf("%s is in the vocabulary but never resolved — it reaches the model as braces:\n%s", v.Ref, got)
		}
	}
	for _, want := range []string{"this turn", "craig", "Wren", "3:04 PM PST",
		"the opening message", "the step before", "an earlier finding", "route", "choosing"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in the composed step:\n%s", want, got)
		}
	}
}

// The asymmetry this closes: a resident step has always been handed
// everything earlier steps established. A transient one was handed
// nothing, so its author hand-copied {state:triage.observation},
// {state:triage.source}, {state:triage.asked} — three references to
// names the definition already knows.
func TestATransientStepIsHandedWhatEarlierStepsEstablished(t *testing.T) {
	def := MachineDef{Name: "m", Start: "look", Phases: []MachinePhase{
		{Name: "look", Prompt: "Go and look.", Next: "weigh",
			Output: []PipelineField{{Name: "found", Type: FieldString, Desc: "what turned up"}}},
		{Name: "weigh", Prompt: "Decide whether it matters.", Next: "reply"},
		{Name: "reply", Prompt: "Say so.", Resident: true},
	}}
	st := MachineState{"look": {Fields: map[string]any{"found": "the pool was resized on Tuesday"}}}
	weigh, _ := def.Phase("weigh")

	got := def.PhaseInstructions(weigh, st, PhaseVars{MachineTurn: MachineTurn{Input: "why is it slow?"}})
	if !strings.Contains(got, "the pool was resized on Tuesday") {
		t.Errorf("a transient step should receive what ran before it:\n%s", got)
	}
	if !strings.Contains(got, "look") {
		t.Errorf("and which step established it:\n%s", got)
	}

	// A prompt that places its own reference keeps its phrasing and does
	// not also get the block — one copy, chosen by the author.
	weigh.Prompt = "Does {state:look.found} explain it?"
	own := def.PhaseInstructions(weigh, st, PhaseVars{MachineTurn: MachineTurn{Input: "why is it slow?"}})
	if strings.Count(own, "the pool was resized on Tuesday") != 1 {
		t.Errorf("expected exactly one copy when the prompt places its own reference:\n%s", own)
	}
}

// A value the framework already holds is not a judgement. Asking a model
// to copy it across costs tokens, invites a paraphrase, and can simply
// be skipped — so a field pointed at a built-in is filled, and never
// reaches the model at all.
func TestAFieldFilledFromAVariableIsNeverAskedFor(t *testing.T) {
	def := MachineDef{Name: "m", Start: "triage", Phases: []MachinePhase{
		{Name: "triage", Prompt: "Work out what kind of turn this is.", Next: "answer",
			Output: []PipelineField{
				{Name: "asked", Type: FieldString, From: "{original_input}",
					Desc: "what they actually want to know"},
				{Name: "kind", Type: FieldString, Desc: "observation or question"},
			}},
		{Name: "answer", Prompt: "Answer.", Resident: true},
	}}
	if probs := def.Problems(); len(probs) > 0 {
		t.Fatalf("a filled field should be valid: %v", probs)
	}
	ph, _ := def.Phase("triage")

	// The contract carries only what is genuinely being asked.
	got := def.PhaseInstructions(ph, MachineState{}, PhaseVars{
		MachineTurn: MachineTurn{Input: "any update?"}, Opening: "why is the export failing?"})
	if strings.Contains(got, `"asked"`) {
		t.Errorf("a filled field must not appear in the contract:\n%s", got)
	}
	if !strings.Contains(got, `"kind"`) {
		t.Errorf("the fields that ARE asked for should still be there:\n%s", got)
	}

	// But it lands on the blackboard exactly like an answered one.
	run, _, _ := scriptedRunner(map[string]string{"triage": `{"kind":"question"}`})
	note, _, _ := collectNotes()
	cur := &MachineCursor{}
	if _, err := (&AppCore{}).AdvanceMachine(context.Background(), def, cur,
		MachineTurn{Input: "why is the export failing?"}, run, note); err != nil {
		t.Fatal(err)
	}
	if got := cur.State["triage"].Fields["asked"]; got != "why is the export failing?" {
		t.Errorf("the field should be filled from the variable, got %#v", got)
	}
	if got := cur.State["triage"].Fields["kind"]; got != "question" {
		t.Errorf("the answered field should survive alongside it, got %#v", got)
	}
}

// A reference that resolves to nothing would leave the field silently
// empty, which is worse than a field nobody set — nobody would know to
// look.
func TestAFieldFilledFromNonsenseIsRefused(t *testing.T) {
	def := MachineDef{Name: "m", Phases: []MachinePhase{
		{Name: "triage", Prompt: "p", Next: "answer",
			Output: []PipelineField{{Name: "asked", Type: FieldString, From: "{whatever}"}}},
		{Name: "answer", Prompt: "a", Resident: true},
	}}
	if !strings.Contains(strings.Join(def.Problems(), "\n"), "not one of the built-in variables") {
		t.Errorf("expected a problem naming the bad reference, got %v", def.Problems())
	}

	// A {state:…} reference is allowed, and checked the same way a
	// prompt's is.
	def.Phases[0].Output[0].From = "{state:ghost.field}"
	if !strings.Contains(strings.Join(def.Problems(), "\n"), "unknown step") {
		t.Errorf("expected the state reference to be checked, got %v", def.Problems())
	}
}

// Everything a variable holds is text — a message, a name, a rendered
// clock. A field filled from one and declared a list describes something
// that cannot happen, so it is stored as text and the author is told,
// rather than the machine being refused over a mistake it survives.
func TestAFilledFieldIsAlwaysText(t *testing.T) {
	ph := MachinePhase{Name: "triage", Prompt: "p", Next: "answer", Output: []PipelineField{
		{Name: "parts", Type: FieldList, From: "{original_input}"},
		{Name: "kind", Type: FieldList},
	}}
	got := ph.DeclaredOutput()
	if got[0].Type != FieldString {
		t.Errorf("a filled field should be text, got %q", got[0].Type)
	}
	if got[1].Type != FieldList {
		t.Errorf("a field the step works out keeps its declared type, got %q", got[1].Type)
	}
	// And the author hears about it, as advice rather than a refusal.
	def := MachineDef{Name: "m", Phases: []MachinePhase{ph, {Name: "answer", Prompt: "a", Resident: true}}}
	if probs := def.Problems(); len(probs) > 0 {
		t.Errorf("this should not block the machine: %v", probs)
	}
	if !strings.Contains(strings.Join(def.Advice(), "\n"), "will hold text") {
		t.Errorf("the author should be told what it actually holds, got %v", def.Advice())
	}
}

// {input} and {original_input} are the WORDS of a message. A turn that
// arrived as a photo and nothing else has none, and a field that goes
// silently empty is the failure this whole design keeps trying to
// prevent — so it leaves a breadcrumb.
func TestAFillThatResolvesToNothingSaysSo(t *testing.T) {
	def := MachineDef{Name: "m", Start: "triage", Phases: []MachinePhase{
		{Name: "triage", Prompt: "Work it out.", Next: "answer",
			Output: []PipelineField{{Name: "asked", Type: FieldString, From: "{original_input}"}}},
		{Name: "answer", Prompt: "Answer.", Resident: true},
	}}
	run, _, _ := scriptedRunner(map[string]string{"triage": "ok"})
	note, kinds, _ := collectNotes()
	cur := &MachineCursor{}
	if _, err := (&AppCore{}).AdvanceMachine(context.Background(), def, cur,
		MachineTurn{Input: ""}, run, note); err != nil {
		t.Fatal(err)
	}
	if !hasNote(*kinds, "machine_static_empty") {
		t.Errorf("an empty fill should leave a breadcrumb, got %v", *kinds)
	}
}

// The name IS the choice, wherever the machine came from. The editor
// offers the wheel, but the machine tool writes these too, extras/ ships
// them, and imports carry them — a rule enforced at one door is a rule
// that is not true.
func TestAFieldNamedAfterABuiltinIsFilledWhoeverWroteIt(t *testing.T) {
	// No "from" anywhere: this is what an imported file or a tool call
	// looks like.
	var def MachineDef
	if err := json.Unmarshal([]byte(`{
		"name": "m", "start": "triage",
		"phases": [
			{"name": "triage", "prompt": "Work out what kind of turn this is.", "next": "answer",
			 "output": [{"name": "original_input", "type": "string"},
			            {"name": "kind", "type": "string", "desc": "observation or question"}]},
			{"name": "answer", "prompt": "Answer.", "resident": true}
		]}`), &def); err != nil {
		t.Fatal(err)
	}
	ph, _ := def.Phase("triage")

	if len(ph.StaticFields()) != 1 || ph.StaticFields()[0].From != "{original_input}" {
		t.Fatalf("a field named after a built-in should be filled, got %+v", ph.StaticFields())
	}
	got := def.PhaseInstructions(ph, MachineState{}, PhaseVars{Opening: "why is it slow?"})
	if strings.Contains(got, `"original_input"`) {
		t.Errorf("it must not be asked for as well:\n%s", got)
	}
	if !strings.Contains(got, `"kind"`) {
		t.Errorf("the step's own field should still be asked for:\n%s", got)
	}

	run, _, _ := scriptedRunner(map[string]string{"triage": `{"kind":"question"}`})
	note, _, _ := collectNotes()
	cur := &MachineCursor{}
	if _, err := (&AppCore{}).AdvanceMachine(context.Background(), def, cur,
		MachineTurn{Input: "why is it slow?"}, run, note); err != nil {
		t.Fatal(err)
	}
	if got := cur.State["triage"].Fields["original_input"]; got != "why is it slow?" {
		t.Errorf("the field should hold the opening message, got %#v", got)
	}
}

// A resident step's prompt is pinned in the cacheable prefix, so only
// values that hold still may resolve there. The stable ones do; the
// volatile ones are refused at save time — the alternative was a prompt
// that said {user} and got a blank.
func TestAResidentPromptResolvesTheStableVariablesOnly(t *testing.T) {
	def := MachineDef{Name: "helpdesk", Start: "reply", Phases: []MachinePhase{
		{Name: "reply", Resident: true,
			Prompt: "You are talking to {user} through {agent}. They opened with: {original_input}. This is step {step} of {machine}."},
	}}
	if probs := def.Problems(); len(probs) > 0 {
		t.Fatalf("stable variables should be valid in a resident prompt: %v", probs)
	}
	got := def.PhaseBlock(def.Phases[0], MachineState{}, PhaseVars{
		MachineTurn: MachineTurn{User: "craig", Agent: "Wren",
			Input: "MUST-NOT-APPEAR", Now: "MUST-NOT-APPEAR"},
		Opening: "why is the export failing?",
		Prev:    "MUST-NOT-APPEAR",
	})
	for _, want := range []string{"craig", "Wren", "why is the export failing?", "step reply of helpdesk"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in the resident block:\n%s", want, got)
		}
	}
	// The volatile fields are zeroed by PhaseBlock itself, not trusted to
	// the caller — one call site passing a clock in would silently break
	// the prompt cache every turn.
	if strings.Contains(got, "MUST-NOT-APPEAR") {
		t.Errorf("a volatile value leaked into the pinned block:\n%s", got)
	}

	// And each volatile token is a save-time answer, with its own reason.
	for _, tok := range []string{"{input}", "{prev}", "{now}", "{established}"} {
		bad := def
		bad.Phases = []MachinePhase{{Name: "reply", Resident: true, Prompt: "use " + tok + " here"}}
		if probs := bad.Problems(); len(probs) == 0 {
			t.Errorf("%s in a resident prompt should be refused", tok)
		}
	}
}
