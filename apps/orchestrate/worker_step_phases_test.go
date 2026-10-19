package orchestrate

// runWorkerStep is the order of its phases (workerStep). What a step could
// lose without any other test noticing: the owner's own tools in the worker's
// catalog, the private-mode cut of what dials out, the step's failure, and
// what the step made reaching the turn after its session is gone.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func workerTurn(t *testing.T, turns ...FakeTurn) (*chatTurn, *FakeLLM) {
	t.Helper()
	app, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	fake := &FakeLLM{Turns: turns}
	app.LLM, app.LeadLLM = fake, fake
	a, err := saveAgent(udb, AgentRecord{Name: "Scout", Owner: user, OrchestratorPrompt: "You find things."})
	if err != nil {
		t.Fatal(err)
	}
	return &chatTurn{app: app, agent: a, user: user, udb: udb, ctx: context.Background()}, fake
}

func offeredTo(fake *FakeLLM, i int) []string {
	var names []string
	for _, tl := range fake.Config(i).Tools {
		names = append(names, tl.Name)
	}
	return names
}

func TestAWorkerStepGetsTheOwnersToolsAndPrivateCutsWhatDialsOut(t *testing.T) {
	turn, fake := workerTurn(t, FakeTurn{Content: "done", Repeat: true})
	if err := AdminPersistTempTool(turn.udb, turn.user, TempTool{Name: "lookup_ticket", Description: "Look up a ticket.", CommandTemplate: "echo {id}",
		Params: map[string]ToolParam{"id": {Type: "string"}}}); err != nil {
		t.Fatal(err)
	}
	if err := AdminPersistTempTool(turn.udb, turn.user, TempTool{Name: "weather_api", Description: "The weather.", Mode: TempToolModeAPI, CommandTemplate: "https://weather.example/today"}); err != nil {
		t.Fatal(err)
	}
	// Notes on, and another agent to hand work to: the framework tools a
	// worker gets on top of the pool.
	turn.agent.EnableNotes = true
	if _, err := saveAgent(turn.udb, AgentRecord{Name: "Helper", Owner: turn.user, OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	step := PlanStep{ID: 1, Title: "Look it up", WorkerBrief: "Find the ticket."}
	if _, err := turn.runWorkerStep(nil, step, "find ticket 7", nil); err != nil {
		t.Fatal(err)
	}
	got := offeredTo(fake, 0)
	for _, want := range []string{"lookup_ticket", "weather_api", "recall_about", "update_notes", "agents"} {
		if !slices.Contains(got, want) {
			t.Fatalf("the worker should be handed %s: %v", want, got)
		}
	}

	turn.privateMode = true
	before := fake.Calls()
	if _, err := turn.runWorkerStep(nil, step, "find ticket 7", nil); err != nil {
		t.Fatal(err)
	}
	got = offeredTo(fake, before)
	for _, gone := range []string{"weather_api", "agents"} {
		if slices.Contains(got, gone) {
			t.Errorf("a Private turn's worker was handed %s, which dials out: %v", gone, got)
		}
	}
	if !slices.Contains(got, "lookup_ticket") {
		t.Errorf("Private cut a tool that does not dial out: %v", got)
	}
}

func TestAWorkerStepsFailureIsItsError(t *testing.T) {
	turn, _ := workerTurn(t, FakeTurn{Err: errors.New("the model is down"), Repeat: true})
	if _, err := turn.runWorkerStep(nil, PlanStep{ID: 1, Title: "Look", WorkerBrief: "Look."}, "x", nil); err == nil || !strings.Contains(err.Error(), "the model is down") {
		t.Errorf("the step's failure should be its error: %v", err)
	}
}

// A file the step writes is the turn's once the step's session is gone, so
// the delivery check after the turn still sees it.
func TestWhatAWorkerStepMakesOutlivesItsSession(t *testing.T) {
	SetWorkspacesDir(t.TempDir())
	t.Cleanup(func() { SetWorkspacesDir("") })
	turn, _ := workerTurn(t,
		FakeTurn{ToolCalls: []ToolCall{{ID: "w1", Name: "workspace", Args: map[string]any{"action": "write", "path": "notes.txt", "content": "found it"}}}},
		FakeTurn{Content: "done", Repeat: true})
	if _, err := turn.runWorkerStep(nil, PlanStep{ID: 1, Title: "Write", WorkerBrief: "Write it down."}, "x", nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(turn.stagedFiles, "notes.txt") {
		t.Errorf("the file the step wrote is not the turn's: %v", turn.stagedFiles)
	}
}
