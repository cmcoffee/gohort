package orchestrate

// Schedules that finished.
//
// A monitor that saw its condition come true or spent the fires it was given,
// and a scheduled agent that reached its goal or its end, stop on their own
// and nothing is wrong with them. Listed among the running ones they read as
// clutter, and nothing ever cleared them. They are filed under Done on the
// Scheduler instead, and deleted doneExpiry after they finished unless their
// owner keeps them. A recurring task that finishes already retires itself.
//
// Like the parked sweep beside it (parked_schedules.go), when a schedule was
// first seen done, and whether it is kept, live in side tables, never on the
// schedule: every path that stops one is covered without each remembering to
// stamp it, and nothing stored changes shape.

import (
	"context"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

const (
	doneSinceTable = "schedule_done_since"
	doneKeptTable  = "schedule_done_kept"
	doneExpiry     = 30 * 24 * time.Hour
)

// standingDone reports whether a scheduled agent stopped because it finished:
// it met its goal, or reached the date or number of runs it was given.
func standingDone(sa StandingAgent) bool {
	if sa.Broken || !sa.Paused {
		return false
	}
	c := StandingStopCause(sa)
	return c == StoppedByMet || c == StoppedByEnd
}

// monitorDone reports whether a monitor stopped because it finished: its
// condition came true, or it spent the fires it was created with.
func monitorDone(m EventMonitor) bool {
	if m.Broken || !m.Paused {
		return false
	}
	c := m.StopCause()
	return c == MonitorStopMet || c == MonitorStopFinished
}

// doneSince is when a schedule was first seen done, recording now the first
// time it is asked.
func doneSince(kind, owner, id string) time.Time {
	if RootDB == nil {
		return time.Now()
	}
	var at time.Time
	k := parkedKey(kind, owner, id)
	if RootDB.Get(doneSinceTable, k, &at) && !at.IsZero() {
		return at
	}
	at = time.Now()
	RootDB.Set(doneSinceTable, k, at)
	return at
}

func doneKept(kind, owner, id string) bool {
	if RootDB == nil {
		return false
	}
	var kept bool
	return RootDB.Get(doneKeptTable, parkedKey(kind, owner, id), &kept) && kept
}

func setDoneKept(kind, owner, id string, keep bool) {
	if RootDB == nil {
		return
	}
	if keep {
		RootDB.Set(doneKeptTable, parkedKey(kind, owner, id), true)
		return
	}
	RootDB.Unset(doneKeptTable, parkedKey(kind, owner, id))
}

// doneStateNote is what a done row adds to its state: kept, or when it goes.
func doneStateNote(kind, owner, id string) string {
	if doneKept(kind, owner, id) {
		return " (kept)"
	}
	at := doneSince(kind, owner, id).Add(doneExpiry)
	return " (removed " + at.In(UserLocation(owner)).Format("Jan 2") + " unless kept)"
}

// sweepDoneSchedules deletes schedules that finished more than doneExpiry ago
// and were not kept, and forgets the done time and the keep of any that are
// running again or gone.
func sweepDoneSchedules(owners []string) int {
	if RootDB == nil {
		return 0
	}
	seen := map[string]bool{}
	removed := 0
	expired := func(kind, owner, id string) bool {
		k := parkedKey(kind, owner, id)
		seen[k] = true
		return !doneKept(kind, owner, id) && time.Since(doneSince(kind, owner, id)) > doneExpiry
	}
	forget := func(kind, owner, id string) {
		k := parkedKey(kind, owner, id)
		RootDB.Unset(doneSinceTable, k)
		RootDB.Unset(doneKeptTable, k)
	}
	for _, owner := range owners {
		for _, sa := range ListStandingAgents(RootDB, owner) {
			if !standingDone(sa) || !expired(schedKindStanding, owner, sa.Name) {
				continue
			}
			DeleteStandingAgent(RootDB, owner, sa.Name)
			forget(schedKindStanding, owner, sa.Name)
			Log("[orchestrate/done] deleted scheduled agent %s/%s: finished over %s ago and not kept", owner, sa.Name, doneExpiry)
			removed++
		}
	}
	for _, m := range ListAllEventMonitors(RootDB) {
		if !monitorDone(m) || !expired(schedKindMonitor, m.Owner, m.Name) {
			continue
		}
		DeleteEventMonitor(RootDB, m.Owner, m.Name)
		forget(schedKindMonitor, m.Owner, m.Name)
		Log("[orchestrate/done] deleted monitor %s/%s: finished over %s ago and not kept", m.Owner, m.Name, doneExpiry)
		removed++
	}
	// Anything no longer done (resumed, deleted): forget when it finished
	// and whether it was kept, so finishing again starts a fresh clock.
	for _, table := range []string{doneSinceTable, doneKeptTable} {
		for _, k := range RootDB.Keys(table) {
			if !seen[k] {
				RootDB.Unset(table, k)
			}
		}
	}
	return removed
}

// registerDoneSweep wires the sweep into the scheduler's reconcilers.
func registerDoneSweep() {
	RegisterReconciler("done_schedule_expiry", func(ctx context.Context) error {
		if AuthDB == nil || AuthDB() == nil {
			return nil
		}
		var owners []string
		for _, u := range AuthListUsers(AuthDB()) {
			owners = append(owners, u.Username)
		}
		if n := sweepDoneSchedules(owners); n > 0 {
			Log("[orchestrate/done] removed %d finished schedule(s) past %s", n, doneExpiry)
		}
		return nil
	})
}

// handleConsoleScheduleKeep keeps a finished schedule past the cleanup
// (POST .../keep?id=<kind>:<name>), or lets it go again (.../unkeep).
func (T *OrchestrateApp) handleConsoleScheduleKeep(c *jsonapi.Call, _ struct{}) (any, error) {
	kind, name, _ := strings.Cut(strings.TrimSpace(c.R.URL.Query().Get("id")), ":")
	done := false
	switch kind {
	case schedKindStanding:
		for _, sa := range ListStandingAgents(RootDB, c.User) {
			if sa.Name == name {
				done = standingDone(sa)
			}
		}
	case schedKindMonitor:
		if m, ok := GetEventMonitor(RootDB, c.User, name); ok {
			done = monitorDone(m)
		}
	}
	if !done {
		return nil, jsonapi.NotFound("no finished schedule by that name")
	}
	setDoneKept(kind, c.User, name, !strings.HasSuffix(c.R.URL.Path, "/unkeep"))
	return jsonapi.Empty(http.StatusNoContent), nil
}
