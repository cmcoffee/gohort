package customapps

import (
	"encoding/json"
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
	w := httptest.NewRecorder()
	T.handleViewReset(w, httptest.NewRequest(http.MethodGet, "/apps/prep/_view/reset", nil), alice, spec)
	if w.Code != http.StatusFound || len(loadViewState(alice, "prep")) != 0 {
		t.Errorf("reset: %d %v", w.Code, loadViewState(alice, "prep"))
	}
}

// The page arrives with the person's view inside every isolated frame, after
// the page helper, and offers Reset view only when there is one to reset.
func TestThePageCarriesTheSavedView(t *testing.T) {
	T := sharingTestApp(t)
	alice := UserDB(T.DB, "alice")
	spec := AppSpec{Slug: "prep", Owner: "alice"}
	page := isolateAppHTML(json.RawMessage(`{"sections":[{"body":{"type":"frame","html":"<p>x</p>"}}]}`), "/apps/prep/")
	fresh := string(T.withViewState(page, alice, spec))
	if !strings.Contains(fresh, `localStorage`) || strings.Contains(fresh, "Reset view") {
		t.Errorf("a fresh page: storage script present=%v, reset offered=%v", strings.Contains(fresh, "localStorage"), strings.Contains(fresh, "Reset view"))
	}
	postViewState(t, T, spec, `{"set":{"note":"</script><b>hi</b>","tab":"logs"}}`)
	out := T.withViewState(page, alice, spec)
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
	if !strings.Contains(string(out), "Reset view") || !strings.Contains(string(out), "/prep/_view/reset") {
		t.Error("no Reset view on a page with a saved view")
	}
	found := false
	for _, p := range appOwnPaths {
		found = found || p == "view-state"
	}
	if !found {
		t.Error("the page may not reach view-state")
	}
}
