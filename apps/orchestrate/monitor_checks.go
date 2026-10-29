package orchestrate

// What a monitor's recent checks found, for the people and the agents that
// look after it. A monitor that never fires looks exactly like one that is
// broken; the check history (core EventMonitor.RecentChecks) says which, and
// shows the checks that WOULD have fired and why they did not.

import (
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
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

// nextCheckNote is when a monitor checks next, for a one-line listing, in the
// owner's zone and as a span from now, so an agent asked "when does it check
// again" reads the answer instead of working it out from the cadence. "" for
// one that is paused, has no check queued, or is a timer (its row says when it
// goes off).
func nextCheckNote(m EventMonitor, now time.Time) string {
	if m.Paused || m.Kind == EventKindTimer || m.NextCheck.IsZero() {
		return ""
	}
	at := m.NextCheck.In(UserLocation(m.Owner)).Format("Jan 2 3:04 PM MST")
	if !m.NextCheck.After(now) {
		return "; next check due now (" + at + ")"
	}
	return "; next check " + at + ", in " + humanUntil(m.NextCheck.Sub(now))
}

// handleConsoleMonitorChecks is a monitor's recent checks, for the Scheduler
// card's Recent checks button: GET ?id=<monitor name>, the caller's own only.
func (T *OrchestrateApp) handleConsoleMonitorChecks(c *jsonapi.Call) (any, error) {
	r := c.R
	user := c.User
	m, found := GetEventMonitor(RootDB, user, strings.TrimSpace(r.URL.Query().Get("id")))
	if !found {
		return nil, jsonapi.NotFound("no such monitor")
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
	return out, nil
}
