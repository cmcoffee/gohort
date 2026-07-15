// Event monitors: standing triggers that WAKE the Operator when something
// happens, as opposed to standing agents (which RUN on a wall-clock schedule).
// Two kinds:
//
//   - "webhook": mints a tokenized URL an external system POSTs to. Each POST
//     wakes the Operator with the posted summary. This is the TeamSpeak model —
//     an outside watcher notices an event and pokes gohort.
//   - "poll": runs a checker agent on an interval; when the checker's answer
//     contains the match string, the Operator is woken with that answer. This
//     is "watch X and tell me when Y" with no external integration needed.
//
// Split mirrors standing_agent.go: the STORE + SCHEDULE + LEDGER glue live here
// in core (domain-agnostic); the agent-aware halves — actually waking the
// Operator and running a checker agent — are supplied by orchestrate via
// RegisterEventWaker / RegisterEventPoller. core can't import AgentRecord, so
// it calls the registered closures and only owns the lifecycle around them.

package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// EventPollKind is the scheduler task kind for interval poll monitors. Exported
// so a task-describer (admin scheduler view / scheduler logs) can key on it
// without duplicating the literal.
const EventPollKind = "event.poll"

const (
	eventMonitorsTable = "event_monitors" // <owner>:<name> -> EventMonitor
	eventPollKind      = EventPollKind

	// The monitor kinds, cheapest-first:
	//   webhook   = external POST wakes it (push, no polling).
	//   http_poll = fetch a URL, extract a value, compare to a threshold. No
	//               LLM — deterministic; best for numeric/value conditions.
	//   watch     = invoke a captured TOOL each interval, hash its output, and
	//               wake ONLY when the hash changes. No LLM until something
	//               changes; the cheap "tell me when X changes" path.
	//   poll      = run an LLM checker agent every interval. Most expensive;
	//               reserve for FUZZY conditions a value/hash can't capture.
	EventKindWebhook = "webhook"
	EventKindPoll    = "poll"
	EventKindHTTP    = "http_poll"
	EventKindWatch   = "watch"

	// Notify modes — where a fire is delivered.
	//   channel (default): wake the channel agent in its home thread (an LLM
	//                      turn reacts there).
	//   direct:            post the change verbatim into the channel home
	//                      thread, NO LLM — it just shows up in the thread (and
	//                      lights the unread dot). Cheapest in-app delivery.
	//   text:              deliver the event straight to the owner's phone via
	//                      the phantom bridge, no LLM.
	EventNotifyChannel = "channel"
	// direct: post the change verbatim, no LLM, WHERE the watcher was created —
	// into its phantom conversation (DeliverChatID, e.g. a group) when set, else
	// the Agency channel thread. Origin-aware, not hardcoded to one surface.
	EventNotifyDirect = "direct"
	EventNotifyText   = "text"

	// minPollInterval floors the poll cadence so a misconfigured monitor can't
	// hammer the checker agent.
	minPollInterval = 30
)

// EventMonitor is one standing trigger. Name is unique per owner.
type EventMonitor struct {
	Name        string `json:"name"`
	Owner       string `json:"owner"`
	Kind        string `json:"kind"`                   // EventKindWebhook | EventKindPoll
	WakeBrief   string `json:"wake_brief"`             // guidance handed to the woken agent on each wake
	WakeAgent   string `json:"wake_agent,omitempty"`   // channel agent woken when this fires; empty = deployment default channel agent
	WakeSession string `json:"wake_session,omitempty"` // session the monitor was created in; the wake lands here. Empty = the agent's channel home thread
	Notify      string `json:"notify,omitempty"`       // delivery: EventNotifyChannel (default) wakes the agent in-thread; EventNotifyText texts the owner (no LLM)
	Token       string `json:"token,omitempty"`        // webhook secret (URL path segment)

	// DeliverChatID is the phantom conversation a notify=direct alert posts into
	// (the chat the watcher was created in — e.g. a group). Empty for a monitor
	// created in the Agency console, where direct posts to the channel thread.
	DeliverChatID string `json:"deliver_chat_id,omitempty"`

	// poll kind
	CheckAgent      string `json:"check_agent,omitempty"`    // agent run each interval to check the condition
	Check           string `json:"check,omitempty"`          // the brief/question given to the checker
	MatchContains   string `json:"match_contains,omitempty"` // fire when the answer contains this (default "YES")
	IntervalSeconds int    `json:"interval_seconds,omitempty"`

	// http_poll kind
	URL       string `json:"url,omitempty"`        // endpoint fetched each interval
	JSONPath  string `json:"json_path,omitempty"`  // dotted path into the JSON response (arrays by index), e.g. "result.0.price"
	Regex     string `json:"regex,omitempty"`      // alternative extraction: first capture group (or whole match)
	CompareOp string `json:"compare_op,omitempty"` // < > <= >= == != contains
	Threshold string `json:"threshold,omitempty"`  // value compared against the extracted one

	// watch kind — capture a tool call, hash its output, wake on change.
	ToolName string         `json:"tool_name,omitempty"` // tool invoked each interval; its output is hashed
	ToolArgs map[string]any `json:"tool_args,omitempty"` // args passed to the tool every invocation
	LastHash string         `json:"last_hash,omitempty"` // sha256 of the last output (the change baseline)
	LastBody string         `json:"last_body,omitempty"` // prior output (capped) — diffed against the new output to show WHAT changed
	// FormatScript (watch kind, optional) is sandboxed python that shapes the
	// alert. It receives {"prior":...,"current":...} JSON on stdin and prints
	// the notification text to stdout — empty stdout means "this change isn't
	// worth alerting" (suppress). No network, no LLM. Empty = use the built-in
	// diff summary.
	FormatScript string `json:"format_script,omitempty"`

	// MatchNew (watch kind, optional) scopes the fire: when set, a change only
	// wakes if the NEWLY-added output contains this substring (case-insensitive);
	// otherwise the baseline advances silently. For an await on a chat this is the
	// sender's name, so a busy group only wakes on THAT person's reply, not on
	// every message (including the agent's own outbound). Empty = fire on any
	// change. Generic: any watched tool whose output labels the thing you care
	// about can be scoped this way.
	MatchNew string `json:"match_new,omitempty"`

	// OneShot monitors fire exactly ONCE and then remove themselves (see
	// fireWake). This is the substrate for "await a deferred result": the caller
	// asked to be woken WHEN a result arrives (a reply, a call outcome, a job
	// finishing), not on every subsequent change, so after the first wake the
	// monitor is done. Standing monitors leave this false and keep watching.
	OneShot      bool      `json:"one_shot,omitempty"`
	Paused       bool      `json:"paused"`
	Created      time.Time `json:"created"`
	NextCheck    time.Time `json:"next_check,omitempty"`
	LastFired    time.Time `json:"last_fired,omitempty"`
	LastChecked  time.Time `json:"last_checked,omitempty"`  // last time the poll ran (every interval) — proves liveness even with no change
	LastResult   string    `json:"last_result,omitempty"`   // last answer/value seen (poll debounce / http display)
	LastBreached bool      `json:"last_breached,omitempty"` // http_poll edge-trigger: was the condition met last check
	LastMatched  bool      `json:"last_matched,omitempty"`  // poll edge-trigger: did the checker answer match last check
	SchedulerID  string    `json:"scheduler_id,omitempty"`
}

// WakeFunc wakes the Operator with an event. Provided by orchestrate; it injects
// the event into the Operator's ongoing thread and runs a turn so it reacts.
type WakeFunc func(ctx context.Context, owner, monitorName, eventSummary string)

// PollCheckFunc runs a checker agent against a brief and returns its answer.
// Provided by orchestrate.
type PollCheckFunc func(ctx context.Context, owner, agentID, check string) (string, error)

var (
	eventWaker   WakeFunc
	eventPoller  PollCheckFunc
	eventMu      sync.RWMutex
	eventStarted bool
)

// RegisterEventWaker installs the Operator-wake closure. Call once at startup.
func RegisterEventWaker(fn WakeFunc) {
	eventMu.Lock()
	eventWaker = fn
	eventMu.Unlock()
}

// RegisterEventPoller installs the checker-agent closure. Call once at startup.
func RegisterEventPoller(fn PollCheckFunc) {
	eventMu.Lock()
	eventPoller = fn
	eventMu.Unlock()
}

// --- duplicate detection (soft warning at creation) --------------------------

// monitorSignature returns a (source, delivery) pair identifying WHAT a monitor
// watches and WHERE its alert lands. Two monitors with the same pair fire on the
// same event and deliver to the same place, so a second one just doubles the
// notification (the cross-agent case: two agents each watching the same feed
// into the same chat). Deliberately keyed on the WATCHED thing + the RESOLVED
// destination, NOT the agent — for notify=direct/text the agent is irrelevant to
// where the message goes. Webhook monitors have no pollable source (an external
// POST drives them), so they never match. ok=false means "not comparable".
func monitorSignature(m EventMonitor) (source, delivery string, ok bool) {
	switch m.Kind {
	case EventKindWatch:
		args, _ := json.Marshal(m.ToolArgs) // encoding/json sorts map keys → stable
		source = "watch:" + m.ToolName + ":" + string(args)
	case EventKindHTTP:
		source = "http:" + m.URL + "|" + m.JSONPath + "|" + m.Regex + "|" + m.CompareOp + "|" + m.Threshold
	case EventKindPoll:
		source = "poll:" + m.CheckAgent + ":" + strings.TrimSpace(m.Check)
	default: // webhook / unknown — no comparable polling source
		return "", "", false
	}
	switch m.Notify {
	case EventNotifyText:
		delivery = "text" // the owner's phone — same target regardless of agent
	case EventNotifyDirect:
		delivery = "direct:" + m.DeliverChatID
	default: // channel (and the empty default): wakes an agent in a thread
		delivery = "channel:" + m.WakeAgent + ":" + m.WakeSession
	}
	return source, delivery, true
}

// FindDuplicateMonitors returns the names of the owner's EXISTING monitors that
// watch the same source AND deliver to the same place as candidate m (m itself
// excluded by name). Empty when none. This powers a soft WARNING at creation —
// never a block: a monitor watching the same source with a different intent is
// legitimate, so the caller informs and proceeds.
func FindDuplicateMonitors(db Database, m EventMonitor) []string {
	src, del, ok := monitorSignature(m)
	if !ok || db == nil {
		return nil
	}
	var dups []string
	for _, k := range db.Keys(eventMonitorsTable) {
		var e EventMonitor
		if !db.Get(eventMonitorsTable, k, &e) {
			continue
		}
		if e.Owner != m.Owner || e.Name == m.Name {
			continue
		}
		if esrc, edel, eok := monitorSignature(e); eok && esrc == src && edel == del {
			dups = append(dups, e.Name)
		}
	}
	sort.Strings(dups)
	return dups
}

// --- store -------------------------------------------------------------------

func eventKey(owner, name string) string { return owner + ":" + name }

// NewEventToken mints a webhook secret (the only thing guarding the public
// /api/operator/event/<token> endpoint, so it must be unguessable).
func NewEventToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// SaveEventMonitor upserts a monitor definition.
func SaveEventMonitor(db Database, m EventMonitor) {
	if db == nil || m.Owner == "" || m.Name == "" {
		return
	}
	db.Set(eventMonitorsTable, eventKey(m.Owner, m.Name), m)
}

// GetEventMonitor fetches one definition, owner-scoped.
func GetEventMonitor(db Database, owner, name string) (EventMonitor, bool) {
	if db == nil || owner == "" || name == "" {
		return EventMonitor{}, false
	}
	var m EventMonitor
	if !db.Get(eventMonitorsTable, eventKey(owner, name), &m) {
		return EventMonitor{}, false
	}
	return m, true
}

// ListEventMonitors returns the owner's monitors.
func ListEventMonitors(db Database, owner string) []EventMonitor {
	if db == nil || owner == "" {
		return nil
	}
	prefix := owner + ":"
	var out []EventMonitor
	for _, k := range db.Keys(eventMonitorsTable) {
		if len(k) < len(prefix) || k[:len(prefix)] != prefix {
			continue
		}
		var m EventMonitor
		if db.Get(eventMonitorsTable, k, &m) {
			out = append(out, m)
		}
	}
	return out
}

// FindEventMonitorByToken resolves a webhook monitor from its token across all
// owners (the public endpoint has no session to scope by). The token is a long
// random secret, so a scan match is the authorization.
func FindEventMonitorByToken(db Database, token string) (EventMonitor, bool) {
	if db == nil || strings.TrimSpace(token) == "" {
		return EventMonitor{}, false
	}
	for _, k := range db.Keys(eventMonitorsTable) {
		var m EventMonitor
		if db.Get(eventMonitorsTable, k, &m) && m.Token == token {
			return m, true
		}
	}
	return EventMonitor{}, false
}

// DeleteEventMonitor removes a definition and cancels its poll task.
func DeleteEventMonitor(db Database, owner, name string) {
	if db == nil {
		return
	}
	if m, ok := GetEventMonitor(db, owner, name); ok && m.SchedulerID != "" {
		UnscheduleTask(m.SchedulerID)
	}
	db.Unset(eventMonitorsTable, eventKey(owner, name))
}

// --- poll match --------------------------------------------------------------

// eventMatch reports whether a checker answer should fire the monitor: it fires
// when the answer contains the match string (default "YES"), case-insensitive.
func eventMatch(answer, match string) bool {
	want := strings.TrimSpace(match)
	if want == "" {
		want = "YES"
	}
	return strings.Contains(strings.ToUpper(answer), strings.ToUpper(want))
}

// isScheduledKind reports whether a monitor runs on the interval scheduler
// (poll, http_poll, and watch do; webhook is push-only).
func isScheduledKind(kind string) bool {
	return kind == EventKindPoll || kind == EventKindHTTP || kind == EventKindWatch
}

func nextPoll(m EventMonitor, from time.Time) time.Time {
	iv := m.IntervalSeconds
	if iv < minPollInterval {
		iv = minPollInterval
	}
	return from.Add(time.Duration(iv) * time.Second)
}

// --- schedule + run ----------------------------------------------------------

type eventPollPayload struct {
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

// EventMonitorForTaskPayload decodes an event.poll scheduler payload and returns
// the monitor it points at, so a task-describer can label the task by monitor
// name + wake agent without knowing the payload shape. ok=false for a malformed
// payload or a monitor that no longer exists (a stale task about to no-op).
func EventMonitorForTaskPayload(payload json.RawMessage) (EventMonitor, bool) {
	var p eventPollPayload
	if json.Unmarshal(payload, &p) != nil || p.Name == "" {
		return EventMonitor{}, false
	}
	return GetEventMonitor(RootDB, p.Owner, p.Name)
}

// ScheduleEventMonitor (re)schedules a poll monitor's next check (cancel-and-
// replace). No-op for webhook monitors (they have no timer).
func ScheduleEventMonitor(db Database, m EventMonitor) error {
	if !isScheduledKind(m.Kind) {
		return nil
	}
	if m.SchedulerID != "" {
		UnscheduleTask(m.SchedulerID)
	}
	next := nextPoll(m, time.Now())
	id, err := ScheduleTask(eventPollKind, eventPollPayload{Owner: m.Owner, Name: m.Name}, next)
	if err != nil {
		return err
	}
	m.SchedulerID = id
	m.NextCheck = next
	SaveEventMonitor(db, m)
	return nil
}

// eventRearmGrace is how stale a NextCheck must be before the reconciler treats
// a taskless active monitor as stranded rather than mid-tick or mid-create. A
// poll's LLM checker can run for a minute or two; this stays comfortably above
// that (a tick holds no queued task while it runs) and below any real cadence.
const eventRearmGrace = 15 * time.Minute

// RearmStrandedEventMonitors reschedules active (non-paused) scheduled monitors
// that have no live poll task — the "tick fired, but the re-arm never
// completed" state that freezes NextCheck and silently kills the monitor. It is
// the ongoing (reconciler) counterpart to the one-shot boot self-heal in
// StartEventMonitorScheduler, and mirrors RearmStrandedStandingAgents: it
// re-arms ONLY stranded monitors, leaving healthy ones (which already hold a
// live task) on their own cadence. Missed checks are not backfilled; each is
// rescheduled from now. Returns the count revived.
func RearmStrandedEventMonitors(db Database) int {
	if db == nil {
		return 0
	}
	live := map[string]bool{}
	for _, t := range ListScheduledTasks(eventPollKind) {
		var p eventPollPayload
		if json.Unmarshal(t.Payload, &p) == nil {
			live[p.Owner+":"+p.Name] = true
		}
	}
	now := time.Now()
	revived := 0
	for _, k := range db.Keys(eventMonitorsTable) {
		var m EventMonitor
		if !db.Get(eventMonitorsTable, k, &m) {
			continue
		}
		if m.Paused || !isScheduledKind(m.Kind) || live[m.Owner+":"+m.Name] {
			continue
		}
		// Future or only-just-passed NextCheck: leave it to the normal path so
		// we never cancel an imminent legitimate tick.
		if !m.NextCheck.IsZero() && !m.NextCheck.Before(now.Add(-eventRearmGrace)) {
			continue
		}
		frozen := m.NextCheck
		if err := ScheduleEventMonitor(db, m); err != nil {
			Log("[event] re-arm failed for %s/%s: %v", m.Owner, m.Name, err)
			continue
		}
		revived++
		Log("[event] re-armed stranded monitor %s/%s (NextCheck was frozen at %s)",
			m.Owner, m.Name, frozen.Format(time.RFC3339))
	}
	if revived > 0 {
		Log("[event] reconciler revived %d stranded monitor(s)", revived)
	}
	return revived
}

// RunEventMonitorNow fires a one-off poll check immediately (async), without
// disturbing the recurring cadence. Webhook monitors have nothing to poll.
func RunEventMonitorNow(db Database, owner, name string) error {
	m, ok := GetEventMonitor(db, owner, name)
	if !ok || !isScheduledKind(m.Kind) {
		return nil
	}
	_, err := ScheduleTask(eventPollKind, eventPollPayload{Owner: owner, Name: name}, time.Now())
	return err
}

// StartEventMonitorScheduler registers the event.poll handler. Idempotent;
// call once at startup. Reads/writes RootDB so its view matches the console's.
func StartEventMonitorScheduler() {
	eventMu.Lock()
	if eventStarted {
		eventMu.Unlock()
		return
	}
	eventStarted = true
	eventMu.Unlock()

	// Ongoing self-heal, symmetric with standing agents: the boot pass below
	// revives stranded monitors once at startup, but a monitor that strands
	// while running (a tick that dies before its deferred re-arm) would sit
	// dead until the next restart. This reconciler runs at scheduler start AND
	// every 30 minutes, re-arming only genuinely stranded monitors so healthy
	// ones keep their own cadence untouched.
	RegisterReconciler("event_monitor_rearm", func(ctx context.Context) error {
		RearmStrandedEventMonitors(RootDB)
		return nil
	})

	RegisterScheduleHandler(eventPollKind, func(ctx context.Context, raw json.RawMessage) {
		var p eventPollPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			Log("[event] bad poll payload: %v", err)
			return
		}
		m, ok := GetEventMonitor(RootDB, p.Owner, p.Name)
		if !ok || !isScheduledKind(m.Kind) {
			return
		}
		// ALWAYS re-arm the next tick — even if this check panics or errors. The
		// recurring run is a self-rescheduling chain: each tick schedules the next.
		// Before this, a single panicking tick (e.g. an external tool call blowing
		// up) died BEFORE the reschedule and left the monitor "active" but dead
		// forever, needing a manual pause/resume to revive. Deferred so it runs no
		// matter how the check exits; recover() stops one bad tick from taking down
		// the scheduler goroutine; the re-read honors a pause/edit/delete that
		// happened during the check.
		defer func() {
			if r := recover(); r != nil {
				Log("[event] monitor %s/%s check PANICKED: %v — re-arming next tick anyway", p.Owner, p.Name, r)
			}
			if cur, ok := GetEventMonitor(RootDB, p.Owner, p.Name); ok && !cur.Paused && isScheduledKind(cur.Kind) {
				if err := ScheduleEventMonitor(RootDB, cur); err != nil {
					Log("[event] reschedule failed for %s/%s: %v", p.Owner, p.Name, err)
				}
			}
		}()
		if !m.Paused {
			// Record liveness every poll (even when nothing changes) so a
			// healthy-but-quiet monitor is visibly alive in the console. Saved
			// before execute* re-reads, so its own save preserves it.
			m.LastChecked = time.Now()
			SaveEventMonitor(RootDB, m)
			switch m.Kind {
			case EventKindPoll:
				executeEventPoll(ctx, RootDB, m)
			case EventKindHTTP:
				executeHTTPPoll(ctx, RootDB, m)
			case EventKindWatch:
				executeWatchPoll(ctx, RootDB, m)
			}
		}
	})

	// Boot self-heal: re-arm every active scheduled monitor across all owners.
	// Scheduler tasks persist across restarts, so a HEALTHY chain resumes on its
	// own after a restart — but a chain that a prior panicking tick left broken
	// has no next task and would stay dead. Re-arming here revives those (it's
	// what a manual pause/resume did by hand). ScheduleEventMonitor cancels-and-
	// replaces via SchedulerID, so a monitor whose task is still live just gets an
	// equivalent fresh one (no duplicate timers), and nextPoll spreads first
	// checks across each monitor's own cadence (no boot stampede).
	rearmed := 0
	for _, k := range RootDB.Keys(eventMonitorsTable) {
		var m EventMonitor
		if !RootDB.Get(eventMonitorsTable, k, &m) {
			continue
		}
		if m.Paused || !isScheduledKind(m.Kind) {
			continue
		}
		if err := ScheduleEventMonitor(RootDB, m); err != nil {
			Log("[event] boot re-arm failed for %s/%s: %v", m.Owner, m.Name, err)
			continue
		}
		rearmed++
	}
	if rearmed > 0 {
		Log("[event] startup self-heal: re-armed %d active scheduled monitor(s)", rearmed)
	}
}

// executeEventPoll runs the checker agent and, when its answer matches and
// differs from the last answer that fired (debounce), wakes the Operator.
// Factored out so it's unit-testable without the scheduler loop.
// executeEventPoll runs the checker agent and wakes the Operator on the
// TRANSITION into a match — edge-triggered, mirroring executeHTTPPoll. It fires
// once when the checker's answer first matches and will NOT fire again until a
// later answer does NOT match, which re-arms it.
//
// This bounds the damage a loose match_contains can do: a checker whose "no
// news" answers happen to contain the match token produces at most one wake,
// not one per interval. The old path was level-triggered with a byte-exact
// debounce — any answer that matched and differed textually from the last fired
// one re-fired, so a verbose checker (slightly different build refs each poll)
// flooded the Operator's single pinned thread with near-duplicate wakes.
//
// The contract the checker must honor (see the create_event_monitor guidance):
// answer the NON-match word (e.g. NONE) whenever the condition is absent, so the
// monitor re-arms between real onsets. A checker that keeps emitting the match
// token will fire once and then stay silent until it stops.
func executeEventPoll(ctx context.Context, db Database, m EventMonitor) {
	eventMu.RLock()
	poller := eventPoller
	eventMu.RUnlock()
	if poller == nil {
		Log("[event] poll %s/%s fired but no poller is registered", m.Owner, m.Name)
		return
	}
	answer, err := poller(ctx, m.Owner, m.CheckAgent, m.Check)
	if err != nil {
		Log("[event] poll %s/%s check failed: %v", m.Owner, m.Name, err)
		return
	}
	matched := eventMatch(answer, m.MatchContains)
	cur, ok := GetEventMonitor(db, m.Owner, m.Name)
	if !ok {
		return
	}
	switch {
	case matched && !cur.LastMatched:
		cur.LastMatched = true
		cur.LastResult = answer
		cur.LastFired = time.Now()
		SaveEventMonitor(db, cur)
		fireWake(ctx, db, m.Owner, m.Name, answer, "poll")
	case !matched && cur.LastMatched:
		// Condition cleared — re-arm so the next onset fires again. No wake.
		cur.LastMatched = false
		cur.LastResult = answer
		SaveEventMonitor(db, cur)
	}
}

// executeHTTPPoll fetches the monitor's URL, extracts a value, compares it to
// the threshold, and wakes the Operator on the TRANSITION into a breach
// (edge-triggered, so "notify me when NVDA goes below 150" fires once when it
// crosses, not every interval while it stays below). Fully self-contained in
// core — no LLM, no checker agent.
func executeHTTPPoll(ctx context.Context, db Database, m EventMonitor) {
	val, err := fetchAndExtract(ctx, m)
	if err != nil {
		Log("[event] http_poll %s/%s fetch/extract failed: %v", m.Owner, m.Name, err)
		return
	}
	breached, cerr := compareValues(val, m.CompareOp, m.Threshold)
	if cerr != nil {
		Log("[event] http_poll %s/%s compare failed: %v", m.Owner, m.Name, cerr)
		return
	}
	cur, ok := GetEventMonitor(db, m.Owner, m.Name)
	if !ok {
		return
	}
	switch {
	case breached && !cur.LastBreached:
		cur.LastBreached = true
		cur.LastResult = val
		cur.LastFired = time.Now()
		SaveEventMonitor(db, cur)
		summary := fmt.Sprintf("Monitor %q tripped: observed value %s %s %s (from %s).",
			m.Name, val, m.CompareOp, m.Threshold, m.URL)
		fireWake(ctx, db, m.Owner, m.Name, summary, "http_poll")
	case !breached && cur.LastBreached:
		// Recovered — reset so the next breach fires again. No wake.
		cur.LastBreached = false
		cur.LastResult = val
		SaveEventMonitor(db, cur)
	}
}

// executeWatchPoll invokes the captured tool, hashes its output, and wakes the
// channel agent ONLY when the hash differs from the stored baseline — the cheap
// "tell me when X changes" path with zero LLM until something actually changes.
// The first observation just records the baseline (no wake), so creating a
// watch on an already-populated source doesn't spuriously fire. Reuses the
// watcher engine's InvokeWatcherTool + sha256Sum (same core package).
func executeWatchPoll(ctx context.Context, db Database, m EventMonitor) {
	body, err := InvokeWatchTool(m.Owner, m.WakeAgent, m.ToolName, m.ToolArgs)
	if err != nil {
		Log("[event] watch %s/%s tool %q failed: %v", m.Owner, m.Name, m.ToolName, err)
		return
	}
	hash := sha256Sum(body)
	cur, ok := GetEventMonitor(db, m.Owner, m.Name)
	if !ok {
		return
	}
	if hash == cur.LastHash {
		return // no change — stay quiet, no LLM
	}
	firstObservation := cur.LastHash == ""
	prior := cur.LastBody
	cur.LastHash = hash
	cur.LastResult = truncateEvent(body, 400)
	cur.LastBody = capBody(body)
	if firstObservation {
		// Baseline only — don't wake on the very first poll.
		Debug("[event] watch %s/%s: first observation, baseline recorded (no wake)", m.Owner, m.Name)
		SaveEventMonitor(db, cur)
		return
	}
	// Sender/content scope: fire only when the NEW lines contain the target
	// substring (e.g. the awaited person's name). The baseline was already
	// advanced above, so an unrelated change (another participant, the agent's
	// own outbound) is absorbed silently and the NEXT change is still watched —
	// exactly the "wake only on Rory's reply, not on every group message" case.
	if strings.TrimSpace(cur.MatchNew) != "" && !addedLinesContain(prior, body, cur.MatchNew) {
		Debug("[event] watch %s/%s: change detected but no new line matched %q — baseline advanced, no wake", m.Owner, m.Name, cur.MatchNew)
		SaveEventMonitor(db, cur)
		return
	}
	// Build the alert text. A format_script (sandboxed python) shapes the
	// notification; empty stdout is its "skip this change" signal (suppress),
	// while a script error / no script falls back to the built-in diff.
	summary, suppress := buildWatchSummary(ctx, cur, prior, body)
	if suppress {
		// Intentional skip: the baseline was already advanced above, so persist
		// it and stay quiet. The next poll diffs against THIS body, not the
		// pre-change one, so the same change won't re-trip.
		Debug("[event] watch %s/%s: change detected but format_script suppressed the alert", m.Owner, m.Name)
		SaveEventMonitor(db, cur)
		return
	}
	Debug("[event] watch %s/%s: change detected — firing (%s)", m.Owner, m.Name, m.Notify)
	cur.LastFired = time.Now()
	SaveEventMonitor(db, cur)
	fireWake(ctx, db, m.Owner, m.Name, summary, "watch")
}

// buildWatchSummary produces the alert text for a watch fire. With a
// FormatScript it runs the user's sandboxed python ({prior,current} on stdin →
// stdout) and uses its stdout as the alert. Empty stdout suppresses the wake
// (the script's intentional "skip this change" signal, suppress=true); a script
// error or no script falls back to the built-in diff + current-output summary so
// a real change still fires.
// addedLinesContain reports whether any line present in current but NOT in prior
// contains needle (case-insensitive). Used to scope a watch fire to newly-added
// content — e.g. a chat whose stable roster/header repeats every poll, so only
// the genuinely new message lines are considered. A shifting fixed-size window
// (new message in, oldest out) is handled by the set difference: the new line is
// "added", the scrolled-off one is "removed" and ignored.
func addedLinesContain(prior, current, needle string) bool {
	needle = strings.ToLower(strings.TrimSpace(needle))
	if needle == "" {
		return true
	}
	priorLines := make(map[string]bool)
	for _, ln := range strings.Split(prior, "\n") {
		priorLines[strings.TrimSpace(ln)] = true
	}
	for _, ln := range strings.Split(current, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || priorLines[t] {
			continue
		}
		if strings.Contains(strings.ToLower(t), needle) {
			return true
		}
	}
	return false
}

func buildWatchSummary(ctx context.Context, m EventMonitor, prior, current string) (summary string, suppress bool) {
	return formatWatchAlert(ctx, m.Owner, m.Name, m.FormatScript, prior, current)
}

// formatWatchAlert produces the alert text for a change/watch fire, decoupled
// from any record type so both the event monitor and the unified trigger engine
// share it. A formatScript's stdout becomes the alert. Empty stdout (a clean
// exit printing nothing) is the script's intentional "skip this change" signal
// and SUPPRESSES the wake (second return = true). A script error or no script
// falls back to the built-in diff + current-output summary so a real change
// still fires.
func formatWatchAlert(ctx context.Context, owner, name, formatScript, prior, current string) (summary string, suppress bool) {
	if strings.TrimSpace(formatScript) != "" {
		// Hand the script the body (HTTP status line split off, so json.loads
		// works directly) AND the status line separately, so a script that wants
		// to react to e.g. a 500 still can. Change detection (the hash + built-in
		// diff) still uses the full body, so a status change trips the watch too.
		priorStatus, priorBody := SplitHTTPStatus(prior)
		curStatus, curBody := SplitHTTPStatus(current)
		payload, _ := json.Marshal(map[string]string{
			"prior": priorBody, "current": curBody,
			"prior_status": priorStatus, "current_status": curStatus,
		})
		sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		res := RunSandboxedScript(sctx, "python3", formatScript, string(payload))
		cancel()
		if res.Err != nil {
			Log("[event] watch %s/%s format_script error: %v (stderr=%q) — using built-in summary",
				owner, name, res.Err, truncateEvent(res.Stderr, 200))
		} else if out := strings.TrimSpace(res.Stdout); out != "" {
			return out, false
		} else {
			// Empty stdout is the script's INTENTIONAL "this change isn't worth
			// notifying" signal — suppress the wake. This is the documented
			// format_script contract. (A script CRASH is different: res.Err above
			// logs and falls through to the built-in diff below, so a bug can't
			// silently eat real changes — only a clean empty exit suppresses.)
			Debug("[event] watch %s/%s: format_script printed nothing — suppressing this change per the empty=skip contract", owner, name)
			return "", true
		}
	}
	// Built-in: lead with WHAT changed (added/removed lines), then the current
	// output for context.
	summary = fmt.Sprintf("Watch monitor %q detected a change.", name)
	if d := diffLines(prior, current); d != "" {
		summary += "\n\nWhat changed:\n" + d
	}
	summary += "\n\nCurrent output:\n" + truncateEvent(current, 1200)
	return summary, false
}

// SplitHTTPStatus splits a tool response into its leading "HTTP <code> <text>"
// status line (the prefix the temptool API path prepends) and the remaining
// body. Returns ("", s) when there's no such prefix. A watch format_script gets
// the body as prior/current (so json.loads works directly) AND the status line
// separately (so a script that wants to react to e.g. a 500 still can).
func SplitHTTPStatus(s string) (status, body string) {
	if strings.HasPrefix(s, "HTTP ") {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			return s[:i], s[i+1:]
		}
		return s, ""
	}
	return "", s
}

// capBody bounds the stored prior-output snapshot used for diffing.
func capBody(s string) string {
	const max = 4096
	if len(s) > max {
		return s[:max]
	}
	return s
}

// diffLines produces a compact line-level diff of two text blobs: lines present
// now but not before (prefixed "+") and lines present before but not now ("-").
// Set-based, so it catches "someone joined / someone left" on a server list and
// "new message" on a chat without caring about order. Capped so a big change
// can't produce a wall of text. Empty when nothing line-level changed.
func diffLines(prior, current string) string {
	priorSet := map[string]bool{}
	for _, l := range strings.Split(prior, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			priorSet[t] = true
		}
	}
	curSet := map[string]bool{}
	var added []string
	for _, l := range strings.Split(current, "\n") {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		curSet[t] = true
		if !priorSet[t] {
			added = append(added, t)
		}
	}
	var removed []string
	for l := range priorSet {
		if !curSet[l] {
			removed = append(removed, l)
		}
	}
	sort.Strings(removed) // map iteration order isn't stable; keep wakes deterministic
	const maxLines = 20
	var b strings.Builder
	for i, l := range added {
		if i >= maxLines {
			fmt.Fprintf(&b, "+ …and %d more added\n", len(added)-maxLines)
			break
		}
		fmt.Fprintf(&b, "+ %s\n", truncateEvent(l, 200))
	}
	for i, l := range removed {
		if i >= maxLines {
			fmt.Fprintf(&b, "- …and %d more removed\n", len(removed)-maxLines)
			break
		}
		fmt.Fprintf(&b, "- %s\n", truncateEvent(l, 200))
	}
	return strings.TrimRight(b.String(), "\n")
}

// fetchAndExtract GETs the monitor's URL and pulls out the value to compare,
// via JSONPath, then Regex, else the whole (trimmed) body. Body is capped at
// 1 MiB and the request times out at 20s.
func fetchAndExtract(ctx context.Context, m EventMonitor) (string, error) {
	return fetchExtractURL(ctx, m.URL, m.JSONPath, m.Regex)
}

// fetchExtractURL GETs url and pulls out the value to compare, via jsonPath,
// then regex, else the whole (trimmed) body. Body is capped at 1 MiB and the
// request times out at 20s. Decoupled from any record type so both the event
// monitor and the unified trigger engine share it.
func fetchExtractURL(ctx context.Context, url, jsonPath, regex string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "gohort-operator-monitor")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	if p := strings.TrimSpace(jsonPath); p != "" {
		return extractJSONPath(body, p)
	}
	if rx := strings.TrimSpace(regex); rx != "" {
		return extractRegex(body, rx)
	}
	return strings.TrimSpace(string(body)), nil
}

// extractJSONPath walks a dotted path into a parsed JSON document. Map keys and
// array indices both use a path segment (e.g. "quoteResponse.result.0.price").
func extractJSONPath(body []byte, path string) (string, error) {
	var data any
	if err := json.Unmarshal(body, &data); err != nil {
		return "", fmt.Errorf("response is not JSON: %w", err)
	}
	cur := data
	for _, seg := range strings.Split(path, ".") {
		switch node := cur.(type) {
		case map[string]any:
			cur = node[seg]
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(node) {
				return "", fmt.Errorf("json_path index %q out of range", seg)
			}
			cur = node[idx]
		default:
			return "", fmt.Errorf("json_path %q runs past a scalar at %q", path, seg)
		}
		if cur == nil {
			return "", fmt.Errorf("json_path %q has no value at %q", path, seg)
		}
	}
	return fmt.Sprintf("%v", cur), nil
}

// extractRegex returns the first capture group (or the whole match if there are
// no groups) of pattern against the body.
func extractRegex(body []byte, pattern string) (string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("bad regex: %w", err)
	}
	mm := re.FindSubmatch(body)
	if mm == nil {
		return "", fmt.Errorf("regex %q matched nothing", pattern)
	}
	if len(mm) > 1 {
		return strings.TrimSpace(string(mm[1])), nil
	}
	return strings.TrimSpace(string(mm[0])), nil
}

// compareValues evaluates `extracted <op> threshold`. Numeric operators parse
// both sides as floats; ==/!= compare as trimmed strings; contains is substring.
func compareValues(extracted, op, threshold string) (bool, error) {
	switch op {
	case "<", ">", "<=", ">=":
		ev, e1 := strconv.ParseFloat(strings.TrimSpace(extracted), 64)
		tv, e2 := strconv.ParseFloat(strings.TrimSpace(threshold), 64)
		if e1 != nil || e2 != nil {
			return false, fmt.Errorf("numeric compare needs numbers (got %q %s %q)", extracted, op, threshold)
		}
		switch op {
		case "<":
			return ev < tv, nil
		case ">":
			return ev > tv, nil
		case "<=":
			return ev <= tv, nil
		default: // ">="
			return ev >= tv, nil
		}
	case "==":
		return strings.TrimSpace(extracted) == strings.TrimSpace(threshold), nil
	case "!=":
		return strings.TrimSpace(extracted) != strings.TrimSpace(threshold), nil
	case "contains":
		return strings.Contains(extracted, threshold), nil
	}
	return false, fmt.Errorf("unknown compare_op %q (use < > <= >= == != contains)", op)
}

// FireEventMonitor wakes the Operator for a webhook event. Public so the
// webhook HTTP handler (orchestrate) can call it.
func FireEventMonitor(ctx context.Context, db Database, m EventMonitor, summary string) {
	if cur, ok := GetEventMonitor(db, m.Owner, m.Name); ok {
		cur.LastFired = time.Now()
		SaveEventMonitor(db, cur)
	}
	fireWake(ctx, db, m.Owner, m.Name, summary, "event")
}

// fireWake invokes the registered waker and records the fire to the run-ledger
// so it shows in the Operator's Activity feed.
func fireWake(ctx context.Context, db Database, owner, name, summary, trigger string) {
	eventMu.RLock()
	waker := eventWaker
	eventMu.RUnlock()

	rec := RunRecord{
		Owner:   owner,
		Agent:   name,
		Trigger: trigger,
		Brief:   summary,
		Started: time.Now(),
	}
	if waker == nil {
		rec.Status = RunAttention
		rec.Summary = "Event fired but no Operator waker is registered."
		rec.Ended = time.Now()
		RecordRun(db, rec)
		Log("[event] %s/%s fired but no waker is registered", owner, name)
		return
	}
	waker(ctx, owner, name, summary)
	rec.Status = RunOK
	rec.Summary = "Woke the Operator: " + truncateEvent(summary, 200)
	rec.Ended = time.Now()
	RecordRun(db, rec)

	// One-shot await: this monitor asked to be woken WHEN its result arrived, so
	// after firing once it's finished. Remove it (DeleteEventMonitor also cancels
	// any pending scheduler task). The poll handler's defer reschedule re-reads
	// the monitor and skips it when gone, so deleting here ends the chain cleanly
	// without an orphaned timer.
	if m, ok := GetEventMonitor(db, owner, name); ok && m.OneShot {
		DeleteEventMonitor(db, owner, name)
		Debug("[event] one-shot await %s/%s fired once — removed", owner, name)
	}
}

func truncateEvent(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return strings.TrimSpace(s[:max]) + "…"
}
