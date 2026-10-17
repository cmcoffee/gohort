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

// A page listing the same things as dashboard cards (My Apps) shares the
// dashboard's one order: what it sets the dashboard draws, what the dashboard
// moves it reads, and cards it does not name stay where they were.
func TestAPageAndTheDashboardShareOneOrder(t *testing.T) {
	withUsers(t)
	pins := []DashboardCard{
		{Name: "Alpha", Path: "/apps/alpha", Group: "My apps"},
		{Name: "Beta", Path: "/apps/beta", Group: "My apps"},
		{Name: "Gamma", Path: "/apps/gamma", Group: "My apps"},
	}
	host := dashboardHost{apps: []dashApp{
		{name: "Notes", desc: "n", path: "/notes", app: pinApp{}},
		{name: "My Apps", desc: "a", path: "/apps", app: pinApp{pins: pins}},
	}}
	apps := []string{"/apps/alpha", "/apps/beta", "/apps/gamma"}
	myApps := func() string {
		var seen []string
		for _, a := range host.orderedCards(asUser(t, "/", "craig"), loadDashPrefs("craig")) {
			if strings.HasPrefix(a.path, "/apps/") {
				seen = append(seen, a.path)
			}
		}
		return strings.Join(seen, " ")
	}

	if got := strings.Join(DashboardOrderOf("craig", apps), " "); got != "/apps/alpha /apps/beta /apps/gamma" {
		t.Fatalf("with nothing placed, the given order should stand: %s", got)
	}
	SetDashboardOrderOf("craig", []string{"/apps/gamma", "/apps/alpha", "/apps/beta"})
	if got := myApps(); got != "/apps/gamma /apps/alpha /apps/beta" {
		t.Errorf("the dashboard does not draw the order the page set: %s", got)
	}

	// The dashboard's own move is read back by the page.
	w := httptest.NewRecorder()
	r := asUser(t, "/api/dashboard/move?path=/apps/beta&dir=up", "craig")
	r.Method = http.MethodPost
	host.handleDashboardMove(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("move: %d %s", w.Code, w.Body.String())
	}
	if got := strings.Join(DashboardOrderOf("craig", apps), " "); got != "/apps/gamma /apps/beta /apps/alpha" {
		t.Errorf("the page does not read the dashboard's move: %s", got)
	}

	// Setting the apps' order leaves every other card where it was.
	p := loadDashPrefs("craig")
	p.Order = []string{"/notes", "/apps/alpha", "/x", "/apps/beta"}
	saveDashPrefs("craig", p)
	SetDashboardOrderOf("craig", []string{"/apps/beta", "/apps/gamma", "/apps/alpha"})
	if got := strings.Join(loadDashPrefs("craig").Order, " "); got != "/notes /apps/beta /apps/gamma /apps/alpha /x" {
		t.Errorf("other cards moved: %s", got)
	}
}

type featuredApp struct{ pinApp }

func (featuredApp) WebFeatured() bool { return true }

type hubApp struct {
	pinApp
	order int
}

func (h hubApp) HubTab() (string, int) { return "hub", h.order }

func bandFixture() dashboardHost {
	return dashboardHost{apps: []dashApp{
		{name: "Home", desc: "home", path: "/home", app: featuredApp{}},
		{name: "Bridges", desc: "b", path: "/bridges", app: hubApp{order: 2}},
		{name: "Knowledge", desc: "k", path: "/knowledge", app: hubApp{order: 1}},
		{name: "Notes", desc: "n", path: "/notes", app: pinApp{}},
		{name: "Wiki", desc: "w", path: "/wiki", app: pinApp{}},
		{name: "Mail", desc: "m", path: "/mail", app: pinApp{}},
		{name: "My Apps", desc: "apps", path: "/apps", app: pinApp{pins: []DashboardCard{
			{Name: "Alpha", Desc: "a", Path: "/apps/alpha", Group: "My apps", GroupOrder: 10},
			{Name: "Beta", Desc: "b", Path: "/apps/beta", Group: "My apps", GroupOrder: 10},
		}}},
		{name: "Directory", desc: "d", path: "/orchestrate", app: cardApp{cards: []DashboardCard{{Name: "Helper", Desc: "an agent", Path: "/agents/helper", Group: "Agents", GroupOrder: 20}}}},
		{name: "Administrator", desc: "admin", path: adminAppPath, app: pinApp{}, order: 99},
	}}
}

func customizeBands(t *testing.T, host dashboardHost) string {
	t.Helper()
	w := httptest.NewRecorder()
	host.handleDashboardItems(w, asUser(t, "/api/dashboard/items", "craig"))
	var got struct{ Records []dashItem }
	json.Unmarshal(w.Body.Bytes(), &got)
	var out []string
	for _, it := range got.Records {
		out = append(out, it.Group+":"+it.Path)
	}
	return strings.Join(out, " ")
}

// The dashboard draws each section Customize lists as an outlined band of its
// own, in the same order: the featured card, the Orchestrator group, then a
// band per kind; the administrator's card comes after them all, in none.
func TestTheDashboardOutlinesTheSectionsCustomizeLists(t *testing.T) {
	withUsers(t)
	host := bandFixture()
	for _, path := range []string{"/apps/alpha", "/apps/beta", "/agents/helper"} {
		w := httptest.NewRecorder()
		r := asUser(t, "/api/dashboard/show?path="+path, "craig")
		r.Method, r.Body = http.MethodPost, io.NopCloser(strings.NewReader(`{"shown": true}`))
		host.handleDashboardShow(w, r)
	}
	want := "Featured:/home Orchestrator:/knowledge Orchestrator:/bridges " +
		"Apps:/orchestrate Apps:/mail Apps:/apps Apps:/notes Apps:/wiki " +
		"My apps:/apps/alpha My apps:/apps/beta Agents:/agents/helper :" + adminAppPath
	if got := customizeBands(t, host); got != want {
		t.Errorf("Customize bands:\n got  %s\n want %s", got, want)
	}
	w := httptest.NewRecorder()
	host.handleDashboardItems(w, asUser(t, "/api/dashboard/items", "craig"))
	var items struct{ Records []dashItem }
	json.Unmarshal(w.Body.Bytes(), &items)
	for _, it := range items.Records {
		if it.Fixed != (it.Path == adminAppPath) {
			t.Errorf("%s: fixed=%v; only the administrator's card stays put", it.Path, it.Fixed)
		}
	}

	w = httptest.NewRecorder()
	host.handleRoot(w, asUser(t, "/", "craig"))
	page := w.Body.String()
	at := func(s string) int { return strings.Index(page, s) }
	head := func(name string) string {
		return `<div class="cluster band"><div class="cluster-head"><span>` + name + `</span>`
	}
	for _, name := range []string{"Apps", "My apps", "Agents"} {
		if strings.Count(page, head(name)) != 1 {
			t.Errorf("the %s band is not drawn exactly once", name)
		}
	}
	if !(at(`href="/home/"`) < at(`cluster-head"><span>Orchestrator`) &&
		at(`cluster-head"><span>Orchestrator`) < at(head("Apps")) &&
		at(head("Apps")) < at(`href="/notes/"`) && at(`href="/notes/"`) < at(head("My apps")) &&
		at(head("My apps")) < at(`href="/apps/alpha/"`) && at(`href="/apps/alpha/"`) < at(head("Agents")) &&
		at(head("Agents")) < at(`href="/agents/helper/"`) && at(`href="/agents/helper/"`) < at(`href="`+adminAppPath+`/"`)) {
		t.Errorf("the bands, or the cards in them, are out of place")
	}
	if strings.Contains(page[at(`href="/agents/helper/"`):at(`href="`+adminAppPath+`/"`)], "cluster-head") {
		t.Errorf("the administrator's card was drawn in a band")
	}
}

// Dragging on Customize sets each band's order and nothing else: a card the
// list puts among another band's stays in its own, a path that is not one of
// the viewer's cards is not stored, and the administrator's card stays last.
func TestCustomizeOrdersEachBandAndNoCardLeavesIt(t *testing.T) {
	withUsers(t)
	host := bandFixture()
	order := func(user, body string) int {
		w := httptest.NewRecorder()
		r := asUser(t, "/api/dashboard/order", user)
		r.Method, r.Body = http.MethodPost, io.NopCloser(strings.NewReader(body))
		host.handleDashboardOrder(w, r)
		return w.Code
	}
	code := order("craig", `{"order":["/apps/beta","`+adminAppPath+`","/wiki","/nowhere","/mail","/apps/alpha","/notes"]}`)
	if code != http.StatusNoContent {
		t.Fatalf("saving the order: %d", code)
	}
	want := "Featured:/home Orchestrator:/knowledge Orchestrator:/bridges " +
		"Apps:/wiki Apps:/mail Apps:/notes Apps:/orchestrate Apps:/apps " +
		"My apps:/apps/beta My apps:/apps/alpha Agents:/agents/helper :" + adminAppPath
	if got := customizeBands(t, host); got != want {
		t.Errorf("after the drag:\n got  %s\n want %s", got, want)
	}
	if hasPath(loadDashPrefs("craig").Order, "/nowhere") {
		t.Error("a path that is not one of the viewer's cards was stored")
	}
	// The dashboard draws the same order.
	w := httptest.NewRecorder()
	host.handleRoot(w, asUser(t, "/", "craig"))
	paths := []string{"/notes/", "/mail/", "/wiki/"}
	sortByIndex(paths, w.Body.String())
	if got := strings.Join(paths, " "); got != "/wiki/ /mail/ /notes/" {
		t.Errorf("the dashboard draws %s", got)
	}

	w = httptest.NewRecorder()
	host.handleDashboardOrder(w, asUser(t, "/api/dashboard/order", "craig"))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("a GET was taken: %d", w.Code)
	}
	if code := order("", `{"order":[]}`); code != http.StatusUnauthorized {
		t.Errorf("nobody signed in: %d", code)
	}
}

// Ordering one band leaves the Orchestrator group in its tab order, on the
// dashboard and on Customize. The dashboard used to sort the group by tab
// order only while the person had no order at all, so a drag on My Apps
// reshuffled it by name.
func TestOrderingMyAppsLeavesTheOrchestratorGroupInTabOrder(t *testing.T) {
	withUsers(t)
	host := bandFixture()
	SetDashboardOrderOf("craig", []string{"/apps/beta", "/apps/alpha"})
	w := httptest.NewRecorder()
	host.handleRoot(w, asUser(t, "/", "craig"))
	paths := []string{"/bridges/", "/knowledge/"}
	sortByIndex(paths, w.Body.String())
	if got := strings.Join(paths, " "); got != "/knowledge/ /bridges/" {
		t.Errorf("the dashboard draws the group as %s", got)
	}
	if got := customizeBands(t, host); !strings.HasPrefix(got, "Featured:/home Orchestrator:/knowledge Orchestrator:/bridges ") {
		t.Errorf("Customize lists %s", got)
	}
}

func dashPage(t *testing.T, host dashboardHost, user string) string {
	t.Helper()
	w := httptest.NewRecorder()
	host.handleRoot(w, asUser(t, "/", user))
	return w.Body.String()
}

func dashPost(t *testing.T, h func(http.ResponseWriter, *http.Request), target, user, body string) int {
	t.Helper()
	w := httptest.NewRecorder()
	r := asUser(t, target, user)
	r.Method, r.Body = http.MethodPost, io.NopCloser(strings.NewReader(body))
	h(w, r)
	return w.Code
}

// Every section has a gear, the featured card on its corner, and a section
// with nothing switched on still gets its heading and gear. The first look
// counts every card already there as seen, so nothing is new at once; a card
// that arrives later is, on its section's gear and in its window, until the
// window is closed.
func TestEachSectionHasAGearThatMarksNewCards(t *testing.T) {
	withUsers(t)
	host := bandFixture()
	page := dashPage(t, host, "craig")
	for _, band := range []string{"Featured", "Orchestrator", "Apps", "My apps", "Agents"} {
		if !strings.Contains(page, `class="band-gear`) || !strings.Contains(page, `data-band="`+band+`"`) {
			t.Errorf("no gear for %s", band)
		}
	}
	if !strings.Contains(page, `<div class="hero-wrap"><button type="button" class="band-gear hero-gear" data-band="Featured"`) {
		t.Error("the featured card has no gear on its corner")
	}
	if !strings.Contains(page, `<div class="cluster band empty"><div class="cluster-head"><span>My apps</span>`) {
		t.Error("a section with nothing switched on was not drawn with its gear")
	}
	if strings.Contains(page, `<span class="band-new">`) {
		t.Error("cards already there on the first look read as new")
	}
	if strings.Contains(page, "/dashboard/customize") {
		t.Error("the page still links to Customize")
	}

	// A card that arrives later: an app shared with them.
	host.apps[6].app = pinApp{pins: append(host.apps[6].app.(pinApp).pins, DashboardCard{Name: "Gamma", Desc: "g", Path: "/apps/gamma", Group: "My apps", GroupOrder: 10})}
	if page := dashPage(t, host, "craig"); !strings.Contains(page, `data-band="My apps" title="Arrange My apps" aria-label="Arrange My apps">`) || !strings.Contains(page, `<span class="band-new">1 new</span>`) {
		t.Error("the new card is not counted on its section's gear")
	}
	w := httptest.NewRecorder()
	host.handleDashboardItems(w, asUser(t, "/api/dashboard/items?band=My+apps", "craig"))
	var got struct{ Records []dashItem }
	json.Unmarshal(w.Body.Bytes(), &got)
	var rows []string
	for _, it := range got.Records {
		rows = append(rows, it.Path+":"+it.Fresh)
	}
	if strings.Join(rows, " ") != "/apps/alpha: /apps/beta: /apps/gamma:new" {
		t.Errorf("the section's window lists %v", rows)
	}
	if code := dashPost(t, host.handleDashboardSeen, "/api/dashboard/seen?band=My+apps", "craig", ""); code != http.StatusNoContent {
		t.Fatalf("seen: %d", code)
	}
	if strings.Contains(dashPage(t, host, "craig"), `<span class="band-new">`) {
		t.Error("closing the window did not clear the new mark")
	}
}

// A section switched off takes its cards with it and is named at the foot,
// where it can be switched back on; a section the viewer does not have
// cannot be switched; and the Customize page sends people to the dashboard.
func TestASectionCanBeSwitchedOffAndBack(t *testing.T) {
	withUsers(t)
	host := bandFixture()
	dashPage(t, host, "craig")
	if code := dashPost(t, host.handleDashboardBand, "/api/dashboard/band?name=Apps", "craig", `{"shown": false}`); code != http.StatusNoContent {
		t.Fatalf("hide: %d", code)
	}
	page := dashPage(t, host, "craig")
	if strings.Contains(page, `href="/notes/"`) || strings.Contains(page, `<span>Apps</span>`) {
		t.Error("a hidden section was drawn")
	}
	if !strings.Contains(page, `<button type="button" class="band-show" data-band="Apps"`) {
		t.Error("a hidden section is not named at the foot")
	}
	if code := dashPost(t, host.handleDashboardBand, "/api/dashboard/band?name=Apps", "craig", `{"shown": true}`); code != http.StatusNoContent {
		t.Fatalf("show: %d", code)
	}
	if page := dashPage(t, host, "craig"); !strings.Contains(page, `href="/notes/"`) || strings.Contains(page, `class="band-show"`) {
		t.Error("the section did not come back")
	}
	if code := dashPost(t, host.handleDashboardBand, "/api/dashboard/band?name=Secrets", "craig", `{"shown": false}`); code != http.StatusNotFound {
		t.Errorf("a section the viewer does not have: %d", code)
	}
	w := httptest.NewRecorder()
	host.handleCustomize(w, asUser(t, "/dashboard/customize", "craig"))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Errorf("Customize answered %d %q", w.Code, w.Header().Get("Location"))
	}
}

// The gear's window is the framework's own draggable table, narrowed to the
// section by the script, with the new mark and a switch per card; closing it
// counts the section as seen.
func TestTheGearWindowIsTheSharedTable(t *testing.T) {
	out := sectionWindowHTML()
	start := strings.Index(out, `type="application/json">`) + len(`type="application/json">`)
	end := strings.Index(out[start:], "</script>")
	var cfg map[string]any
	if err := json.Unmarshal([]byte(strings.ReplaceAll(out[start:start+end], `<\/`, "</")), &cfg); err != nil {
		t.Fatalf("the embedded table: %v", err)
	}
	if cfg["type"] != "table" || cfg["reorder_url"] != "/api/dashboard/order" || cfg["source"] != "/api/dashboard/items?band=" {
		t.Errorf("the table: %v", cfg)
	}
	for _, want := range []string{"encodeURIComponent(name)", "onClose:", "/api/dashboard/seen?band=", "/api/dashboard/band?name=", "insertBefore(css, document.head.querySelector('style'))"} {
		if !strings.Contains(out, want) {
			t.Errorf("the window's script is missing %q", want)
		}
	}
}
