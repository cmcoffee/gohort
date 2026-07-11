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
func TestRecallAgeNote(t *testing.T) {
	if got := recallAgeNote("2026-01-15T09:00:00Z"); got != "(saved 2026-01-15)" {
		t.Fatalf("got %q", got)
	}
	if got := recallAgeNote(""); got != "" {
		t.Fatalf("empty date should give no note, got %q", got)
	}
	if got := recallAgeNote("not-a-date"); got != "" {
		t.Fatalf("unparseable date should give no note, got %q", got)
	}
}
