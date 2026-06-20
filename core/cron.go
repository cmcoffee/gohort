// Cron + daily-window parsing shared across schedulers. Lifted from
// apps/phantom's scheduler so the standing-agent runner, the unified
// trigger engine, and any future scheduled surface parse the same
// human-friendly specs instead of each app growing its own copy. The cron
// spec is deliberately small and readable — "FRI 21:30", "weekdays 17:00",
// "APR-3 09:00" — not full crontab. The window spec is "HH:MM-HH:MM".

package core

import (
	"fmt"
	"math/rand"
	"strings"
	"time"
)

var cronWeekdays = map[string]time.Weekday{
	"sun": time.Sunday, "sunday": time.Sunday,
	"mon": time.Monday, "monday": time.Monday,
	"tue": time.Tuesday, "tuesday": time.Tuesday,
	"wed": time.Wednesday, "wednesday": time.Wednesday,
	"thu": time.Thursday, "thursday": time.Thursday,
	"fri": time.Friday, "friday": time.Friday,
	"sat": time.Saturday, "saturday": time.Saturday,
}

var cronMonths = map[string]time.Month{
	"jan": time.January, "january": time.January,
	"feb": time.February, "february": time.February,
	"mar": time.March, "march": time.March,
	"apr": time.April, "april": time.April,
	"may": time.May,
	"jun": time.June, "june": time.June,
	"jul": time.July, "july": time.July,
	"aug": time.August, "august": time.August,
	"sep": time.September, "september": time.September,
	"oct": time.October, "october": time.October,
	"nov": time.November, "november": time.November,
	"dec": time.December, "december": time.December,
}

// NextCronOccurrence returns the next time after `from` that matches the
// spec. Spec format: "{days} {HH:MM}" where days is one of:
//   - a weekday name or comma-separated list: "FRI", "MON,WED,FRI"
//   - "daily" / "everyday" — every day
//   - "weekdays"           — Monday–Friday
//   - "weekends"           — Saturday–Sunday
//   - "MON-DD" month-day   — annual date, e.g. "APR-3", "DEC-25"
func NextCronOccurrence(spec string, from time.Time) (time.Time, error) {
	parts := strings.Fields(strings.TrimSpace(spec))
	if len(parts) != 2 {
		return time.Time{}, fmt.Errorf("invalid cron spec %q: expected 'DAY(S) HH:MM'", spec)
	}
	t, err := time.Parse("15:04", parts[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid time %q in cron spec: use HH:MM (24-hour)", parts[1])
	}
	hour, min := t.Hour(), t.Minute()

	// Month-day pattern: "APR-3", "DEC-25" — annual repeat.
	if idx := strings.Index(parts[0], "-"); idx > 0 {
		monthStr := strings.ToLower(parts[0][:idx])
		month, ok := cronMonths[monthStr]
		if !ok {
			return time.Time{}, fmt.Errorf("unknown month %q in cron spec", parts[0][:idx])
		}
		var day int
		if _, err := fmt.Sscanf(parts[0][idx+1:], "%d", &day); err != nil || day < 1 || day > 31 {
			return time.Time{}, fmt.Errorf("invalid day %q in cron spec", parts[0][idx+1:])
		}
		for _, year := range []int{from.Year(), from.Year() + 1} {
			c := time.Date(year, month, day, hour, min, 0, 0, from.Location())
			if c.Month() != month {
				continue // day overflowed (e.g. Feb-30)
			}
			if c.After(from) {
				return c, nil
			}
		}
		return time.Time{}, fmt.Errorf("no occurrence found for cron spec %q", spec)
	}

	daySet := make(map[time.Weekday]bool)
	switch strings.ToLower(parts[0]) {
	case "daily", "everyday":
		for d := time.Sunday; d <= time.Saturday; d++ {
			daySet[d] = true
		}
	case "weekdays":
		for _, d := range []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday} {
			daySet[d] = true
		}
	case "weekends":
		daySet[time.Saturday] = true
		daySet[time.Sunday] = true
	default:
		for _, token := range strings.Split(parts[0], ",") {
			wd, ok := cronWeekdays[strings.ToLower(strings.TrimSpace(token))]
			if !ok {
				return time.Time{}, fmt.Errorf("unknown day %q in cron spec", token)
			}
			daySet[wd] = true
		}
	}

	// Search up to 8 days ahead for the next matching slot.
	base := time.Date(from.Year(), from.Month(), from.Day(), hour, min, 0, 0, from.Location())
	for i := 0; i <= 7; i++ {
		c := base.AddDate(0, 0, i)
		if daySet[c.Weekday()] && c.After(from) {
			return c, nil
		}
	}
	return time.Time{}, fmt.Errorf("no occurrence found for cron spec %q", spec)
}

// ParseWindowBounds parses a "HH:MM-HH:MM" window spec into
// (startHour, startMin, endHour, endMin).
func ParseWindowBounds(spec string) (sh, sm, eh, em int, err error) {
	parts := strings.SplitN(spec, "-", 2)
	if len(parts) != 2 {
		return 0, 0, 0, 0, fmt.Errorf("invalid window %q: expected HH:MM-HH:MM", spec)
	}
	var st, et time.Time
	if st, err = time.Parse("15:04", strings.TrimSpace(parts[0])); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("invalid start time in window %q: use HH:MM", spec)
	}
	if et, err = time.Parse("15:04", strings.TrimSpace(parts[1])); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("invalid end time in window %q: use HH:MM", spec)
	}
	return st.Hour(), st.Minute(), et.Hour(), et.Minute(), nil
}

// NextRandomWindowTime returns the next fire time for a HH:MM-HH:MM daily
// window using slot-based distribution: the window is split into N equal
// slots and the next fire is placed at a random instant inside the slot
// matching firedSoFar. If that slot is already in the past (mid-day enable,
// missed fire, server restart), it advances to the next valid slot. When N
// slots are exhausted today, it rolls over to slot 0 of tomorrow's window.
//
// minGap is a safety margin to prevent back-to-back fires when slots are
// short — the chosen instant is shifted into the next slot if it would land
// inside (now + minGap).
//
// N must be >= 1; firedSoFar should be the number of fires already completed
// in today's window (callers that just want one random time per day pass
// n=1, firedSoFar=0).
// cronMinGap is the safety margin that prevents back-to-back fires when
// scheduling slots are short.
func cronMinGap() time.Duration { return TuneDuration("tune_cron_min_gap") }

func init() {
	RegisterTunable(TunableSpec{Key: "tune_cron_min_gap", Category: "Limits", Label: "Cron minimum gap", Help: "Safety margin that shifts a randomly-scheduled fire into the next slot to avoid back-to-back runs.", Kind: KindMinutes, Default: 20, Min: 1, Max: 120})
}

func NextRandomWindowTime(spec string, from time.Time, n, firedSoFar int) (time.Time, error) {
	sh, sm, eh, em, err := ParseWindowBounds(spec)
	if err != nil {
		return time.Time{}, err
	}
	if n < 1 {
		n = 1
	}

	minGap := cronMinGap()

	loc := from.Location()
	windowStart := func(day time.Time) time.Time {
		return time.Date(day.Year(), day.Month(), day.Day(), sh, sm, 0, 0, loc)
	}
	windowEnd := func(day time.Time) time.Time {
		return time.Date(day.Year(), day.Month(), day.Day(), eh, em, 0, 0, loc)
	}

	earliest := from.Add(minGap)

	// pickInSlot returns a random time within slot[i] of [wStart, wEnd], shifting
	// past `earliest` if needed. Returns (time, true) if a valid time was found,
	// (zero, false) if even the slot's tail is before earliest (slot is in past).
	pickInSlot := func(wStart, wEnd time.Time, i int) (time.Time, bool) {
		span := wEnd.Sub(wStart)
		slotWidth := span / time.Duration(n)
		slotStart := wStart.Add(slotWidth * time.Duration(i))
		slotEnd := wStart.Add(slotWidth * time.Duration(i+1))
		// Bound the effective window of this slot by the earliest-allowed time.
		if slotEnd.Before(earliest) {
			return time.Time{}, false
		}
		effective := slotStart
		if earliest.After(effective) {
			effective = earliest
		}
		if !effective.Before(slotEnd) {
			return time.Time{}, false
		}
		jitterRange := slotEnd.Sub(effective)
		jitter := time.Duration(rand.Int63n(int64(jitterRange) + 1))
		if jitter >= jitterRange {
			jitter = jitterRange - 1
		}
		return effective.Add(jitter), true
	}

	for _, day := range []time.Time{from, from.AddDate(0, 0, 1)} {
		wStart := windowStart(day)
		wEnd := windowEnd(day)
		if !wEnd.After(wStart) {
			continue
		}
		startSlot := firedSoFar
		if !day.Equal(from) || day.After(from) && !sameYMD(day, from) {
			// Tomorrow rolls over to slot 0 — yesterday's fires don't count.
			startSlot = 0
		}
		if startSlot >= n {
			// Today's slots are all spoken for; let the loop fall through to tomorrow.
			continue
		}
		for i := startSlot; i < n; i++ {
			if t, ok := pickInSlot(wStart, wEnd, i); ok {
				return t, nil
			}
		}
	}

	// Fallback: slot 0 of the window two days out.
	day2 := from.AddDate(0, 0, 2)
	wStart := windowStart(day2)
	wEnd := windowEnd(day2)
	if !wEnd.After(wStart) {
		return time.Time{}, fmt.Errorf("window %q has zero or negative duration", spec)
	}
	if t, ok := pickInSlot(wStart, wEnd, 0); ok {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("could not find a slot for window %q", spec)
}

// sameYMD reports whether two times fall on the same calendar day in their
// respective locations.
func sameYMD(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// WindowDurationHours returns the duration of an HH:MM-HH:MM window in hours.
// Returns 0 if the spec is malformed or the window has zero/negative span.
func WindowDurationHours(spec string) float64 {
	sh, sm, eh, em, err := ParseWindowBounds(spec)
	if err != nil {
		return 0
	}
	mins := (eh*60 + em) - (sh*60 + sm)
	if mins <= 0 {
		return 0
	}
	return float64(mins) / 60.0
}
