package core

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// laneProbe records, per lane, the order calls arrived in and whether two of
// them were ever in flight at the same time.
type laneProbe struct {
	mu      sync.Mutex
	order   map[string][]string // lane → call args, in the order they entered
	inFline map[string]int      // lane → calls currently running
	overlap map[string]bool     // lane → two calls were in flight at once
}

func newLaneProbe() *laneProbe {
	return &laneProbe{
		order:   map[string][]string{},
		inFline: map[string]int{},
		overlap: map[string]bool{},
	}
}

func (p *laneProbe) enter(lane, id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.order[lane] = append(p.order[lane], id)
	p.inFline[lane]++
	if p.inFline[lane] > 1 {
		p.overlap[lane] = true
	}
}

func (p *laneProbe) exit(lane string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inFline[lane]--
}

// A tool that declares a BatchLane must have same-lane calls run one at a time,
// in submission order. That is the guarantee dispatch depends on: two calls to
// one sub-agent share a deterministic sub-session id, so overlapping them
// would have them overwrite each other's continuity thread and tear down each
// other's session temp tools.
func TestBatchLaneSerializesWithinLane(t *testing.T) {
	app, _ := withTierStubs(t, "test.batchlane.within", func(n int) []ToolCall {
		if n == 1 {
			return []ToolCall{
				{ID: "1", Name: "dispatch", Args: map[string]any{"target": "alpha", "msg": "a1"}},
				{ID: "2", Name: "dispatch", Args: map[string]any{"target": "beta", "msg": "b1"}},
				{ID: "3", Name: "dispatch", Args: map[string]any{"target": "alpha", "msg": "a2"}},
				{ID: "4", Name: "dispatch", Args: map[string]any{"target": "beta", "msg": "b2"}},
			}
		}
		return nil
	})

	probe := newLaneProbe()
	tool := AgentToolDef{
		Tool: Tool{Name: "dispatch", Description: "dispatch", Parameters: map[string]ToolParam{
			"target": {Type: "string"}, "msg": {Type: "string"},
		}},
		BatchLane: func(args map[string]any) string { return fmt.Sprint(args["target"]) },
		Handler: func(args map[string]any) (string, error) {
			lane, id := fmt.Sprint(args["target"]), fmt.Sprint(args["msg"])
			probe.enter(lane, id)
			// Long enough that an unserialized sibling would land inside it.
			time.Sleep(40 * time.Millisecond)
			probe.exit(lane)
			return "ok", nil
		},
	}

	if _, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		Tools: []AgentToolDef{tool}, MaxRounds: 3, RouteKey: "test.batchlane.within",
	}); err != nil {
		t.Fatalf("loop: %v", err)
	}

	for _, lane := range []string{"alpha", "beta"} {
		if probe.overlap[lane] {
			t.Errorf("lane %q ran two calls concurrently; same-lane calls must be a sequence", lane)
		}
		if got := probe.order[lane]; len(got) != 2 {
			t.Errorf("lane %q ran %d calls, want 2 (%v)", lane, len(got), got)
		}
	}
	if got := probe.order["alpha"]; len(got) == 2 && (got[0] != "a1" || got[1] != "a2") {
		t.Errorf("lane alpha ran out of submission order: %v", got)
	}
	if got := probe.order["beta"]; len(got) == 2 && (got[0] != "b1" || got[1] != "b2") {
		t.Errorf("lane beta ran out of submission order: %v", got)
	}
}

// Distinct lanes are the whole point: they must actually overlap, or the lane
// function has bought nothing over a flat serial-fire sequence. Each handler
// announces itself and then waits for the other one, so this can only pass if
// both are genuinely in flight at once.
func TestBatchLaneRunsDistinctLanesConcurrently(t *testing.T) {
	app, _ := withTierStubs(t, "test.batchlane.across", func(n int) []ToolCall {
		if n == 1 {
			return []ToolCall{
				{ID: "1", Name: "dispatch", Args: map[string]any{"target": "alpha"}},
				{ID: "2", Name: "dispatch", Args: map[string]any{"target": "beta"}},
			}
		}
		return nil
	})

	arrived := make(chan string, 2)
	var both sync.WaitGroup
	both.Add(2)
	var timedOut int32
	tool := AgentToolDef{
		Tool: Tool{Name: "dispatch", Description: "dispatch", Parameters: map[string]ToolParam{
			"target": {Type: "string"},
		}},
		BatchLane: func(args map[string]any) string { return fmt.Sprint(args["target"]) },
		Handler: func(args map[string]any) (string, error) {
			arrived <- fmt.Sprint(args["target"])
			both.Done()
			done := make(chan struct{})
			go func() { both.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				// The other lane never started, so they are not concurrent.
				atomic.StoreInt32(&timedOut, 1)
			}
			return "ok", nil
		},
	}

	if _, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		Tools: []AgentToolDef{tool}, MaxRounds: 3, RouteKey: "test.batchlane.across",
	}); err != nil {
		t.Fatalf("loop: %v", err)
	}
	if atomic.LoadInt32(&timedOut) != 0 {
		t.Fatal("calls in DIFFERENT lanes did not overlap; the lane partition is not fanning out")
	}
	if len(arrived) != 2 {
		t.Fatalf("expected both calls to run; %d arrived", len(arrived))
	}
}

// A lane function that returns "" drops the call into the shared serial lane —
// the same one plain SerialFirePerBatch tools use. This is how a tool turns
// its own fan-out OFF (an operator knob set to 1, an argument it cannot
// resolve to a target) without the loop needing to know why, and it must
// serialize against the other serial-fire tools in the batch, not just against
// itself.
func TestBatchLaneEmptyKeyJoinsSharedSerialLane(t *testing.T) {
	app, _ := withTierStubs(t, "test.batchlane.shared", func(n int) []ToolCall {
		if n == 1 {
			return []ToolCall{
				{ID: "1", Name: "laned", Args: map[string]any{"msg": "l1"}},
				{ID: "2", Name: "plain_serial", Args: map[string]any{"msg": "s1"}},
				{ID: "3", Name: "laned", Args: map[string]any{"msg": "l2"}},
			}
		}
		return nil
	})

	probe := newLaneProbe()
	run := func(args map[string]any) (string, error) {
		probe.enter("shared", fmt.Sprint(args["msg"]))
		time.Sleep(40 * time.Millisecond)
		probe.exit("shared")
		return "ok", nil
	}
	laned := AgentToolDef{
		Tool:      Tool{Name: "laned", Description: "laned", Parameters: map[string]ToolParam{"msg": {Type: "string"}}},
		BatchLane: func(map[string]any) string { return "" },
		Handler:   run,
	}
	serial := AgentToolDef{
		Tool:               Tool{Name: "plain_serial", Description: "serial", Parameters: map[string]ToolParam{"msg": {Type: "string"}}},
		SerialFirePerBatch: true,
		Handler:            run,
	}

	if _, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "go"}}, AgentLoopConfig{
		Tools: []AgentToolDef{laned, serial}, MaxRounds: 3, RouteKey: "test.batchlane.shared",
	}); err != nil {
		t.Fatalf("loop: %v", err)
	}
	if probe.overlap["shared"] {
		t.Error("an empty lane key must share the serial lane, not run in parallel")
	}
	want := []string{"l1", "s1", "l2"}
	got := probe.order["shared"]
	if len(got) != len(want) {
		t.Fatalf("shared lane ran %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("shared lane ran out of submission order: %v, want %v", got, want)
		}
	}
}
