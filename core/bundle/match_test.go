package bundle

import (
	"regexp"
	"strings"
	"testing"
)

// The one property that matters: the prefilter never turns down a line the
// regexp would match. Every pattern here is run against every line, through
// both the string and the byte path, and has to agree with regexp exactly.
// The lines are chosen to sit on the edges the needle rules exist for.
func TestMatcherAgreesWithRegexp(t *testing.T) {
	patterns := []string{
		"connection reset",
		"(?i)connection reset",
		"(?i)KILL",
		"(?i)session",
		"(?i)ERROR \\d+",
		"(?i)foo|bar",
		"(?i)(?:foo)?bar",
		"(?i)x(foo)*bar",
		"(?i)(foo){2}",
		"(?i)(foo){0,2}bar",
		"(?i)(foo)+bar",
		"(?i)café",
		"Café",
		"a\uFFFDb",
		"(?i)a\uFFFDb",
		"^2026-.*timeout",
		"[Ee]rror: (\\w+) failed",
		"(?i)id=0*42\\b",
		".*",
		"x",
		"(?i)é",
	}
	lines := []string{
		"",
		"connection reset by peer",
		"Connection Reset by peer",
		"CONNECTION RESET",
		"connection  reset",
		"Kill -9 sent", // Kelvin sign folds to k
		"KILL -9 sent",
		"ſession closed", // long s folds to s
		"SESSION closed",
		"error 42 raised",
		"ERROR x",
		"foo", "bar", "BAR", "FOOBAR", "xbar", "XFOOFOOBAR", "FOOFOO", "fo o",
		"CAFÉ au lait", "café", "Café",
		"a\xffb",   // invalid byte, which regexp reads as U+FFFD
		"a\uFFFDb", // the real thing
		"2026-10-27 request timeout",
		"2025-10-27 request timeout",
		"Error: disk failed", "error: net failed", "ERROR: net failed",
		"id=00042 ok", "ID=42", "id=420",
		"x", "X", "É",
	}
	for _, p := range patterns {
		re := regexp.MustCompile(p)
		m, err := CompileMatcher(p)
		if err != nil {
			t.Fatalf("CompileMatcher(%q): %v", p, err)
		}
		for _, ln := range lines {
			want := re.MatchString(ln)
			if got := m.MatchString(ln); got != want {
				t.Errorf("MatchString(%q, %q) = %v, regexp says %v (needle %q fold=%v)", p, ln, got, want, m.need, m.fold)
			}
			if got := m.Match([]byte(ln)); got != want {
				t.Errorf("Match(%q, %q) = %v, regexp says %v (needle %q fold=%v)", p, ln, got, want, m.need, m.fold)
			}
		}
	}
}

// The prefilter has to actually engage on the patterns searches send, or the
// matcher is just a slower regexp. And it must stand down where the pattern
// requires no text at all.
func TestMatcherFindsTheNeedle(t *testing.T) {
	cases := []struct {
		pattern, need string
		fold          bool
	}{
		{"(?i)connection reset", "connection re", true}, // cut at the s, which folds outside ASCII
		{"connection reset", "connection reset", false},
		{"(?i)timeout after \\d+ms", "timeout after ", true},
		{"(?i)\\Qhttp://host/api\\E", "http://ho", true},
		{"[Ee]rror: (\\w+) failed", " failed", false}, // the longer of the two required runs
		{"(?i)foo|barbaz", "", false},
		{"(?i)(?:needle)?x", "x", true},
		{"(?i)(longer)+y", "longer", true},
		{".*", "", false},
	}
	for _, c := range cases {
		m, err := CompileMatcher(c.pattern)
		if err != nil {
			t.Fatalf("CompileMatcher(%q): %v", c.pattern, err)
		}
		if m.need != c.need || (c.need != "" && m.fold != c.fold) {
			t.Errorf("%q: needle %q fold=%v, want %q fold=%v", c.pattern, m.need, m.fold, c.need, c.fold)
		}
	}
}

// A bad pattern reports regexp's own error, so callers' messages read the same.
func TestMatcherRejectsWhatRegexpRejects(t *testing.T) {
	_, want := regexp.Compile("(unclosed")
	_, got := CompileMatcher("(unclosed")
	if got == nil || got.Error() != want.Error() {
		t.Errorf("CompileMatcher error = %v, want %v", got, want)
	}
}

// Possible may only say no when no line of the text could match: it is how a
// caller holding a whole file skips it, and a wrong no is a file never searched.
func TestMatcherPossibleNeverRulesOutAMatch(t *testing.T) {
	file := "package auth\nfunc CheckToken() {}\n\u212Aelvin\n"
	for _, p := range []string{"(?i)checktoken", "(?i)kelvin", "Check.*\\(", "(?i)nothing here", ".*", "^func"} {
		m, err := CompileMatcher(p)
		if err != nil {
			t.Fatal(err)
		}
		anyLine := false
		for _, ln := range strings.Split(file, "\n") {
			anyLine = anyLine || m.MatchString(ln)
		}
		if anyLine && !m.Possible(file) {
			t.Errorf("%q matches a line but Possible ruled the file out", p)
		}
	}
	if m, _ := CompileMatcher("(?i)nothing here"); m.Possible(file) {
		t.Error("Possible should rule out a file without the needle")
	}
}
