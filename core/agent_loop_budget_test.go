package core

import "testing"

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
