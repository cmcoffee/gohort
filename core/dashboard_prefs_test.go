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
		{name: "My Apps", desc: "apps", path: "/apps", app: pinApp{pins: []DashboardCard{{Name: "Voidrunner", Desc: "a space RPG", Path: "/apps/voidrunner", Group: "My apps"}}}},
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

type cardApp struct{ cards []DashboardCard }

func (cardApp) WebPath() string                                { return "/orchestrate" }
func (cardApp) WebName() string                                { return "Agents" }
func (cardApp) WebDesc() string                                { return "agents" }
func (cardApp) RegisterRoutes(*http.ServeMux, string)          {}
func (c cardApp) DashboardCards(*http.Request) []DashboardCard { return c.cards }

// The Customize page lists cards by kind: the dashboard's own apps first,
// then each source's group in the order the source declares, so a person
// finds an app under Apps, their own under My apps and an agent under
// Agents, whichever source made the card.
func TestTheCustomizePageGroupsCardsByKind(t *testing.T) {
	withUsers(t)
	host := dashboardHost{apps: []dashApp{
		{name: "Agents", desc: "agents", path: "/orchestrate", app: cardApp{cards: []DashboardCard{{Name: "Helper", Desc: "a published agent", Path: "/agents/helper", Group: "Agents", GroupOrder: 20}}}},
		{name: "My Apps", desc: "apps", path: "/apps", app: pinApp{pins: []DashboardCard{{Name: "Voidrunner", Desc: "a space RPG", Path: "/apps/voidrunner", Group: "My apps", GroupOrder: 10}}}},
		{name: "Notes", desc: "notes", path: "/notes", app: pinApp{}},
	}}
	w := httptest.NewRecorder()
	host.handleDashboardItems(w, asUser(t, "/api/dashboard/items", "craig"))
	var got struct{ Records []dashItem }
	json.Unmarshal(w.Body.Bytes(), &got)
	var groups []string
	for _, it := range got.Records {
		groups = append(groups, it.Group+":"+it.Name)
	}
	want := "Apps:Agents Apps:My Apps Apps:Notes My apps:Voidrunner Agents:Helper"
	if s := strings.Join(groups, " "); s != want {
		t.Errorf("groups: %s", s)
	}
	for _, it := range got.Records {
		if it.Path == "/apps/voidrunner" && it.Note != "only on your dashboard" {
			t.Errorf("a card of the viewer's own does not say so: %q", it.Note)
		}
		if it.Path == "/agents/helper" && it.Note != "" {
			t.Errorf("a published card carries a note: %q", it.Note)
		}
	}
}

// The administrator's card is last on the dashboard and on the Customize
// page whatever else is there, and the arrows do not move anything past it.
func TestTheAdministratorCardStaysLast(t *testing.T) {
	withUsers(t)
	host := dashboardHost{apps: []dashApp{
		{name: "Administrator", desc: "admin", path: adminAppPath, app: pinApp{}, order: 99},
		{name: "Notes", desc: "notes", path: "/notes", app: pinApp{pins: []DashboardCard{{Name: "Voidrunner", Desc: "a space RPG", Path: "/apps/voidrunner", Group: "My apps", GroupOrder: 10}}}},
	}}
	w := httptest.NewRecorder()
	r := asUser(t, "/api/dashboard/show?path=/apps/voidrunner", "craig")
	r.Method, r.Body = http.MethodPost, io.NopCloser(strings.NewReader(`{"shown": true}`))
	host.handleDashboardShow(w, r)
	last := func(where string) string {
		w := httptest.NewRecorder()
		if where == "page" {
			host.handleRoot(w, asUser(t, "/", "craig"))
			paths := []string{"/notes/", "/apps/voidrunner/", adminAppPath + "/"}
			sortByIndex(paths, w.Body.String())
			return paths[len(paths)-1]
		}
		host.handleDashboardItems(w, asUser(t, "/api/dashboard/items", "craig"))
		var got struct{ Records []dashItem }
		json.Unmarshal(w.Body.Bytes(), &got)
		return got.Records[len(got.Records)-1].Path + "/"
	}
	for _, where := range []string{"page", "items"} {
		if got := last(where); got != adminAppPath+"/" {
			t.Errorf("%s: last card is %s, want the administrator", where, got)
		}
	}
	w = httptest.NewRecorder()
	r = asUser(t, "/api/dashboard/move?path="+adminAppPath+"&dir=up", "craig")
	r.Method = http.MethodPost
	host.handleDashboardMove(w, r)
	if got := last("page"); got != adminAppPath+"/" {
		t.Errorf("after a move: last card is %s, want the administrator", got)
	}
}
