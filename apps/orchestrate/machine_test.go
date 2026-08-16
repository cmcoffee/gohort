package orchestrate

// Turn-side machine wiring (machine.go). The core driver's walk is
// covered in core/machine_test.go; what these pin down is what
// orchestrate owns — pinning the def to the session, persisting the
// cursor, where the phase lands in the prompt, narrowing the catalog,
// the tier override, and the resident handoff.
//
// Every fixture starts on a RESIDENT phase, so no transient phase runs
// and no LLM is needed. That is also the case that matters most: it is
// what turns 2..N of every machine conversation actually do.

import (
	"bytes"
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func machineTurnFixture(t *testing.T, def MachineDef) (*chatTurn, MachineDef) {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	app := &OrchestrateApp{}
	app.DB = root

	def.Owner = "u"
	if err := def.Validate(); err != nil {
		t.Fatalf("fixture machine should validate: %v", err)
	}
	saved := SaveMachineDef(udb, def)

	sess := &ChatSession{ID: "s1", AgentID: "a1"}
	if stored, err := saveChatSession(udb, *sess); err == nil {
		*sess = stored
	}
	turn := &chatTurn{
		app: app, ctx: context.Background(), user: "u", udb: udb,
		agent:   AgentRecord{ID: "a1", Name: "Wren", Owner: "u", Machine: saved.ID},
		session: sess,
	}
	return turn, saved
}

// residentMachine parks on its first phase and stays there.
func residentMachine() MachineDef {
	return MachineDef{
		Name: "desk", Start: "answer",
		Phases: []MachinePhase{
			{Name: "answer", Desc: "Reply directly.", Resident: true,
				Prompt: "Answer plainly, using what is settled below."},
		},
	}
}

func TestEnterMachine_NoMachineIsInert(t *testing.T) {
	turn, _ := machineTurnFixture(t, residentMachine())
	turn.agent.Machine = "" // the every-agent-today case

	m := turn.enterMachine("hello")
	if m.on {
		t.Fatal("an agent with no machine must not enter one")
	}
	if m.Block() != "" {
		t.Error("no machine must contribute nothing to the prompt")
	}
	if m.Tier() != TierUnset {
		t.Error("no machine must leave tier routing alone")
	}
	if m.Think(true) != true || m.Think(false) != false {
		t.Error("no machine must leave the think setting alone")
	}
	catalog := []AgentToolDef{{Tool: Tool{Name: "web_search"}}}
	if got := m.Tools(catalog); len(got) != 1 {
		t.Error("no machine must leave the catalog alone")
	}
}

func TestEnterMachine_PinsTheDefAndParksTheCursor(t *testing.T) {
	turn, def := machineTurnFixture(t, residentMachine())

	m := turn.enterMachine("what's the status?")
	if !m.on {
		t.Fatal("expected the machine to run")
	}
	if m.Name() != "answer" {
		t.Fatalf("expected to park on answer, got %s", m.Name())
	}
	if turn.session.MachineID != def.ID {
		t.Errorf("the session should pin the machine it started on, got %q", turn.session.MachineID)
	}
	if turn.session.Phase != "answer" {
		t.Errorf("the cursor should persist, got %q", turn.session.Phase)
	}
	// And it survives a reload, which is the whole point of persisting it.
	if reloaded, ok := loadChatSession(turn.udb, "a1", "s1"); !ok {
		t.Fatal("session should reload")
	} else if reloaded.Phase != "answer" || reloaded.MachineID != def.ID {
		t.Errorf("phase state did not survive the round trip: %+v", reloaded)
	}
}

func TestEnterMachine_RepointingTheAgentLeavesLiveSessionsAlone(t *testing.T) {
	// The pin exists so re-pointing an agent reshapes NEW conversations
	// and leaves ones already in flight where they are.
	turn, first := machineTurnFixture(t, residentMachine())
	turn.enterMachine("first turn")

	other := SaveMachineDef(turn.udb, MachineDef{
		Name: "other", Owner: "u", Start: "b",
		Phases: []MachinePhase{{Name: "b", Prompt: "different", Resident: true}},
	})
	turn.agent.Machine = other.ID

	m := turn.enterMachine("second turn")
	if m.def.ID != first.ID {
		t.Errorf("a live session should keep the machine it started on, got %s", m.def.Name)
	}
	if m.Name() != "answer" {
		t.Errorf("expected to stay in the original machine's phase, got %s", m.Name())
	}
}

func TestEnterMachine_MissingMachineDegradesToAPlainTurn(t *testing.T) {
	turn, _ := machineTurnFixture(t, residentMachine())
	turn.agent.Machine = "no-such-machine"
	turn.session.MachineID = ""

	m := turn.enterMachine("hello")
	if m.on {
		t.Fatal("a missing machine must not run")
	}
	// The turn still happens; the breadcrumb is what says why it was
	// different from what the author configured.
	var diags []SessionDiag
	turn.udb.Get(sessionDiagTable, "a1:s1", &diags)
	if len(diags) == 0 {
		t.Error("a missing machine must leave a breadcrumb on the session trail")
	}
}

func TestMachineBlock_CarriesTheDirectiveAndPinnedFindings(t *testing.T) {
	def := MachineDef{
		Name: "triage", Start: "answer",
		Phases: []MachinePhase{
			{Name: "decompose", Prompt: "Split {input}.", Next: "answer",
				Output: []PipelineField{{Name: "parts", Type: FieldList}}},
			{Name: "answer", Desc: "Reply.", Resident: true, Prompt: "Work from what is settled."},
		},
	}
	turn, _ := machineTurnFixture(t, def)
	turn.session.Phase = "answer"
	turn.session.MachineState = MachineState{
		"decompose": {Fields: map[string]any{"parts": []any{"cost", "timeline"}}},
	}

	m := turn.enterMachine("follow-up")
	block := m.Block()
	if !strings.Contains(block, "Current phase: answer") {
		t.Errorf("block should name the phase: %s", block)
	}
	if !strings.Contains(block, "cost") {
		t.Errorf("block should pin what an earlier phase established: %s", block)
	}
	// Byte-stability across turns is what keeps the cached prefix valid.
	if again := m.Block(); again != block {
		t.Error("the phase block must be byte-stable across renders")
	}
}

func TestMachinePhase_NarrowsToolsAndPinsTierAndThink(t *testing.T) {
	def := MachineDef{
		Name: "narrow", Start: "answer",
		Phases: []MachinePhase{
			{Name: "answer", Resident: true, Prompt: "Reply.",
				Tools: []string{"knowledge_search"}, Model: "lead", Think: "off"},
		},
	}
	turn, _ := machineTurnFixture(t, def)
	m := turn.enterMachine("q")

	catalog := []AgentToolDef{
		{Tool: Tool{Name: "web_search"}},
		{Tool: Tool{Name: "knowledge_search"}},
	}
	got := m.Tools(catalog)
	if len(got) != 1 || got[0].Tool.Name != "knowledge_search" {
		t.Errorf("the phase should narrow the catalog to what it named, got %#v", got)
	}
	if m.Tier() != LEAD {
		t.Error("the phase should pin the tier")
	}
	if m.Think(true) {
		t.Error("the phase's think setting is the most specific and should win")
	}
}

func TestChangePhaseTool_OfferedOnlyWhenThereIsAnExit(t *testing.T) {
	turn, _ := machineTurnFixture(t, residentMachine()) // one phase
	turn.enterMachine("hi")
	if turn.hasMachineExit() {
		t.Error("a one-phase machine has nowhere to go; the tool must not be offered")
	}

	two, _ := machineTurnFixture(t, MachineDef{
		Name: "two", Start: "a",
		Phases: []MachinePhase{
			{Name: "a", Resident: true, Prompt: "x"},
			{Name: "b", Resident: true, Prompt: "y"},
		},
	})
	two.enterMachine("hi")
	if !two.hasMachineExit() {
		t.Error("a machine with somewhere to go should offer the tool")
	}
	// And an agent with no machine at all never sees it.
	two.machine = turnMachine{}
	if two.hasMachineExit() {
		t.Error("no machine means no change_phase")
	}
}

func TestChangePhaseTool_MovesTheCursorAndReturnsTheNewDirective(t *testing.T) {
	turn, _ := machineTurnFixture(t, MachineDef{
		Name: "two", Start: "intake",
		Phases: []MachinePhase{
			{Name: "intake", Desc: "Find out what they want.", Resident: true, Prompt: "Ask."},
			{Name: "work", Desc: "Do the job.", Resident: true, Prompt: "Build it."},
		},
	})
	turn.enterMachine("hi")

	out, err := turn.changePhaseToolDef().Handler(map[string]any{
		"phase": "work", "why": "they told me what they want",
	})
	if err != nil {
		t.Fatalf("change_phase: %v", err)
	}
	if turn.session.Phase != "work" {
		t.Fatalf("the cursor should have moved, got %q", turn.session.Phase)
	}
	if turn.machine.Name() != "work" {
		t.Errorf("the rest of the turn should run under the new phase, got %q", turn.machine.Name())
	}
	// The result has to carry the new directive: the system prompt still
	// holds the old one, and the tool result is the only thing that can
	// supersede it inside this turn.
	if !strings.Contains(out, "Build it.") || !strings.Contains(out, "out of date") {
		t.Errorf("the result should replace the stale directive, got: %s", out)
	}
	// It survives to the next turn too.
	if reloaded, ok := loadChatSession(turn.udb, "a1", "s1"); !ok || reloaded.Phase != "work" {
		t.Errorf("the move must persist, got %+v", reloaded)
	}
}

func TestChangePhaseTool_RefusesUnknownPhasesAndThrashing(t *testing.T) {
	turn, _ := machineTurnFixture(t, MachineDef{
		Name: "three", Start: "a",
		Phases: []MachinePhase{
			{Name: "a", Resident: true, Prompt: "x"},
			{Name: "b", Resident: true, Prompt: "y"},
			{Name: "c", Resident: true, Prompt: "z"},
		},
	})
	turn.enterMachine("hi")
	tool := turn.changePhaseToolDef()

	if _, err := tool.Handler(map[string]any{"phase": "ghost", "why": "x"}); err == nil {
		t.Error("expected a refusal naming the available phases")
	} else if !strings.Contains(err.Error(), "b, c") && !strings.Contains(err.Error(), "a, b, c") {
		t.Errorf("the refusal should list the real choices, got: %v", err)
	}
	if turn.session.Phase != "a" {
		t.Fatalf("a refused move must not touch the cursor, got %q", turn.session.Phase)
	}

	for i := 0; i < maxPhaseChangesPerTurn; i++ {
		want := []string{"b", "c"}[i%2]
		if _, err := tool.Handler(map[string]any{"phase": want, "why": "moved on"}); err != nil {
			t.Fatalf("change %d should succeed: %v", i+1, err)
		}
	}
	if _, err := tool.Handler(map[string]any{"phase": "a", "why": "again"}); err == nil {
		t.Error("expected the cap to refuse a third change in one turn")
	}
}

func TestCompleteMachine_OneBeatPhaseHandsOffAfterItsTurn(t *testing.T) {
	def := MachineDef{
		Name: "intakeflow", Start: "intake",
		Phases: []MachinePhase{
			{Name: "intake", Resident: true, Prompt: "Ask what they need.", Next: "work"},
			{Name: "work", Resident: true, Prompt: "Do it."},
		},
	}
	turn, _ := machineTurnFixture(t, def)

	m := turn.enterMachine("hi")
	if m.Name() != "intake" {
		t.Fatalf("expected to start in intake, got %s", m.Name())
	}
	turn.completeMachine(m)
	if turn.session.Phase != "work" {
		t.Fatalf("the one-beat phase should hand off after its turn, cursor is %q", turn.session.Phase)
	}
	// Next turn opens in work, and stays there.
	m2 := turn.enterMachine("go on")
	if m2.Name() != "work" {
		t.Fatalf("expected the next turn to open in work, got %s", m2.Name())
	}
	turn.completeMachine(m2)
	if turn.session.Phase != "work" {
		t.Errorf("a phase with no next must stay put, got %q", turn.session.Phase)
	}
}

// {original_input} on a LIVE session: the cursor is rebuilt from the
// session every turn, so without a persisted home the "written once"
// guard re-latched onto the CURRENT message — the exact lie the
// variable's documentation warns about, and a cache-buster for any
// resident prompt placing it.
func TestOpeningSurvivesAcrossLiveTurns(t *testing.T) {
	turn, _ := machineTurnFixture(t, residentMachine())
	turn.enterMachine("why is the export failing?")
	if turn.session.MachineOpening != "why is the export failing?" {
		t.Fatalf("the opening should persist on the session, got %q", turn.session.MachineOpening)
	}

	// A later turn on the SAME session rebuilds the cursor from the
	// session fields; the opening must ride back rather than re-latch.
	turn.machine = turnMachine{}
	turn.enterMachine("any update?")
	if turn.session.MachineOpening != "why is the export failing?" {
		t.Errorf("the opening drifted to the latest message: %q", turn.session.MachineOpening)
	}
	if turn.machine.vars.Opening != "why is the export failing?" {
		t.Errorf("the resident block should see the ORIGINAL opening, got %q", turn.machine.vars.Opening)
	}
}

// A machine's steps run at the HEAD of a turn, before the persona is
// assembled and before a single word reaches the person — two model
// calls of silence for a decompose-then-route machine. Each one says
// what it is doing first, in the author's own words.
func TestEveryStepSaysWhatItIsDoing(t *testing.T) {
	cases := map[string]struct {
		in   MachinePhase
		want string
	}{
		"the author's description": {
			MachinePhase{Name: "triage", Desc: "Work out what kind of turn this is."},
			"triage: Work out what kind of turn this is…",
		},
		"a step that never got one": {
			MachinePhase{Name: "hunch"},
			"Working through hunch…",
		},
		// A guard is a call paid on EVERY turn spent in a step, and it
		// arrives as a synthetic phase. Naming it is the only honest
		// account of where that second went.
		"a guard": {
			MachinePhase{Name: "guard:answer", Desc: "unused"},
			"Checking whether this is still the same job…",
		},
	}
	for name, c := range cases {
		if got := phaseStatusLine(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}

// And the runner actually emits it — before the call, not after, or the
// line arrives with the answer it was meant to cover for.
func TestPhaseRunnerAnnouncesBeforeItRuns(t *testing.T) {
	turn, _ := machineTurnFixture(t, residentMachine())
	var buf bytes.Buffer
	turn.sse = &sseWriter{live: &buf}
	turn.app.LLM = &stubLLM{reply: "worked it out"}

	run := turn.phaseRunner()
	if _, err := run(context.Background(),
		MachinePhase{Name: "triage", Desc: "Work out what kind of turn this is."}, "do it"); err != nil {
		t.Fatalf("run: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "triage: Work out what kind of turn this is") {
		t.Errorf("the step should announce itself:\n%s", got)
	}
	if !strings.Contains(got, `"type":"status"`) {
		t.Errorf("it should ride the activity surface's status channel:\n%s", got)
	}
}
