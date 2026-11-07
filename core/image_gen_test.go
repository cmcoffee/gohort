package core

import (
	"context"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// CountImageRender counts every render this turn, from 1, and a nil session
// reads as the first: the hard cap is checked against it.
func TestCountImageRenderCountsEveryRender(t *testing.T) {
	var nilSess *ToolSession
	if n := nilSess.CountImageRender(); n != 1 {
		t.Fatalf("nil session: want 1, got %d", n)
	}
	sess := &ToolSession{}
	for want := 1; want <= 3; want++ {
		if n := sess.CountImageRender(); n != want {
			t.Fatalf("render %d counted as %d", want, n)
		}
	}
	if n := sess.ImageRenderCount(); n != 3 {
		t.Errorf("read back without counting: %d, want 3", n)
	}
}

// A render queues on the lane of the turn that asked for it when a detached
// job recorded one, else on its own context's: a detached render carries no
// lane of its own and would otherwise read as background.
func TestARenderQueuesOnTheAskingTurnsLane(t *testing.T) {
	bg := context.Background()
	if got := renderLaneOf(bg); got != LaneBackground {
		t.Errorf("no lane = %q, want background", got)
	}
	if got := renderLaneOf(WithRunLane(bg, LaneInteractive)); got != LaneInteractive {
		t.Errorf("the context's own lane = %q", got)
	}
	detached := WithRenderLane(bg, LaneInteractive)
	if got := renderLaneOf(detached); got != LaneInteractive {
		t.Errorf("a detached render a person asked for = %q, want interactive", got)
	}
	if RunLaneOf(detached) != LaneBackground {
		t.Error("the detached job's model lane changed: it stays background")
	}
}

// Background renders wait for people's by default; an operator can turn that
// off, and then every render is first come, first served.
func TestTheRenderYieldSetting(t *testing.T) {
	prev := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prev })
	if !rendersYield() {
		t.Error("unset should mean background renders wait for people's")
	}
	RootDB.Set(LLMTable, imageRenderYieldKey, "never")
	if rendersYield() {
		t.Error("never should make every render first come, first served")
	}
}
