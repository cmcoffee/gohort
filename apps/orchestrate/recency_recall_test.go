package orchestrate

import (
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/kvlite"

	. "github.com/cmcoffee/gohort/core"
)

func withRecencyWeight(t *testing.T, w float64) func() {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	db.Set(WebTable, TunableRecencyWeight, w)
	SetTunablesDB(db)
	return func() { SetTunablesDB(nil) }
}

// TestRerankFindingsByRecency: a slightly-less-relevant but FRESH finding should
// overtake a slightly-more-relevant YEAR-OLD one only when recency weighting is
// on and strong enough. With it off, semantic score alone decides.
func TestRerankFindingsByRecency(t *testing.T) {
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	old := now.Add(-365 * 24 * time.Hour).Format(time.RFC3339)
	fresh := now.Add(-1 * 24 * time.Hour).Format(time.RFC3339)

	stale := SearchHit{ReportID: "stale", Score: 0.80, Date: old}
	recent := SearchHit{ReportID: "recent", Score: 0.75, Date: fresh}
	input := func() []SearchHit { return []SearchHit{stale, recent} }

	// Weight off → pure semantic order: the 0.80 hit stays first.
	restore := withRecencyWeight(t, 0)
	if got := rerankFindingsByRecency(input(), now); got[0].ReportID != "stale" {
		t.Fatalf("weight off should keep semantic order, got %q first", got[0].ReportID)
	}
	restore()

	// Strong weight → the year-old finding is down-weighted below the fresh one.
	restore = withRecencyWeight(t, 1)
	if got := rerankFindingsByRecency(input(), now); got[0].ReportID != "recent" {
		t.Fatalf("strong recency should promote the fresh finding, got %q first", got[0].ReportID)
	}
	restore()
}

// TestRecallAgeNote: findings show an absolute saved-date hint; a missing or
// unparseable date yields no note (never a bogus one).
//
// Dated RELATIVE to now. The literal date this once asserted kept its text
// only until real time moved past the caution threshold, at which point the
// test failed on correct behavior — a hardcoded date makes an age assertion
// rot by construction.
func TestRecallAgeNote(t *testing.T) {
	fresh := time.Now().AddDate(0, 0, -2)
	want := "(saved " + fresh.Format("2006-01-02") + ")"
	if got := recallAgeNote(fresh.Format(time.RFC3339)); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := recallAgeNote(""); got != "" {
		t.Fatalf("empty date should give no note, got %q", got)
	}
	if got := recallAgeNote("not-a-date"); got != "" {
		t.Fatalf("unparseable date should give no note, got %q", got)
	}
}
