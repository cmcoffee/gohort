package orchestrate

// Moving an agent between top level and sub-agent keeps the record (and so its
// sessions and knowledge) and checks where it is going.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func moveTestAgent(t *testing.T, udb Database, a AgentRecord) AgentRecord {
	t.Helper()
	a.Owner = "alice"
	if a.OrchestratorPrompt == "" {
		a.OrchestratorPrompt = "you are a helper"
	}
	saved, err := saveAgent(udb, a)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func moveAgentReq(t *testing.T, app *OrchestrateApp, udb Database, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/agents/"+id+"/move", bytes.NewBufferString(body))
	app.handleAgentMove(w, r, "alice", udb, id)
	return w
}

func TestMoveAgentUnderAParentAndBackOut(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	parent := moveTestAgent(t, udb, AgentRecord{ID: "p1", Name: "Planner", DispatchMode: dispatchOnly, AllowedDispatchTargets: []string{"other"}})
	moveTestAgent(t, udb, AgentRecord{ID: "other", Name: "Other"})
	child := moveTestAgent(t, udb, AgentRecord{ID: "c1", Name: "Scout", ShowOnDashboard: true})

	if w := moveAgentReq(t, app, udb, child.ID, `{"owned_by":"p1"}`); w.Code != http.StatusOK {
		t.Fatalf("move under: %d %s", w.Code, w.Body.String())
	}
	got, _ := loadAgent(udb, child.ID)
	if got.OwnedBy != parent.ID || !got.Hidden || got.ShowOnDashboard {
		t.Fatalf("after moving under: owned_by=%q hidden=%v card=%v, want p1/true/false", got.OwnedBy, got.Hidden, got.ShowOnDashboard)
	}

	w := moveAgentReq(t, app, udb, child.ID, `{"owned_by":""}`)
	if w.Code != http.StatusOK {
		t.Fatalf("move out: %d %s", w.Code, w.Body.String())
	}
	got, _ = loadAgent(udb, child.ID)
	if got.OwnedBy != "" || got.Hidden {
		t.Fatalf("after moving out: owned_by=%q hidden=%v, want top-level and listed", got.OwnedBy, got.Hidden)
	}
	// The parent called it by ownership and calls from a list: it must still
	// be able to call it, so the move puts it on the list.
	p, _ := loadAgent(udb, parent.ID)
	if !dispatchListContains(p, child.ID) {
		t.Fatalf("parent's dispatch list %v lost the moved-out agent", p.AllowedDispatchTargets)
	}
	if !strings.Contains(w.Body.String(), "dispatch list") {
		t.Errorf("the move did not say it changed the parent's list: %s", w.Body.String())
	}
}

// A list whose every entry is gone reads as Allow all. Adding the moved agent
// to it would narrow the parent to that one agent.
func TestMoveOutLeavesAStaleListAlone(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	moveTestAgent(t, udb, AgentRecord{ID: "p1", Name: "Planner", DispatchMode: dispatchOnly, AllowedDispatchTargets: []string{"deleted-long-ago"}})
	moveTestAgent(t, udb, AgentRecord{ID: "c1", Name: "Scout", OwnedBy: "p1"})
	if w := moveAgentReq(t, app, udb, "c1", `{"owned_by":""}`); w.Code != http.StatusOK {
		t.Fatalf("move out: %d %s", w.Code, w.Body.String())
	}
	p, _ := loadAgent(udb, "p1")
	if dispatchListContains(p, "c1") {
		t.Fatalf("a stale list (healed to Allow all) was narrowed to the moved agent: %v", p.AllowedDispatchTargets)
	}
}

func TestMoveAgentRefusals(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	moveTestAgent(t, udb, AgentRecord{ID: "p1", Name: "Planner"})
	moveTestAgent(t, udb, AgentRecord{ID: "kid", Name: "Kid", OwnedBy: "p1"})
	moveTestAgent(t, udb, AgentRecord{ID: "p2", Name: "Second"})
	moveTestAgent(t, udb, AgentRecord{ID: "shared", Name: "Shared", AllowedUsers: []string{"bob"}})
	cases := []struct{ name, id, body, want string }{
		{"has its own sub-agents", "p1", `{"owned_by":"p2"}`, "sub-agent"},
		{"under a sub-agent", "p2", `{"owned_by":"kid"}`, "top-level agents"},
		{"under itself", "p2", `{"owned_by":"p2"}`, "top-level agents"},
		{"shared with someone", "shared", `{"owned_by":"p2"}`, "Share tab"},
		{"already top-level", "p2", `{"owned_by":""}`, "already"},
		{"a built-in", "seed-builder", `{"owned_by":"p2"}`, "built-in"},
	}
	for _, c := range cases {
		w := moveAgentReq(t, app, udb, c.id, c.body)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("%s: got %d %q, want 400 mentioning %q", c.name, w.Code, strings.TrimSpace(w.Body.String()), c.want)
		}
	}
}

// Every save path, the agent tools included, sets OwnedBy without the move's
// checks, so the loop guard lives in the save itself.
func TestSaveRefusesAnOwnershipLoop(t *testing.T) {
	_, udb, _ := newTestOrchestrate(t)
	moveTestAgent(t, udb, AgentRecord{ID: "a", Name: "A"})
	moveTestAgent(t, udb, AgentRecord{ID: "b", Name: "B", OwnedBy: "a"})
	a, _ := loadAgent(udb, "a")
	a.OwnedBy = "b"
	if _, err := saveAgent(udb, a); err == nil {
		t.Fatal("a owned by its own sub-agent saved")
	}
	a.OwnedBy = "a"
	if _, err := saveAgent(udb, a); err == nil {
		t.Fatal("an agent owning itself saved")
	}
}

// PATCH and the whole-record POST used to re-parent with no checks at all.
func TestEditPathsDoNotReparent(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	moveTestAgent(t, udb, AgentRecord{ID: "p1", Name: "Planner"})
	moveTestAgent(t, udb, AgentRecord{ID: "c1", Name: "Scout"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPatch, "/api/agents?id=c1", bytes.NewBufferString(`{"owned_by":"p1"}`))
	app.patchAgent(w, r, udb, "alice", "")
	if w.Code == http.StatusOK {
		t.Error("PATCH changed owned_by")
	}
	if got, _ := loadAgent(udb, "c1"); got.OwnedBy != "" {
		t.Fatalf("PATCH re-parented: owned_by=%q", got.OwnedBy)
	}
}
