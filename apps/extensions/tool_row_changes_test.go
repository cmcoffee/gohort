package extensions

// A tool row's own changes on Extensions › Tools: the category it is filed
// under and who it is handed to, each read back from the store, and a body
// that is not JSON refused rather than read as a blank category.

import (
	"net/http/httptest"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestAToolRowsCategoryAndShareListAreStored(t *testing.T) {
	app, req := authedExtensions(t)
	if err := AdminPersistTempTool(RootDB, "alice", TempTool{Name: "filed", Description: "d", CommandTemplate: "echo x"}); err != nil {
		t.Fatal(err)
	}
	post := func(path, body string) int {
		t.Helper()
		r := req("POST", path)
		r.Body = readCloser(body)
		w := httptest.NewRecorder()
		serve(app)(w, r)
		return w.Code
	}
	if code := post("/api/tools?action=set_category&name=filed", `{"category":" Reports "}`); code != 204 {
		t.Fatalf("set_category answered %d", code)
	}
	if row, _ := UserToolByName(AuthDB(), "alice", "filed"); row.Tool.Category != "Reports" {
		t.Fatalf("category stored as %q", row.Tool.Category)
	}
	if code := post("/api/tools?action=set_category&name=filed", `not json`); code != 400 {
		t.Errorf("a body that is not JSON answered %d", code)
	}
	if row, _ := UserToolByName(AuthDB(), "alice", "filed"); row.Tool.Category != "Reports" {
		t.Errorf("a refused body changed the category to %q", row.Tool.Category)
	}

	if code := post("/api/tools?action=share&name=filed", `{"shared_with":["bob"]}`); code != 204 {
		t.Fatalf("share answered %d", code)
	}
	var shared []string
	for _, p := range LoadPersistentTempTools(AuthDB(), "alice") {
		if p.Tool.Name == "filed" {
			shared = p.SharedWith
		}
	}
	if len(shared) != 1 || shared[0] != "bob" {
		t.Fatalf("shared with %v, want [bob]", shared)
	}
}
