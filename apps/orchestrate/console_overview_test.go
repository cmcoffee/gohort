package orchestrate

// The Manage menu's two questions, and the rule that keeps them apart.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// Every menu entry says which question it answers, and a fleet entry must be
// fleet-scoped or it is asked about one agent and silently answers wrong.
//
// Three labels appear in BOTH groups, because those three are the same view
// asked about one agent and about everyone. That is the point of the headings,
// and it is why this test parses entries rather than searching for a label: a
// search would find whichever came first and check the wrong one.
func TestManageMenuIsGroupedAndScoped(t *testing.T) {
	page := readFile(t, "page_chat.go")
	entries := navEntries(t, page)

	want := []struct{ label, group string }{
		{"Agent overview", "This agent"},
		{"Enabled agents", "This agent"},
		{"Event monitors", "This agent"},
		{"Recurring tasks", "This agent"},
		{"Compact Cortex", "This agent"},
		{"Clear Cortex", "This agent"},
		{"Fleet overview", "Your fleet"},
		{"Enabled agents", "Your fleet"},
		{"Event monitors", "Your fleet"},
		{"Recurring tasks", "Your fleet"},
		{"Runs", "Your fleet"},
		{"Spend", "Your fleet"},
		{"Guardrail blocks", "Your fleet"},
		{"Broken tools", "Your fleet"},
	}
	if len(entries) != len(want) {
		t.Fatalf("the menu has %d grouped entries, expected %d:\n%s", len(entries), len(want), navSummary(entries))
	}
	// Order matters as much as membership: a heading is drawn when the group
	// value changes, so an entry out of place splits its group in two and the
	// heading appears twice.
	for i, w := range want {
		if entries[i].label != w.label || entries[i].group != w.group {
			t.Fatalf("entry %d is %q under %q, expected %q under %q:\n%s",
				i, entries[i].label, entries[i].group, w.label, w.group, navSummary(entries))
		}
	}

	for _, e := range entries {
		// Every entry that reaches the dropdown must say which group it is in.
		// An ungrouped one renders under whichever heading was drawn last,
		// which is how a fleet-wide view comes to sit under "This agent".
		if e.group == "" {
			t.Errorf("%q renders in the menu with no group, so it inherits the heading above it", e.label)
		}
		switch e.group {
		case "This agent":
			if strings.Contains(e.body, `Scope: "fleet"`) {
				t.Errorf("%q is per-agent and must not be fleet-scoped", e.label)
			}
		case "Your fleet":
			// Every fleet entry fetches, so every one must be fleet-scoped or
			// it answers about whichever agent happens to be open.
			if !strings.Contains(e.body, `Scope: "fleet"`) {
				t.Errorf("%q is fleet-wide but its source is fetched for one agent", e.label)
			}
		}
	}

	// The three panes that appear twice must be the SAME view both times, or
	// the shared label is a lie. Same source, differing only in scope.
	for _, label := range []string{"Enabled agents", "Event monitors", "Recurring tasks"} {
		mine, fleet := entryIn(t, entries, label, "This agent"), entryIn(t, entries, label, "Your fleet")
		if src := sourceOf(mine); src == "" || src != sourceOf(fleet) {
			t.Errorf("%q reads %q per-agent and %q fleet-wide; a shared label must mean a shared view",
				label, src, sourceOf(fleet))
		}
		// And the fleet copy has to be able to act, or it is a report where a
		// teardown used to be.
		if !strings.Contains(fleet.body, `{Label: "Delete"`) {
			t.Errorf("the fleet %q pane lists standing work but cannot remove any of it", label)
		}
	}

	// Active now is gone from the menu; the Monitor app reads the same feed.
	if strings.Contains(page, `{Label: "Active now"`) {
		t.Error("Active now is back in the Manage menu — it duplicates the Monitor app's live table")
	}
	// And its cancel button had to survive that removal somewhere.
	if !strings.Contains(readFile(t, "../monitor/monitor.go"), "api/console/activity/cancel") {
		t.Error("nothing can cancel an in-flight run any more: the Manage pane that could is gone and Monitor did not take the button")
	}
	// Decommission is gone, endpoint included: one irreversible click that
	// showed no list of what it would delete, replaced by the fleet panes.
	for _, f := range []string{"page_chat.go", "console.go", "console_bridges.go"} {
		if strings.Contains(readFile(t, f), "handleChannelDecommission") {
			t.Errorf("%s still wires the decommission endpoint; a route nothing points at is a way to empty a fleet by URL", f)
		}
	}
	if strings.Contains(page, `{Label: "Decommission"`) {
		t.Error("the Decommission action is back in the menu")
	}
}

// navEntry is one parsed nav declaration.
type navEntry struct {
	label string
	group string
	body  string // the whole declaration, row actions included
}

// navEntries parses the OrchestratorNav block into its top-level entries.
// Parsing beats searching here because three labels appear twice, and because
// a test that cannot tell the two apart cannot check either.
func navEntries(t *testing.T, page string) []navEntry {
	t.Helper()
	start := strings.Index(page, "OrchestratorNav: []ui.OrchestratorNavItem{")
	if start < 0 {
		t.Fatal("the orchestrator nav block is gone")
	}
	end := strings.Index(page[start:], "AltNavFlag:")
	if end < 0 {
		t.Fatal("could not find the end of the nav block")
	}
	nav := page[start : start+end]

	// Top-level entries only: they are the ones at this exact indent. A row
	// action is nested deeper, so it never matches.
	const sep = "\n\t\t\t\t\t\t{Label: "
	parts := strings.Split(nav, sep)
	var out []navEntry
	for _, part := range parts[1:] {
		label := part
		if i := strings.Index(label, `"`); i >= 0 {
			label = label[i+1:]
		}
		if i := strings.Index(label, `"`); i >= 0 {
			label = label[:i]
		}
		// Only what actually renders IN the dropdown. A Topbar or Pinned item
		// (Permissions) lives in the nav slice but is lifted out of the menu,
		// so it carries no group and must not be judged as if it did — the
		// same condition the runtime uses to decide the menu has contents.
		if strings.Contains(part, "Topbar: true") || strings.Contains(part, "Pinned: true") {
			continue
		}
		group := ""
		if i := strings.Index(part, `Group: "`); i >= 0 {
			rest := part[i+len(`Group: "`):]
			if j := strings.Index(rest, `"`); j >= 0 {
				group = rest[:j]
			}
		}
		out = append(out, navEntry{label: label, group: group, body: part})
	}
	if len(out) == 0 {
		t.Fatal("parsed no nav entries — the declaration shape changed")
	}
	return out
}

// entryIn finds the entry with one label under one group.
func entryIn(t *testing.T, entries []navEntry, label, group string) navEntry {
	t.Helper()
	for _, e := range entries {
		if e.label == label && e.group == group {
			return e
		}
	}
	t.Fatalf("no %q entry under %q", label, group)
	return navEntry{}
}

// sourceOf pulls an entry's Source URL.
func sourceOf(e navEntry) string {
	i := strings.Index(e.body, `Source: "`)
	if i < 0 {
		return ""
	}
	rest := e.body[i+len(`Source: "`):]
	if j := strings.Index(rest, `"`); j >= 0 {
		return rest[:j]
	}
	return ""
}

// navSummary renders the parsed menu for a failure message.
func navSummary(entries []navEntry) string {
	var b strings.Builder
	for i, e := range entries {
		fmt.Fprintf(&b, "  %2d  %-20s %s\n", i, e.group, e.label)
	}
	return b.String()
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// The overview is a summary, so it renders as one source with several titled
// lists. Cards group on _section, and only run rows may carry a Details button.
func TestOverviewCardsCarrySectionsAndRunMarkers(t *testing.T) {
	cards := []overviewCard{
		{Title: "3 run(s) in 24h", Section: secGlance},
		{Title: "nightly digest", Section: secRuns, Run: true, ID: "run-1"},
		{Title: "some rule", Section: secAttention},
	}
	raw, err := json.Marshal(cards)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{`"_section":"At a glance"`, `"_run":true`, `"_id":"run-1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s from the wire form:\n%s", want, body)
		}
	}
	// A non-run card must not carry the marker, or the Details button renders
	// on a statistic and opens a run record that does not exist.
	if strings.Count(body, `"_run":true`) != 1 {
		t.Errorf("only run rows may carry _run:\n%s", body)
	}
	// Title leads: the card renderer bolds the first visible field, and the
	// headline is what the eye is there for.
	if !strings.HasPrefix(body, `[{"title":`) {
		t.Errorf("title must marshal first so it renders as the card headline:\n%s", body)
	}
}

// The labels have to read correctly at the edges, because an overview is most
// often opened on an agent that has barely run.
func TestOverviewLabelsAtTheEdges(t *testing.T) {
	now := time.Now()
	if got := runCountLabel(nil, now); got != "No runs this week" {
		t.Errorf("empty ledger label = %q", got)
	}
	if got := standingWorkLabel(0, 0, 0); got != "No standing work" {
		t.Errorf("empty standing label = %q", got)
	}
	if got := standingWorkLabel(2, 0, 1); got != "2 schedule(s) · 1 recurring task(s)" {
		t.Errorf("standing label should name only the kinds that exist, got %q", got)
	}
	if got := lastRunLabel(nil, time.UTC); got != "nothing has run yet" {
		t.Errorf("empty last-run label = %q", got)
	}

	runs := []RunRecord{
		{Status: RunRunning, Started: now.Add(-2 * time.Minute)},
		{Status: RunFailed, Started: now.Add(-3 * time.Hour)},
		{Status: RunOK, Started: now.AddDate(0, 0, -3)},
		{Status: RunOK, Started: now.AddDate(0, 0, -30)}, // outside both windows
	}
	if got := runCountLabel(runs, now); got != "2 run(s) in 24h · 3 in 7 days" {
		t.Errorf("run count label = %q", got)
	}
	// Something in flight outranks something that already failed: one is still
	// yours to stop, the other is history.
	if got := runHealthStatus(runs, now); got != "1 running" {
		t.Errorf("health status = %q, want the running count to win", got)
	}
	if got := runHealthStatus(runs[1:], now); got != "1 failed" {
		t.Errorf("health status without a live run = %q", got)
	}
	if got := runHealthStatus(runs[2:], now); got != "" {
		t.Errorf("a clean week should show no pill, got %q", got)
	}
}

// failedRuns is what the "Needs attention" section is built from: recent
// failures only, newest first, capped.
func TestFailedRunsWindowAndCap(t *testing.T) {
	now := time.Now()
	runs := []RunRecord{
		{Status: RunFailed, Started: now.Add(-time.Hour), Agent: "a"},
		{Status: RunOK, Started: now.Add(-2 * time.Hour), Agent: "b"},
		{Status: RunFailed, Started: now.Add(-3 * time.Hour), Agent: "c"},
		{Status: RunFailed, Started: now.AddDate(0, 0, -20), Agent: "old"},
	}
	got := failedRuns(runs, now, 5)
	if len(got) != 2 || got[0].Agent != "a" || got[1].Agent != "c" {
		t.Fatalf("failedRuns = %+v, want the two recent failures newest first", got)
	}
	if len(failedRuns(runs, now, 1)) != 1 {
		t.Error("the cap is not applied")
	}
}
