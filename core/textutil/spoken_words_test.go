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
		{"day after a month name", "Due November 22, 2026.", "Due November 22nd, 20 26."},
		{"teens are th", "on Dec 11 and Dec 13", "on December 11th and December 13th"},
		{"a year is not a day", "since Sept 2026", "since September 20 26"},
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
		{"one is singular", "1 mi and 1.5 hrs", "1 mile and 1 point 5 hours"},
		{"speed", "going 65 mph", "going 65 miles per hour"},
		{"temperature", "It is 72°F, 22°C.", "It is 72 degrees Fahrenheit, 22 degrees Celsius."},
		{"minutes period mid-sentence", "in 10 min. or so", "in 10 minutes or so"},
		{"a unit needs a number", "the ft of the hill", "the ft of the hill"},
		// Dots in numbers.
		{"a decimal is a point", "about 3.5 miles", "about 3 point 5 miles"},
		{"a decimal unit", "it weighs 2.75 kg.", "it weighs 2 point 7 5 kilograms."},
		{"digits after the point one by one", "pi is 3.14, or 3.05 rounded", "pi is 3 point 1 4, or 3 point 0 5 rounded"},
		{"a name's number is a decimal too", "Qwen 3.5 is out.", "Qwen 3 point 5 is out."},
		{"two dots are a version", "Version 0.9.282 shipped.", "Version 0 dot 9 dot 282 shipped."},
		{"an address", "at 192.168.1.1 now", "at 192 dot 168 dot 1 dot 1 now"},
		{"a sentence's period is not a dot", "It took 4.5. Then 2.", "It took 4 point 5. Then 2."},
		{"money stays", "It costs $3.50, or £2.10.", "It costs $3.50, or £2.10."},
		{"thousands with a decimal", "1,234.5 people", "1,234 point 5 people"},
		{"a percent", "up 3.5%", "up 3 point 5%"},
		// Versions.
		{"v is version", "Running v0.9.290 now, up from v3.", "Running version 0 dot 9 dot 290 now, up from version 3."},
		{"a v decimal", "the v3.5 model", "the version 3 point 5 model"},
		{"a capital V or a v inside a word stays", "a V8 engine on dev3", "a V8 engine on dev3"},
		// Four digits, in pairs.
		{"four digits in pairs", "In 1520 and 2026, 1520 people came.", "In 15 20 and 20 26, 15 20 people came."},
		{"a zero third is oh", "It opened in 1905.", "It opened in 19 oh 5."},
		{"hundreds", "By 1500 it was over.", "By 15 hundred it was over."},
		{"thousands and two thousand five stay", "In 2000, 2005 and 3000.", "In 2000, 2005 and 3000."},
		{"a comma is an amount", "We counted 1,520 votes.", "We counted 1,520 votes."},
		{"a price", "It costs $1520 now.", "It costs 15 20 dollars now."},
		{"inside a dotted number", "Version 0.9.1520 and 1520.5 km", "Version 0 dot 9 dot 1520 and 1520 point 5 kilometers"},
		{"five digits and a leading zero stay", "Zip 15201, code 0420.", "Zip 15201, code 0420."},
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
