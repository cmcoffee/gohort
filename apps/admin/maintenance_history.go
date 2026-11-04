package admin

// What each hand-run maintenance function did last time.
//
// The migrations table records the passes that fire on their own. The ones an
// operator presses had no record at all: a run reported its count into the page
// that started it, and the in-memory outcome behind the progress endpoint keeps
// a last word for fifteen minutes and not across a restart. So the honest
// answer to "has this been run, and when" was to ask somebody who remembered.
//
// That is the question to settle before any question about cadence. A schedule
// for a pass whose staleness nobody can see is a guess with a cron on it.
//
// Lives in admin rather than core deliberately: admin owns the endpoint that
// runs these, and the one identity that matters (who pressed it) exists only
// here. Core is also at its file ceiling, so a new file there is a change to
// the shape of the hub for a record only this page reads.

import (
	"fmt"
	"sort"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// maintenanceRunsTable holds one record per maintenance key, overwritten each
// run. A history of every press would be a log; what a person needs before
// pressing is the LAST one, and a growing table for that is storage nobody
// reads.
const maintenanceRunsTable = "maintenance_runs"

// maintenanceRun is how one pass last ended, pressed or run on its own.
type maintenanceRun struct {
	Key     string    `json:"key"`
	RanAt   time.Time `json:"ran_at"`
	By      string    `json:"by,omitempty"` // who pressed it; empty when it ran on its own
	Changed int       `json:"changed"`
	Seconds int       `json:"seconds,omitempty"`
	// Outcome is the pass's own last words, when it said any. Failed and
	// Stopped are how it ended when that was not a finish.
	Outcome string `json:"outcome,omitempty"`
	Failed  bool   `json:"failed,omitempty"`
	Stopped bool   `json:"stopped,omitempty"`
}

// recordMaintenanceEnd stores how a pass ended. It is core's
// OnMaintenanceEnded, so it hears every run: the ones pressed here and the
// ones that run on their own (the nightly vector repair, the daily memory
// pass), which before this left a trace only in the server log. Core calls it
// once per run, from the run that started it, so a press that joined a run
// already going does not overwrite the record with its own name. Best-effort:
// a pass that worked must not report failure because the note about it did
// not save.
func (a *AdminApp) recordMaintenanceEnd(e MaintenanceEnded) {
	if a == nil || a.db == nil || e.Key == "" {
		return
	}
	a.db.Set(maintenanceRunsTable, e.Key, maintenanceRun{
		Key: e.Key, RanAt: time.Now().UTC(), By: e.By, Changed: e.Changed,
		Seconds: int(e.Took.Round(time.Second) / time.Second),
		Outcome: e.Outcome, Failed: e.Failed, Stopped: e.Stopped,
	})
}

// lastMaintenanceRun reads the record for one key.
func (a *AdminApp) lastMaintenanceRun(key string) (maintenanceRun, bool) {
	if a == nil || a.db == nil || key == "" {
		return maintenanceRun{}, false
	}
	var out maintenanceRun
	if !a.db.Get(maintenanceRunsTable, key, &out) || out.RanAt.IsZero() {
		return maintenanceRun{}, false
	}
	return out, true
}

// maintenanceHistoryLine is the sentence under a maintenance button.
//
// "Never run" is the important one and is said plainly. It is the state every
// one of these starts in and the state that a decision about scheduling turns
// on, and a blank line there reads as the feature being new rather than as the
// pass never having happened.
func maintenanceHistoryLine(run maintenanceRun, ok bool) string {
	if !ok {
		return "Never run."
	}
	line := "Last run " + relativeWhen(run.RanAt)
	if run.By != "" {
		line += " by " + run.By
	} else {
		line += " on its own"
	}
	switch {
	case run.Failed:
		line += ", failed"
	case run.Stopped:
		line += ", stopped"
	}
	// The pass's own words say more than a count ("8,400 checked - 340
	// re-embedded - 12 still failing"). Without them the count is said even
	// when zero: "0 changed" is a real answer meaning there was nothing to
	// do, and omitting it makes a clean run look like a run with no result.
	if run.Outcome != "" {
		line += ": " + run.Outcome
	} else {
		line += fmt.Sprintf(", %d changed", run.Changed)
	}
	if run.Seconds >= 60 {
		line += fmt.Sprintf(", took %dm", run.Seconds/60)
	} else if run.Seconds > 0 {
		line += fmt.Sprintf(", took %ds", run.Seconds)
	}
	return line + "."
}

// failedMaintenance is a sentence per pass whose last run failed, for the
// System Status panel, sorted by label. A pass that failed and then ran
// cleanly is not on it: the record is the LAST run, so the line clears itself.
func (a *AdminApp) failedMaintenance() []string {
	var out []string
	for _, f := range ListMaintenanceFuncs() {
		run, ok := a.lastMaintenanceRun(f.Key)
		if !ok || !run.Failed {
			continue
		}
		line := f.Label + " failed " + relativeWhen(run.RanAt)
		if run.Outcome != "" {
			line += ": " + run.Outcome
		}
		out = append(out, line)
	}
	sort.Strings(out)
	return out
}

// relativeWhen says how long ago in the units a person would use. Coarse on
// purpose: the decision this informs is "recently enough or not", and a figure
// to the minute invites reading precision into a record written once a month.
func relativeWhen(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return t.Format("2 Jan 2006")
}

// maintenanceKeysNeverRun lists the registered keys with no record, sorted.
// Used by the page's summary line, so the count of things nobody has pressed is
// visible without opening every group.
func (a *AdminApp) maintenanceKeysNeverRun() []string {
	var out []string
	for _, f := range ListMaintenanceFuncs() {
		if _, ok := a.lastMaintenanceRun(f.Key); !ok {
			out = append(out, f.Key)
		}
	}
	sort.Strings(out)
	return out
}
