package orchestrate

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// Depth is read off the dispatch chain, not a counter. The counter it replaced
// lived on chatTurn and was never copied into the sub-turn, so it reset at
// every hop and the cap was unreachable; it also incremented for the duration
// of a call, so N siblings in flight read as recursion depth N and call N+1
// failed "depth limit exceeded" at a true depth of 1.
func TestDispatchHopsComeFromTheChain(t *testing.T) {
	turn, _ := newRunGateTurn(t, dispatchAll)
	if got := turn.dispatchHops(); got != 0 {
		t.Fatalf("a turn nobody dispatched sits at depth %d, want 0", got)
	}

	turn.dispatchChain = []string{"a", "b"}
	if got := turn.dispatchHops(); got != 2 {
		t.Fatalf("hops = %d for a 2-agent chain, want 2", got)
	}
	if _, _, err := turn.agentsRunGate(map[string]any{"agent": "Comedian", "message": "hi"}); err != nil {
		t.Fatalf("a 2-agent chain is under the cap of %d and must be allowed: %v", maxDispatchDepth, err)
	}

	turn.dispatchChain = []string{"a", "b", "c"}
	_, _, err := turn.agentsRunGate(map[string]any{"agent": "Comedian", "message": "hi"})
	if err == nil {
		t.Fatal("a chain at maxDispatchDepth must refuse a further dispatch")
	}
	if !strings.Contains(err.Error(), "depth limit") {
		t.Fatalf("refusal should cite the depth limit; got: %v", err)
	}
}

// The per-turn cap is a read-modify-write over a plain map, and with sibling
// dispatches fanned out across a batch several callers reach it at once. Under
// the lock the ceiling is exact; without it the map races AND callers read the
// same pre-increment count and sail past the ceiling together.
func TestDispatchCapHoldsUnderConcurrency(t *testing.T) {
	turn, _ := newRunGateTurn(t, dispatchAll)
	const extra = 8
	attempts := maxTotalTargetDispatch + extra

	var mu sync.Mutex
	allowed := 0
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Distinct messages, so this exercises the THRASH ceiling rather
			// than the identical-call loop cap.
			if block := turn.dispatchCap("tid", "Target", fmt.Sprintf("brief %d", i)); block == "" {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	if allowed != maxTotalTargetDispatch {
		t.Fatalf("%d concurrent dispatches got past a ceiling of %d", allowed, maxTotalTargetDispatch)
	}
}

// Shipped default: one lane, so a batch of dispatches is the sequence it has
// always been. Nothing fans out until an operator raises the knob.
func TestAgentsBatchLaneOffByDefault(t *testing.T) {
	turn, target := newRunGateTurn(t, dispatchAll)
	lane := turn.agentsBatchLane(map[string]any{"action": "run", "agent": target.Name, "message": "hi"})
	if lane != "" {
		t.Fatalf("fan-out must be off by default; got lane %q", lane)
	}
}

// With the knob raised, the two spellings of ONE target have to land in the
// same lane. An agent can be dispatched by name or by id, and if those split
// the pair runs concurrently against a shared sub-session id — exactly the
// collision the lane exists to prevent.
func TestAgentsBatchLaneKeysOnResolvedTarget(t *testing.T) {
	turn, target := newRunGateTurn(t, dispatchAll)
	withFanoutLanes(t, turn, 4)

	byName := turn.agentsBatchLane(map[string]any{"action": "run", "agent": target.Name, "message": "hi"})
	byID := turn.agentsBatchLane(map[string]any{"action": "run", "agent": target.ID, "message": "hi"})
	if byName == "" || byName != byID {
		t.Fatalf("name and id of one target must share a lane; got %q vs %q", byName, byID)
	}

	// A target that resolves to nothing falls back to the shared serial lane:
	// wrong-but-serial costs latency, wrong-but-parallel corrupts a session.
	if lane := turn.agentsBatchLane(map[string]any{"action": "run", "agent": "no-such-agent", "message": "hi"}); lane != "" {
		t.Errorf("an unresolvable target must fall back to the serial lane; got %q", lane)
	}
	// Only run is laned apart; the read actions stay in the serial lane.
	if lane := turn.agentsBatchLane(map[string]any{"action": "list"}); lane != "" {
		t.Errorf("action=list must stay in the serial lane; got %q", lane)
	}
}

// The knob is a real ceiling on concurrency, not a lane per target: hashing
// into a fixed number of slots means a 20-target batch fans out to `lanes`
// goroutines, and a collision only ever serializes two targets that could have
// overlapped.
func TestAgentsBatchLaneBoundsLaneCount(t *testing.T) {
	turn, _ := newRunGateTurn(t, dispatchAll)
	const lanes = 3
	withFanoutLanes(t, turn, lanes)

	seen := map[string]bool{}
	for i := 0; i < 40; i++ {
		rec, err := saveAgent(turn.udb, AgentRecord{Name: fmt.Sprintf("Target%d", i), Owner: "u", OrchestratorPrompt: "p"})
		if err != nil {
			t.Fatalf("save target %d: %v", i, err)
		}
		lane := turn.agentsBatchLane(map[string]any{"action": "run", "agent": rec.ID, "message": "hi"})
		if lane == "" {
			t.Fatalf("target %d resolved but got no lane", i)
		}
		seen[lane] = true
	}
	if len(seen) > lanes {
		t.Fatalf("40 targets spread over %d lanes, want at most %d", len(seen), lanes)
	}
}

// withFanoutLanes points the tunables registry at this turn's store and sets
// the lane count for the duration of the test.
func withFanoutLanes(t *testing.T, turn *chatTurn, n int) {
	t.Helper()
	SetTunablesDB(turn.udb)
	turn.udb.Set(WebTable, "tune_dispatch_fanout_lanes", float64(n))
	InvalidateTunables()
	t.Cleanup(func() {
		SetTunablesDB(nil)
		InvalidateTunables()
	})
}
