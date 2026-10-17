package orchestrate

// The Knowledge dialog follows bulletins beside the collections, and both
// pickers save only their own field: a whole-record save from either would
// write back the other's list as it stood when the dialog opened.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBulletinsAndCollectionsSaveWithoutUndoingEachOther(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	ag, err := saveAgent(udb, AgentRecord{Name: "Reader", Owner: user, OrchestratorPrompt: "p",
		AttachedCollections: []string{"c1"}})
	if err != nil {
		t.Fatal(err)
	}
	patch := func(body string) int {
		r := httptest.NewRequest(http.MethodPatch, "/api/agents/"+ag.ID, strings.NewReader(body))
		w := httptest.NewRecorder()
		app.handleAgentOne(w, asUser(r, user))
		return w.Code
	}
	if code := patch(`{"bulletins": ["news"]}`); code != http.StatusOK {
		t.Fatalf("following a board: %d", code)
	}
	got, _ := loadAgent(udb, ag.ID)
	if len(got.Bulletins) != 1 || got.Bulletins[0] != "news" || len(got.AttachedCollections) != 1 {
		t.Errorf("after following a board: bulletins %v, collections %v", got.Bulletins, got.AttachedCollections)
	}
	if code := patch(`{"attached_collections": ["c1", "c2"]}`); code != http.StatusOK {
		t.Fatalf("attaching a collection: %d", code)
	}
	got, _ = loadAgent(udb, ag.ID)
	if len(got.Bulletins) != 1 || len(got.AttachedCollections) != 2 {
		t.Errorf("attaching a collection undid the board: bulletins %v, collections %v", got.Bulletins, got.AttachedCollections)
	}
}
