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
//
// This used to grep the source: `strings.Count(src, "RoundAbortTools:") >= 2`
// plus the worker's literal. Both parts passed while the invariant they
// described could be broken — adding a fifth abort tool to the orchestrator
// left the worker set stale, the count still 2, the literal still present, the
// test still green. It now reads the values, and the worker set is derived from
// the orchestrator's rather than retyped, so the relationship is the compiler's
// to keep.
func TestWorkerStepCanEndItself(t *testing.T) {
	if len(workerAbortTools) == 0 {
		t.Fatal("the worker-step loop needs its own RoundAbortTools")
	}
	orch := map[string]bool{}
	for _, n := range orchestratorAbortTools {
		orch[n] = true
	}
	if !orch["plan_set"] {
		t.Fatal("the orchestrator must be able to end a round by planning")
	}
	for _, n := range workerAbortTools {
		if n == "plan_set" {
			t.Error("a worker step must NOT end its round with plan_set — it is already inside a plan")
		}
		if !orch[n] {
			t.Errorf("worker abort tool %q is not one of the orchestrator's; the sets have diverged", n)
		}
	}
	// Everything the orchestrator aborts on except plan_set must reach the
	// worker, or a step loses a way to pause for the user.
	if len(workerAbortTools) != len(orchestratorAbortTools)-1 {
		t.Errorf("worker has %d abort tools, orchestrator %d; expected exactly one fewer (plan_set)",
			len(workerAbortTools), len(orchestratorAbortTools))
	}
	for _, want := range []string{"ask_user", "ask_user_form", "respond_directly"} {
		found := false
		for _, n := range workerAbortTools {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Errorf("a worker step can no longer end itself with %s", want)
		}
	}
}
