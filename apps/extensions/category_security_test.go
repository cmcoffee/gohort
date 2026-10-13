package extensions

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// multiToolScope is a ScopeProvider over several tools.
type multiToolScope map[string]*fakeToolScope

func (m multiToolScope) provider() ScopeProvider {
	return ScopeProvider{
		State: func(db Database, owner, name string) (ToolScopeState, bool) {
			f := m[name]
			if f == nil {
				return ToolScopeState{}, false
			}
			return f.provider().State(db, owner, name)
		},
		Set: func(db Database, owner, name, target string, on bool) error {
			return m[name].provider().Set(db, owner, name, target, on)
		},
	}
}

// A category's Security names the tools that disagree before anything is
// applied, and Apply sets every tool in it to exactly the choice.
func TestCategorySecurityShowsDifferencesAndAppliesToAll(t *testing.T) {
	app, req := authedExtensions(t)
	for _, n := range []string{"cal_add", "cal_list"} {
		if err := AdminPersistTempTool(RootDB, "alice", TempTool{Name: n, Description: "d", CommandTemplate: "echo", Category: "Calendar"}); err != nil {
			t.Fatal(err)
		}
	}
	scopes := multiToolScope{
		"cal_add":  {global: true, on: map[string]bool{}},
		"cal_list": {global: false, on: map[string]bool{"a1": true}},
	}
	prev, had := ScopeProviderFor("tool")
	RegisterScopeProvider("tool", scopes.provider())
	t.Cleanup(func() {
		if had {
			RegisterScopeProvider("tool", prev)
		}
	})
	get := func(section string) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		app.handleCategorySecurity(w, req("GET", "/api/category-security?name=Calendar&section="+section))
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s: %v %s", section, err, w.Body.String())
		}
		return out
	}
	post := func(section, body string) {
		t.Helper()
		r := req("POST", "/api/category-security?name=Calendar&section="+section)
		r.Body = io.NopCloser(strings.NewReader(body))
		w := httptest.NewRecorder()
		app.handleCategorySecurity(w, r)
		if w.Code >= 300 {
			t.Fatalf("%s %s: %d %s", section, body, w.Code, w.Body.String())
		}
	}

	who := get("who")
	differ, _ := who["differ"].([]any)
	if who["mode"] != "" || len(differ) != 2 {
		t.Fatalf("tools that disagree were not named: %v", who)
	}
	text, _ := json.Marshal(differ)
	if !strings.Contains(string(text), "All my agents") || !strings.Contains(string(text), "Only a1") {
		t.Errorf("the differences do not say what each tool has: %s", text)
	}

	post("who", `{"mode":"agents","agents":["a2"]}`)
	who = get("who")
	if who["mode"] != "agents" || len(who["differ"].([]any)) != 0 {
		t.Fatalf("after Apply the tools still disagree: %v", who)
	}
	for _, n := range []string{"cal_add", "cal_list"} {
		f := scopes[n]
		if f.global || !f.on["a2"] || f.on["a1"] || f.on["a3"] {
			t.Errorf("%s is not exactly a2: global=%v on=%v", n, f.global, f.on)
		}
	}

	// How it runs: dashed where only some have it, set for all by a click.
	if code, err := toolRunApply(AuthDB(), "alice", "cal_add", "ask_first", true); err != nil {
		t.Fatal(code, err)
	}
	run := get("run")
	items, _ := json.Marshal(run["items"])
	if !strings.Contains(string(items), `"key":"ask_first","label":"Ask me before every call","on":false,"partial":true`) {
		t.Errorf("one of two asking is not shown as some: %s", items)
	}
	post("run", `{"target":"ask_first","on":true}`)
	if !UserToolAsksInChat(AuthDB(), "alice", "", "cal_list") {
		t.Error("the category's setting did not reach every tool")
	}
}
