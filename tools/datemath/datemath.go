// Package datemath provides a chat tool for computing date differences
// and adding/subtracting day offsets from a given date, and for reading Unix
// timestamps. Complements the timezone tool (current time + zone conversion):
// datemath answers "how far between X and Y", "X plus N days" and "when was
// 1793305971".
package datemath

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// now is the clock "ago" counts from; a var so a test can fix it.
var now = time.Now

func init() { RegisterChatTool(new(DateMathTool)) }

// DateMathTool handles two operations: "diff" (days between two dates)
// and "add" (offset days from a base date). Kept as a single tool with
// a mode argument rather than split into two to keep the tool registry
// compact and to let the LLM remember one name.
type DateMathTool struct{}

func (t *DateMathTool) Name() string       { return "date_math" }
func (t *DateMathTool) Caps() []Capability { return []Capability{CapRead} } // pure transform, no side effects

func (t *DateMathTool) Desc() string {
	return `Compute date arithmetic, and read Unix timestamps. Three operations:
- operation="diff", date1, date2 → days between the two dates (date2 relative to date1).
- operation="add", date1, days → add days to date1 (days may be negative to subtract).
- operation="unix", value → a Unix timestamp as a date and time in the user's zone, with how long ago it was; or a date or date-time as its timestamp. Use this for any epoch value in an API response or a log rather than converting it yourself.
Dates accept YYYY-MM-DD, "April 16, 2026", "Apr 16, 2026", "04/16/2026", or a Unix timestamp in seconds, milliseconds or microseconds ("1793305971", "1793305971123", Slack's "1793305971.123456").`
}

func (t *DateMathTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"operation": {Type: "string", Description: `"diff" for days between two dates, "add" for offset from a date, or "unix" to read or make a Unix timestamp.`},
		"date1":     {Type: "string", Description: "First date (for diff) or base date (for add). YYYY-MM-DD, common formats, or a Unix timestamp."},
		"date2":     {Type: "string", Description: "Second date: diff only."},
		"days":      {Type: "integer", Description: "Days to add: add only. Negative subtracts."},
		"value":     {Type: "string", Description: `unix only: a timestamp to read ("1793305971"), or a date or date-time to turn into one ("2026-10-29 15:12", "2026-10-29T22:12:51Z").`},
	}
}

func (t *DateMathTool) Run(args map[string]any) (string, error) {
	return t.run(args, time.Local)
}

// RunWithSession reads dates in the zone of the user it runs for, so a
// timestamp's day and time are theirs.
func (t *DateMathTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	loc := time.Local
	if sess != nil {
		loc = UserLocation(sess.Username)
	}
	return t.run(args, loc)
}

func (t *DateMathTool) run(args map[string]any, loc *time.Location) (string, error) {
	op := strings.ToLower(strings.TrimSpace(asString(args["operation"])))
	if op == "" {
		return "", fmt.Errorf(`operation is required ("diff", "add" or "unix")`)
	}

	switch op {
	case "unix":
		v := strings.TrimSpace(asString(args["value"]))
		if v == "" {
			v = strings.TrimSpace(asString(args["date1"]))
		}
		if v == "" {
			return "", fmt.Errorf("unix requires value: a timestamp, or a date or date-time")
		}
		if ts, ok := parseUnix(v); ok {
			return fmt.Sprintf("%s = %s (%s UTC), %s", v, ts.In(loc).Format("Mon, Jan 2, 2006 3:04:05 PM MST"),
				ts.UTC().Format("2006-01-02 15:04:05"), ago(ts, now())), nil
		}
		ts, err := parseDateTime(v, loc)
		if err != nil {
			return "", fmt.Errorf("value: %w", err)
		}
		return fmt.Sprintf("%s = %d (seconds), %d (milliseconds)", ts.Format("Mon, Jan 2, 2006 3:04:05 PM MST"), ts.Unix(), ts.UnixMilli()), nil

	case "diff":
		d1str := asString(args["date1"])
		d2str := asString(args["date2"])
		if d1str == "" || d2str == "" {
			return "", fmt.Errorf("diff requires date1 and date2")
		}
		d1, err := parseDateIn(d1str, loc)
		if err != nil {
			return "", fmt.Errorf("date1: %w", err)
		}
		d2, err := parseDateIn(d2str, loc)
		if err != nil {
			return "", fmt.Errorf("date2: %w", err)
		}
		// Day-granularity difference. Computed as calendar-day delta
		// from d1's date to d2's date, ignoring clock time so DST
		// transitions don't flip results by ±1.
		days := int(d2.Sub(d1).Round(24*time.Hour) / (24 * time.Hour))
		absDays := days
		if absDays < 0 {
			absDays = -absDays
		}
		weeks := float64(absDays) / 7.0
		return fmt.Sprintf("%s to %s = %d days (%.1f weeks)",
			d1.Format("2006-01-02 (Mon)"),
			d2.Format("2006-01-02 (Mon)"),
			days,
			weeks,
		), nil

	case "add":
		// Accept "date1" or "date" as the base-date key — the LLM
		// sometimes picks the shorter name naturally.
		dstr := asString(args["date1"])
		if dstr == "" {
			dstr = asString(args["date"])
		}
		if dstr == "" {
			return "", fmt.Errorf(`add requires date1 (or "date")`)
		}
		days, ok := asInt(args["days"])
		if !ok {
			return "", fmt.Errorf("days is required (integer; negative subtracts)")
		}
		d, err := parseDateIn(dstr, loc)
		if err != nil {
			return "", fmt.Errorf("date1: %w", err)
		}
		result := d.AddDate(0, 0, days)
		sign := "+"
		if days < 0 {
			sign = ""
		}
		return fmt.Sprintf("%s %s%d days = %s",
			d.Format("2006-01-02 (Mon)"),
			sign,
			days,
			result.Format("2006-01-02 (Mon)"),
		), nil
	}

	return "", fmt.Errorf(`unsupported operation %q (use "diff", "add" or "unix")`, op)
}

// unixRE is a Unix timestamp: seconds, milliseconds, microseconds or
// nanoseconds, told apart by their length, with an optional fraction of a
// second (Slack's message ids are "1793305971.123456").
var unixRE = regexp.MustCompile(`^(\d{9,10}|\d{12,13}|\d{15,16}|\d{18,19})(\.\d{1,9})?$`)

// parseUnix reads a Unix timestamp. ok is false for anything that is not one.
func parseUnix(s string) (time.Time, bool) {
	m := unixRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return time.Time{}, false
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	var t time.Time
	switch len(m[1]) {
	case 9, 10:
		t = time.Unix(n, 0)
		if m[2] != "" {
			frac, _ := strconv.ParseFloat("0"+m[2], 64)
			t = t.Add(time.Duration(frac * float64(time.Second)))
		}
	case 12, 13:
		t = time.UnixMilli(n)
	case 15, 16:
		t = time.UnixMicro(n)
	default:
		t = time.Unix(0, n)
	}
	return t, true
}

// parseDateIn is parseDate that also takes a Unix timestamp, read as the day
// it falls on in loc.
func parseDateIn(s string, loc *time.Location) (time.Time, error) {
	if ts, ok := parseUnix(s); ok {
		l := ts.In(loc)
		return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC), nil
	}
	return parseDate(s)
}

// parseDateTime reads a date, or a date and time, for unix: a stamp with a
// zone keeps it; one without is read in loc, and a bare date is its midnight.
func parseDateTime(s string, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, l := range []string{time.RFC3339Nano, "2006-01-02T15:04:05Z0700", time.RFC1123Z, time.RFC1123} {
		if t, err := time.Parse(l, s); err == nil {
			return t, nil
		}
	}
	for _, l := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02 3:04 PM", "2006-01-02 3:04PM"} {
		if t, err := time.ParseInLocation(l, s, loc); err == nil {
			return t, nil
		}
	}
	d, err := parseDate(s)
	if err != nil {
		return time.Time{}, err
	}
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc), nil
}

// ago says how long before (or after) now t is, in the largest unit that
// reads naturally and rounded to the nearest one: "2 hours ago", "in 3 days".
// Rounded, not cut: 1h59m before is "2 hours ago".
func ago(t, now time.Time) string {
	d := now.Sub(t)
	future := d < 0
	if future {
		d = -d
	}
	in := func(unit time.Duration) int { return int((d + unit/2) / unit) }
	var n int
	var unit string
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		n, unit = in(time.Minute), "minute"
	case d < 48*time.Hour:
		n, unit = in(time.Hour), "hour"
	case d < 60*24*time.Hour:
		n, unit = in(24*time.Hour), "day"
	case d < 730*24*time.Hour:
		n, unit = in(30*24*time.Hour), "month"
	default:
		n, unit = in(365*24*time.Hour), "year"
	}
	if n != 1 {
		unit += "s"
	}
	if future {
		return fmt.Sprintf("in %d %s", n, unit)
	}
	return fmt.Sprintf("%d %s ago", n, unit)
}

// parseDate accepts several common formats. Order matters — more
// specific layouts come first so an ambiguous input (e.g., a plain
// "2026-1-2") resolves to the intended interpretation.
func parseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	layouts := []string{
		"2006-01-02",      // ISO strict
		"2006-1-2",        // ISO loose
		"2006/01/02",      // ISO with slashes
		"2006/1/2",        // ISO loose with slashes
		"January 2, 2006", // "April 16, 2026"
		"Jan 2, 2006",     // "Apr 16, 2026"
		"2 January 2006",  // "16 April 2026"
		"2 Jan 2006",      // "16 Apr 2026"
		"01/02/2006",      // US strict
		"1/2/2006",        // US loose
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf(`unrecognized date format %q (try YYYY-MM-DD, "April 16, 2026", or "04/16/2026")`, s)
}

// asString coerces whatever the LLM passed into a string. Tool args
// arrive as map[string]any with concrete types depending on whether
// the call came through native tool-use (typed) or the PromptTools
// <tool_call> XML path (always string).
func asString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", x)
	}
}

// asInt coerces an arg value to int, returning ok=false when the value
// is absent or not convertible. Handles the three realistic shapes: a
// real int/int64 from native tool use, a float64 from JSON unmarshal,
// and a string from PromptTools XML.
func asInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	case string:
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(x), "%d", &n); err == nil {
			return n, true
		}
	}
	return 0, false
}
