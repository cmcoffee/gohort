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
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// registerStandingRunner installs the agent-execution closure for core's
// standing-agent scheduler and ensures the scheduler handler is live.
func registerStandingRunner(app *OrchestrateApp) {
	RegisterStandingRunner(func(ctx context.Context, sa StandingAgent) StandingRunResult {
		// Never run a sub-agent that's still held for approval. A schedule (or a
		// delegation) can be set up against an agent before its owner activates
		// it — running it anyway would defeat the approval gate. Report an
		// attention entry naming why it didn't execute. Covers standing fires AND
		// delegations, since both flow through the registered runner.
		if rec, ok := loadAgent(UserDB(app.DB, sa.Owner), sa.AgentID); ok && rec.PendingApproval {
			return StandingRunResult{
				Status:  RunAttention,
				Summary: "Skipped: agent \"" + rec.Name + "\" is still awaiting approval — activate it in the Authorizations pane and it will run on its next schedule.",
			}
		}

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

	// Reporter: after each run, post the result back into the channel/session
	// the standing agent was created from, so it lands where the user is
	// watching and lights the unread dot (saveChatSession bumps LastAt, which
	// the session list reads as unread). Mirrors the event-monitor notify=direct
	// delivery. Best-effort: a missing session is recreated; failures log only.
	RegisterStandingReporter(func(ctx context.Context, sa StandingAgent, rec RunRecord) {
		reportAgent := strings.TrimSpace(sa.ReportAgentID)
		if reportAgent == "" {
			reportAgent = sa.AgentID // legacy records: fall back to the target agent's channel
		}
		reportSession := strings.TrimSpace(sa.ReportSessionID)
		if reportSession == "" {
			reportSession = channelSessionID(reportAgent)
		}
		udb := UserDB(app.DB, sa.Owner)
		if udb == nil {
			return
		}
		body := strings.TrimSpace(rec.Raw)
		if body == "" {
			body = strings.TrimSpace(rec.Summary)
		}
		if body == "" {
			return // nothing to report
		}
		sess, ok := loadChatSession(udb, reportAgent, reportSession)
		if !ok {
			sess = ChatSession{ID: reportSession, AgentID: reportAgent}
		}
		sess.Messages = append(sess.Messages, ChatMessage{
			Role:       "assistant",
			Content:    body,
			Created:    time.Now(),
			ReportFrom: sa.Name,
		})
		if _, err := saveChatSession(udb, sess); err != nil {
			Log("[standing] report append failed for %s/%s: %v", sa.Owner, sa.Name, err)
		}
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
