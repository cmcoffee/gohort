package ollama

import (
	"context"
	"testing"
	"time"
)

// newLaneTestScheduler is a one-slot scheduler of its own, so the test does
// not touch the package's singletons.
func newLaneTestScheduler() *OllamaScheduler {
	s := &OllamaScheduler{
		submit:  make(chan *ollamaReqToken, 64),
		release: make(chan string, 64),
		setN:    make(chan int, 4),
		statReq: make(chan chan OllamaSchedStats, 4),
	}
	go s.dispatch(1)
	return s
}

// grantOrder starts each request in turn while the one slot is busy, frees
// the slot, and returns the callers in the order they were served.
func grantOrder(t *testing.T, s *OllamaScheduler, reqs []struct {
	caller     string
	background bool
}) []string {
	t.Helper()
	ctx := context.Background()
	if err := s.acquire(ctx, "image", "holder"); err != nil {
		t.Fatal(err)
	}
	served := make(chan string, len(reqs))
	for _, r := range reqs {
		r := r
		go func() {
			if err := s.acquireLane(ctx, "image", r.caller, r.background); err == nil {
				served <- r.caller
				time.Sleep(20 * time.Millisecond)
				s.release <- r.caller
			}
		}()
		time.Sleep(20 * time.Millisecond) // queued in this order
	}
	s.release <- "holder"
	var out []string
	for range reqs {
		select {
		case c := <-served:
			out = append(out, c)
		case <-time.After(3 * time.Second):
			t.Fatalf("stalled after %v", out)
		}
	}
	return out
}

// A render a person waits on goes ahead of a schedule's queued before it.
func TestAPersonsRenderGoesAheadOfAScheduledOne(t *testing.T) {
	got := grantOrder(t, newLaneTestScheduler(), []struct {
		caller     string
		background bool
	}{{"schedule", true}, {"person", false}})
	if len(got) != 2 || got[0] != "person" || got[1] != "schedule" {
		t.Errorf("served %v, want the person first", got)
	}
}

// Past its wait cap a background render is served in its turn: a busy day
// cannot starve a schedule forever.
func TestABackgroundRenderPastItsCapIsServed(t *testing.T) {
	prev := backgroundWaitCap
	backgroundWaitCap = 10 * time.Millisecond
	t.Cleanup(func() { backgroundWaitCap = prev })
	got := grantOrder(t, newLaneTestScheduler(), []struct {
		caller     string
		background bool
	}{{"schedule", true}, {"person", false}})
	if len(got) != 2 || got[0] != "schedule" {
		t.Errorf("served %v, want the schedule that waited past its cap first", got)
	}
}
