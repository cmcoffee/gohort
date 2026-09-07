package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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

// handleConsoleMonitorGet returns an event monitor's editable schedule for the
// Scheduler edit modal. Only scheduled kinds (poll / http_poll / watch) carry an
// interval; a webhook monitor is push-triggered and reports schedulable=false.
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
	})
}

// handleConsoleMonitorUpdate edits an event monitor's poll interval in place and
// re-arms it. Rejected for webhook (push-only) monitors. A paused monitor keeps
// its new interval persisted without re-arming (it applies on resume). POST
// ?id=<name> with {interval_minutes} (or {interval_seconds}).
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
	if !IsScheduledEventKind(m.Kind) {
		http.Error(w, "this monitor is push-triggered — it has no schedule to edit", http.StatusBadRequest)
		return
	}
	var body struct {
		IntervalSeconds int `json:"interval_seconds"`
		IntervalMinutes int `json:"interval_minutes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
		return
	}
	secs := body.IntervalSeconds
	if secs == 0 && body.IntervalMinutes > 0 {
		secs = body.IntervalMinutes * 60
	}
	if secs < 5 {
		http.Error(w, "interval too small — minimum 5 seconds", http.StatusBadRequest)
		return
	}
	m.IntervalSeconds = secs
	if m.Paused {
		SaveEventMonitor(RootDB, m)
	} else if err := ScheduleEventMonitor(RootDB, m); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type consoleMonitorRow struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	State   string `json:"state"`
	Detail  string `json:"detail"`
	Script  string `json:"format_script"` // the watch format_script, if any (so you can SEE it)
	Checked string `json:"last_checked"`  // when the poll last ran (liveness)
	Seen    string `json:"last_seen"`     // last response/value it hashed/observed
	Last    string `json:"last_fired"`
	ID      string `json:"_id"`     // hidden; row-action target (the monitor name)
	Paused  bool   `json:"_paused"` // hidden; gates Pause vs Resume per row
	// Schedulable gates the "Test" row action: only poll / http_poll / watch
	// monitors have a check to run on demand — a webhook is push-only.
	Schedulable bool `json:"_schedulable"`
	Broken      bool `json:"_broken,omitempty"` // hidden; dependency gone → needs relink
}

// handleConsoleMonitors lists the owner's event monitors (webhook / poll /
// http_poll) for the Event-monitors nav pane.
func (T *OrchestrateApp) handleConsoleMonitors(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	// Scope to the agent this pane is for — a monitor belongs to the agent it
	// wakes (WakeAgent, set on create), so without this every agent's pane shows
	// every agent's monitors.
	agentID := strings.TrimSpace(r.URL.Query().Get("agent"))
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
			switch MonitorStopCause(m) {
			case MonitorStopFinished, MonitorStopMet:
				state = "done"
			case MonitorStopIdle:
				state = "stopped — quiet"
			default:
				state = "paused"
			}
		}
		if m.Broken {
			state = brokenStateLabel(m.BrokenReason)
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
			if lbl := MonitorFireLabel(m); lbl != "" {
				detail += " · " + lbl
			}
			// And where its stopping condition stands, in the checker's own
			// words — the same label the other two scheduling surfaces show.
			if lbl := objectiveStateLabel(monitorObjective(m)); lbl != "" {
				detail += " · " + lbl
			}
			// A monitor that has fired and whose condition never went false
			// again is running without being able to do anything. It is not
			// stopped, so it gets no stop mark — it gets told.
			if lbl := MonitorStuckLabel(m); lbl != "" {
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
		rows = append(rows, consoleMonitorRow{Name: m.Name, Kind: m.Kind, State: state, Detail: detail, Script: script, Checked: checked, Seen: seen, Last: last, ID: m.Name, Paused: m.Paused, Schedulable: IsScheduledEventKind(m.Kind), Broken: m.Broken})
	}
	writeJSON(w, rows)
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
				http.Error(w, "can't resume — "+reason+"; relink it to a live agent or delete it", http.StatusConflict)
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
		http.Error(w, "this monitor is push-triggered — it has no check to run", http.StatusBadRequest)
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
	cause := MonitorStopCause(m)
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
	return map[string]any{"icon": icon, "tone": tone, "title": m.Name + " — " + MonitorStopLabel(m)}
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
		if u := monitorStopUrgency(MonitorStopCause(m)); u > best {
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
