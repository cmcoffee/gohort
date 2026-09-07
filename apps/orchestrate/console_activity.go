package orchestrate

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// handleConsoleRuns returns the run-ledger feed (owner-scoped, status-level).
func (T *OrchestrateApp) handleConsoleRuns(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	runs := ListRuns(RootDB, user, RunFilter{Limit: 100})
	if runs == nil {
		runs = []RunRecord{}
	}
	writeJSON(w, runs)
}

// consoleActivityRow is one row of the live "Active now" pane. Cards layout:
// Agent renders bold as the title, the rest as detail lines. _id / _running
// are hidden plumbing for the Cancel row action.
type consoleActivityRow struct {
	Agent    string `json:"agent"`
	Activity string `json:"activity"`
	Brief    string `json:"brief,omitempty"`
	Surface  string `json:"surface"`
	ID       string `json:"_id"`
	Running  bool   `json:"_running,omitempty"`
}

// runOwnerDestination is the conversation a run's owner rejoins from the live
// pill, or "" when the run has no conversation to rejoin.
//
// Only a ROOT run qualifies: a dispatched sub-agent's session id names a
// sub-session, not a thread the chat page can open, and the thread it belongs
// to is its parent's, which is already listed. A root run with a session id
// — a chat turn, a scheduled or standing fire waking a thread, a channel
// turn — opens that thread, where the panel's resume probe finds the run.
func runOwnerDestination(prefix string, s RunSnapshot) string {
	if s.Depth != 0 || strings.TrimSpace(s.SessionID) == "" || strings.TrimSpace(s.AgentID) == "" {
		return ""
	}
	return strings.TrimSuffix(prefix, "/") + "/?agent=" + url.QueryEscape(s.AgentID) + "&session=" + url.QueryEscape(s.SessionID)
}

// handleConsoleActivity serves the live agent-activity view: every run the
// in-memory registry knows about for this user — interactive chat turns,
// scheduled fires, and standing-agent fires — running ones first, then the
// recently completed (the registry's reconnect-retention window doubles as
// the history horizon). This is the "what is the AI doing right now" pane
// the per-session live card can't provide: it only ever shows the session
// you're looking at, while the fleet works in the background.
func (T *OrchestrateApp) handleConsoleActivity(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	rows := []consoleActivityRow{}
	now := time.Now()
	// ?run=<id> narrows to one run and everything it started — the destination
	// the live pill points a background entry at. An id that matches nothing
	// returns nothing rather than falling back to the whole list, so a stale
	// link says "that work is over" instead of quietly showing the deployment.
	snaps := descendantsOf(T.runsRegistry().Activity(user), strings.TrimSpace(r.URL.Query().Get("run")))
	background := markBackground(snaps)
	for _, s := range snaps {
		name := s.AgentName
		if name == "" {
			name = s.AgentID
		}
		var activity string
		switch s.Status {
		case RunStatusRunning:
			activity = "● running — " + shortElapsed(now.Sub(s.StartedAt))
			if s.Round > 0 {
				activity += fmt.Sprintf(", round %d", s.Round)
			}
			if s.LastTool != "" {
				activity += ", last tool: " + s.LastTool
			}
		default:
			activity = s.Status + " " + shortElapsed(now.Sub(s.EndedAt)) + " ago — took " + shortElapsed(s.EndedAt.Sub(s.StartedAt))
			if s.Round > 0 {
				activity += fmt.Sprintf(" (%d rounds)", s.Round)
			}
		}
		surface := s.Kind
		if background[s.ID] && s.Status == RunStatusRunning {
			// Said on the row, not just in a color: this table is also read by
			// people who arrived from a link rather than from the pill.
			surface += " · background"
		}
		rows = append(rows, consoleActivityRow{
			Agent:    runIndentPrefix(s.Depth) + name,
			Activity: activity,
			Brief:    s.Label,
			Surface:  surface,
			ID:       s.ID,
			Running:  s.Status == RunStatusRunning,
		})
	}
	writeJSON(w, rows)
}

// handleConsoleActivityCancel cancels one in-flight run from the Active-now
// pane — the UI kill switch for a runaway cycle. Owner-checked against the
// run's recorded user; canceling an already-finished run is a no-op.
func (T *OrchestrateApp) handleConsoleActivityCancel(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	run := T.runsRegistry().Get(r.URL.Query().Get("id"))
	if run == nil || run.UserID != user {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	run.Cancel()
	w.WriteHeader(http.StatusNoContent)
}

// shortElapsed renders a duration for the activity pane: 42s, 3m10s, 1h04m.
func shortElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// handleConsoleRunDetail returns one run's full record (encrypted raw fetched
// on demand).
func (T *OrchestrateApp) handleConsoleRunDetail(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	rec, found := GetRun(RootDB, user, r.URL.Query().Get("id"))
	if !found {
		writeJSON(w, map[string]any{})
		return
	}
	writeJSON(w, rec)
}
