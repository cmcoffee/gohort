package core

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

import "testing"

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
