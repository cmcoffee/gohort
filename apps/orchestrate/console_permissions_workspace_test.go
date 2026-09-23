package orchestrate

// The workspace decisions on the Permissions page.
//
// They are about the SANDBOX rather than a tool, so they had no row on the one
// page that lists what an owner has granted and lets them take it back. They
// were set in the editor and reviewable nowhere.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func permRowsFor(t *testing.T, app *OrchestrateApp, user string) []map[string]any {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/console/permissions", nil)
	w := httptest.NewRecorder()
	app.handleConsolePermissions(w, asUser(r, user))
	if w.Code != http.StatusOK {
		t.Fatalf("permissions: %d %s", w.Code, w.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("not rows: %v", err)
	}
	return rows
}

func rowByDetail(rows []map[string]any, want string) map[string]any {
	for _, r := range rows {
		if d, _ := r["Detail"].(string); strings.Contains(d, want) {
			return r
		}
	}
	return nil
}

// A row appears while the decision is load-bearing, which is the rule the rest
// of this page already follows. An agent that narrowed nothing grows no rows.
func TestAWorkspaceDecisionShowsOnThePermissionsPage(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "w1", Owner: "alice", Name: "Plain", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	if r := rowByDetail(permRowsFor(t, app, "alice"), "Workspace may not reach"); r != nil {
		t.Errorf("an agent that narrowed nothing grew a row: %+v", r)
	}

	if _, err := saveAgent(udb, AgentRecord{ID: "w2", Owner: "alice", Name: "Held", OrchestratorPrompt: "p",
		WorkspaceNoNetwork:  true,
		DisabledToolActions: []string{"workspace/run"}}); err != nil {
		t.Fatal(err)
	}
	rows := permRowsFor(t, app, "alice")

	net := rowByDetail(rows, "Workspace may not reach")
	if net == nil {
		t.Fatal("the workspace decision is still reviewable nowhere")
	}
	if net["_policy"] != PolicyBlock {
		t.Errorf("the row does not read as blocked: %+v", net)
	}
	// "Needs approval" is not a state a sandbox can hold, so the segment is
	// hidden rather than offered and quietly doing nothing.
	if net["_noask"] != true {
		t.Errorf("the row offers a state it cannot store: %+v", net)
	}
	if sub := rowByDetail(rows, "Switched off"); sub == nil {
		t.Error("a switched-off sub-action is reviewable nowhere")
	} else if sub["_noask"] != true {
		t.Errorf("the sub-action row offers a state it cannot store: %+v", sub)
	}
}

// And it can be taken back from the same control that shows it, which is the
// point of the row existing.
func TestAWorkspaceDecisionCanBeRevokedFromThePage(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "w3", Owner: "alice", Name: "Held", OrchestratorPrompt: "p",
		WorkspaceNoNetwork:  true,
		DisabledToolActions: []string{"workspace/run"}}); err != nil {
		t.Fatal(err)
	}
	set := func(id, value string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/console/permissions/policy?id="+id+"&value="+value, nil)
		w := httptest.NewRecorder()
		app.handleConsolePermissionPolicy(w, asUser(r, "alice"))
		// 204 is this endpoint's success: it stores and returns no body.
		if w.Code != http.StatusOK && w.Code != http.StatusNoContent {
			t.Fatalf("policy %s=%s: %d %s", id, value, w.Code, w.Body.String())
		}
	}
	set("workspace:w3:network", PolicyAllow)
	set("subaction:w3:workspace/run", PolicyAllow)

	rec, _ := loadAgent(udb, "w3")
	// Asked of the RESOLVER, not one field. The decision is a tri-state now
	// and the legacy bool is only read for a record written before it existed,
	// so a test reading that field alone would pass while the agent behaved
	// the other way.
	if !agentWorkspaceNetwork(RootDB, rec) {
		t.Error("allowing the workspace did not take")
	}
	if rec.WorkspaceNoNetwork {
		t.Error("the legacy mark survived, so the resolver reads a stale block under a fresh allow")
	}
	if len(rec.DisabledToolActions) != 0 {
		t.Errorf("allowing the sub-action left it switched off: %v", rec.DisabledToolActions)
	}

	// And back again, so the control moves both ways.
	set("workspace:w3:network", PolicyBlock)
	set("subaction:w3:workspace/run", PolicyBlock)
	rec, _ = loadAgent(udb, "w3")
	if agentWorkspaceNetwork(RootDB, rec) {
		t.Error("blocking the workspace did not take")
	}
	if len(rec.DisabledToolActions) != 1 || rec.DisabledToolActions[0] != "workspace/run" {
		t.Errorf("blocking the sub-action did not take: %v", rec.DisabledToolActions)
	}
}

// Somebody else's agent is not theirs to change from this page.
//
// STRUCTURAL, and that is a limitation worth stating rather than hiding: the
// asUser helper in this package ignores the name it is passed and attaches the
// one session the fixture minted, so an end-to-end test "as bob" is a test as
// alice wearing a label. A test that cannot fail for the reason it claims is
// worse than no test, so this pins the gate in the source instead.
func TestOnlyTheOwnerMovesAWorkspaceRow(t *testing.T) {
	src := packageSource(t)
	// Each case on its own: packageSource is every file concatenated, so a
	// window that runs to the next familiar token runs a very long way.
	for _, c := range []struct{ open, close string }{
		{`case "workspace":`, `case "subaction":`},
		{`case "subaction":`, `case "autotool":`},
	} {
		i := strings.Index(src, c.open)
		if i < 0 {
			t.Fatalf("no %s", c.open)
		}
		j := strings.Index(src[i:], c.close)
		if j < 0 {
			t.Fatalf("%s does not reach %s", c.open, c.close)
		}
		body := src[i : i+j]
		// Loads the record and refuses unless it is the caller's...
		if !strings.Contains(body, "rec.Owner == user") {
			t.Errorf("%s does not check ownership", c.open)
		}
		// ...from the CALLER's own store, so a record that is not theirs is
		// not even found.
		if !strings.Contains(body, "UserDB(T.DB, user)") {
			t.Errorf("%s does not read from the caller's own store", c.open)
		}
	}
}

// permRowsForAgentIn asks the page as it is asked from an agent's chat, which
// always carries the agent the menu was opened from.
func permRowsForAgentIn(t *testing.T, app *OrchestrateApp, user, agentID string) []map[string]any {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/console/permissions?agent="+agentID, nil)
	w := httptest.NewRecorder()
	app.handleConsolePermissions(w, asUser(r, user))
	if w.Code != http.StatusOK {
		t.Fatalf("permissions: %d %s", w.Code, w.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("not rows: %v", err)
	}
	return rows
}

// The permissions of AN AGENT, not of the fleet. A page mixing every agent's
// decisions answers "what have I decided somewhere" when the question is
// "what can THIS one do".
func TestThePermissionsPageIsAboutOneAgent(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	for _, a := range []AgentRecord{
		{ID: "mine", Name: "Wren", Owner: "alice", OrchestratorPrompt: "p", WorkspaceNoNetwork: true},
		{ID: "other", Name: "Scribe", Owner: "alice", OrchestratorPrompt: "p", WorkspaceNoNetwork: true},
	} {
		if _, err := saveAgent(udb, a); err != nil {
			t.Fatal(err)
		}
	}
	rows := permRowsForAgentIn(t, app, "alice", "mine")
	for _, r := range rows {
		if r["Who"] == "Scribe" {
			t.Errorf("another agent's decision is on this agent's page: %+v", r)
		}
	}
	if rowByWho(rows, "Wren") == nil {
		t.Error("this agent's own workspace decision is missing")
	}
}

// A decision that binds EVERY agent governs this one too. Dropping it would
// let the page lie by omission, which is the dangerous direction here.
func TestAFleetWideDecisionStillShowsOnAnAgentsPage(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	if _, err := saveAgent(udb, AgentRecord{
		ID: "mine", Name: "Wren", Owner: "alice", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	// confirmtool binds every agent by construction: the flag is on the tool.
	if err := AdminPersistTempTool(udb, "alice", TempTool{
		Name: "fleet_wide_tool", CommandTemplate: "curl x", ConfirmInChat: true}); err != nil {
		t.Fatal(err)
	}
	if rowByWho(permRowsForAgentIn(t, app, "alice", "mine"), "fleet_wide_tool") == nil {
		t.Error("a tool that asks on every agent is hidden from this agent's page, which governs it")
	}
}

// Setting the workspace back to allowed must not delete the row. The state is
// a plain bool, so "allowed" and "never decided" are the same value, and the
// page used to list only the restricted ones: using the control removed it.
func TestAllowingTheWorkspaceKeepsTheRow(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	if _, err := saveAgent(udb, AgentRecord{
		ID: "mine", Name: "Wren", Owner: "alice", OrchestratorPrompt: "p",
		WorkspaceNoNetwork: true}); err != nil {
		t.Fatal(err)
	}
	set := func(value string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost,
			"/api/console/permissions/policy?id=workspace:mine:network&value="+value, nil)
		w := httptest.NewRecorder()
		app.handleConsolePermissionPolicy(w, asUser(r, "alice"))
		if w.Code != http.StatusOK && w.Code != http.StatusNoContent {
			t.Fatalf("policy %s: %d %s", value, w.Code, w.Body.String())
		}
	}
	set(PolicyAllow)
	row := rowByWho(permRowsForAgentIn(t, app, "alice", "mine"), "Wren")
	if row == nil {
		t.Fatal("allowing the workspace deleted the row, so the control removed itself")
	}
	if row["_policy"] != PolicyAllow {
		t.Errorf("the row does not read as allowed: %+v", row)
	}
	set(PolicyBlock)
	row = rowByWho(permRowsForAgentIn(t, app, "alice", "mine"), "Wren")
	if row == nil || row["_policy"] != PolicyBlock {
		t.Errorf("blocking it again did not take: %+v", row)
	}
}

// Every row lands under exactly one tab, and the tabs are the four questions
// somebody actually arrives with. A row with no kind would vanish from every
// tab except All, which is how a decision goes unreviewed.
func TestEveryRowIsFiledUnderATab(t *testing.T) {
	for id, want := range map[string]string{
		"confirmtool:post":           "tools",
		"autotool:a1:web_search":     "tools",
		"subaction:a1:workspace/run": "tools",
		"workspace:a1:network":       "workspace",
		"contact:+15550109999":       "access",
		"contactfor:a1:handle":       "access",
		"agent:target":               "delegation",
		"agentfor:a1:target":         "delegation",
		// A pending request carries no prefix. It files under requests and so
		// still shows on All, which is the tab the window opens on: something
		// blocking a run must not sit behind a tab nobody clicked.
		"7f3c9a2e-0000-0000-0000-000000000000": "requests",
	} {
		if got := permRowKind(id); got != want {
			t.Errorf("permRowKind(%q) = %q, want %q", id, got, want)
		}
	}
}

// The rows the handler serves actually carry it. A kind derived correctly and
// never attached is the same as no tabs at all.
func TestTheServedRowsCarryTheirTab(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	if _, err := saveAgent(udb, AgentRecord{
		ID: "mine", Name: "Wren", Owner: "alice", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	if err := AdminPersistTempTool(udb, "alice", TempTool{
		Name: "tabbed_tool", CommandTemplate: "curl x"}); err != nil {
		t.Fatal(err)
	}
	rows := permRowsForAgentIn(t, app, "alice", "mine")
	if len(rows) == 0 {
		t.Fatal("no rows to file")
	}
	seen := map[string]bool{}
	for _, r := range rows {
		k, _ := r["_kind"].(string)
		if k == "" {
			t.Errorf("a row carries no tab, so it shows only under All: %+v", r)
		}
		seen[k] = true
	}
	for _, want := range []string{"tools", "workspace"} {
		if !seen[want] {
			t.Errorf("no row filed under %q, so that tab opens empty", want)
		}
	}
}

// A decision made for THIS agent and one that binds every agent are different
// facts, set in different places, reaching different distances. Listed
// together the reader cannot tell which is which without decoding a row id,
// which is what made the surface hard to read.
func TestAgentScopedAndFleetWideDecisionsCanBeAskedForSeparately(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	if _, err := saveAgent(udb, AgentRecord{
		ID: "mine", Name: "Wren", Owner: "alice", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	// One of each: a contact this agent may message, and one every agent may.
	SetContactPolicy(RootDB, "alice", "mine", "+15550109999", PolicyAllow)
	SetContactPolicy(RootDB, "alice", "", "+15550100001", PolicyAllow)

	ask := func(scope string) []map[string]any {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet,
			"/api/console/permissions?agent=mine&kind=access&scope="+scope, nil)
		w := httptest.NewRecorder()
		app.handleConsolePermissions(w, asUser(r, "alice"))
		var rows []map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	for _, r := range ask("agent") {
		if permRowAgent(r["_id"].(string)) == "" {
			t.Errorf("a fleet-wide decision appeared under this agent's own: %+v", r)
		}
	}
	fleet := ask("fleet")
	if len(fleet) == 0 {
		t.Error("the fleet-wide band is empty, so a decision binding this agent is shown nowhere")
	}
	for _, r := range fleet {
		if permRowAgent(r["_id"].(string)) != "" {
			t.Errorf("an agent-scoped decision appeared under every-agent: %+v", r)
		}
	}
	// Unscoped still returns both, which is what a cross-agent ledger wants.
	if len(ask("")) <= len(fleet) {
		t.Error("asking without a scope dropped rows instead of returning both bands")
	}
}

// A decision must be able to move BOTH ways, or the two scopes are not usable.
// Promote existed; without its opposite a decision can only ever widen, and an
// owner who granted something fleet-wide once has no way back except to remove
// it and remember to set it again on the one agent that needed it.
func TestADecisionCanBeWidenedAndBroughtBack(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	if _, err := saveAgent(udb, AgentRecord{
		ID: "mine", Name: "Wren", Owner: "alice", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	SetContactPolicy(RootDB, "alice", "mine", "+15550109999", PolicyAllow)

	post := func(path string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, nil)
		w := httptest.NewRecorder()
		switch {
		case strings.Contains(path, "/promote"):
			app.handleConsolePermissionPromote(w, asUser(r, "alice"))
		default:
			app.handleConsolePermissionNarrow(w, asUser(r, "alice"))
		}
		if w.Code != http.StatusNoContent && w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	// %2B, not a bare +: in a query string a plus decodes as a SPACE, so an
	// unencoded handle arrives as a different subject. The page substitutes
	// through encodeURIComponent, so this mirrors what it actually sends.
	post("/api/console/permissions/promote?id=contactfor:mine:%2B15550109999")
	if ContactPolicy(RootDB, "alice", "", "+15550109999") != PolicyAllow {
		t.Error("widening did not reach every agent")
	}
	// It MOVES. Two records for one decision would leave the wider one binding
	// everything while the page showed the narrow one as the answer.
	//
	// Asked of the SCOPED listing, not ContactPolicy: that falls back to the
	// fleet value when an agent has none of its own, so it cannot tell "has
	// its own" from "inherits", which is exactly the distinction under test.
	scoped := func(scope string) int {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet,
			"/api/console/permissions?agent=mine&kind=access&scope="+scope, nil)
		w := httptest.NewRecorder()
		app.handleConsolePermissions(w, asUser(r, "alice"))
		var rows []map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		return len(rows)
	}
	if scoped("agent") != 0 {
		t.Error("the agent-scoped record survived the widening, so one decision is stored twice")
	}
	if scoped("fleet") == 0 {
		t.Error("the widened decision is listed nowhere")
	}
	post("/api/console/permissions/narrow?id=contact:%2B15550109999&agent=mine")
	if ContactPolicy(RootDB, "alice", "mine", "+15550109999") != PolicyAllow {
		t.Error("narrowing did not land on the agent")
	}
	if scoped("fleet") != 0 {
		t.Error("the fleet-wide record survived the narrowing, so other agents still hold it")
	}
	if scoped("agent") == 0 {
		t.Error("the narrowed decision is not listed as this agent's own")
	}
}

// Narrowing something that is already one agent's would be a no-op that reads
// as a move, so it is refused rather than accepted quietly.
func TestNarrowingRefusesWhatIsNotFleetWide(t *testing.T) {
	app, _, _ := newTestOrchestrate(t)
	pinRootDB(t)
	r := httptest.NewRequest(http.MethodPost,
		"/api/console/permissions/narrow?id=contactfor:mine:+15550109999&agent=mine", nil)
	w := httptest.NewRecorder()
	app.handleConsolePermissionNarrow(w, asUser(r, "alice"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d %s", w.Code, w.Body.String())
	}
}

// Granting CREATES a decision. Every other control acts on one that already
// exists, so a decision could only be born from something requesting it, and
// two agents could not each be given their own from this page.
func TestGrantingCreatesADecisionForOneAgent(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	for _, id := range []string{"agent_a", "agent_b"} {
		if _, err := saveAgent(udb, AgentRecord{
			ID: id, Name: id, Owner: "alice", OrchestratorPrompt: "p"}); err != nil {
			t.Fatal(err)
		}
	}
	grant := func(agentID, kind, body string, want int) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost,
			"/api/console/permissions/grant?kind="+kind+"&agent="+agentID, strings.NewReader(body))
		w := httptest.NewRecorder()
		app.handleConsolePermissionGrant(w, asUser(r, "alice"))
		if w.Code != want {
			t.Fatalf("grant %s %s: %d %s", agentID, kind, w.Code, w.Body.String())
		}
	}
	// The thing the question was about: two agents, each with their own, and
	// they do not have to agree.
	grant("agent_a", "contact", `{"subject":"ops@example.test","value":"allow"}`, http.StatusNoContent)
	grant("agent_b", "contact", `{"subject":"ops@example.test","value":"block"}`, http.StatusNoContent)
	if got := ContactPolicy(RootDB, "alice", "agent_a", "ops@example.test"); got != PolicyAllow {
		t.Errorf("agent_a = %q, want allow", got)
	}
	if got := ContactPolicy(RootDB, "alice", "agent_b", "ops@example.test"); got != PolicyBlock {
		t.Errorf("agent_b = %q, want block", got)
	}
	// Per agent, never fleet-wide: binding everything is a deliberate second
	// step, not something granting does on the way in.
	if ContactPolicy(RootDB, "alice", "", "never_granted@example.test") == PolicyAllow {
		t.Error("granting reached every agent")
	}
	// Delegation, the same shape.
	grant("agent_a", "agent", `{"subject":"agent_b","value":"allow"}`, http.StatusNoContent)
	if got := DelegationPolicy(RootDB, "alice", "agent_a", "agent_b"); got != PolicyAllow {
		t.Errorf("delegation = %q, want allow", got)
	}
}

// What a grant refuses. A policy the gate does not know reads as SOME state on
// the page and behaves as none; an agent dispatching to itself is not a
// decision anybody wants stored.
func TestGrantingRefusesWhatItCannotStore(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	if _, err := saveAgent(udb, AgentRecord{
		ID: "agent_a", Name: "A", Owner: "alice", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		kind, body, why string
	}{
		{"contact", `{"subject":"ops@example.test","value":"sometimes"}`, "an unknown policy"},
		{"contact", `{"subject":"","value":"allow"}`, "nothing named"},
		{"agent", `{"subject":"agent_a","value":"allow"}`, "dispatching to itself"},
		{"nonsense", `{"subject":"x","value":"allow"}`, "an unknown kind"},
	} {
		r := httptest.NewRequest(http.MethodPost,
			"/api/console/permissions/grant?kind="+c.kind+"&agent=agent_a", strings.NewReader(c.body))
		w := httptest.NewRecorder()
		app.handleConsolePermissionGrant(w, asUser(r, "alice"))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d %s", c.why, w.Code, w.Body.String())
		}
	}
}

// Two layers, and the second cannot fire without the first. A decision about
// an agent the dispatch policy does not reach would leave a row saying "always
// allow" about a call that never happens.
func TestADecisionAboutAnUnreachableAgentIsRefused(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	// dispatchNone is the kill switch: it reaches nothing.
	caller := AgentRecord{ID: "caller", Name: "Caller", Owner: "alice",
		OrchestratorPrompt: "p", DispatchMode: string(dispatchNone)}
	if _, err := saveAgent(udb, caller); err != nil {
		t.Fatal(err)
	}
	if _, err := saveAgent(udb, AgentRecord{
		ID: "target", Name: "Target", Owner: "alice", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost,
		"/api/console/permissions/grant?kind=agent&agent=caller",
		strings.NewReader(`{"subject":"target","value":"allow"}`))
	w := httptest.NewRecorder()
	app.handleConsolePermissionGrant(w, asUser(r, "alice"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for a decision that could never fire, got %d %s", w.Code, w.Body.String())
	}
	// And it says WHY, and what to do: a refusal that only says no leaves
	// somebody clicking the same button again.
	body := w.Body.String()
	for _, want := range []string{"Target", "dispatch policy", "Widen"} {
		if !strings.Contains(body, want) {
			t.Errorf("the refusal does not mention %q: %s", want, body)
		}
	}
	// Reachable, and the same grant lands.
	caller.DispatchMode = string(dispatchAll)
	if _, err := saveAgent(udb, caller); err != nil {
		t.Fatal(err)
	}
	r2 := httptest.NewRequest(http.MethodPost,
		"/api/console/permissions/grant?kind=agent&agent=caller",
		strings.NewReader(`{"subject":"target","value":"allow"}`))
	w2 := httptest.NewRecorder()
	app.handleConsolePermissionGrant(w2, asUser(r2, "alice"))
	if w2.Code != http.StatusNoContent {
		t.Fatalf("a reachable target was refused: %d %s", w2.Code, w2.Body.String())
	}
}
