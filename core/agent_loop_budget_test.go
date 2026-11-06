package core

import (
	"sync"
	"testing"
)

// A caller can turn a turn away before it starts when an allowance it would
// run under is already spent, and says whose it was.
func TestASpentAllowanceIsSeenBeforeARunStarts(t *testing.T) {
	withRootDB(t)
	withRates(t)
	person := SharedBudget{Key: "outside|ann|a1", Label: "this person's", DailySpendUSD: 0.50}
	if over, _, _, _ := OverDailySpend([]SharedBudget{person}); over {
		t.Fatal("a fresh allowance read as spent")
	}
	// A lead round at a dollar per 1k tokens: 400 in + 100 out = $0.50.
	chargeDailySpend(AgentLoopConfig{SharedBudgets: []SharedBudget{person}}, &Response{Tier: LEAD, InputTokens: 400, OutputTokens: 100})
	over, spent, limit, label := OverDailySpend([]SharedBudget{person})
	if !over || spent < 0.49 || limit != 0.50 || label != "this person's" {
		t.Errorf("a spent allowance: over=%v spent=%.2f limit=%.2f label=%q", over, spent, limit, label)
	}
	// Another key's spend is not this one's.
	if over, _, _, _ := OverDailySpend([]SharedBudget{{Key: "outside|ben|a1", DailySpendUSD: 0.50}}); over {
		t.Error("one person's spend counted against another")
	}
}

// Charges to one ledger at once are each kept: read, appended and written
// apart, two at once read the same entries and the second write dropped the
// first's cost.
func TestChargesAtOnceAreAllKept(t *testing.T) {
	withRootDB(t)
	const n = 40
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			appendSpend("outside-all|a1", 0.01)
		}()
	}
	wg.Wait()
	if total, entries := dailySpend("outside-all|a1"); len(entries) != n || total < 0.39 || total > 0.41 {
		t.Errorf("after %d charges at once the ledger holds %d entries, $%.2f", n, len(entries), total)
	}
}
