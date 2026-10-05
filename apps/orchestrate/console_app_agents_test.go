package orchestrate

import (
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Every app agent is listed, hidden ones included, under its app. One the
// user changed says what changed and offers Reset, which puts it back as the
// app registered it and refuses a second time.
func TestAppAgentsAreListedAndReset(t *testing.T) {
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: "app-test-listed", Name: "Listed", OwningApp: "Zz Test", Hidden: true, Prompt: "x",
	})
	udb := UserDB(&DBase{Store: kvlite.MemStore()}, "u")
	find := func() appAgentRow {
		for _, r := range appAgentRows(udb) {
			if r.ID == "app-test-listed" {
				return r
			}
		}
		t.Fatal("a hidden app agent is not listed")
		return appAgentRow{}
	}
	if r := find(); r.Customized || r.Section != "Zz Test" || r.Name != "Listed" {
		t.Fatalf("row = %+v", r)
	}
	rec, ok := loadAgent(udb, "app-test-listed")
	if !ok {
		t.Fatal("the app agent does not load")
	}
	rec.MaxWorkerRounds = 40
	rec.Owner = "u"
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatal(err)
	}
	if r := find(); !r.Customized || !strings.Contains(r.Changed, "max worker rounds") {
		t.Fatalf("customized row = %+v", r)
	}
	if err := resetAppAgent(udb, "app-test-listed"); err != nil {
		t.Fatal(err)
	}
	if back, _ := loadAgent(udb, "app-test-listed"); back.MaxWorkerRounds != 0 {
		t.Fatalf("after reset, max worker rounds = %d", back.MaxWorkerRounds)
	}
	if r := find(); r.Customized {
		t.Fatalf("after reset = %+v", r)
	}
	if err := resetAppAgent(udb, "app-test-listed"); err == nil {
		t.Fatal("a reset at defaults did not say so")
	}
	if err := resetAppAgent(udb, "seed-builder"); err == nil {
		t.Fatal("reset reached an agent that is not an app agent")
	}
}

// The editor's back arrow goes only to a path on this server, and an app's
// chat names the agent's editor with its own way back.
func TestAppAgentSettingsLink(t *testing.T) {
	for b, want := range map[string]bool{"/scribe": true, "//evil.example": false, "https://evil.example": false, "/\\evil": false, "": false} {
		if localBackPath(b) != want {
			t.Errorf("localBackPath(%q) = %v", b, !want)
		}
	}
	p := AppChat{Prefix: "chat/", AgentID: "app-guides-author", Back: "/scribe"}.Panel(ui.AgentLoopPanel{})
	if len(p.Actions) != 1 || p.Actions[0].Method != "redirect" || p.Actions[0].URL != "/orchestrate/agent/app-guides-author?back=%2Fscribe" {
		t.Fatalf("actions = %+v", p.Actions)
	}
	if p := (AppChat{Prefix: "chat/"}).Panel(ui.AgentLoopPanel{}); len(p.Actions) != 0 {
		t.Fatalf("no agent, yet actions = %+v", p.Actions)
	}
}

// An app agent cannot be deleted by any path that goes through the agent
// delete (the editor, the API, Builder's tool): that path reverts a seed AND
// drops its memory. The person's copy stays until Reset is used.
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
	if err == nil || !strings.Contains(err.Error(), "cannot be deleted") || !strings.Contains(err.Error(), "Reset to default") {
		t.Fatalf("delete of an app agent = %v", err)
	}
	if back, _ := loadAgent(udb, "app-test-kept"); back.MaxWorkerRounds != 30 {
		t.Fatalf("the refused delete still changed the agent: rounds %d", back.MaxWorkerRounds)
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
	opts, _, _ := agentPickerOptions([]AgentRecord{{ID: "app-test-shown", Name: "Shown"}, {ID: "mine", Name: "Mine"}})
	for _, o := range opts {
		if o.Value == "app-test-shown" && o.Group != "App agents: Zz Test" {
			t.Fatalf("chat picker group = %q", o.Group)
		}
		if o.Value == "mine" && strings.HasPrefix(o.Group, "App agents") {
			t.Fatalf("an own agent landed in an app group: %q", o.Group)
		}
	}
}

// An app agent's editor is the restricted view: budgets, reasoning and
// reset, and none of the surfaces that would let it run outside its app.
func TestAppAgentEditorIsRestricted(t *testing.T) {
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: "app-test-restricted", Name: "Restricted", OwningApp: "Zz Test", Hidden: true, Prompt: "x",
	})
	T := &OrchestrateApp{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}}}
	udb := UserDB(T.DB, "u")
	rec := httptest.NewRecorder()
	T.renderAgentEditor(rec, httptest.NewRequest("GET", "/agent/app-test-restricted?back=/zz", nil), "u", udb, "app-test-restricted")
	page := rec.Body.String()
	for _, want := range []string{"max_worker_rounds", "gap_check", "work_plan", "think_budget", "Reset to default", "app-agents/reset", "belongs to Zz Test"} {
		if !strings.Contains(page, want) {
			t.Errorf("the app-agent editor is missing %q", want)
		}
	}
	for _, not := range []string{"orchestrator_prompt", "\"triggers\"", "capture_prompt", "Intake form", "Delegation", "Where it shows up", "Picture library", "Delete agent"} {
		if strings.Contains(page, not) {
			t.Errorf("the app-agent editor offers %q", not)
		}
	}
	if !strings.Contains(page, "/zz") {
		t.Error("back does not return to the app")
	}
}
