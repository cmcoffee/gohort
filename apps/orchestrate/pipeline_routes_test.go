package orchestrate

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Every pipeline route takes the methods named here and no other, refuses a
// caller who is not signed in, and finds nothing for a stranger. Of the people
// it is shared with, a reader runs and copies it, an editor also changes it,
// and only the owner deletes, shares, publishes or attaches it.
//
// Nothing here runs the pipeline or calls a model.
func TestEveryPipelineRouteTakesItsMethodsAndKeepsItsGates(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	prevRoot, prevBase := RootDB, orchestrateBaseDB
	RootDB, orchestrateBaseDB = root, root
	t.Cleanup(func() { RootDB, orchestrateBaseDB = prevRoot, prevBase })
	signedUp(t, "alice", "bob", "carol", "dave")
	SavePipelineDef(UserDB(root, "alice"), PipelineDef{ID: "pl-a", Owner: "alice", Name: "Flow",
		Stages:       []PipelineStage{{Name: "a", Kind: StageWorker, Prompt: "say hi"}},
		AllowedUsers: []string{"bob", "carol"}, Editors: []string{"carol"}})
	root.Set(sharedPipelinesTable, "pl-a", "alice")
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
		"agents":    "GET POST PUT",
		"duplicate": "POST",
		"revise":    "POST",
		"undo":      "POST",
		"stages":    "GET POST PUT PATCH DELETE",
		"share":     "GET POST",
		"publish":   "POST",
		"unpublish": "POST",
		"run":       "POST",
	}
	for action, methods := range takes {
		path := "/api/pipelines/pl-a"
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
	for _, action := range []string{"block?name=x", "graph", "export", "revisions", "stream", "sessions", "cancel", "followups", "suggest", "sessions/s1", "reconnect/r1", "followup/f/r1"} {
		if w := do("", "GET", "/api/pipelines/pl-a/"+action, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("signed out, GET %s answered %d, want 401", action, w.Code)
		}
		if w := do("dave", "GET", "/api/pipelines/pl-a/"+action, ""); w.Code != http.StatusNotFound {
			t.Errorf("dave, GET %s answered %d, want 404", action, w.Code)
		}
	}

	edit := `{"name":"Flow","stages":[{"name":"a","kind":"worker","prompt":"changed"}]}`
	// The reader reads and does nothing else.
	for _, path := range []string{"/api/pipelines/pl-a", "/api/pipelines/pl-a/export", "/api/pipelines/pl-a/graph"} {
		if w := do("bob", "GET", path, ""); w.Code != http.StatusOK {
			t.Errorf("bob, a reader, GET %s answered %d %s", path, w.Code, w.Body.String())
		}
	}
	for _, c := range []struct{ method, path, body string }{
		{"PUT", "/api/pipelines/pl-a", edit},
		{"POST", "/api/pipelines/pl-a/stages?name=a", `{"prompt":"mine"}`},
		{"DELETE", "/api/pipelines/pl-a/stages?name=a", ""},
		{"POST", "/api/pipelines/pl-a/undo", "{}"},
	} {
		if w := do("bob", c.method, c.path, c.body); w.Code != http.StatusForbidden {
			t.Errorf("bob, a reader, %s %s answered %d, want 403", c.method, c.path, w.Code)
		}
	}
	// The editor edits, and the owner's own doors stay shut to both.
	if w := do("carol", "PUT", "/api/pipelines/pl-a", edit); w.Code != http.StatusOK {
		t.Errorf("carol, an editor, could not save: %d %s", w.Code, w.Body.String())
	}
	for _, u := range []string{"bob", "carol"} {
		for _, c := range []struct{ method, path, body string }{
			{"DELETE", "/api/pipelines/pl-a", ""},
			{"GET", "/api/pipelines/pl-a/share", ""},
			{"POST", "/api/pipelines/pl-a/share", `{"allowed_users":["dave"]}`},
			{"POST", "/api/pipelines/pl-a/publish", ""},
			{"POST", "/api/pipelines/pl-a/unpublish", ""},
			{"GET", "/api/pipelines/pl-a/agents", ""},
			{"POST", "/api/pipelines/pl-a/agents", `{"agents":[]}`},
		} {
			if w := do(u, c.method, c.path, c.body); w.Code != http.StatusForbidden {
				t.Errorf("%s %s %s answered %d, want 403", u, c.method, c.path, w.Code)
			}
		}
	}
	if def, ok := LoadPipelineDef(UserDB(root, "alice"), "alice", "pl-a"); !ok || len(def.AllowedUsers) != 2 {
		t.Errorf("the pipeline or its share changed: %+v %v", def, ok)
	}
	if w := do("alice", "GET", "/api/pipelines/pl-a/share", ""); w.Code != http.StatusOK {
		t.Errorf("alice, the owner, GET share answered %d %s", w.Code, w.Body.String())
	}
}
