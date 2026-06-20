package core

import "testing"

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
