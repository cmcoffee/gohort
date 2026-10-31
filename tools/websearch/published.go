// When a page or a search result was published, said so that an agent need
// not work it out. A source tells time from its own day: "on Friday", "last
// week", "yesterday". Read on a later day, against the turn's own date, those
// words land on the wrong weekend, and the model does the subtraction badly
// when it does it at all. So the date is stated with its weekday and how long
// ago it was, counted here, in the user's zone.
//
// Two steps, because the search backends do not know who is asking: each
// backend tags a result's date as a marker (service.PublishedTag), and web_search,
// which has the session, renders every marker against the user's clock
// (renderPublished). A marker that is never rendered (a caller of
// SearchWithProvider that is not the tool) still reads as a date.

package websearch

import (
	"fmt"
	"regexp"
	"time"

	"github.com/cmcoffee/gohort/core/service"
)

// publishedMarkerRE finds the markers service.PublishedTag writes.
var publishedMarkerRE = regexp.MustCompile(`\[Published (\d{4}-\d{2}-\d{2}(?:T\d{2}:\d{2}:\d{2}Z)?)\]`)

// renderPublished rewrites every marker in a search result as the day with
// its weekday and how long before today it was, in loc.
func renderPublished(text string, loc *time.Location, now time.Time) string {
	return publishedMarkerRE.ReplaceAllStringFunc(text, func(m string) string {
		raw := publishedMarkerRE.FindStringSubmatch(m)[1]
		t, dateOnly, ok := service.ParsePublished(raw, now)
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
	t, dateOnly, ok := service.ParsePublished(raw, now)
	if !ok {
		return ""
	}
	return "Published " + publishedPhrase(t, dateOnly, loc, now) + ". Days the page names (\"on Sunday\", \"last week\", \"yesterday\") count from that date, not from today.\n\n"
}
