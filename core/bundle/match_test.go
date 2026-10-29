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
		// Alternations: the prefilter becomes "any one of these".
		"(?i)error|warn|fail",
		"(?i)timeout|refused",
		"(?i)KILL|session",
		"(?i)err(or)?|warn",
		"foo|.*bar",
		"(?i)(ab|cd)ef",
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
				t.Errorf("MatchString(%q, %q) = %v, regexp says %v (needles %s)", p, ln, got, want, needsOf(m))
			}
			if got := m.Match([]byte(ln)); got != want {
				t.Errorf("Match(%q, %q) = %v, regexp says %v (needles %s)", p, ln, got, want, needsOf(m))
			}
		}
	}
}

// needsOf renders a matcher's prefilter as "a|b", each folded entry marked
// with a trailing ~, for test messages and expectations.
func needsOf(m *Matcher) string {
	var parts []string
	for _, n := range m.needs {
		if n.fold {
			parts = append(parts, n.text+"~")
		} else {
			parts = append(parts, n.text)
		}
	}
	return strings.Join(parts, "|")
}

// The prefilter has to actually engage on the patterns searches send, or the
// matcher is just a slower regexp. And it must stand down where the pattern
// requires no text at all. A trailing ~ marks a folded needle.
func TestMatcherFindsTheNeedle(t *testing.T) {
	for pattern, want := range map[string]string{
		"(?i)connection reset":      "connection re~", // cut at the s, which folds outside ASCII
		"connection reset":          "connection reset",
		"(?i)timeout after \\d+ms":  "timeout after ~",
		"(?i)\\Qhttp://host/api\\E": "http://ho~",
		"[Ee]rror: (\\w+) failed":   " failed", // the longer of the two required runs
		"(?i)(?:needle)?x":          "x~",
		"(?i)(longer)+y":            "longer~",
		".*":                        "",
		// An alternation needs one of its branches: any of their needles.
		"(?i)foo|barbaz":                      "foo~|barbaz~",
		"(?i)error|warn|fail":                 "error~|warn~|fail~",
		"(?i)timeout|refused":                 "timeout~|refu~", // refused cut at its s
		"(?i)err(or)?|warn":                   "err~|warn~",
		"timeout|timer":                       "time", // the parser factors the shared prefix, and one long needle beats two
		"(?i)foo|.*":                          "",     // a branch that needs nothing: the whole needs nothing
		"aa1|bb2|cc3|dd4|ee5|ff6|gg7|hh8|ii9": "",     // past maxNeedles, the scans cost what the regexp does
	} {
		m, err := CompileMatcher(pattern)
		if err != nil {
			t.Fatalf("CompileMatcher(%q): %v", pattern, err)
		}
		if got := needsOf(m); got != want {
			t.Errorf("%q: needles %q, want %q", pattern, got, want)
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
