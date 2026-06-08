// Standing-agent runner closure. core (core/standing_agent.go) owns the
// schedule, the run-ledger, and escalation; this is the only agent-aware
// part — it loads the standing agent's target and runs its mission, then
// hands the outcome back to core to record.
//
// Blast-radius posture (the reason this isn't core/RunAgentSync verbatim):
// an autonomous run has no human to approve a high-consequence (NeedsConfirm)
// tool, so the run uses a DENY-by-default confirm. A refused tool flags the
// run "attention" and names what it wanted to do — the seam the approvals
// queue will later turn into a real pending-approval instead of a hard deny.

package orchestrate

import (
	"context"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// registerStandingRunner installs the agent-execution closure for core's
// standing-agent scheduler and ensures the scheduler handler is live.
func registerStandingRunner(app *OrchestrateApp) {
	RegisterStandingRunner(func(ctx context.Context, sa StandingAgent) StandingRunResult {
		var blockedTool string
		confirm := func(name, _ string) bool {
			if blockedTool == "" {
				blockedTool = name
			}
			return false // unattended run: never auto-approve
		}

		// Standing agents run as their owner (no separate runtime user).
		out, err := app.runAgentSyncConfirm(ctx, sa.Owner, sa.Owner, sa.AgentID, sa.Mission, confirm)
		if err != nil {
			return StandingRunResult{
				Status:  RunFailed,
				Summary: "Run failed: " + err.Error(),
				Err:     err.Error(),
			}
		}

		res := StandingRunResult{
			Status:  RunOK,
			Summary: standingSummary(out),
			Raw:     out,
		}
		if blockedTool != "" {
			res.Status = RunAttention
			res.Summary = "Needed approval to run \"" + blockedTool +
				"\"; skipped on this unattended run. " + res.Summary
		}
		return res
	})

	// Idempotent — the Operator app also starts the scheduler; first wins.
	StartStandingScheduler()
}

// standingSummary makes a short feed line from an agent's full output. The
// ledger keeps the full text as Raw (encrypted, fetched on demand); this is
// just what shows in the Activity feed.
func standingSummary(out string) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return "(no output)"
	}
	const max = 280
	if len(out) <= max {
		return out
	}
	return strings.TrimSpace(out[:max]) + "…"
}
