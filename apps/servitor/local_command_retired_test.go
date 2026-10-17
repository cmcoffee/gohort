package servitor

// Local command systems are retired in favour of file stores: no new one is
// made, an existing one keeps working, and nothing that only works over SSH is
// tried on one.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestNoNewLocalCommandSystemIsMade(t *testing.T) {
	prevAuth, prevRoot := AuthDB, RootDB
	auth := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(auth, "root", "pw", true)
	AuthDB = func() Database { return auth }
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot })

	app := &Servitor{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	udb := UserDB(app.DB, "root")
	udb.Set(applianceTable, "c1", Appliance{ID: "c1", Name: "logs", Type: "command", Command: "logtool", Owner: "root"})
	udb.Set(applianceTable, "s1", Appliance{ID: "s1", Name: "web", Type: "ssh", Host: "web.example.test", Owner: "root"})
	post := func(body string) (int, string) {
		w := httptest.NewRecorder()
		app.handleAppliances(w, reqAs(t, http.MethodPost, "/api/appliances", "root", body))
		return w.Code, w.Body.String()
	}

	if code, body := post(`{"type":"command","name":"new","command":"logtool"}`); code != http.StatusBadRequest || !strings.Contains(body, "file store") {
		t.Errorf("a new local command system was made, even by an admin: %d %s", code, body)
	}
	if code, body := post(`{"id":"s1","type":"command","name":"web","command":"logtool"}`); code != http.StatusBadRequest || !strings.Contains(body, "file store") {
		t.Errorf("an ssh system was turned into a local command one: %d %s", code, body)
	}
	if code, body := post(`{"id":"c1","type":"command","name":"logs renamed","command":"logtool"}`); code != http.StatusOK {
		t.Errorf("an existing local command system could not be edited: %d %s", code, body)
	}
}

// Minted tools and watches dial the system's host. A local command system has
// none, so they dialled port 22 on this machine as root.
func TestNothingSSHOnlyIsTriedOnALocalCommandSystem(t *testing.T) {
	local := Appliance{ID: "c1", Name: "logs", Type: "command", Command: "logtool"}
	if _, err := DispatchApplianceTool(context.Background(), &DBase{Store: kvlite.MemStore()},
		ApplianceDispatch{Appliance: local, ToolName: "parse_run", UserID: "root"}); err == nil || !strings.Contains(err.Error(), "local command system") {
		t.Errorf("a minted tool was tried on a local command system: %v", err)
	}
	if _, err := MintApplianceTool(context.Background(), nil, local, nil, "parse a run", "a1", "root"); err == nil || !strings.Contains(err.Error(), "local command system") {
		t.Errorf("a tool was minted for a local command system: %v", err)
	}
	if _, err := execWatch(local, "root", "uptime"); err == nil || !strings.Contains(err.Error(), "local command system") {
		t.Errorf("a watch was run on a local command system: %v", err)
	}
	for _, a := range []Appliance{{Type: "ssh"}, {Type: "command", PeerName: "box"}} {
		if err := sshOnlyRefusal(a, "run a watch"); err != nil {
			t.Errorf("%+v refused: %v", a, err)
		}
	}
}
