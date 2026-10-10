package orchestrate

import (
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/oddjob/core/appagents"
	"github.com/cmcoffee/oddjob/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

// An app agent's settings page lives in its app: the budgets and reasoning,
// read and saved through the app, Reset to default, and nothing else the
// save will take.
func TestAppAgentSettingsPage(t *testing.T) {
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: "app-test-settings", Name: "Settled", OwningApp: "Zz Test", Hidden: true, Prompt: "x",
	})
	// The same app's second agent, and one owned elsewhere that it runs.
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: "app-test-sibling", Name: "Sibling", OwningApp: "Zz Test", Hidden: true, Prompt: "x",
	})
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: "app-test-borrowed", Name: "Borrowed", OwningApp: "Yy Other", Hidden: true, Prompt: "x",
	})
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: "app-test-unrelated", Name: "Unrelated", OwningApp: "Xx Else", Hidden: true, Prompt: "x",
	})
	T, udb, _ := newTestOrchestrate(t)
	agent, _ := loadAgent(udb, "app-test-settings")
	c := AppChat{Prefix: "chat/", Settings: true, Back: "/zz", Agents: []string{"app-test-borrowed"}}
	serve := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := asUser(httptest.NewRequest(method, "/"+path, strings.NewReader(body)), "alice")
		route, _, _ := strings.Cut(path, "?") // an app routes on the path alone
		if !T.ServeAppChat(rec, req, agent, c, route, "") {
			t.Fatalf("%s %s was not answered", method, path)
		}
		return rec
	}
	page := serve("GET", "chat/settings", "").Body.String()
	for _, want := range []string{"max_worker_rounds", "gap_check", "work_plan", "think_budget", "to default", "settings/reset", "/zz",
		"Zz Test agents", "Settled", "Sibling", "Borrowed", "belongs to Yy Other", "agent=app-test-sibling"} {
		if !strings.Contains(page, want) {
			t.Errorf("the settings page is missing %q", want)
		}
	}
	for _, not := range []string{"orchestrator_prompt", "\"triggers\"", "Delegation", "Intake form", "Unrelated"} {
		if strings.Contains(page, not) {
			t.Errorf("the settings page offers %q", not)
		}
	}
	if rec := serve("PATCH", "chat/settings/data", `{"max_worker_rounds": 40}`); rec.Code != 200 {
		t.Fatalf("save = %d %s", rec.Code, rec.Body.String())
	}
	// The form reads the setting back, and nothing outside the page.
	if data := serve("GET", "chat/settings/data", "").Body.String(); strings.Contains(data, "orchestrator_prompt") || !strings.Contains(data, `"max_worker_rounds":40`) {
		t.Fatalf("data = %s", data)
	}
	if got, _ := loadAgent(udb, "app-test-settings"); got.MaxWorkerRounds != 40 {
		t.Fatalf("rounds after save = %d", got.MaxWorkerRounds)
	}
	if rec := serve("PATCH", "chat/settings/data", `{"orchestrator_prompt": "be someone else"}`); rec.Code != 400 {
		t.Fatalf("a field outside the page was taken: %d", rec.Code)
	}
	if rec := serve("POST", "chat/settings/reset", ""); rec.Code != 200 {
		t.Fatalf("reset = %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := loadAgent(udb, "app-test-settings"); got.MaxWorkerRounds != 0 {
		t.Fatalf("rounds after reset = %d", got.MaxWorkerRounds)
	}
	if rec := serve("POST", "chat/settings/reset", ""); rec.Code != 400 {
		t.Fatal("a reset at defaults did not say so")
	}
	// Each agent on the page saves on its own; one the page does not cover
	// is not reachable through it.
	if rec := serve("PATCH", "chat/settings/data?agent=app-test-borrowed", `{"max_plan_steps": 3}`); rec.Code != 200 {
		t.Fatalf("save borrowed = %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := loadAgent(udb, "app-test-borrowed"); got.MaxPlanSteps != 3 {
		t.Fatalf("borrowed plan steps = %d", got.MaxPlanSteps)
	}
	if got, _ := loadAgent(udb, "app-test-settings"); got.MaxPlanSteps == 3 {
		t.Fatal("a save for one agent landed on another")
	}
	if rec := serve("PATCH", "chat/settings/data?agent=app-test-unrelated", `{"max_plan_steps": 3}`); rec.Code != 404 {
		t.Fatalf("an agent outside the page was saved: %d", rec.Code)
	}
	// Not an app agent, or an app that did not ask for it: no settings here.
	if rec := httptest.NewRecorder(); T.ServeAppChat(rec, asUser(httptest.NewRequest("GET", "/chat/settings", nil), "alice"), AgentRecord{ID: "mine"}, c, "chat/settings", "") && rec.Code != 404 {
		t.Fatalf("an ordinary agent got a settings page: %d", rec.Code)
	}
	if rec := httptest.NewRecorder(); T.ServeAppChat(rec, asUser(httptest.NewRequest("GET", "/chat/settings", nil), "alice"), agent, AppChat{Prefix: "chat/"}, "chat/settings", "") && rec.Code != 404 {
		t.Fatalf("settings served without Settings: %d", rec.Code)
	}
	// The chat's toolbar links to it, relative to the app.
	p := c.Panel(ui.AgentLoopPanel{})
	if len(p.Actions) != 1 || p.Actions[0].URL != "chat/settings" || p.Actions[0].Method != "redirect" {
		t.Fatalf("actions = %+v", p.Actions)
	}
	if p := (AppChat{Prefix: "chat/"}).Panel(ui.AgentLoopPanel{}); len(p.Actions) != 0 {
		t.Fatalf("no Settings, yet actions = %+v", p.Actions)
	}
	// Orchestrate's editor URL for it says where it is set up, and offers
	// none of the agent surfaces.
	rec := httptest.NewRecorder()
	T.renderAgentEditor(rec, httptest.NewRequest("GET", "/agent/app-test-settings", nil), "alice", udb, "app-test-settings")
	if body := rec.Body.String(); !strings.Contains(body, "Set up in Zz Test") || strings.Contains(body, "max_worker_rounds") {
		t.Fatal("orchestrate's editor still edits an app agent")
	}
}

// An app agent cannot be deleted by any path that goes through the agent
// delete (the API, Builder's tool): that path reverts a seed AND drops its
// memory. The person's copy stays until Reset is used.
func TestAppAgentsCannotBeDeleted(t *testing.T) {
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: "app-test-kept", Name: "Kept", OwningApp: "Zz Test", Hidden: true, Prompt: "x",
	})
	udb := UserDB(&DBase{Store: kvlite.MemStore()}, "u")
	rec, _ := loadAgent(udb, "app-test-kept")
	rec.MaxWorkerRounds, rec.Owner = 30, "u"
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatal(err)
	}
	err := deleteAgent(udb, "app-test-kept", "u")
	if err == nil || !strings.Contains(err.Error(), "cannot be deleted") || !strings.Contains(err.Error(), "Agent settings") {
		t.Fatalf("delete of an app agent = %v", err)
	}
	if back, _ := loadAgent(udb, "app-test-kept"); back.MaxWorkerRounds != 30 {
		t.Fatalf("the refused delete still changed the agent: rounds %d", back.MaxWorkerRounds)
	}
	if err := resetAppAgent(udb, "seed-builder"); err == nil {
		t.Fatal("reset reached an agent that is not an app agent")
	}
}

// Every picker labels an app agent the same way, apart from the person's own
// agents, and puts the app groups after everything else.
func TestAppAgentsGroupApartInPickers(t *testing.T) {
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: "app-test-shown", Name: "Shown", OwningApp: "Zz Test", Prompt: "x",
	})
	if g := agentGroup("app-test-shown", "Your agents"); g != "App agents: Zz Test" {
		t.Fatalf("app agent group = %q", g)
	}
	if g := agentGroup("my-own-agent", "Your agents"); g != "Your agents" {
		t.Fatalf("own agent group = %q", g)
	}
	groups := []string{"App agents: Zz Test", "Your agents", "App agents: Aa Test", "", "Your agents"}
	sort.SliceStable(groups, func(i, j int) bool { return appGroupsLast(groups[i], groups[j]) })
	want := []string{"Your agents", "", "Your agents", "App agents: Aa Test", "App agents: Zz Test"}
	if strings.Join(groups, "|") != strings.Join(want, "|") {
		t.Fatalf("order = %q", groups)
	}
}

// Every app agent's tool results are scanned, whatever its stored switch
// says: the switch lived where app agents are not set up, and was off for
// all of them. Anyone else's agent still follows its own switch.
func TestAppAgentsScanToolResults(t *testing.T) {
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: "app-test-scanned", Name: "Scanned", OwningApp: "Zz Test", Hidden: true, Prompt: "x",
	})
	web := Tool{Name: "research", Caps: []Capability{CapNetwork}}
	if !toolResultPolicyFor(AgentRecord{ID: "app-test-scanned"}, web).scan {
		t.Fatal("an app agent's web results are not scanned")
	}
	if toolResultPolicyFor(AgentRecord{ID: "mine"}, web).scan {
		t.Fatal("an agent with scanning off was scanned")
	}
	if !toolResultPolicyFor(AgentRecord{ID: "mine", ScanToolResults: true}, web).scan {
		t.Fatal("an agent with scanning on was not scanned")
	}
	if !toolResultPolicyFor(AgentRecord{ID: "app-test-scanned"}, web).fence {
		t.Fatal("an app agent's web results are not fenced")
	}
}
