package orchestrate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/notices"
	"github.com/cmcoffee/gohort/core/peershare"
	"github.com/cmcoffee/gohort/core/revisions"
	"github.com/cmcoffee/gohort/core/shareledger"
)

// What a maintainer may change is a list, and everything off it stays the
// owner's: reach, access, guardrails. The lists of the owner's things the agent
// uses may shrink but not grow; the lists that switch things off may grow but
// not shrink.
func TestAMaintainerAdjustsButCannotWiden(t *testing.T) {
	cur := AgentRecord{ID: "a1", Owner: "alice", Name: "Support", OrchestratorPrompt: "old",
		AllowedUsers: []string{"group:tier2"}, Maintainers: []string{"group:tier3"},
		AllowedTools: []string{"search_kb", "open_ticket"}, AttachedCollections: []string{"kb"},
		DisabledCredentials: []string{"payments"}, NoUnattendedTools: []string{"open_ticket"},
		Guardrails: "never promise refunds", AllowPrivateMode: false}
	req := cur
	req.Name, req.OrchestratorPrompt, req.Rules = "Support desk", "new", "- be brief"
	req.AllowedUsers, req.Maintainers, req.Everyone, req.MCPExposed = []string{"mallory"}, []string{"mallory"}, true, true
	req.AllowedTools = []string{"search_kb", "run_sql"}
	req.AttachedCollections = []string{"kb", "owner_private_notes"}
	req.DisabledCredentials = nil
	req.NoUnattendedTools = []string{"open_ticket", "search_kb"}
	req.Guardrails, req.AllowPrivateMode = "", true

	got := applyMaintainerEdit(cur, req)
	if got.Name != "Support desk" || got.OrchestratorPrompt != "new" || got.Rules != "- be brief" {
		t.Errorf("the words did not change: %+v", got)
	}
	if strings.Join(got.AllowedUsers, ",") != "group:tier2" || strings.Join(got.Maintainers, ",") != "group:tier3" || got.Everyone || got.MCPExposed {
		t.Errorf("a maintainer changed who reaches it: %+v", got)
	}
	if strings.Join(got.AllowedTools, ",") != "search_kb" {
		t.Errorf("tools: %v (a tool may be dropped, never added)", got.AllowedTools)
	}
	// An empty tool list means every default tool: taking the last one off
	// leaves none, never all, and a list that already means all or none is
	// the owner's to change.
	req.AllowedTools = nil
	if got := applyMaintainerEdit(cur, req); !isNoToolsSentinel(got.AllowedTools) {
		t.Errorf("emptying the tools widened the agent: %v", got.AllowedTools)
	}
	open := cur
	open.AllowedTools = nil
	req.AllowedTools = []string{"search_kb"}
	if got := applyMaintainerEdit(open, req); len(got.AllowedTools) != 0 {
		t.Errorf("a maintainer changed an agent that uses every default tool: %v", got.AllowedTools)
	}
	req.DispatchMode, req.AllowedDispatchTargets = "all", nil
	cur.DispatchMode, cur.AllowedDispatchTargets = "only", []string{"triage"}
	if got := applyMaintainerEdit(cur, req); got.DispatchMode != "only" || len(got.AllowedDispatchTargets) != 1 {
		t.Errorf("a maintainer changed where the agent may hand work: %+v", got)
	}
	if strings.Join(got.AttachedCollections, ",") != "kb" {
		t.Errorf("collections: %v", got.AttachedCollections)
	}
	if strings.Join(got.DisabledCredentials, ",") != "payments" {
		t.Errorf("a maintainer switched a credential back on: %v", got.DisabledCredentials)
	}
	if strings.Join(got.NoUnattendedTools, ",") != "open_ticket,search_kb" {
		t.Errorf("a stricter setting was lost: %v", got.NoUnattendedTools)
	}
	if got.Guardrails != "never promise refunds" || got.AllowPrivateMode {
		t.Errorf("a maintainer changed the owner's guardrails or privacy: %+v", got)
	}
}

// Through the routes: a maintainer opens the owner's agent and adjusts it in
// the owner's store, with their name on the kept version; somebody who is not
// a maintainer finds nothing; the owner sets maintainers on the Sharing list,
// and a save that never mentions them keeps them.
func TestAMaintainerEditsTheOwnersAgent(t *testing.T) {
	app, udb, owner := newTestOrchestrate(t)
	adb := AuthDB()
	prevRoot := RootDB
	RootDB = app.DB
	t.Cleanup(func() { RootDB = prevRoot })
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	adb.Set(AuthTable, "user:carol", AuthUser{Username: "carol"})
	if _, err := saveAgent(udb, AgentRecord{ID: "a1", Owner: owner, Name: "Support", OrchestratorPrompt: "old",
		AllowedTools: []string{"search_kb"}}); err != nil {
		t.Fatal(err)
	}
	as := func(user, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(adb, user)})
		w := httptest.NewRecorder()
		routed(app)(w, r)
		return w
	}
	// The two pickers post their own list alone; neither clears the other.
	if w := as(owner, "POST", "/api/console/permissions/share?agent=a1", `{"allowed_users":["carol"]}`); w.Code != 200 {
		t.Fatalf("the owner could not share it: %d %s", w.Code, w.Body.String())
	}
	if w := as(owner, "POST", "/api/console/permissions/share?agent=a1", `{"maintainers":["bob"]}`); w.Code != 200 {
		t.Fatalf("the owner could not name a maintainer: %d %s", w.Code, w.Body.String())
	}
	if got, _ := loadAgent(udb, "a1"); strings.Join(got.AllowedUsers, ",") != "carol" || strings.Join(got.Maintainers, ",") != "bob" {
		t.Fatalf("one picker's save changed the other's list: users %v, maintainers %v", got.AllowedUsers, got.Maintainers)
	}
	if w := as(owner, "POST", "/api/console/permissions/share?agent=a1", `{"allowed_users":[]}`); w.Code != 200 {
		t.Fatalf("unshare: %d", w.Code)
	}
	if w := as("bob", "GET", "/api/agents/a1", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"Support"`) {
		t.Fatalf("the maintainer cannot open the agent: %d %s", w.Code, w.Body.String())
	}
	// Told, and shown where it is.
	if ns := notices.List(RootDB, "bob"); len(ns) == 0 || !strings.Contains(ns[0].Title, "maintainer") {
		t.Errorf("the new maintainer was not told: %+v", ns)
	}
	registerShareProviders()
	maintained := false
	for _, g := range shareledger.ToMe("bob") {
		if g.ID == "a1" && g.Reach == "Yours to maintain" {
			maintained = true
		}
	}
	if !maintained {
		t.Error("the agent is not in the maintainer's shared-with-me list")
	}
	// The maintainer's editor: only what they may change.
	pr := httptest.NewRequest("GET", "/agent/a1", nil)
	pr.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(adb, "bob")})
	pw := httptest.NewRecorder()
	app.handleAgentPage(pw, pr)
	if page := pw.Body.String(); !strings.Contains(page, "Maintain Support") || strings.Contains(page, `"allowed_tools"`) {
		t.Errorf("the maintainer's editor: %d %.300s", pw.Code, page)
	}
	if w := as("carol", "GET", "/api/agents/a1", ""); w.Code != http.StatusNotFound {
		t.Errorf("somebody who is not a maintainer opened it: %d", w.Code)
	}
	body, _ := json.Marshal(map[string]any{"id": "a1", "name": "Support", "orchestrator_prompt": "adjusted",
		"allowed_tools": []string{"search_kb", "run_sql"}, "maintainers": []string{"bob", "carol"}})
	if w := as("bob", "POST", "/api/agents", string(body)); w.Code != 200 {
		t.Fatalf("the maintainer's save: %d %s", w.Code, w.Body.String())
	}
	got, _ := loadAgent(udb, "a1")
	if got.OrchestratorPrompt != "adjusted" || strings.Contains(strings.Join(got.AllowedTools, ","), "run_sql") || strings.Join(got.Maintainers, ",") != "bob" {
		t.Errorf("the owner's record after the maintainer's save: %+v", got)
	}
	if _, ok := loadAgent(UserDB(app.DB, "bob"), "a1"); ok {
		t.Error("the save made a copy in the maintainer's own store")
	}
	revs := revisions.List(udb, revisions.KindAgent, "a1")
	if len(revs) == 0 || !strings.Contains(revs[0].Reason, "bob") {
		t.Errorf("the kept version does not say who: %+v", revs)
	}
	if w := as("bob", "PATCH", "/api/agents/a1", `{"description":"for tier 2","exposed":true}`); w.Code != 200 {
		t.Fatalf("the maintainer's patch: %d %s", w.Code, w.Body.String())
	}
	if got, _ := loadAgent(udb, "a1"); got.Description != "for tier 2" || got.Everyone {
		t.Errorf("after the patch: %+v", got)
	}
	// The owner's own form save, which never shows maintainers, keeps them.
	if w := as(owner, "POST", "/api/agents", `{"id":"a1","name":"Support","orchestrator_prompt":"owner's"}`); w.Code != 200 {
		t.Fatalf("the owner's save: %d %s", w.Code, w.Body.String())
	}
	if got, _ := loadAgent(udb, "a1"); strings.Join(got.Maintainers, ",") != "bob" {
		t.Errorf("the owner's save dropped the maintainers: %v", got.Maintainers)
	}
	// Taken off the list, the maintainer reaches nothing.
	as(owner, "POST", "/api/console/permissions/share?agent=a1", `{"allowed_users":[],"maintainers":[]}`)
	if w := as("bob", "GET", "/api/agents/a1", ""); w.Code != http.StatusNotFound {
		t.Errorf("a removed maintainer still opens it: %d", w.Code)
	}
}

// The record decides, not the index: a maintainer taken off the agent's list
// by a write that left the index behind still reaches nothing. A PATCH from a
// maintainer cannot change who reaches the agent, nor file a publish request
// in their name; and going back to a version from when somebody was a
// maintainer does not make them one again.
func TestAMaintainerIsHeldToTheRecord(t *testing.T) {
	app, udb, owner := newTestOrchestrate(t)
	adb := AuthDB()
	prevRoot := RootDB
	RootDB = app.DB
	t.Cleanup(func() { RootDB = prevRoot })
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	saveAgent(udb, AgentRecord{ID: "a1", Owner: owner, Name: "Support", OrchestratorPrompt: "p", Maintainers: []string{"bob"}})
	as := func(user, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(adb, user)})
		w := httptest.NewRecorder()
		routed(app)(w, r)
		return w
	}
	if w := as("bob", "PATCH", "/api/agents/a1", `{"allowed_users":["mallory"],"exposed":true}`); w.Code != 200 {
		t.Fatalf("patch: %d %s", w.Code, w.Body.String())
	}
	if got, _ := loadAgent(udb, "a1"); len(got.AllowedUsers) != 0 || got.Everyone {
		t.Errorf("a maintainer's PATCH changed who reaches it: %+v", got)
	}
	for _, req := range ListPromotionRequests(adb, true) {
		if req.Owner == "bob" {
			t.Errorf("a maintainer's PATCH filed a publish request: %+v", req)
		}
	}
	// A version from when bob maintained it, restored after he was taken off.
	cur, _ := loadAgent(udb, "a1")
	cur.Maintainers, cur.OrchestratorPrompt = nil, "q"
	saveAgent(udb, cur)
	if _, err := rollbackAgent(udb, "a1", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := loadAgent(udb, "a1"); len(got.Maintainers) != 0 {
		t.Errorf("a rollback made somebody a maintainer again: %v", got.Maintainers)
	}
	// Off the record but still in the index: nothing.
	raw, _ := loadAgent(udb, "a1")
	raw.Maintainers = nil
	udb.Set(agentsTable, "a1", raw)
	peershare.SetRecipients(RootDB, MaintainedAgentsTable, owner, "a1", []string{"bob"})
	if w := as("bob", "GET", "/api/agents/a1", ""); w.Code != http.StatusNotFound {
		t.Errorf("a stale index entry let a former maintainer in: %d", w.Code)
	}
}
