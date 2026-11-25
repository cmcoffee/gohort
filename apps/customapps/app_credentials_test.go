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
