// When a page or a search result was published, said so that an agent need
// not work it out. A source tells time from its own day: "on Friday", "last
// week", "yesterday". Read on a later day, against the turn's own date, those
// words land on the wrong weekend, and the model does the subtraction badly
// when it does it at all. So the date is stated with its weekday and how long
// ago it was, counted here, in the user's zone.
//
// Two steps, because the search backends do not know who is asking: each
// backend tags a result's date as a marker (publishedTag), and web_search,
// which has the session, renders every marker against the user's clock
// (renderPublished). A marker that is never rendered (a caller of
// SearchWithProvider that is not the tool) still reads as a date.

package websearch

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// publishedLayouts are the shapes a published date arrives in: ISO stamps and
// days from meta tags and APIs, and the written-out days search engines show.
var publishedLayouts = []struct {
	layout   string
	dateOnly bool
}{
	{time.RFC3339Nano, false},
	{"2006-01-02T15:04:05Z0700", false},
	{"2006-01-02T15:04:05.000Z0700", false},
	{"2006-01-02T15:04:05", true},
	{"2006-01-02 15:04:05", true},
	{"2006-01-02", true},
	{time.RFC1123Z, false},
	{time.RFC1123, false},
	{"Jan 2, 2006", true},
	{"January 2, 2006", true},
	{"2 Jan 2006", true},
	{"2 January 2006", true},
}

// publishedAgoRE reads the relative ages search engines show ("3 days ago").
var publishedAgoRE = regexp.MustCompile(`^(\d+)\s+(minute|hour|day|week)s?\s+ago$`)

// parsePublished reads a published date. dateOnly is a day with no time of
// day, or a stamp with no zone, whose calendar day is taken as written
// rather than moved into the reader's zone. Relative ages count back from now.
func parsePublished(raw string, now time.Time) (t time.Time, dateOnly bool, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return t, false, false
	}
	for _, l := range publishedLayouts {
		if t, err := time.Parse(l.layout, raw); err == nil {
			return t, l.dateOnly, true
		}
	}
	if m := publishedAgoRE.FindStringSubmatch(strings.ToLower(raw)); m != nil {
		n, _ := strconv.Atoi(m[1])
		unit := map[string]time.Duration{"minute": time.Minute, "hour": time.Hour, "day": 24 * time.Hour, "week": 7 * 24 * time.Hour}[m[2]]
		return now.Add(-time.Duration(n) * unit), false, true
	}
	return t, false, false
}

// publishedTag is the marker a search backend puts ahead of a result's
// snippet, "" when the date cannot be read. A day stays a day; a stamp keeps
// its instant, so the reader's zone decides its calendar day.
func publishedTag(raw string) string {
	t, dateOnly, ok := parsePublished(raw, time.Now())
	if !ok {
		return ""
	}
	if dateOnly {
		return "[Published " + t.Format("2006-01-02") + "] "
	}
	return "[Published " + t.UTC().Format(time.RFC3339) + "] "
}

// publishedMarkerRE finds the markers publishedTag writes.
var publishedMarkerRE = regexp.MustCompile(`\[Published (\d{4}-\d{2}-\d{2}(?:T\d{2}:\d{2}:\d{2}Z)?)\]`)

// renderPublished rewrites every marker in a search result as the day with
// its weekday and how long before today it was, in loc.
func renderPublished(text string, loc *time.Location, now time.Time) string {
	return publishedMarkerRE.ReplaceAllStringFunc(text, func(m string) string {
		raw := publishedMarkerRE.FindStringSubmatch(m)[1]
		t, dateOnly, ok := parsePublished(raw, now)
		if !ok {
			return m
		}
		return "[Published " + publishedPhrase(t, dateOnly, loc, now) + "]"
	})
}

// publishedPhrase is a published date as an agent should repeat it: "Mon,
// Sep 28, 2026, 31 days ago". The age counts calendar days in loc, so a page
// from last night is "yesterday" in the morning.
func publishedPhrase(t time.Time, dateOnly bool, loc *time.Location, now time.Time) string {
	if loc == nil {
		loc = time.Local
	}
	if !dateOnly {
		t = t.In(loc)
	}
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	n := now.In(loc)
	today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
	when := day.Format("Mon, Jan 2, 2006")
	switch days := int(today.Sub(day).Hours() / 24); {
	case days < 0:
		return when
	case days == 0:
		return when + ", today"
	case days == 1:
		return when + ", yesterday"
	case days < 90:
		return fmt.Sprintf("%s, %d days ago", when, days)
	case days < 730:
		return fmt.Sprintf("%s, about %d months ago", when, days/30)
	default:
		return fmt.Sprintf("%s, about %d years ago", when, days/365)
	}
}

// publishedLine is the line fetch_url puts above a page's text, "" when the
// page carries no date it can read.
func publishedLine(raw string, loc *time.Location, now time.Time) string {
	t, dateOnly, ok := parsePublished(raw, now)
	if !ok {
		return ""
	}
	return "Published " + publishedPhrase(t, dateOnly, loc, now) + ". Days the page names (\"on Sunday\", \"last week\", \"yesterday\") count from that date, not from today.\n\n"
}
