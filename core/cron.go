// Cron parsing shared across schedulers. Lifted from apps/phantom's
// scheduler so the standing-agent runner (and any future scheduled
// surface) parses the same human-friendly spec instead of each app
// growing its own copy. The spec is deliberately small and readable —
// "FRI 21:30", "weekdays 17:00", "APR-3 09:00" — not full crontab.
//
// (phantom still has its own private copy for now; folding it onto this
// one is a mechanical follow-up tracked in the lift-to-core backlog.)

package core

import (
	"fmt"
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
