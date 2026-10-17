package textutil

// Relative day words in text an agent keeps for later.
//
// "The free window closes Monday", saved on a Friday, means the Monday three
// days on. Read a week later it means the Monday coming, and an agent that
// reads it then tells people about a window that shut last week. The words
// were fine when written; what went wrong is that nothing said WHEN they were
// written. So at the moment of saving, each relative day word gets its date
// beside it ("closes Monday (Oct 12, 2026)"): the words stay as the agent
// wrote them, and a wrong reading is there to see.
//
// Only what has one answer is resolved. A phrase that could mean two days
// ("Monday" said on a Monday, "next Friday") is reported instead, for the
// caller to hand back to the agent to write the date itself; a guess there
// is the same mistake made at save time instead of at read time. Words that
// name no day ("soon", "later") and recurring ones ("every Monday",
// "Mondays") are left alone, and so is a phrase already followed by a date.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DateAmbiguity is a phrase that names two possible days.
type DateAmbiguity struct {
	Phrase string // as written
	Why    string // the two readings
}

var (
	relativeDayRE = regexp.MustCompile(`(?i)\b(?:(next|last|this|coming)\s+)?(monday|tuesday|wednesday|thursday|friday|saturday|sunday|weekend|week)\b|\b(today|tonight|tomorrow|yesterday)\b`)
	// alreadyDatedRE: what follows a phrase that already carries its date.
	alreadyDatedRE    = regexp.MustCompile(`(?i)^\s*[,(]?\s*(?:the\s+)?(?:(?:jan|feb|mar|apr|may|jun|jul|aug|sep|sept|oct|nov|dec)[a-z]*\.?\s+\d{1,2}|\d{1,2}(?:st|nd|rd|th)?\s+(?:of\s+)?(?:jan|feb|mar|apr|may|jun|jul|aug|sep|sept|oct|nov|dec)|\d{1,2}/\d{1,2}|\d{4}-\d{2}-\d{2})`)
	recurringBeforeRE = regexp.MustCompile(`(?i)\b(every|each)\s+$`)
	weekdayNames      = map[string]time.Weekday{"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday,
		"wednesday": time.Wednesday, "thursday": time.Thursday, "friday": time.Friday, "saturday": time.Saturday}
)

// ResolveRelativeDates dates every relative day word in text that has one
// reading, as of now (whose location is the person's), and lists the ones
// that do not. When the list is not empty the caller should not save the
// returned text but ask for the dates instead.
func ResolveRelativeDates(text string, now time.Time) (string, []DateAmbiguity) {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	var amb []DateAmbiguity
	var b strings.Builder
	last := 0
	for _, m := range relativeDayRE.FindAllStringSubmatchIndex(text, -1) {
		start, end := m[0], m[1]
		phrase := text[start:end]
		// A plural ("Mondays") or a recurring one ("every Monday") names no
		// single day.
		if end < len(text) && (text[end] == 's' || text[end] == 'S') {
			continue
		}
		if recurringBeforeRE.MatchString(text[:start]) {
			continue
		}
		if alreadyDatedRE.MatchString(text[end:]) {
			continue
		}
		qual, unit, word := "", "", ""
		if m[2] >= 0 {
			qual = strings.ToLower(text[m[2]:m[3]])
		}
		if m[4] >= 0 {
			unit = strings.ToLower(text[m[4]:m[5]])
		}
		if m[6] >= 0 {
			word = strings.ToLower(text[m[6]:m[7]])
		}
		note, why := resolvePhrase(today, qual, unit, word, sentenceTense(text, start, end))
		if why != "" {
			amb = append(amb, DateAmbiguity{Phrase: phrase, Why: why})
			continue
		}
		if note == "" {
			continue
		}
		b.WriteString(text[last:end])
		b.WriteString(" (" + note + ")")
		last = end
	}
	b.WriteString(text[last:])
	return b.String(), amb
}

// Tense of the sentence a bare weekday sits in. "Closes Monday" points ahead
// and "it broke Tuesday" points back, and only the words around it say
// which; a sentence that says neither, or both, is asked about.
var (
	futureCueRE = regexp.MustCompile(`(?i)\b(will|won't|'ll|going to|gonna|shall|closes?|ends?|opens?|starts?|begins?|launch(es)?|due|until|till|by|upcoming|expires?|runs? through|deadline|plan(s)? to|scheduled for)\b`)
	pastCueRE   = regexp.MustCompile(`(?i)\b(was|were|did|didn't|had|has been|have been|happened|went|came|got|said|saw|met|ran|left|broke|ago|earlier|already|[a-z]{2,}ed)\b`)
	// Words ending "ed" that are not past tense.
	notPastED = map[string]bool{"need": true, "feed": true, "seed": true, "speed": true, "breed": true, "bleed": true,
		"exceed": true, "proceed": true, "succeed": true, "indeed": true, "embed": true, "shred": true, "scheduled": true}
)

const (
	tenseUnclear = iota
	tenseAhead
	tenseBack
)

// sentenceTense reads the sentence holding text[start:end] for one clear
// direction in time.
func sentenceTense(text string, start, end int) int {
	from := strings.LastIndexAny(text[:start], ".!?\n") + 1
	to := len(text)
	if i := strings.IndexAny(text[end:], ".!?\n"); i >= 0 {
		to = end + i
	}
	s := text[from:start] + " " + text[end:to]
	ahead := futureCueRE.MatchString(s)
	back := false
	for _, w := range pastCueRE.FindAllString(s, -1) {
		if !notPastED[strings.ToLower(w)] {
			back = true
			break
		}
	}
	switch {
	case ahead && !back:
		return tenseAhead
	case back && !ahead:
		return tenseBack
	}
	return tenseUnclear
}

// resolvePhrase is one phrase's date note, or why it has two readings.
func resolvePhrase(today time.Time, qual, unit, word string, tense int) (note, ambiguous string) {
	switch word {
	case "today", "tonight":
		return dayNote(today), ""
	case "tomorrow":
		return dayNote(today.AddDate(0, 0, 1)), ""
	case "yesterday":
		return dayNote(today.AddDate(0, 0, -1)), ""
	}
	switch unit {
	case "week":
		if qual == "" || qual == "coming" {
			return "", "" // "a week", "the week": no day named
		}
		monday := today.AddDate(0, 0, -((int(today.Weekday()) + 6) % 7))
		switch qual {
		case "next":
			monday = monday.AddDate(0, 0, 7)
		case "last":
			monday = monday.AddDate(0, 0, -7)
		}
		return "week of " + shortDate(monday), ""
	case "weekend":
		wd := int(today.Weekday())
		// The Saturday of this week's weekend: today when Saturday,
		// yesterday when Sunday, otherwise the one coming.
		var sat time.Time
		switch wd {
		case 6:
			sat = today
		case 0:
			sat = today.AddDate(0, 0, -1)
		default:
			sat = today.AddDate(0, 0, 6-wd)
		}
		switch qual {
		case "", "this", "coming":
			if qual == "" {
				return "", "" // "a weekend", "the weekend": no weekend named
			}
		case "next":
			if wd >= 1 && wd <= 4 {
				return "", fmt.Sprintf("this coming weekend (%s) or the one after (%s)", rangeNote(sat), rangeNote(sat.AddDate(0, 0, 7)))
			}
			sat = sat.AddDate(0, 0, 7)
			if wd == 5 {
				sat = today.AddDate(0, 0, 8)
			}
		case "last":
			if wd == 6 || wd == 0 {
				sat = sat.AddDate(0, 0, -7)
			} else {
				sat = today.AddDate(0, 0, -(wd + 1))
			}
		}
		return rangeNote(sat), ""
	}
	w, ok := weekdayNames[unit]
	if !ok {
		return "", ""
	}
	ahead := (int(w) - int(today.Weekday()) + 7) % 7
	switch qual {
	case "this":
		return dayNote(today.AddDate(0, 0, ahead)), ""
	case "":
		back := (int(today.Weekday()) - int(w) + 7) % 7
		if ahead == 0 {
			return "", fmt.Sprintf("today (%s) or a week from today (%s)", dayNote(today), dayNote(today.AddDate(0, 0, 7)))
		}
		switch tense {
		case tenseAhead:
			return dayNote(today.AddDate(0, 0, ahead)), ""
		case tenseBack:
			return dayNote(today.AddDate(0, 0, -back)), ""
		}
		return "", fmt.Sprintf("the one coming (%s) or the one just gone (%s)", dayNote(today.AddDate(0, 0, ahead)), dayNote(today.AddDate(0, 0, -back)))
	case "coming":
		if ahead == 0 {
			ahead = 7
		}
		return dayNote(today.AddDate(0, 0, ahead)), ""
	case "next":
		first := ahead
		if first == 0 {
			first = 7
		}
		return "", fmt.Sprintf("the coming one (%s) or the one after (%s)", dayNote(today.AddDate(0, 0, first)), dayNote(today.AddDate(0, 0, first+7)))
	case "last":
		back := (int(today.Weekday()) - int(w) + 7) % 7
		if back == 0 {
			back = 7
		}
		if back == 1 {
			return "", fmt.Sprintf("yesterday (%s) or the one before (%s)", dayNote(today.AddDate(0, 0, -1)), dayNote(today.AddDate(0, 0, -8)))
		}
		return dayNote(today.AddDate(0, 0, -back)), ""
	}
	return "", ""
}

// dayNote is a day as the note beside a phrase names it: "Fri Oct 16, 2026".
func dayNote(d time.Time) string { return d.Format("Mon Jan 2, 2006") }

func shortDate(d time.Time) string { return d.Format("Jan 2, 2006") }

// rangeNote is a weekend, Saturday to Sunday: "Oct 17-18, 2026", or across a
// month, "Oct 31-Nov 1, 2026".
func rangeNote(sat time.Time) string {
	sun := sat.AddDate(0, 0, 1)
	if sun.Month() == sat.Month() {
		return fmt.Sprintf("%s %d-%d, %d", sat.Format("Jan"), sat.Day(), sun.Day(), sun.Year())
	}
	if sun.Year() == sat.Year() {
		return fmt.Sprintf("%s-%s, %d", sat.Format("Jan 2"), sun.Format("Jan 2"), sun.Year())
	}
	return sat.Format("Jan 2, 2006") + "-" + sun.Format("Jan 2, 2006")
}

// datedNoteRE reads back the notes ResolveRelativeDates writes: a day, a
// weekend range, or a week.
var datedNoteRE = regexp.MustCompile(`\((?:(?:Mon|Tue|Wed|Thu|Fri|Sat|Sun) )?(?:week of )?([A-Z][a-z]{2}) (\d{1,2})(?:-(?:([A-Z][a-z]{2}) )?(\d{1,2}))?, (\d{4})\)`)

// DatedNotesAllPast reports whether text carries date notes and every day
// they name was still to come when it was saved and has gone by now: a note
// about something that was going to happen, and has. A note that already
// named a past day when it was saved ("met Dana yesterday") is history and
// stays current. The last day of a range or a week is what counts.
func DatedNotesAllPast(text string, saved, now time.Time) bool {
	matches := datedNoteRE.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return false
	}
	loc := now.Location()
	savedDay := time.Date(saved.In(loc).Year(), saved.In(loc).Month(), saved.In(loc).Day(), 0, 0, 0, 0, loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	for _, m := range matches {
		year, _ := strconv.Atoi(m[5])
		month := m[1]
		day, _ := strconv.Atoi(m[2])
		if m[4] != "" { // a range: its last day
			if m[3] != "" {
				month = m[3]
			}
			day, _ = strconv.Atoi(m[4])
		}
		mon, err := time.Parse("Jan", month)
		if err != nil {
			return false
		}
		d := time.Date(year, mon.Month(), day, 0, 0, 0, 0, loc)
		if strings.Contains(m[0], "week of") {
			d = d.AddDate(0, 0, 6)
		}
		if d.Before(savedDay) || !d.Before(today) {
			return false
		}
	}
	return true
}
