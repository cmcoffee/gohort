package textutil

import (
	"strings"
	"testing"
	"time"
)

// Friday, October 9, 2026: the day the ARC Raiders note was written.
var fri = time.Date(2026, time.October, 9, 15, 0, 0, 0, time.FixedZone("PDT", -7*3600))

// Every phrase with one reading gets its date beside it, as written; nothing
// else changes.
func TestRelativeDatesWithOneReadingAreDated(t *testing.T) {
	for in, want := range map[string]string{
		"the free window closes Monday":   "the free window closes Monday (Mon Oct 12, 2026)",
		"party tonight, cleanup tomorrow": "party tonight (Fri Oct 9, 2026), cleanup tomorrow (Sat Oct 10, 2026)",
		"met Dana yesterday":              "met Dana yesterday (Thu Oct 8, 2026)",
		"open studios this weekend":       "open studios this weekend (Oct 10-11, 2026)",
		"next weekend is the festival":    "next weekend (Oct 17-18, 2026) is the festival",
		"we shipped last weekend":         "we shipped last weekend (Oct 3-4, 2026)",
		"review is next week":             "review is next week (week of Oct 12, 2026)",
		"this Friday's standup":           "this Friday (Fri Oct 9, 2026)'s standup",
		"the coming Friday":               "the coming Friday (Fri Oct 16, 2026)",
		"saw it last Tuesday":             "saw it last Tuesday (Tue Oct 6, 2026)",
		"closes Monday, Oct 12":           "closes Monday, Oct 12",
		"closes Monday (Oct 12)":          "closes Monday (Oct 12)",
		"closes Monday 10/12":             "closes Monday 10/12",
		"standup every Monday":            "standup every Monday",
		"Mondays are quiet":               "Mondays are quiet",
		"see you soon, later this month":  "see you soon, later this month",
		"it takes a week":                 "it takes a week",
		"Tuesday the meeting moved":       "Tuesday (Tue Oct 6, 2026) the meeting moved",
		"the demo is due Wednesday":       "the demo is due Wednesday (Wed Oct 14, 2026)",
		"it rained Sunday":                "it rained Sunday (Sun Oct 4, 2026)",
	} {
		got, amb := ResolveRelativeDates(in, fri)
		if got != want || len(amb) != 0 {
			t.Errorf("%q:\n got  %q (%v)\n want %q", in, got, amb, want)
		}
	}
}

// A phrase that could mean two days is reported with both readings and not
// dated: guessing there is the mistake this exists to stop.
func TestRelativeDatesWithTwoReadingsGoBack(t *testing.T) {
	for in, both := range map[string]string{
		"closes Friday":           "today (Fri Oct 9, 2026) or a week from today (Fri Oct 16, 2026)",
		"lunch next Tuesday":      "the coming one (Tue Oct 13, 2026) or the one after (Tue Oct 20, 2026)",
		"it broke last Thursday":  "yesterday (Thu Oct 8, 2026) or the one before (Thu Oct 1, 2026)",
		"lunch with Dana Tuesday": "the one coming (Tue Oct 13, 2026) or the one just gone (Tue Oct 6, 2026)",
	} {
		got, amb := ResolveRelativeDates(in, fri)
		if len(amb) != 1 || amb[0].Why != both || got != in {
			t.Errorf("%q: got %q, %+v", in, got, amb)
		}
	}
	mon := time.Date(2026, time.October, 12, 9, 0, 0, 0, time.UTC)
	if _, amb := ResolveRelativeDates("next weekend we hike", mon); len(amb) != 1 || !strings.Contains(amb[0].Why, "Oct 17-18, 2026") || !strings.Contains(amb[0].Why, "Oct 24-25, 2026") {
		t.Errorf("next weekend on a Monday: %+v", amb)
	}
}

// A note about something still to come is past once its day has gone; one
// that named a past day when it was saved is history and stays.
func TestDatedNotesGoPastWhenTheirDayHas(t *testing.T) {
	note, _ := ResolveRelativeDates("the free window closes Monday", fri)
	for now, want := range map[time.Time]bool{
		fri:                  false,
		fri.AddDate(0, 0, 3): false, // Monday itself: still on
		fri.AddDate(0, 0, 4): true,
		fri.AddDate(0, 0, 7): true,
	} {
		if got := DatedNotesAllPast(note, fri, now); got != want {
			t.Errorf("%s at %s: %v, want %v", note, now.Format("Jan 2"), got, want)
		}
	}
	hist, _ := ResolveRelativeDates("met Dana yesterday", fri)
	if DatedNotesAllPast(hist, fri, fri.AddDate(0, 1, 0)) {
		t.Error("a note about the past went past")
	}
	both, _ := ResolveRelativeDates("demo tomorrow, retro next week", fri)
	if DatedNotesAllPast(both, fri, fri.AddDate(0, 0, 3)) || !DatedNotesAllPast(both, fri, fri.AddDate(0, 0, 10)) {
		t.Error("a note goes past only once every day it names has")
	}
	if DatedNotesAllPast("closes Monday, Oct 12", fri, fri.AddDate(0, 1, 0)) {
		t.Error("a date the agent wrote itself is not one of ours to read")
	}
}
