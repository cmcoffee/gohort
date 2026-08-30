package sandbox

// The doctor's arithmetic, which is the part that can be wrong quietly.
//
// The report itself is prose and reads fine or does not. What can be WRONG is
// the subuid range it tells an operator to paste into a privileged command: a
// range that overlaps an existing delegation hands two users the same host
// uids, which is a containment failure with a sudo command in front of it.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fmtSscan keeps the overlap check readable without pulling strconv in for one
// parse in a test.
func fmtSscan(s string, out *int) (int, error) { return fmt.Sscanf(s, "%d", out) }

func TestSubIDRangeParsingAndDetection(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "subuid")
	if err := os.WriteFile(sub, []byte("medic:100000:65536\nother:200000:65536\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !hasSubID(sub, "medic") {
		t.Error("an existing delegation was not seen")
	}
	// Prefix matching has to be on "name:" and not on "name", or a host with
	// "craig" and "craigslist" would report the wrong one as delegated.
	if hasSubID(sub, "med") {
		t.Error("a partial username matched a delegation that is not theirs")
	}
	if hasSubID(sub, "craig") {
		t.Error("an absent user reported as delegated")
	}
	// A file that is not there is not a delegation.
	if hasSubID(filepath.Join(dir, "nope"), "medic") {
		t.Error("a missing subuid file reported a delegation")
	}
}

// The range must clear everything already delegated in EITHER file. A host can
// have subuid without subgid, and picking from only one would collide with the
// other.
func TestFreeSubIDRangeClearsExistingDelegations(t *testing.T) {
	start, count := freeSubIDRange()
	if count != 65536 {
		t.Errorf("range size = %d, want 65536", count)
	}
	if start%100000 != 0 {
		t.Errorf("start %d is not round; a human edits this file", start)
	}
	for _, path := range []string{"/etc/subuid", "/etc/subgid"} {
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(body), "\n") {
			parts := strings.Split(strings.TrimSpace(line), ":")
			if len(parts) != 3 {
				continue
			}
			var s, n int
			if _, err := fmtSscan(parts[1], &s); err != nil {
				continue
			}
			if _, err := fmtSscan(parts[2], &n); err != nil {
				continue
			}
			if start < s+n && s < start+count {
				t.Errorf("proposed range %d-%d overlaps existing %d-%d from %s",
					start, start+count-1, s, s+n-1, path)
			}
		}
	}
}

// Below 3.11 the operator is told what a modern image costs them; at or above
// it, the runtime probe's exact comparison is the right place and the report
// stays quiet rather than guessing.
func TestPythonSkewAdviceOnlyFiresWhenItHasSomethingToSay(t *testing.T) {
	if adv := pythonSkewAdvice(3, 6); len(adv) == 0 {
		t.Error("3.6 against a modern image is exactly the case worth naming")
	} else if !strings.Contains(strings.Join(adv, " "), "3.6") {
		t.Error("the advice should name the version the wheels were built for")
	}
	if adv := pythonSkewAdvice(3, 13); len(adv) != 0 {
		t.Errorf("a current interpreter needs no warning here: %v", adv)
	}
}

// The report has to distinguish "nobody configured anything" from "podman was
// asked for and is not running". Those are different problems and only the
// second one is urgent.
func TestTheReportNamesAnUnmetRequest(t *testing.T) {
	t.Setenv("GOHORT_SANDBOX_BACKEND", "")
	if s := requestedSuffix(); s != "" {
		t.Errorf("an unconfigured host should not claim a request: %q", s)
	}
	t.Setenv("GOHORT_SANDBOX_BACKEND", "podman")
	if s := requestedSuffix(); !strings.Contains(s, "NOT what is running") {
		// activeSandbox() on this host is bubblewrap or none, never podman.
		t.Errorf("an unmet request must say so: %q", s)
	}
}

// Only the two families that were actually tested get a command. A confident
// wrong command costs more than the sentence it replaced.
func TestPackageManagerIsNarrowOnPurpose(t *testing.T) {
	switch m := packageManager(); m {
	case "", "dnf", "apt-get":
	default:
		t.Errorf("packageManager returned %q; only dnf and apt-get are verified", m)
	}
}
