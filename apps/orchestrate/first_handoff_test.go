package orchestrate

// The Access page says, for an agent whose tools are narrowed, which of the
// agents it can hand work to will ask the owner the first time, and lets them
// answer ahead of time. "Allow all" on the dispatch setting read as though
// nothing would ask, and a group chat then met a refusal no setting explained.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func reachRow(rows []map[string]any, name string) map[string]any {
	for _, r := range rows {
		if r["name"] == name {
			return r
		}
	}
	return nil
}

func TestTheAccessPageSaysWhichHandoffsAskFirst(t *testing.T) {
	app, udb := accessFixture(t, AgentRecord{ID: "a-wiwee", Name: "Wiwee", AllowedTools: []string{"workspace"}})
	for _, a := range []AgentRecord{
		{ID: "a-scout", Name: "Scout", Owner: "alice", OrchestratorPrompt: "p"},
		{ID: "a-child", Name: "Helper", Owner: "alice", OrchestratorPrompt: "p", OwnedBy: "a-wiwee"},
	} {
		if _, err := saveAgent(udb, a); err != nil {
			t.Fatal(err)
		}
	}
	rows := accessRowsFor(t, app, "a-wiwee", "reach")
	scout := reachRow(rows, "Scout")
	if scout == nil || scout["first_handoff"] != "Asks you the first time" || scout["_asks"] != true {
		t.Fatalf("a target not yet approved should read as asking: %+v", scout)
	}
	if child := reachRow(rows, "Helper"); child == nil || child["first_handoff"] != "Runs: its own sub-agent" {
		t.Errorf("its own sub-agent never asks: %+v", child)
	}

	post := func(allow string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/agent-access/handoff?agent=a-wiwee&target=a-scout&allow="+allow, nil)
		w := httptest.NewRecorder()
		app.handleAgentAccessHandoff(w, asUser(r, "alice"))
		if w.Code != http.StatusNoContent {
			t.Fatalf("handoff %s answered %d %s", allow, w.Code, w.Body.String())
		}
	}
	wiwee, _ := loadAgent(udb, "a-wiwee")
	scoutRec, _ := loadAgent(udb, "a-scout")
	turn := &chatTurn{udb: udb, agent: wiwee}

	post("1")
	if scout = reachRow(accessRowsFor(t, app, "a-wiwee", "reach"), "Scout"); scout["first_handoff"] != "Runs: you approved it" || scout["_granted"] != true {
		t.Errorf("approved here, the row should say so: %+v", scout)
	}
	if !turn.dispatchEdgeApproved(scoutRec) {
		t.Error("approving on the page did not let the handoff run")
	}
	post("0")
	if turn.dispatchEdgeApproved(scoutRec) {
		t.Error("taking the approval back left the handoff running")
	}

	// An agent never narrowed has nothing to say here.
	app2, udb2 := accessFixture(t, AgentRecord{ID: "a-open", Name: "Open"})
	if _, err := saveAgent(udb2, AgentRecord{ID: "a-scout", Name: "Scout", Owner: "alice", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	if row := reachRow(accessRowsFor(t, app2, "a-open", "reach"), "Scout"); row == nil || row["first_handoff"] != nil {
		t.Errorf("an agent with no narrowed list should show no first-handoff note: %+v", row)
	}
}

// The page draws the column and both answers.
func TestTheAccessPageOffersTheFirstHandoffAnswers(t *testing.T) {
	app, _ := accessFixture(t, AgentRecord{ID: "a-wiwee", Name: "Wiwee", AllowedTools: []string{"workspace"}})
	r := httptest.NewRequest(http.MethodGet, "/agent/a-wiwee/access", nil)
	w := httptest.NewRecorder()
	app.handleAgentPage(w, asUser(r, "alice"))
	body := w.Body.String()
	for _, want := range []string{"first_handoff", "/api/agent-access/handoff?agent=a-wiwee", "Allow all does not answer it"} {
		if !strings.Contains(body, want) {
			t.Errorf("the access page is missing %q", want)
		}
	}
}
