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
	"fmt"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/pacing"
	"strconv"
)

// registerStandingRunner installs the agent-execution closure for core's
// standing-agent scheduler and ensures the scheduler handler is live.
func registerStandingRunner(app *OrchestrateApp) {
	RegisterStandingRunner(func(ctx context.Context, sa StandingAgent) StandingRunResult {
		// A pipeline target runs the pipeline, not an agent. Branch first:
		// everything below this reads sa.AgentID, and for a pipeline
		// schedule that is empty.
		if sa.TargetsPipeline() {
			return runStandingPipeline(ctx, app, sa)
		}
		if sa.TargetsMachine() {
			return runStandingMachine(ctx, app, sa)
		}
		// Never run a sub-agent that's still held for approval. A schedule (or a
		// delegation) can be set up against an agent before its owner activates
		// it — running it anyway would defeat the approval gate. Report an
		// attention entry naming why it didn't execute. Covers standing fires AND
		// delegations, since both flow through the registered runner.
		//
		// Resolve by name OR id, the same way the dispatcher itself does.
		// RunDelegation fills AgentID with whatever string the caller typed, so a
		// delegation addressed by display name ("Research Assistant") missed an
		// id-only lookup entirely: ok came back false and the approval hold was
		// skipped without a word. A gate that holds or not depending on how the
		// target was spelled is not a gate.
		if rec, ok := findAgentByNameOrID(UserDB(app.DB, sa.Owner), sa.Owner, sa.AgentID); ok && rec.PendingApproval {
			return StandingRunResult{
				Status:  RunAttention,
				Summary: "Skipped: agent \"" + rec.Name + "\" is still awaiting approval: activate it in the Authorizations pane and it will run on its next schedule.",
			}
		}

		// Unattended run: a NeedsConfirm tool runs only if the owner pre-authorized
		// it (AutoApproveTools); otherwise it's refused and queued for approval.
		gate := app.newAutonomousGate(sa.Owner, sa.AgentID, nil)

		// Dispatch REQUIRES a non-empty message; a standing agent created without a
		// mission (or one stored before create-time defaulting) would otherwise fail
		// EVERY fire with "message is required". Default it at fire time so existing
		// mission-less schedules run — the agent's orchestrator prompt drives what it
		// actually does each run.
		mission := strings.TrimSpace(sa.Mission)
		if mission == "" {
			mission = "Run your standing task now."
		}
		// What earlier attempts tried, for an objective on its second or later
		// fire. Appended to the mission because that IS this run's prompt.
		if block := objectiveAttemptsBlock(standingObjective(sa)); block != "" {
			mission += "\n\n" + block
		}
		// What earlier fires of this schedule left for this one, appended to
		// the mission for the same reason the attempts block is: the mission IS
		// this run's prompt. Always rendered, including the empty state, so a
		// first fire is told the register exists rather than having to notice a
		// tool in its catalog. See task_notes.go.
		tnotes := standingTaskNotes(sa)
		if block := tnotes.block(); block != "" {
			mission += "\n\n" + block
		}
		// Live-activity registration: a standing fire has no HTTP client, so
		// without this it was invisible while running — only its completed
		// RunRecord ever surfaced. Name prefers the schedule's own label
		// (sa.Name) over the target agent id.
		display := strings.TrimSpace(sa.Name)
		if display == "" {
			if rec, ok := loadAgent(UserDB(app.DB, sa.Owner), sa.AgentID); ok {
				display = rec.Name
			}
		}
		// Cancellable: the returned ctx is what the fire runs under, so Stop on
		// the live pill and Cancel on the Monitor row reach THIS work. Both
		// offered the button before and neither could do anything.
		ctx, liveRun := app.runsRegistry().CreateCancellable(ctx, sa.Owner, sa.AgentID, "")
		liveRun.Describe("standing", display, truncateObs(mission, 100))
		defer liveRun.Complete(RunStatusFailed) // safety net; explicit calls below win (idempotent)

		// Standing agents run as their owner (no separate runtime user).
		// sa.DispatchedBy is set only on a DELEGATION (a transient record built
		// by RunDelegation) — it hands the delegate the delegator's channel
		// reach. A stored schedule leaves it empty and keeps its own scope.
		// Collect what the fire's prompt was made of. A standing agent runs at
		// 5am with the tab closed, so its record is the only place this can be
		// read afterwards.
		ctx, promptDigest := WithPromptDigest(ctx)
		// The one lever this fire has over its own schedule: an objective that
		// cannot finish because it is WAITING may say when to try again
		// (docs/objective-pacing.md). Run-scoped, so it is passed to the run
		// rather than hung on the agent, which has no next fire to move when it
		// is dispatched from a conversation.
		pacingAsk := &pacing.Ask{}
		// The task's notes ride on the context so the prompt assembler keeps the
		// AGENT's own Working-notes block out of a fire that already carries a
		// task's, and the tool is mounted beside the pacing lever: both are
		// things only this turn can use.
		ctx = withTaskNotes(ctx, tnotes)
		run, err := app.runAgentSyncAppTools(ctx, sa.Owner, sa.Owner, sa.AgentID, mission, gate.confirm,
			append(standingPacingTool(sa, pacingAsk), tnotes.tool()...), sa.DispatchedBy...)
		out, hitRoundCap, toolTrace := run.Text, run.HitRoundCap, run.Trace
		if err != nil {
			liveRun.Complete(RunStatusFailed)
		} else {
			liveRun.Complete(RunStatusCompleted)
		}
		// The trace goes in on BOTH paths. A failed fire is the one whose steps
		// are most worth having — what it managed to do before it broke is the
		// question you open a failed run to answer.
		steps := runStepsFromToolCalls(toolTrace)
		if err != nil {
			// A schedule that cannot work should not keep trying at full speed.
			// Re-read first: this run may have taken minutes, and a pause or an
			// edit during it is the owner's word (failure_backoff.go).
			backoff := ""
			if cur, ok := GetStandingAgent(RootDB, sa.Owner, sa.Name); ok {
				backoff = noteStandingFailure(&cur)
				SaveStandingAgent(RootDB, cur)
			}
			return StandingRunResult{
				Status:  RunFailed,
				Summary: "Run failed: " + err.Error() + backoff,
				Err:     err.Error(),
				Steps:   steps,
				Prompt:  promptDigest(),
			}
		}
		// This run worked, so whatever was failing is not failing now. Written
		// only when there is something to clear, so a healthy schedule costs no
		// extra write per run.
		if sa.ConsecutiveFailures > 0 {
			if cur, ok := GetStandingAgent(RootDB, sa.Owner, sa.Name); ok && cur.ConsecutiveFailures > 0 {
				cur.ConsecutiveFailures = 0
				SaveStandingAgent(RootDB, cur)
			}
		}

		res := StandingRunResult{
			Status:  RunOK,
			Summary: standingSummary(out),
			Raw:     out,
			Steps:   steps,
			Prompt:  promptDigest(),
		}
		if blockedTool := gate.blocked(); blockedTool != "" {
			res.Status = RunAttention
			// Two refusals, two sentences. Telling an owner to go approve
			// something they deliberately marked is advice they cannot follow,
			// and the run would read as blocked on them when it is not.
			if withheld := gate.withheldTool(); withheld != "" && withheld == blockedTool {
				res.Summary = "Did not run \"" + withheld +
					"\": it is marked never unattended, so it waits for a run you are watching. Nothing is queued. " + res.Summary
			} else {
				res.Summary = "Needed approval to run \"" + blockedTool +
					"\"; queued in the Authorizations pane. Approve it to pre-authorize the tool for future runs. " + res.Summary
			}
		} else if hitRoundCap {
			// The run stopped mid-task because it exhausted its worker rounds — flag
			// it (not a silent "ok") and drop a breadcrumb in the report session's
			// issues trail so it shows up in the ⚠ affordance, not just the run log.
			res.Status = RunAttention
			res.Summary = "Ran out of worker rounds before finishing: raise this agent's round limit or narrow its task. " + res.Summary
			reportAgent := strings.TrimSpace(sa.ReportAgentID)
			if reportAgent == "" {
				reportAgent = sa.AgentID
			}
			reportSession := strings.TrimSpace(sa.ReportSessionID)
			if reportSession == "" {
				reportSession = cortexSessionID(reportAgent)
			}
			appendSessionDiag(UserDB(app.DB, sa.Owner), reportAgent, reportSession, "round-cap",
				"Scheduled run \""+sa.Name+"\" hit its worker-round limit before finishing: the last action may not have run.")
		}
		// Something this run READ carried instructions aimed at the agent.
		//
		// Its own condition, not another arm of the chain above: the two before
		// it are alternatives (a gated tool explains why the work stopped, and
		// so does the round cap), while this is orthogonal to both — a run can
		// be gated AND have read an injection, and the chain would report only
		// the first. Applied after them so it reads FIRST, because a truncated
		// or gated run announces itself in the summary and this does not.
		//
		// The detection's own ⚠ breadcrumb lands on the turn's trail, and a
		// turn-free run's trail is the synthetic "external-dispatch:<user>:
		// <agent>" session nobody navigates to. So this mirrors the round-cap
		// path and writes to the REPORT session — where the owner already looks
		// for what their schedules did.
		if run.Detections > 0 {
			res.Status = RunAttention
			res.Summary = scanDetectionSummary(run.Detections, run.TaintBlocks) + " " + res.Summary
			reportAgent := strings.TrimSpace(sa.ReportAgentID)
			if reportAgent == "" {
				reportAgent = sa.AgentID
			}
			reportSession := strings.TrimSpace(sa.ReportSessionID)
			if reportSession == "" {
				reportSession = cortexSessionID(reportAgent)
			}
			appendSessionDiag(UserDB(app.DB, sa.Owner), reportAgent, reportSession, "tool-scan-detected",
				"Scheduled run \""+sa.Name+"\" read content carrying instructions aimed at this agent. "+
					"It was marked as hostile and kept, not dropped, so the agent could report it: read what the run did.")
			Log("[orchestrate/standing] agent=%s schedule=%q INJECTION DETECTED x%d (%d follow-up action(s) stopped): run flagged for attention",
				sa.AgentID, sa.Name, run.Detections, run.TaintBlocks)
		}
		// Objective check (docs/loop-objectives.md). A standing agent with an
		// `until` is asking for a state of the world, so the fire that reaches
		// it is the last one, and the fire that runs out of attempts stops and
		// says so. Judged from what this run RAN and reported, never from the
		// reply alone — see objective_judge.go.
		//
		// Only on the success path: a run that errored never produced an
		// attempt to judge, and the failed result already says why.
		//
		// Stopping needs no new plumbing. The scheduler's deferred re-arm
		// re-reads the record and skips a paused one, and MarkStandingAgentBroken
		// pauses and unschedules — so writing the record here is enough.
		if objective := strings.TrimSpace(sa.Until); objective != "" {
			// UnmetCount is attempts spent in the CURRENT allowance — Resume
			// zeroes it — so the attempt number comes from here rather than
			// from the settlement, which cannot know how this record counts.
			attempt := sa.UnmetCount + 1
			settled := app.settleObjective(ctx, objectiveFire{
				Objective: objective, Reply: out, Trace: toolTrace,
				Attempt: attempt, MaxAttempts: sa.MaxAttempts,
			})
			line, stop, stalled := settled.Line, settled.Stop, settled.Stalled
			reason := settled.Reason
			// Re-read: this run may have taken minutes, and a pause or an edit
			// during it is the owner's word, not ours to overwrite.
			cur, ok := GetStandingAgent(RootDB, sa.Owner, sa.Name)
			if !ok {
				cur = sa
			}
			cur.Attempts = appendObjectiveAttempt(cur.Attempts, settled.Met, reason)
			if !settled.Met {
				cur.UnmetCount = attempt
			}
			// Pacing, before the record is written: every arm of the switch
			// below saves `cur`, and the deferred re-arm reads what it saved.
			// Only while the schedule continues — a met objective pauses and a
			// stalled one is marked broken, and an attempt does not get to
			// defer its way out of a bound it has already reached.
			pacedLine := ""
			if !stop {
				pacedLine = applyStandingPacing(&cur, pacingAsk)
			} else if _, _, asked := pacingAsk.Get(); asked {
				Log("[orchestrate/pacing] standing %s/%s asked to move its next attempt, but the objective %s: the ask was dropped", sa.Owner, sa.Name, line)
			}
			switch {
			case stalled:
				SaveStandingAgent(RootDB, cur)
				MarkStandingAgentStalled(RootDB, sa.Owner, sa.Name,
					fmt.Sprintf("objective not met after %d attempt(s): %s", attempt, reason))
				Log("[orchestrate/objective] standing %s/%s stalled after attempt %d: %s", sa.Owner, sa.Name, attempt, reason)
			case stop:
				// Met. Paused rather than deleted: the schedule stays visible,
				// says why it stopped, and can be started again.
				//
				// Deliberately unlike the recurring path, which acts only on a
				// scheduled fire and leaves a manual Run now alone: the runner
				// closure is not told the trigger, and a goal that is met is met
				// however the fire that met it was started.
				cur.Paused = true
				// Say WHY. Setting Paused alone made the one outcome an
				// objective exists to reach look exactly like a schedule
				// somebody paused by hand.
				cur.StopCause = StoppedByMet
				cur.StopNote = reason
				SaveStandingAgent(RootDB, cur)
				Log("[orchestrate/objective] standing %s/%s met its objective on attempt %d: %s", sa.Owner, sa.Name, attempt, reason)
			default:
				SaveStandingAgent(RootDB, cur)
			}
			// The verdict leads the summary, and the pacing follows it: a fire
			// that moved its own next run has to account for it where the owner
			// is already reading, the same way the recurring card carries it.
			lead := sentence(line)
			if pacedLine != "" {
				lead += sentence(pacedLine)
			}
			res.Summary = lead + res.Summary
			if stalled {
				res.Status = RunAttention
			}
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
		// Surface routes the report: session (home) / cortex / background. The run
		// still happened; background just doesn't post the result to any thread.
		reportSession, record := resolveSurface(sa.Surface, sa.ReportSessionID, reportAgent)
		if !record {
			return
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
		// Through the per-session append lock, re-reading the stored thread
		// first: this report goes to a thread that scheduled fires, channel
		// mirrors and other standing agents also write, and a bare
		// load-append-save loses whichever card was written while this one was
		// being composed. Observed on the scheduled path, where the daily blog
		// post card vanished whenever the engagement fire overlapped it.
		if err := appendToStoredSession(udb, reportAgent, reportSession,
			ChatSession{ID: reportSession, AgentID: reportAgent},
			ChatMessage{
				Role:       "assistant",
				Content:    body,
				Created:    time.Now(),
				ReportFrom: sa.Name,
				ReportKind: cortexKindScheduled,
				// What it actually DID, not just what it concluded. The card is
				// the only account of a run nobody watched, and the recurring
				// fire's card has carried its trace all along — two surfaces
				// showing the same kind of run, one of them silent about six
				// tool calls, is the asymmetry rather than a policy.
				ToolCalls: persistedToolCallsFromSteps(rec.Steps),
			}); err != nil {
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

// runStandingPipeline fires a schedule whose target is a pipeline.
//
// Deliberately the SAME execution the page's Run button uses
// (runPipelineHTTP): RunPipelineDefSync as the owner, agent stages
// dispatching through RunAgentSync, and no inherited tool catalog — so a
// worker stage reaches exactly what it declares and nothing otherwise.
// That posture is not a new decision made for schedules; it is the one
// the owner already exercises by hand, which is the point. A scheduled
// run that could reach more than the same pipeline reached when clicked
// would be a surprise nobody asked for.
func runStandingPipeline(ctx context.Context, app *OrchestrateApp, sa StandingAgent) StandingRunResult {
	// Own, or one shared with this user (pipeline_sharing.go). A schedule fires
	// as its owner either way: a shared pipeline's stages dispatch to THIS
	// user's agents and resolve THEIR tools, exactly as a hand-run does.
	def, ok := pipelineForUser(sa.Owner, sa.PipelineID)
	if !ok {
		// Attention rather than failure: the schedule is fine, its target is
		// gone. Which KIND of gone decides what somebody does about it —
		// repoint the schedule, or go ask the person who withdrew the share.
		return StandingRunResult{
			Status: RunAttention,
			Summary: "Skipped: " + pipelineMissingReason(sa.Owner, sa.PipelineID) + ". " +
				"Point this schedule at a pipeline you can reach, or delete it.",
		}
	}
	// A pipeline REFUSES to be stored unrunnable, but a record written by
	// an older build (or restored from a bundle) can still be invalid.
	// Better to say so than to fire it and report whatever the first
	// stage made of a broken reference.
	if err := def.Validate(); err != nil {
		return StandingRunResult{
			Status:  RunAttention,
			Summary: "Skipped: pipeline " + strconv.Quote(def.Name) + " would not run: " + err.Error(),
		}
	}
	input := strings.TrimSpace(sa.Mission)
	if input == "" {
		// A pipeline's input is its subject, not an instruction to begin,
		// so the agent path's "Run your standing task now." would be a
		// worse default here: it becomes {input} in the first stage's
		// prompt. The pipeline's own name is at least about the work.
		input = def.Name
	}

	display := strings.TrimSpace(sa.Name)
	if display == "" {
		display = def.Name
	}
	// Live-activity registration, same as the agent path: a fire has no
	// HTTP client, so without this it is invisible while running and only
	// its finished RunRecord ever surfaces.
	ctx, liveRun := app.runsRegistry().CreateCancellable(ctx, sa.Owner, "", "")
	liveRun.Describe("standing", display, truncateObs(input, 100))
	defer liveRun.Complete(RunStatusFailed) // safety net; the explicit calls below win

	// A pipeline makes no tool calls of its own — its agent stages do, and
	// those are the calls somebody opening this run wants to see. Collect each
	// stage's trace as it returns. Guarded because a fanout stage runs its
	// branches concurrently, so these arrive from several goroutines at once.
	var (
		traceMu    sync.Mutex
		stageTrace []PersistedToolCall
	)
	dispatch := func(c context.Context, agentID, stageInput string) (string, error) {
		// Same auto-approving confirm RunAgentSync uses — this is the identical
		// dispatch, just one that keeps the trace instead of discarding it.
		stage, err := app.runAgentSyncConfirm(c, sa.Owner, sa.Owner, agentID, stageInput,
			func(string, string) bool { return true })
		if len(stage.Trace) > 0 {
			traceMu.Lock()
			stageTrace = append(stageTrace, stage.Trace...)
			traceMu.Unlock()
		}
		return stage.Text, err
	}
	out, _, err := app.RunPipelineDefHooks(ctx, def, input, PipelineHooks{
		Dispatch: dispatch,
		Machine:  app.pipelineMachineRunner(sa.Owner),
	})
	traceMu.Lock()
	steps := runStepsFromToolCalls(stageTrace)
	traceMu.Unlock()
	if err != nil {
		liveRun.Complete(RunStatusFailed)
		return StandingRunResult{
			Status:  RunFailed,
			Summary: "Pipeline " + strconv.Quote(def.Name) + " failed: " + err.Error(),
			Steps:   steps,
		}
	}
	liveRun.Complete(RunStatusCompleted)
	Log("[orchestrate.standing] user=%q fired pipeline %q (%d stages, %d bytes out, %d tool calls)",
		sa.Owner, def.Name, len(def.Stages), len(out), len(steps))
	return StandingRunResult{Status: RunOK, Summary: strings.TrimSpace(out), Steps: steps}
}

// runStandingMachine fires a schedule whose target is a machine.
//
// The third target, and the one the other two could not express: a
// pipeline is dataflow with no memory between stages, an agent decides
// its approach afresh each turn, and a machine carries a working set from
// step to step. "Gather what changed, keep what is new, report on it" is
// a machine, and a schedule is exactly where such a thing belongs.
//
// Same execution the page's Run button uses (handleMachineRun): the
// owner's tool pool narrowed per step, RunUnattended to the step that
// hands off nowhere, that step's result as the outcome. A scheduled run
// that reached further than the same machine reached when clicked would
// be a surprise nobody asked for.
func runStandingMachine(ctx context.Context, app *OrchestrateApp, sa StandingAgent) StandingRunResult {
	// Their own, else one somebody shared with them (machine_sharing.go). A
	// schedule can fire a shared machine for the reason it can fire a shared
	// pipeline: somebody sat down and armed it, against a definition they could
	// read, and it runs in THEIR namespace against THEIR tools. What the
	// widening needs is that a schedule cannot outlive its permission, which is
	// what the revoke and delete paths handle.
	def, ok := machineForUser(sa.Owner, sa.MachineID)
	if !ok {
		// Attention rather than failure: the schedule is fine, its target
		// is gone. Somebody repoints it or deletes it; nothing here can
		// decide which.
		return StandingRunResult{
			Status: RunAttention,
			Summary: "Skipped: " + machineMissingReason(sa.Owner, sa.MachineID) +
				". Point this schedule at a machine you can reach, or delete it.",
		}
	}
	if !def.Unattended {
		// The refusal that earns this target its own runner. A machine
		// with a step that waits for a person, fired at four in the
		// morning, would enter a step the run could never leave.
		return StandingRunResult{
			Status: RunAttention,
			Summary: "Skipped: machine " + strconv.Quote(def.Name) + " converses rather than runs: it has a step that waits for a person, " +
				"and a schedule fires with nobody there. Turn on \"this RUNS instead of converses\" on that machine, or point this schedule elsewhere.",
		}
	}
	// A machine can be SAVED with problems (the editor's whole posture is
	// that a half-built machine is the normal state), so unlike a pipeline
	// this check is not defensive — it is the common case for a schedule
	// armed against something still being built.
	if probs := def.Problems(); len(probs) > 0 {
		return StandingRunResult{
			Status: RunAttention,
			Summary: "Skipped: machine " + strconv.Quote(def.Name) + " will not run yet: " + probs[0] +
				" (" + strconv.Itoa(len(probs)) + " outstanding). Its page lists them.",
		}
	}

	input := strings.TrimSpace(sa.Mission)
	if input == "" {
		// The mission is the run's SUBJECT here, the same as a pipeline's
		// input: it lands in {input} / {original_input}. "Run your standing
		// task now" would be a worse default than the machine's own name.
		input = def.Name
	}
	display := strings.TrimSpace(sa.Name)
	if display == "" {
		display = def.Name
	}
	ctx, liveRun := app.runsRegistry().CreateCancellable(ctx, sa.Owner, "", "")
	liveRun.Describe("standing", display, truncateObs(input, 100))
	defer liveRun.Complete(RunStatusFailed) // safety net; the explicit calls below win

	// The owner's pool, narrowed per step by each step's own Tools list.
	sess := &ToolSession{Username: sa.Owner, DB: AuthDB()}
	catalog, err := GetAgentToolsWithSession(sess, availableWorkerToolNames()...)
	if err != nil {
		Log("[orchestrate.standing] machine %q: tool catalog partly unresolved for %q: %v", def.Name, sa.Owner, err)
	}
	cache := NewRunToolCache()
	catalog = WrapToolsWithRunCache(cache, catalog)

	cur := &MachineCursor{}
	// Everything the walk says about itself — each step's result, every
	// framework decision, a sub-run's progress — lands in the ledger as the
	// run's Steps, and the live row advances per step. Before this the notes
	// went into a slice nobody read (standing_machine_trace.go says why that
	// mattered).
	trace := newMachineRunTrace(liveRun)
	// The full host: a scheduled machine whose steps delegate, run a pipeline,
	// or run a child machine now does that at 3am the same way it does in a
	// conversation. It used to run those steps as an ordinary prompt and report
	// an answer that had done none of the arranged work.
	//
	// The run id is per FIRE, not per schedule: a delegate's thread hangs off it,
	// and one that persisted across fires would carry January's findings into
	// February's run.
	runner := trace.wrap(app.unattendedHost(unattendedRun{
		User:     sa.Owner,
		ID:       "standing:" + sa.Name + ":" + strconv.FormatInt(time.Now().UnixNano(), 36),
		Tools:    catalog,
		Cursor:   cur,
		Note:     trace.note,
		Activity: trace.activity,
	}).phaseRunner())
	final, out, err := app.RunUnattended(ctx, def, cur, MachineTurn{
		Input: input,
		User:  sa.Owner,
		Now:   time.Now().In(UserLocation(sa.Owner)).Format("Mon, January 2, 2006 at 3:04 PM MST"),
	}, runner, trace.note)
	if err != nil {
		liveRun.Complete(RunStatusFailed)
		// The partial result rides along: a run that stopped at step nine
		// did nine steps of work, and a summary that threw it away would
		// leave the ledger saying only that something went wrong.
		summary := "Machine " + strconv.Quote(def.Name) + " stopped: " + err.Error()
		if partial := strings.TrimSpace(out); partial != "" {
			summary += "\n\nWhat it had produced:\n" + partial
		}
		return StandingRunResult{Status: RunFailed, Summary: summary, Steps: trace.Steps()}
	}
	liveRun.Complete(RunStatusCompleted)
	Log("[orchestrate.standing] user=%q fired machine %q → finished at %s after %d step(s), %d bytes out, %d cached tool call(s), %d trace entries",
		sa.Owner, def.Name, final.Name, trace.phases(), len(out), cache.Hits(), len(trace.Steps()))
	return StandingRunResult{Status: RunOK, Summary: strings.TrimSpace(out), Steps: trace.Steps()}
}
