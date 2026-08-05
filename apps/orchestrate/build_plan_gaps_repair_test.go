package orchestrate

// report_build_gaps on a REPAIR — no build plan, because nobody presents a plan
// to fix one broken tool. It used to hard-error there, which removed the check
// exactly where it mattered most: an agent that edited a tool twice, watched
// its test fail twice, and then wrote a confident "this can't be fixed" reply.

import (
	"strings"
	"testing"
)

func TestReportBuildGapsWorksWithoutAPlan(t *testing.T) {
	turn := &chatTurn{session: &ChatSession{ID: "s1"}}
	out, err := turn.reportBuildGapsToolDef().Handler(map[string]any{})
	if err != nil {
		t.Fatalf("a repair has no build plan; report_build_gaps must still run: %v", err)
	}
	if !strings.Contains(out, "no gaps") {
		t.Errorf("with nothing unverified it should report no gaps; got %q", out)
	}
}

func TestReportBuildGapsStillNeedsASession(t *testing.T) {
	turn := &chatTurn{}
	if _, err := turn.reportBuildGapsToolDef().Handler(map[string]any{}); err == nil {
		t.Error("without a session there is no ledger to grade — that must still error")
	}
}
