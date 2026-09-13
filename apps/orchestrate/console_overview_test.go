package orchestrate

// The Manage menu's two questions, and the rule that keeps them apart.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// Every menu entry says which question it answers, and a fleet entry must be
// fleet-scoped or it is asked about one agent and silently answers wrong.
func TestManageMenuIsGroupedAndScoped(t *testing.T) {
	src, err := os.ReadFile("page_chat.go")
	if err != nil {
		t.Fatal(err)
	}
	page := string(src)

	// The nav block only. Later toolbar groups use the same Group field for a
	// different menu, and matching those would make this test pass on the
	// wrong thing.
	start := strings.Index(page, "OrchestratorNav: []ui.OrchestratorNavItem{")
	if start < 0 {
		t.Fatal("the orchestrator nav block is gone")
	}
	end := strings.Index(page[start:], "AltNavFlag:")
	if end < 0 {
		t.Fatal("could not find the end of the nav block")
	}
	nav := page[start : start+end]

	perAgent := []string{"Agent overview", "Enabled agents", "Event monitors", "Recurring tasks", "Compact Cortex", "Clear Cortex"}
	fleet := []string{"Fleet overview", "Runs", "Spend", "Guardrail blocks", "Broken tools", "Decommission"}

	for _, label := range perAgent {
		line := navLine(t, nav, label)
		if !strings.Contains(line, `Group: "This agent"`) {
			t.Errorf("%q acts on the open agent but is not grouped under it:\n%s", label, line)
		}
		if strings.Contains(line, `Scope: "fleet"`) {
			t.Errorf("%q is per-agent and must not be fleet-scoped:\n%s", label, line)
		}
	}
	for _, label := range fleet {
		line := navLine(t, nav, label)
		if !strings.Contains(line, `Group: "Your fleet"`) {
			t.Errorf("%q reports on every agent but is not grouped under the fleet:\n%s", label, line)
		}
	}
	// Every fleet entry that FETCHES must be fleet-scoped. Decommission is an
	// action, not a source, so it is exempt.
	for _, label := range []string{"Fleet overview", "Runs", "Spend", "Guardrail blocks", "Broken tools"} {
		line := navLine(t, nav, label)
		if !strings.Contains(line, `Scope: "fleet"`) {
			t.Errorf("%q is fleet-wide but its source is fetched for one agent, so it answers about the wrong thing:\n%s", label, line)
		}
	}

	// Active now is gone from the menu; the Monitor app reads the same feed.
	if strings.Contains(nav, `"Active now"`) {
		t.Error("Active now is back in the Manage menu — it duplicates the Monitor app's live table")
	}
	// And its cancel button has to survive that removal somewhere.
	mon, err := os.ReadFile("../monitor/monitor.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mon), "api/console/activity/cancel") {
		t.Error("nothing can cancel an in-flight run any more: the Manage pane that could is gone and Monitor did not take the button")
	}
}

// navLine returns the declaration line for one nav label.
func navLine(t *testing.T, nav, label string) string {
	t.Helper()
	i := strings.Index(nav, `{Label: "`+label+`"`)
	if i < 0 {
		t.Fatalf("no nav entry labelled %q", label)
	}
	rest := nav[i:]
	if j := strings.Index(rest, "\n"); j >= 0 {
		return rest[:j]
	}
	return rest
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
