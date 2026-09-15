package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Cancel has to say whether it did anything, because for most of this
// product's life it did not and said nothing. Eleven of the twelve run-creation
// sites passed no cancel func, so the Monitor row's Cancel and the live pill's
// Stop appeared on scheduled fires, standing fires, dispatches, pipelines and
// machines, answered success, and left the work running.
func TestCancelSaysWhetherItDidAnything(t *testing.T) {
	rr := NewRunRegistry()

	deaf := rr.Create("u", "a", "", nil).Describe("scheduled", "Nightly", "digest")
	if deaf.Cancel() {
		t.Error("a run with no cancel func reported that it stopped something")
	}
	if deaf.Cancellable() || deaf.Snapshot().Cancellable {
		t.Error("a run with no cancel func must not advertise itself as cancellable")
	}
	if deaf.Status() != RunStatusRunning {
		t.Error("Cancel must not force-complete a run it could not reach")
	}

	stopped := false
	live := rr.Create("u", "a", "", func() { stopped = true })
	if !live.Cancellable() || !live.Snapshot().Cancellable {
		t.Error("a run with a cancel func should advertise it")
	}
	if !live.Cancel() {
		t.Error("cancelling a stoppable run reported that nothing happened")
	}
	if !stopped {
		t.Error("the cancel func was not called")
	}
}

// The whole point of CreateCancellable: the context it hands back is the one
// the work runs under, so pressing the button reaches the work.
func TestCreateCancellableStopsTheWorkItHandsBack(t *testing.T) {
	rr := NewRunRegistry()
	ctx, run := rr.CreateCancellable(context.Background(), "u", "a", "")
	if ctx.Err() != nil {
		t.Fatalf("the context arrived already cancelled: %v", ctx.Err())
	}
	if !run.Cancel() {
		t.Fatal("a run from CreateCancellable reported nothing to cancel")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("Cancel did not reach the context the work runs under")
	}
}

// The guard that keeps this from coming back at the thirteenth site. Every live
// run has to be stoppable: a row that offers Cancel and cannot deliver it is
// worse than one that never offered.
func TestNoRunIsCreatedWithoutAWayToStopIt(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("reading the package: %v", err)
	}
	call := regexp.MustCompile(`runsRegistry\(\)\.Create\(([^)]*)\)`)
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, rerr := os.ReadFile(f)
		if rerr != nil {
			t.Fatalf("reading %s: %v", f, rerr)
		}
		for _, m := range call.FindAllStringSubmatch(string(src), -1) {
			checked++
			args := strings.Split(m[1], ",")
			last := strings.TrimSpace(args[len(args)-1])
			if last == "nil" {
				t.Errorf("%s: %s passes no cancel func, so its Cancel button would do nothing. "+
					"Use runsRegistry().CreateCancellable(ctx, …) and run the work under the ctx it returns.", f, m[0])
			}
		}
	}
	if checked == 0 {
		t.Fatal("found no run-creation sites to check — did the call shape change?")
	}
}

// A synthetic session id shared by concurrent dispatches must not put them in
// the registry's one-run-per-session slot: with a real cancel behind it, a
// pipeline fanning several branches at the same agent would have each branch
// cancel the one before it.
func TestParallelDispatchesDoNotCancelEachOther(t *testing.T) {
	rr := NewRunRegistry()
	firstStopped := false
	ctx1, first := rr.CreateCancellable(context.Background(), "u", "agent-1", "")
	go func() { <-ctx1.Done(); firstStopped = true }()
	_, second := rr.CreateCancellable(context.Background(), "u", "agent-1", "")

	time.Sleep(20 * time.Millisecond)
	if firstStopped || ctx1.Err() != nil {
		t.Fatal("starting a second dispatch to the same agent cancelled the first")
	}
	if first.Status() != RunStatusRunning || second.Status() != RunStatusRunning {
		t.Fatal("both dispatches should still be running")
	}

	// A real conversation keeps the replace-the-previous rule, because there a
	// fresh send IS meant to abandon the turn before it.
	ctx3, _ := rr.CreateCancellable(context.Background(), "u", "agent-1", "sess-real")
	rr.CreateCancellable(context.Background(), "u", "agent-1", "sess-real")
	select {
	case <-ctx3.Done():
	case <-time.After(time.Second):
		t.Fatal("a new run on the same conversation did not replace the previous one")
	}
}
