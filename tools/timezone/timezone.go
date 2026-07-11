// Package timezone provides a chat tool for time: the current time
// (locally or in any named zone) and translating a clock time between
// zones. Complements datemath ("days between / plus N days"). LLMs are
// unreliable at offset/DST math, so this is the deterministic path for
// any time-of-day or cross-timezone question.
package timezone

import (
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func init() { RegisterChatTool(new(TimeZoneTool)) }

// TimeZoneTool handles "now" (current time in a zone) and "convert"
// (a clock time from one zone expressed in another). One tool, two
// operations — keeps the registry compact and the name memorable.
type TimeZoneTool struct{}

func (t *TimeZoneTool) Name() string       { return "time_in_zone" }
func (t *TimeZoneTool) Caps() []Capability { return []Capability{CapRead} } // reads system clock

func (t *TimeZoneTool) Desc() string {
	return `Current time and timezone math — use this for ANY "what time is it" or cross-timezone question; do NOT compute offsets or DST yourself. Two operations:
- operation="now" → the current date and time. Pass zone for a specific place; omit zone for the local time.
- operation="convert", time, from, to (+ optional date) → that clock time in 'from' expressed in 'to'.
Zones accept IANA names ("America/New_York", "Asia/Tokyo"), major city names ("New York", "Tokyo", "London"), or US abbreviations ("EST", "PST").`
}

func (t *TimeZoneTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"operation": {Type: "string", Description: `"now" for the current time in a zone, or "convert" to translate a clock time between zones.`},
		"zone":      {Type: "string", Description: `Zone for operation="now" — IANA name, major city, or US abbreviation. Omit for the local (server) time.`},
		"time":      {Type: "string", Description: `Clock time for operation="convert", e.g. "3:00 PM", "15:00", "3pm".`},
		"from":      {Type: "string", Description: `Source zone for operation="convert".`},
		"to":        {Type: "string", Description: `Target zone for operation="convert".`},
		"date":      {Type: "string", Description: `Optional date for operation="convert" (YYYY-MM-DD); defaults to today in the source zone.`},
	}
}

func (t *TimeZoneTool) Run(args map[string]any) (string, error) {
	op := strings.ToLower(strings.TrimSpace(asString(args["operation"])))
	if op == "" {
		return "", fmt.Errorf(`operation is required ("now" or "convert")`)
	}

	switch op {
	case "now":
		// Zone is optional for "now": omitted → the local (server) zone,
		// so "what time is it" works with no argument (this subsumes the
		// old zero-arg get_local_time tool).
		loc, label := time.Local, "the local zone"
		if z := strings.TrimSpace(asString(args["zone"])); z != "" {
			var err error
			loc, label, err = resolveZone(z)
			if err != nil {
				return "", err
			}
		}
		return fmt.Sprintf("Current time in %s: %s", label,
			time.Now().In(loc).Format("Monday, January 2, 2006 3:04 PM MST")), nil

	case "convert":
		tstr := asString(args["time"])
		from := asString(args["from"])
		to := asString(args["to"])
		if strings.TrimSpace(tstr) == "" || strings.TrimSpace(from) == "" || strings.TrimSpace(to) == "" {
			return "", fmt.Errorf("convert requires time, from, and to")
		}
		fromLoc, fromLabel, err := resolveZone(from)
		if err != nil {
			return "", fmt.Errorf("from: %w", err)
		}
		toLoc, toLabel, err := resolveZone(to)
		if err != nil {
			return "", fmt.Errorf("to: %w", err)
		}
		h, m, err := parseClock(tstr)
		if err != nil {
			return "", err
		}
		// Base date: caller-supplied (interpreted in the source zone) or
		// today in the source zone.
		base := time.Now().In(fromLoc)
		if dstr := strings.TrimSpace(asString(args["date"])); dstr != "" {
			d, derr := parseDateOnly(dstr)
			if derr != nil {
				return "", fmt.Errorf("date: %w", derr)
			}
			base = d
		}
		src := time.Date(base.Year(), base.Month(), base.Day(), h, m, 0, 0, fromLoc)
		dst := src.In(toLoc)
		// The full date is in both sides of the output, so a day rollover
		// (e.g. 11pm NYC → next-day in Tokyo) is self-evident.
		return fmt.Sprintf("%s in %s = %s in %s",
			src.Format("Mon Jan 2, 3:04 PM MST"), fromLabel,
			dst.Format("Mon Jan 2, 3:04 PM MST"), toLabel), nil
	}

	return "", fmt.Errorf(`unsupported operation %q (use "now" or "convert")`, op)
}

// zoneAliases maps the names an LLM actually produces (cities, US
// abbreviations) to IANA zone names, since time.LoadLocation only accepts
// IANA. DST is handled automatically by the IANA zone, so EST and EDT both
// map to America/New_York. "ist" is ambiguous (India / Israel / Irish) —
// resolved to India, the most common usage.
var zoneAliases = map[string]string{
	"utc": "UTC", "gmt": "UTC", "z": "UTC", "zulu": "UTC",
	"est": "America/New_York", "edt": "America/New_York", "et": "America/New_York", "eastern": "America/New_York",
	"cst": "America/Chicago", "cdt": "America/Chicago", "ct": "America/Chicago", "central": "America/Chicago",
	"mst": "America/Denver", "mdt": "America/Denver", "mt": "America/Denver", "mountain": "America/Denver",
	"pst": "America/Los_Angeles", "pdt": "America/Los_Angeles", "pt": "America/Los_Angeles", "pacific": "America/Los_Angeles",
	"new york": "America/New_York", "nyc": "America/New_York", "newyork": "America/New_York", "boston": "America/New_York", "miami": "America/New_York", "atlanta": "America/New_York",
	"los angeles": "America/Los_Angeles", "la": "America/Los_Angeles", "san francisco": "America/Los_Angeles", "sf": "America/Los_Angeles", "seattle": "America/Los_Angeles", "portland": "America/Los_Angeles",
	"chicago": "America/Chicago", "dallas": "America/Chicago", "houston": "America/Chicago", "austin": "America/Chicago",
	"denver": "America/Denver", "phoenix": "America/Phoenix",
	"toronto": "America/Toronto", "mexico city": "America/Mexico_City",
	"sao paulo": "America/Sao_Paulo",
	"london":    "Europe/London", "uk": "Europe/London", "dublin": "Europe/Dublin", "lisbon": "Europe/Lisbon",
	"paris": "Europe/Paris", "berlin": "Europe/Berlin", "madrid": "Europe/Madrid", "rome": "Europe/Rome",
	"amsterdam": "Europe/Amsterdam", "zurich": "Europe/Zurich", "stockholm": "Europe/Stockholm",
	"moscow": "Europe/Moscow", "istanbul": "Europe/Istanbul", "athens": "Europe/Athens",
	"dubai": "Asia/Dubai", "abu dhabi": "Asia/Dubai", "gst": "Asia/Dubai",
	"mumbai": "Asia/Kolkata", "delhi": "Asia/Kolkata", "bangalore": "Asia/Kolkata", "india": "Asia/Kolkata", "ist": "Asia/Kolkata",
	"singapore": "Asia/Singapore", "bangkok": "Asia/Bangkok", "jakarta": "Asia/Jakarta",
	"hong kong": "Asia/Hong_Kong", "hongkong": "Asia/Hong_Kong",
	"shanghai": "Asia/Shanghai", "beijing": "Asia/Shanghai", "china": "Asia/Shanghai",
	"tokyo": "Asia/Tokyo", "japan": "Asia/Tokyo", "jst": "Asia/Tokyo",
	"seoul": "Asia/Seoul", "kst": "Asia/Seoul",
	"sydney": "Australia/Sydney", "melbourne": "Australia/Melbourne", "perth": "Australia/Perth", "brisbane": "Australia/Brisbane",
	"auckland": "Pacific/Auckland", "new zealand": "Pacific/Auckland",
	"honolulu": "Pacific/Honolulu", "hawaii": "Pacific/Honolulu",
}

// resolveZone turns a city / abbreviation / IANA name into a Location plus
// the IANA label it resolved to. Tries the alias map first, then the raw
// input as a literal IANA name.
func resolveZone(s string) (*time.Location, string, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return nil, "", fmt.Errorf("zone is required")
	}
	if iana, ok := zoneAliases[strings.ToLower(raw)]; ok {
		if loc, err := time.LoadLocation(iana); err == nil {
			return loc, iana, nil
		}
	}
	if loc, err := time.LoadLocation(raw); err == nil {
		return loc, raw, nil
	}
	return nil, "", fmt.Errorf(`unknown timezone %q — use an IANA name like "America/New_York" or "Asia/Tokyo", a major city ("Tokyo", "London"), or a US abbreviation (EST/CST/MST/PST)`, raw)
}

// parseClock parses a clock time into hour (24h) + minute. Accepts
// "3:00 PM", "3pm", "15:00", "15", "3:30pm".
func parseClock(s string) (int, int, error) {
	norm := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
	for _, layout := range []string{"3:04PM", "3PM", "15:04", "15"} {
		if t, err := time.Parse(layout, norm); err == nil {
			return t.Hour(), t.Minute(), nil
		}
	}
	return 0, 0, fmt.Errorf(`unrecognized time %q (try "3:00 PM" or "15:00")`, s)
}

// parseDateOnly parses a date in a few common formats (date component only).
func parseDateOnly(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"2006-01-02", "2006-1-2", "01/02/2006", "1/2/2006", "January 2, 2006", "Jan 2, 2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf(`unrecognized date %q (try YYYY-MM-DD)`, s)
}

// asString coerces a tool arg into a string — args arrive typed via native
// tool-use or as strings via the PromptTools XML path.
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
