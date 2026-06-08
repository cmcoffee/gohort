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
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	eventMonitorsTable = "event_monitors" // <owner>:<name> -> EventMonitor
	eventPollKind      = "event.poll"

	// The monitor kinds. webhook = external POST; poll = LLM checker agent on
	// an interval; http_poll = fetch a URL, extract a value, compare to a
	// threshold (no LLM, deterministic — best for numeric/value conditions).
	EventKindWebhook = "webhook"
	EventKindPoll    = "poll"
	EventKindHTTP    = "http_poll"

	// minPollInterval floors the poll cadence so a misconfigured monitor can't
	// hammer the checker agent.
	minPollInterval = 30
)

// EventMonitor is one standing trigger. Name is unique per owner.
type EventMonitor struct {
	Name      string `json:"name"`
	Owner     string `json:"owner"`
	Kind      string `json:"kind"`            // EventKindWebhook | EventKindPoll
	WakeBrief string `json:"wake_brief"`      // guidance handed to the Operator on each wake
	Token     string `json:"token,omitempty"` // webhook secret (URL path segment)

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

	Paused       bool      `json:"paused"`
	Created      time.Time `json:"created"`
	NextCheck    time.Time `json:"next_check,omitempty"`
	LastFired    time.Time `json:"last_fired,omitempty"`
	LastResult   string    `json:"last_result,omitempty"`   // last answer/value seen (poll debounce / http display)
	LastBreached bool      `json:"last_breached,omitempty"` // http_poll edge-trigger: was the condition met last check
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
// (both LLM poll and http_poll do; webhook is push-only).
func isScheduledKind(kind string) bool {
	return kind == EventKindPoll || kind == EventKindHTTP
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
		if !m.Paused {
			switch m.Kind {
			case EventKindPoll:
				executeEventPoll(ctx, RootDB, m)
			case EventKindHTTP:
				executeHTTPPoll(ctx, RootDB, m)
			}
		}
		// Reschedule the recurring cadence (re-read in case it was paused or
		// edited during the check).
		if cur, ok := GetEventMonitor(RootDB, p.Owner, p.Name); ok && !cur.Paused && isScheduledKind(cur.Kind) {
			if err := ScheduleEventMonitor(RootDB, cur); err != nil {
				Log("[event] reschedule failed for %s/%s: %v", p.Owner, p.Name, err)
			}
		}
	})
}

// executeEventPoll runs the checker agent and, when its answer matches and
// differs from the last answer that fired (debounce), wakes the Operator.
// Factored out so it's unit-testable without the scheduler loop.
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
	if !eventMatch(answer, m.MatchContains) {
		return
	}
	// Debounce: don't re-fire while the condition reports the same answer.
	if strings.TrimSpace(answer) == strings.TrimSpace(m.LastResult) {
		return
	}
	if cur, ok := GetEventMonitor(db, m.Owner, m.Name); ok {
		cur.LastFired = time.Now()
		cur.LastResult = answer
		SaveEventMonitor(db, cur)
	}
	fireWake(ctx, db, m.Owner, m.Name, answer, "poll")
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

// fetchAndExtract GETs the monitor's URL and pulls out the value to compare,
// via JSONPath, then Regex, else the whole (trimmed) body. Body is capped at
// 1 MiB and the request times out at 20s.
func fetchAndExtract(ctx context.Context, m EventMonitor) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.URL, nil)
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
		return "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, m.URL)
	}
	if p := strings.TrimSpace(m.JSONPath); p != "" {
		return extractJSONPath(body, p)
	}
	if rx := strings.TrimSpace(m.Regex); rx != "" {
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
}

func truncateEvent(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return strings.TrimSpace(s[:max]) + "…"
}
