package servitor

// "Clear Memory" must leave the profile cleared.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestClearMemoryDoesNotResurrectTheProfile(t *testing.T) {
	prev := AuthDB
	auth := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(auth, "alice", "pw", false)
	AuthDB = func() Database { return auth }
	t.Cleanup(func() { AuthDB = prev })

	app := &Servitor{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	udb := UserDB(app.DB, "alice")
	for _, typ := range []string{"repo", "ssh"} {
		udb.Set(applianceTable, "a-"+typ, Appliance{
			ID: "a-" + typ, Name: "host-a", Type: typ, Owner: "alice",
			RepoURL: "https://host-a/org/repo.git", RepoFiles: 3, RepoCloned: "2026-01-01T00:00:00Z",
			Profile: "# old profile", Scanned: "2026-01-01T00:00:00Z",
		})
		w := httptest.NewRecorder()
		app.handleMemoryClear(w, reqAs(t, http.MethodPost, "/api/memory/clear", "alice", `{"appliance_id":"a-`+typ+`"}`))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: clear returned %d: %s", typ, w.Code, w.Body.String())
		}
		var got Appliance
		udb.Get(applianceTable, "a-"+typ, &got)
		if got.Profile != "" || got.Scanned != "" {
			t.Errorf("%s: the profile came back after Clear Memory: %q", typ, got.Profile)
		}
		if typ == "repo" && (got.RepoFiles != 0 || got.RepoCloned != "") {
			t.Errorf("repo: clone bookkeeping not reset: %+v", got)
		}
		if got.RepoURL == "" {
			t.Errorf("%s: connection settings must survive a clear", typ)
		}
	}
}
