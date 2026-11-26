package customapps

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/applocal"
	"github.com/cmcoffee/gohort/tools/appscript"
	"github.com/cmcoffee/snugforge/kvlite"
)

// credStore gives the test a credential store of its own.
func credStore(t *testing.T) {
	t.Helper()
	auth := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return auth }
	RebindSecure()
	t.Cleanup(func() { AuthDB = prev; RebindSecure() })
}

// The owner chooses on the app's Settings how it lends each credential of
// theirs it uses, and may keep one out of it; a maintainer may only narrow;
// anyone else sees none of it. Runs carry the choice, and the record of what
// the app uses grows the list.
func TestTheOwnerChoosesHowTheAppLendsEachCredential(t *testing.T) {
	T := sharingTestApp(t)
	credStore(t)
	for _, name := range []string{"wiki", "other", "unused"} {
		if err := Secure().Save(SecureCredential{Name: name, Type: SecureCredNone, Owner: "alice", BaseURL: "https://" + name + ".example"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	spec := AppSpec{Slug: "prep", Name: "Prep", Owner: "alice", RecordKey: "id", Maintainers: []string{"bob"},
		Actions: []AppAction{{Name: "post", Script: "x", Capabilities: []string{"fetch_via:wiki"}}}}
	rows := func(user string) (int, []map[string]any) {
		w := appAs(t, T, spec, user, httptest.NewRequest(http.MethodGet, "/apps/prep/_settings/reach", nil))
		var out struct {
			Rows []map[string]any `json:"rows"`
		}
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out.Rows
	}
	choose := func(user, name, lend string) int {
		return appAs(t, T, spec, user, httptest.NewRequest(http.MethodPost, "/apps/prep/_settings/reach", strings.NewReader(`{"name":"`+name+`","lend":"`+lend+`"}`))).Code
	}
	use := func(user, name string, on bool) int {
		body := `{"used":false}`
		if on {
			body = `{"used":true}`
		}
		return appAs(t, T, spec, user, httptest.NewRequest(http.MethodPost, "/apps/prep/_settings/reach/used/"+name, strings.NewReader(body))).Code
	}

	code, got := rows("alice")
	if code != 200 || len(got) != 1 || got[0]["name"] != "wiki" || got[0]["lend"] != "read" || got[0]["used"] != true {
		t.Fatalf("the owner's table: %d %v", code, got)
	}
	if code, _ := rows("carol"); code == 200 {
		t.Error("someone who only uses the app read how it lends")
	}
	steps := []struct {
		user, lend string
		want       int
	}{
		{"bob", "write", 403}, // a maintainer never lends the key further
		{"bob", "own", 200},
		{"bob", "read", 403}, // nor back to reads from each person's own
		{"alice", "write", 200},
		{"bob", "read", 200}, // from writes back to reads narrows
	}
	for _, s := range steps {
		if c := choose(s.user, "wiki", s.lend); c != s.want {
			t.Errorf("%s choosing %s: %d, want %d", s.user, s.lend, c, s.want)
		}
	}
	if c := choose("alice", "unused", "write"); c != 400 {
		t.Errorf("a choice for a credential the app does not use: %d", c)
	}
	if c := use("bob", "wiki", false); c != 200 {
		t.Errorf("a maintainer keeping it out: %d", c)
	}
	if c := use("bob", "wiki", true); c != 403 {
		t.Errorf("a maintainer letting it back in: %d", c)
	}

	var j appscript.Job
	withAppCredentials(&j, "alice", "prep")
	if j.CredentialLend("wiki") != "read" || len(j.CredentialsOff) != 1 || j.CredentialsOff[0] != "wiki" {
		t.Errorf("a run's rules: lend %q, off %v", j.CredentialLend("wiki"), j.CredentialsOff)
	}
	if c := use("alice", "wiki", true); c != 200 {
		t.Errorf("the owner letting it back in: %d", c)
	}
	// A credential a run goes out through joins the list.
	j.CredentialUsed("other")
	if _, got := rows("alice"); len(got) != 2 || got[0]["name"] != "other" || !strings.Contains(got[0]["how"].(string), "used since") {
		t.Errorf("after a run used another: %v", got)
	}
	if !strings.Contains(appAs(t, T, spec, "alice", httptest.NewRequest(http.MethodGet, "/apps/prep/_settings", nil)).Body.String(), "What it reaches") {
		t.Error("the Settings page has no What it reaches")
	}
	T.deleteApp("alice", "prep")
	if a := applocal.LoadAppCredentials("alice", "prep"); len(a.Lend) != 0 || len(a.Used) != 0 {
		t.Errorf("a deleted app's choices outlived it: %+v", a)
	}
}

// What other people's apps lend a person, for core: each credential a
// shared app uses and does not withhold, named with the app; not their own
// apps', and not one the app keeps out.
func TestAppLendsAreWhatSharedAppsLendThePerson(t *testing.T) {
	T := sharingTestApp(t)
	credStore(t)
	Secure().Save(SecureCredential{Name: "wiki", Type: SecureCredBearer, Owner: "alice", BaseURL: "https://wiki.example"}, "k")
	spec := AppSpec{Slug: "prep", Name: "Prep", Owner: "alice", RecordKey: "id",
		Actions: []AppAction{{Name: "post", Script: "x", Capabilities: []string{"fetch_via:wiki"}}}}
	appAs(t, T, spec, "carol", httptest.NewRequest(http.MethodGet, "/apps/prep/", nil)) // shared with carol
	if got := T.appLends("carol"); len(got) != 1 || got[0] != [3]string{"alice", "wiki", "Prep"} {
		t.Fatalf("what the app lends carol: %v", got)
	}
	if got := T.appLends("alice"); len(got) != 0 {
		t.Errorf("an owner's own app lends her her own key: %v", got)
	}
	applocal.UpdateAppCredentials("alice", "prep", func(c *applocal.AppCredentials) { c.Lend = map[string]string{"wiki": "withhold"} })
	if got := T.appLends("carol"); len(got) != 0 {
		t.Errorf("a withheld key is listed as lent: %v", got)
	}
}

// When a data source run for someone waits on their own account, their page
// is told which and where to connect it, whatever the script printed; once
// they connect their own, it is not said again. The owner is never told it.
func TestAPageSaysWhichAccountItsDataWaitedOn(t *testing.T) {
	T := sharingTestApp(t)
	credStore(t)
	Secure().Save(SecureCredential{Name: "wiki", Type: SecureCredBearer, Owner: "alice", BaseURL: "https://wiki.example"}, "k")
	Secure().SetAppLends(T.appLends)
	t.Cleanup(func() { Secure().SetAppLends(nil) })
	spec := AppSpec{Slug: "prep", Name: "Prep", Owner: "alice", RecordKey: "id",
		DataSources: []AppDataSource{{Name: "pages", Script: "x", Capabilities: []string{"fetch_via:wiki"}}},
		Page:        json.RawMessage(`{"title":"Prep","sections":[]}`)}
	prev := runAppScript
	t.Cleanup(func() { runAppScript = prev })
	runAppScript = func(j appscript.Job) (string, error) {
		if j.OnConnectNeeded != nil && j.Caller == "bob" {
			j.OnConnectNeeded("alice", "wiki")
		}
		return `[]`, nil // the script swallowed the refusal and showed nothing
	}
	needs := func(user string) []any {
		w := appAs(t, T, spec, user, httptest.NewRequest(http.MethodGet, "/apps/prep/needs-account", nil))
		var out struct {
			Names []any `json:"names"`
		}
		json.Unmarshal(w.Body.Bytes(), &out)
		return out.Names
	}
	appAs(t, T, spec, "bob", httptest.NewRequest(http.MethodGet, "/apps/prep/data/pages", nil))
	if got := needs("bob"); len(got) != 1 || got[0] != "wiki" {
		t.Fatalf("bob is not told which account his data waited on: %v", got)
	}
	if got := needs("alice"); len(got) != 0 {
		t.Errorf("the owner was told: %v", got)
	}
	if !strings.Contains(appAs(t, T, spec, "bob", httptest.NewRequest(http.MethodGet, "/apps/prep/", nil)).Body.String(), "data-needs-account") {
		t.Error("bob's page cannot say it")
	}
	if strings.Contains(appAs(t, T, spec, "alice", httptest.NewRequest(http.MethodGet, "/apps/prep/", nil)).Body.String(), "data-needs-account") {
		t.Error("the owner's page carries it")
	}
	if err := Secure().SaveSharedAccount("alice", "wiki", "bob", "bob-key"); err != nil {
		t.Fatalf("bob connecting his own: %v", err)
	}
	if got := needs("bob"); len(got) != 0 {
		t.Errorf("still told after connecting his own: %v", got)
	}
	// And a note is kept only a while.
	noteNeedsAccount("alice", "prep", "carol", "wiki")
	needsAccount.Lock()
	needsAccount.m["alice/prep/carol"]["wiki"] = time.Now().Add(-needsAccountKept - time.Minute)
	needsAccount.Unlock()
	if got := needsAccountFor("alice", "prep", "carol"); len(got) != 0 {
		t.Errorf("an old note is still shown: %v", got)
	}
}

// The page asks a little after it loads, and shows which accounts once,
// below the header, with where to connect them; with nothing to say it
// shows nothing.
func TestThePageBarNamesTheAccountsToConnect(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	js := strings.TrimSuffix(strings.TrimPrefix(accountNeedsJS("/apps/prep/"), "<script>"), "</script>")
	run := func(names string) string {
		harness := `
function node(tag) { return {tag: tag, kids: [], style: {}, attrs: {}, textContent: "", setAttribute: function(k, v) { this.attrs[k] = v; }, appendChild: function(c) { this.kids.push(c); }}; }
var placed = [], asks = 0;
var header = node("div"); header.parentNode = {insertBefore: function(c) { placed.push(c); }};
var document = {createElement: node, querySelector: function() { return header; }, getElementById: function() { return null; }, body: node("body")};
global.setTimeout = function(fn) { Promise.resolve().then(fn); };
global.fetch = function(u) { asks++; return Promise.resolve({json: function() { return Promise.resolve({names: ` + names + `}); }}); };
` + js + `
var ticks = 0;
(function wait() { if (++ticks < 60) { setTimeout(wait); return; }
  var bar = placed[0];
  console.log(JSON.stringify({placed: placed.length, asks: asks, text: bar && bar.kids[0].textContent, href: bar && bar.kids[1].href}));
})();
`
		f := filepath.Join(t.TempDir(), "n.js")
		if err := os.WriteFile(f, []byte(harness), 0o644); err != nil {
			t.Fatal(err)
		}
		b, err := exec.Command(node, f).CombinedOutput()
		if err != nil {
			t.Fatalf("node: %v\n%s", err, b)
		}
		return strings.TrimSpace(string(b))
	}
	got := run(`["wiki", "jira"]`)
	if !strings.Contains(got, `"placed":1`) || !strings.Contains(got, `"asks":1`) || !strings.Contains(got, "your own wiki and jira account") || !strings.Contains(got, `"href":"/extensions/"`) {
		t.Errorf("with accounts to connect: %s", got)
	}
	if got := run(`[]`); !strings.Contains(got, `"placed":0`) || !strings.Contains(got, `"asks":5`) {
		t.Errorf("with nothing to say: %s", got)
	}
}

// reachMCPTool stands in for a registered MCP proxy: its category is its
// server's, which is how a script's MCP tool is told from the others.
type reachMCPTool struct{ name, server string }

func (f reachMCPTool) Name() string                       { return f.name }
func (f reachMCPTool) Desc() string                       { return "MCP tool." }
func (f reachMCPTool) Params() map[string]ToolParam       { return nil }
func (f reachMCPTool) Run(map[string]any) (string, error) { return "", nil }
func (f reachMCPTool) Category() string                   { return MCPToolCategory(f.server) }

// The owner's sign-in to an MCP server that signs in per person is listed on
// What it reaches when a script names one of its tools, with only reads or
// Disallow to choose from; a maintainer may disallow it but never lend it
// again. A server on the deployment's own connection is not the owner's and
// is not listed, and a sign-in is never offered to the person as an account
// of theirs to connect.
func TestTheOwnerChoosesWhetherTheAppLendsTheirMCPSignIn(t *testing.T) {
	T := sharingTestApp(t)
	credStore(t)
	MCP().Save(MCPServerConfig{Name: "apptest-own", URL: "https://issues.example/mcp", AuthMode: MCPAuthOAuth, Enabled: true, ExposeTools: true}, "")
	MCP().Save(MCPServerConfig{Name: "apptest-dep", URL: "https://pages.example/mcp", Enabled: true, ExposeTools: true}, "")
	t.Cleanup(func() { MCP().Delete("apptest-own"); MCP().Delete("apptest-dep") })
	RegisterChatTool(reachMCPTool{name: "apptest-own_get_issue", server: "apptest-own"})
	RegisterChatTool(reachMCPTool{name: "apptest-dep_get_page", server: "apptest-dep"})
	spec := AppSpec{Slug: "desk", Name: "Desk", Owner: "alice", RecordKey: "id", Maintainers: []string{"bob"},
		DataSources: []AppDataSource{{Name: "issues", Script: "x", Capabilities: []string{"tool:apptest-own_get_issue", "tool:apptest-dep_get_page"}}}}
	rows := func(user string) []map[string]any {
		w := appAs(t, T, spec, user, httptest.NewRequest(http.MethodGet, "/apps/desk/_settings/reach", nil))
		var out struct {
			Rows []map[string]any `json:"rows"`
		}
		json.Unmarshal(w.Body.Bytes(), &out)
		return out.Rows
	}
	choose := func(user, lend string) int {
		return appAs(t, T, spec, user, httptest.NewRequest(http.MethodPost, "/apps/desk/_settings/reach", strings.NewReader(`{"name":"mcp:apptest-own","lend":"`+lend+`"}`))).Code
	}
	got := rows("alice")
	if len(got) != 1 || got[0]["name"] != "mcp:apptest-own" || got[0]["label"] != "apptest-own sign-in" || got[0]["goes"] != "issues.example" ||
		got[0]["lend"] != "read" || got[0]["decide_mcp"] != true || got[0]["decide"] != nil {
		t.Fatalf("the owner's table: %v", got)
	}
	steps := []struct {
		user, lend string
		want       int
	}{
		{"alice", "write", 400}, // a script cannot write with it
		{"alice", "own", 400},   // nobody's own sign-in is behind the owner's script
		{"bob", "withhold", 200},
		{"bob", "read", 403}, // a maintainer never lends it again
		{"alice", "read", 200},
		{"alice", "withhold", 200},
	}
	for _, s := range steps {
		if c := choose(s.user, s.lend); c != s.want {
			t.Errorf("%s choosing %s: %d, want %d", s.user, s.lend, c, s.want)
		}
	}
	if got := rows("alice"); len(got) != 1 || got[0]["lend"] != "withhold" {
		t.Errorf("after disallowing it: %v", got)
	}
	// A choice a sign-in is not offered shows as what a run reads it as.
	applocal.UpdateAppCredentials("alice", "desk", func(c *applocal.AppCredentials) { c.Lend["mcp:apptest-own"] = "own" })
	if got := rows("alice"); len(got) != 1 || got[0]["lend"] != "withhold" {
		t.Errorf("a stored own: %v", got)
	}
	applocal.UpdateAppCredentials("alice", "desk", func(c *applocal.AppCredentials) { c.Lend["mcp:apptest-own"] = "withhold" })
	if c := appAs(t, T, spec, "alice", httptest.NewRequest(http.MethodPost, "/apps/desk/_settings/reach/used/mcp:apptest-own", strings.NewReader(`{"used":false}`))).Code; c != 200 {
		t.Errorf("keeping it out of the app: %d", c)
	}
	var j appscript.Job
	withAppCredentials(&j, "alice", "desk")
	if j.CredentialLend("mcp:apptest-own") != "withhold" || len(j.CredentialsOff) != 1 || j.CredentialsOff[0] != "mcp:apptest-own" {
		t.Errorf("a run's rules: lend %q, off %v", j.CredentialLend("mcp:apptest-own"), j.CredentialsOff)
	}
	// Reached by a run without being named, it is listed all the same.
	stored, _ := loadSpec("alice", "desk")
	stored.DataSources = nil
	SaveAppSpec(stored)
	if got := rows("alice"); len(got) != 0 {
		t.Errorf("listed with nothing naming or using it: %v", got)
	}
	j.CredentialUsed("mcp:apptest-own")
	j.CredentialUsed("mcp:apptest-dep") // the deployment's: not the owner's to lend
	if got := rows("alice"); len(got) != 1 || got[0]["name"] != "mcp:apptest-own" || !strings.Contains(got[0]["how"].(string), "used since") {
		t.Errorf("after a run reached it: %v", got)
	}
	// An imported app's tool name mapped to one of its tools lists it too.
	applocal.UpdateAppCredentials("alice", "desk", func(c *applocal.AppCredentials) { c.Used = nil })
	applocal.SaveAppNeeds("alice", "desk", []applocal.AppNeed{{Kind: "tool", Name: "get_issue", Map: "apptest-own_get_issue"}})
	if got := rows("alice"); len(got) != 1 || got[0]["name"] != "mcp:apptest-own" || got[0]["how"] != "named by its scripts" {
		t.Errorf("mapped from an imported app's tool: %v", got)
	}
	appAs(t, T, spec, "carol", httptest.NewRequest(http.MethodGet, "/apps/desk/", nil)) // shared with carol
	applocal.UpdateAppCredentials("alice", "desk", func(c *applocal.AppCredentials) { c.Lend = nil; c.Off = nil })
	if got := T.appLends("carol"); len(got) != 0 {
		t.Errorf("a sign-in was offered as an account to connect: %v", got)
	}
}
