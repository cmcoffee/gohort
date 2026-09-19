package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// handleConsoleMonitorRelink re-points a broken monitor's wake agent at a live
// one and clears the broken flag — but LEAVES it paused, so recovery finishes
// with an explicit Resume (which re-checks the now-healthy dependency).
func (T *OrchestrateApp) handleConsoleMonitorRelink(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("id"))
	newAgent := strings.TrimSpace(r.URL.Query().Get("value"))
	// The agent is OPTIONAL for a monitor: an empty WakeAgent resolves to the
	// deployment default channel agent, and a direct-delivery monitor doesn't use
	// the agent for delivery at all — so "" / the __default__ sentinel means
	// "just use the default", and the picker no longer forces a specific choice.
	// A real id is still validated + used when the user DOES want a specific
	// persona (the case that actually matters, notify=channel).
	useDefault := newAgent == "" || newAgent == "__default__"
	if !useDefault {
		if _, ok := loadAgent(UserDB(T.DB, user), newAgent); !ok {
			http.Error(w, "no such agent", http.StatusBadRequest)
			return
		}
	}
	m, found := GetEventMonitor(RootDB, user, name)
	if !found {
		http.Error(w, "no such monitor", http.StatusNotFound)
		return
	}
	if useDefault {
		m.WakeAgent = "" // deployment default channel agent
	} else {
		m.WakeAgent = newAgent
	}
	m.Broken = false
	m.BrokenReason = ""
	// Paused stays true on purpose — the user resumes explicitly.
	SaveEventMonitor(RootDB, m)
	w.WriteHeader(http.StatusNoContent)
}

// handleConsoleMonitorMove sets a monitor's Surface in place — moves the whole
// monitor (card + rail badge + wake all follow) without a delete+recreate. The
// home session (WakeSession) is left intact, so Session always works to return.
func (T *OrchestrateApp) handleConsoleMonitorMove(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("id"))
	surface, valid := normalizeSurface(r.URL.Query().Get("value"))
	if !valid {
		http.Error(w, "move target must be cortex, session, or background", http.StatusBadRequest)
		return
	}
	m, found := GetEventMonitor(RootDB, user, name)
	if !found {
		http.Error(w, "no such monitor", http.StatusNotFound)
		return
	}
	m.Surface = surface
	SaveEventMonitor(RootDB, m)
	w.WriteHeader(http.StatusNoContent)
}

// handleConsoleMonitorGet returns an event monitor's editable record for the
// Scheduler edit modal: what it watches for, what it hands the agent when it
// fires, and how often it looks. Only scheduled kinds (poll / http_poll /
// watch) carry an interval; a webhook monitor is push-triggered and reports
// schedulable=false — it still has a condition and a brief to edit, which is
// why it now gets an editor at all.
//
// Every kind's condition fields are returned, not just the current kind's: the
// editor shows one kind's section and the rest are absent from the record
// anyway (omitempty on the way in), so branching here would only move the same
// switch to the far side of the wire.
func (T *OrchestrateApp) handleConsoleMonitorGet(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	m, found := GetEventMonitor(RootDB, user, strings.TrimSpace(r.URL.Query().Get("id")))
	if !found {
		http.Error(w, "no such monitor", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{
		"name":             m.Name,
		"kind":             m.Kind,
		"interval_seconds": m.IntervalSeconds,
		"interval_minutes": m.IntervalSeconds / 60,
		"schedulable":      IsScheduledEventKind(m.Kind),
		"paused":           m.Paused,
		// What it tells the woken agent — every kind has one, including the
		// push-triggered webhook.
		"wake_brief": m.WakeBrief,
		// poll: the question put to the checker agent, and the answer that
		// counts as a yes. check_agent is shown but not edited here; pointing a
		// monitor at a different agent is what Relink is for.
		"check":          m.Check,
		"match_contains": m.MatchContains,
		"check_agent":    m.CheckAgent,
		// http_poll: the fetch, the extraction, and the test.
		"url":        m.URL,
		"json_path":  m.JSONPath,
		"regex":      m.Regex,
		"compare_op": m.CompareOp,
		"threshold":  m.Threshold,
		// watch: the source is the tool call, shown read-only — re-pointing it
		// is a different operation from editing what it watches for. The format
		// script IS editable: it shapes the alert, not the source.
		"tool_name":     m.ToolName,
		"source_kind":   m.SourceKind,
		"format_script": m.FormatScript,
	})
}

// handleConsoleMonitorUpdate edits an event monitor in place and re-arms it:
// its poll interval, the brief handed to the woken agent, and the condition it
// watches for. A paused monitor keeps its edit persisted without re-arming (it
// applies on resume). POST ?id=<name>.
//
// Every editable field but the interval is a POINTER: absent preserves what is
// stored. A monitor's record holds four kinds' worth of fields and the editor
// only ever shows one kind's, so "not sent" has to mean "unchanged" or opening
// the modal on a poll monitor would blank the http_poll half of a record that
// had both.
//
// A field belonging to a different kind is REFUSED by name rather than stored.
// It would be dead weight on the record and live weight in the reader's head:
// a poll monitor carrying a url reads like something that fetches it.
func (T *OrchestrateApp) handleConsoleMonitorUpdate(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	m, found := GetEventMonitor(RootDB, user, strings.TrimSpace(r.URL.Query().Get("id")))
	if !found {
		http.Error(w, "no such monitor", http.StatusNotFound)
		return
	}
	var body monitorUpdateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
		return
	}
	before := m
	if err := applyMonitorUpdate(&m, body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// The stored edge/baseline state describes the condition that was just
	// replaced. Left alone, an http_poll whose threshold moved past the current
	// value reports a RECOVERY from a breach of a threshold that never existed,
	// and a watch whose source moved reports its first re-read as a change.
	// Clearing it makes the next check a silent re-baseline — the same thing
	// import does when it lands a monitor somewhere new (artifact_types.go).
	if monitorConditionChanged(before, m) {
		m.LastHash, m.LastBody, m.LastResult = "", "", ""
		m.LastBreached, m.LastMatched = false, false
	}
	if m.Paused || !IsScheduledEventKind(m.Kind) || !monitorNeedsRearm(before, m) {
		SaveEventMonitor(RootDB, m)
	} else if err := ScheduleEventMonitor(RootDB, m); err != nil {
		// Put the original back, so a rejected edit does not leave the monitor
		// holding a new condition it is not armed to check.
		if before.Paused {
			SaveEventMonitor(RootDB, before)
		} else {
			_ = ScheduleEventMonitor(RootDB, before)
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// monitorUpdateBody is what the Scheduler's monitor editor posts. Interval is a
// plain int because zero has no meaning for it (a monitor polling every zero
// seconds is not a thing to express), so absent and zero can safely mean the
// same "leave it".
type monitorUpdateBody struct {
	IntervalSeconds int `json:"interval_seconds"`
	IntervalMinutes int `json:"interval_minutes"`

	WakeBrief *string `json:"wake_brief"`

	// poll
	Check         *string `json:"check"`
	MatchContains *string `json:"match_contains"`

	// http_poll
	URL       *string `json:"url"`
	JSONPath  *string `json:"json_path"`
	Regex     *string `json:"regex"`
	CompareOp *string `json:"compare_op"`
	Threshold *string `json:"threshold"`

	// watch
	FormatScript *string `json:"format_script"`
}

// applyMonitorUpdate writes the posted fields onto m, refusing anything the
// monitor's kind does not own and anything the fire path could not act on.
// Validation happens BEFORE any assignment where it can, so a body that is
// rejected leaves the caller's monitor exactly as it was.
func applyMonitorUpdate(m *EventMonitor, body monitorUpdateBody) error {
	secs := body.IntervalSeconds
	if secs == 0 && body.IntervalMinutes > 0 {
		secs = body.IntervalMinutes * 60
	}
	if secs != 0 {
		if !IsScheduledEventKind(m.Kind) {
			return Error("this monitor is push-triggered: it has no interval to set")
		}
		if secs < 5 {
			return Error("interval too small: minimum 5 seconds")
		}
	}

	// Which condition fields this kind owns. Anything else sent is a caller
	// bug, and is named rather than dropped.
	var owned map[string]bool
	switch m.Kind {
	case EventKindPoll:
		owned = map[string]bool{"check": true, "match_contains": true}
	case EventKindHTTP:
		owned = map[string]bool{"url": true, "json_path": true, "regex": true, "compare_op": true, "threshold": true}
	case EventKindWatch:
		owned = map[string]bool{"format_script": true}
	default: // webhook — an external POST decides when it fires; only the brief is editable
		owned = map[string]bool{}
	}
	sent := map[string]*string{
		"check": body.Check, "match_contains": body.MatchContains,
		"url": body.URL, "json_path": body.JSONPath, "regex": body.Regex,
		"compare_op": body.CompareOp, "threshold": body.Threshold,
		"format_script": body.FormatScript,
	}
	// Sorted, so a body with two stray fields names the same one every time
	// rather than whichever the map handed back first.
	for _, field := range sortedKeys(sent) {
		if sent[field] != nil && !owned[field] {
			return Error("a " + m.Kind + " monitor has no " + field + " to edit")
		}
	}

	if body.WakeBrief != nil {
		// Refuse to BLANK a brief, not to save one that was already blank.
		// An empty WakeBrief is a legal, ordinary state: create_event_monitor
		// requires only a name and a kind, the bridge and connector paths pass
		// whatever the caller gave, and a notify=text monitor never wakes an
		// agent at all so its brief is unused by design. Requiring one here
		// made every such monitor UNEDITABLE: the owner could not change a
		// format script or an interval without inventing a brief first.
		brief := strings.TrimSpace(*body.WakeBrief)
		if brief == "" && strings.TrimSpace(m.WakeBrief) != "" {
			return Error("a monitor that has a brief needs to keep one: it is what the agent is told when this fires")
		}
		m.WakeBrief = brief
	}
	switch m.Kind {
	case EventKindPoll:
		check := strPtrOr(body.Check, m.Check)
		if strings.TrimSpace(check) == "" {
			return Error("a poll monitor needs a check: the question its checker agent answers each interval")
		}
		m.Check = strings.TrimSpace(check)
		if body.MatchContains != nil {
			// Empty is legitimate here: the fire path falls back to "YES", so
			// clearing it restores the default rather than disabling the test.
			m.MatchContains = strings.TrimSpace(*body.MatchContains)
		}
	case EventKindHTTP:
		url := strings.TrimSpace(strPtrOr(body.URL, m.URL))
		if url == "" {
			return Error("an http_poll monitor needs a url to fetch")
		}
		op := strings.TrimSpace(strPtrOr(body.CompareOp, m.CompareOp))
		if !ValidCompareOp(op) {
			return Error("compare_op must be one of < > <= >= == != contains")
		}
		threshold := strPtrOr(body.Threshold, m.Threshold)
		if strings.TrimSpace(threshold) == "" {
			return Error("an http_poll monitor needs a threshold: the value its extracted one is compared against")
		}
		m.URL, m.CompareOp, m.Threshold = url, op, threshold
		if body.JSONPath != nil {
			m.JSONPath = strings.TrimSpace(*body.JSONPath)
		}
		if body.Regex != nil {
			m.Regex = strings.TrimSpace(*body.Regex)
		}
	case EventKindWatch:
		if body.FormatScript != nil {
			// Empty is the documented "use the built-in diff", so it clears.
			m.FormatScript = strings.TrimSpace(*body.FormatScript)
		}
	}
	if secs != 0 {
		m.IntervalSeconds = secs
	}
	return nil
}

// monitorConditionChanged reports whether an edit moved what the monitor
// watches or how it decides — as opposed to how often it looks, or what it says
// when it fires, neither of which invalidates the baseline it has stored.
// format_script is deliberately absent: it shapes the alert AFTER the change is
// detected, so rewriting it must not throw away the baseline and silently eat
// the next change.
func monitorConditionChanged(before, after EventMonitor) bool {
	return before.Check != after.Check ||
		before.MatchContains != after.MatchContains ||
		before.URL != after.URL ||
		before.JSONPath != after.JSONPath ||
		before.Regex != after.Regex ||
		before.CompareOp != after.CompareOp ||
		before.Threshold != after.Threshold
}

// strPtrOr is the "absent means unchanged" read: the posted value when one was
// sent, the stored one when it was not.
func strPtrOr(sent *string, stored string) string {
	if sent == nil {
		return stored
	}
	return *sent
}

// sortedKeys returns a map's keys in order, so a message built by walking one
// reads the same on every request.
func sortedKeys(m map[string]*string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type consoleMonitorRow struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	State string `json:"state"`
	// Objective and Failing are the same two lines the other scheduled kinds
	// carry (schedule_row_state.go). The objective used to be appended to
	// Detail, where it sat behind the url, the operator and the threshold —
	// the one part of that string that changes on its own, at the end of the
	// part that never does.
	Objective string `json:"objective,omitempty"`
	// PartOf names the schedule this one exists to serve, when it has one.
	// A link and nothing more: see task_parent.go for why it carries no
	// authority over this row.
	PartOf string `json:"part_of,omitempty"`
	// RollUp says this one finishes when everything under it does, and what it
	// is still waiting for. Empty unless the owner turned it on.
	RollUp  string `json:"roll_up,omitempty"`
	Failing string `json:"failing,omitempty"`
	Detail  string `json:"detail"`
	Script  string `json:"format_script"` // the watch format_script, if any (so you can SEE it)
	// NextRun is when the next check is due, under the same key the other two
	// scheduled kinds use: a monitor's check IS its scheduled run, and the
	// merged Scheduler page orders every section by this one field. Empty for
	// a push-triggered monitor and for one at rest, neither of which has a
	// next anything.
	NextRun string `json:"next_run,omitempty"`
	Checked string `json:"last_checked"` // when the poll last ran (liveness)
	Seen    string `json:"last_seen"`    // last response/value it hashed/observed
	Last    string `json:"last_fired"`
	ID      string `json:"_id"`     // hidden; row-action target (the monitor name)
	Paused  bool   `json:"_paused"` // hidden; gates Pause vs Resume per row
	// Schedulable gates the "Test" row action: only poll / http_poll / watch
	// monitors have a check to run on demand — a webhook is push-only.
	Schedulable bool `json:"_schedulable"`
	Broken      bool `json:"_broken,omitempty"` // hidden; parked and kept — see State for which kind
	// Relinkable gates Relink: a monitor whose dependency is GONE can be
	// re-pointed. One whose checks keep failing cannot be repaired that way —
	// no choice of agent fixes a hostname that does not resolve.
	Relinkable bool `json:"_relinkable,omitempty"`
}

// handleConsoleMonitors lists the owner's event monitors (webhook / poll /
// http_poll) for the Event-monitors nav pane.
func (T *OrchestrateApp) handleConsoleMonitors(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agentID := strings.TrimSpace(r.URL.Query().Get("agent"))
	writeJSON(w, consoleMonitorRows(user, agentID))
}

// consoleMonitorRows builds the rows for this view. Split off the handler so the
// merged Scheduler page (console_scheduler.go) renders THESE rows rather than
// its own copy of the same logic — a second builder is how the two views
// come to disagree about what is scheduled.
func consoleMonitorRows(user, agentID string) []consoleMonitorRow {
	rows := []consoleMonitorRow{}
	for _, m := range ListEventMonitors(RootDB, user) {
		// A monitor belongs to the agent it wakes. An empty WakeAgent means the
		// default Chat agent (seed-chat) — map it so those monitors show on
		// Chat's pane instead of vanishing from every pane.
		wake := m.WakeAgent
		if wake == "" {
			wake = "seed-chat"
		}
		if agentID != "" && wake != agentID {
			continue
		}
		state := "active"
		if m.Paused {
			// The four ways a monitor comes to rest, told apart. All of them
			// are stopped; only one of them is something the owner did, and
			// only one of them wants their attention.
			switch m.StopCause() {
			case MonitorStopFinished, MonitorStopMet:
				state = "done"
			case MonitorStopIdle:
				state = "stopped: quiet"
			default:
				state = "paused"
			}
		}
		if m.Broken {
			// "needs relink" only when something is actually gone. Checks that
			// keep failing are a target to fix, not a link to re-point — the
			// label says so, and Relink is withheld below.
			state = "⚠ " + m.StopLabel()
		}
		detail := ""
		switch m.Kind {
		case EventKindPoll:
			detail = fmt.Sprintf("every %ds via %s", m.IntervalSeconds, m.CheckAgent)
		case EventKindHTTP:
			detail = fmt.Sprintf("every %ds: %s %s %s", m.IntervalSeconds, m.URL, m.CompareOp, m.Threshold)
		case EventKindWatch:
			detail = fmt.Sprintf("every %ds: watch %s", m.IntervalSeconds, m.ToolName)
			if len(m.ToolArgs) > 0 {
				if b, err := json.Marshal(m.ToolArgs); err == nil {
					detail += " " + string(b)
				}
			}
			detail += " for changes"
			if strings.TrimSpace(m.FormatScript) != "" {
				// A format_script only suppresses now when it emits the explicit
				// SKIP sentinel; empty output fails open to the built-in diff. Flag
				// its presence so a custom-formatted watcher is still visible here.
				detail += " [format_script]"
			}
		case EventKindWebhook:
			detail = "webhook (POST .../event/" + m.Token + ")"
		}
		if detail != "" {
			var dests []string
			hasWake := false
			for _, mode := range strings.Split(m.Notify, ",") {
				switch strings.TrimSpace(mode) {
				case EventNotifyText:
					dests = append(dests, "texts you")
				case EventNotifyDirect:
					if m.DeliverChatID != "" {
						dests = append(dests, "posts to its chat")
					} else {
						dests = append(dests, "posts here")
					}
				case EventNotifyChannel:
					dests = append(dests, "wakes here")
					hasWake = true
				}
			}
			if len(dests) == 0 {
				dests = append(dests, "wakes here")
				hasWake = true
			}
			detail += " → " + strings.Join(dests, " + ")
			if !hasWake {
				detail += " (no LLM)"
			}
			// Show where the monitor surfaces (Surface) so the user sees + can change
			// it via the Move-to action (its card/badge/wake all follow).
			detail += surfaceSuffix(m.Surface)
			// A bounded monitor says so in the row. Without this the list shows
			// "active" for a monitor with one fire left and for one that will
			// run forever, which is the whole reason a missing bound went
			// unnoticed until the alerts kept arriving.
			if lbl := m.FireLabel(); lbl != "" {
				detail += " · " + lbl
			}
			// A monitor that has fired and whose condition never went false
			// again is running without being able to do anything. It is not
			// stopped, so it gets no stop mark — it gets told.
			if lbl := m.StuckLabel(); lbl != "" {
				detail += " · " + lbl
			}
		}
		last := ""
		if !m.LastFired.IsZero() {
			last = m.LastFired.Local().Format("Jan 2 3:04 PM")
		}
		checked := ""
		if !m.LastChecked.IsZero() {
			checked = m.LastChecked.Local().Format("Jan 2 3:04 PM")
		}
		seen := strings.ReplaceAll(m.LastResult, "\n", " ")
		if r := []rune(seen); len(r) > 80 {
			seen = string(r[:80]) + "…"
		}
		// Full script — the UI table renders long/multi-line cells with a
		// click-to-expand toggle, so send it whole rather than truncating here.
		script := strings.TrimSpace(m.FormatScript)
		rows = append(rows, consoleMonitorRow{Name: m.Name, Kind: m.Kind, State: state, Detail: detail, Script: script, Checked: checked, Seen: seen, Last: last, ID: m.Name, Paused: m.Paused, Schedulable: IsScheduledEventKind(m.Kind), Broken: m.Broken,
			// Where its stopping condition stands, in the checker's own words.
			Objective: objectiveStateLabel(monitorObjective(m)),
			PartOf:    taskParentLabel(user, m.Parent),
			RollUp:    rollUpStateLabel(user, schedKindMonitor, m.Name),
			// A monitor does not back off, it PARKS: the streak counts towards
			// a bound, so the label says what it is counting towards rather
			// than leaving a rising number to mean whatever the reader guesses.
			Failing: scheduleFailingLabel(m.ConsecutiveFailures, MonitorFailureThreshold(), time.Time{}, nil),
			NextRun: monitorNextRun(m),
			// The monitor vocabulary's own answer to core.RelinkFixesIt: a
			// monitor separates "the thing it needs is gone" (relink) from
			// "everything resolves and the checks keep failing" (does not), and
			// only the first is repaired by re-pointing it. See ParkCauseOf.
			Relinkable: m.StopCause() == MonitorStopBroken})
	}
	return rows
}

func (T *OrchestrateApp) handleConsoleMonitorDelete(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("id"))
	if name == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	DeleteEventMonitor(RootDB, user, name)
	w.WriteHeader(http.StatusNoContent)
}

// setConsoleMonitorPaused pauses/resumes an event monitor. Pausing a scheduled
// monitor (poll/http_poll) cancels its next check; resuming reschedules it. For
// a webhook monitor the Paused flag alone gates the public endpoint.
func (T *OrchestrateApp) setConsoleMonitorPaused(w http.ResponseWriter, r *http.Request, paused bool) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	m, found := GetEventMonitor(RootDB, user, strings.TrimSpace(r.URL.Query().Get("id")))
	if !found {
		http.Error(w, "no such monitor", http.StatusNotFound)
		return
	}
	m.Paused = paused
	if paused {
		// A person did this, which is one of the four things that stop a
		// monitor and the only one that needs no explanation.
		m.StopReason = MonitorStopOwner
	} else {
		// Resuming a monitor that stopped at its bound means "watch again", not
		// "fire once more and stop immediately". Its lifetime count is kept;
		// only the allowance restarts.
		RearmMonitorFires(&m)
		m.StopReason = ""
	}
	if paused {
		if m.SchedulerID != "" {
			UnscheduleTask(m.SchedulerID)
			m.SchedulerID = ""
			m.NextCheck = time.Time{}
		}
		SaveEventMonitor(RootDB, m)
	} else {
		// Resume — recovery from a broken monitor is a GATED, explicit action:
		// only allow it once the missing dependency is actually back, otherwise
		// we'd resume into a monitor that just re-breaks on its next tick. Re-check
		// here and refuse while it's still missing; clear the broken flag on
		// success so the monitor comes back healthy.
		if m.Broken {
			if reason := eventMonitorDependencyError(m); reason != "" {
				http.Error(w, "can't resume: "+reason+"; relink it to a live agent or delete it", http.StatusConflict)
				return
			}
			m.Broken = false
			m.BrokenReason = ""
		}
		SaveEventMonitor(RootDB, m)
		_ = ScheduleEventMonitor(RootDB, m)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (T *OrchestrateApp) handleConsoleMonitorPause(w http.ResponseWriter, r *http.Request) {
	T.setConsoleMonitorPaused(w, r, true)
}

func (T *OrchestrateApp) handleConsoleMonitorResume(w http.ResponseWriter, r *http.Request) {
	T.setConsoleMonitorPaused(w, r, false)
}

// handleConsoleMonitorRun runs a monitor's check immediately from the
// Event-monitors pane's "Test" button — a one-off manual poll that fires the
// wake/notify if the condition matches, without touching the monitor's cadence
// (RunEventMonitorCheck does not re-arm). Rejected for webhook (push-only)
// monitors, which have no check to run. The check runs off-request in a
// goroutine with a background context (a poll can call an external agent/URL and
// must outlive the response); the row's last-checked timestamp updates on
// reload. id = the monitor's name. Returns 202 once the check is launched.
func (T *OrchestrateApp) handleConsoleMonitorRun(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("id"))
	m, found := GetEventMonitor(RootDB, user, name)
	if !found {
		http.Error(w, "no such monitor", http.StatusNotFound)
		return
	}
	if !IsScheduledEventKind(m.Kind) {
		http.Error(w, "this monitor is push-triggered: it has no check to run", http.StatusBadRequest)
		return
	}
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				Log("[orchestrate/console] test-check panicked for monitor %s/%s: %v", user, name, rec)
			}
		}()
		if err := RunEventMonitorCheck(context.Background(), RootDB, user, name); err != nil {
			Log("[orchestrate/console] test-check failed for monitor %s/%s: %v", user, name, err)
		}
	}()
	w.WriteHeader(http.StatusAccepted)
}

// --- the row mark ------------------------------------------------------------

// monitorRowState maps a monitor that has come to rest onto the generic state
// mark core/ui draws on a list row, or nil for one that is still running.
//
// core/ui names shapes and nothing else; this is where a shape acquires a
// meaning. Two of the four causes are outcomes the owner asked for and read
// muted; the one that needs them reads as a warning. Idle gets its own shape
// rather than the pause bars because "I stopped watching, nothing was
// happening" is a different sentence from "you paused me", and collapsing them
// is exactly what the bare Paused bool used to do.
func monitorRowState(m EventMonitor) map[string]any {
	cause := m.StopCause()
	if cause == "" {
		return nil
	}
	icon, tone := "pause", "muted"
	switch cause {
	case MonitorStopFinished, MonitorStopMet:
		icon = "check"
	case MonitorStopBroken:
		icon, tone = "alert", "warn"
	case MonitorStopIdle:
		icon = "off"
	}
	return map[string]any{"icon": icon, "tone": tone, "title": m.Name + " · " + m.StopLabel()}
}

// monitorStopUrgency ranks the causes so a row fed by several monitors shows
// the one worth acting on. A channel with one broken watcher and three
// finished ones has a problem, and the problem is what the row should say.
func monitorStopUrgency(cause string) int {
	switch cause {
	case MonitorStopBroken:
		return 4
	case MonitorStopIdle:
		return 3
	case MonitorStopFinished, MonitorStopMet:
		return 2
	case MonitorStopOwner:
		return 1
	}
	return 0
}

// channelRowState is the mark for a channel row: the most urgent stopped
// monitor that DELIVERS INTO this channel, or nil when none has stopped.
//
// Delivery is the test, deliberately. A monitor that wakes an agent in a
// thread has nothing to do with this channel even when the same agent is bound
// to it, and marking the channel for it would say something untrue about a
// conversation.
func channelRowState(ch Channel, monitors []EventMonitor) map[string]any {
	best := 0
	var found EventMonitor
	for _, m := range monitors {
		if !monitorDeliversTo(m, ch) {
			continue
		}
		if u := monitorStopUrgency(m.StopCause()); u > best {
			best, found = u, m
		}
	}
	if best == 0 {
		return nil
	}
	return monitorRowState(found)
}

// monitorDeliversTo reports whether a monitor's alert lands in this channel —
// either bound to it (WakeChannel) or posting into the conversation it sits on
// (DeliverChatID, which is that conversation's own id).
func monitorDeliversTo(m EventMonitor, ch Channel) bool {
	if id := strings.TrimSpace(ch.ID); id != "" && strings.TrimSpace(m.WakeChannel) == id {
		return true
	}
	addr := strings.TrimSpace(ch.Address)
	return addr != "" && strings.TrimSpace(m.DeliverChatID) == addr
}

// scheduleStopLabel is what a standing agent or recurring task shows for the
// state it is in — the same four kinds of stop a monitor has, in the words
// each one earns. A schedule that REACHED its objective reads as done rather
// than as "paused", which is what it looked like when Paused was the only
// thing the record stored.
func scheduleStopLabel(cause, note string) string {
	switch cause {
	case StoppedByMet:
		if strings.TrimSpace(note) != "" {
			return "✓ done, objective met: " + note
		}
		return "✓ done: objective met"
	case StoppedByOwner:
		return "paused"
	case ParkedByObjective, ParkedByDependency:
		return parkedStateLabel(cause, note)
	}
	return ""
}

// scheduleRowState maps a stopped schedule onto the same generic row mark the
// channel rail and the monitor rows use. Nil for a running one.
//
// The tones carry the only distinction that matters at a glance: amber for the
// two stops that are waiting on a person, muted for the two that are not.
func scheduleRowState(name, cause, note string) map[string]any {
	icon, tone := "", "muted"
	switch cause {
	case StoppedByMet:
		icon = "check"
	case StoppedByOwner:
		icon = "pause"
	case ParkedByObjective, ParkedByDependency:
		icon, tone = "alert", "warn"
	default:
		return nil
	}
	title := name
	if lbl := scheduleStopLabel(cause, note); lbl != "" {
		title += " · " + strings.TrimPrefix(strings.TrimPrefix(lbl, "✓ "), "⚠ ")
	}
	return map[string]any{"icon": icon, "tone": tone, "title": title}
}
