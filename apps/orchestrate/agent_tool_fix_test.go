package orchestrate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/notices"
)

// A maintainer fixes one of the owner's HTTP tools as a copy only this agent
// uses: the copy keeps the owner's credential and response pipe whatever was
// sent, the original is switched off for this agent alone, and the owner is
// told. Scripts are not offered, and only a maintainer makes a fix. The owner
// keeps it (into the original) or discards it (back to the original).
func TestAMaintainerFixesABrokenToolAsACopyOnlyThisAgentUses(t *testing.T) {
	app, udb, owner := newTestOrchestrate(t)
	pinRootDB(t)
	adb := AuthDB()
	adb.Set(AuthTable, "user:mia", AuthUser{Username: "mia"})
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	orig := TempTool{Name: "jira_search", Mode: TempToolModeAPI, Description: "Search Jira.",
		CommandTemplate: "https://jira.example/rest/api/2/search?q={q}", Method: "GET",
		Credential: "jira", ResponsePipe: "jq .issues"}
	if err := AdminPersistTempTool(AuthDB(), owner, orig); err != nil {
		t.Skipf("no tool store here: %v", err)
	}
	if err := AdminPersistTempTool(AuthDB(), owner, TempTool{Name: "run_report", Mode: "shell", CommandTemplate: "report {x}"}); err != nil {
		t.Fatal(err)
	}
	if _, err := saveAgent(udb, AgentRecord{ID: "a11", Owner: owner, Name: "Tracker", OrchestratorPrompt: "p",
		AllowedUsers: []string{"bob"}, Maintainers: []string{"mia"}, AllowedTools: []string{"jira_search", "run_report"}}); err != nil {
		t.Fatal(err)
	}
	as := func(user, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(adb, user)})
		w := httptest.NewRecorder()
		routed(app)(w, r)
		return w
	}
	fix := func(user, tool, def string) int {
		b, _ := json.Marshal(map[string]string{"definition": def})
		return as(user, "POST", "/api/agents/a11/tool-fix?tool="+tool, string(b)).Code
	}

	w := as("mia", "GET", "/api/agents/a11/fixable-tools", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "jira_search") || strings.Contains(w.Body.String(), "run_report") {
		t.Fatalf("what a maintainer may fix: %d %s", w.Code, w.Body.String())
	}
	if w := as("mia", "GET", "/api/agents/a11/tool-fix?tool=jira_search", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "rest/api/2/search") {
		t.Fatalf("the fix form's definition: %d %s", w.Code, w.Body.String())
	}
	sent := `{"description":"Search Jira (v3).","command_template":"https://jira.example/rest/api/3/search?q={q}","method":"GET","credential":"someone_elses","response_pipe":"cat /etc/passwd","mode":"shell"}`
	if code := fix("bob", "jira_search", sent); code != http.StatusNotFound {
		t.Errorf("somebody who only uses the agent made a fix: %d", code)
	}
	if code := fix(owner, "jira_search", sent); code != http.StatusBadRequest {
		t.Errorf("the owner went through the fix door for their own tool: %d", code)
	}
	if code := fix("mia", "run_report", `{"command_template":"rm -rf /"}`); code != http.StatusNotFound {
		t.Errorf("a script was offered for fixing: %d", code)
	}
	if code := fix("mia", "jira_search", sent); code != http.StatusNoContent {
		t.Fatalf("the maintainer's fix: %d", code)
	}

	p, ok := UserToolByName(AuthDB(), owner, "jira_search_fix")
	if !ok {
		t.Fatal("no copy was made")
	}
	if p.Tool.CommandTemplate != "https://jira.example/rest/api/3/search?q={q}" || p.Tool.Description != "Search Jira (v3)." {
		t.Errorf("the copy does not carry the fix: %+v", p.Tool)
	}
	if p.Tool.Credential != "jira" || p.Tool.ResponsePipe != "jq .issues" || p.Tool.Mode != TempToolModeAPI {
		t.Errorf("the copy took what a fix may not change: credential %q pipe %q mode %q", p.Tool.Credential, p.Tool.ResponsePipe, p.Tool.Mode)
	}
	if !p.ScopedToAgent("a11") || len(p.ScopeAgents) != 1 {
		t.Errorf("the copy is not this agent's alone: %v", p.ScopeAgents)
	}
	if o, _ := UserToolByName(AuthDB(), owner, "jira_search"); o.Tool.CommandTemplate != orig.CommandTemplate {
		t.Errorf("the owner's tool was changed by the fix: %q", o.Tool.CommandTemplate)
	}
	a, _ := loadAgent(udb, "a11")
	tools := agentPoolTools(owner, a)
	if _, on := tools["jira_search_fix"]; !on {
		t.Error("the agent does not use the fix")
	}
	if _, on := tools["jira_search"]; on {
		t.Error("the agent still uses the broken original")
	}
	if len(a.ToolFixes) != 1 || a.ToolFixes[0].By != "mia" {
		t.Errorf("the fix is not on record: %+v", a.ToolFixes)
	}
	told := false
	for _, n := range notices.List(RootDB, owner) {
		if strings.Contains(n.Title, "mia fixed jira_search") {
			told = true
		}
	}
	if !told {
		t.Error("the owner was not told of the fix")
	}

	if code := as("mia", "POST", "/api/agents/a11/tool-fix/resolve?tool=jira_search&keep=1", "{}").Code; code != http.StatusForbidden {
		t.Errorf("a maintainer kept their own fix: %d", code)
	}
	if code := as(owner, "POST", "/api/agents/a11/tool-fix/resolve?tool=jira_search&keep=1", "{}").Code; code != http.StatusNoContent {
		t.Fatalf("keep: %d", code)
	}
	o, _ := UserToolByName(AuthDB(), owner, "jira_search")
	if o.Tool.CommandTemplate != "https://jira.example/rest/api/3/search?q={q}" || o.Tool.ResponsePipe != "jq .issues" || o.Tool.Credential != "jira" {
		t.Errorf("keeping the fix: %+v", o.Tool)
	}
	if _, still := UserToolByName(AuthDB(), owner, "jira_search_fix"); still {
		t.Error("the copy outlived the owner keeping it")
	}
	a, _ = loadAgent(udb, "a11")
	if namedIn(a.DisabledPersistentTools, "jira_search") || len(a.ToolFixes) != 0 {
		t.Errorf("after keeping: disabled %v fixes %+v", a.DisabledPersistentTools, a.ToolFixes)
	}

	// Discarding goes back to the original as it was.
	if code := fix("mia", "jira_search", `{"command_template":"https://jira.example/rest/api/4/search?q={q}","method":"GET"}`); code != http.StatusNoContent {
		t.Fatalf("second fix: %d", code)
	}
	if code := as(owner, "POST", "/api/agents/a11/tool-fix/resolve?tool=jira_search", "{}").Code; code != http.StatusNoContent {
		t.Fatalf("discard: %d", code)
	}
	if o, _ := UserToolByName(AuthDB(), owner, "jira_search"); !strings.Contains(o.Tool.CommandTemplate, "/api/3/") {
		t.Errorf("discarding changed the original: %q", o.Tool.CommandTemplate)
	}
	if _, still := UserToolByName(AuthDB(), owner, "jira_search_fix"); still {
		t.Error("the discarded copy is still there")
	}
}

// A toolbox fix changes its actions' requests; each action keeps the
// original's pipe and read mark whatever was sent, a new action brings none,
// and no action can carry a command.
func TestAToolboxFixKeepsWhatAFixMayNotChange(t *testing.T) {
	base := TempTool{Name: "jira", Mode: TempToolModeToolbox, Credential: "jira", Actions: []TempToolAction{
		{Name: "search", URLTemplate: "https://jira.example/2/search", Method: "POST", ResponsePipe: "jq .issues", Reads: true},
	}}
	sent := TempTool{Credential: "other", Actions: []TempToolAction{
		{Name: "search", URLTemplate: "https://jira.example/3/search", Method: "POST", ResponsePipe: "cat /etc/passwd", Reads: false},
		{Name: "comment", URLTemplate: "https://jira.example/3/issue/{key}/comment", Method: "POST", ResponsePipe: "sh evil", Reads: true, CommandTemplate: "rm -rf /"},
	}}
	got := fixedShape(base, sent)
	if got.Credential != "jira" || len(got.Actions) != 2 {
		t.Fatalf("got %+v", got)
	}
	s, c := got.Actions[0], got.Actions[1]
	if s.URLTemplate != "https://jira.example/3/search" || s.ResponsePipe != "jq .issues" || !s.Reads {
		t.Errorf("the fixed action: %+v", s)
	}
	if c.ResponsePipe != "" || c.Reads || c.CommandTemplate != "" {
		t.Errorf("a new action brought what a fix may not set: %+v", c)
	}
	if !fixableTool(base) || fixableTool(TempTool{Mode: TempToolModeToolbox, Actions: []TempToolAction{{Name: "x", CommandTemplate: "ls"}}}) {
		t.Error("which toolboxes may be fixed")
	}
}
