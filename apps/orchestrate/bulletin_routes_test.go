package orchestrate

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The bulletin board, ask-owner and run-cancel routes take the methods named
// here and no other, refuse a caller who is not signed in, and find only the
// caller's own: a board is in its owner's store, a run belongs to whoever
// started it.
//
// Only refusals and reads are sent, so nothing posts, notifies or cancels.
func TestBulletinAskOwnerAndCancelRoutesKeepTheirMethodsAndOwners(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	prevRoot, prevBase := RootDB, orchestrateBaseDB
	RootDB, orchestrateBaseDB = root, root
	t.Cleanup(func() { RootDB, orchestrateBaseDB = prevRoot, prevBase })
	signedUp(t, "alice", "bob")
	if err := saveBulletin(UserDB(root, "alice"), bulletinBoard{Name: "news", Desc: "d"}); err != nil {
		t.Fatal(err)
	}
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
		"/api/bulletins/news":           "GET POST PUT DELETE",
		"/api/bulletins/news/post":      "POST",
		"/api/bulletins/news/clear":     "POST",
		"/api/bulletins/news/followers": "GET POST",
		"/api/bulletins/news/posters":   "GET POST",
		"/api/ask-owner":                "POST",
		"/api/runs/r1/cancel":           "POST",
	}
	for path, methods := range takes {
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
		}
	}
	// Another user's board is not there for them.
	for _, path := range []string{"/api/bulletins/news", "/api/bulletins/news/followers", "/api/bulletins/news/posters"} {
		if w := do("bob", "GET", path, ""); w.Code != http.StatusNotFound {
			t.Errorf("bob, GET %s answered %d, want 404", path, w.Code)
		}
		if w := do("alice", "GET", path, ""); w.Code != http.StatusOK {
			t.Errorf("alice, GET %s answered %d %s", path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/bulletins/news/post", "/api/bulletins/news/clear"} {
		if w := do("bob", "POST", path, `{"text":"mine now"}`); w.Code != http.StatusNotFound {
			t.Errorf("bob, POST %s answered %d, want 404", path, w.Code)
		}
	}
	if w := do("bob", "POST", "/api/ask-owner?agent_id=nope", `{"request":"hi"}`); w.Code != http.StatusNotFound {
		t.Errorf("ask-owner for an agent bob cannot open answered %d", w.Code)
	}
	if w := do("bob", "POST", "/api/runs/r1/cancel", ""); w.Code != http.StatusNotFound {
		t.Errorf("cancelling a run that is not bob's answered %d", w.Code)
	}
}
