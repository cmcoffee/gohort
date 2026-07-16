// Recurring-task scheduling patterns. The `recurring` tool supports two shapes:
//
//   fixed  — fire every N minutes (the original behavior).
//   random — fire TimesPerDay times at random points inside a daily window
//            [WindowFromMin, WindowToMin] (local time), each at least MinGap
//            apart, re-rolled fresh for each day.
//
// Both shapes accept the same modifiers: an optional daily active window
// (fires outside it are deferred to the next window open), a per-task MaxFires
// cap, and — for random — a minimum gap between fires.
//
// The time-planning math here is pure and deterministic given its rng, so it is
// unit-tested in recurring_pattern_test.go. The stateful reschedule loop that
// drives it lives in scheduled_updates.go.

package orchestrate

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"time"
)

const (
	RecurringFixed  = "fixed"
	RecurringRandom = "random"
)

// RecurringSpec is the validated request the recurring(schedule) tool builds and
// hands to ScheduleOrchestrateUpdate. Struct-first so the pattern modifiers stay
// grouped instead of ballooning the function signature.
type RecurringSpec struct {
	SessionID string
	AgentID   string
	Username  string
	Prompt    string

	Pattern         string // RecurringFixed (default) | RecurringRandom
	IntervalSeconds int    // fixed: gap between fires
	TimesPerDay     int    // random: fires per active window (0 = continuous/unlimited)
	MinGapSeconds   int    // random: minimum spacing between fires
	MaxGapSeconds   int    // random (continuous): maximum spacing; 0 → 2× min

	HasWindow     bool // whether the daily active window applies
	WindowFromMin int  // window start, minutes since local midnight
	WindowToMin   int  // window end, minutes since local midnight

	MaxFires int // per-task total cap; 0 = deployment default
}

// isContinuousRandom reports the unbounded spaced-random shape: pattern=random
// with no times_per_day — fire at random gaps in [MinGap, MaxGap] indefinitely
// rather than N fixed times per window.
func (p orchUpdatePayload) isContinuousRandom() bool {
	return p.Pattern == RecurringRandom && p.TimesPerDay <= 0
}

// effectiveMaxFires resolves a payload's total-fire cap: the per-task MaxFires
// when set and below the deployment ceiling, otherwise the ceiling. A per-task
// value can only LOWER the cap, never raise it past the global guardrail —
// EXCEPT continuous spaced-random, whose whole point is "unlimited times per
// day": there MaxFires>0 is honored verbatim and 0 means no cap (run until
// cancelled). The MinGap floor is the real throttle for that mode.
func (p orchUpdatePayload) effectiveMaxFires() int {
	if p.isContinuousRandom() {
		if p.MaxFires > 0 {
			return p.MaxFires
		}
		return math.MaxInt32
	}
	ceiling := orchUpdateMaxFires()
	if p.MaxFires > 0 && p.MaxFires < ceiling {
		return p.MaxFires
	}
	return ceiling
}

// parseHHMM parses "H:MM" / "HH:MM" (24h) into minutes since midnight.
func parseHHMM(s string) (int, error) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, fmt.Errorf("time %q must be HH:MM (24-hour)", s)
	}
	var h, m int
	if _, err := fmt.Sscanf(parts[0], "%d", &h); err != nil {
		return 0, fmt.Errorf("bad hour in %q", s)
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &m); err != nil {
		return 0, fmt.Errorf("bad minute in %q", s)
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("time %q out of range (00:00–23:59)", s)
	}
	return h*60 + m, nil
}

// fmtHHMM renders minutes-since-midnight back to "HH:MM" for labels.
func fmtHHMM(min int) string {
	if min < 0 {
		min = 0
	}
	return fmt.Sprintf("%02d:%02d", (min/60)%24, min%60)
}

// startOfLocalDay returns midnight (local) of t's calendar day.
func startOfLocalDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// nextWindowOpen returns the first instant at/after t that is inside the daily
// window [fromMin, toMin]. If t is already inside, t is returned unchanged; if
// before today's window, today's start; if past it, tomorrow's start. Windows
// are same-day (fromMin < toMin), validated at schedule time.
func nextWindowOpen(t time.Time, fromMin, toMin int) time.Time {
	day := startOfLocalDay(t)
	start := day.Add(time.Duration(fromMin) * time.Minute)
	end := day.Add(time.Duration(toMin) * time.Minute)
	if t.Before(start) {
		return start
	}
	if t.Before(end) {
		return t
	}
	return start.AddDate(0, 0, 1)
}

// planRandomFireTimes returns n times within [start, end], sorted ascending and
// each at least minGap apart, drawn using randFloat (a source of values in
// [0,1)). It reserves the minimum gaps first, then scatters the remaining slack
// uniformly — guaranteeing spacing without rejection sampling. Errors when the
// window can't hold n fires at the requested spacing.
func planRandomFireTimes(start, end time.Time, n int, minGap time.Duration, randFloat func() float64) ([]time.Time, error) {
	if n <= 0 {
		return nil, errors.New("times_per_day must be >= 1")
	}
	span := end.Sub(start)
	if span <= 0 {
		return nil, errors.New("active window has no room left")
	}
	reserved := time.Duration(n-1) * minGap
	if span < reserved {
		return nil, fmt.Errorf("window %s can't hold %d fires spaced %s apart", span.Round(time.Minute), n, minGap)
	}
	slack := float64(span - reserved)
	offs := make([]float64, n)
	for i := range offs {
		offs[i] = randFloat() * slack
	}
	sort.Float64s(offs)
	out := make([]time.Time, n)
	for i := 0; i < n; i++ {
		d := time.Duration(offs[i]) + time.Duration(i)*minGap
		out[i] = start.Add(d)
	}
	return out, nil
}

// parseFutureTimes decodes RFC3339 strings, keeping only those strictly after
// now, sorted ascending. Malformed entries are skipped.
func parseFutureTimes(raw []string, now time.Time) []time.Time {
	var out []time.Time
	for _, s := range raw {
		if t, err := time.Parse(time.RFC3339, s); err == nil && t.After(now) {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

// formatTimes renders times as RFC3339 for payload storage.
func formatTimes(ts []time.Time) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Format(time.RFC3339))
	}
	return out
}

// computeNextFire resolves when the schedule should next fire, given the current
// time. For random it also MUTATES p.RemainingToday: it consumes the earliest
// planned time and stores the rest, re-planning a fresh day when the queue is
// empty. Returns an error only when a random plan can't be built (bad window).
func computeNextFire(p *orchUpdatePayload, now time.Time) (time.Time, error) {
	if p.Pattern == RecurringRandom {
		if p.isContinuousRandom() {
			return nextSpacedRandomFire(p, now, rand.Float64), nil
		}
		return nextRandomFire(p, now, rand.Float64)
	}
	next := now.Add(time.Duration(p.IntervalSeconds) * time.Second)
	if p.HasWindow {
		next = nextWindowOpen(next, p.WindowFromMin, p.WindowToMin)
	}
	return next, nil
}

// nextSpacedRandomFire schedules the next fire a random gap in [MinGap, MaxGap]
// from now, clamped into the daily window when one is set. Count is unbounded —
// the min gap is the throttle. Never errors: with no window it's always a valid
// future time; with a window it defers to the next open.
func nextSpacedRandomFire(p *orchUpdatePayload, now time.Time, randFloat func() float64) time.Time {
	minGap := time.Duration(p.MinGapSeconds) * time.Second
	maxGap := time.Duration(p.MaxGapSeconds) * time.Second
	if maxGap < minGap {
		maxGap = minGap
	}
	gap := minGap + time.Duration(randFloat()*float64(maxGap-minGap))
	next := now.Add(gap)
	if p.HasWindow {
		next = nextWindowOpen(next, p.WindowFromMin, p.WindowToMin)
	}
	return next
}

// nextRandomFire pops the next fire from the current day's plan, re-planning the
// next window occurrence when the queue is exhausted. randFloat is injected so
// the logic is testable; production passes rand.Float64.
func nextRandomFire(p *orchUpdatePayload, now time.Time, randFloat func() float64) (time.Time, error) {
	future := parseFutureTimes(p.RemainingToday, now)
	if len(future) == 0 {
		open := nextWindowOpen(now, p.WindowFromMin, p.WindowToMin)
		end := startOfLocalDay(open).Add(time.Duration(p.WindowToMin) * time.Minute)
		times, err := planRandomFireTimes(open, end, p.TimesPerDay, time.Duration(p.MinGapSeconds)*time.Second, randFloat)
		if err != nil {
			return time.Time{}, err
		}
		future = times
	}
	next := future[0]
	p.RemainingToday = formatTimes(future[1:])
	return next, nil
}

// recurringDetail renders a one-line human summary of a schedule's cadence for
// the Schedules rail, introspect, and the admin describer.
func recurringDetail(p orchUpdatePayload) string {
	win := ""
	if p.HasWindow {
		win = fmt.Sprintf(" · %s–%s", fmtHHMM(p.WindowFromMin), fmtHHMM(p.WindowToMin))
	}
	if p.Pattern == RecurringRandom {
		if p.isContinuousRandom() {
			return fmt.Sprintf("random · every %d–%dm%s", p.MinGapSeconds/60, effectiveMaxGapMin(p.MinGapSeconds, p.MaxGapSeconds), win)
		}
		gap := ""
		if p.MinGapSeconds > 0 {
			gap = fmt.Sprintf(" (≥%dm apart)", p.MinGapSeconds/60)
		}
		return fmt.Sprintf("random · %d×/day%s%s", p.TimesPerDay, win, gap)
	}
	return fmt.Sprintf("recurring · every %dm%s", p.IntervalSeconds/60, win)
}

// effectiveMaxGapMin returns the continuous-mode max gap in minutes, applying
// the "2× min when unset" default so labels match what the scheduler will use.
func effectiveMaxGapMin(minSec, maxSec int) int {
	if maxSec <= minSec {
		maxSec = minSec * 2
	}
	return maxSec / 60
}

// specCadence renders a RecurringSpec's cadence as a natural phrase for the
// schedule confirmation string ("every 30 min", "at 5 random times per day
// between 09:00 and 17:00, at least 30m apart").
func specCadence(s RecurringSpec) string {
	win := ""
	if s.HasWindow {
		win = fmt.Sprintf(" between %s and %s", fmtHHMM(s.WindowFromMin), fmtHHMM(s.WindowToMin))
	}
	if s.Pattern == RecurringRandom {
		if s.TimesPerDay <= 0 {
			return fmt.Sprintf("at random times %d–%dm apart%s, unlimited per day", s.MinGapSeconds/60, effectiveMaxGapMin(s.MinGapSeconds, s.MaxGapSeconds), win)
		}
		gap := ""
		if s.MinGapSeconds > 0 {
			gap = fmt.Sprintf(", at least %dm apart", s.MinGapSeconds/60)
		}
		return fmt.Sprintf("at %d random time(s) per day%s%s", s.TimesPerDay, win, gap)
	}
	return fmt.Sprintf("every %d min%s", s.IntervalSeconds/60, win)
}
