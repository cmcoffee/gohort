package orchestrate

// The Scheduler took about a second to open, several times over: the nav
// rebuilt both Schedulers to draw count badges whenever a session changed,
// and each build read the whole run ledger once or twice per standing agent.

import (
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// The index answers what each row asked the ledger for, from one read.
func TestTheSchedulerReadsTheRunLedgerOnce(t *testing.T) {
	db := pinRootDB(t)
	now := time.Now()
	subject := RunFilter{}.AboutStanding("nightly").Subject
	RecordRun(db, RunRecord{Owner: "u", Agent: "nightly", Status: RunFailed, Started: now.Add(-3 * time.Hour)}) // before subjects
	RecordRun(db, RunRecord{Owner: "u", Agent: "nightly", Subject: subject, Status: RunOK, Started: now.Add(-time.Hour)})
	RecordRun(db, RunRecord{Owner: "u", Agent: "other", Subject: RunFilter{}.AboutStanding("other").Subject, Status: RunFailed, Started: now})

	ix := newStandingRunIndex("u")
	if got := ix.latestStatus("nightly"); got != string(RunOK) {
		t.Errorf("latest status %q, want the newest of its own runs", got)
	}
	if !ix.hasLegacyRunsNamed("nightly") || ix.hasLegacyRunsNamed("other") {
		t.Error("the legacy check should find only the run recorded before subjects")
	}
	if ix.latestStatus("nobody") != "" {
		t.Error("an agent with no runs has no status")
	}
	// One read per request: a run recorded after the first question is not
	// read again for the next.
	RecordRun(db, RunRecord{Owner: "u", Agent: "nightly", Subject: subject, Status: RunFailed, Started: now.Add(time.Minute)})
	if ix.latestStatus("nightly") != string(RunOK) {
		t.Error("the index read the ledger a second time")
	}
	// And it agrees with asking the ledger directly.
	if direct := ListRuns(db, "u", RunFilter{Limit: 1}.AboutStanding("nightly")); len(direct) != 1 || newStandingRunIndex("u").latestStatus("nightly") != string(direct[0].Status) {
		t.Error("the index and the ledger disagree about the latest run")
	}
}

// Neither Scheduler draws a count badge, so a session change does not rebuild
// them; the runtime skips the fetch for an item that declares none.
func TestTheSchedulerIsNotRebuiltForABadge(t *testing.T) {
	n := 0
	for _, items := range [][]ui.OrchestratorNavItem{chatAgentNav(), chatFleetNav()} {
		for _, it := range items {
			if it.Source == "api/console/scheduler" {
				n++
				if !it.NoBadge {
					t.Errorf("%s (%s) still draws a badge from the whole Scheduler", it.Label, it.Scope)
				}
				// Its badge counts what needs attention, from the cheap list.
				if it.BadgeSource != "api/console/scheduler/attention" || it.BadgeField != "_attention" {
					t.Errorf("%s (%s) counts %q/%q, not what needs attention", it.Label, it.Scope, it.BadgeSource, it.BadgeField)
				}
			}
		}
	}
	if n != 2 {
		t.Errorf("expected the agent and fleet Schedulers, found %d", n)
	}
	js := readFile(t, "../../core/ui/assets/runtime/30_agent_loop_panel.js")
	if !strings.Contains(js, "} else if (item.badge_source || (item.source && !item.no_badge)) {") {
		t.Error("the runtime still makes a badge, and fetches its source, for an item with no_badge")
	}
	// Whatever the row's placement (a pinned row always has a badge element),
	// no_badge without a badge_source fetches nothing for a count.
	if !strings.Contains(js, "if (item.no_badge && !item.badge_source) return;") {
		t.Error("a pinned item with no_badge still fetches its source for a count")
	}
	// And a badge_source is what the count is fetched from.
	if !strings.Contains(js, "var bsrc = item.badge_source || item.source;") || !strings.Contains(js, "fetch(orchSourceURL(bsrc, item))") {
		t.Error("the badge is not counted from badge_source")
	}
}

// Each schedule's badge says how its latest run went, found by what that kind
// files its runs under: a standing agent by name, a recurring task by its
// agent and task name, a monitor by name. One read serves them all.
func TestEachScheduleIsBadgedWithItsLatestRun(t *testing.T) {
	db := pinRootDB(t)
	now := time.Now()
	RecordRun(db, RunRecord{Owner: "u", Agent: "Nightly", Status: RunFailed, Started: now.Add(-2 * time.Hour)}.AboutStanding("Nightly"))
	RecordRun(db, RunRecord{Owner: "u", Agent: "Nightly", Status: RunOK, Started: now.Add(-time.Hour)}.AboutStanding("Nightly"))
	RecordRun(db, RunRecord{Owner: "u", Agent: "Herald", Task: "Morning summary", Status: RunAttention, Started: now}.AboutAgent("a-herald"))
	// The same agent's other task ran since: the badge is the task's own.
	RecordRun(db, RunRecord{Owner: "u", Agent: "Herald", Task: "Evening summary", Status: RunOK, Started: now.Add(time.Minute)}.AboutAgent("a-herald"))
	RecordRun(db, RunRecord{Owner: "u", Agent: "Site up", Status: RunFailed, Started: now}.AboutMonitor("Site up"))

	rows := []map[string]any{
		{"_kind": schedKindStanding, "_id": "Nightly", "status": "ok"},
		{"_kind": schedKindRecurring, "_id": "t1", "_task": "Morning summary", "_agent": "a-herald"},
		{"_kind": schedKindMonitor, "_id": "Site up"},
		{"_kind": schedKindStanding, "_id": "Brand new"},
		{"_section": "a heading row"},
	}
	addLastRunBadges(rows, newStandingRunIndex("u"))
	for i, want := range []struct{ label, tone string }{{"OK", "ok"}, {"ATTENTION", "warn"}, {"FAILED", "bad"}, {"NOT RUN YET", ""}} {
		if rows[i]["_status"] != want.label || rows[i]["_status_tone"] != want.tone {
			t.Errorf("row %d: %v/%v, want %s/%s", i, rows[i]["_status"], rows[i]["_status_tone"], want.label, want.tone)
		}
	}
	if _, ok := rows[0]["status"]; ok {
		t.Error("the standing row says its status twice, in grey and in the badge")
	}
	if _, ok := rows[4]["_status"]; ok {
		t.Error("a row that is not a schedule was badged")
	}
}
