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

	IntervalSeconds int    `json:"interval_seconds"`
	FireCount       int    `json:"fire_count"`
	CreatedAt       string `json:"created_at"`

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
	var armed orchUpdatePayload
	if reArm {
		armedID, armed, _ = preArmNextFire(p)
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
	sess, ok := loadChatSession(udb, p.AgentID, p.SessionID)
	if !ok {
		// The target thread doesn't exist yet — synthesize it rather than
		// dropping the task. A channel agent's Cortex home thread
		// (channel:<agentID>) is created lazily and won't exist until its first
		// turn, and a recurring task can be scheduled against a session before
		// any human posts to it; in both cases a missing session is expected,
		// not a failure. Start a fresh thread with the scheduled id (parity with
		// the chat GET path, which returns an empty session on a miss); the
		// fire's reply is what materializes it via saveChatSession below.
		sess = ChatSession{ID: p.SessionID, AgentID: p.AgentID}
	}

	// Cap message history so a long-running tracker doesn't accumulate
	// ever-growing context — same 30-turn cutoff chat uses.
	history := sess.Messages
	if len(history) > 30 {
		history = history[len(history)-30:]
	}
	msgs := make([]Message, 0, len(history)+1)
	for _, m := range history {
		msgs = append(msgs, Message{Role: m.Role, Content: m.Content})
	}
	msgs = append(msgs, Message{
		Role: "user",
		Content: fmt.Sprintf(
			"[SCHEDULED UPDATE — fire %d, %s] %s\n\n%s",
			p.FireCount+1, recurringDetail(p), p.Prompt, scheduledFireDirective),
	})

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
		LLM:               app.LLM,
		LeadLLM:           app.LeadLLM,
		Username:          p.Username,
		DB:                udb,
		ChatSessionID:     schedSessID,
		DeniedCredentials: credentialDenySet(agent, p.Username),
	}
	if ws, werr := EnsureWorkspaceDir(p.Username); werr == nil {
		subSess.WorkspaceDir = ws
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
	resp, transcript, runErr := app.RunAgentLoop(ctx, msgs, AgentLoopConfig{
		SystemPrompt:  sysPrompt,
		Tools:         tools,
		MaxRounds:     softCap,
		StampLocation: UserLocation(p.Username), // stamp the turn in the owning user's zone
		ThinkBudget:   agent.ThinkBudget,
		Confirm:       func(name, args string) bool { return true },
		OnStep: func(s StepInfo) {
			if s.Round > lastRound {
				lastRound = s.Round
			}
		},
		// Custom-tool resolution, same as the dispatch path: resolve a direct
		// call to a has-args custom tool and surface tools loaded via load_tool.
		ToolFallbackResolver: subTurn.lazyToolFallback,
		DynamicTools:         subTurn.dynamicNewTempTools(subSess),
		DrainViewImages:      subSess.DrainViewImages,
		ChatOptions: []ChatOption{
			WithRouteKey("app.orchestrate.worker"),
			WithThink(think),
		},
	})

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
	// Tool trace, reconstructed once from the loop transcript: the card renders
	// it as chips, the ledger stores it as Steps, and the preamble guard keys on
	// whether it's empty. A scheduled fire has no live chatTurn to snapshot from,
	// so this reconstruction IS the record of what the fire did.
	toolTrace := persistedToolCallsFromTranscript(transcript)
	steps := runStepsFromToolCalls(toolTrace)
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
		return nil
	}
	reply := ""
	if resp != nil {
		reply = strings.TrimSpace(resp.Content)
	}
	if reply == "" {
		Log("[orchestrate/scheduled] agent=%s session=%s fire %d produced no reply, skipping append", agentLabel, p.SessionID, p.FireCount+1)
		record(RunOK, "(no output — nothing to post this cycle)", "", "")
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
	if len(toolTrace) == 0 {
		Log("[orchestrate/scheduled] agent=%s session=%s fire %d produced text but no tool calls (preamble only), skipping append", agentLabel, p.SessionID, p.FireCount+1)
		record(RunOK, "(no tool activity — preamble only, nothing posted)", reply, "")
		return nil
	}

	// Round-budget exhaustion: the loop reached its soft cap and had to be forced
	// to wrap up, so this fire's work is probably incomplete (it ran out of rounds
	// mid-task rather than finishing). Surface it — badge the ledger run "attention"
	// and mark the card — instead of letting a truncated cycle read as a clean one.
	// Raising the agent's max_worker_rounds is the fix when this recurs.
	hitCap := lastRound >= softCap
	detail := fmt.Sprintf("%s · %s · fire %d", agentLabel, recurringDetail(p), p.FireCount+1)
	if hitCap {
		detail += fmt.Sprintf(" · hit round cap (%d) — may be incomplete", softCap)
	}

	// Render the fire as a scheduled-report card (ReportFrom/ReportKind), the
	// same distinct-bubble treatment standing-agent reports and monitor wakes
	// get — a bare assistant bubble hid that the message was an automated fire.
	// Carry the full tool trace too (extracted from the loop transcript, since a
	// scheduled fire has no live chatTurn to snapshot from) so the export and the
	// session UI show WHAT the agent did to produce the reply, not just the text.
	sess.Messages = append(sess.Messages, ChatMessage{
		Role:         "assistant",
		Content:      reply,
		Created:      time.Now(),
		ReportFrom:   recurringName(p),
		ReportKind:   cortexKindScheduled,
		ReportDetail: detail,
		ToolCalls:    toolTrace,
	})
	sess.LastAt = time.Now()
	if _, err := saveChatSession(udb, sess); err != nil {
		Log("[orchestrate/scheduled] save failed for session %s: %v", p.SessionID, err)
	}
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
func preArmNextFire(p orchUpdatePayload) (string, orchUpdatePayload, bool) {
	if idleDays := orchUpdateIdleDays(); p.idleReapDue(time.Now(), idleDays) {
		Log("[orchestrate/scheduled] session=%s reaped: idle > %d days — recurring task auto-cancelled", p.SessionID, idleDays)
		return "", p, false
	}
	armed := p
	armed.FireCount++
	if armed.FireCount >= armed.effectiveMaxFires() {
		Log("[orchestrate/scheduled] session=%s retiring: this fire reaches the fire cap %d (recurring task auto-cancelled after it)", p.SessionID, armed.effectiveMaxFires())
		return "", p, false
	}
	next, err := computeNextFire(&armed, time.Now().In(UserLocation(p.Username)))
	if err != nil {
		Log("[orchestrate/scheduled] cannot compute next fire for session %s: %v — stopping after this fire", p.SessionID, err)
		return "", p, false
	}
	id, err := ScheduleTask(OrchestrateScheduledUpdateKind, armed, next)
	if err != nil {
		Log("[orchestrate/scheduled] pre-arm failed for session %s: %v", p.SessionID, err)
		return "", p, false
	}
	return id, armed, true
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
