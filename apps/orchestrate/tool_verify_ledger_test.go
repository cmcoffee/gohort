package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestToolVerifyLedgerLatestOutcomeWins — the ledger is replace-by-name, so a
// tool's CURRENT standing is whatever happened last: a passing re-test clears an
// earlier failure, and an edit after a pass demotes it straight back. Getting
// this backwards in either direction defeats the gate (stale FAIL nags forever;
// stale PASS ships a broken tool).
func TestToolVerifyLedgerLatestOutcomeWins(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	const sid = "sess-1"

	recordToolVerify(db, sid, "sentiment_analyzer", false, "verification call failed")
	if got := unverifiedTools(db, sid); len(got) != 1 || got[0].Tool != "sentiment_analyzer" {
		t.Fatalf("failed tool should be unverified; got %+v", got)
	}

	// A passing re-test clears it.
	recordToolVerify(db, sid, "sentiment_analyzer", true, "")
	if got := unverifiedTools(db, sid); len(got) != 0 {
		t.Fatalf("passing re-test should clear the failure; got %+v", got)
	}

	// An edit invalidates the pass — the tool that was tested is not the tool
	// that now exists.
	recordToolVerify(db, sid, "sentiment_analyzer", false, "edited since it was last tested")
	got := unverifiedTools(db, sid)
	if len(got) != 1 {
		t.Fatalf("an edit must invalidate an earlier pass; got %+v", got)
	}
	if !strings.Contains(got[0].Reason, "edited") {
		t.Fatalf("reason should say why it's unverified; got %q", got[0].Reason)
	}
	// One row per tool, not one per event.
	if all := loadToolVerifications(db, sid); len(all) != 1 {
		t.Fatalf("ledger should hold one row per tool; got %d", len(all))
	}
}

// TestToolVerifyLedgerScopedToSession — one session's failures must not gate
// another's reply.
func TestToolVerifyLedgerScopedToSession(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	recordToolVerify(db, "sess-a", "broken", false, "boom")
	if got := unverifiedTools(db, "sess-b"); len(got) != 0 {
		t.Fatalf("session b must not see session a's ledger; got %+v", got)
	}
	clearToolVerifications(db, "sess-a")
	if got := unverifiedTools(db, "sess-a"); len(got) != 0 {
		t.Fatalf("clear should empty the slot; got %+v", got)
	}
}

// TestReportBuildGapsFlagsUnverifiedToolOnDoneSteps is the regression for the
// observed failure, end to end: EVERY step marked done (the model marks its own
// homework), but a tool whose verification failed. The gate used to answer "All
// steps completed successfully — no gaps to report", and the model duly told the
// user everything was implemented. It must now refuse.
func TestReportBuildGapsFlagsUnverifiedToolOnDoneSteps(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	const sid = "sess-1"
	turn := &chatTurn{
		udb: db,
		session: &ChatSession{
			ID: sid,
			BuildPlan: &BuildPlanState{
				Steps: []BuildPlanStep{
					{Number: 1, Title: "Create sub-agent", Status: "done"},
					{Number: 2, Title: "Create tool", Status: "done"},
				},
			},
		},
	}

	// Baseline: all done, nothing authored — the gate signs off.
	out, err := turn.reportBuildGapsToolDef().Handler(map[string]any{})
	if err != nil {
		t.Fatalf("report_build_gaps: %v", err)
	}
	if !strings.Contains(out, "no gaps to report") {
		t.Fatalf("clean plan should report no gaps; got %q", out)
	}

	// Now a tool exists whose verification failed, while the steps still read
	// "done" — the transcript's exact state.
	recordToolVerify(db, sid, "sentiment_analyzer", false, "verification call failed: script not found")

	out, err = turn.reportBuildGapsToolDef().Handler(map[string]any{})
	if err != nil {
		t.Fatalf("report_build_gaps: %v", err)
	}
	if strings.Contains(out, "no gaps to report") {
		t.Fatal("gate signed off while an authored tool was unverified — the exact bug: step status is self-reported and cannot stand in for verification")
	}
	if !strings.Contains(out, "sentiment_analyzer") {
		t.Fatalf("gap report must name the unverified tool; got %q", out)
	}
	if !strings.Contains(out, "unverified") {
		t.Fatalf("gap report must carry an unverified section; got %q", out)
	}
	if !strings.Contains(out, "Do NOT tell the user they are working") {
		t.Fatalf("gap report must forbid claiming an unverified tool works; got %q", out)
	}

	// Verifying it clears the gate — the gate must be satisfiable, or the model
	// learns to ignore it.
	recordToolVerify(db, sid, "sentiment_analyzer", true, "")
	out, err = turn.reportBuildGapsToolDef().Handler(map[string]any{})
	if err != nil {
		t.Fatalf("report_build_gaps: %v", err)
	}
	if !strings.Contains(out, "no gaps to report") {
		t.Fatalf("a verified tool should clear the gate; got %q", out)
	}
}
