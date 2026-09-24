package provenance

// What a memory is about in TIME, as distinct from how fast its content goes
// stale (Volatility).
//
// A standing fact ("prefers dark mode") is true until something replaces it.
// An event ("just got back from a trip to Oregon") was true once, on a date,
// and reads as live news for a week or two; after that, bringing it up again
// ("how was the trip?") is the agent failing to notice time has passed. An open
// item ("pending edits on the photos") waits on somebody; left alone for weeks
// it is either done or dropped, and an agent that treats it as live keeps
// raising work nobody is doing.
//
// Volatility cannot express either. Both are about the claim's relationship to
// a date, not about how quickly its value changes, and a trip is not "volatile"
// in the price-of-a-GPU sense. So they get their own axis, set once at write
// time, and a daily pass (in the app that owns the memory) acts on it.

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MemKind is what a memory is about in time. Zero is UNCLASSIFIED: every row
// written before this existed. It is kept distinct from a standing fact so a
// pass can classify old rows once, and so a row somebody explicitly kept as a
// standing fact (an undone move, a judge's call) is never second-guessed.
type MemKind uint8

const (
	MemKindUnknown  MemKind = iota // written before kinds were recorded
	MemKindFact                    // true until replaced
	MemKindEvent                   // happened on a date (EventAt)
	MemKindOpenItem                // waiting on a decision or a follow-up
)

// String names a kind for logs and UI.
func (k MemKind) String() string {
	switch k {
	case MemKindEvent:
		return "event"
	case MemKindOpenItem:
		return "open item"
	case MemKindFact:
		return "fact"
	default:
		return "unclassified"
	}
}

// ParseMemKind reads a kind named by a writer (a model's JSON, a form). Anything
// unrecognized is a standing fact: a wrong guess there changes nothing.
func ParseMemKind(s string) MemKind {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "event":
		return MemKindEvent
	case "open", "open_item", "open item", "pending", "todo":
		return MemKindOpenItem
	default:
		return MemKindFact
	}
}

// openItemSignals mark a note recording work somebody still means to do. Tested
// BEFORE the event signals: "pending edits on the trip photos" is waiting work
// that happens to mention a trip.
var openItemSignals = []string{
	"pending", "to-do", "todo", "to do:", "still needs", "needs to be done", "not yet done",
	"hasn't been done", "has not been done", "follow up on", "follow-up on", "following up on",
	"dig into later", "look into later", "look at later", "come back to", "circle back",
	"remind me", "waiting on", "waiting for", "open question", "outstanding", "backlog",
	"wants to dig into", "wants to look into", "want to dig into", "want to look into",
	"plans to", "planning to", "intends to", "meaning to",
}

// eventSignals mark a note about something that HAPPENED (or will, on a date).
// Deliberately phrases, not bare nouns that also name standing facts: a
// "birthday" is a date that recurs forever, a "birthday party" happened once.
var eventSignals = []string{
	" trip", "trip to", "vacation", "holiday in", "returned from", "back from", "got back",
	"visited", "visiting ", "went to", "flew to", "flying to", "traveled to", "travelled to",
	"traveling to", "travelling to", "appointment", "wedding", "funeral", "conference in",
	"birthday party", "concert", "surgery", "moved house", "moving day", "interview with",
	"this weekend", "last weekend", "next weekend", "yesterday", "tomorrow", "tonight",
	"last week", "next week", "last night",
}

// ClassifyMemKind infers what a note is about in time, and for an event when
// it happened. Conservative like classifyVolatility: a standing fact is the
// default, and a missed event only means it ages the old way. now dates an
// event the note does not date itself, and resolves "yesterday" and friends.
func ClassifyMemKind(note string, now time.Time) (MemKind, time.Time) {
	n := " " + strings.ToLower(note) + " "
	for _, sig := range openItemSignals {
		if strings.Contains(n, sig) {
			return MemKindOpenItem, time.Time{}
		}
	}
	for _, sig := range eventSignals {
		if strings.Contains(n, sig) {
			if at, ok := ParseEventDate(note, now); ok {
				return MemKindEvent, at
			}
			return MemKindEvent, now
		}
	}
	return MemKindFact, time.Time{}
}

// monthNames is spelled out rather than "jan[a-z]*" so a name like "Marco 12"
// is not read as March 12.
const monthNames = "january|february|march|april|may|june|july|august|september|october|november|december|jan|feb|mar|apr|jun|jul|aug|sept|sep|oct|nov|dec"

var (
	isoDateRE   = regexp.MustCompile(`\b(\d{4})-(\d{2})-(\d{2})\b`)
	monthDayRE  = regexp.MustCompile(`(?i)\b(` + monthNames + `)\.?\s+(\d{1,2})(?:st|nd|rd|th)?(?:,?\s+(\d{4}))?\b`)
	dayMonthRE  = regexp.MustCompile(`(?i)\b(\d{1,2})(?:st|nd|rd|th)?\s+(` + monthNames + `)\.?(?:,?\s+(\d{4}))?\b`)
	monthByName = map[string]time.Month{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "sept": 9, "oct": 10, "nov": 11, "dec": 12}
)

// relativeDays are the relative dates a note says outright, in days from now.
var relativeDays = []struct {
	phrase string
	days   int
}{
	{"yesterday", -1}, {"last night", -1}, {"last weekend", -7}, {"last week", -7},
	{"tomorrow", 1}, {"next weekend", 7}, {"next week", 7},
}

// ParseEventDate finds the date a note (or a model's answer) gives: an ISO
// date, "August 24, 2026", "24 Aug", or a relative phrase. A month and day
// with no year takes now's year, or last year's when that would put it more
// than a month in the future (a note written in January about "Dec 28").
func ParseEventDate(s string, now time.Time) (time.Time, bool) {
	loc := now.Location()
	if m := isoDateRE.FindStringSubmatch(s); m != nil {
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		d, _ := strconv.Atoi(m[3])
		if mo >= 1 && mo <= 12 && d >= 1 && d <= 31 {
			return time.Date(y, time.Month(mo), d, 12, 0, 0, 0, loc), true
		}
	}
	monthDay := func(mon, day, year string) (time.Time, bool) {
		mo, ok := monthByName[strings.ToLower(mon)[:3]]
		d, _ := strconv.Atoi(day)
		if !ok || d < 1 || d > 31 {
			return time.Time{}, false
		}
		y := now.Year()
		if year != "" {
			y, _ = strconv.Atoi(year)
		}
		at := time.Date(y, mo, d, 12, 0, 0, 0, loc)
		if year == "" && at.After(now.AddDate(0, 1, 0)) {
			at = at.AddDate(-1, 0, 0)
		}
		return at, true
	}
	if m := monthDayRE.FindStringSubmatch(s); m != nil {
		if at, ok := monthDay(m[1], m[2], m[3]); ok {
			return at, true
		}
	}
	if m := dayMonthRE.FindStringSubmatch(s); m != nil {
		if at, ok := monthDay(m[2], m[1], m[3]); ok {
			return at, true
		}
	}
	low := strings.ToLower(s)
	for _, r := range relativeDays {
		if strings.Contains(low, r.phrase) {
			return now.AddDate(0, 0, r.days), true
		}
	}
	return time.Time{}, false
}

// EventDate is when an event happened, falling back to when the row was
// written for one that never recorded it.
func (p MemoryProvenance) EventDate(created time.Time) time.Time {
	if !p.EventAt.IsZero() {
		return p.EventAt
	}
	return created
}
