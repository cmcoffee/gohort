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
	re    *regexp.Regexp
	need  string // text every match contains; empty when the pattern names none
	needB []byte // need, for byte lines, so a check converts nothing
	fold  bool   // need is lowercase ASCII and is looked for in either case
}

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
	// regexp runs. Longest literal wins: it is the one fewest lines contain.
	if tree, err := syntax.Parse(expr, syntax.Perl); err == nil {
		var best literal
		for _, l := range requiredLiterals(tree) {
			if len(l.text) > len(best.text) {
				best = l
			}
		}
		m.need, m.needB, m.fold = best.text, []byte(best.text), best.fold
	}
	return m, nil
}

// MatchString reports whether s contains a match.
func (m *Matcher) MatchString(s string) bool {
	switch {
	case m.need == "":
	case m.fold:
		if !containsFold(s, m.need) {
			return false
		}
	case !strings.Contains(s, m.need):
		return false
	}
	return m.re.MatchString(s)
}

// Match reports whether b contains a match. For a caller holding the bytes of
// a line it has not yet made a string, which on a line that does not match it
// never has to.
func (m *Matcher) Match(b []byte) bool {
	switch {
	case m.need == "":
	case m.fold:
		if !containsFold(b, m.need) {
			return false
		}
	case !bytes.Contains(b, m.needB):
		return false
	}
	return m.re.Match(b)
}

// literal is text a match must contain; fold means in either case.
type literal struct {
	text string
	fold bool
}

// requiredLiterals lists literals that every match of re contains. It only
// descends where the subexpression has to be present in a match: through a
// concatenation, a group, and a repeat of at least one. An alternation, an
// optional or a starred part requires nothing, so it contributes nothing.
func requiredLiterals(re *syntax.Regexp) []literal {
	switch re.Op {
	case syntax.OpLiteral:
		return literalRuns(re.Rune, re.Flags&syntax.FoldCase != 0)
	case syntax.OpConcat:
		var out []literal
		for _, sub := range re.Sub {
			out = append(out, requiredLiterals(sub)...)
		}
		return out
	case syntax.OpCapture, syntax.OpPlus:
		return requiredLiterals(re.Sub[0])
	case syntax.OpRepeat:
		if re.Min >= 1 {
			return requiredLiterals(re.Sub[0])
		}
	}
	return nil
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
