package textutil

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SpokenWords writes out the abbreviations a reply uses in writing, so a
// speech model says "Saturday" for Sat and "Drive" for the Dr of an address
// instead of a guess. Only a reading the text around it settles is written
// out: lowercase "sat" stays the verb, "Sun" is the day only beside a date or
// another day, and a Dr is Doctor before a name but Drive after a street's.
// Anything unsettled is left as written. Rules, not a model: it runs on every
// spoken reply, before the first sound, and must not add a wait.
func SpokenWords(s string) string {
	for _, r := range spokenRules {
		s = r.apply(s)
	}
	return s
}

// spokenRule finds candidates with its pattern and lets say decide each one
// from the text around it; say returns false to leave a match as written.
type spokenRule struct {
	pattern *regexp.Regexp
	say     func(s string, m []int) (string, bool)
}

func (r spokenRule) apply(s string) string {
	var out strings.Builder
	last := 0
	for _, m := range r.pattern.FindAllStringSubmatchIndex(s, -1) {
		words, ok := r.say(s, m)
		if !ok {
			continue
		}
		out.WriteString(s[last:m[0]])
		out.WriteString(words)
		last = m[1]
	}
	out.WriteString(s[last:])
	return out.String()
}

var spokenDays = map[string]string{
	"Mon": "Monday", "Tue": "Tuesday", "Tues": "Tuesday", "Wed": "Wednesday",
	"Thu": "Thursday", "Thur": "Thursday", "Thurs": "Thursday", "Fri": "Friday",
	"Sat": "Saturday", "Sun": "Sunday",
}

var spokenMonths = map[string]string{
	"Jan": "January", "Feb": "February", "Mar": "March", "Apr": "April",
	"Jun": "June", "Jul": "July", "Aug": "August", "Sep": "September",
	"Sept": "September", "Oct": "October", "Nov": "November", "Dec": "December",
}

// Street words an address ends with: written out only after a street's name.
var spokenStreets = map[string]string{
	"Ave": "Avenue", "Blvd": "Boulevard", "Rd": "Road", "Ln": "Lane",
	"Ct": "Court", "Pkwy": "Parkway", "Dr": "Drive", "St": "Street",
}

// The same abbreviation before a name.
var spokenTitles = map[string]string{"Dr": "Doctor", "St": "Saint", "Mt": "Mount", "Ft": "Fort"}

// Units, after a number: the singular for exactly 1.
var spokenUnits = map[string][2]string{
	"km": {"kilometer", "kilometers"}, "mi": {"mile", "miles"}, "ft": {"foot", "feet"},
	"lb": {"pound", "pounds"}, "lbs": {"pound", "pounds"}, "oz": {"ounce", "ounces"},
	"kg": {"kilogram", "kilograms"}, "mph": {"mile per hour", "miles per hour"},
	"km/h": {"kilometer per hour", "kilometers per hour"}, "kph": {"kilometer per hour", "kilometers per hour"},
	"min": {"minute", "minutes"}, "mins": {"minute", "minutes"},
	"hr": {"hour", "hours"}, "hrs": {"hour", "hours"},
	"sec": {"second", "seconds"}, "secs": {"second", "seconds"},
	"°F": {"degree Fahrenheit", "degrees Fahrenheit"}, "°C": {"degree Celsius", "degrees Celsius"},
	"°": {"degree", "degrees"},
}

const (
	spokenDayNames   = `Monday|Tuesday|Wednesday|Thursday|Friday|Saturday|Sunday|Mon|Tues?|Wed|Thu(?:rs?)?|Fri|Sat|Sun`
	spokenMonthNames = `January|February|March|April|May|June|July|August|September|October|November|December|Jan|Feb|Mar|Apr|Jun|Jul|Aug|Sept?|Oct|Nov|Dec`
	spokenJoiners    = `(?:-|–|—|/|&|,|and|or|to|through|thru)`
)

var (
	spokenDayWord   = regexp.MustCompile(`\b(Mon|Tues?|Wed|Thu(?:rs?)?|Fri|Sat|Sun)\b\.?`)
	spokenMonthWord = regexp.MustCompile(`\b(Jan|Feb|Mar|Apr|Jun|Jul|Aug|Sept?|Oct|Nov|Dec)\b\.?`)
	// A day with a date, a time of day or another day after it.
	spokenDayAhead = regexp.MustCompile(`^,?[ \t]*(?:\d|(?:` + spokenMonthNames + `)\b|(?:morning|afternoon|evening|night)\b|:)|^[ \t]*` + spokenJoiners + `[ \t]*(?:` + spokenDayNames + `)\b`)
	// Another day just before it, as in Mon-Sat.
	spokenDayBehind = regexp.MustCompile(`\b(?:` + spokenDayNames + `)\.?[ \t]*` + spokenJoiners + `[ \t]*$`)
	spokenDayLead   = regexp.MustCompile(`(?i)\b(?:on|this|next|last|by|until|till|through|thru|every|before|after)[ \t]+$`)
	spokenMonthLead = regexp.MustCompile(`(?i)\b(?:in|since|until|till|early|late|mid|this|next|last|through)[ \t]+$`)
	spokenMonthNext = regexp.MustCompile(`^[ \t]*` + spokenJoiners + `[ \t]*(?:` + spokenMonthNames + `)\b`)
	spokenMonthPrev = regexp.MustCompile(`\b(?:` + spokenMonthNames + `)\.?[ \t]*` + spokenJoiners + `[ \t]*$`)
	spokenDigitPrev = regexp.MustCompile(`\d[ \t]+$`)
	// The day of the month after a month's name, said as "the 2nd".
	// A dash between two days or two months is read as a pause; it means "to".
	spokenRange    = regexp.MustCompile(`\b(Monday|Tuesday|Wednesday|Thursday|Friday|Saturday|Sunday|January|February|March|April|May|June|July|August|September|October|November|December)[ \t]*[-–—][ \t]*(Monday|Tuesday|Wednesday|Thursday|Friday|Saturday|Sunday|January|February|March|April|May|June|July|August|September|October|November|December)\b`)
	spokenMonthDay = regexp.MustCompile(`\b(` + spokenMonthNames + `)([ \t]+)([1-9]|[12]\d|3[01])\b`)

	spokenPlace     = regexp.MustCompile(`\b(Dr|St|Ave|Blvd|Rd|Ln|Ct|Pkwy|Mt|Ft)\b\.?`)
	spokenHighway   = regexp.MustCompile(`\bHwy\b\.?`)
	spokenUnitAfter = regexp.MustCompile(`\b(\d+(?:[.,]\d+)?)([ \t]?)(km/h|km|mi|ft|lbs?|oz|kg|mph|kph|mins?|hrs?|secs?)\b\.?|\b(\d+(?:\.\d+)?)[ \t]?(°[FC]?)`)
	spokenLatin     = regexp.MustCompile(`\b([eE])\.[gG]\.|\b([iI])\.[eE]\.|\betc\.|\bvs\b\.?|\bapprox\b\.?`)
	spokenWith      = regexp.MustCompile(`(^|[\s(])[wW]/(o\b)?[ \t]*`)
)

var spokenRules = []spokenRule{
	{spokenLatin, sayLatin},
	{spokenWith, sayWith},
	{spokenUnitAfter, sayUnit},
	{spokenHighway, func(s string, m []int) (string, bool) { return "Highway" + keptPeriod(s, m), true }},
	{spokenPlace, sayPlace},
	{spokenDayWord, sayDay},
	{spokenMonthWord, sayMonth},
	{spokenRange, func(s string, m []int) (string, bool) { return s[m[2]:m[3]] + " to " + s[m[4]:m[5]], true }},
	{spokenMonthDay, sayMonthDay},
}

func sayLatin(s string, m []int) (string, bool) {
	w := s[m[0]:m[1]]
	switch {
	case m[2] >= 0:
		return "for example" + spokenPause(s, m), true
	case m[4] >= 0:
		return "that is" + spokenPause(s, m), true
	case w == "etc.":
		return "et cetera" + keptPeriod(s, m), true
	case strings.HasPrefix(w, "vs"):
		return "versus", true
	}
	return "approximately", true
}

func sayWith(s string, m []int) (string, bool) {
	rest := s[m[1]:]
	if m[4] < 0 && (rest == "" || !unicode.IsLetter(firstRune(rest))) {
		return "", false
	}
	if m[4] >= 0 {
		return s[m[2]:m[3]] + "without ", true
	}
	return s[m[2]:m[3]] + "with ", true
}

func sayUnit(s string, m []int) (string, bool) {
	num, unit := "", ""
	if m[2] >= 0 {
		num, unit = s[m[2]:m[3]], s[m[6]:m[7]]
	} else {
		num, unit = s[m[8]:m[9]], s[m[10]:m[11]]
	}
	names, ok := spokenUnits[unit]
	if !ok {
		return "", false
	}
	name := names[1]
	if num == "1" {
		name = names[0]
	}
	return num + " " + name + keptPeriod(s, m), true
}

// sayPlace reads Dr and St as a street after a street's name and as a title
// before a name; the rest of the street words only after a street's name,
// Mt and Ft only before one.
func sayPlace(s string, m []int) (string, bool) {
	abbr := s[m[2]:m[3]]
	prev, initial := wordBefore(s, m[0])
	afterStreet := prev != "" && (unicode.IsUpper(firstRune(prev)) || unicode.IsDigit(firstRune(prev)))
	beforeName := nameAfter(s, m)
	street, isStreet := spokenStreets[abbr]
	title, isTitle := spokenTitles[abbr]
	switch {
	case isStreet && afterStreet && !initial:
		return street + keptPeriod(s, m), true
	case isTitle && beforeName:
		return title, true
	case isStreet && afterStreet:
		return street + keptPeriod(s, m), true
	}
	return "", false
}

func sayDay(s string, m []int) (string, bool) {
	ahead := strings.TrimPrefix(s[m[1]:], ".")
	behind := s[:m[0]]
	if !spokenDayAhead.MatchString(ahead) && !spokenDayBehind.MatchString(behind) && !spokenDayLead.MatchString(behind) {
		return "", false
	}
	return spokenDays[s[m[2]:m[3]]] + keptPeriod(s, m), true
}

func sayMonth(s string, m []int) (string, bool) {
	rest := s[m[1]:]
	if strings.HasPrefix(rest, "'") || strings.HasPrefix(rest, "’") {
		return "", false
	}
	behind := s[:m[0]]
	after := strings.TrimLeft(rest, " \t")
	dated := len(after) < len(rest) && unicode.IsDigit(firstRune(after))
	if !dated && !spokenDigitPrev.MatchString(behind) && !spokenMonthNext.MatchString(rest) &&
		!spokenMonthPrev.MatchString(behind) && !spokenMonthLead.MatchString(behind) {
		return "", false
	}
	return spokenMonths[s[m[2]:m[3]]] + keptPeriod(s, m), true
}

// sayMonthDay says "November 2" as "November 2nd". A year is not a day (the
// pattern stops at 31), nor is the hour of a time.
func sayMonthDay(s string, m []int) (string, bool) {
	if strings.HasPrefix(s[m[1]:], ":") {
		return "", false
	}
	month := s[m[2]:m[3]]
	if month == "May" {
		if _, initial := wordBefore(s, m[0]); initial || m[0] == 0 {
			return "", false
		}
	}
	n, _ := strconv.Atoi(s[m[6]:m[7]])
	return month + s[m[4]:m[5]] + strconv.Itoa(n) + ordinalSuffix(n), true
}

func ordinalSuffix(n int) string {
	if n%100 >= 11 && n%100 <= 13 {
		return "th"
	}
	switch n % 10 {
	case 1:
		return "st"
	case 2:
		return "nd"
	case 3:
		return "rd"
	}
	return "th"
}

// spokenPause is the comma a reader hears after "for example" whether or not
// the writer put one after the e.g.
func spokenPause(s string, m []int) string {
	if rest := s[m[1]:]; rest == "" || strings.ContainsRune(",;:)", firstRune(rest)) {
		return ""
	}
	return ","
}

// keptPeriod is the period an abbreviation ended with when it also ended the
// sentence: kept then, dropped otherwise, so a speech model does not stop
// for one in the middle of "Dr. Smith".
func keptPeriod(s string, m []int) string {
	if !strings.HasSuffix(s[m[0]:m[1]], ".") {
		return ""
	}
	rest := s[m[1]:]
	trimmed := strings.TrimLeft(rest, " \t")
	if trimmed == "" || trimmed[0] == '\n' || (len(trimmed) < len(rest) && unicode.IsUpper(firstRune(trimmed))) {
		return "."
	}
	return ""
}

// nameAfter reports whether a capitalized word follows the match on its line.
func nameAfter(s string, m []int) bool {
	rest := s[m[1]:]
	trimmed := strings.TrimLeft(rest, " \t")
	return len(trimmed) < len(rest) && unicode.IsUpper(firstRune(trimmed))
}

// wordBefore is the word before i on its line, and whether that word starts
// its sentence.
func wordBefore(s string, i int) (string, bool) {
	head := strings.TrimRight(s[:i], " \t")
	if head == "" || strings.HasSuffix(head, "\n") {
		return "", true
	}
	j := strings.LastIndexAny(head, " \t\n")
	word := head[j+1:]
	if strings.TrimRight(word, ",;:") != word {
		return "", false
	}
	lead := strings.TrimRight(head[:j+1], " \t")
	initial := lead == "" || strings.HasSuffix(lead, "\n") || strings.HasSuffix(lead, ".") ||
		strings.HasSuffix(lead, "!") || strings.HasSuffix(lead, "?")
	return word, initial
}

func firstRune(s string) rune {
	r, _ := utf8.DecodeRuneInString(s)
	return r
}
