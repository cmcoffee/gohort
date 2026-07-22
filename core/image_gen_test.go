package core

import (
	"strings"
	"testing"
)

// NextImageAttempt tracks two counters: the retry CHAIN (resets on a new subject,
// bounds the budget) and the absolute TOTAL (never resets, feeds the hard cap).
func TestNextImageAttemptCounts(t *testing.T) {
	var nilSess *ToolSession
	if a, tot := nilSess.NextImageAttempt(true); a != 1 || tot != 1 {
		t.Fatalf("nil session: want (1,1), got (%d,%d)", a, tot)
	}
	sess := &ToolSession{}
	// First image (not a refine) → chain 1.
	if a, tot := sess.NextImageAttempt(false); a != 1 || tot != 1 {
		t.Fatalf("first: want (1,1), got (%d,%d)", a, tot)
	}
	// Two refines → chain climbs, total climbs.
	if a, tot := sess.NextImageAttempt(true); a != 2 || tot != 2 {
		t.Fatalf("refine 1: want (2,2), got (%d,%d)", a, tot)
	}
	if a, tot := sess.NextImageAttempt(true); a != 3 || tot != 3 {
		t.Fatalf("refine 2: want (3,3), got (%d,%d)", a, tot)
	}
	// New subject → chain RESETS to 1, but total keeps climbing (hard cap is
	// evasion-proof).
	if a, tot := sess.NextImageAttempt(false); a != 1 || tot != 4 {
		t.Fatalf("new subject: want chain 1 / total 4, got (%d,%d)", a, tot)
	}
}

// imageVerifyText carries the checkable-criteria gate and the shrinking retry
// budget: it must offer regeneration while budget remains, then forbid it once
// spent, and never mention retry when the budget is zero (Tier-1-only mode).
func TestImageVerifyTextBudget(t *testing.T) {
	// Budget of 2: attempt 1 → 2 left, attempt 2 → 1 left, attempt 3 → spent.
	if s := imageVerifyText(1, 2); !strings.Contains(s, "2 regeneration(s) left") {
		t.Fatalf("attempt 1/budget 2 should offer 2 regens: %q", s)
	}
	if s := imageVerifyText(2, 2); !strings.Contains(s, "1 regeneration(s) left") {
		t.Fatalf("attempt 2/budget 2 should offer 1 regen: %q", s)
	}
	spent := imageVerifyText(3, 2)
	if !strings.Contains(spent, "budget for this image is spent") || strings.Contains(spent, "regeneration(s) left") {
		t.Fatalf("attempt 3/budget 2 should be spent, no offer: %q", spent)
	}
	// Budget 0 = verification only: no retry language at all.
	zero := imageVerifyText(1, 0)
	if strings.Contains(zero, "regeneration") || strings.Contains(zero, "generate_image again") {
		t.Fatalf("budget 0 should not mention retry: %q", zero)
	}
	// Every variant keeps the gate (verify against explicit criteria).
	for _, s := range []string{imageVerifyText(1, 2), spent, zero} {
		if !strings.Contains(s, "EXPLICITLY asked for") {
			t.Fatalf("missing checkable-criteria gate: %q", s)
		}
	}
}
