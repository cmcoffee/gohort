package orchestrate

// What a scheduled row says about itself beyond "active".
//
// Three records run on a clock and each had grown its own answer to "how is it
// going": a recurring task put its objective in the state cell, a monitor
// appended it to a detail string, and a standing agent said nothing at all. A
// failing streak was invisible on all three until it ended in a park or pushed
// the next fire somewhere nobody chose — leaving a row reading "active", a next
// run hours off its cadence, and nothing joining the two.
//
// One vocabulary now, the same on all three: State is whether it is running,
// Objective is where its goal stands, Failing is whether it is in trouble. The
// labels are built here so the three rows cannot drift into saying the same
// thing three ways again.

import (
	"fmt"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// scheduleFailingLabel is what a schedule in trouble says on its row. Empty
// while nothing is wrong, so a healthy row gains no line at all.
//
// Both halves matter. The count says whether this is one bad run or six, and
// the time says when to expect the next word — a schedule that has gone quiet
// because it backed off reads nothing like one that has stopped. stopsAt is the
// streak at which this kind of schedule parks itself, 0 where none does.
func scheduleFailingLabel(streak, stopsAt int, nextAt time.Time, loc *time.Location) string {
	if streak <= 0 {
		return ""
	}
	label := fmt.Sprintf("failed %d time(s) in a row", streak)
	if stopsAt > 0 {
		label += fmt.Sprintf(" (stops at %d)", stopsAt)
	}
	if !nextAt.IsZero() && loc != nil {
		label += ", next try " + nextAt.In(loc).Format("Mon 2006-01-02 15:04")
	}
	return label
}

// parseSchedTime reads a stored RFC3339 stamp, returning the zero time for an
// empty or unparseable one. The recurring payload keeps its next-attempt time
// as a string (it rides a scheduler payload); the standing record keeps a
// time.Time. One reader here rather than a parse at each call site.
func parseSchedTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// monitorNextRun is when a monitor next checks, in the same RFC3339 UTC form
// the other two scheduled kinds report. Empty where there is no next check to
// name: a push-triggered monitor is not on a clock at all, and one that is
// paused or parked has a stored NextCheck left over from before it stopped,
// which would read as a promise it is not going to keep.
func monitorNextRun(m EventMonitor) string {
	if !IsScheduledEventKind(m.Kind) || m.Paused || m.Broken || m.NextCheck.IsZero() {
		return ""
	}
	return m.NextCheck.UTC().Format(time.RFC3339)
}

// monitorNeedsRearm reports whether an edit has to replace the monitor's armed
// check.
//
// A timing change obviously does. An edit that only changed what it watches for
// or what it tells the agent does NOT, and re-arming anyway is not harmless:
// ScheduleEventMonitor computes the next check as now plus the WHOLE interval,
// so a one-word fix to a six-hourly watch would push its next look six hours
// out. Before the editor reached the condition, an interval change was the only
// edit there was, and resetting the clock came free with it.
//
// A record that is not armed re-arms regardless of what changed: a save is a
// chance to repair one that lost its task, and it had no clock to preserve.
func monitorNeedsRearm(before, after EventMonitor) bool {
	if before.IntervalSeconds != after.IntervalSeconds {
		return true
	}
	return after.SchedulerID == "" || after.NextCheck.IsZero() || after.NextCheck.Before(time.Now())
}

// standingNeedsRearm is the same judgement for a scheduled agent: its cron or
// its interval moved, or it is not currently armed. A cron schedule re-arms to
// the same instant either way, so this only really spares the interval ones.
func standingNeedsRearm(before, after StandingAgent) bool {
	if before.Cron != after.Cron || before.IntervalSeconds != after.IntervalSeconds {
		return true
	}
	return after.SchedulerID == "" || after.NextRun.IsZero() || after.NextRun.Before(time.Now())
}
