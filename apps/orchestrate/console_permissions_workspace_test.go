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
	if rec.WorkspaceNoNetwork {
		t.Error("allowing the workspace left the mark on the record")
	}
	if len(rec.DisabledToolActions) != 0 {
		t.Errorf("allowing the sub-action left it switched off: %v", rec.DisabledToolActions)
	}

	// And back again, so the control moves both ways.
	set("workspace:w3:network", PolicyBlock)
	set("subaction:w3:workspace/run", PolicyBlock)
	rec, _ = loadAgent(udb, "w3")
	if !rec.WorkspaceNoNetwork {
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
