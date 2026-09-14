package orchestrate

// The route from a summary figure to the rows it counts.

import (
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// A View names its target "<Menu>/<Label>". Nothing resolves it until someone
// clicks, and a miss is silent in the UI (a console line and a button that
// does nothing), so the declaration is checked here instead.
func TestEveryNavViewTargetExists(t *testing.T) {
	page := readFile(t, "page_chat.go")
	entries := navEntries(t, page)
	targets := map[string]bool{}
	for _, e := range entries {
		menu := e.menu
		if menu == "" {
			menu = "Manage" // the runtime's default
		}
		targets[menu+"/"+e.label] = true
	}
	var found int
	const key = `View: "`
	for i := 0; ; {
		j := strings.Index(page[i:], key)
		if j < 0 {
			break
		}
		rest := page[i+j+len(key):]
		i += j + len(key)
		k := strings.Index(rest, `"`)
		if k < 0 {
			break
		}
		want := rest[:k]
		found++
		if !targets[want] {
			t.Errorf("a row action navigates to %q, which no nav item declares", want)
		}
	}
	if found == 0 {
		t.Skip("no navigating row actions declared")
	}
}

// The failure count and the button that opens the list must agree about
// whether there is anything to see: a pill reading "2 failed" beside no way
// through is the dead end this replaced, and a button on a clean week is worse.
func TestTheFailuresLinkShowsExactlyWhenThePillCounts(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		runs []RunRecord
		want bool
	}{
		{"nothing ran", nil, false},
		{"a clean week", []RunRecord{{Status: RunOK, Started: now.Add(-2 * time.Hour)}}, false},
		{"one failure this week", []RunRecord{{Status: RunFailed, Started: now.Add(-2 * time.Hour)}}, true},
		// The pill's window is seven days. An older failure is not what it
		// counts, so offering to open the list would open an empty one.
		{"a failure older than the window", []RunRecord{{Status: RunFailed, Started: now.AddDate(0, 0, -9)}}, false},
	}
	for _, c := range cases {
		if got := hasRecentFailure(c.runs, now); got != c.want {
			t.Errorf("%s: link shown = %v, want %v", c.name, got, c.want)
		}
		// And the pill itself must say the same thing, or the card contradicts
		// its own button.
		pill := strings.Contains(runHealthStatus(c.runs, now), "failed")
		if pill != c.want {
			t.Errorf("%s: pill says failed = %v but the link says %v", c.name, pill, c.want)
		}
	}
}
