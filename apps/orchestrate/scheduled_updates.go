// Scheduled orchestrate updates — the LLM can use schedule_recurring to
// set up a recurring task that posts back into the user's current
// session (e.g. "every 30 minutes, check the build status and post if
// it's red"). Same shape as apps/chat/scheduled_updates.go but
// scoped per-(user, agent) so each fire runs against the correct
// agent's persona / tools / memory.
//
// On fire:
//   1. Load the user's session under the per-(user, agent) sub-store.
//   2. Build messages from the session's history + a synthetic
//      "[SCHEDULED UPDATE — fire N]" user turn.
//   3. Run a worker-tier RunAgentLoop with the target agent's
//      orchestrator_prompt + memory + facts + allowed tools.
//   4. Append the model's reply as an assistant turn in the session.
//   5. Reschedule for the next interval, unless the task was
//      cancelled or hit the fire cap.
//
// Guardrails (matched to chat's):
//   - Min interval 60s
//   - Max 5 active updates per session
//   - Max 50 fires per task before auto-cancel

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

func init() {
	RegisterTunable(TunableSpec{Key: "tune_orch_update_min_interval", Category: "Timeouts", Label: "Scheduled update min interval", Help: "Minimum interval allowed for a recurring orchestrate update.", Kind: KindSeconds, Default: 60, Min: 10, Max: 3600})
	RegisterTunable(TunableSpec{Key: "tune_orch_update_max_per_session", Category: "Limits", Label: "Scheduled updates per session", Help: "Max active recurring updates a single session may hold.", Kind: KindInt, Default: 5, Min: 1, Max: 50})
	RegisterTunable(TunableSpec{Key: "tune_orch_update_max_fires", Category: "Limits", Label: "Scheduled update fire cap", Help: "Max times a recurring update fires before auto-cancel.", Kind: KindInt, Default: 50, Min: 5, Max: 1000})
}

// orchUpdateMinInterval is the floor on a recurring update's interval.
func orchUpdateMinInterval() time.Duration { return TuneDuration("tune_orch_update_min_interval") }

// orchUpdateMaxPerSession caps active recurring updates per session.
func orchUpdateMaxPerSession() int { return TuneInt("tune_orch_update_max_per_session") }

// orchUpdateMaxFires caps how many times a recurring update fires.
func orchUpdateMaxFires() int { return TuneInt("tune_orch_update_max_fires") }

type orchUpdatePayload struct {
	SessionID       string `json:"session_id"`
	AgentID         string `json:"agent_id"`
	Username        string `json:"username"`
	Prompt          string `json:"prompt"`
	IntervalSeconds int    `json:"interval_seconds"`
	FireCount       int    `json:"fire_count"`
	CreatedAt       string `json:"created_at"`

	// Pattern modifiers (empty Pattern == fixed, the original every-N-minutes
	// behavior). See recurring_pattern.go for the scheduling math.
	Pattern       string `json:"pattern,omitempty"`         // "" | "fixed" | "random"
	TimesPerDay   int    `json:"times_per_day,omitempty"`   // random: fires per active window
	MinGapSeconds int    `json:"min_gap_seconds,omitempty"` // random: minimum spacing between fires
	MaxGapSeconds int    `json:"max_gap_seconds,omitempty"` // random (continuous): maximum spacing
	HasWindow     bool   `json:"has_window,omitempty"`      // whether the daily window applies
	WindowFromMin int    `json:"window_from_min,omitempty"` // window start, minutes since local midnight
	WindowToMin   int    `json:"window_to_min,omitempty"`   // window end, minutes since local midnight
	MaxFires      int    `json:"max_fires,omitempty"`       // per-task total cap; 0 = deployment default
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
		return fmt.Sprintf("%s — %s (agent: %s)", recurringDetail(p), firstLineLabel(p.Prompt), agent)
	})
}

// recurringTaskRow pairs a recurring update's scheduler task id (the cancel
// key, needed for delete URLs) with its decoded payload.
type recurringTaskRow struct {
	TaskID  string
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
		out = append(out, recurringTaskRow{TaskID: task.ID, Payload: p})
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

// handleOrchestrateScheduledUpdate is the scheduler callback. Loads
// the session, runs the agent loop, appends the reply, reschedules.
func handleOrchestrateScheduledUpdate(ctx context.Context, raw json.RawMessage) {
	var p orchUpdatePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		Log("[orchestrate/scheduled] payload unmarshal failed: %v", err)
		return
	}
	// A fire that panics must NOT silently kill the recurring chain: the
	// scheduler already removed this task from the queue before calling us, so
	// without this the schedule would be gone forever. Recover and re-arm the
	// next tick (mirrors the event monitor's always-reschedule guard). Only
	// fires on panic — the normal error/empty/success paths reschedule
	// explicitly, and the intentional "drop" returns (agent/session gone) must
	// stay stopped.
	defer func() {
		if r := recover(); r != nil {
			Log("[orchestrate/scheduled] fire panicked for session %s: %v — rescheduling", p.SessionID, r)
			reschedule(p)
		}
	}()
	orchRefMu.Lock()
	app := orchRef
	orchRefMu.Unlock()
	if app == nil {
		Log("[orchestrate/scheduled] not initialized, dropping task for session %s", p.SessionID)
		return
	}
	if p.FireCount >= p.effectiveMaxFires() {
		Log("[orchestrate/scheduled] task %s reached %d fires, auto-cancelling", p.SessionID, p.effectiveMaxFires())
		return
	}

	udb := UserDB(app.DB, p.Username)
	if udb == nil {
		Log("[orchestrate/scheduled] no udb for user %s, dropping task", p.Username)
		return
	}
	agent, ok := loadAgent(udb, p.AgentID)
	if !ok {
		Log("[orchestrate/scheduled] agent %s missing for user %s, dropping task", p.AgentID, p.Username)
		return
	}
	sess, ok := loadChatSession(udb, p.AgentID, p.SessionID)
	if !ok {
		Log("[orchestrate/scheduled] session %s missing, dropping task", p.SessionID)
		return
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
			"[SCHEDULED UPDATE — fire %d, %s] %s",
			p.FireCount+1, recurringDetail(p), p.Prompt),
	})

	// Build system prompt the same way runPlan would for this agent:
	// gated persona + facts. facts() is a chatTurn method we can't
	// use here (no chatTurn for a scheduled fire), so call the
	// underlying helper directly.
	facts := ListMemoryFacts(udb, factsNamespace(agent.ID))
	sysPrompt := prependAgentContext(agent.OrchestratorPrompt, agent, facts, agentOperatingNotes(udb, agent))
	sysPrompt = StripPromptSectionsForTools(sysPrompt, nil) // no allowlist gate for scheduled

	// Resolve the agent's tool set the same way runWorkerStep does.
	toolNames := agent.AllowedTools
	if len(toolNames) == 0 {
		for _, td := range RegisteredChatTools() {
			toolNames = append(toolNames, td.Name())
		}
	}
	tools, err := GetAgentTools(toolNames...)
	if err != nil {
		tools = nil
		for _, n := range toolNames {
			if td, terr := GetAgentTools(n); terr == nil && len(td) > 0 {
				tools = append(tools, td[0])
			}
		}
	}

	f := false
	resp, _, runErr := app.RunAgentLoop(ctx, msgs, AgentLoopConfig{
		SystemPrompt: sysPrompt,
		Tools:        tools,
		MaxRounds:    resolveMaxWorkerRounds(agent),
		Confirm:      func(name, args string) bool { return true },
		ChatOptions: []ChatOption{
			WithRouteKey("app.orchestrate.worker"),
			WithThink(f),
		},
	})
	if runErr != nil {
		Log("[orchestrate/scheduled] LLM error for session %s: %v", p.SessionID, runErr)
		reschedule(p)
		return
	}
	reply := ""
	if resp != nil {
		reply = strings.TrimSpace(resp.Content)
	}
	if reply == "" {
		Log("[orchestrate/scheduled] empty reply for session %s, skipping append", p.SessionID)
		reschedule(p)
		return
	}

	sess.Messages = append(sess.Messages, ChatMessage{
		Role:    "assistant",
		Content: reply,
		Created: time.Now(),
	})
	sess.LastAt = time.Now()
	if _, err := saveChatSession(udb, sess); err != nil {
		Log("[orchestrate/scheduled] save failed for session %s: %v", p.SessionID, err)
	}
	Log("[orchestrate/scheduled] posted update to agent=%s session=%s (fire %d, %d chars)",
		agent.ID, p.SessionID, p.FireCount+1, len(reply))

	reschedule(p)
}

// reschedule emits the next fire of a recurring orchestrate update. The next
// time — and, for the random pattern, the mutation of p.RemainingToday — is
// computed by computeNextFire so the fixed/random branch lives in one place.
func reschedule(p orchUpdatePayload) {
	p.FireCount++
	if p.FireCount >= p.effectiveMaxFires() {
		return
	}
	next, err := computeNextFire(&p, time.Now())
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
		Pattern:         spec.Pattern,
		IntervalSeconds: spec.IntervalSeconds,
		TimesPerDay:     spec.TimesPerDay,
		MinGapSeconds:   spec.MinGapSeconds,
		MaxGapSeconds:   spec.MaxGapSeconds,
		HasWindow:       spec.HasWindow,
		WindowFromMin:   spec.WindowFromMin,
		WindowToMin:     spec.WindowToMin,
		MaxFires:        spec.MaxFires,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
	}
	next, err := computeNextFire(&p, time.Now())
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
