package orchestrate

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

func backdateDone(kind, owner, id string, ago time.Duration) {
	RootDB.Set(doneSinceTable, parkedKey(kind, owner, id), time.Now().Add(-ago))
}

// What counts as finished: a goal met, an end reached, a fire allowance spent.
// Paused by a person, gone quiet, or broken is not finished.
func TestWhatCountsAsDone(t *testing.T) {
	for _, c := range []struct {
		sa   StandingAgent
		want bool
	}{
		{StandingAgent{Paused: true, StopCause: StoppedByMet}, true},
		{StandingAgent{Paused: true, StopCause: StoppedByEnd}, true},
		{StandingAgent{Paused: true, StopCause: StoppedByOwner}, false},
		{StandingAgent{Paused: true}, false},
		{StandingAgent{StopCause: StoppedByMet}, false},
		{StandingAgent{Paused: true, StopCause: StoppedByMet, Broken: true}, false},
	} {
		if got := standingDone(c.sa); got != c.want {
			t.Errorf("standingDone(%+v) = %v", c.sa, got)
		}
	}
	for _, c := range []struct {
		m    EventMonitor
		want bool
	}{
		{EventMonitor{Paused: true, StopReason: MonitorStopMet}, true},
		{EventMonitor{Paused: true, StopReason: MonitorStopFinished}, true},
		{EventMonitor{Paused: true, StopReason: MonitorStopOwner}, false},
		{EventMonitor{Paused: true, StopReason: MonitorStopIdle}, false},
		{EventMonitor{StopReason: MonitorStopMet}, false},
		{EventMonitor{Paused: true, StopReason: MonitorStopMet, Broken: true}, false},
	} {
		if got := monitorDone(c.m); got != c.want {
			t.Errorf("monitorDone(%+v) = %v", c.m, got)
		}
	}
}

// A finished schedule is removed 30 days after it finished, unless kept; one
// that finished recently stays and says when it goes; one running again
// forgets when it finished.
func TestFinishedSchedulesAreCleanedUpUnlessKept(t *testing.T) {
	parkedTestSetup(t)
	for _, n := range []string{"old", "kept", "recent", "paused"} {
		SaveEventMonitor(RootDB, EventMonitor{Owner: "u", Name: n, Kind: EventKindWebhook, Paused: true, StopReason: MonitorStopMet})
	}
	paused, _ := GetEventMonitor(RootDB, "u", "paused")
	paused.StopReason = MonitorStopOwner
	SaveEventMonitor(RootDB, paused)
	SaveStandingAgent(RootDB, StandingAgent{Owner: "u", Name: "agent-old", AgentID: "a", Mission: "m", IntervalSeconds: 3600, Paused: true, StopCause: StoppedByEnd})
	for _, n := range []string{"old", "kept", "paused"} {
		backdateDone(schedKindMonitor, "u", n, doneExpiry+time.Hour)
	}
	backdateDone(schedKindStanding, "u", "agent-old", doneExpiry+time.Hour)
	setDoneKept(schedKindMonitor, "u", "kept", true)

	if n := sweepDoneSchedules([]string{"u"}); n != 2 {
		t.Fatalf("swept %d, want 2", n)
	}
	if _, ok := GetEventMonitor(RootDB, "u", "old"); ok {
		t.Error("a monitor finished past the expiry was not removed")
	}
	if _, ok := GetStandingAgent(RootDB, "u", "agent-old"); ok {
		t.Error("a scheduled agent finished past the expiry was not removed")
	}
	for _, n := range []string{"kept", "recent", "paused"} {
		if _, ok := GetEventMonitor(RootDB, "u", n); !ok {
			t.Errorf("%s was removed", n)
		}
	}
	var at time.Time
	if RootDB.Get(doneSinceTable, parkedKey(schedKindMonitor, "u", "paused"), &at) {
		t.Error("a schedule a person paused kept a finished time")
	}
	if note := doneStateNote(schedKindMonitor, "u", "recent"); !strings.Contains(note, "unless kept") {
		t.Errorf("a recent one's note: %q", note)
	}
	if note := doneStateNote(schedKindMonitor, "u", "kept"); note != " (kept)" {
		t.Errorf("a kept one's note: %q", note)
	}
}

// On the Scheduler a finished schedule is filed under Done, with Keep; Keep
// holds it past the cleanup and offers its undo; a running one cannot be kept.
func TestTheSchedulerFilesFinishedOnesUnderDone(t *testing.T) {
	parkedTestSetup(t)
	SaveEventMonitor(RootDB, EventMonitor{Owner: "u", Name: "merged", Kind: EventKindWebhook, Paused: true, StopReason: MonitorStopMet})
	SaveEventMonitor(RootDB, EventMonitor{Owner: "u", Name: "watching", Kind: EventKindWebhook})
	rowsFor := func() map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, r := range consoleMonitorRows("u", "") {
			out[r.Name] = schedulerRow(r, schedSectionMonitors, schedKindMonitor)
		}
		return out
	}
	rows := rowsFor()
	if rows["merged"]["_section"] != schedSectionDone || rows["merged"]["_keep"] != true {
		t.Fatalf("a finished monitor's row: %+v", rows["merged"])
	}
	if !strings.Contains(rows["merged"]["state"].(string), "unless kept") {
		t.Errorf("the row does not say when it goes: %v", rows["merged"]["state"])
	}
	if rows["watching"]["_section"] != schedSectionMonitors || rows["watching"]["_keep"] == true {
		t.Errorf("a running monitor was filed as done: %+v", rows["watching"])
	}

	T := &OrchestrateApp{}
	keep := func(path, id string) int {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", path+"?id="+id, nil)
		_, err := T.handleConsoleScheduleKeep(&jsonapi.Call{W: w, R: r, User: "u"}, struct{}{})
		if err != nil {
			return http.StatusNotFound
		}
		return http.StatusNoContent
	}
	if code := keep("/api/console/scheduler/keep", "monitor:watching"); code != http.StatusNotFound {
		t.Error("a running monitor was kept")
	}
	if code := keep("/api/console/scheduler/keep", "monitor:merged"); code != http.StatusNoContent || !doneKept(schedKindMonitor, "u", "merged") {
		t.Fatal("Keep did not keep it")
	}
	if rows := rowsFor(); rows["merged"]["_unkeep"] != true || !strings.Contains(rows["merged"]["state"].(string), "(kept)") {
		t.Errorf("a kept row: %+v", rows["merged"])
	}
	keep("/api/console/scheduler/unkeep", "monitor:merged")
	if doneKept(schedKindMonitor, "u", "merged") {
		t.Error("Don't keep did not let it go")
	}
}
