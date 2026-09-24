package orchestrate

// Nothing breaks SILENTLY when a shared thing is taken away: the model is told
// on every turn, the session gets one breadcrumb, the people who lost it are
// told which of their agents relied on it, and the owner sees those agents
// before taking it back.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/notices"
	"github.com/cmcoffee/gohort/core/shareledger"
)

// depStores wires every store the path touches onto one in-memory root, with
// the named accounts on it, and clears the in-process memories this file keeps.
func depStores(t *testing.T, users ...string) Database {
	t.Helper()
	root := scopeStores(t)
	prevBase, prevAuth, prevRef := orchestrateBaseDB, AuthDB, orchRef
	orchestrateBaseDB = root
	AuthDB = func() Database { return root }
	orchRef = &OrchestrateApp{AppCore: AppCore{DB: root}}
	t.Cleanup(func() { orchestrateBaseDB, AuthDB, orchRef = prevBase, prevAuth, prevRef })
	for _, u := range users {
		root.Set(AuthTable, "user:"+u, AuthUser{Username: u})
	}
	missingByRun.Lock()
	missingByRun.m = map[string]*pendingMissing{}
	missingByRun.Unlock()
	missingDiagSeen.Lock()
	missingDiagSeen.m = map[string]bool{}
	missingDiagSeen.Unlock()
	missingNameCache.Lock()
	missingNameCache.m = map[string]string{}
	missingNameCache.Unlock()
	return root
}

// runTurn loads an agent's tools the way every run path does and returns the
// turn-scoped notes the loop would append to the user's message.
func runTurn(t *testing.T, root Database, user string, a AgentRecord, sessionID string) string {
	t.Helper()
	udb := UserDB(root, user)
	turn := &chatTurn{user: user, udb: udb, agent: a, session: &ChatSession{ID: sessionID}}
	sess := &ToolSession{Username: user, AgentID: a.ID}
	turn.loadAgentTempTools(sess, user, root)
	return turnNotes(sess, udb, sessionID, "what can you do?")
}

// diagsOf counts the session's breadcrumbs of one kind.
func diagsOf(root Database, user, agentID, sessionID, kind string) int {
	var list []SessionDiag
	UserDB(root, user).Get(sessionDiagTable, agentID+":"+sessionID, &list)
	n := 0
	for _, d := range list {
		if d.Kind == kind {
			n++
		}
	}
	return n
}

// noticeFor finds the recipient's notice whose title mentions name.
func noticeFor(root Database, user, name string) (notices.Notice, bool) {
	for _, n := range notices.List(root, user) {
		if strings.Contains(n.Title, name) {
			return n, true
		}
	}
	return notices.Notice{}, false
}

// The note is the whole point of (1): said on the newest user turn, every turn
// the gap stands, and a breadcrumb that is NOT repeated every turn.
func assertNotedOnce(t *testing.T, root Database, user string, a AgentRecord, name string) {
	t.Helper()
	for turn := 0; turn < 3; turn++ {
		note := runTurn(t, root, user, a, "s1")
		if !strings.Contains(note, "\""+name+"\"") || !strings.Contains(note, "no longer available to you") ||
			!strings.Contains(note, "Do not claim to use") {
			t.Fatalf("turn %d: the model was not told %q is gone: %q", turn, name, note)
		}
		if strings.Contains(note, "—") {
			t.Errorf("model-facing text carries an em-dash: %q", note)
		}
	}
	if got := diagsOf(root, user, a.ID, "s1", "dependency-missing"); got != 1 {
		t.Errorf("want ONE dependency-missing breadcrumb for the session, got %d", got)
	}
	// A new session gets its own.
	runTurn(t, root, user, a, "s2")
	if got := diagsOf(root, user, a.ID, "s2", "dependency-missing"); got != 1 {
		t.Errorf("a new session should get its own breadcrumb, got %d", got)
	}
}

func TestARevokedToolIsNotedAndItsTakerTold(t *testing.T) {
	root := depStores(t, "lender", "u")
	if err := AdminPersistTempTool(root, "lender", TempTool{Name: "wiki_read", Description: "d", CommandTemplate: "echo lent"}); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolSharedWith(root, "lender", "wiki_read", []string{"u"}); err != nil {
		t.Fatal(err)
	}
	if err := SetGlobalToolAdopted(root, "u", "wiki_read", "lender", true); err != nil {
		t.Fatal(err)
	}
	a := AgentRecord{ID: "agent-1", Owner: "u", Name: "Helper", OrchestratorPrompt: "p"}
	if _, err := saveAgent(UserDB(root, "u"), a); err != nil {
		t.Fatal(err)
	}
	// Loaded, nothing to say.
	if note := runTurn(t, root, "u", a, "s0"); strings.Contains(note, "no longer available") {
		t.Fatalf("a working tool was reported missing: %q", note)
	}

	// The owner sees who relies on it before taking it back.
	var grant shareledger.Grant
	for _, g := range shareledger.Mine("lender") {
		if g.Kind == "tool" && g.ID == "wiki_read" {
			grant = g
		}
	}
	if len(grant.Dependents) != 1 || grant.Dependents[0].User != "u" || !namedIn(grant.Dependents[0].Uses, "Helper") {
		t.Fatalf("the grant should name u's Helper as relying on it, got %+v", grant.Dependents)
	}

	// Taken back through the setter every door uses.
	if err := SetPersistentTempToolSharedWith(root, "lender", "wiki_read", nil); err != nil {
		t.Fatal(err)
	}
	n, ok := noticeFor(root, "u", "wiki_read")
	if !ok {
		t.Fatalf("the taker was not told: %+v", notices.List(root, "u"))
	}
	if !strings.Contains(n.Body, "Helper") || n.Kind != notices.KindBlocked {
		t.Errorf("the notice should name the agent that relied on it and wait on them: %+v", n)
	}
	assertNotedOnce(t, root, "u", a, "wiki_read")
}

func TestARevokedSkillIsNotedAndItsHolderTold(t *testing.T) {
	root := depStores(t, "alice", "u")
	s, err := SaveSkill(nil, "alice", SkillRecord{Name: "Runbook steps", Description: "d", AllowedUsers: []string{"u"}})
	if err != nil {
		t.Fatal(err)
	}
	a := AgentRecord{ID: "agent-2", Owner: "u", Name: "Triage", OrchestratorPrompt: "p", AllowedSkills: []string{s.ID}}
	if _, err := saveAgent(UserDB(root, "u"), a); err != nil {
		t.Fatal(err)
	}
	if note := runTurn(t, root, "u", a, "s0"); strings.Contains(note, "no longer available") {
		t.Fatalf("a shared skill was reported missing while shared: %q", note)
	}
	if err := shareledger.Revoke("skill", "alice", s.ID, "u"); err != nil {
		t.Fatal(err)
	}
	n, ok := noticeFor(root, "u", "Runbook steps")
	if !ok || !strings.Contains(n.Body, "Triage") {
		t.Fatalf("the holder was not told, naming their agent: %+v", notices.List(root, "u"))
	}
	// Named, not quoted as an id: the skill still exists under its owner.
	assertNotedOnce(t, root, "u", a, "Runbook steps")
}

func TestADeletedCollectionIsNotedAndItsReaderTold(t *testing.T) {
	root := depStores(t, "alice", "u")
	cdb := UserDB(CollectionsDB(), "alice")
	if cdb == nil {
		t.Skip("no per-user collection store")
	}
	SaveCollection(cdb, Collection{ID: "col-1", Owner: "alice", Name: "Runbooks", AllowedUsers: []string{"u"}})
	a := AgentRecord{ID: "agent-3", Owner: "u", Name: "Support", OrchestratorPrompt: "p", AttachedCollections: []string{"col-1"}}
	if _, err := saveAgent(UserDB(root, "u"), a); err != nil {
		t.Fatal(err)
	}
	if note := runTurn(t, root, "u", a, "s0"); strings.Contains(note, "no longer available") {
		t.Fatalf("a shared collection was reported missing while shared: %q", note)
	}
	DeleteCollection(cdb, nil, "alice", "col-1")
	n, ok := noticeFor(root, "u", "Runbooks")
	if !ok || !strings.Contains(n.Body, "Support") {
		t.Fatalf("the reader was not told, naming their agent: %+v", notices.List(root, "u"))
	}
	// The record is gone; its name survives because the withdrawal left it.
	assertNotedOnce(t, root, "u", a, "Runbooks")
}

// Somebody who never used the thing is still told they lost it, but nothing
// is waiting on them.
func TestARecipientWithNoDependentsIsToldWithoutBeingBlocked(t *testing.T) {
	root := depStores(t, "alice", "v")
	s, err := SaveSkill(nil, "alice", SkillRecord{Name: "Tone guide", AllowedUsers: []string{"v"}})
	if err != nil {
		t.Fatal(err)
	}
	if !DeleteSkill(nil, "alice", s.ID) {
		t.Fatal("delete failed")
	}
	n, ok := noticeFor(root, "v", "Tone guide")
	if !ok {
		t.Fatalf("the recipient was not told: %+v", notices.List(root, "v"))
	}
	if n.Kind == notices.KindBlocked {
		t.Errorf("nothing relied on it, so nothing should be waiting on them: %+v", n)
	}
}

func TestSkillAndCollectionGrantsNameTheirDependents(t *testing.T) {
	root := depStores(t, "alice", "u", "v")
	s, err := SaveSkill(nil, "alice", SkillRecord{Name: "Runbook steps", AllowedUsers: []string{"u", "v"}})
	if err != nil {
		t.Fatal(err)
	}
	cdb := UserDB(CollectionsDB(), "alice")
	SaveCollection(cdb, Collection{ID: "col-9", Owner: "alice", Name: "Runbooks", AllowedUsers: []string{"u"}})
	if _, err := saveAgent(UserDB(root, "u"), AgentRecord{ID: "a-u", Owner: "u", Name: "Triage", OrchestratorPrompt: "p",
		AllowedSkills: []string{s.ID}, AttachedCollections: []string{"col-9"}}); err != nil {
		t.Fatal(err)
	}
	byKind := map[string]shareledger.Grant{}
	for _, g := range shareledger.Mine("alice") {
		byKind[g.Kind] = g
	}
	for _, kind := range []string{"skill", "collection"} {
		deps := byKind[kind].Dependents
		if len(deps) != 1 || deps[0].User != "u" || !namedIn(deps[0].Uses, "Triage") {
			t.Errorf("%s grant: want u's Triage and nobody else (v has nothing using it), got %+v", kind, deps)
		}
	}
}

// The Tools modal draws a checkbox per catalog tool and posts the checked set.
// A name with no checkbox - a taken tool whose owner withdrew it - must survive
// that save; only Remove takes it off.
func TestToolsModalSaveKeepsADanglingToolName(t *testing.T) {
	app, req, udb := authedApp(t)
	prevRoot := RootDB
	RootDB = app.DB
	t.Cleanup(func() { RootDB = prevRoot })
	if err := AdminPersistTempTool(app.DB, "lender", TempTool{Name: "wiki_read", Description: "d", CommandTemplate: "echo"}); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolSharedWith(app.DB, "lender", "wiki_read", []string{"alice"}); err != nil {
		t.Fatal(err)
	}
	if err := SetGlobalToolAdopted(app.DB, "alice", "wiki_read", "lender", true); err != nil {
		t.Fatal(err)
	}
	if _, err := saveAgent(udb, AgentRecord{ID: "agent-1", Owner: "alice", Name: "Helper", OrchestratorPrompt: "p",
		AllowedTools: []string{"wiki_read"}}); err != nil {
		t.Fatal(err)
	}
	// Withdrawn: the name now resolves to nothing, and no catalog lists it.
	if err := SetPersistentTempToolSharedWith(app.DB, "lender", "wiki_read", nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := loadAgent(udb, "agent-1"); !namedIn(got.AllowedTools, "wiki_read") {
		t.Fatalf("loading the agent healed the withdrawn name away before anybody saw it: %v", got.AllowedTools)
	}
	// Every catalog box unchecked: the modal posts the sentinel.
	w := httptest.NewRecorder()
	app.handleAgentList(w, req(http.MethodPost, "/api/agents?tools_modal=1", map[string]any{
		"id": "agent-1", "owner": "alice", "name": "Helper", "orchestrator_prompt": "p", "allowed_tools": []string{"__none__"},
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	got, _ := loadAgent(udb, "agent-1")
	if !namedIn(got.AllowedTools, "wiki_read") {
		t.Fatalf("the save dropped a name the modal never showed: %v", got.AllowedTools)
	}

	// Remove is the explicit door, and it leaves an emptied list as "no
	// tools", never as "every tool".
	w = httptest.NewRecorder()
	app.handleAgentOne(w, req(http.MethodPost, "/api/agents/agent-1/missing", map[string]any{"kind": "tool", "id": "wiki_read"}))
	if w.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	got, _ = loadAgent(udb, "agent-1")
	if namedIn(got.AllowedTools, "wiki_read") || !isNoToolsSentinel(got.AllowedTools) {
		t.Errorf("Remove should take the name off and leave no-tools, got %v", got.AllowedTools)
	}
}

func TestKeepUnlistedTools(t *testing.T) {
	catalog := []string{"web_search", "fetch_url"}
	cases := []struct {
		name              string
		stored, submitted []string
		want              []string
	}{
		{"nothing unlisted: the modal decides", []string{"web_search"}, []string{"fetch_url"}, []string{"fetch_url"}},
		{"default pool stays default", nil, nil, nil},
		{"some checked keeps the unlisted", []string{"web_search", "wiki_read"}, []string{"fetch_url"}, []string{"fetch_url", "wiki_read"}},
		{"all checked does not collapse away the unlisted", []string{"wiki_read"}, nil, []string{"web_search", "fetch_url", "wiki_read"}},
		{"none checked keeps the unlisted", []string{"wiki_read"}, []string{"__none__"}, []string{"wiki_read"}},
	}
	for _, c := range cases {
		got := keepUnlistedTools(c.stored, c.submitted, catalog)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// The editor's list of what is gone is the runtime's, by the last known name.
func TestTheEditorListsWhatIsMissing(t *testing.T) {
	app, req, udb := authedApp(t)
	prevRoot, prevBase := RootDB, orchestrateBaseDB
	RootDB, orchestrateBaseDB = app.DB, app.DB
	t.Cleanup(func() { RootDB, orchestrateBaseDB = prevRoot, prevBase })
	if err := AdminPersistTempTool(app.DB, "lender", TempTool{Name: "wiki_read", Description: "d", CommandTemplate: "echo"}); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolSharedWith(app.DB, "lender", "wiki_read", []string{"alice"}); err != nil {
		t.Fatal(err)
	}
	if err := SetGlobalToolAdopted(app.DB, "alice", "wiki_read", "lender", true); err != nil {
		t.Fatal(err)
	}
	if _, err := saveAgent(udb, AgentRecord{ID: "agent-1", Owner: "alice", Name: "Helper", OrchestratorPrompt: "p",
		AllowedSkills: []string{"skill-gone"}}); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolSharedWith(app.DB, "lender", "wiki_read", nil); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	app.handleAgentOne(w, req(http.MethodGet, "/api/agents/agent-1/missing", nil))
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `"wiki_read"`) || !strings.Contains(body, `"skill-gone"`) {
		t.Fatalf("want the withdrawn tool and the missing skill listed, got %d %s", w.Code, body)
	}
	// Removing a withdrawn TAKEN tool clears the adoption every agent loaded
	// it through.
	w = httptest.NewRecorder()
	app.handleAgentOne(w, req(http.MethodPost, "/api/agents/agent-1/missing", map[string]any{"kind": "tool", "id": "wiki_read"}))
	if w.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	if LoadAdoptedGlobalTools(app.DB, "alice")["wiki_read"] {
		t.Error("the dangling adoption survived Remove")
	}
}

// A published tool its author withdrew is offered back to the taker as their
// own copy; the agent keeps the name and works again. A revoked peer share is
// not offered: that was taken from them on purpose.
func TestTheEditorOffersToKeepAWithdrawnTool(t *testing.T) {
	app, req, udb := authedApp(t)
	prevRoot, prevBase := RootDB, orchestrateBaseDB
	RootDB, orchestrateBaseDB = app.DB, app.DB
	t.Cleanup(func() { RootDB, orchestrateBaseDB = prevRoot, prevBase })
	for _, name := range []string{"wiki_read", "ticket_read"} {
		if err := AdminPersistTempTool(app.DB, "lender", TempTool{Name: name, Description: "d", CommandTemplate: "echo " + name}); err != nil {
			t.Fatal(err)
		}
	}
	_ = SetPersistentTempToolShared(app.DB, "lender", "wiki_read", true)
	_ = SetPersistentTempToolSharedWith(app.DB, "lender", "ticket_read", []string{"alice"})
	for _, name := range []string{"wiki_read", "ticket_read"} {
		if err := SetGlobalToolAdopted(app.DB, "alice", name, "lender", true); err != nil {
			t.Fatal(err)
		}
	}
	AdoptedToolsFor(app.DB, "alice")
	if _, err := saveAgent(udb, AgentRecord{ID: "agent-1", Owner: "alice", Name: "Helper", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	_ = SetPersistentTempToolShared(app.DB, "lender", "wiki_read", false)
	_ = SetPersistentTempToolSharedWith(app.DB, "lender", "ticket_read", nil)
	// The notice says so too, for the one that can be kept.
	if n, ok := noticeFor(app.DB, "alice", "wiki_read"); !ok || !strings.Contains(n.Body, "Keep it") {
		t.Errorf("the withdrawal notice should offer to keep it: %+v", n)
	}
	if n, ok := noticeFor(app.DB, "alice", "ticket_read"); !ok || strings.Contains(n.Body, "Keep it") {
		t.Errorf("a revoked share must not be offered: %+v", n)
	}

	w := httptest.NewRecorder()
	app.handleAgentOne(w, req(http.MethodGet, "/api/agents/agent-1/missing", nil))
	var got struct {
		Items []missingRef `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	offered := map[string]bool{}
	for _, it := range got.Items {
		offered[it.ID] = it.Recreatable
	}
	if !offered["wiki_read"] || offered["ticket_read"] {
		t.Fatalf("want the withdrawn published tool offered and the revoked share not, got %+v", got.Items)
	}

	w = httptest.NewRecorder()
	app.handleAgentOne(w, req(http.MethodPost, "/api/agents/agent-1/missing", map[string]any{"kind": "tool", "id": "ticket_read", "action": "recreate"}))
	if w.Code == http.StatusOK {
		t.Fatal("a revoked share was recreated")
	}
	w = httptest.NewRecorder()
	app.handleAgentOne(w, req(http.MethodPost, "/api/agents/agent-1/missing", map[string]any{"kind": "tool", "id": "wiki_read", "action": "recreate"}))
	if w.Code != http.StatusOK {
		t.Fatalf("keep: %d %s", w.Code, w.Body.String())
	}
	mine := false
	for _, p := range LoadPersistentTempTools(app.DB, "alice") {
		mine = mine || (p.Tool.Name == "wiki_read" && p.Tool.CommandTemplate == "echo wiki_read")
	}
	if !mine {
		t.Fatal("alice has no copy of the tool she kept")
	}
	w = httptest.NewRecorder()
	app.handleAgentOne(w, req(http.MethodGet, "/api/agents/agent-1/missing", nil))
	if strings.Contains(w.Body.String(), `"wiki_read"`) {
		t.Errorf("the kept tool is still listed missing: %s", w.Body.String())
	}
}
