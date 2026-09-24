package extensions

// The two lists a user reads about versions: their own published tool says
// which version everybody else runs and whether their copy has moved on; the
// catalog says when a colleague's tool they took has an update, and Accept
// takes it.

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func rowsNamed(t *testing.T, body []byte, name string) map[string]any {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("decode: %v %s", err, body)
	}
	for _, r := range rows {
		if r["name"] == name {
			return r
		}
	}
	t.Fatalf("no row %s in %s", name, body)
	return nil
}

func TestAPublishedToolRowSaysItsCopyDiffers(t *testing.T) {
	app, req := authedExtensions(t)
	if err := AdminPersistTempTool(RootDB, "alice", TempTool{Name: "lookup", Description: "d", CommandTemplate: "echo one"}); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolShared(RootDB, "alice", "lookup", true); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	app.handleUserTools(w, req("GET", "/api/tools"))
	if r := rowsNamed(t, w.Body.Bytes(), "lookup"); r["release"] != "Published v1" || r["can_update"] == true {
		t.Fatalf("an unchanged published tool: %+v", r)
	}
	if err := AdminPersistTempTool(RootDB, "alice", TempTool{Name: "lookup", Description: "d", CommandTemplate: "echo two"}); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	app.handleUserTools(w, req("GET", "/api/tools"))
	r := rowsNamed(t, w.Body.Bytes(), "lookup")
	if r["can_update"] != true || r["differs"] != true || !strings.Contains(r["release"].(string), "your copy differs") {
		t.Fatalf("an edited published tool should offer Request update: %+v", r)
	}
	if d, _ := r["diff"].(string); !strings.Contains(d, "+ echo two") {
		t.Fatalf("the row's diff does not show the edit: %q", d)
	}

	// The owner withdraws it themselves.
	w = httptest.NewRecorder()
	app.handleUserTools(w, req("POST", "/api/tools?action=withdraw&name=lookup"))
	if w.Code/100 != 2 {
		t.Fatalf("withdraw: %d %s", w.Code, w.Body.String())
	}
	if p, _ := UserToolByName(RootDB, "alice", "lookup"); p.Shared {
		t.Fatal("withdraw left the tool published")
	}
}

func TestTheCatalogOffersAColleaguesUpdate(t *testing.T) {
	app, req := authedExtensions(t)
	if err := AdminPersistTempTool(RootDB, "lender", TempTool{Name: "wiki_read", Description: "d", CommandTemplate: "echo one"}); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolSharedWith(RootDB, "lender", "wiki_read", []string{"alice"}); err != nil {
		t.Fatal(err)
	}
	if err := SetGlobalToolAdopted(RootDB, "alice", "wiki_read", "lender", true); err != nil {
		t.Fatal(err)
	}
	if err := AdminPersistTempTool(RootDB, "lender", TempTool{Name: "wiki_read", Description: "d", CommandTemplate: "echo two"}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	app.handleGlobalTools(w, req("GET", "/api/global-tools"))
	r := rowsNamed(t, w.Body.Bytes(), "wiki_read")
	if r["update_available"] != true || !strings.Contains(r["diff"].(string), "+ echo two") {
		t.Fatalf("the colleague's edit should read as an update available: %+v", r)
	}
	w = httptest.NewRecorder()
	app.handleGlobalTools(w, req("POST", "/api/global-tools?name=wiki_read&owner=lender&adopt=true"))
	if w.Code/100 != 2 {
		t.Fatalf("accept: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	app.handleGlobalTools(w, req("GET", "/api/global-tools"))
	if r := rowsNamed(t, w.Body.Bytes(), "wiki_read"); r["update_available"] == true {
		t.Fatalf("accepting should clear the update: %+v", r)
	}
	for _, p := range AdoptedToolsFor(RootDB, "alice") {
		if p.Tool.Name == "wiki_read" && p.Tool.CommandTemplate != "echo two" {
			t.Fatalf("after accepting, alice still runs %q", p.Tool.CommandTemplate)
		}
	}
}
