package orchestrate

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Every machine route takes the methods named here and no other, refuses a
// caller who is not signed in, and finds nothing for a stranger. Of the people
// it is shared with, a reader runs and copies it, an editor also changes it,
// and only the owner deletes, shares, publishes or attaches it.
//
// Nothing here runs the machine or calls a model.
func TestEveryMachineRouteTakesItsMethodsAndKeepsItsGates(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	prevRoot, prevBase := RootDB, orchestrateBaseDB
	RootDB, orchestrateBaseDB = root, root
	t.Cleanup(func() { RootDB, orchestrateBaseDB = prevRoot, prevBase })
	signedUp(t, "alice", "bob", "carol", "dave")
	SaveMachineDef(UserDB(root, "alice"), MachineDef{ID: "m-a", Owner: "alice", Name: "Triage",
		Phases:       []MachinePhase{{Name: "ask", Prompt: "ask them", Resident: true}, {Name: "answer", Prompt: "answer"}},
		AllowedUsers: []string{"bob", "carol"}, Editors: []string{"carol"}})
	root.Set(sharedMachinesTable, "m-a", "alice")
	h := routed(&OrchestrateApp{AppCore: AppCore{DB: root}})
	do := func(user, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if user != "" {
			r = withSession(r, user)
		}
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}

	takes := map[string]string{
		"":          "GET PUT DELETE",
		"editor":    "GET",
		"meta":      "GET POST PUT PATCH",
		"phases":    "GET POST PUT PATCH DELETE",
		"try":       "POST",
		"duplicate": "POST",
		"revise":    "POST",
		"undo":      "POST",
		"repair":    "POST",
		"move":      "POST",
		"agents":    "GET POST PUT",
		"share":     "GET POST",
		"publish":   "POST",
		"unpublish": "POST",
	}
	for action, methods := range takes {
		path := "/api/machines/m-a"
		if action != "" {
			path += "/" + action
		}
		for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
			if !strings.Contains(" "+methods+" ", " "+m+" ") {
				if w := do("alice", m, path, "{}"); w.Code != http.StatusMethodNotAllowed {
					t.Errorf("%s %s answered %d, want 405", m, path, w.Code)
				}
				continue
			}
			if w := do("", m, path, "{}"); w.Code != http.StatusUnauthorized {
				t.Errorf("signed out, %s %s answered %d, want 401", m, path, w.Code)
			}
			if w := do("dave", m, path, "{}"); w.Code != http.StatusNotFound {
				t.Errorf("dave, %s %s answered %d, want 404", m, path, w.Code)
			}
		}
	}
	for _, action := range []string{"suggest", "block?name=x", "graph", "export", "revisions", "runs", "runs/sessions"} {
		if w := do("", "GET", "/api/machines/m-a/"+action, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("signed out, GET %s answered %d, want 401", action, w.Code)
		}
		if w := do("dave", "GET", "/api/machines/m-a/"+action, ""); w.Code != http.StatusNotFound {
			t.Errorf("dave, GET %s answered %d, want 404", action, w.Code)
		}
	}

	edit := `{"name":"Triage","phases":[{"name":"ask","prompt":"changed","resident":true}]}`
	// The reader reads and does nothing else; the editor's own reads of the
	// form are edits too, as they always were.
	for _, path := range []string{"/api/machines/m-a", "/api/machines/m-a/export", "/api/machines/m-a/graph", "/api/machines/m-a/editor"} {
		if w := do("bob", "GET", path, ""); w.Code != http.StatusOK {
			t.Errorf("bob, a reader, GET %s answered %d %s", path, w.Code, w.Body.String())
		}
	}
	for _, c := range []struct{ method, path, body string }{
		{"PUT", "/api/machines/m-a", edit},
		{"GET", "/api/machines/m-a/meta", ""},
		{"POST", "/api/machines/m-a/meta", `{"description":"mine"}`},
		{"GET", "/api/machines/m-a/phases", ""},
		{"POST", "/api/machines/m-a/phases?name=ask", `{"prompt":"mine"}`},
		{"DELETE", "/api/machines/m-a/phases?name=ask", ""},
		{"POST", "/api/machines/m-a/move", `{"name":"answer","dir":"up"}`},
		{"POST", "/api/machines/m-a/repair?kind=problems", ""},
		{"POST", "/api/machines/m-a/undo", "{}"},
		{"POST", "/api/machines/m-a/suggest", "{}"},
	} {
		if w := do("bob", c.method, c.path, c.body); w.Code != http.StatusForbidden {
			t.Errorf("bob, a reader, %s %s answered %d, want 403", c.method, c.path, w.Code)
		}
	}
	if w := do("carol", "PUT", "/api/machines/m-a", edit); w.Code != http.StatusOK {
		t.Errorf("carol, an editor, could not save: %d %s", w.Code, w.Body.String())
	}
	for _, u := range []string{"bob", "carol"} {
		for _, c := range []struct{ method, path, body string }{
			{"DELETE", "/api/machines/m-a", ""},
			{"GET", "/api/machines/m-a/share", ""},
			{"POST", "/api/machines/m-a/share", `{"allowed_users":["dave"]}`},
			{"POST", "/api/machines/m-a/publish", ""},
			{"POST", "/api/machines/m-a/unpublish", ""},
			{"GET", "/api/machines/m-a/agents", ""},
			{"POST", "/api/machines/m-a/agents", `{"agents":[]}`},
		} {
			if w := do(u, c.method, c.path, c.body); w.Code != http.StatusForbidden {
				t.Errorf("%s %s %s answered %d, want 403", u, c.method, c.path, w.Code)
			}
		}
	}
	if def, ok := LoadMachineDef(UserDB(root, "alice"), "alice", "m-a"); !ok || len(def.AllowedUsers) != 2 || len(def.Phases) != 1 {
		t.Errorf("the machine or its share changed beyond the editor's save: %+v %v", def, ok)
	}
}
