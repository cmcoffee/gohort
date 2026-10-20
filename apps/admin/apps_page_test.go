package admin

// The Apps tab is one table of every app, and a row with something to set
// opens that app's own page. These drive the table's endpoint and the page
// through the admin mux, as a browser reaches them.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

// authoredHeader turns on the test's authored apps. The source registry is
// for the whole process, so its rows appear only for a request that asks.
const authoredHeader = "X-Apps-Page-Test"

func init() {
	RegisterApp(fakeWebApp{path: "/pagetestclaims"})
	RegisterRouteStage(RouteStage{Key: "app.pagetestclaims", Label: "P", App: "/pagetestclaims"})
	RegisterApp(fakeWebApp{path: "/pagetestbare"})
	RegisterAdminAppSource(func(r *http.Request) []AdminAppEntry {
		if r.Header.Get(authoredHeader) == "" {
			return nil
		}
		return []AdminAppEntry{
			{Key: "carol/notes", Name: "Notes", Owner: "carol", Access: "its owner only", Settings: "2 controls",
				Page: func() []ui.Section {
					return []ui.Section{{Title: "Operator controls", Body: ui.EmptyState{Title: "notes controls"}}}
				},
				Head: "<!-- notes head -->"},
			{Key: "carol/bare", Name: "Bare", Owner: "carol", Disabled: true},
		}
	})
}

func appsPageMux(t *testing.T) func(path string) (int, string) {
	db := &DBase{Store: kvlite.MemStore()}
	db.Set(LLMTable, "provider", "openai") // configured, so no setup-wizard redirect
	prev := AuthDB
	AuthDB = func() Database { return db }
	t.Cleanup(func() { AuthDB = prev })
	a := &AdminApp{db: db}
	mux := http.NewServeMux()
	a.RegisterRoutes(mux, "/admin")
	return func(path string) (int, string) {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set(authoredHeader, "1")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
}

func TestTheAppsTableListsEveryAppAndOpensTheOnesWithSettings(t *testing.T) {
	get := appsPageMux(t)
	code, body := get("/admin/api/app-switches")
	if code != http.StatusOK {
		t.Fatalf("the table's source: %d %s", code, body)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		t.Fatal(err)
	}
	by := map[string]map[string]any{}
	lastBuiltIn, firstAuthored := -1, len(rows)
	for i, rw := range rows {
		by[rw["key"].(string)] = rw
		if rw["group"] == "Built in" {
			lastBuiltIn = i
		} else if i < firstAuthored {
			firstAuthored = i
		}
	}
	if lastBuiltIn > firstAuthored {
		t.Error("the apps that ship should come before the authored ones, or the bands interleave")
	}

	claims, bare := by["/pagetestclaims"], by["/pagetestbare"]
	if claims == nil || bare == nil {
		t.Fatalf("a shipped app is missing from the table: %v", rows)
	}
	if claims["href"] != "/admin/app?path=%2Fpagetestclaims" || claims["settings"] != "1 routing dial" {
		t.Errorf("an app with a setting should open its page: %v", claims)
	}
	if bare["href"] != "" || bare["settings"] != "" {
		t.Errorf("an app with nothing to set should open nothing: %v", bare)
	}
	if bare["switchable"] != true || bare["state"] != "enabled" || bare["access"] == "" {
		t.Errorf("a shipped app is switchable and says who can open it: %v", bare)
	}
	// A hidden app with a pane has no switch: it is plumbing other apps use.
	if h := by["/hiddenpanetest"]; h == nil || h["switchable"] != false || h["state"] != "always_on" || h["href"] == "" {
		t.Errorf("a hidden app with settings: %v", h)
	}

	notes, aBare := by["authored:carol/notes"], by["authored:carol/bare"]
	if notes == nil || aBare == nil {
		t.Fatalf("an authored app is missing from the table: %v", rows)
	}
	if notes["group"] != "Built here" || notes["owner"] != "carol" || notes["href"] != "/admin/app?key=carol%2Fnotes" ||
		notes["access"] != "its owner only" || notes["settings"] != "2 controls" {
		t.Errorf("an authored app's row: %v", notes)
	}
	if notes["switchable"] == true {
		t.Error("the shipped-app switch would refuse an authored app; it should not be offered")
	}
	if aBare["href"] != "" || aBare["state"] != "disabled" {
		t.Errorf("an authored app with nothing to set: %v", aBare)
	}
}

func TestAnAppHasItsOwnPage(t *testing.T) {
	get := appsPageMux(t)
	code, page := get("/admin/app?path=/pagetestclaims")
	if code != http.StatusOK {
		t.Fatalf("a shipped app's page: %d", code)
	}
	for _, want := range []string{
		`"title":"Fake /pagetestclaims"`, // the app's name heads the page
		`"back_url":"/admin/apps"`,       // and the back arrow returns to the table
		`"title":"About"`, `"title":"Model routing"`,
		`"url":"/admin/apps","active":true`, // Apps stays marked in the menu
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the app's page lacks %s", want)
		}
	}
	if strings.Contains(page, "api/app-switches") {
		t.Error("the app's page also carries the table")
	}

	code, page = get("/admin/app?key=carol/notes")
	if code != http.StatusOK {
		t.Fatalf("an authored app's page: %d", code)
	}
	for _, want := range []string{`"title":"Notes"`, "notes controls", "<!-- notes head -->"} {
		if !strings.Contains(page, want) {
			t.Errorf("the authored app's page lacks %s", want)
		}
	}

	for _, url := range []string{"/admin/app?path=/nothing-serves-this", "/admin/app?key=carol/nobody", "/admin/app?key=carol/bare", "/admin/app"} {
		if code, _ := get(url); code != http.StatusNotFound {
			t.Errorf("%s: %d, want a 404", url, code)
		}
	}
}
