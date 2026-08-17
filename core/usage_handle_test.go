package core

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

import (
	"context"
	"errors"
	"testing"
)

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
