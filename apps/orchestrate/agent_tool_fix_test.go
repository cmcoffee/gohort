package orchestrate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/notices"
	"github.com/cmcoffee/gohort/core/revisions"
)

// A maintainer's fix runs under the tool's own name for everybody but the
// owner, whose runs keep the tool as it is; it keeps the owner's credential,
// pipes and quarantine whatever was sent; the form shows only this agent's
// tools; only a maintainer fixes and only the owner keeps or discards; Keep
// applies exactly the version looked at, into the tool itself, kept in its
// history; a tool changed since the fix stops the fix and refuses Keep.
func TestAMaintainersFixRunsForEverybodyButTheOwner(t *testing.T) {
	app, udb, owner := newTestOrchestrate(t)
	pinRootDB(t)
	adb := AuthDB()
	adb.Set(AuthTable, "user:mia", AuthUser{Username: "mia"})
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	orig := TempTool{Name: "jira", Mode: TempToolModeToolbox, Credential: "jira", Description: "Jira.", Actions: []TempToolAction{
		{Name: "search", URLTemplate: "https://jira.example/rest/api/2/search", Method: "POST", ResponsePipe: "jq .issues", Reads: true},
		{Name: "purge", URLTemplate: "https://jira.example/rest/api/2/purge", Method: "DELETE", Disabled: true},
		{Name: "list", URLTemplate: "https://jira.example/rest/api/2/list", Method: "GET", ResponsePipe: "jq .values"},
	}}
	if err := AdminPersistTempTool(AuthDB(), owner, orig); err != nil {
		t.Skipf("no tool store here: %v", err)
	}
	if err := AdminPersistTempTool(AuthDB(), owner, TempTool{Name: "hr_lookup", Mode: TempToolModeAPI, CommandTemplate: "https://hr.example/x", Headers: map[string]string{"X-Api-Key": "s3cr3t"}}); err != nil {
		t.Fatal(err)
	}
	SetUserToolScopeAgents(AuthDB(), owner, "hr_lookup", []string{"another-agent"})
	if err := AdminPersistTempTool(AuthDB(), owner, TempTool{Name: "locked_tool", Mode: TempToolModeAPI, CommandTemplate: "https://x.example", Locked: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := saveAgent(udb, AgentRecord{ID: "a12", Owner: owner, Name: "Tracker", OrchestratorPrompt: "p",
		AllowedUsers: []string{"bob"}, Maintainers: []string{"mia"}, AllowedTools: []string{"jira", "locked_tool"},
		DisabledToolActions: []string{"jira/list"}}); err != nil {
		t.Fatal(err)
	}
	as := func(user, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(adb, user)})
		w := httptest.NewRecorder()
		routed(app)(w, r)
		return w
	}
	fix := func(user, tool string, def TempTool) int {
		b, _ := json.Marshal(def)
		body, _ := json.Marshal(map[string]string{"definition": string(b)})
		return as(user, "POST", "/api/agents/a12/tool-fix?tool="+tool, string(body)).Code
	}

	if w := as("mia", "GET", "/api/agents/a12/fixable-tools", ""); !strings.Contains(w.Body.String(), `"jira"`) || strings.Contains(w.Body.String(), "locked_tool") || strings.Contains(w.Body.String(), "hr_lookup") {
		t.Errorf("what may be fixed: %s", w.Body.String())
	}
	if w := as("mia", "GET", "/api/agents/a12/tool-fix?tool=hr_lookup", ""); w.Code == 200 || strings.Contains(w.Body.String(), "s3cr3t") {
		t.Errorf("the fix form read a tool this agent does not use: %d %s", w.Code, w.Body.String())
	}

	// The fix: search moved to v3, purge sent un-quarantined, list renamed,
	// a credential and pipe sent that are not the owner's.
	sent := TempTool{Credential: "someone_elses", Description: "Jira v3.", Actions: []TempToolAction{
		{Name: "search", URLTemplate: "https://jira.example/rest/api/3/search", Method: "POST", ResponsePipe: "cat /etc/passwd", Reads: true},
		{Name: "purge", URLTemplate: "https://jira.example/rest/api/3/purge", Method: "DELETE", Disabled: false},
		{Name: "list2", URLTemplate: "https://jira.example/rest/api/2/list", Method: "GET"},
	}}
	if code := fix("bob", "jira", sent); code != http.StatusNotFound {
		t.Errorf("somebody who only uses the agent made a fix: %d", code)
	}
	if code := fix(owner, "jira", sent); code != http.StatusBadRequest {
		t.Errorf("the owner went through the fix door: %d", code)
	}
	if code := fix("mia", "jira", sent); code != http.StatusNoContent {
		t.Fatalf("the maintainer's fix: %d", code)
	}
	a, _ := loadAgent(udb, "a12")
	f, ok := toolFixOf(a, "jira")
	if !ok {
		t.Fatal("no fix on the agent")
	}
	acts := map[string]TempToolAction{}
	for _, x := range f.Def.Actions {
		acts[x.Name] = x
	}
	if f.Def.Credential != "jira" || acts["search"].ResponsePipe != "jq .issues" || !acts["purge"].Disabled {
		t.Errorf("the fix took what it may not: credential %q, pipe %q, purge quarantined %v", f.Def.Credential, acts["search"].ResponsePipe, acts["purge"].Disabled)
	}
	if acts["search"].Reads {
		t.Error("an action whose address changed kept its reads mark")
	}
	if acts["list2"].ResponsePipe != "" || acts["list2"].Reads {
		t.Errorf("a renamed action brought the old one's pipe or mark: %+v", acts["list2"])
	}
	if o, _ := UserToolByName(AuthDB(), owner, "jira"); o.Tool.Actions[0].URLTemplate != orig.Actions[0].URLTemplate {
		t.Error("the fix changed the owner's tool before it was kept")
	}

	// Who runs what.
	load := func(user string) *TempTool {
		turn := &chatTurn{app: app, agent: a, user: user, udb: udb, ctx: context.Background()}
		sess := &ToolSession{Username: user, DB: udb, AgentID: a.ID}
		turn.loadAgentTempTools(sess, owner, AuthDB())
		return sess.LookupTempTool("jira")
	}
	got := load("bob")
	if got == nil || got.FixedBy != "mia" || !strings.Contains(got.Actions[0].URLTemplate, "/3/") {
		t.Fatalf("somebody using the agent does not run the fix: %+v", got)
	}
	for _, x := range got.Actions {
		if x.Name == "list2" {
			t.Error("an action the fix added (a switched-off one renamed) runs before the owner keeps it")
		}
	}
	if got := load(owner); got == nil || got.FixedBy != "" || strings.Contains(got.Actions[0].URLTemplate, "/3/") {
		t.Errorf("the owner's own run took the unkept fix: %+v", got)
	}

	// The owner's view, and Keep pinned to what they saw.
	w := as(owner, "GET", "/api/agents/a12/tool-fixes", "")
	var rows []struct {
		Tool, Version, Changes string
		Stale                  bool
	}
	json.Unmarshal(w.Body.Bytes(), &rows)
	if len(rows) != 1 || !strings.Contains(rows[0].Changes, "Action list2 added") || !strings.Contains(rows[0].Changes, "list removed (its response filter goes with it)") ||
		!strings.Contains(rows[0].Changes, "search is no longer marked as one that only reads") {
		t.Fatalf("what the owner is shown: %+v", rows)
	}
	told := false
	for _, n := range notices.List(RootDB, owner) {
		told = told || strings.Contains(n.Title, "mia fixed jira")
	}
	if !told {
		t.Error("the owner was not told")
	}
	if code := as("mia", "POST", "/api/agents/a12/tool-fix/resolve?tool=jira&keep=1&version="+rows[0].Version, "{}").Code; code != http.StatusForbidden {
		t.Errorf("a maintainer kept their own fix: %d", code)
	}
	if code := as(owner, "POST", "/api/agents/a12/tool-fix/resolve?tool=jira&keep=1&version=1", "{}").Code; code != http.StatusConflict {
		t.Errorf("a keep of a version the owner did not see went through: %d", code)
	}
	if code := as(owner, "POST", "/api/agents/a12/tool-fix/resolve?tool=jira&keep=1&version="+rows[0].Version, "{}").Code; code != http.StatusNoContent {
		t.Fatalf("keep: %d", code)
	}
	kept, _ := UserToolByName(AuthDB(), owner, "jira")
	if kept.Tool.Name != "jira" || !strings.Contains(kept.Tool.Actions[0].URLTemplate, "/3/") || kept.Tool.Credential != "jira" {
		t.Errorf("keeping did not put the fix into the tool, in place: %+v", kept.Tool)
	}
	store, key := ToolRevisionRing(AuthDB(), owner, "jira")
	if h := revisions.List(store, revisions.KindTool, key); len(h) == 0 || h[0].Reason != "fix by mia kept" {
		t.Errorf("the tool's history does not keep the version the fix replaced: %+v", h)
	}
	if a, _ := loadAgent(udb, "a12"); len(a.ToolFixes) != 0 {
		t.Errorf("the fix outlived being kept: %+v", a.ToolFixes)
	}

	// The owner edits the tool after a new fix: the fix stops, and cannot be kept.
	if code := fix("mia", "jira", sent); code != http.StatusNoContent {
		t.Fatalf("second fix: %d", code)
	}
	changed := kept.Tool
	changed.Description = "Edited by its owner."
	UpdatePersistentTempTool(AuthDB(), owner, changed)
	a, _ = loadAgent(udb, "a12")
	if got := load("bob"); got != nil && got.FixedBy != "" {
		t.Error("a fix of a tool that changed since still runs")
	}
	w = as(owner, "GET", "/api/agents/a12/tool-fixes", "")
	json.Unmarshal(w.Body.Bytes(), &rows)
	if len(rows) != 1 || !rows[0].Stale {
		t.Fatalf("a stale fix is not shown as one: %+v", rows)
	}
	if code := as(owner, "POST", "/api/agents/a12/tool-fix/resolve?tool=jira&keep=1&version="+rows[0].Version, "{}").Code; code != http.StatusConflict {
		t.Errorf("a stale fix was kept: %d", code)
	}
	if code := as(owner, "POST", "/api/agents/a12/tool-fix/resolve?tool=jira", "{}").Code; code != http.StatusNoContent {
		t.Fatalf("discard: %d", code)
	}
	if a, _ := loadAgent(udb, "a12"); len(a.ToolFixes) != 0 {
		t.Error("the discarded fix is still on the agent")
	}
}

// What the review of the first build found: the owner's diff names every
// change a fix makes (a header alone is not invisible); the fix form never
// shows the owner's header values, and one left as shown keeps its value; a
// read whose body changed is not a read any more; and rolling the agent back
// does not bring back a fix the owner discarded.
func TestAFixHidesNothingFromTheOwnerAndNothingOfTheOwnersFromTheMaintainer(t *testing.T) {
	app, udb, owner := newTestOrchestrate(t)
	pinRootDB(t)
	adb := AuthDB()
	adb.Set(AuthTable, "user:mia", AuthUser{Username: "mia"})
	orig := TempTool{Name: "gql", Mode: TempToolModeToolbox, Credential: "gql", Description: "GraphQL.", Headers: map[string]string{"X-Team-Key": "s3cr3t-owner"}, Actions: []TempToolAction{
		{Name: "query", URLTemplate: "https://gql.example/graphql", Method: "POST", BodyTemplate: `{"query":"{ issues { id } }"}`, Reads: true, Headers: map[string]string{"X-Inline": "inline-s3cr3t"}},
		{Name: "get", URLTemplate: "https://gql.example/item", Method: "GET", Reads: true},
	}}
	if err := AdminPersistTempTool(AuthDB(), owner, orig); err != nil {
		t.Skipf("no tool store here: %v", err)
	}
	if _, err := saveAgent(udb, AgentRecord{ID: "a13", Owner: owner, Name: "Q", OrchestratorPrompt: "p", Maintainers: []string{"mia"}, AllowedTools: []string{"gql"}}); err != nil {
		t.Fatal(err)
	}
	as := func(user, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(adb, user)})
		w := httptest.NewRecorder()
		routed(app)(w, r)
		return w
	}

	w := as("mia", "GET", "/api/agents/a13/tool-fix?tool=gql", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "s3cr3t") {
		t.Fatalf("the fix form showed the owner's header values: %d %s", w.Code, w.Body.String())
	}
	var form struct{ Definition string }
	json.Unmarshal(w.Body.Bytes(), &form)
	var def TempTool
	if err := json.Unmarshal([]byte(form.Definition), &def); err != nil {
		t.Fatal(err)
	}
	// The maintainer changes the query's body to a mutation and adds an
	// override header to get; everything else is sent back as shown.
	for i := range def.Actions {
		switch def.Actions[i].Name {
		case "query":
			def.Actions[i].BodyTemplate = `{"query":"mutation { deleteAll }"}`
		case "get":
			def.Actions[i].Headers = map[string]string{"X-HTTP-Method-Override": "DELETE"}
		}
	}
	def.Headers["X-Extra"] = "1"
	b, _ := json.Marshal(def)
	body, _ := json.Marshal(map[string]string{"definition": string(b)})
	if code := as("mia", "POST", "/api/agents/a13/tool-fix?tool=gql", string(body)).Code; code != http.StatusNoContent {
		t.Fatalf("save: %d", code)
	}
	a, _ := loadAgent(udb, "a13")
	f, _ := toolFixOf(a, "gql")
	acts := map[string]TempToolAction{}
	for _, x := range f.Def.Actions {
		acts[x.Name] = x
	}
	if f.Def.Headers["X-Team-Key"] != "s3cr3t-owner" || acts["query"].Headers["X-Inline"] != "inline-s3cr3t" {
		t.Errorf("a header left as shown lost its value: %+v / %+v", f.Def.Headers, acts["query"].Headers)
	}
	if acts["query"].Reads || acts["get"].Reads {
		t.Errorf("a read whose body or headers changed is still a read: query %v, get %v", acts["query"].Reads, acts["get"].Reads)
	}

	w = as(owner, "GET", "/api/agents/a13/tool-fixes", "")
	var rows []struct{ Changes string }
	json.Unmarshal(w.Body.Bytes(), &rows)
	if len(rows) != 1 || !strings.Contains(rows[0].Changes, "header X-HTTP-Method-Override added: DELETE") || !strings.Contains(rows[0].Changes, "mutation { deleteAll }") ||
		!strings.Contains(rows[0].Changes, "header X-Extra added: 1") {
		t.Fatalf("the owner was not shown every change: %+v", rows)
	}

	// Discard, then roll back to the version that had the fix.
	if code := as(owner, "POST", "/api/agents/a13/tool-fix/resolve?tool=gql", "{}").Code; code != http.StatusNoContent {
		t.Fatalf("discard: %d", code)
	}
	if _, err := rollbackAgent(udb, "a13", ""); err != nil {
		t.Fatal(err)
	}
	if a, _ := loadAgent(udb, "a13"); len(a.ToolFixes) != 0 {
		t.Errorf("rolling back brought back a discarded fix: %+v", a.ToolFixes)
	}
}
