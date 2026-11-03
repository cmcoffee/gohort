package extensions

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The Agents dialog on a tool the user added: it reads back what was chosen,
// saves Only these agents for agents the user has, refuses an empty pick, and
// the catalog row names the agents.
func TestTheAgentsDialogKeepsAnAddedToolToSomeAgents(t *testing.T) {
	app, req := authedExtensions(t)
	prev, had := ScopeProviderFor("tool")
	RegisterScopeProvider("tool", ScopeProvider{State: func(db Database, owner, name string) (ToolScopeState, bool) {
		return ToolScopeState{Name: name, Agents: []ToolScopeAgent{
			{ID: "a-1", Name: "Travel"}, {ID: "a-2", Name: "Fares", ParentID: "a-1"},
		}}, false
	}})
	t.Cleanup(func() {
		if had {
			RegisterScopeProvider("tool", prev)
		}
	})
	if err := AdminPersistTempTool(RootDB, "lender", TempTool{Name: "fx_rate", Description: "d", CommandTemplate: "echo one"}); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolSharedWith(RootDB, "lender", "fx_rate", []string{"alice"}); err != nil {
		t.Fatal(err)
	}
	if err := SetGlobalToolAdopted(RootDB, "alice", "fx_rate", "lender", true); err != nil {
		t.Fatal(err)
	}
	post := func(body string) int {
		r := req("POST", "/api/global-tools/agents?name=fx_rate")
		r.Body = io.NopCloser(strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		serve(app)(w, r)
		return w.Code
	}
	get := func(path string) string {
		w := httptest.NewRecorder()
		serve(app)(w, req("GET", path))
		return w.Body.String()
	}
	if got := get("/api/global-tools/agents?name=fx_rate"); !strings.Contains(got, `"who":"all"`) {
		t.Fatalf("an added tool starts on all agents: %s", got)
	}
	if got := get("/api/global-tools/agents?name=fx_rate&options=1"); !strings.Contains(got, "Travel ▸ Fares") {
		t.Fatalf("a sub-agent should read under its parent: %s", got)
	}
	if code := post(`{"who":"agents","agents":[]}`); code/100 == 2 {
		t.Fatal("Only these agents with none ticked was saved")
	}
	if code := post(`{"who":"agents","agents":["a-2","not-mine"]}`); code/100 != 2 {
		t.Fatalf("save: %d", code)
	}
	if ids, _ := adoptedAgents("alice", "fx_rate"); len(ids) != 1 || ids[0] != "a-2" {
		t.Fatalf("only the user's own agents are kept: %v", ids)
	}
	if got := get("/api/global-tools/agents?name=fx_rate"); !strings.Contains(got, `"who":"agents"`) {
		t.Fatalf("the dialog should read back Only these agents: %s", got)
	}
	if r := rowsNamed(t, []byte(get("/api/global-tools")), "fx_rate"); r["agents"] != "Travel ▸ Fares" {
		t.Fatalf("the catalog row should name the agent: %+v", r)
	}
	if code := post(`{"who":"all"}`); code/100 != 2 {
		t.Fatalf("all: %d", code)
	}
	if ids, _ := adoptedAgents("alice", "fx_rate"); len(ids) != 0 {
		t.Fatalf("All my agents should clear the list: %v", ids)
	}
}
