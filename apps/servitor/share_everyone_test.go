package servitor

// A system's sharing is one list: Everyone, people, or a team. It used to be a
// "Shared with all users" box above a separate list of names.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestEveryoneIsOneMoreChoiceInASystemsShareList(t *testing.T) {
	prev := AuthDB
	auth := &DBase{Store: kvlite.MemStore()}
	for _, u := range []string{"alice", "bob", "dana"} {
		AuthSetUser(auth, u, "pw", false)
	}
	AuthDB = func() Database { return auth }
	t.Cleanup(func() { AuthDB = prev })
	app := &Servitor{}
	app.DB = &DBase{Store: kvlite.MemStore()}

	save := func(body string) Appliance {
		t.Helper()
		w := httptest.NewRecorder()
		app.handleAppliances(w, reqAs(t, http.MethodPost, "/api/appliances", "alice", body))
		if w.Code != http.StatusOK {
			t.Fatalf("save answered %d: %s", w.Code, w.Body.String())
		}
		var a Appliance
		json.Unmarshal(w.Body.Bytes(), &a)
		return a
	}
	reaches := func(user, id string) bool {
		_, ok := app.systemsSharedWith(user)[id]
		return ok
	}

	a := save(`{"type":"ssh","name":"web01","host":"web01.example.com","allowed_users":["*","bob"]}`)
	if !a.Shared || strings.Join(a.AllowedUsers, ",") != "bob" {
		t.Errorf("Everyone is kept as the shared flag, the names beside it: %+v", a)
	}
	if !reaches("dana", a.ID) || !reaches("bob", a.ID) {
		t.Error("shared with Everyone, it does not reach everyone")
	}

	// Taking Everyone out of the list takes it back from dana.
	a = save(`{"id":"` + a.ID + `","type":"ssh","name":"web01","host":"web01.example.com","allowed_users":["bob"]}`)
	if a.Shared || reaches("dana", a.ID) || !reaches("bob", a.ID) {
		t.Errorf("narrowed to bob: shared %v, dana %v, bob %v", a.Shared, reaches("dana", a.ID), reaches("bob", a.ID))
	}

	// The picker offers Everyone first when it asks for it, and not otherwise.
	for q, want := range map[string]bool{"?groups=1&everyone=1": true, "?groups=1": false} {
		w := httptest.NewRecorder()
		app.handleUserCandidates(w, reqAs(t, http.MethodGet, "/api/user-candidates"+q, "alice", ""))
		if got := strings.HasPrefix(w.Body.String(), `[{"value":"*"`); got != want {
			t.Errorf("%s: Everyone offered first = %v, want %v: %s", q, got, want, w.Body.String())
		}
	}
}
