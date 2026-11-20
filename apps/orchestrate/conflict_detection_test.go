package orchestrate

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/kvlite"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/costledger"
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
	if got, err := judgeFindingConflicts(context.Background(), full.Chat, "The bridge reopened.", hits); got != nil || err != nil || full.Calls() != 0 {
		t.Errorf("settled none, the full check still ran: %v after %d calls", got, full.Calls())
	}
	QuickDecide = func(context.Context, QuickChoice) (string, bool) { return "", false }
	if got, err := judgeFindingConflicts(context.Background(), full.Chat, "The bridge reopened.", hits); len(got) != 1 || err != nil || full.Calls() != 1 {
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
	if got, err := judgeFindingConflicts(context.Background(), prose.Chat, "The bridge reopened.", hits); got != nil || err == nil {
		t.Fatalf("an unreadable check: %v, %v; want no hits and an error", got, err)
	}
	tc := conflictCases()[0]
	out := runConflictCase(context.Background(), prose, tc)
	if out.Got != ScanNoVerdict || !strings.Contains(out.Reason, "These two findings disagree") {
		t.Errorf("the check test reads it as %q (%s), want no_verdict with the reply", out.Got, out.Reason)
	}
}

// The check runs under the work it is part of: its calls carry that work's
// context (who pays, a check test's watch, its deadline), and work already
// stopped asks nothing.
func TestTheConflictCheckRunsUnderItsCaller(t *testing.T) {
	prev := QuickDecide
	t.Cleanup(func() { QuickDecide = prev })
	var quickPayer costledger.Spender
	QuickDecide = func(ctx context.Context, _ QuickChoice) (string, bool) {
		quickPayer = costledger.SpenderFrom(ctx)
		return "", false
	}
	full := &FakeLLM{Turns: []FakeTurn{{Content: `{"findings":[{"finding":1,"why":"closed vs reopened","answer":"contradicts"}]}`, Repeat: true}}}
	var payer costledger.Spender
	chat := func(ctx context.Context, msgs []Message, opts ...ChatOption) (*Response, error) {
		payer = costledger.SpenderFrom(ctx)
		return full.Chat(ctx, msgs, opts...)
	}
	hits := []SearchHit{{ID: "1", Text: "The bridge is closed."}}
	ctx := costledger.WithSpender(context.Background(), costledger.Spender{Person: "alice"})
	if got, err := judgeFindingConflicts(ctx, chat, "The bridge reopened.", hits); len(got) != 1 || err != nil {
		t.Fatalf("the check: %v %v", got, err)
	}
	if payer.Person != "alice" || quickPayer.Person != "alice" {
		t.Errorf("the check's calls ran outside its caller's work: full %+v, System One %+v", payer, quickPayer)
	}
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := judgeFindingConflicts(stopped, chat, "The bridge reopened.", hits); err == nil {
		t.Error("work already stopped still got a verdict")
	}
	if _, err := judgeFindingConflicts(nil, chat, "The bridge reopened.", hits); err != nil {
		t.Errorf("no caller context: %v", err)
	}
}

// When the full check gives no verdict but System One was sure of a
// contradiction, the save is still flagged on its word: with one finding it
// names it, with several it says one of them. Not sure, or sure of none, and
// a check with no verdict flags nothing; a full check that answers decides.
// The check test still reads the model's failure as its own.
func TestANoVerdictFallsBackOnASureSystemOne(t *testing.T) {
	prev := QuickDecide
	t.Cleanup(func() { QuickDecide = prev })
	lean := "contradicts"
	QuickDecide = func(ctx context.Context, q QuickChoice) (string, bool) {
		if q.Key != "memory.conflict_check" {
			return "", false
		}
		return lean, false
	}
	refused := &FakeLLM{Turns: []FakeTurn{{Content: "", Repeat: true}}}
	one := []SearchHit{{ID: "1", ReportID: "r1", Title: "Bridge closed", Text: "The bridge is closed."}}
	two := append(one, SearchHit{ID: "2", ReportID: "r2", Title: "Bridge built", Text: "The bridge was built in 1962."})

	note := findingConflictNote(context.Background(), refused.Chat, "The bridge reopened.", one)
	for _, want := range []string{findingConflictMarker, "a quick check says this may contradict a finding you already saved", `"Bridge closed"`, "mem:r1", "The full check gave no verdict"} {
		if !strings.Contains(note, want) {
			t.Errorf("one finding: the note lacks %q: %s", want, note)
		}
	}
	note = findingConflictNote(context.Background(), refused.Chat, "The bridge reopened.", two)
	for _, want := range []string{"one of these findings", "without saying which", "mem:r1", "mem:r2"} {
		if !strings.Contains(note, want) {
			t.Errorf("two findings: the note lacks %q: %s", want, note)
		}
	}

	// The check test: no verdict, whatever System One leaned to.
	tc := conflictCases()[0]
	if out := runConflictCase(context.Background(), refused, tc); out.Got != ScanNoVerdict {
		t.Errorf("the check test read a failed full check with a lean as %q", out.Got)
	}

	// A full check that answers decides, lean or not.
	agrees := &FakeLLM{Turns: []FakeTurn{{Content: `{"findings":[{"finding":1,"why":"same claim","answer":"consistent"}]}`, Repeat: true}}}
	if note := findingConflictNote(context.Background(), agrees.Chat, "The bridge reopened.", one); note != "" {
		t.Errorf("the full check said consistent, the lean flagged it: %s", note)
	}
	finds := &FakeLLM{Turns: []FakeTurn{{Content: `{"findings":[{"finding":1,"why":"closed vs open","answer":"contradicts"}]}`, Repeat: true}}}
	if note := findingConflictNote(context.Background(), finds.Chat, "The bridge reopened.", one); !strings.Contains(note, "this may contradict a finding you already saved") || strings.Contains(note, "quick check") {
		t.Errorf("the full check's own note: %s", note)
	}

	// Not sure, and no verdict: nothing flagged.
	lean = ""
	if note := findingConflictNote(context.Background(), refused.Chat, "The bridge reopened.", one); note != "" {
		t.Errorf("no lean, no verdict, still flagged: %s", note)
	}
	// A lean on another answer is no contradiction.
	lean = "none"
	if note := findingConflictNote(context.Background(), refused.Chat, "The bridge reopened.", one); note != "" {
		t.Errorf("a lean that is not a contradiction flagged it: %s", note)
	}
	// A sure, standing answer is not a lean either.
	QuickDecide = func(ctx context.Context, q QuickChoice) (string, bool) { return "contradicts", true }
	if note := findingConflictNote(context.Background(), refused.Chat, "The bridge reopened.", one); note != "" {
		t.Errorf("an answer given as standing was taken as a lean: %s", note)
	}
}

// A turn's save checks its in-band neighbours as part of the turn: the
// worker's check is paid for by its person, and what it finds is the note.
func TestATurnsSaveIsCheckedUnderTheTurn(t *testing.T) {
	prev := QuickDecide
	t.Cleanup(func() { QuickDecide = prev })
	QuickDecide = nil
	SetTunablesDB(&DBase{Store: kvlite.MemStore()})
	defer SetTunablesDB(nil)
	llm := &payerLLM{FakeLLM: &FakeLLM{}, marker: "EXISTING findings", reply: `{"findings":[{"finding":1,"why":"closed vs open","answer":"contradicts"}]}`}
	T := &OrchestrateApp{AppCore: AppCore{LLM: llm}}
	ct := &chatTurn{app: T, ctx: costledger.WithSpender(context.Background(), costledger.Spender{Person: "alice"})}
	band := []SearchHit{{ReportID: "x", Title: "Bridge closed", Text: "The bridge is closed.", Score: 0.75}}
	note := ct.detectFindingConflict("The bridge reopened.", band)
	if !strings.Contains(note, `"Bridge closed"`) {
		t.Errorf("the turn's note: %q", note)
	}
	if paid := llm.paid(); len(paid) != 1 || paid[0].Person != "alice" {
		t.Errorf("the check's payers: %+v", paid)
	}
}
