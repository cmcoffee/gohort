package servitor

// A remote system runs commands through the deployment's peer link, so only
// an administrator makes one, and only for a system the peer offers.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestOnlyAnAdminPointsASystemAtAPeer(t *testing.T) {
	prevAuth, prevRoot := AuthDB, RootDB
	auth := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(auth, "alice", "pw", false)
	AuthSetUser(auth, "root", "pw", true)
	AuthDB = func() Database { return auth }
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot })
	RootDB.Set("remote_peers", "den", RemotePeer{Name: "den", BaseURL: "https://den.example",
		Investigable: []PeerInvestigable{{ID: "web-1", Name: "web"}}})

	app := &Servitor{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	post := func(user, remoteID string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		app.handleAppliances(w, reqAs(t, http.MethodPost, "/api/appliances", user,
			`{"type":"remote","name":"web","peer_name":"den","remote_id":"`+remoteID+`"}`))
		return w
	}
	if w := post("alice", "web-1"); w.Code != http.StatusForbidden {
		t.Errorf("alice made a remote system: %d %s", w.Code, w.Body.String())
	}
	if w := post("root", "db-9"); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "does not offer") {
		t.Errorf("a system the peer does not offer: %d %s", w.Code, w.Body.String())
	}
	if w := post("root", "web-1"); w.Code != http.StatusOK {
		t.Errorf("the admin making an offered one: %d %s", w.Code, w.Body.String())
	}

	// One a non-admin made before the rule runs nothing.
	if _, err := peerExecFor(context.Background(), Appliance{Owner: "alice", PeerName: "den", RemoteID: "web-1"})("id"); err == nil || !strings.Contains(err.Error(), "not an administrator") {
		t.Errorf("a non-admin's older remote system ran: %v", err)
	}
}
