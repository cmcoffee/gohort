package admin

// What each hand-run maintenance pass did last time.
//
// The state that matters most is "never run", because it is the one every
// function starts in and the one a decision about scheduling turns on. It is
// also the easy one to render as a blank line, which reads as the feature being
// new rather than as the pass never having happened.

import (
	"os"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

func historyApp(t *testing.T) *AdminApp {
	t.Helper()
	return &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
}

// Driven, not read: a record that does not survive the round trip through the
// store is the whole failure, and no amount of reading the formatter catches it.
func TestARunIsRememberedAcrossTheRoundTrip(t *testing.T) {
	a := historyApp(t)
	if _, ok := a.lastMaintenanceRun("vector_repair"); ok {
		t.Fatal("an unrun pass reports a run")
	}
	a.recordMaintenanceRun("vector_repair", "craig", 12, 95*time.Second)

	run, ok := a.lastMaintenanceRun("vector_repair")
	if !ok {
		t.Fatal("the record did not survive the store")
	}
	if run.By != "craig" || run.Changed != 12 {
		t.Errorf("record = %+v", run)
	}
	if run.Seconds != 95 {
		t.Errorf("seconds = %d, want 95", run.Seconds)
	}
	line := maintenanceHistoryLine(run, ok)
	for _, want := range []string{"Last run", "by craig", "12 changed", "took 1m"} {
		if !strings.Contains(line, want) {
			t.Errorf("history line %q is missing %q", line, want)
		}
	}

	// Overwritten, not appended: what a person needs before pressing is the
	// last run, and a growing table for that is storage nobody reads.
	a.recordMaintenanceRun("vector_repair", "someone-else", 0, time.Second)
	run, _ = a.lastMaintenanceRun("vector_repair")
	if run.By != "someone-else" || run.Changed != 0 {
		t.Errorf("the second run did not replace the first: %+v", run)
	}
}

// The important sentence, said plainly.
func TestNeverRunSaysSo(t *testing.T) {
	if got := maintenanceHistoryLine(maintenanceRun{}, false); got != "Never run." {
		t.Errorf("history line for an unrun pass = %q", got)
	}
	// And a run that changed nothing still says so: "0 changed" means there
	// was nothing to do, which is a result. Omitting it makes a clean run look
	// like a run with no outcome.
	line := maintenanceHistoryLine(maintenanceRun{RanAt: time.Now(), Changed: 0}, true)
	if !strings.Contains(line, "0 changed") {
		t.Errorf("a zero-change run does not report its count: %q", line)
	}
}

// Coarse on purpose: the decision is "recently enough or not", and a figure to
// the minute invites reading precision into a record written once a month.
func TestHowLongAgoIsSaidInHumanUnits(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		when time.Time
		want string
	}{
		{now.Add(-10 * time.Second), "just now"},
		{now.Add(-20 * time.Minute), "20m ago"},
		{now.Add(-5 * time.Hour), "5h ago"},
		{now.Add(-9 * 24 * time.Hour), "9d ago"},
	} {
		if got := relativeWhen(c.when); got != c.want {
			t.Errorf("relativeWhen(%v) = %q, want %q", c.when, got, c.want)
		}
	}
	// Past a couple of months a day count stops being readable and becomes a
	// number to divide, so it becomes a date.
	if got := relativeWhen(now.Add(-200 * 24 * time.Hour)); strings.HasSuffix(got, "d ago") {
		t.Errorf("a very old run still reports in days: %q", got)
	}
}

// Every never-run pass is countable without opening each group, which is what
// makes "should we schedule this" answerable at a glance.
func TestNeverRunPassesAreCountable(t *testing.T) {
	a := historyApp(t)
	all := ListMaintenanceFuncs()
	if len(all) == 0 {
		t.Skip("no maintenance functions registered in this binary")
	}
	if got := len(a.maintenanceKeysNeverRun()); got != len(all) {
		t.Errorf("%d of %d passes report as never run on a fresh store", got, len(all))
	}
	a.recordMaintenanceRun(all[0].Key, "craig", 1, time.Second)
	if got := len(a.maintenanceKeysNeverRun()); got != len(all)-1 {
		t.Errorf("after one run, %d report never-run, want %d", got, len(all)-1)
	}
}

// The line only appears if the list is told which field carries it.
func TestTheMaintenanceListAsksForItsHistory(t *testing.T) {
	list := maintenanceList("Housekeeping", "none")
	if list.HistoryField == "" {
		t.Fatal("the maintenance list declares no history field, so the line never renders")
	}
	// And the endpoint has to emit that exact key, or the field names a
	// property no item carries and the line is silently always absent.
	api, err := os.ReadFile("api_maintenance.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(api), `"`+list.HistoryField+`":`) {
		t.Errorf("the list reads %q but the endpoint never emits it", list.HistoryField)
	}
	var _ ui.ActionList = list
}
