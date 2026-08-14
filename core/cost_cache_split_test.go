package core

// "in=961,041" can be worth thirty cents or three dollars depending on
// how much of it was a cache read, a cache write, or fresh input. The
// report that exists to answer "what did this cost" could not answer
// "is caching being counted at all", which is the question a surprising
// number provokes.

import (
	"strings"
	"testing"
)

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
