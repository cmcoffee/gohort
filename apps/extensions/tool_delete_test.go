package extensions

// The break-glass control on Extensions › Tools: DELETE drops a tool from the
// owner's pool, and refuses a request that names none.

import (
	"net/http/httptest"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestDeletingAToolDropsItFromThePool(t *testing.T) {
	app, req := authedExtensions(t)
	if err := AdminPersistTempTool(RootDB, "alice", TempTool{Name: "misbehaves", Description: "d", CommandTemplate: "echo x"}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	app.handleUserTools(w, req("DELETE", "/api/tools?name=misbehaves"))
	if w.Code != 204 {
		t.Fatalf("delete answered %d %s", w.Code, w.Body.String())
	}
	if _, still := UserToolByName(AuthDB(), "alice", "misbehaves"); still {
		t.Error("the tool is still in the pool")
	}
	w = httptest.NewRecorder()
	app.handleUserTools(w, req("DELETE", "/api/tools"))
	if w.Code != 400 {
		t.Errorf("a delete naming no tool answered %d", w.Code)
	}
}
