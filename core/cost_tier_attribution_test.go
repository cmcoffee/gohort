package core

// Which TIER gets billed for an escalated call.
//
// GetLeadLLM returns the worker when no lead is configured, and LeadIsDistinct
// is false when a lead is set but resolves to the same model. In both cases
// LeadChat's lead.Chat runs the WORKER, succeeds, and skips the two fallback
// branches — each of which requires a distinct lead to exist. So every
// escalation on a worker-only deployment was recorded as lead tokens and priced
// at the lead rate. With the usual arrangement — a local worker at no cost, a
// cloud lead that is anything but — that does not shade the estimate, it
// invents the entire bill.

import (
	"context"
	"testing"
)

type countingLLM struct {
	in, out int
	calls   int
}

func (c *countingLLM) Chat(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, error) {
	c.calls++
	return &Response{Content: "ok", InputTokens: c.in, OutputTokens: c.out}, nil
}

func (c *countingLLM) ChatStream(ctx context.Context, messages []Message, handler StreamHandler, opts ...ChatOption) (*Response, error) {
	return c.Chat(ctx, messages, opts...)
}

// usageDelta runs fn and reports what the PROCESS tracker recorded — the
// counters AddWorker/AddLead feed, and the ones the tier split is read from.
// UsageScope is session-scoped and would read zero here.
func usageDelta(t *testing.T, fn func()) UsageDiff {
	t.Helper()
	start := ProcessUsage().Snapshot()
	fn()
	return ProcessUsage().Diff(start)
}

func TestWorkerOnlyDeploymentBillsEscalationsAsWorker(t *testing.T) {
	prevW, prevL := SharedWorkerLLM(), SharedLeadLLM()
	t.Cleanup(func() { SetSharedLLMs(prevW, prevL) })

	worker := &countingLLM{in: 1000, out: 100}
	SetSharedLLMs(worker, nil) // no lead configured at all
	app := &AppCore{LLM: worker}

	d := usageDelta(t, func() {
		if _, err := app.LeadChat(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
			t.Fatalf("lead chat: %v", err)
		}
	})

	if worker.calls != 1 {
		t.Fatalf("the worker ran %d times, want 1", worker.calls)
	}
	if d.LeadInput != 0 || d.LeadOutput != 0 {
		t.Errorf("a call served by the worker was billed as lead (in=%d out=%d) — with a free local worker and a paid cloud lead, that is an invented bill",
			d.LeadInput, d.LeadOutput)
	}
	if d.WorkerInput != 1000 || d.WorkerOutput != 100 {
		t.Errorf("worker tokens = in %d/out %d, want the 1000/100 that were actually spent", d.WorkerInput, d.WorkerOutput)
	}
}

// A genuinely distinct lead must still bill as lead — the fix must not collapse
// the tiers on deployments that really do run two models.
func TestADistinctLeadStillBillsAsLead(t *testing.T) {
	prevW, prevL := SharedWorkerLLM(), SharedLeadLLM()
	t.Cleanup(func() { SetSharedLLMs(prevW, prevL) })

	worker := &countingLLM{in: 1000, out: 100}
	lead := &countingLLM{in: 2000, out: 300}
	SetSharedLLMs(worker, lead) // LeadIsDistinct() is now true
	app := &AppCore{LLM: worker, LeadLLM: lead}

	d := usageDelta(t, func() {
		if _, err := app.LeadChat(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
			t.Fatalf("lead chat: %v", err)
		}
	})

	if lead.calls != 1 || worker.calls != 0 {
		t.Fatalf("lead ran %d, worker ran %d — want the lead to serve it", lead.calls, worker.calls)
	}
	if d.LeadInput != 2000 || d.LeadOutput != 300 {
		t.Errorf("lead tokens = in %d/out %d, want 2000/300", d.LeadInput, d.LeadOutput)
	}
	if d.WorkerInput != 0 || d.WorkerOutput != 0 {
		t.Errorf("a distinct lead's tokens leaked into the worker tier (in=%d out=%d)", d.WorkerInput, d.WorkerOutput)
	}
}

// The response's own Tier has to agree — it drives what the UI shows, so a
// worker-served call labelled LEAD misreports on screen as well as in the cost.
func TestTierOnTheResponseMatchesWhoServedIt(t *testing.T) {
	prevW, prevL := SharedWorkerLLM(), SharedLeadLLM()
	t.Cleanup(func() { SetSharedLLMs(prevW, prevL) })

	worker := &countingLLM{in: 10, out: 5}
	SetSharedLLMs(worker, nil)
	app := &AppCore{LLM: worker}

	resp, err := app.LeadChat(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("lead chat: %v", err)
	}
	if resp.Tier != WORKER {
		t.Errorf("Tier = %v, want WORKER — no lead is configured, so the worker served it", resp.Tier)
	}
}

// Guards the exact predicate. Reverting to `if fellBackToWorker` alone is the
// regression, and it is invisible in any test that only exercises a two-model
// deployment — which is why this asserts the WORKER-ONLY shape specifically.
func TestHasDistinctLeadDrivesTheAttribution(t *testing.T) {
	prevW, prevL := SharedWorkerLLM(), SharedLeadLLM()
	t.Cleanup(func() { SetSharedLLMs(prevW, prevL) })
	worker := &countingLLM{in: 1, out: 1}

	// No lead at all.
	SetSharedLLMs(worker, nil)
	if (&AppCore{LLM: worker}).HasDistinctLead() {
		t.Error("no lead is configured but HasDistinctLead says otherwise")
	}
	// LeadLLM set on the app, but nothing distinct registered process-wide:
	// GetLeadLLM hands back something that resolves to the same model.
	if (&AppCore{LLM: worker, LeadLLM: worker}).HasDistinctLead() {
		t.Error("a lead that is not distinct must not count as one — its tokens cost worker rates")
	}
	// A real second model.
	lead := &countingLLM{in: 2, out: 2}
	SetSharedLLMs(worker, lead)
	if !(&AppCore{LLM: worker, LeadLLM: lead}).HasDistinctLead() {
		t.Error("a genuinely distinct lead is not being recognized")
	}
}
