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
					t.Errorf("%s (%s) still draws a badge", it.Label, it.Scope)
				}
			}
		}
	}
	if n != 2 {
		t.Errorf("expected the agent and fleet Schedulers, found %d", n)
	}
	js := readFile(t, "../../core/ui/assets/runtime/30_agent_loop_panel.js")
	if !strings.Contains(js, "} else if (item.source && !item.no_badge) {") {
		t.Error("the runtime still makes a badge, and fetches its source, for an item with no_badge")
	}
}
