package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A move one app would refuse is refused on the FIRST app's call, before
// anything moves: moves run one app at a time, and a refusal met only on a
// later app's turn left the earlier ones moved and the rest not.
func TestAMoveThatWouldClashMovesNothing(t *testing.T) {
	prevRoot, prevAuth := RootDB, AuthDB
	adb := &DBase{Store: kvlite.MemStore()}
	RootDB = &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { RootDB, AuthDB = prevRoot, prevAuth })
	for _, u := range []string{"bob", "carol"} {
		AuthSetUser(adb, u, "pw", false)
		if err := AdminPersistTempTool(RootDB, u, TempTool{Name: "weather", Description: "d", CommandTemplate: "echo w"}); err != nil {
			t.Fatal(err)
		}
	}
	// Whichever app the call names, the clash in another stops it.
	app := ""
	for _, h := range RegisteredUserDataHandlers() {
		if h.AppName() != "tools, skills and collections" {
			app = h.AppName()
			break
		}
	}
	if app == "" {
		app = "tools, skills and collections"
	}
	a := &AdminApp{db: adb}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/users/bob/data-action",
		strings.NewReader(`{"app":"`+app+`","action":"reassign","target":"carol"}`))
	// The handler alone, past the admin gate: what is under test is the move.
	admit := jsonapi.Authorize(func(http.ResponseWriter, *http.Request) bool { return true })
	jsonapi.Post(adb, a.handleUserDataAction, admit)(w, r)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "nothing was moved") || !strings.Contains(w.Body.String(), "weather") {
		t.Fatalf("%s: %d %s", app, w.Code, w.Body.String())
	}
	if got := LoadPersistentTempTools(RootDB, "bob"); len(got) != 1 {
		t.Error("bob's tool moved anyway")
	}
}
