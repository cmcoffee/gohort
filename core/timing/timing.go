// Package timing is when something on the Scheduler fires, the same way for
// every kind of thing on it.
//
// Scheduled agents, machine and pipeline runs, recurring tasks and link groups
// used to describe their timing three different ways (a cron-like string, an
// interval, a pattern with its own random modes), and each edit dialog spoke
// its own dialect. A Spec is one description: which days, how often on those
// days, an optional daily window, and when it stops. Next is the one rule for
// when it fires; Label is the one way to say it.
//
// Pure: no store, no clock of its own, randomness passed in, so the rule can be
// tested exactly and every caller gets the same answer.
package timing

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// How a Spec fires on the days it runs.
const (
	HowDaily       = "daily"        // once a day, at Times[0]
	HowTimes       = "times"        // at each of Times
	HowEvery       = "every"        // every EverySeconds, inside the window
	HowRandomCount = "random_count" // PerDay times a day at random, inside the window
	HowRandomGap   = "random_gap"   // at random gaps of MinGap to MaxGap, inside the window
	HowYearly      = "yearly"       // once a year, on Month/Day at Times[0]
)

// Day masks: bit d is time.Weekday d (Sunday is bit 0).
const (
	AllDays  uint8 = 0x7f
	Weekdays uint8 = 1<<time.Monday | 1<<time.Tuesday | 1<<time.Wednesday | 1<<time.Thursday | 1<<time.Friday
	Weekends uint8 = 1<<time.Saturday | 1<<time.Sunday
)

// Spec is when something fires.
type Spec struct {
	// Days it runs on, as a weekday mask. Zero means every day.
	Days uint8 `json:"days,omitempty"`
	// How it fires on those days: one of the How constants.
	How string `json:"how"`
	// Times of day, minutes after local midnight, for HowDaily / HowTimes,
	// and HowYearly's one time.
	Times []int `json:"times,omitempty"`
	// Month (1-12) and Day (1-31) for HowYearly. Feb 29 fires in leap years.
	Month int `json:"month,omitempty"`
	Day   int `json:"day,omitempty"`
	// EverySeconds is the interval for HowEvery. Seconds, so a monitor's
	// "every 30 seconds" is the same rule as a schedule's "every 2 hours".
	EverySeconds int `json:"every_seconds,omitempty"`
	// PerDay is how many random fires a day for HowRandomCount.
	PerDay int `json:"per_day,omitempty"`
	// MinGapSeconds / MaxGapSeconds bound each random gap for HowRandomGap.
	MinGapSeconds int `json:"min_gap_seconds,omitempty"`
	MaxGapSeconds int `json:"max_gap_seconds,omitempty"`
	// The daily window, minutes after local midnight, for the every and
	// random kinds: a fire that would land outside it waits for the next
	// opening on a day it runs. Set times ignore it; they are their own times.
	HasWindow  bool `json:"has_window,omitempty"`
	WindowFrom int  `json:"window_from,omitempty"`
	WindowTo   int  `json:"window_to,omitempty"`
	// Stops. Whichever comes first ends it; neither set is indefinitely. A goal
	// ("until X is true") lives on the schedule, which has the judge.
	StopAt  time.Time `json:"stop_at,omitzero"`
	MaxRuns int       `json:"max_runs,omitempty"`
}

// Set reports whether the Spec says anything at all.
func (s Spec) Set() bool { return s.How != "" }

func (s Spec) runsOn(d time.Weekday) bool { return s.Days == 0 || s.Days&(1<<uint(d)) != 0 }

func dayOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func minuteOf(t time.Time) int { return t.Hour()*60 + t.Minute() }

// Validate refuses a Spec that cannot fire, in words a person can act on.
func (s Spec) Validate() error {
	if s.Days&^AllDays != 0 {
		return errors.New("days has an unknown day in it")
	}
	if s.Days != 0 && s.Days&AllDays == 0 {
		return errors.New("pick at least one day")
	}
	if s.HasWindow {
		if s.WindowFrom < 0 || s.WindowTo > 24*60 || s.WindowTo <= s.WindowFrom {
			return errors.New("the time window has to end after it starts, on the same day")
		}
	}
	switch s.How {
	case HowDaily, HowTimes:
		if len(s.Times) == 0 {
			return errors.New("give at least one time of day")
		}
		if s.How == HowDaily && len(s.Times) != 1 {
			return errors.New("once a day takes one time")
		}
		for _, m := range s.Times {
			if m < 0 || m >= 24*60 {
				return fmt.Errorf("%d is not a time of day", m)
			}
		}
	case HowEvery:
		if s.EverySeconds < 1 {
			return errors.New("the interval has to be at least a second")
		}
	case HowRandomCount:
		if s.PerDay < 1 || s.PerDay > 96 {
			return errors.New("times a day at random has to be between 1 and 96")
		}
		if s.windowMinutes()/s.PerDay < 1 {
			return errors.New("the window is too short for that many times a day")
		}
	case HowRandomGap:
		if s.MinGapSeconds < 1 || s.MaxGapSeconds <= s.MinGapSeconds {
			return errors.New("the longest gap has to be longer than the shortest")
		}
	case HowYearly:
		if s.Month < 1 || s.Month > 12 {
			return errors.New("pick a month")
		}
		if s.Day < 1 || s.Day > daysInMonth(s.Month) {
			return fmt.Errorf("%s has no day %d", time.Month(s.Month), s.Day)
		}
		if len(s.Times) != 1 || s.Times[0] < 0 || s.Times[0] >= 24*60 {
			return errors.New("once a year takes one time of day")
		}
	case "":
		return errors.New("say how often it runs")
	default:
		return fmt.Errorf("unknown how %q", s.How)
	}
	return nil
}

// daysInMonth is the most days month (1-12) can have: Feb counts 29, so
// Feb 29 is a date that fires in leap years.
func daysInMonth(month int) int {
	switch time.Month(month) {
	case time.February:
		return 29
	case time.April, time.June, time.September, time.November:
		return 30
	}
	return 31
}

// windowMinutes is the length of the day it may fire in.
func (s Spec) windowMinutes() int {
	if s.HasWindow {
		return s.WindowTo - s.WindowFrom
	}
	return 24 * 60
}

func (s Spec) windowStart() int {
	if s.HasWindow {
		return s.WindowFrom
	}
	return 0
}

// open reports whether t is a moment it may fire: a day it runs, inside the
// window.
func (s Spec) open(t time.Time) bool {
	if !s.runsOn(t.Weekday()) {
		return false
	}
	if !s.HasWindow {
		return true
	}
	m := minuteOf(t)
	return m >= s.WindowFrom && m < s.WindowTo
}

// nextOpening is t when it may fire then, otherwise the start of the next
// opening after it: later that day when t is before the window, else the
// window's start on the next day it runs.
func (s Spec) nextOpening(t time.Time) (time.Time, bool) {
	if s.open(t) {
		return t, false
	}
	day := dayOf(t)
	start := day.Add(time.Duration(s.windowStart()) * time.Minute)
	if s.runsOn(t.Weekday()) && t.Before(start) {
		return start, true
	}
	for i := 1; i <= 8; i++ {
		d := day.AddDate(0, 0, i)
		if s.runsOn(d.Weekday()) {
			return d.Add(time.Duration(s.windowStart()) * time.Minute), true
		}
	}
	return t, false // unreachable with a valid Days
}

// Next is when it next fires after from, in from's location (the owner's).
// rnd returns a number in [0,1); random kinds use it and nothing else does.
func (s Spec) Next(from time.Time, rnd func() float64) (time.Time, error) {
	if err := s.Validate(); err != nil {
		return time.Time{}, err
	}
	if rnd == nil {
		rnd = func() float64 { return 0.5 }
	}
	switch s.How {
	case HowYearly:
		// This year's date, else the next year it exists in (Feb 29).
		for y := from.Year(); y <= from.Year()+8; y++ {
			c := time.Date(y, time.Month(s.Month), s.Day, 0, 0, 0, 0, from.Location()).Add(time.Duration(s.Times[0]) * time.Minute)
			if c.Month() != time.Month(s.Month) || c.Day() != s.Day {
				continue // no such day this year
			}
			if c.After(from) {
				return c, nil
			}
		}
	case HowDaily, HowTimes:
		times := append([]int(nil), s.Times...)
		sort.Ints(times)
		day := dayOf(from)
		for i := 0; i <= 8; i++ {
			d := day.AddDate(0, 0, i)
			if !s.runsOn(d.Weekday()) {
				continue
			}
			for _, m := range times {
				if c := d.Add(time.Duration(m) * time.Minute); c.After(from) {
					return c, nil
				}
			}
		}
	case HowEvery:
		t, _ := s.nextOpening(from.Add(time.Duration(s.EverySeconds) * time.Second))
		return t, nil
	case HowRandomGap:
		spread := float64(s.MaxGapSeconds - s.MinGapSeconds)
		c := from.Add(time.Duration(float64(s.MinGapSeconds)+rnd()*spread) * time.Second)
		t, moved := s.nextOpening(c)
		if moved {
			// Landing on the window's first minute every morning is not random:
			// spread the first fire of an opening across the first gap.
			off := time.Duration(rnd()*spread) * time.Second
			if lim := time.Duration(s.windowMinutes()) * time.Minute; off >= lim {
				off = lim / 2
			}
			t = t.Add(off)
		}
		return t, nil
	case HowRandomCount:
		// The window is cut into PerDay equal slots and each fire lands at a
		// random moment in the next slot, so a day gets PerDay fires spread
		// across it with no state to keep between them.
		slot := time.Duration(s.windowMinutes()) * time.Minute / time.Duration(s.PerDay)
		day := dayOf(from)
		startOf := func(d time.Time) time.Time { return d.Add(time.Duration(s.windowStart()) * time.Minute) }
		pick := func(d time.Time, k int) time.Time {
			return startOf(d).Add(time.Duration(k)*slot + time.Duration(rnd()*float64(slot)))
		}
		if s.runsOn(from.Weekday()) {
			if since := from.Sub(startOf(day)); since < 0 {
				return pick(day, 0), nil
			} else if k := int(since/slot) + 1; k < s.PerDay {
				return pick(day, k), nil
			}
		}
		for i := 1; i <= 8; i++ {
			if d := day.AddDate(0, 0, i); s.runsOn(d.Weekday()) {
				return pick(d, 0), nil
			}
		}
	}
	return time.Time{}, errors.New("no time to fire was found")
}

// Done reports whether a stop has been reached: the date has passed, or it has
// run as many times as it was allowed.
func (s Spec) Done(now time.Time, runs int) bool {
	if !s.StopAt.IsZero() && !now.Before(s.StopAt) {
		return true
	}
	return s.MaxRuns > 0 && runs >= s.MaxRuns
}

// --- words ---------------------------------------------------------------

var dayShort = [...]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}

// DaysLabel names a day mask: "every day", "weekdays", "weekends", or the days.
func DaysLabel(days uint8) string {
	switch days {
	case 0, AllDays:
		return "every day"
	case Weekdays:
		return "weekdays"
	case Weekends:
		return "weekends"
	}
	var names []string
	for _, d := range []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday, time.Sunday} {
		if days&(1<<uint(d)) != 0 {
			names = append(names, dayShort[d])
		}
	}
	return strings.Join(names, ", ")
}

// HHMM is minutes after midnight as "HH:MM".
func HHMM(m int) string { return fmt.Sprintf("%02d:%02d", m/60, m%60) }

func durationWords(secs int) string {
	switch {
	case secs%86400 == 0:
		return plural(secs/86400, "day")
	case secs%3600 == 0:
		return plural(secs/3600, "hour")
	case secs%60 == 0:
		return plural(secs/60, "minute")
	}
	return plural(secs, "second")
}

func plural(n int, unit string) string {
	if n == 1 {
		return unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

func gapWords(lo, hi int) string {
	switch {
	case lo%3600 == 0 && hi%3600 == 0:
		return fmt.Sprintf("%d-%d hours", lo/3600, hi/3600)
	case lo%60 == 0 && hi%60 == 0:
		return fmt.Sprintf("%d-%d minutes", lo/60, hi/60)
	}
	return fmt.Sprintf("%d-%d seconds", lo, hi)
}

// Label says when it fires, in one line: "weekdays at 09:00", "every 30
// minutes between 09:00 and 17:00", "4 times a day at random, until Nov 30".
func (s Spec) Label() string {
	days := DaysLabel(s.Days)
	var b strings.Builder
	switch s.How {
	case HowDaily, HowTimes:
		var ts []string
		times := append([]int(nil), s.Times...)
		sort.Ints(times)
		for _, m := range times {
			ts = append(ts, HHMM(m))
		}
		b.WriteString(days + " at " + strings.Join(ts, ", "))
	case HowEvery:
		b.WriteString("every " + durationWords(s.EverySeconds))
	case HowRandomCount:
		b.WriteString(fmt.Sprintf("%d times a day at random", s.PerDay))
		if s.PerDay == 1 {
			b.Reset()
			b.WriteString("once a day at random")
		}
	case HowRandomGap:
		b.WriteString("at random, every " + gapWords(s.MinGapSeconds, s.MaxGapSeconds))
	case HowYearly:
		b.WriteString(fmt.Sprintf("every year on %s %d at %s", time.Month(s.Month).String()[:3], s.Day, HHMM(s.Times[0])))
	default:
		return "(no schedule)"
	}
	if s.How != HowDaily && s.How != HowTimes && s.How != HowYearly {
		if s.HasWindow {
			b.WriteString(" between " + HHMM(s.WindowFrom) + " and " + HHMM(s.WindowTo))
		}
		if s.Days != 0 && s.Days != AllDays {
			b.WriteString(", " + days)
		}
	}
	if !s.StopAt.IsZero() {
		// StopAt is the moment it ends; a date given as "until Nov 30" ends at
		// the start of Dec 1, so the label names the last day it runs.
		b.WriteString(", until " + s.StopAt.Add(-time.Second).Format("Jan 2"))
	}
	if s.MaxRuns > 0 {
		b.WriteString(", " + plural(s.MaxRuns, "run") + " in all")
	}
	return b.String()
}

// --- the old forms ---------------------------------------------------------

var weekdayNames = map[string]time.Weekday{
	"sun": time.Sunday, "sunday": time.Sunday,
	"mon": time.Monday, "monday": time.Monday,
	"tue": time.Tuesday, "tuesday": time.Tuesday,
	"wed": time.Wednesday, "wednesday": time.Wednesday,
	"thu": time.Thursday, "thursday": time.Thursday,
	"fri": time.Friday, "friday": time.Friday,
	"sat": time.Saturday, "saturday": time.Saturday,
}

// FromCron reads the "{days} HH:MM" form scheduled agents have always stored
// ("daily 09:00", "weekdays 17:00", "MON,WED 08:30"), and an annual date
// ("DEC-25 09:00") as HowYearly.
func FromCron(spec string) (Spec, bool) {
	parts := strings.Fields(strings.TrimSpace(spec))
	if len(parts) != 2 {
		return Spec{}, false
	}
	t, err := time.Parse("15:04", parts[1])
	if err != nil {
		return Spec{}, false
	}
	at := t.Hour()*60 + t.Minute()
	if mon, day, ok := monthDay(parts[0]); ok {
		y := Spec{How: HowYearly, Month: mon, Day: day, Times: []int{at}}
		return y, y.Validate() == nil
	}
	s := Spec{How: HowDaily, Times: []int{at}}
	switch strings.ToLower(parts[0]) {
	case "daily", "everyday":
	case "weekdays":
		s.Days = Weekdays
	case "weekends":
		s.Days = Weekends
	default:
		for _, tok := range strings.Split(parts[0], ",") {
			d, ok := weekdayNames[strings.ToLower(strings.TrimSpace(tok))]
			if !ok {
				return Spec{}, false
			}
			s.Days |= 1 << uint(d)
		}
	}
	return s, true
}

// monthDay reads an annual date's day part, "DEC-25" or "dec-25", as month
// (1-12) and day.
func monthDay(tok string) (month, day int, ok bool) {
	i := strings.Index(tok, "-")
	if i <= 0 {
		return 0, 0, false
	}
	name := strings.ToLower(tok[:i])
	for m := time.January; m <= time.December; m++ {
		full := strings.ToLower(m.String())
		if name == full || name == full[:3] {
			month = int(m)
		}
	}
	if month == 0 {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(tok[i+1:], "%d", &day); err != nil {
		return 0, 0, false
	}
	return month, day, true
}
