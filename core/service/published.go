package service

// When a search result was published, as the engine gave it, put ahead of
// its snippet as a marker that web_search renders against the reader's own
// day (tools/websearch, renderPublished): "[Published 2026-09-28] ".

import (
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
	{"2006 Jan 2", true},
}

// publishedAgoRE reads the relative ages search engines show ("3 days ago").
var publishedAgoRE = regexp.MustCompile(`^(\d+)\s+(minute|hour|day|week)s?\s+ago$`)

// ParsePublished reads a published date. dateOnly is a day with no time of
// day, or a stamp with no zone, whose calendar day is taken as written
// rather than moved into the reader's zone. Relative ages count back from now.
func ParsePublished(raw string, now time.Time) (t time.Time, dateOnly bool, ok bool) {
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

// PublishedTag is the marker a search backend puts ahead of a result's
// snippet, "" when the date cannot be read. A day stays a day; a stamp keeps
// its instant, so the reader's zone decides its calendar day.
func PublishedTag(raw string) string {
	t, dateOnly, ok := ParsePublished(raw, time.Now())
	if !ok {
		return ""
	}
	if dateOnly {
		return "[Published " + t.Format("2006-01-02") + "] "
	}
	return "[Published " + t.UTC().Format(time.RFC3339) + "] "
}
