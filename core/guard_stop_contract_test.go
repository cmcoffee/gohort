package core

// The STOP verdict is a contract carried in prose, which is the shape that
// broke the paste marker.
//
// Three places build one and one place recognises it, across two packages. A
// non-match is a LEGAL outcome there — a tool result that is not a guard
// verdict is the common case — so a producer whose wording drifts does not
// fail, it just stops being recognised, and the loop starts counting guard
// verdicts as successful tool calls.
//
// This scans the source because orchestrate cannot see an unexported const and
// should not gain an exported one for a string.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// stopProducerRE finds a literal that OPENS a STOP verdict. Only a literal:
// a comment mentioning one is not a producer.
var stopProducerRE = regexp.MustCompile(`"STOP[:\s]`)

func TestEveryGuardStopUsesTheSharedPrefix(t *testing.T) {
	root := ".."
	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && info.IsDir() {
				switch info.Name() {
				case ".git", "node_modules", "vendor":
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if !stopProducerRE.MatchString(line) {
				continue
			}
			// The shared const is how a producer SHOULD spell it; a literal is
			// only acceptable when it carries the exact prefix the matcher
			// looks for.
			// A comment is not a producer.
			if t := strings.TrimSpace(line); strings.HasPrefix(t, "//") || strings.HasPrefix(t, "*") {
				continue
			}
			// Acceptable two ways: built from the shared const, or spelled
			// with the exact opening the matcher anchors on.
			if strings.Contains(line, "guardStopPrefix") {
				continue
			}
			if guardStopRE.MatchString(strings.ReplaceAll(strings.Trim(strings.TrimSpace(line), "\t"), `"`, "\n")) {
				continue
			}
			offenders = append(offenders, strings.TrimSpace(path+": "+strings.TrimSpace(line)))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	for _, o := range offenders {
		if len(o) > 160 {
			o = o[:160] + "…"
		}
		t.Errorf("a STOP verdict that isGuardStopResult will not recognise:\n  %s", o)
	}
}

// And the matcher really does recognise what the producers build.
func TestTheMatcherRecognisesAProducedVerdict(t *testing.T) {
	for _, s := range []string{
		guardStopPrefix + "you have already called 'x' with these exact arguments 3 times",
		"[untrusted content follows]\n" + guardStopPrefix + "you have tried 4 different ways past an enforced limit",
		// The three the old Contains("STOP: you") scan missed.
		guardStopPrefix + "'send_email' was NOT called. It has already run 6 time(s)",
		"fence\n" + guardStopPrefix + "'x' was NOT called. Its id is \"z\", an identifier nothing produced",
		guardStopPrefix + "you've dispatched \"Comedian\" 120 times this turn",
	} {
		if !isGuardStopResult(s) {
			t.Errorf("a real verdict was not recognised: %q", s)
		}
	}
	// Ordinary output that merely mentions stopping is not a verdict.
	for _, s := range []string{
		"The deploy will STOP if the check fails.",
		"stop: you should see the log",
		// Mid-sentence, which the old Contains-anywhere scan would have taken.
		"the log said STOP: you can ignore that line",
		"",
	} {
		if isGuardStopResult(s) {
			t.Errorf("ordinary output was taken for a guard verdict: %q", s)
		}
	}
}
