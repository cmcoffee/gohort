package orchestrate

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A surface that hosts one shared agent about one thing lists only the
// conversations filed under it: Builder's mapping of one command must not show
// every other Builder conversation, nor another command's mapping.
func TestASessionListCanBeOnlyThoseFiledUnderAContext(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return root }
	t.Cleanup(func() { AuthDB = prev })
	AuthSetUser(root, "alice", "pw-alice-123", true)
	app := &OrchestrateApp{}
	app.DB = root.Bucket("orchestrate")
	udb := UserDB(app.DB, "alice")
	for _, s := range []ChatSession{
		{ID: "s-map-1", AgentID: "builder", Title: "mapping extract", AppContext: "filestore-map:intake/extract"},
		{ID: "s-map-2", AgentID: "builder", Title: "mapping kb", AppContext: "filestore-map:intake/kb"},
		{ID: "s-plain", AgentID: "builder", Title: "an ordinary Builder chat"},
	} {
		if _, err := saveChatSession(udb, s); err != nil {
			t.Fatal(err)
		}
	}
	list := func(only bool) string {
		r := httptest.NewRequest(http.MethodGet, "/x", nil)
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(root, "alice")})
		w := httptest.NewRecorder()
		if only {
			app.PublicHandleSessionListOnly(w, r, "builder", "filestore-map:intake/extract")
		} else {
			app.PublicHandleSessionListFor(w, r, "builder", "filestore-map:intake/extract")
		}
		return w.Body.String()
	}
	got := list(true)
	if !strings.Contains(got, "s-map-1") || strings.Contains(got, "s-map-2") || strings.Contains(got, "s-plain") {
		t.Errorf("only this command's mappings should be listed: %s", got)
	}
	// The existing filtered list is unchanged: it still includes unfiled ones.
	if got := list(false); !strings.Contains(got, "s-plain") || strings.Contains(got, "s-map-2") {
		t.Errorf("the ordinary filtered list changed: %s", got)
	}
}
