package orchestrate

// machine(action="run"): the way Builder tries an unattended machine it has
// just built. Before it existed every route it reached for failed: an unknown
// action, a one-off schedule whose result never came back, a dispatch list
// fixed at the start of the turn, and a wrapping pipeline with no machine
// runner.

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/oddjob/core"
)

// machineRunTurn is a turn whose owner can actually run a machine: a catalog
// to resolve (machineCatalogWorld) and a scripted model behind the steps.
func machineRunTurn(t *testing.T, llm *FakeLLM) *chatTurn {
	t.Helper()
	app, udb := machineCatalogWorld(t)
	app.LLM = llm
	return &chatTurn{app: app, user: "owner", udb: udb, ctx: context.Background(),
		agent: AgentRecord{ID: "builder", Name: "Builder", Owner: "owner"}}
}

// oneStepRun is the smallest unattended machine: one model step that reaches
// nothing and hands off nowhere, so its reply is the run's result.
func oneStepRun(name string) MachineDef {
	return MachineDef{Owner: "owner", Name: name, Start: "answer", Unattended: true, Phases: []MachinePhase{
		{Name: "answer", Prompt: "Summarise {input}.", Reach: ReachNone},
	}}
}

func TestTheMachineToolRunsAnUnattendedMachine(t *testing.T) {
	llm := &FakeLLM{Turns: []FakeTurn{{Content: "a summary of the example", Repeat: true}}}
	turn := machineRunTurn(t, llm)
	SaveMachineDef(turn.udb, oneStepRun("Summary"))
	tool := turn.machineGroupedToolDef()

	for _, action := range []string{"run", "try"} {
		out, err := tool.Handler(context.Background(), map[string]any{"action": action, "name": "Summary", "input": "the example"})
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		if !strings.Contains(out, "a summary of the example") {
			t.Fatalf("%s returned %q; the run's result is the finishing step's reply", action, out)
		}
	}
	// The input reached the step: a try on something other than the
	// person's example proves nothing about their example.
	var sent strings.Builder
	for _, m := range llm.LastSent() {
		sent.WriteString(m.Content)
	}
	if !strings.Contains(sent.String(), "the example") {
		t.Errorf("the input never reached the step:\n%s", sent.String())
	}
	// It is a live run while it goes, so the Monitor can Stop it, and it is
	// recorded as finished once it has.
	runs := turn.app.runsRegistry().Activity("owner")
	if len(runs) != 2 || runs[0].Kind != "machine" || runs[0].Status != RunStatusCompleted {
		t.Fatalf("each try should be a live machine run, recorded as completed: %+v", runs)
	}

	// And the tool says it can: a model learns an action from the spec.
	if !strings.Contains(machineHelpText, `machine(action="run", name, input)`) {
		t.Error("the help never tells Builder to try what it built")
	}
	if !hasCap(tool.Tool.Caps, CapNetwork) {
		t.Error("run reaches whatever the steps reach; the tool must carry CapNetwork for private mode")
	}
}

func TestRunningAConversationalMachineSaysHowToMakeItRun(t *testing.T) {
	turn := machineRunTurn(t, &FakeLLM{})
	SaveMachineDef(turn.udb, MachineDef{Owner: "owner", Name: "Chat", Start: "answer", Phases: []MachinePhase{
		{Name: "answer", Prompt: "reply", Resident: true},
	}})
	_, err := turn.machineGroupedToolDef().Handler(context.Background(), map[string]any{"action": "run", "name": "Chat", "input": "hi"})
	if err == nil {
		t.Fatal("a machine that converses was run")
	}
	for _, want := range []string{`"answer"`, "unattended: true", "attach_to_agents"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal is missing %q:\n%v", want, err)
		}
	}
}

func TestRunningAHalfBuiltMachineListsEverythingOutstanding(t *testing.T) {
	llm := &FakeLLM{}
	turn := machineRunTurn(t, llm)
	def := oneStepRun("Broken")
	def.Phases[0].Next = "ghost"
	def.Start = "nowhere"
	SaveMachineDef(turn.udb, def)
	_, err := turn.machineGroupedToolDef().Handler(context.Background(), map[string]any{"action": "run", "name": "Broken", "input": "x"})
	if err == nil || !strings.Contains(err.Error(), "ghost") || !strings.Contains(err.Error(), "nowhere") {
		t.Fatalf("every finding should be named, not just the first: %v", err)
	}
	if llm.Calls() != 0 {
		t.Error("a machine that cannot run spent a model call")
	}
}

// A run that stops reports where, and keeps what it had produced: the
// finishing step skipping is the live case (a lookup's raw result came back
// as a finished run's answer).
func TestARunEndingOnASkippedStepIsAFailureWithItsPartial(t *testing.T) {
	llm := &FakeLLM{Turns: []FakeTurn{{Content: `{"isbn": ""}`, Repeat: true}}}
	turn := machineRunTurn(t, llm)
	SaveMachineDef(turn.udb, MachineDef{Owner: "owner", Name: "Two", Start: "find", Unattended: true, Phases: []MachinePhase{
		{Name: "find", Prompt: "Find the ISBN in {input}.", Reach: ReachNone, Next: "send",
			Output: []PipelineField{{Name: "isbn", Type: FieldString}}},
		// A tool step whose argument comes out empty skips.
		{Name: "send", Tool: "geo", Args: map[string]string{"q": "{state:find.isbn}"}},
	}})
	_, err := turn.machineGroupedToolDef().Handler(context.Background(), map[string]any{"action": "run", "name": "Two", "input": "x"})
	if err == nil {
		t.Fatal("a run whose last step did not apply reported success")
	}
	if !strings.Contains(err.Error(), "the last step, send, did not apply") || !strings.Contains(err.Error(), "isbn") {
		t.Fatalf("the failure should name the step and carry the partial:\n%v", err)
	}
}

// A pipeline run from the pipeline tool reaches its machine stages. It had no
// runner, so the stage failed "started without a machine runner" while the
// same pipeline ran from its own page.
func TestThePipelineToolRunsAMachineStage(t *testing.T) {
	llm := &FakeLLM{Turns: []FakeTurn{{Content: "machine result", Repeat: true}}}
	turn := machineRunTurn(t, llm)
	SaveMachineDef(turn.udb, oneStepRun("Inner"))
	SavePipelineDef(turn.udb, PipelineDef{Owner: "owner", Name: "Outer", Stages: []PipelineStage{
		{Name: "run_it", Kind: StageMachine, Machine: "Inner", Prompt: "{input}"},
	}})
	out, err := turn.pipelineGroupedToolDef().Handler(context.Background(), map[string]any{"action": "run", "name": "Outer", "input": "go"})
	if err != nil {
		t.Fatalf("pipeline run: %v", err)
	}
	if !strings.Contains(out, "machine result") {
		t.Fatalf("result %q; the machine stage should have run", out)
	}
}

func TestOutputTypeSynonymsAreTheTypesTheyMean(t *testing.T) {
	fields, err := parsePipelineFields(1, []any{
		map[string]any{"name": "items", "type": "array"},
		map[string]any{"name": "n", "type": "Integer"},
		map[string]any{"name": "x", "type": "float"},
		map[string]any{"name": "ok", "type": "boolean"},
		map[string]any{"name": "m", "type": "dict"},
		map[string]any{"name": "o", "type": "map"},
		map[string]any{"name": "s", "type": "string"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []PipelineFieldType{FieldList, FieldNumber, FieldNumber, FieldBool, FieldObject, FieldObject, FieldString}
	for i, f := range fields {
		if f.Type != want[i] {
			t.Errorf("%s: type %q, want %q", f.Name, f.Type, want[i])
		}
	}
	// Machine steps declare output through the same decoder.
	phases, err := parseMachinePhases([]any{map[string]any{"name": "a", "prompt": "p",
		"output": []any{map[string]any{"name": "parts", "type": "array"}}}})
	if err != nil || phases[0].Output[0].Type != FieldList {
		t.Fatalf("a machine step's array did not become a list: %v %+v", err, phases)
	}
}

func TestAStepWithAKindIsToldWhatSetsOne(t *testing.T) {
	_, err := parseMachinePhases([]any{map[string]any{"name": "a", "kind": "tool", "prompt": "p"}})
	if err == nil || !strings.Contains(err.Error(), "A machine step has no kind") || !strings.Contains(err.Error(), "tool (with args)") {
		t.Fatalf("kind on a step: %v", err)
	}
}

// update_phase handed a machine-level field says where it goes, and saves
// nothing: a reply that said "Updated" about part of a call read as all of it.
func TestUpdatePhaseNamesMachineLevelFields(t *testing.T) {
	turn := machineToolFixture(t)
	tool := turn.machineGroupedToolDef()
	if _, err := tool.Handler(context.Background(), map[string]any{"action": "create", "name": "M", "phases": toolPhases()}); err != nil {
		t.Fatal(err)
	}
	_, err := tool.Handler(context.Background(), map[string]any{"action": "update_phase", "name": "M", "phase": "answer",
		"prompt": "new prompt", "unattended": true, "machine_deny": []any{"x"}})
	if err == nil {
		t.Fatal("machine-level fields were accepted by update_phase")
	}
	for _, want := range []string{"machine_deny, unattended", "whole machine", `action="update"`, "Drop machine_deny, unattended"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message is missing %q:\n%v", want, err)
		}
	}
	def, _ := turn.findMachine(map[string]any{"name": "M"})
	if def.Phases[1].Prompt == "new prompt" {
		t.Error("the step's prompt was written by a refused call")
	}
}
