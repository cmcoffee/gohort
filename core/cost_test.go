package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

// "in=961,041" can be worth thirty cents or three dollars depending on
// how much of it was a cache read, a cache write, or fresh input. The
// report that exists to answer "what did this cost" could not answer
// "is caching being counted at all", which is the question a surprising
// number provokes.

func TestUsageReportShowsTheCacheSplit(t *testing.T) {
	prev := GetCostRates()
	prevConfigured := RatesConfigured()
	SetCostRates(CostRates{LeadInputPer1K: 0.003, LeadOutputPer1K: 0.015})
	t.Cleanup(func() {
		if prevConfigured {
			SetCostRates(prev)
		}
	})

	d := UsageDiff{LeadInput: 2, LeadCacheRead: 175045, LeadCacheWrite: 785994, LeadOutput: 3423}
	out := FormatUsageReport("orchestrate /api/send", d)

	// The whole prompt is still the headline — that is what the model saw.
	if !strings.Contains(out, "in=961041") {
		t.Errorf("the prompt total should stay the headline figure:\n%s", out)
	}
	// And the split must be there, or the total is unreadable.
	for _, want := range []string{"fresh=2", "cached=175045", "written=785994"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s from the report:\n%s", want, out)
		}
	}

	// The cost must apply the multipliers, not price the whole prompt as
	// fresh input. Fresh 2 + read 175045*0.10 + write 785994*1.25 =
	// 999,999.7 billable-equivalent tokens at 0.003/1K ≈ $3.00, whereas
	// pricing all 961,041 as fresh would give $2.88 — close enough to
	// look plausible, which is exactly why this needs pinning.
	got := GetCostRates().Estimate(d)
	if got < 2.99 || got > 3.06 {
		t.Errorf("cache-weighted estimate = %.4f, want ≈3.00", got)
	}
	flat := 961041.0 / 1000.0 * 0.003
	if got <= flat {
		t.Errorf("a write-heavy prompt must cost MORE than the same tokens priced flat (%.4f vs %.4f) — writes carry a premium", got, flat)
	}

	// A provider that reports no caching gets no column of zeroes.
	plain := FormatUsageReport("x", UsageDiff{WorkerInput: 10, WorkerOutput: 5})
	if strings.Contains(plain, "fresh=") {
		t.Errorf("no cache counters means no split:\n%s", plain)
	}
}

// The multipliers are settable and their EFFECTIVE values are what a
// settings form must show. They were reachable only by editing the
// stored record — so a deployment on 1-hour caching had no way to
// correct a 37.5% under-report of every cache write.
func TestCacheMultipliersAreSettableAndReportEffectiveValues(t *testing.T) {
	// Unset: the defaults are in force, and that is what a form shows.
	var zero CostRates
	if got := zero.EffectiveCacheReadMultiplier(); got != 0.10 {
		t.Errorf("default read multiplier = %v, want 0.10", got)
	}
	if got := zero.EffectiveCacheWriteMultiplier(); got != 1.25 {
		t.Errorf("default write multiplier = %v, want 1.25 (the 5-minute TTL figure)", got)
	}

	// Set: the override wins, and it actually changes the money.
	fiveMin := CostRates{LeadInputPer1K: 3.0}
	oneHour := CostRates{LeadInputPer1K: 3.0, CacheWriteMultiplier: 2.0}
	if got := oneHour.EffectiveCacheWriteMultiplier(); got != 2.0 {
		t.Fatalf("override ignored: %v", got)
	}
	d := UsageDiff{LeadCacheWrite: 100000}
	cheap, dear := fiveMin.Estimate(d), oneHour.Estimate(d)
	if dear <= cheap {
		t.Fatalf("1-hour caching must cost more per write: %.4f vs %.4f", dear, cheap)
	}
	// 2.0/1.25 = 1.6x exactly — the size of the correction being offered.
	if ratio := dear / cheap; ratio < 1.59 || ratio > 1.61 {
		t.Errorf("expected a 1.6x difference between the two TTLs, got %.3f", ratio)
	}
}

// The chart's own arithmetic. AggregateDailyCost summed the four cache
// counters correctly and then built a UsageDiff literal WITHOUT them on
// the way into Estimate — so a day whose spend was almost entirely
// cached priced at a rounding error while the per-request line said
// $11.45. That is the exact shape of "the costs I see in orchestrate
// don't show up in the admin UI".
//
// dailyCostUsage exists because two copies of this projection had to be
// kept in step by hand; this was a third copy nobody updated.
func TestChartPricesTheCachedShareOfADay(t *testing.T) {
	prev, prevSet := GetCostRates(), RatesConfigured()
	SetCostRates(CostRates{LeadInputPer1K: 0.003, LeadOutputPer1K: 0.015})
	t.Cleanup(func() {
		if prevSet {
			SetCostRates(prev)
		}
	})

	// One real request: almost nothing fresh, millions cached, a big write.
	day := time.Now().Format("2006-01-02")
	rec := DatedUsage{Date: day, Usage: UsageDiff{
		LeadInput: 17, LeadCacheRead: 5059849, LeadCacheWrite: 1387657, LeadOutput: 9773,
	}}

	got := AggregateDailyCost([]DatedUsage{rec}, 7)
	var today DailyCost
	for _, d := range got {
		if d.Date == day {
			today = d
		}
	}
	if today.Date == "" {
		t.Fatal("today is missing from the window")
	}
	// The counters must survive aggregation...
	if today.LeadCacheRead != 5059849 || today.LeadCacheWrite != 1387657 {
		t.Fatalf("cache counters lost in aggregation: %+v", today)
	}
	// ...and reach the price. Fresh alone would be 17 tokens ≈ $0.00005.
	if today.Cost < 5 {
		t.Errorf("the day priced at $%.5f — the cached share was dropped on the way into Estimate", today.Cost)
	}
	// It must equal what the per-request report would say for the same usage.
	perRequest := GetCostRates().Estimate(rec.Usage)
	if diff := today.Cost - perRequest; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("chart says $%.4f, the request report says $%.4f — the two must agree", today.Cost, perRequest)
	}

	// The windowless branch is a second copy of the same arithmetic.
	if all := AggregateDailyCost([]DatedUsage{rec}, 0); len(all) != 1 || all[0].Cost < 5 {
		t.Errorf("the windowless path dropped the cached share: %+v", all)
	}
}

// The cost side of the cached prompt.
//
// v0.5.999 taught the SESSION UI that InputTokens is only the uncached
// remainder and the real prompt is the sum of three numbers. The cost tracker
// was not told: it kept recording resp.InputTokens alone, so a 38,679-token
// turn served almost entirely from cache went into the ledger as 2. The chart
// said 2, the session said 38,679, and the expensive turns were the ones that
// looked free.

// A near-total cache hit, exactly as the Anthropic API reports one.
func cachedTurn() *Response {
	return &Response{InputTokens: 2, OutputTokens: 25, CacheReadTokens: 38_000, CacheWriteTokens: 677}
}

func TestTheCachedPromptIsCountedNotDiscarded(t *testing.T) {
	start := ProcessUsage().Snapshot()
	(&AppCore{}).trackTokens(context.Background(), cachedTurn())
	d := ProcessUsage().Diff(start)

	if d.WorkerInput != 2 {
		t.Errorf("uncached remainder = %d, want the 2 the API reported", d.WorkerInput)
	}
	if d.WorkerCacheRead != 38_000 || d.WorkerCacheWrite != 677 {
		t.Errorf("cached share = read %d / write %d, want 38000 / 677 — this is where the prompt went",
			d.WorkerCacheRead, d.WorkerCacheWrite)
	}
	// The number a human means by "input", and the number the session UI shows.
	if got := d.WorkerPrompt(); got != 38_679 {
		t.Errorf("worker prompt = %d, want 38679 — the session UI shows that, and the two must agree", got)
	}
}

// Size is a plain sum; cost is a weighted one. Pricing the cached share at the
// full input rate overstates a cache-heavy turn about as badly as dropping it
// understates one.
func TestCostWeightsTheCachedShare(t *testing.T) {
	rates := CostRates{WorkerInputPer1K: 1.0, WorkerOutputPer1K: 0}

	full := rates.Estimate(UsageDiff{WorkerInput: 1000})
	read := rates.Estimate(UsageDiff{WorkerCacheRead: 1000})
	write := rates.Estimate(UsageDiff{WorkerCacheWrite: 1000})

	if read >= full {
		t.Errorf("a cache read (%.4f) is not cheaper than an ordinary input token (%.4f)", read, full)
	}
	if write <= full {
		t.Errorf("a cache write (%.4f) should cost slightly more than an ordinary input token (%.4f)", write, full)
	}
	// And it is not free: a stored zero multiplier on a deployment that
	// predates these fields must fall back to the default, not to nothing.
	if read == 0 {
		t.Error("the cached share priced at zero — the largest part of a prompt would cost nothing")
	}
}

// Reporting shows the whole prompt, so the operator-facing summary and the
// session UI cannot disagree.
func TestUsageReportShowsTheWholePrompt(t *testing.T) {
	d := UsageDiff{WorkerInput: 2, WorkerCacheRead: 38_000, WorkerCacheWrite: 677, WorkerOutput: 25}
	if out := FormatUsageReport("run", d); !strings.Contains(out, "in=38679") {
		t.Errorf("report shows the uncached remainder rather than the prompt:\n%s", out)
	}
	if out := FormatUsage(d); !strings.Contains(out, "worker in=38679") {
		t.Errorf("summary shows the uncached remainder rather than the prompt:\n%s", out)
	}
}

// A turn that was ENTIRELY a cache hit must still be recorded. The old
// emptiness guards predated the cache counters, so the most expensive kind of
// conversation matched "nothing happened" and was dropped.
func TestAFullyCachedTurnIsNotTreatedAsEmpty(t *testing.T) {
	sess := &Session{Tier: WORKER}
	sess.recordTokens(&Response{CacheReadTokens: 41_318, Tier: WORKER})
	if got := sess.AsDiff().WorkerCacheRead; got != 41_318 {
		t.Errorf("a fully-cached turn recorded %d cache-read tokens, want 41318", got)
	}
	if got := sess.Report().Input; got != 41_318 {
		t.Errorf("the session's flat input = %d, want the whole prompt", got)
	}
}

// What the cost chart SHOWS has to be able to produce what the cost chart
// CHARGES.
//
// DailyCost.LeadInput is the provider's input_tokens — the uncached remainder
// only — while DailyCost.Cost prices all three components of the prompt. The
// admin chart displayed the first beside the second, so on a cache-heavy
// deployment (any long agent conversation against a caching provider) it
// reported a few hundred lead input tokens next to a dollar figure earned by a
// few hundred thousand. Neither number was wrong; together they were
// unreadable, and the honest conclusion an operator reached was that the cost
// was invented.

// A day of agent turns against a caching provider: almost the whole prompt
// arrives as cache reads, with a small fresh remainder and one prefix write.
func cachedLeadDay() []DatedUsage {
	return []DatedUsage{{
		Date: "2026-08-17",
		Usage: UsageDiff{
			LeadInput: 640, LeadOutput: 12_400,
			LeadCacheRead: 1_842_000, LeadCacheWrite: 61_500,
		},
	}}
}

func TestTheChartsInFigureIsTheWholePrompt(t *testing.T) {
	rows := AggregateDailyCost(cachedLeadDay(), 0)
	if len(rows) != 1 {
		t.Fatalf("aggregated %d rows, want 1", len(rows))
	}
	d := rows[0]

	// 640 + 1,842,000 + 61,500 — the prompt the model actually read.
	if want := int64(1_904_140); d.LeadPrompt != want {
		t.Errorf("lead prompt = %d, want %d — the chart's \"Lead in\" row reads this field, and showing the %d-token uncached remainder instead is what made a six-figure prompt look like a rounding error",
			d.LeadPrompt, want, d.LeadInput)
	}
	// The uncached remainder is still carried, unchanged: it is the only one
	// of the three billed at the full input rate, so pricing needs it apart.
	if d.LeadInput != 640 {
		t.Errorf("uncached remainder = %d, want the 640 the provider reported", d.LeadInput)
	}
	if d.WorkerPrompt != 0 {
		t.Errorf("worker prompt = %d on a lead-only day, want 0", d.WorkerPrompt)
	}
}

// The reconciliation the operator performs by eye: the rows shown in the
// hover breakdown, priced at the rates shown in the Prices form, must come
// out at the cost shown on the bar.
func TestTheDisplayedTokensReproduceTheDisplayedCost(t *testing.T) {
	rates := CostRates{
		LeadInputPer1K: 0.003, LeadOutputPer1K: 0.015,
		CacheReadMultiplier: 0.1, CacheWriteMultiplier: 1.25,
	}
	prior := GetCostRates()
	SetCostRates(rates)
	defer SetCostRates(prior)

	d := AggregateDailyCost(cachedLeadDay(), 0)[0]

	// Every visible row, at its documented weight. "Lead in" is the whole
	// prompt, so the fresh share is what remains after the two cache rows.
	fresh := d.LeadPrompt - d.LeadCacheRead - d.LeadCacheWrite
	byHand := float64(fresh)/1000*rates.LeadInputPer1K +
		float64(d.LeadCacheRead)/1000*rates.LeadInputPer1K*rates.CacheReadMultiplier +
		float64(d.LeadCacheWrite)/1000*rates.LeadInputPer1K*rates.CacheWriteMultiplier +
		float64(d.LeadOutput)/1000*rates.LeadOutputPer1K

	if diff := d.Cost - byHand; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("the chart charges $%.6f but the rows it displays add up to $%.6f — an operator checking the arithmetic finds it does not close", d.Cost, byHand)
	}
}

// Report() aggregates from sessions rather than the process tracker, and its
// aggregation step used to carry four of the eight token fields.
func TestBuildDiffCarriesTheCachedShare(t *testing.T) {
	sess := &Session{Tier: LEAD}
	sess.recordTokens(&Response{
		InputTokens: 2, OutputTokens: 25,
		CacheReadTokens: 38_000, CacheWriteTokens: 677,
		Tier: LEAD,
	})

	d := BuildDiff(ProcessUsage().Snapshot(), sess)

	if d.LeadCacheRead != 38_000 || d.LeadCacheWrite != 677 {
		t.Errorf("cached share = read %d / write %d, want 38000 / 677 — dropped here, the end-of-run report describes a large cached run as two fresh tokens",
			d.LeadCacheRead, d.LeadCacheWrite)
	}
	if got := d.LeadPrompt(); got != 38_679 {
		t.Errorf("lead prompt = %d, want 38679", got)
	}
}

// TestCostLedger: recording metered calls rolls up per-source and per-day, and
// a zero/absent rate records nothing.
func TestCostLedger(t *testing.T) {
	SetCostLedgerDB(memDB(t))

	// Two calls to one hook, one to a credential; a free hook records nothing.
	RecordExternalCost("hook:westlaw", "Westlaw", 0.10)
	RecordExternalCost("hook:westlaw", "Westlaw", 0.10)
	RecordExternalCost("cred:vendor", "Vendor API", 0.05)
	RecordExternalCost("hook:free", "Free Hook", 0) // untracked — no-op
	RecordExternalCost("", "bad", 0.10)             // empty source — no-op

	by := CostBySource(30)
	if len(by) != 2 {
		t.Fatalf("expected 2 sources, got %d: %+v", len(by), by)
	}
	// Sorted by cost desc — Westlaw (0.20) before Vendor (0.05).
	if by[0].SourceID != "hook:westlaw" || by[0].Calls != 2 {
		t.Errorf("top source wrong: %+v", by[0])
	}
	if d := by[0].Cost - 0.20; d > 1e-9 || d < -1e-9 {
		t.Errorf("westlaw cost = %g, want 0.20", by[0].Cost)
	}
	if by[1].SourceID != "cred:vendor" || by[1].Calls != 1 {
		t.Errorf("second source wrong: %+v", by[1])
	}

	// Daily total folds both sources into today's bucket: 0.20 + 0.05 = 0.25.
	daily := CostExternalDaily(30)
	var total float64
	for _, v := range daily {
		total += v
	}
	if d := total - 0.25; d > 1e-9 || d < -1e-9 {
		t.Errorf("daily external total = %g, want 0.25", total)
	}
}

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
