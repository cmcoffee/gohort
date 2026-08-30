// Scheduled orchestrate updates — the LLM can use schedule_recurring to
// set up a recurring task that posts back into the user's current
// session (e.g. "every 30 minutes, check the build status and post if
// it's red"). Same shape as apps/chat/scheduled_updates.go but
// scoped per-(user, agent) so each fire runs against the correct
// agent's persona / tools / memory.
//
// On fire:
//   1. PRE-ARM the next occurrence (persist it before running — the
//      scheduler dequeues before invoking us, and the loop below runs
//      minutes, so a process restart mid-fire must not end the chain).
//   2. Load the user's session under the per-(user, agent) sub-store.
//   3. Build messages from the session's history + a synthetic
//      "[SCHEDULED UPDATE — fire N]" user turn.
//   4. Run a worker-tier RunAgentLoop with the target agent's
//      orchestrator_prompt + memory + facts + allowed tools.
//   5. Append the model's reply as an assistant turn in the session,
//      renewing the armed occurrence's idle clock on productive work.
//
// Guardrails (matched to chat's):
//   - Min interval 60s
//   - Max 5 active updates per session
//   - MaxFires>0 = explicit total-fire bound; 0 = indefinite, watched
//     by the renewable idle guard (tune_orch_update_idle_days)

package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const OrchestrateScheduledUpdateKind = "orchestrate.scheduled_update"

// scheduledFireDirective is appended to every recurring fire's synthetic user
// turn. A scheduled fire runs with no human watching the instant it happens, so
// the conversational reflexes that are fine in live chat ("let me check…",
// "starting cycle N…", "shall I proceed?") are pure noise here: nobody is there
// to read the narration or answer the question, and on this path the loop ends
// the moment the model emits text without a tool call, so an intent-stub becomes
// the whole reply and posts as a bogus report card. This tells the model to skip
// the narration, actually do the work, and report only the concrete result — or
// stay silent when there is nothing to report. Prompt-only by design: we prevent
// the intent-stub at the source rather than deterministically dropping a
// no-tool-call reply after the fact, which would also eat a legitimate pure-text
// fire. If preambles still leak on the 27B, the drop is a small follow-up.
// Written without em-dashes to match house style.
const scheduledFireDirective = "[This is an autonomous scheduled fire. No human is watching right now to read narration, answer a question, or approve anything. Do NOT announce what you are about to do or narrate intent (\"let me check…\", \"starting cycle N…\", \"shall I proceed?\"). Call your tools, do the work, and reply with ONLY the concrete result of what you actually did this cycle. If there is nothing to act on or report, produce no output at all rather than a status line or preamble.]"

func init() {
	RegisterTunable(TunableSpec{Key: "tune_orch_update_min_interval", Category: "Timeouts", Label: "Scheduled update min interval", Help: "Minimum interval allowed for a recurring orchestrate update.", Kind: KindSeconds, Default: 60, Min: 10, Max: 3600})
	RegisterTunable(TunableSpec{Key: "tune_orch_update_max_per_session", Category: "Limits", Label: "Scheduled updates per session", Help: "Max active recurring updates a single session may hold.", Kind: KindInt, Default: 5, Min: 1, Max: 50})
	RegisterTunable(TunableSpec{Key: "tune_orch_update_idle_days", Category: "Limits", Label: "Recurring task idle-reap (days)", Help: "Auto-cancel a recurring task that has gone this many days without a productive fire (one that called tools) or a create/edit. A productive fire or an edit renews it; 0 disables the guard. Replaces the old total fire cap, so a task set to max_fires=0 runs indefinitely.", Kind: KindInt, Default: 90, Min: 7, Max: 365})
}

// orchUpdateMinInterval is the floor on a recurring update's interval.
func orchUpdateMinInterval() time.Duration { return TuneDuration("tune_orch_update_min_interval") }

// orchUpdateMaxPerSession caps active recurring updates per session.
func orchUpdateMaxPerSession() int { return TuneInt("tune_orch_update_max_per_session") }

// orchUpdateIdleDays is how many days a recurring task may go without a
// productive fire or an edit before the idle guard reaps it (0 = disabled).
func orchUpdateIdleDays() int { return TuneInt("tune_orch_update_idle_days") }

type orchUpdatePayload struct {
	SessionID string `json:"session_id"`
	AgentID   string `json:"agent_id"`
	Username  string `json:"username"`
	Prompt    string `json:"prompt"`
	Name      string `json:"name,omitempty"` // short task label; empty = derive from Prompt's first line

	// HistoryNote is what the thread KEEPS of this fire's prompt, when the
	// prompt itself shouldn't be kept. A background task's wake carries both a
	// fact (what came back, and the handle naming it) and an instruction for
	// this turn only; persisting the instruction leaves the model able to read
	// it again later and act on it a second time. Empty = keep nothing, which
	// is what an ordinary recurring fire wants.
	HistoryNote string `json:"history_note,omitempty"`

	// ChannelChatID / ChannelHandle are the conversation a BACKGROUND TASK was
	// started from, captured at detach time and carried to delivery.
	//
	// Recovering it afterwards from SessionID does not work in the case that
	// matters most: a whole-service channel binds with an empty Address (it
	// matches every thread on the service), and a cortex agent collapses all of
	// them into one home thread — so the id names the agent, not the person, and
	// there is nothing left to derive a recipient FROM. That configuration
	// silently delivered nothing, forever. Derivation stays as the fallback for
	// records written before this existed.
	ChannelChatID string `json:"channel_chat_id,omitempty"`
	ChannelHandle string `json:"channel_handle,omitempty"`

	IntervalSeconds int    `json:"interval_seconds"`
	FireCount       int    `json:"fire_count"`
	CreatedAt       string `json:"created_at"`

	// Surface — same mode as EventMonitor/StandingAgent, resolved at fire time
	// against SessionID (the home): "" / "session" appends the fire into the
	// creating session, "cortex" into the agent's cortex home thread, "background"
	// runs it but posts nothing to a thread. The recurring conversation continues
	// in whichever thread it surfaces to.
	Surface string `json:"surface,omitempty"`

	// Broken parks a recurring task whose target agent was deleted. Unlike a
	// monitor/standing agent (which have a stored record), a recurring task lives
	// ONLY as its scheduler entry — so "keep it, don't drop it" means re-arming a
	// dormant no-op tick with this flag set (parkRecurringBroken) instead of the
	// old silent stop. The fire handler skips running a broken task; the console
	// shows a "needs relink" row. BrokenReason records why.
	Broken       bool   `json:"broken,omitempty"`
	BrokenReason string `json:"broken_reason,omitempty"`
	// LastActive (RFC3339) is renewed on a productive fire (one that called
	// tools) and on create / edit-in-place. The idle guard reaps a task whose
	// LastActive — or CreatedAt, for legacy tasks that predate this field — is
	// older than tune_orch_update_idle_days. See reschedule().
	LastActive string `json:"last_active,omitempty"`

	// Pattern modifiers (empty Pattern == fixed, the original every-N-minutes
	// behavior). See recurring_pattern.go for the scheduling math.
	Pattern       string `json:"pattern,omitempty"`         // "" | "fixed" | "random"
	TimesPerDay   int    `json:"times_per_day,omitempty"`   // random: fires per active window
	MinGapSeconds int    `json:"min_gap_seconds,omitempty"` // random: minimum spacing between fires
	MaxGapSeconds int    `json:"max_gap_seconds,omitempty"` // random (continuous): maximum spacing
	HasWindow     bool   `json:"has_window,omitempty"`      // whether the daily window applies
	WindowFromMin int    `json:"window_from_min,omitempty"` // window start, minutes since local midnight
	WindowToMin   int    `json:"window_to_min,omitempty"`   // window end, minutes since local midnight
	MaxFires      int    `json:"max_fires,omitempty"`       // per-task total cap; 0 = indefinite (run until cancelled or idle-reaped)
	// RemainingToday holds the random pattern's still-pending fire times for the
	// current day (RFC3339), so the plan survives restarts and each fire just
	// pops the next. Empty for fixed, or when a fresh day needs planning.
	RemainingToday []string `json:"remaining_today,omitempty"`
}

// orchRef points at the running OrchestrateApp so scheduler callbacks
// (which fire async, off-request) can reach the LLM + app DB. Set
// once by Routes() at startup.
var (
	orchRef   *OrchestrateApp
	orchRefMu sync.Mutex
)

// registerOrchestrateScheduledUpdates wires the scheduler handler.
// Idempotent — safe to call multiple times.
func registerOrchestrateScheduledUpdates(o *OrchestrateApp) {
	orchRefMu.Lock()
	orchRef = o
	orchRefMu.Unlock()
	RegisterScheduleHandler(OrchestrateScheduledUpdateKind, handleOrchestrateScheduledUpdate)
	// Label recurring-update tasks in the admin scheduler view + logs with the
	// owning agent + interval + prompt snippet, instead of a bare kind + uuid.
	// Registered here (not in core) because resolving the agent id to a friendly
	// name needs the orchestrate agent store; core stays generic.
	RegisterTaskDescriber(OrchestrateScheduledUpdateKind, func(payload json.RawMessage) string {
		var p orchUpdatePayload
		if json.Unmarshal(payload, &p) != nil {
			return ""
		}
		agent := p.AgentID
		if a, ok := loadAgent(UserDB(o.DB, p.Username), p.AgentID); ok && strings.TrimSpace(a.Name) != "" {
			agent = a.Name
		}
		return fmt.Sprintf("%s — %s (agent: %s)", recurringDetail(p), recurringName(p), agent)
	})
}

// recurringTaskRow pairs a recurring update's scheduler task id (the cancel
// key, needed for delete URLs) with its decoded payload. RunAt carries the
// scheduler's next-fire time (RFC3339 UTC) so status surfaces can show when the
// task fires next without re-deriving it.
type recurringTaskRow struct {
	TaskID  string
	RunAt   string
	Payload orchUpdatePayload
}

// listAgentRecurringTasks returns the recurring orchestrate updates owned by
// user that run as agentID (empty agentID = all of the user's). It filters the
// GLOBAL scheduler bucket by payload — unlike event monitors / standing agents,
// these tasks carry no <owner>:<name> storage key, so the Username filter is
// what prevents cross-user leakage and MUST NOT be dropped.
func listAgentRecurringTasks(user, agentID string) []recurringTaskRow {
	var out []recurringTaskRow
	for _, task := range ListScheduledTasks(OrchestrateScheduledUpdateKind) {
		var p orchUpdatePayload
		if json.Unmarshal(task.Payload, &p) != nil {
			continue
		}
		if p.Username != user {
			continue
		}
		if agentID != "" && p.AgentID != agentID {
			continue
		}
		out = append(out, recurringTaskRow{TaskID: task.ID, RunAt: task.RunAt, Payload: p})
	}
	return out
}

// firstLineLabel condenses a recurring task's prompt to a single short line for
// schedule rows / admin labels (first line, trimmed, rune-safe cap).
func firstLineLabel(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if r := []rune(s); len(r) > 60 {
		s = string(r[:59]) + "…"
	}
	return s
}

// recurringName is the stable label for a recurring task: its explicit Name when
// set, else the first line of its prompt. Same fallback everywhere a task is
// labelled — the admin describer, the Schedules rail, and the report card — so a
// given task reads the same in all three. Unlike the scheduler task id (which
// reschedule mints fresh each fire), this is stable across the task's lifetime.
func recurringName(p orchUpdatePayload) string {
	if n := strings.TrimSpace(p.Name); n != "" {
		return n
	}
	return firstLineLabel(p.Prompt)
}

// handleOrchestrateScheduledUpdate is the scheduler callback. Loads
// the session, runs the agent loop, appends the reply, reschedules.
// errSchedNotReady marks a fire that couldn't run because the app (or the
// user's store) wasn't wired yet — the boot race: a task that came due while
// the process was down dequeues at startup before orchestrate initializes.
// The handler re-arms a short retry instead of dropping the chain; with a
// high-frequency task this race is near-certain after any downtime, and the
// old "dropping task" path was a primary way recurring schedules evaporated.
var errSchedNotReady = errors.New("orchestrate not ready")

func handleOrchestrateScheduledUpdate(ctx context.Context, raw json.RawMessage) {
	var p orchUpdatePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		Log("[orchestrate/scheduled] payload unmarshal failed: %v", err)
		return
	}
	if err := fireOrchestrateUpdate(ctx, p, true); err != nil {
		if errors.Is(err, errSchedNotReady) {
			// Same payload, no fire counted — the retry IS this occurrence.
			if _, aerr := ScheduleTask(OrchestrateScheduledUpdateKind, p, time.Now().Add(2*time.Minute)); aerr != nil {
				Log("[orchestrate/scheduled] %v — retry re-arm FAILED for session %s: %v", err, p.SessionID, aerr)
			} else {
				Log("[orchestrate/scheduled] %v — retrying in 2m (session %s)", err, p.SessionID)
			}
			return
		}
		Log("[orchestrate/scheduled] %v", err)
	}
}

// fireOrchestrateUpdate runs one recurring fire: load the session, assemble the
// full agent toolkit, run the loop, append the reply to the thread, and record
// the run in the ledger. When reArm is true (the scheduler-driven chain) it
// schedules the next tick on every normal exit path; when false (the console
// "Run now" test) it fires exactly once and leaves the schedule untouched — no
// reschedule, no FireCount increment, and the per-task fire cap is ignored so a
// test always runs. Returns a descriptive error only when the fire can't happen
// at all (app not ready, or the agent/session has been deleted); the scheduled
// caller logs it, the manual caller surfaces it.
func fireOrchestrateUpdate(ctx context.Context, p orchUpdatePayload, reArm bool) error {
	orchRefMu.Lock()
	app := orchRef
	orchRefMu.Unlock()
	if app == nil {
		return fmt.Errorf("%w for session %s", errSchedNotReady, p.SessionID)
	}
	// A parked (broken) task stays LISTED but never runs — re-arm its dormant tick
	// and return. Resume/relink clears Broken and puts it back on its real cadence.
	if p.Broken {
		if reArm {
			parkRecurringBroken(p, "")
		}
		return nil
	}
	if reArm && p.FireCount >= p.effectiveMaxFires() {
		Log("[orchestrate/scheduled] task %s reached %d fires, auto-cancelling", p.SessionID, p.effectiveMaxFires())
		return nil
	}

	udb := UserDB(app.DB, p.Username)
	if udb == nil {
		return fmt.Errorf("%w: no udb yet for user %s (session %s)", errSchedNotReady, p.Username, p.SessionID)
	}
	agent, ok := loadAgent(udb, p.AgentID)
	if !ok {
		// Don't silently drop the chain — park it as broken so the owner sees a
		// "needs relink" task instead of a vanished one, and can relink or delete
		// it deliberately.
		Log("[orchestrate/scheduled] agent %s missing for user %s — parking task as broken", p.AgentID, p.Username)
		if reArm {
			parkRecurringBroken(p, fmt.Sprintf("its agent was deleted (id %s)", p.AgentID))
		}
		return nil
	}

	// PRE-ARM the next occurrence BEFORE running the fire. The scheduler
	// removed this task from the persistent queue before invoking us, and the
	// fire below runs a full agent loop — minutes on a local model. The old
	// order re-armed only after the fire returned, so a process restart (a
	// deploy, a crash) landing anywhere in that window silently ended the
	// chain with no trace: the #1 cause of "my recurring task evaporated".
	// From here on, the chain survives anything that kills this fire; the
	// productive path updates the armed payload's idle clock at the end, and
	// the panic guard below only falls back to reschedule() when the pre-arm
	// itself hadn't happened yet.
	armedID := ""
	retireReason := ""
	var armed orchUpdatePayload
	if reArm {
		var ok bool
		armedID, armed, ok, retireReason = preArmNextFire(p)
		if !ok {
			// Visible where the user reads the task, not just in the log.
			appendSessionDiag(udb, p.AgentID, p.SessionID, "recurring-retired",
				fmt.Sprintf("Recurring task %q is retiring after this fire: %s. It will not run again — recreate it if you still want it.", recurringName(p), retireReason))
		}
		defer func() {
			if r := recover(); r != nil {
				if armedID != "" {
					Log("[orchestrate/scheduled] fire panicked for session %s: %v (next fire already armed)", p.SessionID, r)
					return
				}
				Log("[orchestrate/scheduled] fire panicked for session %s: %v — rescheduling", p.SessionID, r)
				reschedule(p)
			}
		}()
	}
	// Surface routes the recurring conversation: "cortex" moves it (context +
	// reply) to the agent's cortex home thread; "background" runs it but posts no
	// reply card; "" / "session" keeps it in the creating session. The home
	// (p.SessionID) is left intact so a switch back always works.
	loadSession := p.SessionID
	if strings.TrimSpace(p.Surface) == "cortex" {
		loadSession = cortexSessionID(p.AgentID)
	}
	recordFire := strings.TrimSpace(p.Surface) != "background"
	sess, ok := loadChatSession(udb, p.AgentID, loadSession)
	if !ok {
		// The target thread doesn't exist yet — synthesize it rather than
		// dropping the task. A channel agent's Cortex home thread
		// (channel:<agentID>) is created lazily and won't exist until its first
		// turn, and a recurring task can be scheduled against a session before
		// any human posts to it; in both cases a missing session is expected,
		// not a failure. Start a fresh thread with the scheduled id (parity with
		// the chat GET path, which returns an empty session on a miss); the
		// fire's reply is what materializes it via saveChatSession below.
		sess = ChatSession{ID: loadSession, AgentID: p.AgentID}
	}

	// Bound the history this fire runs on, with the same rolling-summary
	// compaction the Cortex thread and every dispatch already use: a running
	// summary plus a verbatim recent tail, sized by the agent's context depth.
	// No-op until the thread grows past the fold trigger, so a young task is
	// unaffected.
	//
	// It used to keep the last 30 MESSAGES, described as "the same 30-turn
	// cutoff chat uses" — which chat had already stopped doing, and which is a
	// count standing in for a size. That proxy holds while messages are short
	// human turns and collapses when they are not: an agent whose every reply
	// is a batch of finished posts writes ~17KB a turn, so thirty of them is
	// half a megabyte before the fire has done anything. Measured at 523749
	// chars on a live fire, whose model then narrated three posts in prose and
	// called no tool, because a prompt that size loses its system prompt to
	// context-shift.
	//
	// The 30 is kept as a BACKSTOP under the fold, for the case compaction
	// declines to run at all (no worker LLM configured): fewer messages is
	// still better than every message.
	history := app.compactOperatorHistory(udb, p.Username, agent, loadSession, sess.Messages)
	if len(history) > 30 {
		history = history[len(history)-30:]
	}
	msgs := make([]Message, 0, len(history)+1)
	for _, m := range history {
		msgs = append(msgs, Message{Role: m.Role, Content: m.Content})
	}
	// Explicit time context with the local↔UTC pairing. Engagement-style
	// tasks count "posts today" against API timestamps that are usually
	// UTC; without the conversion spelled out, the model counts UTC-dated
	// items against the local day (a 06:31Z post is YESTERDAY evening
	// locally) and talks itself into wrong per-day totals.
	nowLocal := time.Now().In(UserLocation(p.Username))
	timeCtx := fmt.Sprintf(
		"[Time context: it is now %s (= %s). Timestamps from tools/APIs are usually UTC — convert each to the LOCAL zone before deciding what happened \"today\"; the day boundary is LOCAL midnight. When counting per-day items, list each id with its local date ONCE, then count that list — do not re-count.]",
		nowLocal.Format("Mon 2006-01-02 15:04 MST"),
		time.Now().UTC().Format("2006-01-02 15:04 UTC"))
	// A background task's result is not a scheduled fire and must not be dressed
	// as one: the fire framing announces a cadence that doesn't exist and the
	// directive tells the agent to go do work, when the work is already done and
	// the only job left is to hand it over.
	fireContent := fmt.Sprintf(
		"[SCHEDULED UPDATE — fire %d, %s] %s\n\n%s\n%s",
		p.FireCount+1, recurringDetail(p), p.Prompt, scheduledFireDirective, timeCtx)
	if isTaskWake(p.Prompt) {
		fireContent = p.Prompt + "\n\n" + timeCtx
	}
	msgs = append(msgs, Message{Role: "user", Content: fireContent})

	// Assemble the SAME toolkit a live turn / standing-agent fire gets, so a
	// recurring task can actually DO its job: call its authored api / toolbox /
	// shell tools, dispatch through SecureAPI credentials, reach its attached
	// pipelines, knowledge, and memory. The prior implementation ran the loop
	// with only GetAgentTools(agent.AllowedTools…) = the STATIC built-in chat
	// tools and NO ToolSession, so the per-credential call_/fetch_url_ tools,
	// the persistent temp-tool pool, and the agent-scoped kit were all invisible
	// — the fire behaved as a disconnected scheduler that couldn't perform the
	// task. Mirror the dispatch path (runAgentSyncConfirm) via its shared seams:
	// GetAgentToolsWithSession (built-ins + credentials) plus
	// buildDispatchTurnExtrasWithOwner (conversational closures + agents grouped
	// tool + attached pipelines + hydrated custom tools). A DISTINCT sub-session
	// id keeps this fire's ephemeral load_tool state off the user's interactive
	// session (we still append the reply into the real session below).
	schedSessID := "scheduled:" + p.SessionID
	subSess := &ToolSession{
		LLM:           app.LLM,
		LeadLLM:       app.LeadLLM,
		Username:      p.Username,
		DB:            udb,
		ChatSessionID: schedSessID,
		// The fire runs under its own id; anything it starts in the background
		// belongs to the conversation the user is actually in. See
		// ToolSession.DeliverySessionID — without this a picture started from a
		// wake was delivered into "scheduled:<real>" and nobody ever saw it.
		DeliverySessionID: p.SessionID,
		AgentID:           agent.ID,
		IntentText:        p.Prompt, // Tier-1 tool elevation matches against the mission
		DeniedCredentials: credentialDenySet(agent, p.Username),
	}
	// The fire runs in ITS AGENT's directory, the same place the agent's own
	// turns run, so a wake can see what the agent just made.
	subSess.WorkspaceDir, subSess.WorkspaceFallback = agentTurnWorkspace(p.Username, agent.ID)
	// A background task's result arrives through this same fire path, and what
	// it produced is staged against the session rather than described in the
	// prompt. Fold it in BEFORE the loop: from here the files are ordinary
	// outbound attachments on this turn, so whichever way the agent answers —
	// send_message on a channel, the reply on any other surface — collects them
	// the way it collects anything else it made. Gated on the wake marker so an
	// ordinary recurring fire never picks up a delivery meant for another turn.
	carriedAttachments := 0
	if isTaskWake(p.Prompt) {
		if carriedAttachments = claimTaskAttachments(p.SessionID, subSess); carriedAttachments > 0 {
			Log("[orchestrate/scheduled] session=%s carrying %d staged attachment(s) from a finished background task", p.SessionID, carriedAttachments)
		}
	}
	defer DeleteSessionTempTools(udb, schedSessID)
	// Clone AllowedTools; force-add the always-on delivery + utility tools the
	// interactive turn includes so a tightly-scoped agent can still deliver an
	// attachment / tell the time (parity with the dispatch path).
	toolNames := append([]string(nil), agent.AllowedTools...)
	if len(toolNames) == 0 {
		for _, td := range RegisteredChatTools() {
			toolNames = append(toolNames, td.Name())
		}
	} else if !isNoToolsSentinel(toolNames) {
		has := func(n string) bool {
			for _, x := range toolNames {
				if x == n {
					return true
				}
			}
			return false
		}
		for _, n := range append([]string{"workspace"}, frameworkUtilityTools...) {
			if !has(n) {
				toolNames = append(toolNames, n)
			}
		}
	}
	tools, err := GetAgentToolsWithSession(subSess, toolNames...)
	if err != nil {
		tools = nil
		for _, n := range toolNames {
			if td, terr := GetAgentToolsWithSession(subSess, n); terr == nil && len(td) > 0 {
				tools = append(tools, td[0])
			}
		}
	}
	extraTools, availableBlock, customToolPrompt, subTurn := app.buildDispatchTurnExtrasWithOwner(ctx, agent, p.Username, udb, subSess, p.Username, udb)
	tools = append(tools, extraTools...)

	// Full dispatch persona: gated prompt + facts + available blocks +
	// customToolPrompt (so the LLM SEES the names of its lazily-loaded custom
	// tools) + per-agent capability guidance.
	facts := ListMemoryFacts(udb, factsNamespace(agent.ID))
	sysPrompt := dispatchSystemPrompt(agent, facts, availableBlock, customToolPrompt, schedSessID, udb, p.Username)

	started := time.Now()
	// Think the SAME way every other surface runs this agent. resolveDispatchThink
	// defaults ON (agent's Think setting / route default) and is the single source
	// of truth for chat, channel, and dispatch. The scheduled path used to hardcode
	// WithThink(false), so a fire ran the agent brain-off while its live turns think
	// — and a no-think 27B answers a scheduled directive with a conversational ack
	// ("Let me handle this cycle.") and stops at round 1 with zero tool calls
	// instead of planning and executing the work. Align it so a scheduled fire
	// plans and acts like a live turn does.
	think := resolveDispatchThink(agent)
	// Track the highest round the loop reached, so we can tell a fire that
	// finished with budget to spare from one that consumed its whole round
	// allowance and had to be forced to wrap up (its work is likely incomplete).
	softCap := resolveMaxWorkerRounds(agent)
	lastRound := 0
	// liveCalls accumulates the tool calls the model actually requested each
	// round, straight from OnStep — the SAME stream the live activity card
	// renders. This is the faithful record: a text-based-tool-call model (the
	// A3B emits calls as text, not native structures) doesn't always round-trip
	// every call into the transcript's structured ToolCalls, so reconstructing
	// the trace from the post-hoc transcript under-counts — the export then
	// showed fewer calls than the card. Recording live off OnStep closes that
	// gap; results get stitched back on from the transcript below.
	var liveCalls []ToolCall
	// Unattended fire: same pre-authorization policy as the standing runner — a
	// NeedsConfirm tool runs only if the owner pre-authorized it (AutoApproveTools),
	// else it's refused and queued. Replaces the old blanket auto-approve that
	// silently bypassed every tool's "Require confirm" contract on a schedule.
	// The fire's turn has no *session (it was built for this run; the session
	// record is p.SessionID here). Point its diagnostics at that record so
	// guard breadcrumbs — which guardrail stopped this fire, and why — land in
	// the same trail the rest of this function writes to, instead of being
	// dropped for want of a session pointer.
	subTurn.beginDispatchDiag(p.AgentID, p.SessionID)

	gate := app.newAutonomousGate(p.Username, agent.ID, subSess)
	// Live-activity registration: a recurring fire runs with no HTTP client
	// attached, so without this it was invisible while running — the "Active
	// now" surface only knew about interactive turns. No SSE ring is tailed;
	// the run exists for its snapshot (status / round / last tool).
	liveRun := app.runsRegistry().Create(p.Username, agent.ID, "", nil).
		Describe("scheduled", agent.Name, truncateObs(p.Prompt, 100))
	// Tag the context with this run, and give the session the tagged context.
	//
	// Without it, nothing started from a wake or a scheduled fire could detach.
	// TaskRunnerFunc resolves the owning agent by walking back to the enclosing
	// run (parentRunFromCtx) and refuses when it finds none, and the refusal is
	// a SILENT fallback to running inline — correct as a fallback, invisible as
	// a diagnosis.
	//
	// What it cost: a set of three pictures where the wake turn faithfully
	// started pieces two and three, both ran inline instead of detaching, both
	// came back with "call workspace(attach)" that the model never called, and
	// the user received one picture out of three while being told all three
	// were done. Every guarantee built on detaching — the one-job slot, the
	// per-piece wake, the delivery of what it made — was off in exactly the
	// turns that needed it, because the dispatch paths tag their runs here and
	// this one never did.
	ctx = withParentRun(ctx, liveRun.ID)
	// A scheduled fire has no request behind it, so nothing ever reported
	// what one costs — the tokens landed in the process counter and the
	// admin rollup and nowhere a reader would look. Give the fire its own
	// line, same shape as a dispatch, labelled with the run so it can be
	// matched against the live tree.
	ctx, reportUsage := WithSubUsage(ctx, "scheduled "+agent.Name+" "+liveRun.ID)
	defer reportUsage()
	subSess.Ctx = ctx
	// Safety net only — Complete is idempotent (first call sticks), and the
	// explicit call right after the loop below lands first with the real
	// status. This catches a panic/early-return path so the run can't be
	// stuck "running" until the sweeper's retention window.
	defer liveRun.Complete(RunStatusFailed)
	msgs, gDecline := subTurn.applyInputGuardrail(msgs)
	resp, transcript, runErr := app.RunAgentLoop(ctx, msgs, AgentLoopConfig{
		// A terminal-rule pre_input block refused this request outright: the loop
		// delivers this text and never calls a model. Empty on every other turn.
		PreEmptedReply:      gDecline,
		SendGuardKey:        sendGuardKey,
		SystemPrompt:        sysPrompt,
		Tools:               tools,
		MaxRounds:           softCap,
		StampLocation:       UserLocation(p.Username), // stamp the turn in the owning user's zone
		ThinkBudget:         agent.ThinkBudget,
		Confirm:             gate.confirm,
		GuardrailCheck:      subTurn.guardrailEnforcer().Check,
		GuardrailActionGate: subTurn.guardrailEnforcer().ActionGate,
		GuardrailHalted:     subTurn.guardrailEnforcer().Halted,
		GuardrailReject:     subTurn.guardrailEnforcer().Reject,
		GuardrailDeclines:   subTurn.agent.GuardrailDeclines,
		OnStep: func(s StepInfo) {
			if s.Round > lastRound {
				lastRound = s.Round
			}
			liveCalls = append(liveCalls, s.ToolCalls...)
			liveRun.SetProgress(s.Round, s.ToolCalls)
		},
		// Is the reply true about what this fire actually did?
		//
		// A scheduled fire is the turn that needs this MOST and was the one
		// without it. Nobody is reading along: a fire that writes out three
		// finished posts, marks them done and calls no posting tool produces a
		// transcript nothing disputes, and the run ledger records a success.
		// The interactive paths had the judge because a person was there to
		// notice anyway. See turn_judge.go.
		//
		// No PriorWork: a fire has no machine steps running ahead of its loop,
		// so the tools the loop ran are the whole of what happened.
		TurnClaimJudge: app.turnClaimJudge(ctx),
		// And it is asked on every fire, not only the ones whose evidence looks
		// wrong. The judge was already attached here for the reason above, and
		// the pre-filter then declined to call it on exactly the turn this
		// comment describes: nine moltbook reads, no failures, three posts
		// reported as done. Ran cleanly, claimed everything, judged never.
		Unattended: true,
		// And whether it KNOWS what it asserts. The two travel together
		// everywhere else and a test enforces it, which is the right rule: a
		// fire stating an unchecked fact as certain is exactly as unwatched as
		// one claiming work it did not do. UncheckedClaims is the scope that
		// makes it fire at all — the agent's own remembered facts, the same
		// ones the dispatch path hands it.
		TurnGroundingJudge: app.turnGroundingJudge(ctx),
		UncheckedClaims:    UncheckedFactNotes(facts),
		// Custom-tool resolution, same as the dispatch path: resolve a direct
		// call to a has-args custom tool and surface tools loaded via load_tool.
		ToolFallbackResolver: subTurn.lazyToolFallback,
		DynamicTools:         subTurn.dynamicNewTempTools(subSess),
		DrainViewImages:      subSess.DrainViewImages,
		BeforeToolRound:      func() { SnapshotImageRefs(subSess) },
		ChatOptions: []ChatOption{
			WithRouteKey("app.orchestrate.worker"),
			WithThink(think),
		},
	})

	// CANCELED (superseded / shutdown) is distinct from FAILED — see runOutcomeStatus.
	liveRun.Complete(runOutcomeStatus(runErr, resp != nil))

	// Record every fire in the run-ledger — the same store standing agents and
	// event monitors write to (RootDB, owner=username), so recurring fires show
	// up in list_runs / inspect_run / the Activity feed instead of only in a
	// bespoke log line. A scheduled fire is badged "schedule"; a manual "Run now"
	// test is badged "manual" (parity with standing-agent run-now) so the two are
	// distinguishable in the ledger. The prompt is the brief and the reply is kept
	// (encrypted) as Raw.
	trigger := "schedule"
	if !reArm {
		trigger = "manual"
	}
	agentLabel := agent.Name
	if strings.TrimSpace(agentLabel) == "" {
		agentLabel = agent.ID
	}
	// Tool trace: the card renders it as chips, the ledger stores it as Steps,
	// and the preamble guard keys on whether it's empty. Prefer the LIVE OnStep
	// calls (faithful to what actually ran, including a text-based model's calls)
	// and stitch result bodies back on from the transcript. Fall back to pure
	// transcript reconstruction only if OnStep captured nothing (e.g. a run that
	// errored before its first round).
	toolTrace := persistedToolCallsFromLiveCalls(liveCalls, transcript)
	if len(toolTrace) == 0 {
		toolTrace = persistedToolCallsFromTranscript(transcript)
	}
	steps := runStepsFromToolCalls(toolTrace)
	// A wake that carried files but ended without sending anything means the
	// picture went nowhere — the exact silent failure this staging exists to
	// end, so say so where someone reading the log will see it rather than
	// letting a "done!" with no attachment look like a success. A messaging
	// surface collects them through the send; a plain web thread has no stored
	// attachment channel, so this is expected there and still worth recording.
	if carriedAttachments > 0 && !toolCallsInclude(toolTrace, "send_message") {
		Log("[orchestrate/scheduled] WARN session=%s wake carried %d attachment(s) but the turn sent no message — they were not delivered",
			p.SessionID, carriedAttachments)
	}
	record := func(status RunStatus, summary, raw, errStr string) {
		RecordRun(RootDB, RunRecord{
			Owner:   p.Username,
			Agent:   agentLabel,
			Trigger: trigger,
			Brief:   p.Prompt,
			Status:  status,
			Summary: summary,
			Raw:     raw,
			Steps:   steps,
			Started: started,
			Ended:   time.Now(),
			Err:     errStr,
		})
	}

	// NOTE: the next occurrence was pre-armed above, so the failure / empty /
	// preamble exits below just return — nothing to reschedule.
	if runErr != nil {
		Log("[orchestrate/scheduled] agent=%s session=%s fire %d FAILED: %v", agentLabel, p.SessionID, p.FireCount+1, runErr)
		record(RunFailed, "Recurring fire errored before it could post.", "", runErr.Error())
		appendSessionDiag(udb, p.AgentID, p.SessionID, "recurring-fire-failed",
			fmt.Sprintf("Recurring task %q fire %d errored before it could post: %v", recurringName(p), p.FireCount+1, runErr))
		return nil
	}
	// A guardrail that stopped this fire is the single most useful thing the
	// ledger can say about it, and it used to say nothing: the decline text
	// became the reply and the run recorded OK, so a task silently doing nothing
	// looked identical to one working fine. Named rules, human-readable status.
	if rules := subTurn.guardrailRulesHit; len(rules) > 0 {
		named := strings.Join(quoteAll(rules), ", ")
		Log("[orchestrate/scheduled] agent=%s session=%s fire %d hit guardrail(s) %s", agentLabel, p.SessionID, p.FireCount+1, named)
		appendSessionDiag(udb, p.AgentID, p.SessionID, "recurring-fire-guardrail",
			fmt.Sprintf("Recurring task %q fire %d was stopped by guardrail %s. The fire is recorded in Activity; nothing was posted from the blocked content.", recurringName(p), p.FireCount+1, named))
		record(RunAttention,
			fmt.Sprintf("Stopped by guardrail %s.", named),
			strings.TrimSpace(respContent(resp)), "")
		return nil
	}
	reply := ""
	if resp != nil {
		reply = strings.TrimSpace(resp.Content)
	}
	if reply == "" {
		Log("[orchestrate/scheduled] agent=%s session=%s fire %d produced no reply, skipping append", agentLabel, p.SessionID, p.FireCount+1)
		record(RunOK, "(no output — nothing to post this cycle)", "", "")
		appendSessionDiag(udb, p.AgentID, p.SessionID, "recurring-fire-empty",
			fmt.Sprintf("Recurring task %q fire %d produced no reply — nothing was posted this cycle.", recurringName(p), p.FireCount+1))
		return nil
	}

	// Preamble guard (backstop to scheduledFireDirective): a fire that produced
	// text but called ZERO tools did no real work this cycle — it narrated intent
	// ("Starting cycle 5. Let me check notifications…") and stopped. On this path
	// the loop ends the instant the model emits text without a tool call, so that
	// intent-stub becomes resp.Content and would post as a bogus report card.
	// Don't append it. Still record the fire in the ledger (Raw = the preamble) so
	// it stays inspectable via list_runs / inspect_run, then reschedule. The
	// prompt directive prevents most of these; this catches the ones the 27B emits
	// anyway. NOTE: a recurring task that legitimately produces pure text with no
	// tools would also be skipped — not a shape these action-oriented fires use;
	// add an opt-out flag if that ever becomes real.
	//
	// NOT for a background-task wake. There the model has nothing left to do but
	// say what came back — answering in one line with no tool call is the
	// CORRECT shape, and suppressing it threw away the result the user was
	// waiting for and left the conversation looking like the task never
	// finished.
	if len(toolTrace) == 0 && !isTaskWake(p.Prompt) {
		Log("[orchestrate/scheduled] agent=%s session=%s fire %d produced text but no tool calls (preamble only), skipping append", agentLabel, p.SessionID, p.FireCount+1)
		record(RunOK, "(no tool activity — preamble only, nothing posted)", reply, "")
		appendSessionDiag(udb, p.AgentID, p.SessionID, "recurring-fire-suppressed",
			fmt.Sprintf("Recurring task %q fire %d produced text but called no tools — treated as a preamble stub and NOT posted. The full text is preserved in the run ledger (Activity).", recurringName(p), p.FireCount+1))
		return nil
	}

	// Round-budget exhaustion: the loop reached its soft cap and had to be forced
	// to wrap up, so this fire's work is probably incomplete (it ran out of rounds
	// mid-task rather than finishing). Surface it — badge the ledger run "attention"
	// and mark the card — instead of letting a truncated cycle read as a clean one.
	// Raising the agent's max_worker_rounds is the fix when this recurs.
	hitCap := lastRound >= softCap
	detail := fmt.Sprintf("%s · %s · fire %d", agentLabel, recurringDetail(p), p.FireCount+1)
	// A background task shares this path but has no cadence and no fire count.
	// The recurring subtitle read "recurring · every 0m · fire 1" on it, which
	// announces a schedule that does not exist.
	if isTaskWake(p.Prompt) {
		detail = agentLabel + " · finished in the background"
	}
	// A wake that was told to start the next piece, and started nothing.
	//
	// The no-tool-call suppression above is deliberately OFF for a task wake,
	// because an ordinary one has nothing left to do but say what came back.
	// A wake carrying a continuation is the opposite shape: the tool call WAS
	// the job. Observed — "Here is your first take… Starting the second
	// variation now", no call, set stranded at 1 of 3, and the only trace was
	// a ledger entry that expired half an hour later.
	//
	// The reply still posts: the picture it delivered is real and the user is
	// waiting for it. What changes is that the set closes now rather than
	// lingering to renumber the next unrelated render, and the stall leaves a
	// breadcrumb instead of looking like a set that simply ended.
	if strings.Contains(p.Prompt, SeriesContinuationMarker) && len(toolTrace) == 0 {
		CloseTaskSeries(p.SessionID, RenderDetachIdentity)
		Log("[orchestrate/task] agent=%s session=%s delivered a piece but called no tool — the set stops here", agentLabel, p.SessionID)
		appendSessionDiag(udb, p.AgentID, p.SessionID, "series-abandoned",
			"A background set was told to start its next piece and the turn made no tool call — it answered in prose only. The finished piece was delivered; the rest of the set was NOT started, and the set has been closed rather than left open. If this recurs, the continuation instruction is not reaching the model, or the model is answering before acting.")
		detail += " · set not continued"
	}
	if hitCap {
		detail += fmt.Sprintf(" · hit round cap (%d) — may be incomplete", softCap)
	}
	// FINAL fire: the pre-arm declined to schedule a successor, so this task
	// stops here. Say so on the card. Retirement used to be a single log line
	// and a row that quietly stopped existing — indistinguishable, from the
	// outside, from a schedule the framework had dropped, which is exactly how
	// it got read. The last thing a task posts should be the fact that it is
	// the last thing it will post.
	if retireReason != "" {
		detail += " · FINAL FIRE — " + retireReason + "; this task will not run again"
	}

	// Render the fire as a scheduled-report card (ReportFrom/ReportKind), the
	// same distinct-bubble treatment standing-agent reports and monitor wakes
	// get — a bare assistant bubble hid that the message was an automated fire.
	// Carry the full tool trace too (extracted from the loop transcript, since a
	// scheduled fire has no live chatTurn to snapshot from) so the export and the
	// session UI show WHAT the agent did to produce the reply, not just the text.
	// Background (Surface): the fire ran (tools executed, recorded to the run
	// ledger below) but posts no reply card to any thread — no agent visibility.
	if recordFire {
		// A task wake's note is the only record of WHAT came back — the handle
		// for a finished picture, the id of a produced file. It reached this
		// turn's model as a prompt and was then thrown away, so the next thing
		// the user said ran against a history where the agent announced a
		// result that nothing identifies. Asked for one more change to it, the
		// model has no handle to name, invents one, and the edit fails.
		//
		// Hidden: the LLM reads it, the transcript doesn't render it. The user
		// already saw the reply it produced; showing them the machinery that
		// prompted it would be showing the same news twice.
		if note := strings.TrimSpace(p.HistoryNote); note != "" {
			sess.Messages = append(sess.Messages, ChatMessage{
				Role:    "user",
				Content: note,
				Created: time.Now(),
				Hidden:  true,
			})
		}
		sess.Messages = append(sess.Messages, ChatMessage{
			Role:         "assistant",
			Content:      reply,
			Created:      time.Now(),
			ReportFrom:   recurringName(p),
			ReportKind:   cortexKindScheduled,
			ReportDetail: detail,
			ToolCalls:    toolTrace,
			// This turn had no live stream to paint an attachment onto — it ran
			// with nobody watching. Keeping the bytes is the only way the thread
			// can ever show what it delivered.
			Attachments: keepDeliveredAttachments(p.Username, subSess.Images),
		})
		sess.LastAt = time.Now()
		if _, err := saveChatSession(udb, sess); err != nil {
			Log("[orchestrate/scheduled] save failed for session %s: %v", p.SessionID, err)
		}
	}
	// A background result whose conversation lives on a messaging channel has to
	// be SENT there — appending it to the stored session is what the recurring
	// path wants and leaves the person who asked with nothing.
	deliverWakeToChannel(p, subSess, reply, toolTrace)
	status, summary := RunOK, standingSummary(reply)
	if hitCap {
		status = RunAttention
		summary = fmt.Sprintf("hit round cap (%d rounds) — cycle may be incomplete. %s", softCap, summary)
		Log("[orchestrate/scheduled] agent=%s session=%s fire %d HIT ROUND CAP (%d) — likely incomplete", agentLabel, p.SessionID, p.FireCount+1, softCap)
	}
	record(status, summary, reply, "")
	Log("[orchestrate/scheduled] agent=%s session=%s posted fire %d (%d chars)",
		agentLabel, p.SessionID, p.FireCount+1, len(reply))

	if reArm && armedID != "" {
		// Productive fire — reaching here means toolTrace was non-empty (a
		// preamble-only fire returns at the guard above), so the task did real
		// work this cycle. Renew the idle clock ON THE ALREADY-ARMED next
		// occurrence. If that occurrence has fired already (a fire that
		// outlived its own gap), skip the renewal — never re-create a consumed
		// task, that's how chains duplicate.
		armed.LastActive = time.Now().UTC().Format(time.RFC3339)
		if !UpdateScheduledTaskPayload(armedID, armed) {
			Log("[orchestrate/scheduled] session=%s: armed next fire already consumed — idle-clock renewal skipped", p.SessionID)
		}
	}
	return nil
}

// preArmNextFire persists the NEXT occurrence of a recurring task BEFORE the
// current fire runs (see the call site for why). Applies the same retire
// gates the old post-fire reschedule did — idle reap, total fire cap, pattern
// exhaustion — each logged; on any of them the chain intentionally ends here
// while the current (final) fire still runs. Returns the armed task id and
// the payload it was armed with.
func preArmNextFire(p orchUpdatePayload) (string, orchUpdatePayload, bool, string) {
	if idleDays := orchUpdateIdleDays(); p.idleReapDue(time.Now(), idleDays) {
		Log("[orchestrate/scheduled] task %q (session=%s) reaped: idle > %d days — recurring task auto-cancelled", recurringName(p), p.SessionID, idleDays)
		return "", p, false, fmt.Sprintf("idle for more than %d days", idleDays)
	}
	armed := p
	armed.FireCount++
	if armed.FireCount >= armed.effectiveMaxFires() {
		Log("[orchestrate/scheduled] task %q (session=%s) retiring: this fire reaches the fire cap %d (recurring task auto-cancelled after it)", recurringName(p), p.SessionID, armed.effectiveMaxFires())
		return "", p, false, fmt.Sprintf("reached its %d-fire cap (max_fires)", armed.effectiveMaxFires())
	}
	next, err := computeNextFire(&armed, time.Now().In(UserLocation(p.Username)))
	if err != nil {
		Log("[orchestrate/scheduled] cannot compute next fire for task %q (session=%s): %v — stopping after this fire", recurringName(p), p.SessionID, err)
		return "", p, false, fmt.Sprintf("no next fire could be computed (%v)", err)
	}
	id, err := ScheduleTask(OrchestrateScheduledUpdateKind, armed, next)
	if err != nil {
		Log("[orchestrate/scheduled] pre-arm failed for task %q (session=%s): %v", recurringName(p), p.SessionID, err)
		return "", p, false, fmt.Sprintf("the next fire could not be scheduled (%v)", err)
	}
	return id, armed, true, ""
}

// RunOrchestrateUpdateNow fires one recurring task immediately by its scheduler
// task id — a one-off manual test that does NOT touch the schedule (no
// reschedule, no FireCount increment); the recurring chain keeps firing on its
// own timer. Ownership is enforced by matching the task's payload username via
// listAgentRecurringTasks, so a user can't fire another user's task by guessing
// its id. Backs the console "Run now" action.
func RunOrchestrateUpdateNow(ctx context.Context, user, taskID string) error {
	for _, rt := range listAgentRecurringTasks(user, "") {
		if rt.TaskID == taskID {
			return fireOrchestrateUpdate(ctx, rt.Payload, false)
		}
	}
	return fmt.Errorf("recurring task not found")
}

// reschedule emits the next fire of a recurring orchestrate update. The next
// time — and, for the random pattern, the mutation of p.RemainingToday — is
// computed by computeNextFire so the fixed/random branch lives in one place.
// brokenDormantReArm is how often a parked (broken) recurring task re-checks. It
// never runs the agent while broken — this just keeps a scheduler entry alive so
// the task stays LISTED for the owner to resume/relink or delete.
const brokenDormantReArm = 24 * time.Hour

// parkRecurringBroken keeps a recurring task LISTED but dormant: it marks the
// task broken and re-arms a no-op tick at a slow cadence WITHOUT counting a fire,
// tripping the fire cap, or idle-reaping it. This replaces the old silent drop
// when a task's agent is gone, so the owner sees a "needs relink" task instead of
// a vanished one. A subsequent resume/relink clears Broken and reschedules the
// task on its real cadence.
func parkRecurringBroken(p orchUpdatePayload, reason string) {
	p.Broken = true
	if reason != "" {
		p.BrokenReason = reason
	}
	next := time.Now().Add(brokenDormantReArm)
	if _, err := ScheduleTask(OrchestrateScheduledUpdateKind, p, next); err != nil {
		Log("[orchestrate/scheduled] park-broken reschedule failed for session %s: %v", p.SessionID, err)
	}
}

func reschedule(p orchUpdatePayload) {
	// Idle guard — reap a task that has gone tune_orch_update_idle_days without a
	// productive fire or an edit. A task that keeps doing useful work (or gets
	// edited) renews LastActive and never trips this; only a spinning or
	// forgotten one ages out. This replaced the old flat fire cap so max_fires=0
	// can mean "indefinite" without a task running forever unwatched.
	if idleDays := orchUpdateIdleDays(); p.idleReapDue(time.Now(), idleDays) {
		Log("[orchestrate/scheduled] session=%s reaped: idle > %d days — recurring task auto-cancelled", p.SessionID, idleDays)
		return
	}
	p.FireCount++
	if p.FireCount >= p.effectiveMaxFires() {
		// A recurring task that hits its total fire cap retires here. Log it —
		// otherwise the task simply stops rescheduling and vanishes with no
		// trace (the sibling error path below logs; this one used to be silent,
		// which made a capped-out schedule impossible to distinguish from a
		// delete). A high-frequency pattern (e.g. 24x/day) burns the default
		// cap in days, so this fires more than the name "cap" suggests.
		Log("[orchestrate/scheduled] session=%s retired: reached fire cap %d (recurring task auto-cancelled)", p.SessionID, p.effectiveMaxFires())
		return
	}
	next, err := computeNextFire(&p, time.Now().In(UserLocation(p.Username)))
	if err != nil {
		Log("[orchestrate/scheduled] cannot compute next fire for session %s: %v — stopping", p.SessionID, err)
		return
	}
	if _, err := ScheduleTask(OrchestrateScheduledUpdateKind, p, next); err != nil {
		Log("[orchestrate/scheduled] reschedule failed for session %s: %v", p.SessionID, err)
	}
}

// ListOrchestrateUpdates returns the active scheduled updates for one
// (agent, session) pair.
func ListOrchestrateUpdates(sessionID string) []orchUpdatePayload {
	var out []orchUpdatePayload
	for _, t := range ListScheduledTasks(OrchestrateScheduledUpdateKind) {
		var p orchUpdatePayload
		if json.Unmarshal(t.Payload, &p) != nil || p.SessionID != sessionID {
			continue
		}
		out = append(out, p)
	}
	return out
}

// ScheduleOrchestrateUpdate is the public helper the recurring(schedule) tool
// calls. Validates the spec (per-pattern), enforces guardrails, and schedules
// the first fire (which for the random pattern also seeds the day's plan).
func ScheduleOrchestrateUpdate(spec RecurringSpec) (string, error) {
	if spec.SessionID == "" || spec.AgentID == "" || spec.Username == "" {
		return "", errors.New("recurring(schedule) needs session, agent, and user")
	}
	if strings.TrimSpace(spec.Prompt) == "" {
		return "", errors.New("recurring(schedule) requires a prompt")
	}
	if spec.Pattern == "" {
		spec.Pattern = RecurringFixed
	}
	if spec.HasWindow {
		if spec.WindowFromMin < 0 || spec.WindowToMin > 24*60 || spec.WindowFromMin >= spec.WindowToMin {
			return "", errors.New("active window must be a same-day range with from < to (00:00–24:00)")
		}
	}
	minInterval := orchUpdateMinInterval()
	switch spec.Pattern {
	case RecurringFixed:
		if time.Duration(spec.IntervalSeconds)*time.Second < minInterval {
			return "", fmt.Errorf("interval too small — minimum %s", minInterval)
		}
	case RecurringRandom:
		// Default and floor the gap to the deployment minimum interval.
		if time.Duration(spec.MinGapSeconds)*time.Second < minInterval {
			spec.MinGapSeconds = int(minInterval / time.Second)
		}
		if spec.TimesPerDay > 0 {
			// N random times inside a daily window.
			if !spec.HasWindow {
				return "", errors.New("random pattern with times_per_day needs an active window (active_from / active_to) to place the fires within")
			}
			if spec.TimesPerDay > 48 {
				return "", errors.New("times_per_day is capped at 48")
			}
			windowSec := (spec.WindowToMin - spec.WindowFromMin) * 60
			if need := spec.MinGapSeconds * (spec.TimesPerDay - 1); windowSec < need {
				return "", fmt.Errorf("window %s–%s can't hold %d fires spaced %dm apart — widen the window, lower the count, or shorten the gap",
					fmtHHMM(spec.WindowFromMin), fmtHHMM(spec.WindowToMin), spec.TimesPerDay, spec.MinGapSeconds/60)
			}
		} else {
			// Continuous spaced-random: unlimited fires at random gaps in
			// [min, max]; default max to 2× min so the spacing actually varies.
			// The window is optional here (fires outside it defer to the next
			// open); the min gap is the throttle.
			if spec.MaxGapSeconds <= spec.MinGapSeconds {
				spec.MaxGapSeconds = spec.MinGapSeconds * 2
			}
			if spec.HasWindow {
				windowSec := (spec.WindowToMin - spec.WindowFromMin) * 60
				if windowSec < spec.MinGapSeconds {
					return "", fmt.Errorf("active window %s–%s is shorter than the minimum gap (%dm) — widen it or lower the gap",
						fmtHHMM(spec.WindowFromMin), fmtHHMM(spec.WindowToMin), spec.MinGapSeconds/60)
				}
			}
		}
	default:
		return "", fmt.Errorf("unknown pattern %q — use fixed or random", spec.Pattern)
	}
	active := ListOrchestrateUpdates(spec.SessionID)
	if len(active) >= orchUpdateMaxPerSession() {
		return "", fmt.Errorf("session %s already has %d active recurring tasks (cap %d) — cancel one first", spec.SessionID, len(active), orchUpdateMaxPerSession())
	}
	p := orchUpdatePayload{
		SessionID:       spec.SessionID,
		AgentID:         spec.AgentID,
		Username:        spec.Username,
		Prompt:          spec.Prompt,
		Name:            strings.TrimSpace(spec.Name),
		Pattern:         spec.Pattern,
		IntervalSeconds: spec.IntervalSeconds,
		TimesPerDay:     spec.TimesPerDay,
		MinGapSeconds:   spec.MinGapSeconds,
		MaxGapSeconds:   spec.MaxGapSeconds,
		HasWindow:       spec.HasWindow,
		WindowFromMin:   spec.WindowFromMin,
		WindowToMin:     spec.WindowToMin,
		MaxFires:        spec.MaxFires,
		Surface:         strings.TrimSpace(spec.Surface),
		// Preserve fire count + creation time on edit-in-place; fresh schedules
		// pass zero/empty and start clean.
		FireCount: spec.FireCount,
		CreatedAt: firstNonEmptyStr(strings.TrimSpace(spec.CreatedAt), time.Now().UTC().Format(time.RFC3339)),
		// Create AND edit-in-place both renew the idle clock — reaching this
		// path is a deliberate user action. The automatic re-arm does NOT come
		// through here (it goes reschedule -> ScheduleTask, preserving LastActive),
		// so only real user/productive activity renews.
		LastActive: time.Now().UTC().Format(time.RFC3339),
	}
	next, err := computeNextFire(&p, time.Now().In(UserLocation(p.Username)))
	if err != nil {
		return "", err
	}
	id, err := ScheduleTask(OrchestrateScheduledUpdateKind, p, next)
	if err != nil {
		return "", err
	}
	return id, nil
}

// CancelOrchestrateUpdate removes a scheduled update by task id.
// Validates the session id matches so one session's tools can't
// cancel another session's updates.
func CancelOrchestrateUpdate(sessionID, taskID string) error {
	if sessionID == "" || taskID == "" {
		return errors.New("session and task id required")
	}
	for _, t := range ListScheduledTasks(OrchestrateScheduledUpdateKind) {
		if t.ID != taskID {
			continue
		}
		var p orchUpdatePayload
		if json.Unmarshal(t.Payload, &p) != nil || p.SessionID != sessionID {
			return errors.New("task does not belong to this session")
		}
		UnscheduleTask(taskID)
		return nil
	}
	return errors.New("task not found")
}

// persistedToolCallsFromLiveCalls builds the fire's tool trace from the calls
// OnStep observed live — the authoritative record of what the model requested
// each round — and stitches each call's result body back on from the transcript
// (results land in tool-role messages keyed by call ID). This is faithful where
// the pure-transcript reconstruction is lossy: a text-based-tool-call model
// doesn't always record every call in an assistant message's structured
// ToolCalls, so reconstructing from the transcript alone under-counts, and the
// export showed fewer calls than the live card. A call whose ID doesn't match a
// transcript result (a text-parsed call may carry no stable ID) is still kept —
// better a call with no result body than a missing call.
func persistedToolCallsFromLiveCalls(calls []ToolCall, transcript []Message) []PersistedToolCall {
	if len(calls) == 0 {
		return nil
	}
	results := map[string]ToolResult{}
	for _, m := range transcript {
		for _, tr := range m.ToolResults {
			results[tr.ID] = tr
		}
	}
	out := make([]PersistedToolCall, 0, len(calls))
	for _, tc := range calls {
		pc := PersistedToolCall{Name: tc.Name, Args: tc.Args}
		if tr, ok := results[tc.ID]; ok {
			if tr.IsError {
				pc.Err = tr.Content
			} else {
				pc.Result = tr.Content
			}
		}
		out = append(out, pc)
	}
	return out
}

// persistedToolCallsFromTranscript reconstructs the per-message tool trace from
// a completed RunAgentLoop transcript. The live chat path snapshots calls off a
// chatTurn as they fire; a scheduled fire has no chatTurn, so we recover the
// same [ ]PersistedToolCall shape from the returned messages instead: each
// assistant message carries its ToolCalls, and the following tool-role message
// carries the matching ToolResults keyed by call ID. Order is preserved so the
// export reads top-to-bottom like a live turn.
func persistedToolCallsFromTranscript(transcript []Message) []PersistedToolCall {
	if len(transcript) == 0 {
		return nil
	}
	// Index every tool result by its call ID across the whole transcript — a
	// result can land in any later message, not strictly the next one.
	results := map[string]ToolResult{}
	for _, m := range transcript {
		for _, tr := range m.ToolResults {
			results[tr.ID] = tr
		}
	}
	var out []PersistedToolCall
	for _, m := range transcript {
		for _, tc := range m.ToolCalls {
			pc := PersistedToolCall{Name: tc.Name, Args: tc.Args}
			if tr, ok := results[tc.ID]; ok {
				if tr.IsError {
					pc.Err = tr.Content
				} else {
					pc.Result = tr.Content
				}
			}
			out = append(out, pc)
		}
	}
	return out
}

// runStepsFromToolCalls flattens the card's tool trace into the ledger's []RunStep
// so inspect_run shows WHICH tools a fire ran (name, args, result/err), not just
// its final text. Args are JSON-encoded to match how the trace was serialized.
func runStepsFromToolCalls(calls []PersistedToolCall) []RunStep {
	if len(calls) == 0 {
		return nil
	}
	out := make([]RunStep, 0, len(calls))
	for _, c := range calls {
		args := ""
		if len(c.Args) > 0 {
			if b, err := json.Marshal(c.Args); err == nil {
				args = string(b)
			}
		}
		out = append(out, RunStep{Name: c.Name, Args: args, Result: c.Result, Err: c.Err})
	}
	return out
}

// quoteAll wraps each entry in quotes for a human-readable list — guardrail
// rules are user-authored sentences, and an unquoted join of them reads as one
// run-on rule rather than several.
func quoteAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, fmt.Sprintf("%q", s))
	}
	return out
}

// respContent is the reply text, nil-safe.
func respContent(r *Response) string {
	if r == nil {
		return ""
	}
	return r.Content
}
