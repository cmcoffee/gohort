package orchestrate

// A lead's work_step says what its worker is doing: the worker's tools, on
// the card the run is drawn under, labelled with the step; and a line on the
// run's own row on the live surface, which ends saying the step finished.
// Before, the card read "work_step" for as long as the worker ran.

import (
	"context"
	"sync"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestAWorkStepReportsWhatItsWorkerIsDoing(t *testing.T) {
	T, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	T.LLM = &FakeLLM{Turns: []FakeTurn{
		{ToolCalls: []ToolCall{{ID: "c1", Name: "lookup", Args: map[string]any{"id": "A17"}}}},
		{ToolCalls: []ToolCall{{ID: "c2", Name: "lookup", Args: map[string]any{"id": "A17"}}}},
		{Content: "Order A17 shipped Tuesday.", Repeat: true},
	}}
	T.LeadLLM = T.LLM

	var mu sync.Mutex
	var events []map[string]any
	card := toolCard{msgID: "m1", callID: "ws1", send: func(ev map[string]any) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}}
	ctx, run := T.runsRegistry().CreateCancellable(context.Background(), user, "a1", "")
	t.Cleanup(func() { run.Complete(RunStatusCompleted) })
	ctx = withParentRun(withToolCard(ctx, card), run.ID)

	// What the run's row said each time the tool ran: the second call runs after
	// the first round has reported, so it sees that round's line.
	var during []string
	lookup := AgentToolDef{
		Tool: Tool{Name: "lookup", Description: "Look up an order.",
			Parameters: map[string]ToolParam{"id": {Type: "string"}}, Required: []string{"id"}},
		Handler: func(context.Context, map[string]any) (string, error) {
			during = append(during, run.Snapshot().Detail)
			return "order A17: shipped Tuesday", nil
		},
	}
	turn := &chatTurn{app: T, agent: AgentRecord{ID: "a1", Name: "Clerk", Owner: user}, user: user, udb: udb, ctx: ctx}
	ws := turn.workStepToolDef("Where is order A17?", []AgentToolDef{lookup}, nil)
	out, err := ws.Handler(ctx, map[string]any{"title": "Check the order", "brief": "Look up A17.", "tools": []any{"lookup"}})
	if err != nil || out != "Order A17 shipped Tuesday." {
		t.Fatalf("the step: %q %v", out, err)
	}

	mu.Lock()
	defer mu.Unlock()
	sawTool, sawRound := false, false
	for _, ev := range events {
		if ev["kind"] != "tool_progress" || ev["call_id"] != "ws1" || ev["agent"] != "Clerk · step 1" {
			continue
		}
		// The round counts up from the worker's own round starts: without them
		// every line says round 0.
		if r, _ := ev["round"].(int); r >= 1 {
			sawRound = true
		}
		if ev["tool"] == "lookup" {
			sawTool = true
		}
	}
	if !sawRound || !sawTool {
		t.Errorf("the card should show the step's worker and the tool it ran: %+v", events)
	}
	if len(during) != 2 || during[0] != "Step 1: Check the order" {
		t.Fatalf("while it ran, the run's row should say which step is running: %q", during)
	}
	if during[1] != "Step 1: lookup, round 1" {
		t.Errorf("after a round, the row should say what the worker ran: %q", during[1])
	}
	if got := run.Snapshot().Detail; got != "Step 1 done: Check the order" {
		t.Errorf("the row should end on the step finishing, got %q", got)
	}
}
