package orchestrate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func parkedTestSetup(t *testing.T) Database {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	prevBase, prevRoot := orchestrateBaseDB, RootDB
	orchestrateBaseDB, RootDB = root, root
	t.Cleanup(func() { orchestrateBaseDB, RootDB = prevBase, prevRoot })
	PreInitScheduler()
	return root
}

// backdateParked pretends a schedule was first seen parked long ago.
func backdateParked(kind, owner, id string, ago time.Duration) {
	RootDB.Set(parkedSinceTable, parkedKey(kind, owner, id), time.Now().Add(-ago))
}

// A schedule left parked because its agent was deleted is removed after the
// expiry; one parked for a stalled objective is not (nothing is missing), and
// one parked recently stays, showing when it will go.
func TestParkedSchedulesExpire(t *testing.T) {
	parkedTestSetup(t)
	for _, n := range []string{"old", "recent", "stalled", "running"} {
		SaveStandingAgent(RootDB, StandingAgent{Owner: "u", Name: n, AgentID: "gone", Mission: n, IntervalSeconds: 3600})
	}
	MarkStandingAgentBroken(RootDB, "u", "old", "its target agent was deleted")
	MarkStandingAgentBroken(RootDB, "u", "recent", "its target agent was deleted")
	MarkStandingAgentStalled(RootDB, "u", "stalled", "the goal was not met")
	backdateParked(schedKindStanding, "u", "old", parkedExpiry+time.Hour)
	backdateParked(schedKindStanding, "u", "stalled", parkedExpiry+time.Hour)
	backdateParked(schedKindStanding, "u", "running", parkedExpiry+time.Hour) // not parked any more

	if n := sweepParkedSchedules([]string{"u"}); n != 1 {
		t.Fatalf("swept %d, want 1", n)
	}
	if _, ok := GetStandingAgent(RootDB, "u", "old"); ok {
		t.Error("a schedule parked past the expiry was kept")
	}
	for _, n := range []string{"recent", "stalled", "running"} {
		if _, ok := GetStandingAgent(RootDB, "u", n); !ok {
			t.Errorf("%s was deleted", n)
		}
	}
	var at time.Time
	if RootDB.Get(parkedSinceTable, parkedKey(schedKindStanding, "u", "running"), &at) {
		t.Error("a schedule running again kept its parked time")
	}
	if note := parkedDeadlineNote(schedKindStanding, "u", "recent"); !strings.Contains(note, "unless relinked") {
		t.Errorf("deadline note: %q", note)
	}
}

// A parked recurring task expires the same way.
func TestParkedRecurringTaskExpires(t *testing.T) {
	parkedTestSetup(t)
	if _, err := ScheduleOrchestrateUpdate(RecurringSpec{SessionID: "s1", AgentID: "gone", Username: "u",
		Prompt: "gather", Name: "gather", Pattern: RecurringFixed, IntervalSeconds: 7200}); err != nil {
		t.Fatal(err)
	}
	rt := listAgentRecurringTasks("u", "")[0]
	UnscheduleTask(rt.TaskID)
	parkRecurringBroken(rt.Payload, "its agent was deleted")
	backdateParked(schedKindRecurring, "u", recurringTaskUID(rt.Payload), parkedExpiry+time.Hour)
	if n := sweepParkedSchedules([]string{"u"}); n != 1 || len(listAgentRecurringTasks("u", "")) != 0 {
		t.Fatalf("a parked recurring task past the expiry was kept (swept %d)", n)
	}
}

// Builder's relink: a deleted agent's parked schedule moves to the replacement
// and runs again, keeping what it had; a schedule that is not parked is refused.
func TestScheduleRelinkMovesAParkedSchedule(t *testing.T) {
	root := parkedTestSetup(t)
	if _, err := saveAgent(UserDB(root, "u"), AgentRecord{ID: "new-id", Owner: "u", Name: "Replacement", OrchestratorPrompt: "x"}); err != nil {
		t.Fatal(err)
	}
	SaveStandingAgent(RootDB, StandingAgent{Owner: "u", Name: "poster", AgentID: "gone", Mission: "post daily", IntervalSeconds: 3600, MaxIntervalSeconds: 7200})
	MarkStandingAgentBroken(RootDB, "u", "poster", "its target agent was deleted")
	SaveStandingAgent(RootDB, StandingAgent{Owner: "u", Name: "healthy", AgentID: "new-id", Mission: "x", IntervalSeconds: 3600})

	out, err := scheduleRelink("u", "poster", "Replacement")
	if err != nil {
		t.Fatalf("relink: %v", err)
	}
	sa, _ := GetStandingAgent(RootDB, "u", "poster")
	if sa.AgentID != "new-id" || sa.Broken || sa.Paused || sa.SchedulerID == "" {
		t.Fatalf("not relinked and running: %+v (%s)", sa, out)
	}
	if sa.Mission != "post daily" || sa.MaxIntervalSeconds != 7200 {
		t.Fatalf("relink lost what the schedule had: %+v", sa)
	}
	if _, err := scheduleRelink("u", "healthy", "Replacement"); err == nil {
		t.Error("a schedule that is not parked was relinked")
	}
	if _, err := scheduleRelink("u", "poster", "nobody"); err == nil {
		t.Error("an unknown agent was accepted")
	}
}

// Deleting an agent with its schedules takes those of its sub-agents too, and
// only theirs.
func TestDeleteAgentSchedules(t *testing.T) {
	root := parkedTestSetup(t)
	udb := UserDB(root, "u")
	saveAgent(udb, AgentRecord{ID: "parent", Owner: "u", Name: "Parent", OrchestratorPrompt: "x"})
	saveAgent(udb, AgentRecord{ID: "child", Owner: "u", Name: "Child", OwnedBy: "parent", OrchestratorPrompt: "x"})
	SaveStandingAgent(RootDB, StandingAgent{Owner: "u", Name: "p-task", AgentID: "parent", Mission: "x", IntervalSeconds: 3600})
	SaveStandingAgent(RootDB, StandingAgent{Owner: "u", Name: "c-task", AgentID: "child", Mission: "x", IntervalSeconds: 3600})
	SaveStandingAgent(RootDB, StandingAgent{Owner: "u", Name: "other", AgentID: "someone", Mission: "x", IntervalSeconds: 3600})

	family := agentFamilyIDs(udb, "u", "parent")
	if names := agentScheduleNames("u", family); len(names) != 2 {
		t.Fatalf("schedules of the agent and its sub-agent: %v", names)
	}
	if n := deleteAgentSchedules("u", family); n != 2 {
		t.Fatalf("deleted %d, want 2", n)
	}
	if _, ok := GetStandingAgent(RootDB, "u", "other"); !ok {
		t.Error("another agent's schedule was deleted")
	}
}

// The delete confirmation, driven: it names the schedules, keeps them by
// default, and sends schedules=delete only when that is chosen.
func TestAgentDeleteAskOffersToKeepSchedules(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	js := strings.TrimPrefix(agentDeleteAskJS, "<script>")
	js = js[:strings.LastIndex(js, "</")]
	harness := `
function fail(m) { console.log('FAIL ' + m); process.exit(1); }
function node(tag) {
  return {tagName: tag, style: {}, dataset: {}, children: [], textContent: '', checked: false, value: '',
    appendChild: function(c) { this.children.push(c); return c; }, addEventListener: function(ev, fn) { this['on' + ev] = fn; }};
}
global.document = {readyState: 'complete', createElement: node, createTextNode: function(t) { return {textContent: t}; }};
var opened = null, fetched = [];
global.window = {
  uiRegisterClientAction: function() {},
  uiOpenModal: function(o) { opened = o; },
  uiAlert: function(m) { fail('alert: ' + m); },
  uiGoTo: function() {},
};
global.fetch = function(url, opts) {
  fetched.push((opts && opts.method || 'GET') + ' ' + url);
  if (!opts || !opts.method) return Promise.resolve({ok: true, json: function() { return Promise.resolve({names: ['poster', 'gather'], keep_days: 30}); }});
  return Promise.resolve({ok: true, status: 204});
};
` + js + `
var done = false;
window.orchDeleteAgentAsk('../', 'a1', 'Wren', function() { done = true; });
setTimeout(function() {
  if (!opened) fail('no confirmation opened');
  var body = node('div');
  opened.mount(body);
  var text = body.children.map(function(c) { return c.textContent || (c.children || []).map(function(x) { return x.textContent; }).join(''); }).join(' | ');
  if (text.indexOf('poster, gather') < 0) fail('the schedules are not named: ' + text);
  if (body.dataset.schedules !== 'keep') fail('keeping them is not the default');
  var m = {body: body, close: function() {}};
  body.dataset.schedules = 'delete';
  opened.actions[1].onClick(m);
  setTimeout(function() {
    var last = fetched[fetched.length - 1];
    if (last !== 'DELETE ../api/agents/a1?schedules=delete') fail('delete sent: ' + last);
    if (!done) fail('the page did not move on after the delete');
    console.log('OK');
  }, 5);
}, 5);
`
	tmp := filepath.Join(t.TempDir(), "ask.js")
	if err := os.WriteFile(tmp, []byte(harness), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", tmp).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("%v\n%s", err, out)
	}
}
