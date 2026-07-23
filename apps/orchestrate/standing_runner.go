// Standing-agent runner closure. core (core/standing_agent.go) owns the
// schedule, the run-ledger, and escalation; this is the only agent-aware
// part — it loads the standing agent's target and runs its mission, then
// hands the outcome back to core to record.
//
// Blast-radius posture (the reason this isn't core/RunAgentSync verbatim):
// an autonomous run has no human to approve a high-consequence (NeedsConfirm)
// tool, so it runs through the shared autonomousGate (autonomous_approval.go):
// a tool the owner pre-authorized (AutoApproveTools) runs; any other is refused
// for this fire and QUEUED as an "autonomous_tool" authorization the owner can
// approve to pre-authorize it for future runs. Same policy the recurring
// scheduled-update path now uses — the two used to disagree (deny-all vs
// approve-all).

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

		// Unattended run: a NeedsConfirm tool runs only if the owner pre-authorized
		// it (AutoApproveTools); otherwise it's refused and queued for approval.
		gate := app.newAutonomousGate(sa.Owner, sa.AgentID)

		// Dispatch REQUIRES a non-empty message; a standing agent created without a
		// mission (or one stored before create-time defaulting) would otherwise fail
		// EVERY fire with "message is required". Default it at fire time so existing
		// mission-less schedules run — the agent's orchestrator prompt drives what it
		// actually does each run.
		mission := strings.TrimSpace(sa.Mission)
		if mission == "" {
			mission = "Run your standing task now."
		}
		// Standing agents run as their owner (no separate runtime user).
		// sa.DispatchedBy is set only on a DELEGATION (a transient record built
		// by RunDelegation) — it hands the delegate the delegator's channel
		// reach. A stored schedule leaves it empty and keeps its own scope.
		out, hitRoundCap, err := app.runAgentSyncConfirm(ctx, sa.Owner, sa.Owner, sa.AgentID, mission, gate.confirm, sa.DispatchedBy...)
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
		if blockedTool := gate.blocked(); blockedTool != "" {
			res.Status = RunAttention
			res.Summary = "Needed approval to run \"" + blockedTool +
				"\"; queued in the Authorizations pane. Approve it to pre-authorize the tool for future runs. " + res.Summary
		} else if hitRoundCap {
			// The run stopped mid-task because it exhausted its worker rounds — flag
			// it (not a silent "ok") and drop a breadcrumb in the report session's
			// issues trail so it shows up in the ⚠ affordance, not just the run log.
			res.Status = RunAttention
			res.Summary = "Ran out of worker rounds before finishing — raise this agent's round limit or narrow its task. " + res.Summary
			reportAgent := strings.TrimSpace(sa.ReportAgentID)
			if reportAgent == "" {
				reportAgent = sa.AgentID
			}
			reportSession := strings.TrimSpace(sa.ReportSessionID)
			if reportSession == "" {
				reportSession = cortexSessionID(reportAgent)
			}
			appendSessionDiag(UserDB(app.DB, sa.Owner), reportAgent, reportSession, "round-cap",
				"Scheduled run \""+sa.Name+"\" hit its worker-round limit before finishing — the last action may not have run.")
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
			reportSession = cortexSessionID(reportAgent)
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
			ReportKind: cortexKindScheduled,
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
