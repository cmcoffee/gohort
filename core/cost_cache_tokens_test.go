package core

// The cost side of the cached prompt.
//
// v0.5.999 taught the SESSION UI that InputTokens is only the uncached
// remainder and the real prompt is the sum of three numbers. The cost tracker
// was not told: it kept recording resp.InputTokens alone, so a 38,679-token
// turn served almost entirely from cache went into the ledger as 2. The chart
// said 2, the session said 38,679, and the expensive turns were the ones that
// looked free.

import (
	"context"
	"strings"
	"testing"
)

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
