package orchestrate

import (
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"

	. "github.com/cmcoffee/gohort/core"
)

// TestFindingConflictCandidates: only neighbors in the band
// [floor, dedup-ceiling) survive — duplicates and unrelated hits are dropped.
func TestFindingConflictCandidates(t *testing.T) {
	neighbors := []SearchHit{
		{ReportID: "dup", Score: 0.95},   // above dedup ceiling → excluded (it's a duplicate)
		{ReportID: "hi", Score: 0.75},    // in band
		{ReportID: "floor", Score: 0.60}, // == floor → included
		{ReportID: "low", Score: 0.55},   // below floor → excluded (unrelated)
	}
	var ids []string
	for _, h := range findingConflictCandidates(neighbors) {
		ids = append(ids, h.ReportID)
	}
	if got := strings.Join(ids, ","); got != "hi,floor" {
		t.Fatalf("band candidates = %q, want \"hi,floor\"", got)
	}
}

// TestRenderFindingConflictNote: the surfacing message names each conflicting
// finding with its saved-date hint and a recall id, and pluralizes.
func TestRenderFindingConflictNote(t *testing.T) {
	one := renderFindingConflictNote([]SearchHit{
		{ReportID: "r1", Title: "Craig lives in Santa Cruz", Date: "2026-01-15T00:00:00Z"},
	})
	for _, want := range []string{"a finding you already saved", "Craig lives in Santa Cruz", "(saved 2026-01-15)", "mem:r1", "Both are kept"} {
		if !strings.Contains(one, want) {
			t.Fatalf("single-conflict note missing %q: %s", want, one)
		}
	}

	two := renderFindingConflictNote([]SearchHit{
		{ReportID: "a", Title: "A"}, {ReportID: "b", Section: "## B"},
	})
	for _, want := range []string{"2 findings", "mem:a", "mem:b", `"B"`} {
		if !strings.Contains(two, want) {
			t.Fatalf("multi-conflict note missing %q: %s", want, two)
		}
	}
}

// TestDetectFindingConflictGateOff: with the rail off, a save never spends a
// worker call or emits a note, even when a band candidate is present.
func TestDetectFindingConflictGateOff(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	db.Set(WebTable, TunableConflictDetection, float64(0))
	SetTunablesDB(db)
	defer SetTunablesDB(nil)

	ct := &chatTurn{}
	band := []SearchHit{{ReportID: "x", Score: 0.75}} // in-band candidate present
	if got := ct.detectFindingConflict("new finding", band); got != "" {
		t.Fatalf("gate off must return no note even with a band candidate, got %q", got)
	}
}
