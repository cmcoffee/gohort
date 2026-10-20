package orchestrate

// A pipeline or machine shared to edit: the editor changes the owner's copy,
// which stays the owner's; a recipient shared to run cannot change it; and
// sharing, deleting and publishing stay the owner's.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestARecipeSharedToEditIsChangedInTheOwnersCopy(t *testing.T) {
	T, udb, _ := newTestOrchestrate(t)
	prevP, prevM := PipelineSavedHook, MachineSavedHook
	PipelineSavedHook, MachineSavedHook = syncPipelineShareIndex, syncMachineShareIndex
	t.Cleanup(func() { PipelineSavedHook, MachineSavedHook = prevP, prevM })
	for _, u := range []string{"bob", "carol"} {
		AuthDB().Set(AuthTable, "user:"+u, AuthUser{Username: u})
	}
	as := func(r *http.Request, who string) *http.Request {
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(AuthDB(), who)})
		return r
	}

	pl := SavePipelineDef(udb, PipelineDef{Owner: "alice", Name: "Nightly", Stages: []PipelineStage{{Name: "one"}},
		AllowedUsers: []string{"bob", "carol"}})
	mc := SaveMachineDef(udb, MachineDef{Owner: "alice", Name: "Triage", Unattended: true,
		Phases: []MachinePhase{{Name: "work", Prompt: "do the work"}}, AllowedUsers: []string{"bob", "carol"}})
	// The editor's PUT is the whole record, as the editor form sends it.
	v2 := mc
	v2.Name = "Triage v2"
	machinePut, _ := json.Marshal(v2)

	for _, c := range []struct {
		kind, base, put string
		handle          func(http.ResponseWriter, *http.Request)
		read            func() (name, owner string, editors []string)
	}{
		{"pipeline", "/api/pipelines/" + pl.ID, `{"name":"Nightly v2","stages":[{"name":"one"}]}`, T.handlePipelineOne, func() (string, string, []string) {
			d, _ := LoadPipelineDef(udb, "alice", pl.ID)
			return d.Name, d.Owner, d.Editors
		}},
		{"machine", "/api/machines/" + mc.ID, string(machinePut), T.handleMachineOne, func() (string, string, []string) {
			d, _ := LoadMachineDef(udb, "alice", mc.ID)
			return d.Name, d.Owner, d.Editors
		}},
	} {
		call := func(who, method, path, body string) int {
			w := httptest.NewRecorder()
			c.handle(w, as(httptest.NewRequest(method, c.base+path, strings.NewReader(body)), who))
			return w.Code
		}

		// The owner makes bob an editor. Everyone, and a name the list does
		// not reach, are not kept as editors.
		if code := call("alice", http.MethodPost, "/share", `{"allowed_users":["bob","carol"],"editors":["bob","*","dan"]}`); code != http.StatusOK {
			t.Fatalf("%s: sharing answered %d", c.kind, code)
		}
		if _, _, eds := c.read(); strings.Join(eds, ",") != "bob" {
			t.Errorf("%s: editors stored as %v", c.kind, eds)
		}

		// carol may run it, not change it.
		if code := call("carol", http.MethodPut, "", c.put); code != http.StatusForbidden {
			t.Errorf("%s: a run-only recipient changed it (%d)", c.kind, code)
		}
		// bob changes it, in alice's copy, which stays alice's.
		if code := call("bob", http.MethodPut, "", c.put); code != http.StatusOK {
			t.Fatalf("%s: the editor's change answered %d", c.kind, code)
		}
		if name, owner, eds := c.read(); !strings.HasSuffix(name, " v2") || owner != "alice" || strings.Join(eds, ",") != "bob" {
			t.Errorf("%s: after bob's edit: name %q, owner %q, editors %v", c.kind, name, owner, eds)
		}
		if got := ListPipelineDefs(UserDB(T.DB, "bob"), "bob"); c.kind == "pipeline" && len(got) != 0 {
			t.Errorf("the editor's change forked a copy into their store: %d", len(got))
		}
		// What stays the owner's.
		for _, step := range []struct{ method, path, body string }{
			{http.MethodPost, "/share", `{"allowed_users":["bob","dan"]}`},
			{http.MethodDelete, "", ""},
		} {
			if code := call("bob", step.method, step.path, step.body); code != http.StatusForbidden {
				t.Errorf("%s: the editor could %s %s (%d)", c.kind, step.method, step.path, code)
			}
		}
	}
}
