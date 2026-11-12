package orchestrate

import (
	"context"
	"strings"
	"testing"
	"time"

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
	// Dated relative to now: a literal date keeps its rendering only until real
	// time crosses an age threshold, at which point the assertion fails on
	// correct behavior.
	saved := time.Now().AddDate(0, 0, -2)
	one := renderFindingConflictNote([]SearchHit{
		{ReportID: "r1", Title: "Craig lives in Santa Cruz", Date: saved.Format(time.RFC3339)},
	})
	for _, want := range []string{"a finding you already saved", "Craig lives in Santa Cruz", "(saved " + saved.Format("2006-01-02") + ")", "mem:r1", "Both are kept"} {
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

// The rail is ON by default. It shipped off, on a cost estimate that read the
// worker call as per-save; it is per-save-that-already-found-a-neighbour, and
// the search it needs is one dedup performs anyway.
//
// Pinned because a default is a one-character edit and the failure is silent:
// findings would go back to accumulating beside the ones they supersede, with
// recall returning both and nothing marking which is current.
func TestConflictDetectionIsOnByDefault(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()} // nothing stored → spec default applies
	SetTunablesDB(db)
	defer SetTunablesDB(nil)

	if !conflictDetectionEnabled() {
		t.Error("a deployment that has never touched this knob should have the rail on")
	}
}

// And an operator who turns it off still gets that.
func TestConflictDetectionCanStillBeTurnedOff(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	db.Set(WebTable, TunableConflictDetection, float64(0))
	SetTunablesDB(db)
	defer SetTunablesDB(nil)

	if conflictDetectionEnabled() {
		t.Error("an explicit 0 must win over the default")
	}
}

// Where System One settles the conflict check ("none", sure, on a mapped
// check), the full check is not asked; a contradiction is never taken from
// it, since the full check names which findings.
func TestTheConflictCheckTakesASettledNone(t *testing.T) {
	prev := QuickDecide
	t.Cleanup(func() { QuickDecide = prev })
	QuickDecide = func(ctx context.Context, q QuickChoice) (string, bool) {
		return "none", q.Key == "memory.conflict_check"
	}
	full := &FakeLLM{Turns: []FakeTurn{{Content: `{"findings":[{"finding":1,"why":"closed vs reopened","answer":"contradicts"}]}`, Repeat: true}}}
	hits := []SearchHit{{ID: "1", Text: "The bridge is closed."}}
	if got, err := judgeFindingConflicts(full.Chat, "The bridge reopened.", hits); got != nil || err != nil || full.Calls() != 0 {
		t.Errorf("settled none, the full check still ran: %v after %d calls", got, full.Calls())
	}
	QuickDecide = func(context.Context, QuickChoice) (string, bool) { return "", false }
	if got, err := judgeFindingConflicts(full.Chat, "The bridge reopened.", hits); len(got) != 1 || err != nil || full.Calls() != 1 {
		t.Errorf("unsettled, the full check decides: %v after %d calls", got, full.Calls())
	}
}

// A conflict check whose reply cannot be read gives no verdict, saying what
// came back, never "none": a model answering in another shape read as
// missing every contradiction in the check tests, and live a contradiction
// went unflagged with nothing said.
func TestAnUnreadableConflictCheckIsNoVerdict(t *testing.T) {
	prev := QuickDecide
	t.Cleanup(func() { QuickDecide = prev })
	QuickDecide = func(context.Context, QuickChoice) (string, bool) { return "", false }
	prose := &FakeLLM{Turns: []FakeTurn{{Content: "These two findings disagree about the bridge.", Repeat: true}}}
	hits := []SearchHit{{ID: "1", Text: "The bridge is closed."}}
	if got, err := judgeFindingConflicts(prose.Chat, "The bridge reopened.", hits); got != nil || err == nil {
		t.Fatalf("an unreadable check: %v, %v; want no hits and an error", got, err)
	}
	tc := conflictCases()[0]
	out := runConflictCase(context.Background(), prose, tc)
	if out.Got != ScanNoVerdict || !strings.Contains(out.Reason, "These two findings disagree") {
		t.Errorf("the check test reads it as %q (%s), want no_verdict with the reply", out.Got, out.Reason)
	}
}
