package core

import (
	"context"
	"errors"
	"testing"
)

// Usage recording at the LLM HANDLE, not just at the chat wrappers.
//
// WorkerChat / LeadChat / ChatStreamWithReport each record what they serve, so
// everything routed through them was counted. About twenty call sites are not
// routed through them — the turn judge, the grounding judge, gap check, channel
// gatekeeping, operator compaction, the suggest and draft helpers — and each
// one reaches LLM.Chat directly because it wants the model without the
// wrapper's opinions (WorkerChat turns thinking on by default, which a judge
// parsing strict JSON should not inherit merely to get counted). Their spend
// was real and appeared in no report at any level.
//
// Every one of them holds a reloadable handle, so the handle is where this
// belongs. The wrappers keep recording as well, for an AppCore built around a
// raw LLM that never sees a handle; the two must not both count the same call.

// withSharedLLMs installs a shared pair for the duration of a test.
func withSharedLLMs(t *testing.T, worker, lead LLM) {
	t.Helper()
	prevW, prevL := SharedWorkerLLM(), SharedLeadLLM()
	t.Cleanup(func() { SetSharedLLMs(prevW, prevL) })
	SetSharedLLMs(worker, lead)
}

// The case the instrumentation exists for: no wrapper anywhere in sight.
func TestADirectCallThroughTheHandleIsCounted(t *testing.T) {
	worker := &countingLLM{in: 4200, out: 130}
	withSharedLLMs(t, worker, nil)

	llm := ReloadableWorkerLLM()
	d := usageDelta(t, func() {
		if _, err := llm.Chat(context.Background(), []Message{{Role: "user", Content: "judge this"}}); err != nil {
			t.Fatalf("chat: %v", err)
		}
	})

	if d.WorkerInput != 4200 || d.WorkerOutput != 130 {
		t.Errorf("a direct LLM.Chat recorded in=%d out=%d, want 4200/130 — this is the shape of every judge and suggest call in the framework, and it used to record nothing at all",
			d.WorkerInput, d.WorkerOutput)
	}
}

// Both layers run on a normal wrapped call. Exactly one of them may count it.
func TestTheHandleAndTheWrapperDoNotBothCount(t *testing.T) {
	worker := &countingLLM{in: 1000, out: 100}
	withSharedLLMs(t, worker, nil)
	app := &AppCore{LLM: ReloadableWorkerLLM()}

	d := usageDelta(t, func() {
		if _, err := app.WorkerChat(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
			t.Fatalf("worker chat: %v", err)
		}
	})

	if d.WorkerInput != 1000 || d.WorkerOutput != 100 {
		t.Errorf("one call recorded in=%d out=%d, want 1000/100 — double-counting inflates every wrapped call, which is most of them",
			d.WorkerInput, d.WorkerOutput)
	}
}

// An AppCore holding a raw LLM (the SDK's entry point, a test's fake) never
// touches a handle. The wrapper is its only recorder and must stay one.
func TestARawLLMIsStillCountedByTheWrapper(t *testing.T) {
	worker := &countingLLM{in: 77, out: 7}
	withSharedLLMs(t, worker, nil)
	app := &AppCore{LLM: worker} // raw, not reloadable

	d := usageDelta(t, func() {
		if _, err := app.WorkerChat(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
			t.Fatalf("worker chat: %v", err)
		}
	})

	if d.WorkerInput != 77 || d.WorkerOutput != 7 {
		t.Errorf("a raw-LLM AppCore recorded in=%d out=%d, want 77/7", d.WorkerInput, d.WorkerOutput)
	}
}

// The lead handle falls back to the worker when no distinct lead is wired, so
// it cannot bill by its own name. Same invariant the tier-attribution tests
// pin for LeadChat, now on the path that skips LeadChat entirely.
func TestTheLeadHandleBillsLeadOnlyWhenALeadExists(t *testing.T) {
	worker := &countingLLM{in: 1000, out: 100}
	lead := &countingLLM{in: 2000, out: 300}

	withSharedLLMs(t, worker, lead)
	d := usageDelta(t, func() {
		if _, err := ReloadableLeadLLM().Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
			t.Fatalf("chat: %v", err)
		}
	})
	if d.LeadInput != 2000 || d.WorkerInput != 0 {
		t.Errorf("with a distinct lead wired: lead in=%d worker in=%d, want 2000/0", d.LeadInput, d.WorkerInput)
	}

	// Same handle, no lead configured — it now resolves to the worker, and the
	// worker's rates are the ones that apply.
	withSharedLLMs(t, worker, nil)
	d = usageDelta(t, func() {
		if _, err := ReloadableLeadLLM().Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
			t.Fatalf("chat: %v", err)
		}
	})
	if d.LeadInput != 0 {
		t.Errorf("billed %d lead input tokens on a deployment with no lead — with a free local worker and cloud lead rates still filled in, that invents the entire bill", d.LeadInput)
	}
	if d.WorkerInput != 1000 {
		t.Errorf("worker in=%d, want the 1000 the worker actually served", d.WorkerInput)
	}
}

// The per-request cost line reads the request-scoped tracker, so a direct call
// has to reach that one too — otherwise a request whose whole cost is judges
// and compaction reports as free.
func TestADirectCallReachesTheRequestTracker(t *testing.T) {
	worker := &countingLLM{in: 900, out: 90}
	withSharedLLMs(t, worker, nil)

	ctx, tracker := WithRequestUsage(context.Background())
	if _, err := ReloadableWorkerLLM().Chat(ctx, []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("chat: %v", err)
	}

	if got := tracker.Snapshot(); got.WorkerInput != 900 || got.WorkerOutput != 90 {
		t.Errorf("request tracker saw in=%d out=%d, want 900/90", got.WorkerInput, got.WorkerOutput)
	}
}

// haltingLLM answers with the prompt counted and then an error, the way a
// stream that dies partway through does.
type haltingLLM struct{ in int }

func (f *haltingLLM) Chat(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	return &Response{InputTokens: f.in}, errors.New("upstream hung up")
}

func (f *haltingLLM) ChatStream(ctx context.Context, messages []Message, handler StreamHandler, opts ...ChatOption) (*Response, error) {
	return f.Chat(ctx, messages, opts...)
}

// The prompt went out and the provider will bill it. Dropping it because the
// call ended badly is how a deployment under-reports its worst days.
func TestAFailedCallStillCountsThePromptItSent(t *testing.T) {
	withSharedLLMs(t, &haltingLLM{in: 12_000}, nil)

	d := usageDelta(t, func() {
		if _, err := ReloadableWorkerLLM().Chat(context.Background(), nil); err == nil {
			t.Fatal("want the error through")
		}
	})

	if d.WorkerInput != 12_000 {
		t.Errorf("a failed call recorded %d input tokens, want the 12000 it sent", d.WorkerInput)
	}
}

// Per-request cost attribution.
//
// The middleware's "Est. cost" line used to diff the PROCESS-WIDE tracker
// across the request's lifetime, so one request's line absorbed every
// concurrent user turn, scheduled run, and background pipeline that
// overlapped it. The lines over-counted individually, over-counted worse
// when summed, and never reconciled with the admin chart. Now a fresh
// tracker rides the request context and the instrumentation credits it
// alongside the global — the line reports the request's own spend.

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
