package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func gateSession(sid string, statuses ...string) *ChatSession {
	steps := make([]BuildPlanStep, 0, len(statuses))
	for i, st := range statuses {
		steps = append(steps, BuildPlanStep{Number: i + 1, Title: "step", Status: st})
	}
	return &ChatSession{ID: sid, BuildPlan: &BuildPlanState{Steps: steps}}
}

// TestGapGateFiresWhenPlanClosedWithoutReport — the enforcement this adds:
// GapsReported was written and never read, so a model could finish the plan and
// reply without ever gap-checking. Every step done, no report, a reply anyway.
func TestGapGateFiresWhenPlanClosedWithoutReport(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	sess := gateSession("s1", "done", "done")

	if !injectSkippedGapReportWarning(sess, db, "All requested enhancements have been implemented.") {
		t.Fatal("gate must fire: plan closed out with no report_build_gaps call")
	}
	last := sess.Messages[len(sess.Messages)-1]
	if last.Role != "user" || !last.Hidden {
		t.Fatalf("correction must be a hidden user-role note (matching the sibling checks); got role=%q hidden=%v", last.Role, last.Hidden)
	}
	if !strings.Contains(last.Content, "report_build_gaps") {
		t.Fatalf("note must name the skipped call; got %q", last.Content)
	}
}

// TestGapGateCarriesUnverifiedTools — skipping the call is exactly how an
// unverified tool stays invisible, so the correction must carry the payload the
// skipped call would have delivered. Otherwise the next turn re-runs blind.
func TestGapGateCarriesUnverifiedTools(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	sess := gateSession("s1", "done")
	recordToolVerify(db, "s1", "sentiment_analyzer", false, "never tested")

	if !injectSkippedGapReportWarning(sess, db, "Done!") {
		t.Fatal("gate must fire")
	}
	note := sess.Messages[len(sess.Messages)-1].Content
	if !strings.Contains(note, "sentiment_analyzer") || !strings.Contains(note, "never tested") {
		t.Fatalf("note must name the unverified tool and why; got %q", note)
	}
}

// TestGapGateQuietOnLegitimateReplies is the important half. Warning on turns
// that legitimately end without a gap report — the plan-approval turn, and any
// mid-execution turn — would train the model to ignore the notice entirely.
func TestGapGateQuietOnLegitimateReplies(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}

	cases := []struct {
		name  string
		sess  *ChatSession
		reply string
	}{
		// The plan-approval turn: plan presented, nothing executed, and the
		// reply is the approval question itself.
		{"awaiting approval", gateSession("s1", "pending", "pending"), "Approve this plan?"},
		// Mid-execution: some steps done, more to go.
		{"still executing", gateSession("s1", "done", "pending"), "Created the sub-agent, continuing."},
		// Tool-only round with no prose — nothing was claimed.
		{"empty reply", gateSession("s1", "done"), "   "},
		// No plan at all — an ordinary agent turn.
		{"no build plan", &ChatSession{ID: "s1"}, "Here you go."},
		// No steps — a plan shell with nothing in it.
		{"empty plan", &ChatSession{ID: "s1", BuildPlan: &BuildPlanState{}}, "Here you go."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if injectSkippedGapReportWarning(tc.sess, db, tc.reply) {
				t.Fatal("gate fired on a legitimate reply — false positives here teach the model to ignore the notice")
			}
		})
	}
}

// TestGapGateQuietOnceReported — the gate must be satisfiable: having made the
// call clears it, and a blocked step is a valid closed-out state.
func TestGapGateQuietOnceReported(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}

	reported := gateSession("s1", "done", "done")
	reported.BuildPlan.GapsReported = true
	if injectSkippedGapReportWarning(reported, db, "All done.") {
		t.Fatal("gate fired even though report_build_gaps was called — it must be satisfiable")
	}

	// "blocked" is a legitimately closed-out step, so the plan still counts as
	// finished and the gate should apply.
	blocked := gateSession("s2", "done", "blocked")
	if !injectSkippedGapReportWarning(blocked, db, "Finished what I could.") {
		t.Fatal("a blocked step still closes the plan — the gap check is exactly what should surface it")
	}
}
