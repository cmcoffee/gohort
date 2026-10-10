package orchestrate

// What a monitor's recent checks found, for the people and the agents that
// look after it. A monitor that never fires looks exactly like one that is
// broken; the check history (core EventMonitor.RecentChecks) says which, and
// shows the checks that WOULD have fired and why they did not.

import (
	"net/http"
	"strings"

	. "github.com/cmcoffee/oddjob/core"
)

// monitorCheckLines is m's recent checks, newest first, at most n, one line
// each: when, the outcome, and what it saw.
func monitorCheckLines(m EventMonitor, n int) []string {
	var out []string
	for i := len(m.RecentChecks) - 1; i >= 0 && len(out) < n; i-- {
		c := m.RecentChecks[i]
		line := c.At.Local().Format("Jan 2 3:04 PM") + "  " + c.Outcome
		if d := strings.TrimSpace(c.Detail); d != "" {
			line += ": " + d
		}
		out = append(out, line)
	}
	return out
}

// lastCheckNote is a monitor's latest check for a one-line listing, or "".
func lastCheckNote(m EventMonitor) string {
	if l := monitorCheckLines(m, 1); len(l) > 0 {
		return "; last check " + l[0]
	}
	return ""
}

// handleConsoleMonitorChecks is a monitor's recent checks, for the Scheduler
// card's Recent checks button: GET ?id=<monitor name>, the caller's own only.
func (T *OrchestrateApp) handleConsoleMonitorChecks(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	m, found := GetEventMonitor(RootDB, user, strings.TrimSpace(r.URL.Query().Get("id")))
	if !found {
		http.Error(w, "no such monitor", http.StatusNotFound)
		return
	}
	lines := monitorCheckLines(m, len(m.RecentChecks))
	out := map[string]any{"monitor": m.Name}
	if !m.LastChecked.IsZero() {
		out["last checked"] = m.LastChecked.Local().Format("Jan 2 3:04 PM")
	}
	if len(lines) == 0 {
		out["recent checks"] = "None recorded yet: each check is kept from the next one on."
	} else {
		out["recent checks"] = lines
	}
	writeJSON(w, out)
}
