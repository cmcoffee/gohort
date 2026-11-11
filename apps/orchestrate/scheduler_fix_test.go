package orchestrate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// schedFixApp is the app schedFixRoutes made, for a signed-out request.
var schedFixApp *OrchestrateApp

// schedFixRoutes is the test app's routes, as user.
func schedFixRoutes(t *testing.T) func(user, method, path, body string) (int, string) {
	t.Helper()
	app, _, _ := newTestOrchestrate(t)
	schedFixApp = app
	pinRootDB(t)
	PreInitScheduler()
	// asUser always signs in as alice, so anyone else gets a session of
	// their own here: a test of who may reach a schedule has to send them.
	sessions := map[string]string{"alice": token}
	return func(user, method, path, body string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if _, ok := sessions[user]; !ok {
			AuthDB().Set(AuthTable, "user:"+user, AuthUser{Username: user})
			sessions[user] = AuthCreateSession(AuthDB(), user)
		}
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: sessions[user]})
		w := httptest.NewRecorder()
		routed(app)(w, r)
		return w.Code, strings.TrimSpace(w.Body.String())
	}
}

// A parked recurring task's Fix: the window names why it is in trouble, its
// form holds what the task does, and saving changes those words and nothing
// about when it runs. The task is found by its lasting uid, so a second save
// works though the first rescheduled it under a new task id.
func TestFixingARecurringTaskChangesItsWordsNotItsTiming(t *testing.T) {
	do := schedFixRoutes(t)
	if _, err := ScheduleOrchestrateUpdate(RecurringSpec{SessionID: "s1", AgentID: "gone", Username: "alice",
		Prompt: "gather the news", Name: "gather", Pattern: RecurringFixed, IntervalSeconds: 7200}); err != nil {
		t.Fatal(err)
	}
	// Found by its words: the scheduler is shared, and another test's task
	// may be listed first.
	mine := func() recurringTaskRow {
		for _, x := range listAgentRecurringTasks("alice", "") {
			if x.Payload.Prompt == "gather the news" {
				return x
			}
		}
		t.Fatal("the task is not scheduled")
		return recurringTaskRow{}
	}
	rt := mine()
	UnscheduleTask(rt.TaskID)
	parkRecurringBroken(rt.Payload, "its agent was deleted")
	rt = mine()
	every := func(p orchUpdatePayload) int { return max(p.IntervalSeconds, p.Timing.EverySeconds) }

	code, body := do("alice", "GET", "/api/console/scheduler/fix-panel?kind=recurring&id="+rt.TaskID, "")
	if code != 200 {
		t.Fatalf("panel: %d %s", code, body)
	}
	var panel struct {
		Title, Subtitle string
		Panel           struct {
			URL  string `json:"url"`
			Work struct {
				Source  string `json:"source"`
				PostURL string `json:"post_url"`
			} `json:"work"`
		} `json:"panel"`
	}
	if err := json.Unmarshal([]byte(body), &panel); err != nil {
		t.Fatalf("panel json: %v\n%s", err, body)
	}
	uid := recurringTaskUID(rt.Payload)
	if !strings.Contains(panel.Subtitle, "its agent was deleted") || !strings.Contains(panel.Panel.URL, "id="+uid) || !strings.Contains(panel.Panel.Work.Source, "id="+uid) {
		t.Fatalf("the window should name the trouble and the task by its uid: %s", body)
	}
	if code, got := do("alice", "GET", "/"+panel.Panel.Work.Source, ""); code != 200 || !strings.Contains(got, `"prompt":"gather the news"`) || strings.Contains(got, "timing") {
		t.Fatalf("form: %d %s", code, got)
	}
	for i, words := range []string{"gather the news from two sources", "gather the news from three sources"} {
		if code, got := do("alice", "POST", "/"+panel.Panel.Work.PostURL, `{"name":"gather","prompt":"`+words+`"}`); code >= 300 {
			t.Fatalf("save %d: %d %s", i, code, got)
		}
		// The same interval, though a save writes it the way the editor
		// does (as timing). A save also puts a parked task back on its
		// schedule, as the recurring update always has; the brief says so.
		var got *orchUpdatePayload
		for _, x := range listAgentRecurringTasks("alice", "") {
			if recurringTaskUID(x.Payload) == uid {
				p := x.Payload
				got = &p
			}
		}
		if got == nil || got.Prompt != words || every(*got) != 7200 || got.Broken {
			t.Fatalf("save %d: %+v", i, got)
		}
	}
	if code, _ := do("bob", "GET", "/api/console/scheduler/fix-panel?kind=recurring&id="+uid, ""); code != 404 {
		t.Errorf("another user opened alice's Fix: %d", code)
	}
	if code, _ := do("bob", "POST", "/"+panel.Panel.Work.PostURL, `{"prompt":"x"}`); code != 404 {
		t.Errorf("another user saved alice's task: %d", code)
	}
	if code, _ := do("bob", "POST", "/"+panel.Panel.URL, `{"message":"why"}`); code != 404 {
		t.Errorf("another user reached alice's assistant: %d", code)
	}
	for _, path := range []string{"/" + panel.Panel.URL, "/" + panel.Panel.Work.PostURL} {
		r := httptest.NewRequest("POST", path, strings.NewReader("{}"))
		w := httptest.NewRecorder()
		routed(schedFixApp)(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("signed out, POST %s answered %d, want 401", path, w.Code)
		}
	}
}

// A scheduled agent's Fix edits its mission and keeps its schedule; a
// monitor's sends only the fields its kind owns, so its update does not
// refuse the save.
func TestFixingAScheduledAgentAndAMonitor(t *testing.T) {
	do := schedFixRoutes(t)
	SaveStandingAgent(RootDB, StandingAgent{Owner: "alice", Name: "poster", AgentID: "a1", Mission: "post daily", IntervalSeconds: 3600})
	if code, got := do("alice", "POST", "/api/console/scheduler/fix-form?kind=standing&id=poster", `{"mission":"post daily, with a link"}`); code >= 300 {
		t.Fatalf("standing save: %d %s", code, got)
	}
	sa, _ := GetStandingAgent(RootDB, "alice", "poster")
	if sa.Mission != "post daily, with a link" || StandingScheduleLabel(sa) != "every hour" {
		t.Fatalf("standing after: %q %q", sa.Mission, StandingScheduleLabel(sa))
	}
	SaveEventMonitor(RootDB, EventMonitor{Owner: "alice", Name: "watch", Kind: EventKindPoll, IntervalSeconds: 600, URL: "https://example.test/x", Check: "is it up?", CheckAgent: "a1"})
	if code, got := do("alice", "GET", "/api/console/scheduler/fix-form?kind=monitor&id=watch", ""); code != 200 || !strings.Contains(got, `"check":"is it up?"`) {
		t.Fatalf("monitor form: %d %s", code, got)
	}
	if code, got := do("alice", "POST", "/api/console/scheduler/fix-form?kind=monitor&id=watch", `{"check":"is it up and quick?","url":"https://elsewhere.test"}`); code >= 300 {
		t.Fatalf("monitor save: %d %s", code, got)
	}
	m, _ := GetEventMonitor(RootDB, "alice", "watch")
	if m.Check != "is it up and quick?" || m.URL != "https://example.test/x" || m.IntervalSeconds != 600 {
		t.Fatalf("monitor after: check %q url %q every %d", m.Check, m.URL, m.IntervalSeconds)
	}
}

// What the assistant is told: what the schedule does, why it is in trouble,
// its last runs with their ids, and the two ways out. Its one tool reads a
// run of THIS schedule and refuses any other.
func TestTheFixAssistantKnowsTheScheduleAndReadsOnlyItsRuns(t *testing.T) {
	schedFixRoutes(t)
	SaveStandingAgent(RootDB, StandingAgent{Owner: "alice", Name: "poster", AgentID: "a1", Mission: "post daily", IntervalSeconds: 3600})
	MarkStandingAgentBroken(RootDB, "alice", "poster", "its target agent was deleted")
	run := RecordRun(RootDB, RunRecord{Owner: "alice", Agent: "poster", Status: RunFailed, Err: "fetch_url: 403 from the site", Raw: "tried twice"}.AboutStanding("poster"))
	other := RecordRun(RootDB, RunRecord{Owner: "alice", Agent: "elsewhere", Status: RunOK, Summary: "not this one"}.AboutStanding("elsewhere"))
	tg, ok := findSchedFix("alice", UserDB(RootDB, "alice"), schedKindStanding, "poster")
	if !ok {
		t.Fatal("not found")
	}
	about := schedFixAbout("alice", tg, "")
	for _, want := range []string{"post daily", "its target agent was deleted", "403 from the site", "run " + run.ID, "Relink on the row", "never propose words that hide a failure", "stays parked after a save"} {
		if !strings.Contains(strings.ToLower(about), strings.ToLower(want)) {
			t.Errorf("the brief is missing %q:\n%s", want, about)
		}
	}
	tool := schedFixTools("alice", tg)[0]
	if out, err := tool.Handler(context.Background(), map[string]any{"run": run.ID}); err != nil || !strings.Contains(out, "403 from the site") {
		t.Errorf("reading its own run: %v %s", err, out)
	}
	if _, err := tool.Handler(context.Background(), map[string]any{"run": other.ID}); err == nil {
		t.Error("the tool read a run of another schedule")
	}
	if _, ok := findSchedFix("bob", UserDB(RootDB, "bob"), schedKindStanding, "poster"); ok {
		t.Error("another user found alice's schedule")
	}
}

// Fix is in an item's View, offered on every item but a link group, and
// not on the rows: one way in, where the runs are read.
func TestFixIsInTheView(t *testing.T) {
	page := readFile(t, "assets/web_assets.html")
	view := page[strings.Index(page, "window.uiRegisterClientAction('orchestrate_view_task'"):]
	view = view[:strings.Index(view, "window.uiRegisterClientAction('orchestrate_task_notes'")]
	if !strings.Contains(view, "if (kind !== 'link') {") || !strings.Contains(view, "openSchedFix(kind, id, row)") {
		t.Error("the View does not offer Fix")
	}
	if strings.Contains(readFile(t, "page_chat.go"), "orchestrate_fix_task") {
		t.Error("Fix is still a row action too")
	}
}

// An item whose card shows no trouble still opens a Fix, asked what keeps
// going wrong in its runs; a flagged one is asked why it keeps failing.
func TestAnUnflaggedItemIsAskedWhatRepeats(t *testing.T) {
	do := schedFixRoutes(t)
	SaveStandingAgent(RootDB, StandingAgent{Owner: "alice", Name: "quiet", AgentID: "a1", Mission: "post daily", IntervalSeconds: 3600})
	code, body := do("alice", "GET", "/api/console/scheduler/fix-panel?kind=standing&id=quiet", "")
	if code != 200 || !strings.Contains(body, schedFixAskRepeat) || !strings.Contains(body, "read for what keeps going wrong") {
		t.Fatalf("unflagged: %d %s", code, body)
	}
	MarkStandingAgentBroken(RootDB, "alice", "quiet", "its target agent was deleted")
	if code, body := do("alice", "GET", "/api/console/scheduler/fix-panel?kind=standing&id=quiet", ""); code != 200 || !strings.Contains(body, schedFixAsk) || !strings.Contains(body, "its target agent was deleted") {
		t.Fatalf("flagged: %d %s", code, body)
	}
}
