package servitor

// Local command systems are retired in favour of file stores, and tool-backed
// services in favour of an agent's own tools: no new one of either is made, an
// existing one keeps working, and nothing that only works over SSH is tried on
// a local command system.

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

	udb.Set(applianceTable, "t1", Appliance{ID: "t1", Name: "gitlab", Type: "toolset", Owner: "root"})
	if code, body := post(`{"type":"toolset","name":"new service"}`); code != http.StatusBadRequest || !strings.Contains(body, "MCP") {
		t.Errorf("a new tool-backed service was made: %d %s", code, body)
	}
	if code, body := post(`{"id":"s1","type":"toolset","name":"web"}`); code != http.StatusBadRequest || !strings.Contains(body, "MCP") {
		t.Errorf("an ssh system was turned into a tool-backed service: %d %s", code, body)
	}
	if code, body := post(`{"id":"t1","type":"toolset","name":"gitlab renamed"}`); code != http.StatusOK {
		t.Errorf("an existing tool-backed service could not be edited: %d %s", code, body)
	}
	if code, body := post(`{"type":"ssh","name":"db","host":"db.example.test"}`); code != http.StatusOK {
		t.Errorf("a new ssh system was refused: %d %s", code, body)
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
