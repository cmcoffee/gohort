package extensions

// The "How it runs" section of a tool's Security panel: never unattended,
// and ask before every call, each a pill that reads back what it set.

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestToolRunSectionTogglesReadBack(t *testing.T) {
	app, req := authedExtensions(t)
	if err := AdminPersistTempTool(RootDB, "alice", TempTool{Name: "wiki_read", Description: "d", CommandTemplate: "echo x"}); err != nil {
		t.Fatal(err)
	}
	pills := func() map[string]bool {
		t.Helper()
		w := httptest.NewRecorder()
		serve(app)(w, req("GET", "/api/tool-access?section=run&name=wiki_read"))
		var out struct {
			Items []struct {
				Key string `json:"key"`
				On  bool   `json:"on"`
			} `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("pills: %v %s", err, w.Body.String())
		}
		got := map[string]bool{}
		for _, p := range out.Items {
			got[p.Key] = p.On
		}
		return got
	}
	set := func(target string, on bool) int {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"target": target, "on": on})
		r := req("POST", "/api/tool-access?section=run&name=wiki_read")
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		w := httptest.NewRecorder()
		serve(app)(w, r)
		return w.Code
	}

	before := pills()
	for _, k := range []string{"never_unattended", "ask_first"} {
		if on, ok := before[k]; !ok || on {
			t.Fatalf("%s: offered=%v on=%v, want offered and off", k, ok, on)
		}
	}
	for _, k := range []string{"never_unattended", "ask_first"} {
		if code := set(k, true); code != 204 {
			t.Fatalf("turning on %s: %d", k, code)
		}
	}
	after := pills()
	for _, k := range []string{"never_unattended", "ask_first"} {
		if !after[k] {
			t.Errorf("%s did not read back on", k)
		}
	}
	if !UserToolAsksInChat(AuthDB(), "alice", "", "wiki_read") {
		t.Error("ask first is not the fleet-wide mark the permission check reads")
	}
	if set("bogus", true) != 400 {
		t.Error("an unknown setting was accepted")
	}
}

// A tool that is not kept for the user's agents has no record to mark.
func TestToolRunSectionRefusesAFlagOutsideThePool(t *testing.T) {
	app, req := authedExtensions(t)
	r := req("POST", "/api/tool-access?section=run&name=nothing_here")
	r.Body = io.NopCloser(strings.NewReader(`{"target":"never_unattended","on":true}`))
	w := httptest.NewRecorder()
	serve(app)(w, r)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "keep the tool") {
		t.Errorf("a flag on a tool outside the pool: %d %s", w.Code, w.Body.String())
	}
	// Lock is gone: changes to a tool always ask the owner instead.
	r = req("POST", "/api/tool-access?section=run&name=nothing_here")
	r.Body = io.NopCloser(strings.NewReader(`{"target":"locked","on":true}`))
	w = httptest.NewRecorder()
	serve(app)(w, r)
	if w.Code != 400 {
		t.Errorf("locked is still a setting: %d", w.Code)
	}
}

// fakeToolScope is a ScopeProvider over one tool: the user-wide pool switch
// and a set of agents.
type fakeToolScope struct {
	global bool
	on     map[string]bool
}

func (f *fakeToolScope) provider() ScopeProvider {
	return ScopeProvider{
		State: func(db Database, owner, name string) (ToolScopeState, bool) {
			st := ToolScopeState{Global: f.global}
			for _, id := range []string{"a1", "a2", "a3"} {
				st.Agents = append(st.Agents, ToolScopeAgent{ID: id, Name: id, On: f.global || f.on[id]})
			}
			return st, true
		},
		Set: func(db Database, owner, name, target string, on bool) error {
			if target == scopeAllAgents {
				if !on && f.global {
					// Demoted: kept on every agent that could see it.
					f.on = map[string]bool{"a1": true, "a2": true, "a3": true}
				}
				f.global = on
				return nil
			}
			f.on[target] = on
			return nil
		},
	}
}

// Who can use it, as three choices: the flags and the scope move together,
// and Builder-only is "Only these agents" with none ticked.
func TestToolWhoChoices(t *testing.T) {
	app, req := authedExtensions(t)
	if err := AdminPersistTempTool(RootDB, "alice", TempTool{Name: "wiki_read", Description: "d", CommandTemplate: "echo x"}); err != nil {
		t.Fatal(err)
	}
	scope := &fakeToolScope{global: true, on: map[string]bool{}}
	prev, had := ScopeProviderFor("tool")
	RegisterScopeProvider("tool", scope.provider())
	t.Cleanup(func() {
		if had {
			RegisterScopeProvider("tool", prev)
		}
	})
	state := func() (string, map[string]bool) {
		t.Helper()
		w := httptest.NewRecorder()
		serve(app)(w, req("GET", "/api/tool-access?section=who&name=wiki_read"))
		var out struct {
			Mode   string `json:"mode"`
			Agents []struct {
				ID string `json:"id"`
				On bool   `json:"on"`
			} `json:"agents"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("who: %v %s", err, w.Body.String())
		}
		on := map[string]bool{}
		for _, a := range out.Agents {
			if a.On {
				on[a.ID] = true
			}
		}
		return out.Mode, on
	}
	post := func(body string) {
		t.Helper()
		r := req("POST", "/api/tool-access?section=who&name=wiki_read")
		r.Body = io.NopCloser(strings.NewReader(body))
		w := httptest.NewRecorder()
		serve(app)(w, r)
		if w.Code != 204 {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
	flags := func() (bool, bool) {
		row, _ := UserToolByName(AuthDB(), "alice", "wiki_read")
		return row.Tool.BuilderOnly, row.Tool.BoundOnly
	}

	if mode, _ := state(); mode != "all" {
		t.Fatalf("a pool tool reads %q, want all", mode)
	}
	// Only these agents, from All: nobody yet, which is Builder-only.
	post(`{"mode":"agents"}`)
	if mode, on := state(); mode != "agents" || len(on) != 0 {
		t.Fatalf("after choosing Only these agents: %q %v", mode, on)
	}
	if b, _ := flags(); !b {
		t.Error("no agents ticked is not Builder-only")
	}
	// Ticking one: that agent alone, whatever sat under the flag.
	post(`{"agent":"a2","on":true}`)
	if mode, on := state(); mode != "agents" || len(on) != 1 || !on["a2"] {
		t.Fatalf("after ticking a2: %q %v", mode, on)
	}
	if b, _ := flags(); b {
		t.Error("ticking an agent left Builder-only on")
	}
	post(`{"agent":"a3","on":true}`)
	post(`{"agent":"a2","on":false}`)
	if _, on := state(); len(on) != 1 || !on["a3"] {
		t.Fatalf("after a3 on, a2 off: %v", on)
	}
	// Unticking the last: nobody, kept as Builder-only rather than dropped.
	post(`{"agent":"a3","on":false}`)
	if mode, on := state(); mode != "agents" || len(on) != 0 {
		t.Fatalf("after unticking the last: %q %v", mode, on)
	}
	if b, _ := flags(); !b {
		t.Error("unticking the last agent did not leave Builder-only")
	}
	// Only where attached, then back to All: the flags never both hold.
	post(`{"mode":"bound"}`)
	if b, bound := flags(); b || !bound {
		t.Errorf("bound-only: builder=%v bound=%v", b, bound)
	}
	if mode, _ := state(); mode != "bound" {
		t.Errorf("bound reads %q", mode)
	}
	post(`{"mode":"all"}`)
	if b, bound := flags(); b || bound {
		t.Errorf("All left a flag on: builder=%v bound=%v", b, bound)
	}
	if mode, _ := state(); mode != "all" {
		t.Errorf("All reads %q", mode)
	}

	// Choosing another option and coming back keeps who was ticked.
	post(`{"mode":"agents"}`)
	post(`{"agent":"a1","on":true}`)
	post(`{"agent":"a3","on":true}`)
	for _, away := range []string{"all", "bound"} {
		post(`{"mode":"` + away + `"}`)
		post(`{"mode":"agents"}`)
		if mode, on := state(); mode != "agents" || len(on) != 2 || !on["a1"] || !on["a3"] {
			t.Errorf("back from %s, the ticked agents were lost: %q %v", away, mode, on)
		}
	}
}
