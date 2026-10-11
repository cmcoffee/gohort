package core

import (
	"context"
	"testing"
	"time"
)

// A person's call goes at once. A background call waits while a person's
// call is out and for the quiet after it, then goes. A lowest call waits
// while ANY call is out. The waits are capped.
func TestLanesGiveWayToPeople(t *testing.T) {
	q, lq, mw, tick := laneQuiet, laneLowestQuiet, laneMaxWait, laneTick
	laneQuiet, laneLowestQuiet, laneMaxWait, laneTick = 60*time.Millisecond, 60*time.Millisecond, 400*time.Millisecond, 5*time.Millisecond
	defer func() { laneQuiet, laneLowestQuiet, laneMaxWait, laneTick = q, lq, mw, tick }()
	modelUseMu.Lock()
	modelUses = map[LLMTier]*modelUse{}
	modelUseMu.Unlock()

	r := reloadableLLM{}
	person := WithRunLane(context.Background(), LaneInteractive)
	done, err := r.beginModelUse(person)
	if err != nil {
		t.Fatal(err)
	}
	if held, _ := holdForLane(person, WORKER); held != 0 {
		t.Fatalf("a person's call was held %s", held)
	}
	// Background waits while the person's call is out, and is released soon
	// after it ends: within the quiet plus a tick or two.
	go func() { time.Sleep(50 * time.Millisecond); done() }()
	start := time.Now()
	held, err := holdForLane(context.Background(), WORKER)
	if err != nil {
		t.Fatal(err)
	}
	if held < 50*time.Millisecond || time.Since(start) > 300*time.Millisecond {
		t.Fatalf("background held %s; want about the call plus the quiet", held)
	}
	// Past the quiet, nothing holds background.
	time.Sleep(80 * time.Millisecond)
	if held, _ := holdForLane(context.Background(), WORKER); held != 0 {
		t.Fatalf("background held %s with nobody using the model", held)
	}
	// Lowest waits for a BACKGROUND call too; background does not wait for
	// background.
	bgDone, _ := r.beginModelUse(context.Background())
	if held, _ := holdForLane(context.Background(), WORKER); held != 0 {
		t.Fatalf("background held %s behind another background call", held)
	}
	lowest := WithRunLane(context.Background(), LaneLowest)
	go func() { time.Sleep(40 * time.Millisecond); bgDone() }()
	if held, _ := holdForLane(lowest, WORKER); held < 40*time.Millisecond {
		t.Fatalf("lowest went after %s with another call out", held)
	}
	// The cap: a person's call that never ends does not hold background
	// forever.
	time.Sleep(80 * time.Millisecond)
	forever, _ := r.beginModelUse(person)
	defer forever()
	if held, _ := holdForLane(context.Background(), WORKER); held < laneMaxWait || held > laneMaxWait+100*time.Millisecond {
		t.Fatalf("background held %s; want the cap %s", held, laneMaxWait)
	}
	// A cancelled wait returns the context's error.
	cctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if _, err := holdForLane(cctx, WORKER); err == nil {
		t.Fatal("a cancelled wait should return the error")
	}
}

func TestRunLaneDefaultsToBackground(t *testing.T) {
	if RunLaneOf(context.Background()) != LaneBackground || RunLaneOf(nil) != LaneBackground {
		t.Fatal("an unmarked context is background")
	}
	if RunLaneOf(WithRunLane(context.Background(), LaneLowest)) != LaneLowest {
		t.Fatal("the mark is not read back")
	}
}
