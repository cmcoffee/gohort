package servitor

// A system is never newly shared with everyone. Whoever it is shared with runs
// it as the owner, so Everyone would hand every account the owner's access to
// the machine. One shared with everyone before stays so until its owner takes
// it out, and then it cannot be put back.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestASystemIsNeverNewlySharedWithEveryone(t *testing.T) {
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
		routed(app)(w, reqAs(t, http.MethodPost, "/api/appliances", "alice", body))
		if w.Code != http.StatusOK {
			t.Fatalf("save answered %d: %s", w.Code, w.Body.String())
		}
		var a Appliance
		json.Unmarshal(w.Body.Bytes(), &a)
		return a
	}
	box := func(id, extra string) string {
		return `{"id":"` + id + `","type":"ssh","name":"web01","host":"web01.example.com"` + extra + `}`
	}

	// Neither Everyone in the list nor the old flag shares a new one, or an
	// existing one, with everyone. The names beside it still count.
	a := save(box("", `,"allowed_users":["*","bob"]`))
	if a.Shared || strings.Join(a.AllowedUsers, ",") != "bob" || app.reaches("dana", a.ID) || !app.reaches("bob", a.ID) {
		t.Errorf("Everyone in the list shared it: shared %v, list %v", a.Shared, a.AllowedUsers)
	}
	if a = save(box(a.ID, `,"shared":true`)); a.Shared || app.reaches("dana", a.ID) {
		t.Error("the old shared:true shared it with everyone")
	}

	// One shared with everyone before keeps it while its list keeps Everyone.
	stored := a
	stored.Shared = true
	UserDB(app.DB, "alice").Set(applianceTable, a.ID, stored)
	app.setApplianceShared(a.ID, "alice", true)
	if a = save(box(a.ID, `,"allowed_users":["*","bob"]`)); !a.Shared || !app.reaches("dana", a.ID) {
		t.Error("a save that kept Everyone took it away")
	}
	// Taken out, it is gone, and putting it back does nothing.
	if a = save(box(a.ID, `,"allowed_users":["bob"]`)); a.Shared || app.reaches("dana", a.ID) {
		t.Error("taking Everyone out left it shared with everyone")
	}
	if a = save(box(a.ID, `,"allowed_users":["*","bob"]`)); a.Shared || app.reaches("dana", a.ID) {
		t.Error("Everyone went back in")
	}

	// The picker does not offer it.
	w := httptest.NewRecorder()
	routed(app)(w, reqAs(t, http.MethodGet, "/api/user-candidates?groups=1&everyone=1", "alice", ""))
	if strings.Contains(w.Body.String(), `"value":"*"`) {
		t.Errorf("the picker offers Everyone: %s", w.Body.String())
	}
}
