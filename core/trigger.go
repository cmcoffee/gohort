// ScheduledTrigger: the unified {when, gate, action, target} record that all of
// gohort's timer/condition surfaces converge onto. It generalizes the event
// monitor (change/rule gates, wake/notify delivery) and the phantom scheduler
// (timed callbacks, calendar/window timing, stop conditions) into one engine.
//
//   - WHEN: a webhook push, a one-shot RunAt, or a recurrence (interval / cron /
//     daily random window).
//   - GATE: "always" (fire every elapse), "change" (invoke a tool, hash output,
//     fire on change), or "rule" (a deterministic http threshold or an LLM
//     checker, edge-triggered).
//   - ACTION: "notify" (deliver the change/summary verbatim) or "callback" (run a
//     prompt as a fresh turn and deliver its reply).
//   - TARGET: an app-defined token (e.g. a channel session or a phantom chat).
//     core never interprets it — it hands the trigger to an app-registered
//     dispatcher, mirroring how the event monitor calls RegisterEventWaker.
//
// As with event_monitor.go, the STORE + SCHEDULE + LEDGER glue lives here in
// core (domain-agnostic); the app-aware halves (delivering the action, running
// an LLM stop-condition) are supplied via RegisterTriggerAction /
// RegisterStopConditionChecker. core owns only the lifecycle around them.
//
// Shared low-level helpers (sha256Sum, formatWatchAlert, fetchExtractURL,
// compareValues, eventMatch, diffLines, truncateEvent, capBody) are reused from
// event_monitor.go so the two engines detect changes identically.

package core

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

const (
	scheduledTriggersTable = "scheduled_triggers" // <owner>:<name> -> ScheduledTrigger
	triggerKind            = "scheduled.trigger"

	// Gate — what decides a fire when the timer elapses.
	GateAlways = "always" // no gate: fire unconditionally each elapse
	GateChange = "change" // invoke a tool, hash output, fire on change
	GateRule   = "rule"   // fire when a condition holds (edge-triggered)

	// RuleMode — how a GateRule condition is evaluated.
	RuleHTTP  = "http"  // fetch a URL, extract a value, compare to a threshold (no LLM)
	RuleCheck = "check" // run an LLM checker agent and match its answer

	// Action — what happens on a fire.
	ActionNotify   = "notify"   // deliver the change/summary verbatim (no LLM by default)
	ActionCallback = "callback" // run Prompt as a fresh turn and deliver the reply

	// minTriggerInterval floors recurring cadence so a misconfigured trigger
	// can't hammer its tool/checker. The LLM-facing tool validates tighter.
	minTriggerInterval = 30
)

// ScheduledTrigger is one unified standing trigger. Name is unique per owner.
type ScheduledTrigger struct {
	Name  string `json:"name"`
	Owner string `json:"owner"`

	// --- WHEN ---
	Push            bool      `json:"push,omitempty"`             // external POST fires it; no timer (orthogonal to Gate)
	Token           string    `json:"token,omitempty"`            // push secret (URL path segment)
	RunAt           time.Time `json:"run_at,omitempty"`           // absolute first/next fire (one-shot or seed)
	IntervalSeconds int       `json:"interval_seconds,omitempty"` // fixed-interval recurrence
	Cron            string    `json:"cron,omitempty"`             // NextCronOccurrence spec, e.g. "FRI 21:30"
	RandomWindow    string    `json:"random_window,omitempty"`    // "HH:MM-HH:MM" once-per-day random

	// --- GATE ---
	Gate string `json:"gate"` // GateAlways | GateChange | GateRule
	// gate=change
	ToolName     string         `json:"tool_name,omitempty"`
	ToolArgs     map[string]any `json:"tool_args,omitempty"`
	FormatScript string         `json:"format_script,omitempty"`
	// gate=rule
	RuleMode      string `json:"rule_mode,omitempty"` // RuleHTTP | RuleCheck
	CheckAgent    string `json:"check_agent,omitempty"`
	Check         string `json:"check,omitempty"`
	MatchContains string `json:"match_contains,omitempty"`
	URL           string `json:"url,omitempty"`
	JSONPath      string `json:"json_path,omitempty"`
	Regex         string `json:"regex,omitempty"`
	CompareOp     string `json:"compare_op,omitempty"`
	Threshold     string `json:"threshold,omitempty"`

	// --- ACTION ---
	Action    string `json:"action"`               // ActionNotify | ActionCallback
	Notify    string `json:"notify,omitempty"`     // EventNotifyChannel | Direct | Text (action=notify)
	WakeBrief string `json:"wake_brief,omitempty"` // guidance for the woken agent (action=notify, channel)
	Prompt    string `json:"prompt,omitempty"`     // directive run each fire (action=callback)

	// --- TARGET (app-interpreted; core never reads these) ---
	TargetKind string `json:"target_kind,omitempty"` // e.g. "channel_session" | "phantom_chat"
	TargetID   string `json:"target_id,omitempty"`   // session id / chat id
	TargetMeta string `json:"target_meta,omitempty"` // handle / agent id — opaque to core

	// --- REPEAT / STOP ---
	RepeatUntil string `json:"repeat_until,omitempty"` // LLM-evaluated stop condition
	RepeatCount int    `json:"repeat_count,omitempty"` // fires so far

	// --- edge-trigger state (gate=change / gate=rule only; always carries none) ---
	LastHash     string `json:"last_hash,omitempty"`
	LastBody     string `json:"last_body,omitempty"`
	LastMatched  bool   `json:"last_matched,omitempty"`
	LastBreached bool   `json:"last_breached,omitempty"`

	Paused      bool      `json:"paused"`
	Created     time.Time `json:"created"`
	NextRun     time.Time `json:"next_run,omitempty"`
	LastFired   time.Time `json:"last_fired,omitempty"`
	LastResult  string    `json:"last_result,omitempty"`
	SchedulerID string    `json:"scheduler_id,omitempty"`
}

// --- app-aware hooks ---------------------------------------------------------

// TriggerActionFunc delivers a fired trigger's action. Provided per TargetKind by
// the app: action=notify delivers `summary`; action=callback runs t.Prompt and
// delivers the reply. Mirrors RegisterEventWaker.
type TriggerActionFunc func(ctx context.Context, t ScheduledTrigger, summary string)

// StopConditionFunc evaluates a trigger's RepeatUntil after a fire and returns
// true to stop repeating. Provided by the app (it owns the LLM); core only owns
// when to call it. The closure fetches whatever context it needs from t.Target*.
type StopConditionFunc func(ctx context.Context, t ScheduledTrigger) bool

var (
	triggerActions     = map[string]TriggerActionFunc{}
	triggerStopChecker StopConditionFunc
	triggerMu          sync.RWMutex
	triggerStarted     bool
)

// RegisterTriggerAction installs the action dispatcher for a TargetKind. Call
// once at startup per app surface.
func RegisterTriggerAction(targetKind string, fn TriggerActionFunc) {
	triggerMu.Lock()
	triggerActions[targetKind] = fn
	triggerMu.Unlock()
}

// RegisterStopConditionChecker installs the LLM-backed stop-condition evaluator.
func RegisterStopConditionChecker(fn StopConditionFunc) {
	triggerMu.Lock()
	triggerStopChecker = fn
	triggerMu.Unlock()
}

// --- store -------------------------------------------------------------------

// SaveScheduledTrigger upserts a trigger definition.
func SaveScheduledTrigger(db Database, t ScheduledTrigger) {
	if db == nil || t.Owner == "" || t.Name == "" {
		return
	}
	db.Set(scheduledTriggersTable, eventKey(t.Owner, t.Name), t)
}

// GetScheduledTrigger fetches one definition, owner-scoped.
func GetScheduledTrigger(db Database, owner, name string) (ScheduledTrigger, bool) {
	if db == nil || owner == "" || name == "" {
		return ScheduledTrigger{}, false
	}
	var t ScheduledTrigger
	if !db.Get(scheduledTriggersTable, eventKey(owner, name), &t) {
		return ScheduledTrigger{}, false
	}
	return t, true
}

// ListScheduledTriggers returns the owner's triggers.
func ListScheduledTriggers(db Database, owner string) []ScheduledTrigger {
	if db == nil || owner == "" {
		return nil
	}
	prefix := owner + ":"
	var out []ScheduledTrigger
	for _, k := range db.Keys(scheduledTriggersTable) {
		if len(k) < len(prefix) || k[:len(prefix)] != prefix {
			continue
		}
		var t ScheduledTrigger
		if db.Get(scheduledTriggersTable, k, &t) {
			out = append(out, t)
		}
	}
	return out
}

// FindScheduledTriggerByToken resolves a push trigger from its token across all
// owners (the public endpoint has no session to scope by).
func FindScheduledTriggerByToken(db Database, token string) (ScheduledTrigger, bool) {
	if db == nil || token == "" {
		return ScheduledTrigger{}, false
	}
	for _, k := range db.Keys(scheduledTriggersTable) {
		var t ScheduledTrigger
		if db.Get(scheduledTriggersTable, k, &t) && t.Token == token {
			return t, true
		}
	}
	return ScheduledTrigger{}, false
}

// DeleteScheduledTrigger removes a definition and cancels its pending task.
func DeleteScheduledTrigger(db Database, owner, name string) {
	if db == nil {
		return
	}
	if t, ok := GetScheduledTrigger(db, owner, name); ok && t.SchedulerID != "" {
		UnscheduleTask(t.SchedulerID)
	}
	db.Unset(scheduledTriggersTable, eventKey(owner, name))
}

// --- schedule ----------------------------------------------------------------

type triggerPayload struct {
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

// nextTriggerRun computes the next fire after `from` from the WHEN fields, and
// whether the trigger recurs at all. A trigger with none of interval/cron/window
// is one-shot (recurring=false).
func nextTriggerRun(t ScheduledTrigger, from time.Time) (time.Time, bool) {
	switch {
	case t.Cron != "":
		n, err := NextCronOccurrence(t.Cron, from)
		if err != nil {
			return time.Time{}, false
		}
		return n, true
	case t.RandomWindow != "":
		n, err := NextRandomWindowTime(t.RandomWindow, from, 1, 0)
		if err != nil {
			return time.Time{}, false
		}
		return n, true
	case t.IntervalSeconds > 0:
		iv := t.IntervalSeconds
		if iv < minTriggerInterval {
			iv = minTriggerInterval
		}
		return from.Add(time.Duration(iv) * time.Second), true
	}
	return time.Time{}, false
}

// scheduleTriggerAt (re)schedules the trigger's next task (cancel-and-replace)
// and persists the new SchedulerID/NextRun.
func scheduleTriggerAt(db Database, t ScheduledTrigger, runAt time.Time) error {
	if t.SchedulerID != "" {
		UnscheduleTask(t.SchedulerID)
	}
	id, err := ScheduleTask(triggerKind, triggerPayload{Owner: t.Owner, Name: t.Name}, runAt)
	if err != nil {
		return err
	}
	t.SchedulerID = id
	t.NextRun = runAt
	SaveScheduledTrigger(db, t)
	return nil
}

// ScheduleTrigger schedules a trigger's first fire. Push triggers carry no timer.
// The first fire is RunAt when it's set and in the future, else the next
// recurrence. Returns an error if there is nothing to schedule.
func ScheduleTrigger(db Database, t ScheduledTrigger) error {
	if t.Push {
		return nil
	}
	var runAt time.Time
	switch {
	case !t.RunAt.IsZero() && t.RunAt.After(time.Now()):
		runAt = t.RunAt
	default:
		n, recurring := nextTriggerRun(t, time.Now())
		if !recurring {
			return fmt.Errorf("trigger %q has no schedule: set run_at, interval_seconds, cron, or random_window", t.Name)
		}
		runAt = n
	}
	return scheduleTriggerAt(db, t, runAt)
}

// RunTriggerNow fires a one-off check/callback immediately without disturbing the
// recurring cadence. Push triggers have nothing to poll.
func RunTriggerNow(db Database, owner, name string) error {
	t, ok := GetScheduledTrigger(db, owner, name)
	if !ok || t.Push {
		return nil
	}
	_, err := ScheduleTask(triggerKind, triggerPayload{Owner: owner, Name: name}, time.Now())
	return err
}

// --- run ---------------------------------------------------------------------

// StartTriggerScheduler registers the scheduled.trigger handler. Idempotent;
// call once at startup. Reads/writes RootDB so its view matches any console.
func StartTriggerScheduler() {
	triggerMu.Lock()
	if triggerStarted {
		triggerMu.Unlock()
		return
	}
	triggerStarted = true
	triggerMu.Unlock()

	RegisterScheduleHandler(triggerKind, func(ctx context.Context, raw json.RawMessage) {
		var p triggerPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			Log("[trigger] bad payload: %v", err)
			return
		}
		// Survive a mid-fire panic: a recurring trigger must not be lost because
		// one fire blew up. On panic, reschedule from the stored definition.
		defer func() {
			if r := recover(); r != nil {
				Log("[trigger] %s/%s handler panicked: %v — rescheduling to survive", p.Owner, p.Name, r)
				if cur, ok := GetScheduledTrigger(RootDB, p.Owner, p.Name); ok && !cur.Paused && !cur.Push {
					if next, recurring := nextTriggerRun(cur, time.Now()); recurring {
						_ = scheduleTriggerAt(RootDB, cur, next)
					}
				}
			}
		}()

		t, ok := GetScheduledTrigger(RootDB, p.Owner, p.Name)
		if !ok || t.Push {
			return
		}
		if !t.Paused {
			fire, summary := evaluateGate(ctx, RootDB, t)
			if fire {
				cur, ok := GetScheduledTrigger(RootDB, p.Owner, p.Name)
				if ok {
					cur.LastFired = time.Now()
					cur.RepeatCount++
					SaveScheduledTrigger(RootDB, cur)
					recordTriggerRun(RootDB, cur, summary)
					dispatchTriggerAction(ctx, cur, summary)
					if cur.RepeatUntil != "" && triggerStopMet(ctx, cur) {
						Log("[trigger] %s/%s stop condition met after %d fire(s) — not rescheduling",
							cur.Owner, cur.Name, cur.RepeatCount)
						cur.SchedulerID = ""
						cur.NextRun = time.Time{}
						SaveScheduledTrigger(RootDB, cur)
						return
					}
				}
			}
		}
		// Reschedule the recurring cadence (re-read in case it was paused/edited).
		if cur, ok := GetScheduledTrigger(RootDB, p.Owner, p.Name); ok && !cur.Paused && !cur.Push {
			if next, recurring := nextTriggerRun(cur, time.Now()); recurring {
				if err := scheduleTriggerAt(RootDB, cur, next); err != nil {
					Log("[trigger] reschedule failed for %s/%s: %v", p.Owner, p.Name, err)
				}
			} else {
				// One-shot: clear the consumed scheduler id.
				cur.SchedulerID = ""
				cur.NextRun = time.Time{}
				SaveScheduledTrigger(RootDB, cur)
			}
		}
	})
}

// evaluateGate decides whether this elapse fires and produces the alert summary.
// It owns the gate-specific edge-trigger bookkeeping (hash/breach/match state),
// saving those advances itself; the handler owns LastFired/RepeatCount/reschedule.
func evaluateGate(ctx context.Context, db Database, t ScheduledTrigger) (fire bool, summary string) {
	switch t.Gate {
	case GateChange:
		body, err := InvokeWatchTool(t.Owner, "", t.ToolName, t.ToolArgs)
		if err != nil {
			Log("[trigger] change %s/%s tool %q failed: %v", t.Owner, t.Name, t.ToolName, err)
			return false, ""
		}
		hash := sha256Sum(body)
		cur, ok := GetScheduledTrigger(db, t.Owner, t.Name)
		if !ok {
			return false, ""
		}
		if hash == cur.LastHash {
			return false, "" // no change — stay quiet
		}
		firstObservation := cur.LastHash == ""
		prior := cur.LastBody
		cur.LastHash = hash
		cur.LastResult = truncateEvent(body, 400)
		cur.LastBody = capBody(body)
		if firstObservation {
			SaveScheduledTrigger(db, cur) // baseline only — don't fire on first poll
			return false, ""
		}
		s, suppress := formatWatchAlert(ctx, cur.Owner, cur.Name, cur.FormatScript, prior, body)
		SaveScheduledTrigger(db, cur) // advance baseline regardless
		if suppress {
			return false, "" // format_script printed nothing — intentional skip
		}
		return true, s

	case GateRule:
		if t.RuleMode == RuleHTTP {
			val, err := fetchExtractURL(ctx, t.URL, t.JSONPath, t.Regex)
			if err != nil {
				Log("[trigger] rule(http) %s/%s fetch failed: %v", t.Owner, t.Name, err)
				return false, ""
			}
			breached, cerr := compareValues(val, t.CompareOp, t.Threshold)
			if cerr != nil {
				Log("[trigger] rule(http) %s/%s compare failed: %v", t.Owner, t.Name, cerr)
				return false, ""
			}
			cur, ok := GetScheduledTrigger(db, t.Owner, t.Name)
			if !ok {
				return false, ""
			}
			switch {
			case breached && !cur.LastBreached:
				cur.LastBreached = true
				cur.LastResult = val
				SaveScheduledTrigger(db, cur)
				return true, fmt.Sprintf("Trigger %q tripped: observed %s %s %s (from %s).",
					cur.Name, val, cur.CompareOp, cur.Threshold, cur.URL)
			case !breached && cur.LastBreached:
				cur.LastBreached = false // recovered — re-arm, no fire
				cur.LastResult = val
				SaveScheduledTrigger(db, cur)
			}
			return false, ""
		}
		// RuleCheck: run the LLM checker via the registered event poller.
		eventMu.RLock()
		poller := eventPoller
		eventMu.RUnlock()
		if poller == nil {
			Log("[trigger] rule(check) %s/%s fired but no poller is registered", t.Owner, t.Name)
			return false, ""
		}
		answer, err := poller(ctx, t.Owner, t.CheckAgent, t.Check)
		if err != nil {
			Log("[trigger] rule(check) %s/%s check failed: %v", t.Owner, t.Name, err)
			return false, ""
		}
		matched := eventMatch(answer, t.MatchContains)
		cur, ok := GetScheduledTrigger(db, t.Owner, t.Name)
		if !ok {
			return false, ""
		}
		switch {
		case matched && !cur.LastMatched:
			cur.LastMatched = true
			cur.LastResult = answer
			SaveScheduledTrigger(db, cur)
			return true, answer
		case !matched && cur.LastMatched:
			cur.LastMatched = false // condition cleared — re-arm, no fire
			cur.LastResult = answer
			SaveScheduledTrigger(db, cur)
		}
		return false, ""

	default: // GateAlways — no gate; the action (callback prompt / notify brief) carries the payload.
		return true, ""
	}
}

// FireScheduledTrigger fires a push trigger for an external POST. Public so the
// webhook HTTP handler can call it.
func FireScheduledTrigger(ctx context.Context, db Database, t ScheduledTrigger, summary string) {
	if cur, ok := GetScheduledTrigger(db, t.Owner, t.Name); ok {
		cur.LastFired = time.Now()
		cur.RepeatCount++
		SaveScheduledTrigger(db, cur)
		t = cur
	}
	recordTriggerRun(db, t, summary)
	dispatchTriggerAction(ctx, t, summary)
}

func dispatchTriggerAction(ctx context.Context, t ScheduledTrigger, summary string) {
	triggerMu.RLock()
	fn := triggerActions[t.TargetKind]
	triggerMu.RUnlock()
	if fn == nil {
		Log("[trigger] %s/%s fired but no action dispatcher for target_kind %q", t.Owner, t.Name, t.TargetKind)
		return
	}
	fn(ctx, t, summary)
}

func triggerStopMet(ctx context.Context, t ScheduledTrigger) bool {
	triggerMu.RLock()
	chk := triggerStopChecker
	triggerMu.RUnlock()
	if chk == nil {
		return false
	}
	return chk(ctx, t)
}

// recordTriggerRun logs a fire to the run-ledger so it shows in the Activity feed.
func recordTriggerRun(db Database, t ScheduledTrigger, summary string) {
	desc := summary
	if desc == "" {
		desc = t.Prompt
	}
	RecordRun(db, RunRecord{
		Owner:   t.Owner,
		Agent:   t.Name,
		Trigger: "trigger:" + t.Gate,
		Brief:   desc,
		Started: time.Now(),
		Ended:   time.Now(),
		Status:  RunOK,
		Summary: "Trigger fired: " + truncateEvent(desc, 200),
	})
}
