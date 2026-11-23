package customapps

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// The page arrives with the person's view inside every isolated frame, after
// the page helper, and says whether there is one to reset: Reset view is on
// the Settings page then, not in the header.
func TestThePageCarriesTheSavedView(t *testing.T) {
	T := sharingTestApp(t)
	alice := UserDB(T.DB, "alice")
	spec := AppSpec{Slug: "prep", Owner: "alice"}
	page := isolateAppHTML(json.RawMessage(`{"sections":[{"body":{"type":"frame","html":"<p>x</p>"}}]}`), "/apps/prep/")
	freshOut, hasView := T.withViewState(page, alice, spec)
	fresh := string(freshOut)
	if !strings.Contains(fresh, `localStorage`) || hasView || viewSection(alice, spec, "/apps/prep") != nil {
		t.Errorf("a fresh page: storage script present=%v, a view to reset=%v", strings.Contains(fresh, "localStorage"), hasView)
	}
	postViewState(t, T, spec, `{"set":{"note":"</script><b>hi</b>","tab":"logs"}}`)
	out, hasView := T.withViewState(page, alice, spec)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	body := m["sections"].([]any)[0].(map[string]any)["body"].(map[string]any)
	pre, _ := body["isolate_prelude"].(string)
	if !strings.HasPrefix(pre, appPagePrelude) || !strings.Contains(pre, `"tab":"logs"`) {
		t.Errorf("prelude does not carry the view after the helper: %.200s", pre)
	}
	if strings.Count(pre, "</script>") != strings.Count(appPagePrelude, "</script>")+1 {
		t.Error("a saved value closed the script it rides in")
	}
	if !hasView || strings.Contains(string(out), "Reset view") {
		t.Errorf("a saved view: reported %v, still in the header %v", hasView, strings.Contains(string(out), "Reset view"))
	}
	if sec := viewSection(alice, spec, "/apps/prep"); sec == nil || !strings.Contains(fmt.Sprint(sec.Body), "/apps/prep/_view/reset") {
		t.Errorf("Settings offers no Reset view for a saved view: %+v", sec)
	}
	found := false
	for _, p := range appOwnPaths {
		found = found || p == "view-state"
	}
	if !found {
		t.Error("the page may not reach view-state")
	}
}

// Reset view and Recent runs are behind the ⚙, not in the header: someone the
// app is shared with gets the ⚙ once they have a view to reset, though they
// have nothing else there, and only the owner is offered the runs.
func TestResetViewAndRecentRunsAreBehindTheGear(t *testing.T) {
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
		t.Fatal("bob, with nothing of his behind the gear, was shown one")
	}
	if c := appAs(t, T, spec, "bob", httptest.NewRequest(http.MethodPost, "/apps/prep/view-state", strings.NewReader(`{"set":{"tab":"logs"}}`))).Code; c != 204 {
		t.Fatalf("bob's view: %d", c)
	}
	if p := page("bob"); !strings.Contains(p, "Settings...") || strings.Contains(p, "Reset view") {
		t.Errorf("bob with a saved view: gear %v, Reset view in the header %v", strings.Contains(p, "Settings..."), strings.Contains(p, "Reset view"))
	}
	if s := settings("bob"); !strings.Contains(s, "Reset view") || !strings.Contains(s, "/prep/_view/reset") || strings.Contains(s, "Recent runs") {
		t.Errorf("bob's settings: Reset view %v, Recent runs %v", strings.Contains(s, "Reset view"), strings.Contains(s, "Recent runs"))
	}
	if s := settings("alice"); !strings.Contains(s, "Recent runs") || !strings.Contains(s, "/prep/_runs") || strings.Contains(s, "Reset view") {
		t.Errorf("alice's settings: Recent runs %v, Reset view (she has no saved view) %v", strings.Contains(s, "Recent runs"), strings.Contains(s, "Reset view"))
	}
}
