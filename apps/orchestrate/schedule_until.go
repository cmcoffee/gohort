package orchestrate

// Until: when something on the Scheduler stops, said the same way for every
// kind. Indefinitely is none of these set; otherwise whichever comes first:
//
//   - on a date (stop_on, the last day it runs, in the owner's zone),
//   - after a number of runs (scheduled runs, a link group's rounds, a
//     recurring task's fires, a monitor's alerts),
//   - when a goal is met (judged after each run; not for link groups, whose
//     members each carry their own).
//
// The date and the count live in the timing (core/timing.Spec: StopAt,
// MaxRuns) where a kind has one, and on the record where it does not. The goal
// was always on the record; the dialog can now set it.

import (
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// untilBody is the dialog's Until section, sent and served the same for all.
type untilBody struct {
	StopOn      string  `json:"stop_on"`  // "YYYY-MM-DD", the last day it runs; "" = no date
	MaxRuns     int     `json:"max_runs"` // 0 = no count
	Goal        *string `json:"goal"`     // nil leaves the goal as it is
	MaxAttempts int     `json:"max_attempts"`
}

// untilOut is the Until section as served: runs so far beside the limits.
type untilOut struct {
	StopOn      string `json:"stop_on"`
	MaxRuns     int    `json:"max_runs"`
	Runs        int    `json:"runs"`
	Goal        string `json:"goal"`
	MaxAttempts int    `json:"max_attempts"`
	HasGoal     bool   `json:"has_goal"` // whether this kind takes a goal at all
}

// stopAtFrom turns the last day it runs into the moment it ends: the start of
// the day after, in the owner's zone. Zero for "".
func stopAtFrom(owner, day string) (time.Time, error) {
	day = strings.TrimSpace(day)
	if day == "" {
		return time.Time{}, nil
	}
	d, err := time.ParseInLocation("2006-01-02", day, UserLocation(owner))
	if err != nil {
		return time.Time{}, Error("the end date has to be a date, like 2026-11-30")
	}
	return d.AddDate(0, 0, 1), nil
}

// stopOnOut is the last day it runs, for the dialog.
func stopOnOut(owner string, at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.Add(-time.Second).In(UserLocation(owner)).Format("2006-01-02")
}

// applyStandingUntil writes a scheduled agent's (or link group's) stops. The
// date and count go in its timing, which an older record is first given from
// its old fields; one with an annual date has no timing to carry them.
func applyStandingUntil(sa *StandingAgent, u untilBody, goals bool) error {
	if u.MaxRuns < 0 || u.MaxAttempts < 0 {
		return Error("a count cannot be negative")
	}
	stopAt, err := stopAtFrom(sa.Owner, u.StopOn)
	if err != nil {
		return err
	}
	needsTiming := !stopAt.IsZero() || u.MaxRuns > 0
	if !sa.Timing.Set() && needsTiming {
		spec, ok := sa.EffectiveTiming()
		if !ok {
			return Error("set when it runs in this dialog first: its current time cannot carry an end")
		}
		sa.Timing = spec
		sa.Cron, sa.IntervalSeconds, sa.MaxIntervalSeconds = "", 0, 0
		sa.HasWindow, sa.WindowFromMin, sa.WindowToMin = false, 0, 0
	}
	if sa.Timing.Set() {
		sa.Timing.StopAt, sa.Timing.MaxRuns = stopAt, u.MaxRuns
	}
	if goals && u.Goal != nil {
		sa.Until = strings.TrimSpace(*u.Goal)
		sa.MaxAttempts = u.MaxAttempts
		if sa.Until == "" {
			sa.MaxAttempts = 0
		}
	}
	return nil
}

func standingUntilOut(sa StandingAgent, goals bool) untilOut {
	out := untilOut{Runs: sa.Runs, HasGoal: goals}
	if spec, ok := sa.EffectiveTiming(); ok {
		out.StopOn, out.MaxRuns = stopOnOut(sa.Owner, spec.StopAt), spec.MaxRuns
	}
	if goals {
		out.Goal, out.MaxAttempts = sa.Until, sa.MaxAttempts
	}
	return out
}

// applyRecurringUntil writes a recurring task's stops onto the spec it is
// re-armed with: its date in the timing, its count as its fire cap, its goal.
func applyRecurringUntil(spec *RecurringSpec, owner string, u untilBody) error {
	if u.MaxRuns < 0 || u.MaxAttempts < 0 {
		return Error("a count cannot be negative")
	}
	stopAt, err := stopAtFrom(owner, u.StopOn)
	if err != nil {
		return err
	}
	if !stopAt.IsZero() && !spec.Timing.Set() {
		return Error("set when it runs in this dialog first: its current time cannot carry an end")
	}
	spec.Timing.StopAt = stopAt
	spec.MaxFires = u.MaxRuns
	if u.Goal != nil {
		spec.Until = strings.TrimSpace(*u.Goal)
		spec.MaxAttempts = u.MaxAttempts
		if spec.Until == "" {
			spec.MaxAttempts = 0
		}
	}
	return nil
}

func recurringUntilOut(p orchUpdatePayload) untilOut {
	return untilOut{StopOn: stopOnOut(p.Username, p.Timing.StopAt), MaxRuns: p.MaxFires, Runs: p.FireCount,
		Goal: p.Until, MaxAttempts: p.MaxAttempts, HasGoal: true}
}

// applyMonitorUntil writes a monitor's stops: a date, a number of alerts
// (its fire cap), and a goal. A monitor's goal has no attempt count: it stops
// when the thing is true.
func applyMonitorUntil(m *EventMonitor, u untilBody) error {
	if u.MaxRuns < 0 {
		return Error("a count cannot be negative")
	}
	stopAt, err := stopAtFrom(m.Owner, u.StopOn)
	if err != nil {
		return err
	}
	m.StopAt = stopAt
	m.MaxFires = u.MaxRuns
	if u.Goal != nil {
		m.Until = strings.TrimSpace(*u.Goal)
	}
	return nil
}

func monitorUntilOut(m EventMonitor) untilOut {
	return untilOut{StopOn: stopOnOut(m.Owner, m.StopAt), MaxRuns: m.MaxFires, Runs: m.FiresUsed(), Goal: m.Until, HasGoal: true}
}
