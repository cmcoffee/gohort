package orchestrate

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A route whose body is optional takes a request with none, as its buttons
// send; one whose body is required refuses it. The body's type says which
// (jsonapi: *T is optional, T required), so a route moved from one to the
// other changes what its callers can send.
func TestOptionalBodiesAreOptional(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	prevRoot, prevBase := RootDB, orchestrateBaseDB
	RootDB, orchestrateBaseDB = root, root
	t.Cleanup(func() { RootDB, orchestrateBaseDB = prevRoot, prevBase })
	signedUp(t, "alice")
	udb := UserDB(root, "alice")
	if _, err := saveAgent(udb, AgentRecord{ID: "agent-a", Owner: "alice", Name: "Ada", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	SavePipelineDef(udb, PipelineDef{ID: "pl-a", Owner: "alice", Name: "Flow",
		Stages: []PipelineStage{{Name: "one", Prompt: "do it"}}})
	h := routed(&OrchestrateApp{AppCore: AppCore{DB: root}})
	do := func(path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h(w, withSession(httptest.NewRequest("POST", path, strings.NewReader(body)), "alice"))
		return w
	}
	for _, path := range []string{
		"/api/agents/agent-a/clone",
		"/api/pipelines/pl-a/publish",
		"/api/console/permissions/policy?id=agent:agent-a&value=ask",
	} {
		if w := do(path, ""); w.Code == http.StatusBadRequest || w.Code >= 500 {
			t.Errorf("POST %s with no body answered %d %s", path, w.Code, w.Body.String())
		}
		if w := do(path, `{"note":`); w.Code != http.StatusBadRequest {
			t.Errorf("POST %s with a malformed body answered %d", path, w.Code)
		}
	}
	if w := do("/api/approvals/answer", ""); w.Code != http.StatusBadRequest {
		t.Errorf("an approval answer with no body answered %d, want 400", w.Code)
	}
}
