package servitor

// A system is shared to Read or with Full access. Read asks from what has been
// gathered and nothing reaches the system; Full investigates it live. A share
// saved before levels existed keeps the full use it always had.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestASystemIsSharedToReadOrWithFullAccess(t *testing.T) {
	prev := AuthDB
	auth := &DBase{Store: kvlite.MemStore()}
	for _, u := range []string{"alice", "bob", "carol"} {
		AuthSetUser(auth, u, "pw", false)
	}
	AuthDB = func() Database { return auth }
	t.Cleanup(func() { AuthDB = prev })
	app := &Servitor{}
	app.DB = &DBase{Store: kvlite.MemStore()}

	// Shared before levels: bob keeps full use.
	old := Appliance{ID: "web01", Name: "web01", Type: "ssh", Host: "web01.example.com", Owner: "alice", AllowedUsers: []string{"bob", "carol"}}
	UserDB(app.DB, "alice").Set(applianceTable, old.ID, old)
	app.indexNamedShares("alice", old.ID, old.AllowedUsers)
	if !old.fullAccessFor("alice", "bob") {
		t.Fatal("a share from before levels lost its full use")
	}

	// The owner saves levels: bob Full, carol Read.
	w := httptest.NewRecorder()
	app.handleAppliances(w, reqAs(t, http.MethodPost, "/api/appliances", "alice",
		`{"id":"web01","type":"ssh","name":"web01","host":"web01.example.com","allowed_users":["bob","carol"],"full_access":["bob","dan"]}`))
	if w.Code != http.StatusOK {
		t.Fatalf("save answered %d: %s", w.Code, w.Body.String())
	}
	var a Appliance
	json.Unmarshal(w.Body.Bytes(), &a)
	if !a.LevelsSet || strings.Join(a.FullAccess, ",") != "bob" {
		t.Fatalf("levels stored as %v (set %v)", a.FullAccess, a.LevelsSet)
	}
	if !a.fullAccessFor("alice", "bob") || a.fullAccessFor("alice", "carol") || !a.fullAccessFor("alice", "alice") {
		t.Error("the levels read wrong")
	}
	// A save that sends no levels keeps them.
	w = httptest.NewRecorder()
	app.handleAppliances(w, reqAs(t, http.MethodPost, "/api/appliances", "alice",
		`{"id":"web01","type":"ssh","name":"web01 renamed","host":"web01.example.com"}`))
	json.Unmarshal(w.Body.Bytes(), &a)
	if !a.LevelsSet || strings.Join(a.FullAccess, ",") != "bob" {
		t.Errorf("a save without levels changed them: %v", a.FullAccess)
	}

	// As an agent's Source: carol's agent reads what is gathered and cannot
	// send it a question; bob's can.
	names := func(user string) string {
		var out []string
		for _, td := range (servitorSource{app: app}).ItemToolsWithSession(nil, user, "web01") {
			out = append(out, td.Tool.Name)
		}
		return strings.Join(out, ",")
	}
	if got := names("carol"); strings.Contains(got, "investigate_") || !strings.Contains(got, "search_") {
		t.Errorf("Read's Source tools: %s", got)
	}
	if got := names("bob"); !strings.Contains(got, "investigate_") {
		t.Errorf("Full access's Source tools: %s", got)
	}

	// Mapping runs commands: refused for Read.
	w = httptest.NewRecorder()
	app.handleMap(w, reqAs(t, http.MethodPost, "/api/map", "carol", `{"appliance_id":"web01"}`))
	if w.Code != http.StatusForbidden {
		t.Errorf("Read mapped the system: %d", w.Code)
	}

	// A Read session's toolkit holds nothing that reaches the system or
	// writes to what is known about it.
	pr := &probeRun{T: app, ctx: context.Background(), id: "s-read", userID: "carol", ownerUser: "alice",
		appliance: a, readOnly: true, udb: UserDB(app.DB, "carol"), ownerUDB: UserDB(app.DB, "alice"), a: &Servitor{}}
	pr.execSeam()
	pr.execTools()
	pr.memoryTools()
	pr.readTools()
	pr.reportTools()
	pr.assembleToolkit()
	var got []string
	for _, td := range pr.workerTools {
		got = append(got, td.Tool.Name)
	}
	for _, banned := range []string{"run_command", "run_pty", pr.read_log_tool.Tool.Name, pr.store_fact_tool.Tool.Name, pr.note_lesson_tool.Tool.Name} {
		for _, n := range got {
			if n == banned {
				t.Errorf("a Read session holds %s", banned)
			}
		}
	}
	if !strings.Contains(strings.Join(got, ","), pr.search_facts_tool.Tool.Name) {
		t.Errorf("a Read session cannot search what is gathered: %v", got)
	}
}
