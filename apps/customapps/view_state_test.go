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

	. "github.com/cmcoffee/gohort/core"
)

func postViewState(t *testing.T, T *CustomApps, spec AppSpec, body string) int {
	t.Helper()
	return appAs(t, T, spec, spec.Owner, httptest.NewRequest(http.MethodPost, "/apps/"+spec.Slug+"/view-state", strings.NewReader(body))).Code
}

// A page's localStorage changes merge into the person's saved view, in the
// order they were made, and come back on the next load.
func TestViewStateMergesAndReturns(t *testing.T) {
	T := sharingTestApp(t)
	alice, bob := UserDB(T.DB, "alice"), UserDB(T.DB, "bob")
	spec := AppSpec{Slug: "prep", Owner: "alice"}
	if c := postViewState(t, T, spec, `{"set":{"tab":"logs","case":"CASE-1"}}`); c != 204 {
		t.Fatalf("save: %d", c)
	}
	postViewState(t, T, spec, `{"remove":["case"],"set":{"filter":"errors"}}`)
	got := loadViewState(alice, "prep")
	if len(got) != 2 || got["tab"] != "logs" || got["filter"] != "errors" {
		t.Errorf("alice's view: %v", got)
	}
	if len(loadViewState(bob, "prep")) != 0 || len(loadViewState(alice, "other")) != 0 {
		t.Error("the view leaked to another person or another app")
	}
	postViewState(t, T, spec, `{"clear":true,"set":{"tab":"summary"}}`)
	if got := loadViewState(alice, "prep"); len(got) != 1 || got["tab"] != "summary" {
		t.Errorf("after clear: %v", got)
	}
	big := strings.Repeat("x", viewStateMax)
	if c := postViewState(t, T, spec, `{"set":{"blob":"`+big+`"}}`); c != http.StatusRequestEntityTooLarge {
		t.Errorf("an oversized view was kept: %d", c)
	}
	if got := loadViewState(alice, "prep"); got["tab"] != "summary" || got["blob"] != "" {
		t.Errorf("a refused save changed the view: %d keys", len(got))
	}
	// Reset view asks first, saying the records are kept: opening the link
	// clears nothing. Confirming clears the view, from this site only.
	w := httptest.NewRecorder()
	T.handleViewReset(w, httptest.NewRequest(http.MethodGet, "/apps/prep/_view/reset", nil), alice, spec)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "records are kept") || len(loadViewState(alice, "prep")) == 0 {
		t.Errorf("opening Reset view: %d, view %v", w.Code, loadViewState(alice, "prep"))
	}
	cross := httptest.NewRequest(http.MethodPost, "/apps/prep/_view/reset", nil)
	cross.Header.Set("Sec-Fetch-Site", "cross-site")
	w = httptest.NewRecorder()
	T.handleViewReset(w, cross, alice, spec)
	if w.Code != http.StatusForbidden || len(loadViewState(alice, "prep")) == 0 {
		t.Errorf("a reset from another site: %d, view %v", w.Code, loadViewState(alice, "prep"))
	}
	w = httptest.NewRecorder()
	T.handleViewReset(w, httptest.NewRequest(http.MethodPost, "/apps/prep/_view/reset", nil), alice, spec)
	if w.Code != http.StatusOK || len(loadViewState(alice, "prep")) != 0 {
		t.Errorf("reset: %d %v", w.Code, loadViewState(alice, "prep"))
	}
}

// Every open starts fresh, reloads included: each frame gets its own empty
// localStorage, and nothing the app saved for the person is put into it.
func TestEveryOpenStartsFresh(t *testing.T) {
	T := sharingTestApp(t)
	spec := AppSpec{Slug: "prep", Owner: "alice", RecordKey: "id",
		Page: json.RawMessage(`{"title":"Prep","sections":[{"body":{"type":"frame","html":"<p>x</p>"}}]}`)}
	postViewState(t, T, spec, `{"set":{"step":"\"three\""}}`)
	body := appAs(t, T, spec, "alice", httptest.NewRequest(http.MethodGet, "/apps/prep/", nil)).Body.String()
	if !strings.Contains(body, "(function(){var d={};function own(k)") || strings.Contains(body, "three") {
		t.Errorf("the page did not start fresh: fresh storage in its frame %v, carries the saved step %v",
			strings.Contains(body, "(function(){var d={};function own(k)"), strings.Contains(body, "three"))
	}
	if strings.Contains(body, `fetch(\"view-state\"`) && strings.Contains(body, "pagehide") {
		t.Error("the page's storage still saves itself to the server")
	}
	found := false
	for _, p := range appOwnPaths {
		found = found || p == "view-state"
	}
	if !found {
		t.Error("the page may not reach app.state")
	}
}

// The ⚙ is for settings and for what the app saved for the person: someone
// with neither has no ⚙; once the app saves something for them with
// app.state, its Settings page says so with a Clear. The owner, who always
// has a Settings page, sees no such line while nothing is saved.
func TestTheGearShowsWhatTheAppSavedForThePerson(t *testing.T) {
	T := sharingTestApp(t)
	spec := AppSpec{Slug: "prep", Name: "Prep", Owner: "alice", RecordKey: "id",
		Page: json.RawMessage(`{"title":"Prep","sections":[]}`)}
	page := func(user string) string {
		return appAs(t, T, spec, user, httptest.NewRequest(http.MethodGet, "/apps/prep/", nil)).Body.String()
	}
	settings := func(user string) string {
		return appAs(t, T, spec, user, httptest.NewRequest(http.MethodGet, "/apps/prep/_settings", nil)).Body.String()
	}
	if !strings.Contains(page("alice"), "Settings...") {
		t.Fatal("the owner sees no gear, so the checks below would prove nothing")
	}
	if strings.Contains(page("bob"), "Settings...") {
		t.Fatal("bob, with nothing behind the gear, was shown one")
	}
	if c := appAs(t, T, spec, "bob", httptest.NewRequest(http.MethodPost, "/apps/prep/view-state", strings.NewReader(`{"set":{"step":"3"}}`))).Code; c != 204 {
		t.Fatalf("bob's app.state: %d", c)
	}
	if !strings.Contains(page("bob"), "Settings...") {
		t.Error("bob has something saved and no gear to clear it from")
	}
	if s := settings("bob"); !strings.Contains(s, "What it saved for you") || !strings.Contains(s, "/prep/_view/reset") {
		t.Errorf("bob's settings do not offer to clear what it saved")
	}
	if strings.Contains(settings("alice"), "What it saved for you") {
		t.Error("the owner, with nothing saved, is offered to clear it")
	}
}

// window.app.state keeps what the app chooses between visits: get reads one
// value back as it was set (or a value saved before app.state as its text),
// set, remove and clear change only what they name.
func TestThePageHelperKeepsAppState(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	js := strings.TrimSuffix(strings.TrimPrefix(appPageHelper, "<script>"), "</script>")
	store := strings.TrimSuffix(strings.TrimPrefix(freshStorageScript, "<script>"), "</script>")
	harness := `
var calls = [], window = {}, saved = {old: "plain text", step: JSON.stringify({n: 3})};
function resp(status, body) { return {ok: true, status: status, text: function() { return Promise.resolve(body == null ? "" : JSON.stringify(body)); }}; }
global.fetch = function(u, o) {
  calls.push((o && o.method || "GET") + " " + u + (o && o.body ? " " + o.body : ""));
  if (o && o.method === "POST") return Promise.resolve(resp(204, null));
  return Promise.resolve(resp(200, saved));
};
` + js + `
` + store + `
var app = window.app, out = {};
window.localStorage.setItem("a", "1");
out.storage = window.localStorage.getItem("a");
app.state.get("step").then(function(v) {
  out.step = v.n;
  return app.state.get("old");
}).then(function(v) {
  out.old = v;
  return app.state.get("none");
}).then(function(v) {
  out.none = v;
  return app.state.set("step", {n: 4});
}).then(function() { return app.state.remove("old"); })
  .then(function() { return app.state.clear(); })
  .then(function() { console.log(JSON.stringify({out: out, calls: calls})); });
`
	f := filepath.Join(t.TempDir(), "s.js")
	if err := os.WriteFile(f, []byte(harness), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := exec.Command(node, f).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, b)
	}
	got := string(b)
	for _, want := range []string{`"step":3`, `"old":"plain text"`, `"none":null`, `"storage":"1"`,
		`POST view-state {\"set\":{\"step\":\"{\\\"n\\\":4}\"}}`, `POST view-state {\"remove\":[\"old\"]}`, `POST view-state {\"clear\":true}`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in\n%s", want, got)
		}
	}
}
