package orchestrate

// The nav menus' separate questions, and the rule that keeps them apart.

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
// Three labels appear in BOTH menus, because those three are the same view
// asked about one agent and about everyone. That is what the menu names carry,
// and it is why this test parses entries rather than searching for a label: a
// search would find whichever came first and check the wrong one.
func TestNavMenusAreNamedAndScoped(t *testing.T) {
	page := readFile(t, "page_chat.go")
	entries := navEntries(t, page)

	want := []struct{ label, menu string }{
		{"Overview", agentMenu},
		{"Enabled agents", agentMenu},
		{"Event monitors", agentMenu},
		{"Recurring tasks", agentMenu},
		{"Compact Cortex", agentMenu},
		{"Clear Cortex", agentMenu},
		{"Overview", "Fleet"},
		{"Enabled agents", "Fleet"},
		{"Event monitors", "Fleet"},
		{"Recurring tasks", "Fleet"},
		{"Runs", "Fleet"},
		{"Spend", "Fleet"},
		{"Guardrail blocks", "Fleet"},
		{"Broken tools", "Fleet"},
	}
	if len(entries) != len(want) {
		t.Fatalf("the nav has %d menu entries, expected %d:\n%s", len(entries), len(want), navSummary(entries))
	}
	// Order matters as much as membership: a menu is created when its name is
	// first seen and the buttons sit in that order, so an entry out of place
	// reorders the topbar or lands in the wrong panel.
	for i, w := range want {
		if entries[i].label != w.label || entries[i].menu != w.menu {
			t.Fatalf("entry %d is %q in %q, expected %q in %q:\n%s",
				i, entries[i].label, entries[i].menu, w.label, w.menu, navSummary(entries))
		}
	}

	for _, e := range entries {
		// Every entry that reaches a dropdown must name its menu. An unnamed
		// one falls into the default "Manage" menu, which would appear as a
		// fourth button holding whatever was forgotten.
		if e.menu == "" {
			t.Errorf("%q names no menu, so it lands in a default Manage dropdown of its own", e.label)
		}
		switch e.menu {
		case agentMenu:
			if strings.Contains(e.body, `Scope: "fleet"`) {
				t.Errorf("%q is per-agent and must not be fleet-scoped", e.label)
			}
		case "Fleet":
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
		mine, fleet := entryIn(t, entries, label, agentMenu), entryIn(t, entries, label, "Fleet")
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

	// Active now is gone from the nav; the Monitor app reads the same feed.
	if strings.Contains(page, `{Label: "Active now"`) {
		t.Error("Active now is back in the nav — it duplicates the Monitor app's live table")
	}
	// And its cancel button had to survive that removal somewhere.
	if !strings.Contains(readFile(t, "../monitor/monitor.go"), "api/console/activity/cancel") {
		t.Error("nothing can cancel an in-flight run any more: the nav pane that could is gone and Monitor did not take the button")
	}
	// Decommission is gone, endpoint included: one irreversible click that
	// showed no list of what it would delete, replaced by the fleet panes.
	for _, f := range []string{"page_chat.go", "console.go", "console_bridges.go"} {
		if strings.Contains(readFile(t, f), "handleChannelDecommission") {
			t.Errorf("%s still wires the decommission endpoint; a route nothing points at is a way to empty a fleet by URL", f)
		}
	}
	if strings.Contains(page, `{Label: "Decommission"`) {
		t.Error("the Decommission action is back in the nav")
	}
}

// agentMenu is the label of the per-agent dropdown. Named once because the
// topbar carries action groups in the same bar and no name may appear in both:
// "Agent" belongs to the group that creates and deletes the agent record, so
// what the agent is DOING is "Manage". It is also the runtime's default menu
// name, which means a nav item that forgets its Menu lands here rather than
// opening a fourth button — TestNavMenusAreNamedAndScoped still catches it.
const agentMenu = "Manage"

// The nav dropdowns and the toolbar action groups render into the SAME topbar,
// so a name used by both produces two buttons reading alike with different
// contents behind them. Nothing in core/ui can catch that — the two come from
// different fields — so the app that declares both has to hold the line.
func TestNoNavMenuSharesAToolbarGroupName(t *testing.T) {
	page := readFile(t, "page_chat.go")
	menus := map[string]bool{}
	for _, e := range navEntries(t, page) {
		if e.menu == "" {
			// An item that names no menu lands in the runtime's default one,
			// which collides just as loudly as an explicit name would.
			menus["Manage"] = true
			continue
		}
		menus[e.menu] = true
	}
	for _, group := range toolbarGroups(page) {
		if menus[group] {
			t.Errorf("the toolbar group %q and a nav menu of the same name both render as %q ▾ in one bar", group, group)
		}
	}
}

// toolbarGroups collects the distinct Group values on the page's ToolbarActions.
// Bounded to that block: nav items carry a Group of their own (a heading INSIDE
// a menu), and counting one of those as a toolbar button would compare two
// things that never share a row.
func toolbarGroups(page string) []string {
	var out []string
	seen := map[string]bool{}
	const key = `{Group: "`
	if i := strings.Index(page, "Actions: pruneToolbar("); i >= 0 {
		page = page[i:]
	}
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
		if g := rest[:k]; !seen[g] {
			seen[g] = true
			out = append(out, g)
		}
	}
	return out
}

// navEntry is one parsed nav declaration.
type navEntry struct {
	label string
	menu  string
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
		// Only what actually renders IN a dropdown. A Topbar or Pinned item
		// (Permissions) lives in the nav slice but is lifted out of the menus,
		// so it names none and must not be judged as if it did — the same
		// condition the runtime uses to decide which items build menus.
		if strings.Contains(part, "Topbar: true") || strings.Contains(part, "Pinned: true") {
			continue
		}
		menu := ""
		if i := strings.Index(part, `Menu: "`); i >= 0 {
			rest := part[i+len(`Menu: "`):]
			if j := strings.Index(rest, `"`); j >= 0 {
				menu = rest[:j]
			}
		}
		out = append(out, navEntry{label: label, menu: menu, body: part})
	}
	if len(out) == 0 {
		t.Fatal("parsed no nav entries — the declaration shape changed")
	}
	return out
}

// entryIn finds the entry with one label in one menu.
func entryIn(t *testing.T, entries []navEntry, label, menu string) navEntry {
	t.Helper()
	for _, e := range entries {
		if e.label == label && e.menu == menu {
			return e
		}
	}
	t.Fatalf("no %q entry in %q", label, menu)
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
		fmt.Fprintf(&b, "  %2d  %-20s %s\n", i, e.menu, e.label)
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

// In a fleet view the agent leads. The list is drawn from every agent the
// owner has, so the first question about any row in it is whose — and the
// renderer bolds whichever field marshals first, which makes field ORDER the
// whole of the answer.
func TestFleetRowsLeadWithTheAgent(t *testing.T) {
	// A run in the fleet feed: agent first, the schedule that fired it second.
	raw, err := json.Marshal(consoleRunRow{
		Agent: "Support bot", Task: "nightly digest", Status: "ok", When: "Jan 2 15:04", ID: "r1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), `{"agent":"Support bot","task":"nightly digest"`) {
		t.Errorf("a fleet run must lead with the agent, then what fired it:\n%s", raw)
	}

	// A guardrail block across the fleet: agent first, then the rule.
	fleet, _ := json.Marshal(consoleGuardrailRow{
		Agent: "Support bot", Name: "never email customers", Where: "pre-send", At: "t", ID: "a1",
	})
	if !strings.HasPrefix(string(fleet), `{"agent":"Support bot","name":"never email customers"`) {
		t.Errorf("a fleet guardrail row must lead with the agent:\n%s", fleet)
	}

	// Scoped to one agent, the name is absent rather than demoted: printing it
	// on every row of that agent's own pane says nothing, and an empty leading
	// field would leave the card with no bold title at all.
	scoped, _ := json.Marshal(consoleGuardrailRow{
		Name: "never email customers", Where: "pre-send", At: "t", ID: "a1",
	})
	if strings.Contains(string(scoped), `"agent"`) {
		t.Errorf("a scoped guardrail row should omit the agent entirely:\n%s", scoped)
	}
	if !strings.HasPrefix(string(scoped), `{"name":`) {
		t.Errorf("with no agent, the rule must lead:\n%s", scoped)
	}
}

// joinDetail builds the line that follows the headline. A row missing one of
// its parts must not render a stray separator.
func TestJoinDetailSkipsEmptyParts(t *testing.T) {
	if got := joinDetail("nightly digest", "Jan 2 15:04"); got != "nightly digest · Jan 2 15:04" {
		t.Errorf("joinDetail = %q", got)
	}
	if got := joinDetail("", "Jan 2 15:04"); got != "Jan 2 15:04" {
		t.Errorf("a missing first part should leave no separator, got %q", got)
	}
	if got := joinDetail("  ", ""); got != "" {
		t.Errorf("all-empty should render nothing, got %q", got)
	}
}

// consoleRunTask answers "what fired this", and only when that is something
// other than the agent itself — otherwise the fleet card would print the same
// name twice, once bold and once muted.
func TestConsoleRunTaskOmitsTheAgentsOwnName(t *testing.T) {
	if got := consoleRunTask(RunRecord{Agent: "Support bot", Task: "nightly digest"}); got != "nightly digest" {
		t.Errorf("task = %q", got)
	}
	if got := consoleRunTask(RunRecord{Agent: "Support bot", Task: "Support bot"}); got != "" {
		t.Errorf("a task named after its own agent should not repeat it, got %q", got)
	}
	if got := consoleRunTask(RunRecord{Agent: "Support bot"}); got != "" {
		t.Errorf("no task means no second line, got %q", got)
	}
}
