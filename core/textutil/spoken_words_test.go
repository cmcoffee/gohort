package textutil

import "testing"

// A speech model guesses at an abbreviation, and guesses wrong for the ones a
// reply uses most: "Sat" as a word, the Dr of an address as Doctor, the St of
// one as Saint. Each is written out only where the words around it settle
// which it is; everywhere else it is left as written.
func TestSpokenWords(t *testing.T) {
	cases := []struct{ name, in, want string }{
		// Days.
		{"day before a date", "Open Sat, Nov 2 from 10am.", "Open Saturday, November 2nd from 10am."},
		{"day range", "Open Mon-Fri, 9 to 5.", "Open Monday to Friday, 9 to 5."},
		{"days joined by and", "Closed Sat and Sun.", "Closed Saturday and Sunday."},
		{"day after on", "The party is on Sat at 7:30 pm.", "The party is on Saturday at 7:30 pm."},
		{"day before a time of day", "See you Thurs morning.", "See you Thursday morning."},
		{"day in a schedule", "Sat: 9-5", "Saturday: 9-5"},
		{"day period kept at a sentence end", "Let's meet next Tue. It works.", "Let's meet next Tuesday. It works."},
		{"day period dropped mid-sentence", "next Wed. at noon", "next Wednesday at noon"},
		{"the verb stays", "She sat down. Sat there all day.", "She sat down. Sat there all day."},
		{"the Sun stays", "The Sun is out.", "The Sun is out."},
		// Months.
		{"month before a day", "Due Jan 5.", "Due January 5th."},
		{"day after a month name", "Due November 22, 2026.", "Due November 22nd, 2026."},
		{"teens are th", "on Dec 11 and Dec 13", "on December 11th and December 13th"},
		{"a year is not a day", "since Sept 2026", "since September 2026"},
		{"a day before the month", "on 5 Mar", "on 5 March"},
		{"month range", "Jan-Mar", "January to March"},
		{"a range of full names", "Monday – Friday", "Monday to Friday"},
		{"month after in", "back in Aug.", "back in August."},
		{"a name stays", "Jan said hi to Jan's dog.", "Jan said hi to Jan's dog."},
		{"a name after in stays", "We stayed in Jan's flat.", "We stayed in Jan's flat."},
		{"an hour after a month is not a day", "Due Oct 3:15 pm", "Due October 3:15 pm"},
		{"May at the start of a sentence stays", "May 5 people come?", "May 5 people come?"},
		{"May mid-sentence is a date", "It opens May 5.", "It opens May 5th."},
		// Streets and titles.
		{"drive after a street", "Meet me at 123 Oak Dr tomorrow.", "Meet me at 123 Oak Drive tomorrow."},
		{"doctor before a name", "Dr. Smith will see you now.", "Doctor Smith will see you now."},
		{"doctor after a lowercase word", "a visit with Dr Lee", "a visit with Doctor Lee"},
		{"doctor after a sentence's first word", "Ask Dr. Lee.", "Ask Doctor Lee."},
		{"street mid-sentence drops the period", "It's on Main St. near the bank.", "It's on Main Street near the bank."},
		{"street keeps the period that ends a sentence", "Turn on Elm St. Then stop.", "Turn on Elm Street. Then stop."},
		{"street at the start of a sentence", "Oak St is closed.", "Oak Street is closed."},
		{"doctor after a later sentence's first word", "Thanks. Ask Dr. Lee.", "Thanks. Ask Doctor Lee."},
		{"saint after a list's comma", "We saw Dallas, St. Louis, and Austin.", "We saw Dallas, Saint Louis, and Austin."},
		{"saint before a name", "We visited St. Louis.", "We visited Saint Louis."},
		{"avenue after a number street", "on 5th Ave.", "on 5th Avenue."},
		{"street words after a name only", "the Rd ahead", "the Rd ahead"},
		{"highway", "Take Hwy 101 to Mt. Hood.", "Take Highway 101 to Mount Hood."},
		{"fort", "near Ft. Worth", "near Fort Worth"},
		// Units.
		{"units after a number", "about 5 km, or 3 mi, away", "about 5 kilometers, or 3 miles, away"},
		{"one is singular", "1 mi and 1.5 hrs", "1 mile and 1.5 hours"},
		{"speed", "going 65 mph", "going 65 miles per hour"},
		{"temperature", "It is 72°F, 22°C.", "It is 72 degrees Fahrenheit, 22 degrees Celsius."},
		{"minutes period mid-sentence", "in 10 min. or so", "in 10 minutes or so"},
		{"a unit needs a number", "the ft of the hill", "the ft of the hill"},
		// Latin and shorthand.
		{"e.g. gets its pause", "Bring snacks, e.g. chips, salsa, etc.", "Bring snacks, for example, chips, salsa, et cetera."},
		{"e.g. keeps a written comma", "things, e.g., chips", "things, for example, chips"},
		{"i.e.", "the first, i.e. the oldest", "the first, that is, the oldest"},
		{"etc. mid-sentence", "chips, etc. are fine", "chips, et cetera are fine"},
		{"versus", "Bulls vs Lakers, Cats vs. Dogs", "Bulls versus Lakers, Cats versus Dogs"},
		{"approx", "approx. 20 people", "approximately 20 people"},
		{"with and without", "Coffee w/ milk, toast w/o butter, tea w/lemon.", "Coffee with milk, toast without butter, tea with lemon."},
		{"a bare w/ stays", "and/or w/ 2", "and/or w/ 2"},
		{"plain text unchanged", "Sure, I'll call at noon.", "Sure, I'll call at noon."},
	}
	for _, c := range cases {
		if got := SpokenWords(c.in); got != c.want {
			t.Errorf("%s:\n  SpokenWords(%q)\n  = %q\n want %q", c.name, c.in, got, c.want)
		}
	}
}
