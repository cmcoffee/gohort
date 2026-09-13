package orchestrate

import (
	"context"
	"errors"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A scheduled machine run used to collect the walk's notes into a slice
// nobody read. The trace must keep every step result and every framework
// decision, in the order they happened, so the ledger can show the run.
func TestMachineRunTraceKeepsStepsAndDecisionsInOrder(t *testing.T) {
	tr := newMachineRunTrace(nil)
	run := tr.wrap(func(_ context.Context, ph MachinePhase, _ string) (string, error) {
		if ph.Name == "verify" {
			return "", errors.New("nothing to verify")
		}
		return "result of " + ph.Name, nil
	})
	tr.note("machine_phase_changed", "gather → verify")
	if _, err := run(context.Background(), MachinePhase{Name: "gather", Tool: "fetch_url"}, ""); err != nil {
		t.Fatal(err)
	}
	tr.activity("delegate", "3 of 5 stages")
	if _, err := run(context.Background(), MachinePhase{Name: "verify"}, ""); err == nil {
		t.Fatal("verify should fail")
	}

	steps := tr.Steps()
	want := []RunStep{
		{Name: "machine_phase_changed", Result: "gather → verify"},
		{Name: "step gather", Result: "result of gather"},
		{Name: "activity", Result: "delegate: 3 of 5 stages"},
		{Name: "step verify", Err: "nothing to verify"},
	}
	if len(steps) != len(want) {
		t.Fatalf("got %d steps, want %d: %+v", len(steps), len(want), steps)
	}
	for i := range want {
		if steps[i] != want[i] {
			t.Errorf("step %d = %+v, want %+v", i, steps[i], want[i])
		}
	}
	if tr.phases() != 2 {
		t.Errorf("phases = %d, want 2", tr.phases())
	}
	// A blank note leaves nothing; the trace is the run's transcript, not a log.
	tr.note("", "  ")
	if n := len(tr.Steps()); n != 4 {
		t.Errorf("blank note recorded: %d steps", n)
	}
}

// The live activity row advances one round per step and names the tool a
// tool step calls, so the owner watching "Active now" sees where a 3am run is.
func TestMachineRunTraceAdvancesTheLiveRun(t *testing.T) {
	reg := NewRunRegistry()
	live := reg.Create("alice", "", "", nil)
	tr := newMachineRunTrace(live)
	run := tr.wrap(func(context.Context, MachinePhase, string) (string, error) { return "ok", nil })
	run(context.Background(), MachinePhase{Name: "one"}, "")
	run(context.Background(), MachinePhase{Name: "two", Tool: "read_file"}, "")
	snap := live.Snapshot()
	if snap.Round != 2 {
		t.Errorf("round = %d, want 2", snap.Round)
	}
	if snap.LastTool != "read_file" {
		t.Errorf("last tool = %q, want read_file", snap.LastTool)
	}
}
