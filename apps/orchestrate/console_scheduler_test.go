package orchestrate

// The merged Scheduler view. Three record types in one list, each keeping its
// own fields and its own actions — so the thing most worth pinning is that a
// row never offers an action belonging to one of the other two, which would
// POST a recurring task's id at the standing-agent endpoint.

import (
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func flagsOf(row any, section, kind string) map[string]any {
	return schedulerRow(row, section, kind)
}

// A row carries only its own kind's flags. Every other action gates on a field
// that is not there, which reads as false in the panel.
func TestARowOffersOnlyItsOwnActions(t *testing.T) {
	standing := flagsOf(consoleAgentRow{Name: "Molt Poster", ID: "Molt Poster"}, schedSectionStanding, schedKindStanding)
	for _, want := range []string{"_run_standing", "_pause_standing", "_move_standing", "_del_standing"} {
		if standing[want] != true {
			t.Errorf("a live standing row must offer %s: %v", want, standing[want])
		}
	}
	for _, gone := range []string{"_run_recurring", "_del_recurring", "_test_monitor", "_pause_monitor", "_del_monitor"} {
		if _, present := standing[gone]; present {
			t.Errorf("a standing row must not carry %s — it would POST its id at another endpoint", gone)
		}
	}

	recurring := flagsOf(consoleRecurringRow{Name: "check feed", ID: "t-1"}, schedSectionRecurring, schedKindRecurring)
	if recurring["_run_recurring"] != true || recurring["_del_recurring"] != true {
		t.Errorf("recurring row missing its own actions: %v", recurring)
	}
	for _, gone := range []string{"_pause_standing", "_pause_monitor", "_move_standing"} {
		if _, present := recurring[gone]; present {
			t.Errorf("a recurring row must not carry %s (it has no pause concept at all)", gone)
		}
	}

	monitor := flagsOf(consoleMonitorRow{Name: "price watch", ID: "price watch", Schedulable: true}, schedSectionMonitors, schedKindMonitor)
	if monitor["_test_monitor"] != true || monitor["_pause_monitor"] != true {
		t.Errorf("monitor row missing its own actions: %v", monitor)
	}
	if _, present := monitor["_run_standing"]; present {
		t.Error("a monitor row must not carry the standing Run now")
	}
}

// The per-row conditions the single views expressed with only_if/hide_if pairs,
// now composed server-side because a merged row needs two facts joined and a
// row action gates on one field.
func TestTheConditionsSurviveTheMerge(t *testing.T) {
	paused := flagsOf(consoleAgentRow{Name: "a", Paused: true}, schedSectionStanding, schedKindStanding)
	if paused["_pause_standing"] != false || paused["_resume_standing"] != true {
		t.Errorf("a paused row offers Resume and not Pause: %v", paused)
	}
	broken := flagsOf(consoleAgentRow{Name: "a", Broken: true, Relinkable: true}, schedSectionStanding, schedKindStanding)
	if broken["_run_standing"] != false {
		t.Error("a parked row has no live agent to Run")
	}
	if broken["_relink_standing"] != true {
		t.Error("a row whose target is gone is the one Relink is for")
	}
	// A webhook has no check to run on demand; a broken one has no dependency
	// left to check.
	if got := flagsOf(consoleMonitorRow{Name: "m"}, schedSectionMonitors, schedKindMonitor)["_test_monitor"]; got != false {
		t.Errorf("a push-only monitor must not offer Test: %v", got)
	}
	if got := flagsOf(consoleMonitorRow{Name: "m", Schedulable: true, Broken: true}, schedSectionMonitors, schedKindMonitor)["_test_monitor"]; got != false {
		t.Errorf("a broken monitor must not offer Test: %v", got)
	}
	// A parked recurring task gets Resume, not Run — a parked payload
	// short-circuits at the top of the fire.
	parked := flagsOf(consoleRecurringRow{Name: "t", Broken: true}, schedSectionRecurring, schedKindRecurring)
	if parked["_run_recurring"] != false || parked["_resume_recurring"] != true {
		t.Errorf("a parked task offers Resume and not Run now: %v", parked)
	}
}

// The visible fields are whatever the single view produced. Converting through
// JSON rather than copying fields is what keeps the two from drifting — and
// the cards layout reads each row's OWN keys, so three shapes coexist.
func TestTheMergedRowKeepsTheSingleViewsFields(t *testing.T) {
	m := flagsOf(consoleAgentRow{Name: "Molt Poster", Mission: "post daily", Schedule: "daily 09:00", State: "active"},
		schedSectionStanding, schedKindStanding)
	for k, want := range map[string]string{"name": "Molt Poster", "mission": "post daily", "schedule": "daily 09:00"} {
		if m[k] != want {
			t.Errorf("%s = %v, want %q", k, m[k], want)
		}
	}
	if m["_section"] != schedSectionStanding {
		t.Errorf("section = %v", m["_section"])
	}
	// A monitor's own shape, in the same list.
	mon := flagsOf(consoleMonitorRow{Name: "price watch", Kind: "watch", Detail: "watch · every 300s"},
		schedSectionMonitors, schedKindMonitor)
	if mon["kind"] != "watch" || mon["detail"] != "watch · every 300s" {
		t.Errorf("monitor fields lost in the merge: %v", mon)
	}
	if _, leaked := mon["mission"]; leaked {
		t.Error("a monitor row gained a standing agent's field")
	}
}

// Six nav entries became two, and the two differ only in scope.
func TestTheNavHasOneSchedulerPerScope(t *testing.T) {
	src, err := os.ReadFile("page_chat.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if n := strings.Count(body, `Label: "Scheduler"`); n != 2 {
		t.Errorf("expected one Scheduler per menu, found %d", n)
	}
	for _, gone := range []string{`Label: "Enabled agents"`, `Label: "Event monitors"`, `Label: "Recurring tasks"`} {
		if strings.Contains(body, gone) {
			t.Errorf("%s is still its own nav entry — the point was to stop opening three lists", gone)
		}
	}
	// Both read the merged source; the Fleet one asks fleet-wide.
	if n := strings.Count(body, `Source: "api/console/scheduler"`); n != 2 {
		t.Errorf("both entries must read the merged view, found %d", n)
	}
	if !strings.Contains(body, `Menu: "Fleet", AllAgents: true, Scope: "fleet", Source: "api/console/scheduler"`) {
		t.Error("the Fleet entry must keep Scope fleet, or it answers for one agent and the fleet view is gone")
	}
}

// The three single-view endpoints stay. They are what the merged view is built
// from, and the rail modal still reads its own.
func TestTheSingleViewsStillExist(t *testing.T) {
	src, err := os.ReadFile("console.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, route := range []string{"/api/console/agents", "/api/console/monitors", "/api/console/recurring", "/api/console/scheduler"} {
		if !strings.Contains(body, `"`+route+`"`) {
			t.Errorf("route %s is not registered", route)
		}
	}
}

// The rail carried three things the nav page did not, and retiring it had to
// bring each across rather than drop it: editing a schedule's timing, creating
// one, and saying what a schedule RUNS.
func TestRetiringTheRailKeptWhatItCouldDo(t *testing.T) {
	page := readFile(t, "page_chat.go")
	// Editing. The modal that knows what a cron is lives in this app's JS, so
	// the row action invokes it as a client action.
	for _, action := range []string{"orchestrate_edit_standing", "orchestrate_edit_schedule", "orchestrate_edit_monitor"} {
		if !strings.Contains(page, action) {
			t.Errorf("no way to edit a schedule's timing any more: %s is unreachable", action)
		}
	}
	if n := strings.Count(page, `Label: "Edit schedule"`); n != 6 {
		t.Errorf("expected three kinds × two menus of Edit schedule, found %d", n)
	}
	// Creating. These were buttons at the top of the retired modal, and are
	// now nav entries of their own. Checked end to end — the nav names the
	// action and this app's JS registers it — because either half alone is a
	// button that does nothing.
	assets := readFile(t, "assets/web_assets.html")
	for _, create := range []string{"orchestrate_new_recurring", machineRunCreatorAction} {
		if !strings.Contains(assets, `uiRegisterClientAction('`+create+`'`) {
			t.Errorf("%s is not registered, so the nav entry opens nothing", create)
		}
	}
	for _, named := range []string{`ActionURL: "orchestrate_new_recurring"`, "ActionURL: machineRunCreatorAction"} {
		if !strings.Contains(page, named) {
			t.Errorf("no way to create a schedule from the UI any more: %s is not in the nav", named)
		}
	}
	if n := strings.Count(page, `ActionMethod: "client"`); n < 2 {
		t.Errorf("the create entries must invoke their form rather than POST: found %d", n)
	}
	// And the rail itself is gone, endpoint included.
	if strings.Contains(page, "SchedulesURL") {
		t.Error("the page still opts into the retired rail")
	}
	if strings.Contains(readFile(t, "console.go"), `"/api/schedules"`) {
		t.Error("the rail's endpoint is still routed; a route nothing points at is a surface nobody maintains")
	}
}

// Saying WHAT a schedule fires. A row reading "every 24h" is the same sentence
// whether it runs an agent, a pipeline or a machine.
func TestAScheduleSaysWhatItRuns(t *testing.T) {
	if got := standingRunsLabel("alice", StandingAgent{Owner: "alice"}); got != "" {
		t.Errorf("an agent running its own mission needs no extra label: %q", got)
	}
	// Ownership shows only when it is somebody else's — a schedule that breaks
	// because a shared thing changed is otherwise a mystery with no thread to
	// pull.
	if got := sharedBySuffix("alice", "alice"); got != "" {
		t.Errorf("your own thing is not shared with you: %q", got)
	}
	if got := sharedBySuffix("alice", "bob"); !strings.Contains(got, "bob") {
		t.Errorf("a shared target must name its owner: %q", got)
	}
	if got := sharedBySuffix("alice", ""); got != "" {
		t.Errorf("an unowned target names nobody: %q", got)
	}
}
