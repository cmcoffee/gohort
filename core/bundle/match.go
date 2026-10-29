// Matching a search pattern against log lines.
//
// A search looks at every line of every file and almost none of them match, so
// the cost that decides how long a search takes is the cost of REJECTING a
// line. Go's regexp rejects quickly when it can scan for a literal, and case
// folding takes that away: "(?i)connection reset" measured about forty times
// slower per line than the same pattern without the flag, and every bundle
// search folds case. Most patterns name some text a match has to contain (an
// error string, an id, a hostname), so a line is checked for that text first
// and the regexp runs only on the lines that have it.
//
// The check is a prefilter. It may let through a line the regexp then turns
// down, but it must never turn down a line the regexp would match: that is a
// search reporting an absence nobody established. Every rule below that makes
// the needle shorter than it could be exists for that.
package bundle

import (
	"bytes"
	"regexp"
	"regexp/syntax"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Matcher is a compiled search pattern with its prefilter.
type Matcher struct {
	re *regexp.Regexp
	// needs is the prefilter: every match contains at least ONE of these. A
	// single entry for a pattern that names text every match contains; one
	// per branch for an alternation ("error|warn|fail"); none when the
	// pattern names nothing, and then every line goes to the regexp.
	needs []needle
}

// needle is one literal the prefilter looks for.
type needle struct {
	text string
	b    []byte // text, for byte lines, so a check converts nothing
	fold bool   // text is lowercase ASCII and is looked for in either case
}

// maxNeedles bounds the prefilter's set. Each entry is a scan of the line,
// and past a handful the scans cost about what the regexp does.
const maxNeedles = 8

// CompileMatcher compiles expr exactly as regexp.Compile does, and returns
// regexp.Compile's error unchanged so a caller's message about a bad pattern
// reads the same as it did before.
func CompileMatcher(expr string) (*Matcher, error) {
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, err
	}
	m := &Matcher{re: re}
	// Parsed with the flags regexp.Compile uses, so the tree is the one the
	// regexp runs.
	if tree, err := syntax.Parse(expr, syntax.Perl); err == nil {
		for _, l := range needSet(tree) {
			m.needs = append(m.needs, needle{text: l.text, b: []byte(l.text), fold: l.fold})
		}
	}
	return m, nil
}

// has reports whether a line holds the needle.
func (n needle) has(s string) bool {
	if n.fold {
		return containsFold(s, n.text)
	}
	return strings.Contains(s, n.text)
}

func (n needle) hasBytes(b []byte) bool {
	if n.fold {
		return containsFold(b, n.text)
	}
	return bytes.Contains(b, n.b)
}

// MatchString reports whether s contains a match.
func (m *Matcher) MatchString(s string) bool {
	return m.Possible(s) && m.re.MatchString(s)
}

// Match reports whether b contains a match. For a caller holding the bytes of
// a line it has not yet made a string, which on a line that does not match it
// never has to.
func (m *Matcher) Match(b []byte) bool {
	if len(m.needs) > 0 {
		hit := false
		for _, n := range m.needs {
			if n.hasBytes(b) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return m.re.Match(b)
}

// Possible reports whether s could hold a match on any of its lines. False is
// certain: s holds none of the text a match needs, so a caller holding a whole
// file can skip it without splitting it into lines. True says nothing, and
// neither does a pattern that names no such text.
func (m *Matcher) Possible(s string) bool {
	if len(m.needs) == 0 {
		return true
	}
	for _, n := range m.needs {
		if n.has(s) {
			return true
		}
	}
	return false
}

// literal is text a match must contain; fold means in either case.
type literal struct {
	text string
	fold bool
}

// needSet returns literals such that every match of re contains at least one
// of them, or nil when re promises no such text. It only descends where a
// subexpression has to be present in a match: a literal, a concatenation, a
// group, a repeat of at least one, and an alternation, which needs one of its
// branches and so offers the union of their sets when EVERY branch has one. A
// single branch that requires nothing, an optional or a starred part, or a
// character class leaves nothing to promise.
func needSet(re *syntax.Regexp) []literal {
	switch re.Op {
	case syntax.OpLiteral:
		// Every run is required; the longest is the one fewest lines hold.
		var best []literal
		for _, l := range literalRuns(re.Rune, re.Flags&syntax.FoldCase != 0) {
			if best == nil || len(l.text) > len(best[0].text) {
				best = []literal{l}
			}
		}
		return best
	case syntax.OpConcat:
		// Every part is required, so any one part's set will do: the best.
		var best []literal
		for _, sub := range re.Sub {
			if set := needSet(sub); betterSet(set, best) {
				best = set
			}
		}
		return best
	case syntax.OpCapture, syntax.OpPlus:
		return needSet(re.Sub[0])
	case syntax.OpRepeat:
		if re.Min >= 1 {
			return needSet(re.Sub[0])
		}
	case syntax.OpAlternate:
		var union []literal
		seen := map[literal]bool{}
		for _, sub := range re.Sub {
			set := needSet(sub)
			if set == nil {
				return nil
			}
			for _, l := range set {
				if !seen[l] {
					seen[l] = true
					union = append(union, l)
				}
			}
		}
		if len(union) > maxNeedles {
			return nil
		}
		return union
	}
	return nil
}

// betterSet reports whether a filters more lines than b: its shortest literal
// is longer (a set is only as selective as its least selective entry), and on
// a tie it has fewer entries to scan for.
func betterSet(a, b []literal) bool {
	if len(a) == 0 {
		return false
	}
	if len(b) == 0 {
		return true
	}
	if sa, sb := shortest(a), shortest(b); sa != sb {
		return sa > sb
	}
	return len(a) < len(b)
}

func shortest(set []literal) int {
	n := len(set[0].text)
	for _, l := range set[1:] {
		if len(l.text) < n {
			n = len(l.text)
		}
	}
	return n
}

// literalRuns splits a literal into the runs a plain substring check can
// stand in for, breaking it at every rune that would make the check stricter
// than the regexp.
//
// U+FFFD is broken out in both modes, because the regexp reads every invalid
// byte in a line as U+FFFD while a substring check looks for its encoding.
// Under folding, so is every rune outside ASCII and every ASCII letter that
// folds to something outside it: "(?i)k" matches the Kelvin sign and
// "(?i)s" the long s, and an ASCII comparison would turn both lines down.
func literalRuns(runes []rune, fold bool) []literal {
	var out []literal
	var run strings.Builder
	flush := func() {
		if run.Len() > 0 {
			out = append(out, literal{text: run.String(), fold: fold})
			run.Reset()
		}
	}
	for _, r := range runes {
		switch {
		case r == utf8.RuneError:
			flush()
		case !fold:
			run.WriteRune(r)
		case r < utf8.RuneSelf && foldsInASCII[r]:
			run.WriteByte(lowerASCII[r])
		default:
			flush()
		}
	}
	flush()
	return out
}

// foldsInASCII marks the ASCII runes whose whole case-folding orbit is ASCII.
// Derived from unicode.SimpleFold, the table the regexp folds with, rather
// than written out, so it cannot disagree with it.
var foldsInASCII [utf8.RuneSelf]bool

// lowerASCII maps a byte to its ASCII lowercase, every other byte to itself.
var lowerASCII [256]byte

func init() {
	for c := rune(0); c < utf8.RuneSelf; c++ {
		ok := true
		for f := unicode.SimpleFold(c); f != c; f = unicode.SimpleFold(f) {
			if f >= utf8.RuneSelf {
				ok = false
				break
			}
		}
		foldsInASCII[c] = ok
	}
	for i := range lowerASCII {
		c := byte(i)
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		lowerASCII[i] = c
	}
}

// containsFold reports whether s contains need in either case. need is
// lowercase ASCII (literalRuns guarantees it), so a byte of s compares by its
// lowercase and a multi-byte rune in s can never equal any part of need.
func containsFold[T string | []byte](s T, need string) bool {
	n := len(need)
	first, alt := need[0], need[0]
	if 'a' <= first && first <= 'z' {
		alt = first - ('a' - 'A')
	}
	for i := 0; i+n <= len(s); i++ {
		if c := s[i]; c != first && c != alt {
			continue
		}
		j := 1
		for j < n && lowerASCII[s[i+j]] == need[j] {
			j++
		}
		if j == n {
			return true
		}
	}
	return false
}

// Filter is what a search asks of a line: a pattern it must match, and
// optionally one it must not. Exclusion is its own pattern because the regexp
// syntax cannot say "but not": there is no lookahead, so "ERROR, but not the
// health check" is not one expression, and without this a search over a noisy
// log pages through the noise.
type Filter struct {
	Match *Matcher
	// Also are further patterns looked for in the same pass, by a search of
	// several (lanes.go). Each is its own lane, answered as if searched
	// alone: Match is lane 0, Also[i] lane i+1.
	Also    []*Matcher
	Exclude *Matcher // nil when nothing is excluded
	// Level keeps only lines declaring a severity at this rank or worse
	// (ParseLevel); zero keeps every line.
	Level int
}

// LevelOK reports whether a line passes the filter's severity: always, with
// no Level set; otherwise only when the line declares one at Level or worse.
// A line declaring none (a stack frame, a wrapped message) does not.
func (f Filter) LevelOK(line string) bool {
	if f.Level == 0 {
		return true
	}
	r := severityRank[severityOf(line)]
	return r > 0 && r <= f.Level
}

// Lanes is how many patterns the filter looks for.
func (f Filter) Lanes() int { return 1 + len(f.Also) }

// Which reports the patterns a line matches as a set of lanes, bit i for lane
// i; zero for none. Each pattern runs its own prefilter, so a line none of
// them could match costs a few substring tests.
func (f Filter) Which(b []byte) uint32 {
	var m uint32
	if f.Match.Match(b) {
		m = 1
	}
	for i, a := range f.Also {
		if a.Match(b) {
			m |= 2 << i
		}
	}
	return m
}

// Possible reports whether any of the filter's patterns could match a line in
// text, by their prefilters alone (Matcher.Possible): false means none can,
// so text can be passed over unread.
func (f Filter) Possible(text string) bool {
	if f.Match.Possible(text) {
		return true
	}
	for _, a := range f.Also {
		if a.Possible(text) {
			return true
		}
	}
	return false
}

// WhichString is Which for a line held as a string.
func (f Filter) WhichString(s string) uint32 {
	var m uint32
	if f.Match.MatchString(s) {
		m = 1
	}
	for i, a := range f.Also {
		if a.MatchString(s) {
			m |= 2 << i
		}
	}
	return m
}

// Excludes reports whether a line the pattern matched is taken back out.
func (f Filter) Excludes(b []byte) bool { return f.Exclude != nil && f.Exclude.Match(b) }

// ExcludesString is Excludes for a line held as a string.
func (f Filter) ExcludesString(s string) bool {
	return f.Exclude != nil && f.Exclude.MatchString(s)
}
