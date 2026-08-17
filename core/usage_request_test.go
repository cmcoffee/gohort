package core

// Per-request cost attribution.
//
// The middleware's "Est. cost" line used to diff the PROCESS-WIDE tracker
// across the request's lifetime, so one request's line absorbed every
// concurrent user turn, scheduled run, and background pipeline that
// overlapped it. The lines over-counted individually, over-counted worse
// when summed, and never reconciled with the admin chart. Now a fresh
// tracker rides the request context and the instrumentation credits it
// alongside the global — the line reports the request's own spend.

import (
	"context"
	"testing"
)

func TestRequestTrackerSeesOnlyItsOwnRequest(t *testing.T) {
	ctx, reqUsage := WithRequestUsage(context.Background())
	app := &AppCore{}

	// This request's call.
	app.trackTokens(ctx, &Response{InputTokens: 100, OutputTokens: 10})
	// A concurrent call on a context NOT belonging to this request —
	// background pipeline, another user's turn.
	app.trackLeadTokens(context.Background(), &Response{InputTokens: 5_000, OutputTokens: 500})

	d := reqUsage.Snapshot()
	if d.WorkerInput != 100 || d.WorkerOutput != 10 {
		t.Errorf("request tracker worker = in %d / out %d, want 100 / 10", d.WorkerInput, d.WorkerOutput)
	}
	if d.LeadInput != 0 || d.LeadOutput != 0 {
		t.Errorf("request tracker lead = in %d / out %d, want 0 / 0 — the background call leaked in",
			d.LeadInput, d.LeadOutput)
	}
}

func TestGlobalTrackerStillCountsEverything(t *testing.T) {
	start := ProcessUsage().Snapshot()
	ctx, _ := WithRequestUsage(context.Background())
	app := &AppCore{}

	app.trackTokens(ctx, &Response{InputTokens: 100, OutputTokens: 10})
	app.trackTokens(context.Background(), &Response{InputTokens: 40, OutputTokens: 4})

	d := ProcessUsage().Diff(start)
	if d.WorkerInput != 140 || d.WorkerOutput != 14 {
		t.Errorf("global worker = in %d / out %d, want 140 / 14 — request scoping must not steal from the global",
			d.WorkerInput, d.WorkerOutput)
	}
}

func TestRequestUsageIsNilOffRequestPaths(t *testing.T) {
	if RequestUsage(context.Background()) != nil {
		t.Error("bare context should carry no request tracker")
	}
	if RequestUsage(nil) != nil {
		t.Error("nil context should be safe and return nil")
	}
}

// A handler that detaches the WORK's lifetime from the request (chat
// turns root their ctx at Background so a disconnect can't kill an
// in-flight tool call) still owns the spend. Before CarryRequestUsage
// the detached ctx carried no tracker, the request tracker never moved,
// and the middleware's zero-delta skip swallowed the cost line for
// exactly the requests worth costing.
func TestCarryRequestUsageCreditsDetachedWork(t *testing.T) {
	reqCtx, reqUsage := WithRequestUsage(context.Background())

	// The handler's detached run context.
	runCtx, cancel := context.WithCancel(CarryRequestUsage(context.Background(), reqCtx))
	defer cancel()

	app := &AppCore{}
	app.trackTokens(runCtx, &Response{InputTokens: 100, OutputTokens: 10})
	app.trackLeadTokens(runCtx, &Response{InputTokens: 40, OutputTokens: 5})

	d := reqUsage.Snapshot()
	if d.WorkerInput != 100 || d.WorkerOutput != 10 {
		t.Errorf("worker tokens not credited to request: in=%d out=%d", d.WorkerInput, d.WorkerOutput)
	}
	if d.LeadInput != 40 || d.LeadOutput != 5 {
		t.Errorf("lead tokens not credited to request: in=%d out=%d", d.LeadInput, d.LeadOutput)
	}
	if d == (UsageDiff{}) {
		t.Fatal("zero diff — the middleware would skip the cost line entirely")
	}

	// Cancellation stays detached: the request context ending must not
	// reach the run.
	if runCtx.Err() != nil {
		t.Errorf("run ctx inherited cancellation: %v", runCtx.Err())
	}
}

// Background work the request merely SPAWNS and does not wait on keeps
// the deliberate drop — no source tracker, nothing to carry.
func TestCarryRequestUsageNoTrackerIsPassthrough(t *testing.T) {
	bg := context.Background()
	if got := CarryRequestUsage(bg, context.Background()); got != bg {
		t.Error("expected dst returned unchanged when src carries no tracker")
	}
	if RequestUsage(CarryRequestUsage(context.Background(), nil)) != nil {
		t.Error("nil src should not install a tracker")
	}
}

// A dispatched sub-agent reports on its OWN line: its tokens land on
// its own tracker and never inflate the delegator's. Without the
// shadowing scope, a turn that dispatched three sub-agents printed one
// line carrying all four runs and no way to tell them apart.
func TestSubUsageKeepsSpendOffTheParent(t *testing.T) {
	reqCtx, reqUsage := WithRequestUsage(context.Background())
	app := &AppCore{}

	// The delegator's own turn.
	app.trackTokens(reqCtx, &Response{InputTokens: 100, OutputTokens: 10})

	// A dispatched sub-agent, scoped out.
	subCtx, reportSub := WithSubUsage(reqCtx, "dispatch tester run_1")
	app.trackTokens(subCtx, &Response{InputTokens: 900, OutputTokens: 90})
	app.trackLeadTokens(subCtx, &Response{InputTokens: 40, OutputTokens: 4})
	reportSub()

	if d := reqUsage.Snapshot(); d.WorkerInput != 100 || d.WorkerOutput != 10 || d.LeadInput != 0 {
		t.Errorf("sub-agent spend leaked into the parent: %+v", d)
	}

	// The parent's own spend still counts after the sub finishes.
	app.trackTokens(reqCtx, &Response{InputTokens: 5, OutputTokens: 1})
	if d := reqUsage.Snapshot(); d.WorkerInput != 105 {
		t.Errorf("parent stopped counting its own calls: %+v", d)
	}
}

// Nesting: each level shadows the one above, so a sub-agent that
// dispatches its own sub-agent doesn't absorb the grandchild either.
func TestSubUsageNests(t *testing.T) {
	app := &AppCore{}
	reqCtx, reqUsage := WithRequestUsage(context.Background())
	childCtx, reportChild := WithSubUsage(reqCtx, "dispatch child run_1")
	grandCtx, reportGrand := WithSubUsage(childCtx, "dispatch grandchild run_2")

	app.trackTokens(grandCtx, &Response{InputTokens: 700, OutputTokens: 70})
	reportGrand()
	app.trackTokens(childCtx, &Response{InputTokens: 300, OutputTokens: 30})
	reportChild()
	app.trackTokens(reqCtx, &Response{InputTokens: 100, OutputTokens: 10})

	if d := reqUsage.Snapshot(); d.WorkerInput != 100 {
		t.Errorf("request absorbed descendant spend: %+v", d)
	}
	if got := RequestUsage(childCtx).Snapshot().WorkerInput; got != 300 {
		t.Errorf("child absorbed grandchild spend: in=%d", got)
	}
}

// Searches stay window-diffed off the process counter, so a parent's
// window contains its children's searches. The child claims what it
// reported; the parent nets it out. Otherwise the same search is billed
// on two lines that were supposed to sum to the process total.
func TestSubUsageSearchClaimsNetOut(t *testing.T) {
	reqCtx, reqUsage := WithRequestUsage(context.Background())
	globalStart := ProcessUsage().Snapshot()

	// One search by the parent, two by a dispatched sub-agent.
	ProcessUsage().AddSearchCall()
	_, reportSub := WithSubUsage(reqCtx, "dispatch searcher run_1")
	ProcessUsage().AddSearchCall()
	ProcessUsage().AddSearchCall()
	reportSub()

	window := ProcessUsage().Diff(globalStart).SearchCalls
	if window != 3 {
		t.Fatalf("window should see all three searches, got %d", window)
	}
	if own := reqUsage.UnclaimedSearchCalls(window); own != 1 {
		t.Errorf("parent should report only its own search, got %d", own)
	}
}

// A child that outlives the parent's report can claim more than the
// parent's window ever held. A low count beats a negative one.
func TestUnclaimedSearchCallsNeverNegative(t *testing.T) {
	u := &UsageTracker{}
	u.ClaimSearchCalls(5)
	if got := u.UnclaimedSearchCalls(2); got != 0 {
		t.Errorf("expected clamp to 0, got %d", got)
	}
	u.ClaimSearchCalls(-3) // ignored
	if got := u.UnclaimedSearchCalls(9); got != 4 {
		t.Errorf("expected 9-5=4, got %d", got)
	}
}
