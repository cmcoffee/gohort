package orchestrate

import (
	"strings"
	"testing"
)

// A control tool ends the LOOP it fires in. A plan's steps are driven by an
// ordinary for-loop OUTSIDE that call, so "this turn is now closed" — which is
// what stay_silent's own result promises the model — never reached the queue.
// Observed: the model called stay_silent, then watched nine more rounds of its
// own flailing with no way to intervene.

// TestSilencedFlagIsConsumed — stay_silent writes ToolSession.Silenced, and
// until this landed nothing anywhere read it. If this fails, the flag is dead
// again and the turn-closed path is decorative.
func TestSilencedFlagIsConsumed(t *testing.T) {
	src, err := readRunnerSource()
	if err != nil {
		t.Skip("runner source unavailable")
	}
	if !strings.Contains(src, "sess.Silenced {") {
		t.Error("nothing reads ToolSession.Silenced — stay_silent cannot close a turn")
	}
	if strings.Count(src, "t.turnClosed = true") < 2 {
		t.Error("silence should be consumed from BOTH the orchestrator loop and a worker step")
	}
}

// TestPlanDriverHonorsClosedTurn — the queue must be checked between steps,
// not just at the top, or a turn closed mid-plan still runs to the end.
func TestPlanDriverHonorsClosedTurn(t *testing.T) {
	src, err := readRunnerSource()
	if err != nil {
		t.Skip("runner source unavailable")
	}
	driver := sectionAfter(src, "--- Per-step worker execution ---", 2000)
	if !strings.Contains(driver, "turn.turnClosed") {
		t.Fatal("the per-step driver must check for a closed turn between steps")
	}
	if !strings.Contains(driver, "StepBlocked") {
		t.Error("abandoned steps should be marked blocked, not left pending forever")
	}
}

// TestWorkerStepCanEndItself — without RoundAbortTools, respond_directly inside
// a step is an ordinary tool call: it returns, the round completes, and the
// loop takes another turn.
func TestWorkerStepCanEndItself(t *testing.T) {
	src, err := readRunnerSource()
	if err != nil {
		t.Skip("runner source unavailable")
	}
	if strings.Count(src, "RoundAbortTools:") < 2 {
		t.Fatal("the worker-step loop needs its own RoundAbortTools — only the orchestrator declared them")
	}
	// The worker's set, exactly: endable by the tools that finish or pause a
	// turn, but NOT by plan_set — a step does not get to re-plan the turn it
	// belongs to.
	const workerSet = `RoundAbortTools: []string{"ask_user", "ask_user_form", "respond_directly"}`
	if !strings.Contains(src, workerSet) {
		t.Errorf("worker step should declare exactly %s", workerSet)
	}
}
