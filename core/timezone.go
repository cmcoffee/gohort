// Deployment timezone: one authoritative zone the whole server resolves
// against. Stored as an IANA name under WebTable "timezone"; empty means
// "use the host's zone" (the process's inherited time.Local — the historical
// behavior). ApplyDeploymentTimezone sets time.Local at startup so every
// .Local() format, bare-local time.Now() day key, cron fire, and the
// time_in_zone tool's zero-arg default all follow it with no per-site
// plumbing. Restart-to-apply: reassigning time.Local live would race with
// concurrent formatting, so a change takes effect on the next boot.
//
// ResolveZone (and its alias table) live here — not in a leaf tool package —
// because both the tool and this deployment setting need to turn a city /
// abbreviation / IANA name into a *time.Location. Per-user timezone (Phase 2)
// layers on top by passing an explicit *time.Location at the few user-facing
// sites; the deployment zone stays the default for anything without a user.

package core

import (
	"fmt"
	"strings"
	"time"

	"github.com/cmcoffee/gohort/core/ui"
)

// TimezoneKey is the WebTable key holding the deployment IANA zone name.
const TimezoneKey = "timezone"

// zoneAliases maps the names a human (or LLM) actually produces — cities, US
// abbreviations — to IANA zone names, since time.LoadLocation only accepts
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

// ResolveZone turns a city / abbreviation / IANA name into a Location plus
// the IANA label it resolved to. Tries the alias map first, then the raw
// input as a literal IANA name.
func ResolveZone(s string) (*time.Location, string, error) {
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

// commonTimezones is the curated shortlist offered in the timezone dropdowns —
// one canonical IANA zone per common wall-clock, grouped by region for the
// select's optgroups. It is intentionally NOT the full ~350-entry IANA set: a
// scrollable wall of zones is worse UX than a friendly shortlist. Values are
// IANA (what we store), so the dropdown round-trips with a stored value; a zone
// off this list can still be set via the CLI (which takes free text through
// ResolveZone). DST is handled by the IANA zone, so one entry covers EST/EDT etc.
var commonTimezones = []struct{ IANA, Label, Region string }{
	{"Pacific/Honolulu", "Honolulu / Hawaii", "Americas"},
	{"America/Anchorage", "Anchorage / Alaska", "Americas"},
	{"America/Los_Angeles", "Los Angeles / Pacific", "Americas"},
	{"America/Denver", "Denver / Mountain", "Americas"},
	{"America/Phoenix", "Phoenix (no DST)", "Americas"},
	{"America/Chicago", "Chicago / Central", "Americas"},
	{"America/New_York", "New York / Eastern", "Americas"},
	{"America/Toronto", "Toronto", "Americas"},
	{"America/Mexico_City", "Mexico City", "Americas"},
	{"America/Sao_Paulo", "São Paulo", "Americas"},
	{"UTC", "UTC", "UTC"},
	{"Europe/London", "London", "Europe & Africa"},
	{"Europe/Dublin", "Dublin", "Europe & Africa"},
	{"Europe/Lisbon", "Lisbon", "Europe & Africa"},
	{"Europe/Paris", "Paris", "Europe & Africa"},
	{"Europe/Berlin", "Berlin", "Europe & Africa"},
	{"Europe/Madrid", "Madrid", "Europe & Africa"},
	{"Europe/Rome", "Rome", "Europe & Africa"},
	{"Europe/Amsterdam", "Amsterdam", "Europe & Africa"},
	{"Europe/Stockholm", "Stockholm", "Europe & Africa"},
	{"Europe/Athens", "Athens", "Europe & Africa"},
	{"Europe/Istanbul", "Istanbul", "Europe & Africa"},
	{"Europe/Moscow", "Moscow", "Europe & Africa"},
	{"Africa/Johannesburg", "Johannesburg", "Europe & Africa"},
	{"Asia/Dubai", "Dubai", "Asia"},
	{"Asia/Kolkata", "India (Mumbai / Delhi)", "Asia"},
	{"Asia/Bangkok", "Bangkok", "Asia"},
	{"Asia/Jakarta", "Jakarta", "Asia"},
	{"Asia/Singapore", "Singapore", "Asia"},
	{"Asia/Hong_Kong", "Hong Kong", "Asia"},
	{"Asia/Shanghai", "Shanghai / Beijing", "Asia"},
	{"Asia/Tokyo", "Tokyo", "Asia"},
	{"Asia/Seoul", "Seoul", "Asia"},
	{"Australia/Perth", "Perth", "Pacific & Oceania"},
	{"Australia/Sydney", "Sydney", "Pacific & Oceania"},
	{"Pacific/Auckland", "Auckland", "Pacific & Oceania"},
}

// TimezoneSelectOptions builds the option list for a timezone dropdown: a
// leading blank option labeled blankLabel (its value is "" — meaning "use the
// default zone"), then the curated common zones grouped by region. Shared by
// the admin deployment setting and the per-user /account picker so the two
// can't drift. Each option's Label includes the IANA name for disambiguation.
func TimezoneSelectOptions(blankLabel string) []ui.SelectOption {
	opts := make([]ui.SelectOption, 0, len(commonTimezones)+1)
	opts = append(opts, ui.SelectOption{Value: "", Label: blankLabel})
	for _, z := range commonTimezones {
		label := z.Label
		if z.IANA != "UTC" {
			label = z.Label + " (" + z.IANA + ")"
		}
		opts = append(opts, ui.SelectOption{Value: z.IANA, Label: label, Group: z.Region})
	}
	return opts
}

// DeploymentTimezoneName returns the stored IANA zone name, or "" when the
// operator hasn't set one (meaning: use the host's zone). Safe on a nil db.
func DeploymentTimezoneName(db Database) string {
	if db == nil {
		return ""
	}
	var name string
	db.Get(WebTable, TimezoneKey, &name)
	return strings.TrimSpace(name)
}

// ApplyDeploymentTimezone reads the stored deployment zone and, if set and
// valid, installs it as the process time.Local so every local-time format and
// day boundary in the tree resolves against it. A blank setting leaves the
// host zone untouched; an invalid one is logged and ignored (the host zone
// stays in effect) so a typo can't strand the server in the wrong time.
//
// Call once at startup, before schedulers or the web server begin formatting
// times. Reassigning time.Local is a one-time boot mutation, not safe to
// repeat while goroutines format concurrently — hence restart-to-apply.
func ApplyDeploymentTimezone(db Database) {
	name := DeploymentTimezoneName(db)
	if name == "" {
		return
	}
	loc, iana, err := ResolveZone(name)
	if err != nil {
		Warn("[timezone] configured zone %q is invalid (%s) — using host zone %q", name, err, time.Local.String())
		return
	}
	time.Local = loc
	Log("[timezone] deployment zone set to %s", iana)
}

// UserLocation resolves a user's effective timezone: their personal override
// if set and valid, else the deployment/host zone (time.Local). This is the
// Phase 2 per-user seam — pass its result as a location-bearing time at the
// per-user sites (turn stamp, owned schedules) so the deployment zone stays
// the default for anything without a known user.
//
// Safe to call with an empty username or before auth is wired: both return
// time.Local. Reads the auth DB per call, which is fine at the low-frequency
// sites that use it (once per turn / once per schedule computation).
func UserLocation(username string) *time.Location {
	if strings.TrimSpace(username) == "" || AuthDB == nil {
		return time.Local
	}
	db := AuthDB()
	if db == nil {
		return time.Local
	}
	tz := AuthGetUserTimezone(db, username)
	if tz == "" {
		return time.Local
	}
	loc, _, err := ResolveZone(tz)
	if err != nil {
		return time.Local
	}
	return loc
}
