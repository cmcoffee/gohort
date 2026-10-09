package core

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type pinApp struct{ pins []DashboardCard }

func (pinApp) WebPath() string                                   { return "/apps" }
func (pinApp) WebName() string                                   { return "My Apps" }
func (pinApp) WebDesc() string                                   { return "apps" }
func (pinApp) RegisterRoutes(*http.ServeMux, string)             {}
func (p pinApp) DashboardPinnable(*http.Request) []DashboardCard { return p.pins }

// Each person picks what their own dashboard shows: a card they hide is gone
// from their page only, a card of their own they switch on appears, and
// nothing outside what they may see can be switched.
func TestADashboardShowsWhatItsViewerPicked(t *testing.T) {
	withUsers(t)
	host := dashboardHost{apps: []dashApp{
		{name: "Notes", desc: "notes", path: "/notes", app: pinApp{}},
		{name: "My Apps", desc: "apps", path: "/apps", app: pinApp{pins: []DashboardCard{{Name: "Voidrunner", Desc: "a space RPG", Path: "/apps/voidrunner", Group: "Your apps"}}}},
	}}
	items := func(user string) map[string]bool {
		w := httptest.NewRecorder()
		host.handleDashboardItems(w, asUser(t, "/api/dashboard/items", user))
		var got struct{ Records []dashItem }
		json.Unmarshal(w.Body.Bytes(), &got)
		out := map[string]bool{}
		for _, it := range got.Records {
			out[it.Path] = it.Shown
		}
		return out
	}
	show := func(user, path string, on bool) int {
		w := httptest.NewRecorder()
		body := `{"shown": false}`
		if on {
			body = `{"shown": true}`
		}
		r := asUser(t, "/api/dashboard/show?path="+path, user)
		r.Method, r.Body = http.MethodPost, io.NopCloser(strings.NewReader(body))
		host.handleDashboardShow(w, r)
		return w.Code
	}
	page := func(user string) string {
		w := httptest.NewRecorder()
		host.handleRoot(w, asUser(t, "/", user))
		return w.Body.String()
	}

	if got := items("craig"); !got["/notes"] || got["/apps/voidrunner"] {
		t.Fatalf("defaults: %v", got)
	}
	if code := show("craig", "/notes", false); code != http.StatusOK {
		t.Fatalf("hide: %d", code)
	}
	if code := show("craig", "/apps/voidrunner", true); code != http.StatusOK {
		t.Fatalf("pin: %d", code)
	}
	p := page("craig")
	if strings.Contains(p, `href="/notes/"`) || !strings.Contains(p, `href="/apps/voidrunner/"`) {
		t.Errorf("the page does not show what was picked")
	}
	if got := items("craig"); got["/notes"] || !got["/apps/voidrunner"] {
		t.Errorf("items after picking: %v", got)
	}
	if code := show("craig", "/apps/someone-elses", true); code != http.StatusNotFound {
		t.Errorf("a card the viewer cannot see was switched: %d", code)
	}
	if code := show("craig", "/notes", true); code != http.StatusOK || !items("craig")["/notes"] {
		t.Errorf("a hidden card could not be brought back")
	}
}

// A card moves within its section of the dashboard, the dashboard draws it
// there, and a move past the end of its section stays put.
func TestADashboardCardMovesWithinItsSection(t *testing.T) {
	withUsers(t)
	host := dashboardHost{apps: []dashApp{
		{name: "Alpha", desc: "a", path: "/alpha", app: pinApp{}},
		{name: "Beta", desc: "b", path: "/beta", app: pinApp{}},
		{name: "Gamma", desc: "c", path: "/gamma", app: pinApp{}},
	}}
	move := func(path, dir string) {
		w := httptest.NewRecorder()
		r := asUser(t, "/api/dashboard/move?path="+path+"&dir="+dir, "craig")
		r.Method = http.MethodPost
		host.handleDashboardMove(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("move %s %s: %d %s", path, dir, w.Code, w.Body.String())
		}
	}
	order := func() string {
		w := httptest.NewRecorder()
		host.handleRoot(w, asUser(t, "/", "craig"))
		page := w.Body.String()
		var seen []string
		for _, p := range []string{"/alpha/", "/beta/", "/gamma/"} {
			seen = append(seen, p)
		}
		sortByIndex(seen, page)
		return strings.Join(seen, " ")
	}
	if got := order(); got != "/alpha/ /beta/ /gamma/" {
		t.Fatalf("default: %s", got)
	}
	move("/gamma", "up")
	if got := order(); got != "/alpha/ /gamma/ /beta/" {
		t.Errorf("after gamma up: %s", got)
	}
	move("/alpha", "up")  // already first: stays
	move("/beta", "down") // already last: stays
	if got := order(); got != "/alpha/ /gamma/ /beta/" {
		t.Errorf("a move past the end changed the order: %s", got)
	}
}

// sortByIndex orders paths by where their card's link appears in page.
func sortByIndex(paths []string, page string) {
	idx := func(p string) int { return strings.Index(page, `href="`+p+`"`) }
	for i := 1; i < len(paths); i++ {
		for j := i; j > 0 && idx(paths[j]) < idx(paths[j-1]); j-- {
			paths[j], paths[j-1] = paths[j-1], paths[j]
		}
	}
}
