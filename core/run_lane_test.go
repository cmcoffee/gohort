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

// A call with a short deadline of its own is never held, and one with a
// longer deadline is held only until the floor remains: the warden's check
// inside a held run used to expire waiting and block the turn.
func TestAShortDeadlineIsNeverHeld(t *testing.T) {
	q, mw, tick, floor := laneQuiet, laneMaxWait, laneTick, laneDeadlineFloor
	laneQuiet, laneMaxWait, laneTick, laneDeadlineFloor = time.Second, 2*time.Second, 5*time.Millisecond, 100*time.Millisecond
	defer func() { laneQuiet, laneMaxWait, laneTick, laneDeadlineFloor = q, mw, tick, floor }()
	modelUseMu.Lock()
	modelUses = map[LLMTier]*modelUse{}
	modelUseMu.Unlock()

	done, _ := reloadableLLM{}.beginModelUse(WithRunLane(context.Background(), LaneInteractive))
	defer done()
	short, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if held, err := holdForLane(short, WORKER); held != 0 || err != nil {
		t.Fatalf("a call with less than the floor left was held %s (%v)", held, err)
	}
	longer, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	held, err := holdForLane(longer, WORKER)
	if err != nil {
		t.Fatal(err)
	}
	if held < 150*time.Millisecond || held > 260*time.Millisecond {
		t.Fatalf("held %s; want the deadline less the floor, about 200ms", held)
	}
}

// The run gives way at its OWN call, in the loop, and the wait is a figure of
// its own: the reply carries it, OnWait hears it, and a short call made
// beside the run (a warden, a title) is never held.
func TestTheLoopGivesWayAtItsOwnCall(t *testing.T) {
	q, mw, tick, every := laneQuiet, laneMaxWait, laneTick, laneProgressEvery
	laneQuiet, laneMaxWait, laneTick, laneProgressEvery = 30*time.Millisecond, 2*time.Second, 5*time.Millisecond, 20*time.Millisecond
	defer func() { laneQuiet, laneMaxWait, laneTick, laneProgressEvery = q, mw, tick, every }()
	modelUseMu.Lock()
	modelUses = map[LLMTier]*modelUse{}
	modelUseMu.Unlock()

	person := WithRunLane(context.Background(), LaneInteractive)
	done, _ := reloadableLLM{}.beginModelUse(person)
	// A helper call beside the run goes at once while the person's call is
	// out: the hold is the loop's, not every call's.
	helperDone, err := reloadableLLM{}.beginModelUse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	helperDone()

	go func() { time.Sleep(60 * time.Millisecond); done() }()
	stub := &FakeLLM{Turns: []FakeTurn{{Content: "done", Repeat: true}}}
	app := &AppCore{LLM: stub}
	var progress, finished int
	var total time.Duration
	start := time.Now()
	resp, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "hi"}}, AgentLoopConfig{
		MaxRounds: 2,
		OnWait: func(waited time.Duration, isDone bool) {
			if isDone {
				finished++
				total = waited
			} else {
				progress++
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil || resp.LaneWait < 60*time.Millisecond {
		t.Fatalf("the reply should carry the wait, got %+v", resp)
	}
	if finished != 1 || total != resp.LaneWait || progress == 0 {
		t.Fatalf("OnWait: progress=%d finished=%d total=%s (reply %s)", progress, finished, total, resp.LaneWait)
	}
	if time.Since(start) > time.Second {
		t.Fatal("the loop waited far longer than the person's call")
	}
	// A person's own run is never held.
	modelUseMu.Lock()
	modelUses = map[LLMTier]*modelUse{}
	modelUseMu.Unlock()
	hold, _ := reloadableLLM{}.beginModelUse(person)
	defer hold()
	resp, _, err = app.RunAgentLoop(person, []Message{{Role: "user", Content: "hi"}}, AgentLoopConfig{MaxRounds: 2})
	if err != nil || resp.LaneWait != 0 {
		t.Fatalf("a person's run was held %s (%v)", resp.LaneWait, err)
	}
}
