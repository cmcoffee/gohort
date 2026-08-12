package core

import (
	"context"
	"testing"
	"time"
)

// Renders took no slot at all: the heaviest thing a peer can ask for was the
// only one racing local work instead of taking turns with it.

// TestRendersQueueAgainstEachOther — with the limiter at one, a second render
// waits. That is the property; throughput is unchanged because image backends
// serialize internally anyway, but the queue is now HERE where it can be fair.
func TestRendersQueueAgainstEachOther(t *testing.T) {
	StartImageScheduler(1)

	first, err := AcquireImageSlot(context.Background(), "local")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := AcquireImageSlot(ctx, "peer:mac"); err == nil {
		ReleaseImageSlot("peer:mac")
		t.Error("a second render started while the first held the only slot")
	}
	first()
	// Once freed, the next one goes through.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	rel, err := AcquireImageSlot(ctx2, "peer:mac")
	if err != nil {
		t.Fatalf("the queue did not drain after the slot was released: %v", err)
	}
	rel()
}

// TestTheQueueKnowsWhoIsRendering — fair-share needs distinguishable callers,
// and the surface that reports what is holding the GPU reads the same state.
func TestTheQueueKnowsWhoIsRendering(t *testing.T) {
	StartImageScheduler(1)
	rel, err := AcquireImageSlot(context.Background(), "peer:studio")
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	if got := ImageSchedulerStats().Callers["peer:studio"]; got == 0 {
		t.Errorf("the scheduler does not know which caller holds the render slot: %+v",
			ImageSchedulerStats().Callers)
	}
}

// TestAnUnlabelledRenderIsLocal — a render with no label must not become its own
// anonymous participant, or every unlabelled caller would compete as a separate
// share and local work would fragment against itself.
func TestAnUnlabelledRenderIsLocal(t *testing.T) {
	if got := renderCaller(context.Background()); got != "local" {
		t.Errorf("renderCaller = %q, want local", got)
	}
	if got := renderCaller(nil); got != "local" {
		t.Errorf("a nil context yielded %q", got)
	}
	if got := renderCaller(WithRenderCaller(context.Background(), "  ")); got != "local" {
		t.Errorf("a blank label yielded %q", got)
	}
	if got := renderCaller(WithRenderCaller(context.Background(), "peer:mac")); got != "peer:mac" {
		t.Errorf("a set label was lost: %q", got)
	}
}

// TestReleaseIsAlwaysUsable — queueRender defers its release unconditionally, so
// a nil or a panic there would take down every render path.
func TestReleaseIsAlwaysUsable(t *testing.T) {
	StartImageScheduler(0) // disabled
	rel, err := AcquireImageSlot(context.Background(), "local")
	if err != nil {
		t.Fatalf("a disabled scheduler refused: %v", err)
	}
	if rel == nil {
		t.Fatal("a disabled scheduler returned a nil release — every deferred call would panic")
	}
	rel()
	ReleaseImageSlot("never-acquired") // must not block or panic
	StartImageScheduler(1)

	// A caller whose context is already done gets a usable release too, rather
	// than a nil the render path would defer straight into a panic.
	done, cancel := context.WithCancel(context.Background())
	cancel()
	if got := queueRender(done); got == nil {
		t.Error("queueRender returned nil for a cancelled caller")
	} else {
		got()
	}
}
