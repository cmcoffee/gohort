// The Apps tab: admin organised by SUBJECT rather than only by mechanism.
//
// Everything configurable about an app is otherwise spread across tabs by the
// kind of thing it is — its tier is a route stage under LLMs, its knobs are
// tunables under Tuning, its own settings are a contributed section under
// Extensions. That grouping is genuinely useful ("show me all routing at once"
// is what you want when chasing a bill) and it stays. This is the other axis:
// one row per app, and a page per app showing what belongs to THAT app.
//
// The tab is one table, read down a column: which apps are running, who can
// open each, and what can be set on it. A row with something to set opens
// that app's own page (handleAppPage), whose sections are its controls. It was
// a section per app, and the tab's index of sections grew with every app.
//
// Custom apps join the same table without admin importing them: they arrive
// through the AdminAppSource that apps/customapps registers. The apps that
// ship come first and the authored ones follow, each in a band of its own.
//
// See docs/design/apps-tab.md.
package admin

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// AppsTabGroup is the tab both admin and the custom-app source render into.
const AppsTabGroup = "Apps"

// appsTabSections is the Apps tab: the one table of apps.
func (a *AdminApp) appsTabSections(r *http.Request) []ui.Section {
	return newPanes(r).sections()
}

// panes is one request's view of the Apps tab. It reads the contributed
// admin sections ONCE: every source runs on each read (the custom-app source
// loads every user's app specs), and the tab asked the question once per
// app per check, some fifty reads for a page that needed one. That was most
// of the time the admin page took to build.
type panes struct {
	r       *http.Request
	entries []AdminSectionEntry
	// authored is the custom apps, read on first use: only the table and an
	// authored app's page ask, and the source builds every app's controls.
	authored     []AdminAppEntry
	readAuthored bool
}

func newPanes(r *http.Request) *panes {
	return &panes{r: r, entries: AdminSectionEntriesFor(r)}
}

// sections is the Apps tab: the table, and nothing else. An app's controls
// are on its own page.
func (p *panes) sections() []ui.Section {
	return []ui.Section{appsListSection()}
}

// authoredApps is the custom apps, from the registered sources.
func (p *panes) authoredApps() []AdminAppEntry {
	if !p.readAuthored {
		p.authored, p.readAuthored = AdminAppEntriesFor(p.r), true
	}
	return p.authored
}

// authoredApp is the custom app with this key, or nil.
func (p *panes) authoredApp(key string) *AdminAppEntry {
	for i, e := range p.authoredApps() {
		if e.Key == key {
			return &p.authoredApps()[i]
		}
	}
	return nil
}

// paneApps is every app that gets a pane: each app on the switchboard, plus
// each HIDDEN app that has claimed a control.
//
// Hidden apps are kept off the switchboard because switching off the plumbing
// other apps stand on is a trap (see listableApps), and that still holds. But
// several of them exist mostly to be configured (the prompt-block editor, the
// file store, publishing destinations), so "what can I change about this app"
// is exactly the question their pane answers. A pane is not a switch, so
// listing them here costs nothing the switchboard was protecting. A hidden app
// that claims nothing stays out: it has nothing an operator could look for.
func paneApps(r *http.Request) []appRow { return newPanes(r).apps() }

func (p *panes) apps() []appRow {
	rows := listableApps()
	for _, wa := range AllWebApps() {
		if wa.WebPath() == "/admin" || !appIsHidden(wa) || !p.claims(wa.WebPath()) {
			continue
		}
		rows = append(rows, appRow{path: wa.WebPath(), name: wa.WebName(), desc: wa.WebDesc(), hidden: true})
	}
	sort.Slice(rows, func(i, j int) bool { return strings.ToLower(rows[i].name) < strings.ToLower(rows[j].name) })
	return rows
}

// claimsAnything reports whether an app has claimed a route stage, a tunable or
// an admin section, which is whether its page has anything to set.
func claimsAnything(r *http.Request, path string) bool { return newPanes(r).claims(path) }

func (p *panes) claims(path string) bool {
	return len(RouteStagesForApp(path)) > 0 || len(TunablesForApp(path)) > 0 ||
		len(p.elsewhere(path)) > 0 || len(p.onPage(path)) > 0
}

// appPageSections is one app's page: what it is, then a section for each kind
// of control it has claimed.
//
// The summary comes first and stands alone for an app that claims nothing,
// because that is the true answer for it. Claimed routing follows as the same
// table the LLMs tab shows, then claimed knobs as the same fields the Tuning
// tab shows, each filtered to this app and writing the same keys; a change in
// either place reaches the other, so neither view contradicts the other.
func appPageSections(r *http.Request, path string) []ui.Section { return newPanes(r).page(path) }

func (p *panes) page(path string) []ui.Section {
	secs := []ui.Section{{
		Title: "About",
		Body: ui.DisplayPanel{
			Source: "api/app-summary?path=" + path,
			Pairs: []ui.DisplayPair{
				{Label: "Path", Field: "path", Mono: true},
				{Label: "State", Field: "state", StatusField: "state_severity"},
				{Label: "What it is", Field: "desc"},
				{Label: "Who can open it", Field: "access"},
				{Label: "Its own controls", Field: "controls"},
			},
		},
	}}
	if len(RouteStagesForApp(path)) > 0 {
		secs = append(secs, ui.Section{
			Title:    "Model routing",
			Subtitle: "The model tier each of its steps runs on. The same dials as LLMs › LLM Routing: a change in one is in the other.",
			Body:     routingTable(path, []string{"api/routing"}),
		})
	}
	if form := appTuningForm(path); form != nil {
		secs = append(secs, ui.Section{
			Title:    "Tuning",
			Subtitle: "Its limits and timings. The same settings as the Tuning tab: a change in one is in the other.",
			Body:     *form,
		})
	}
	// A section the app filed under Apps is drawn here, its one place: the
	// tab itself is the table (see onPage).
	for _, e := range p.onPage(path) {
		secs = append(secs, e.Section)
	}
	// Panels on other tabs are LINKED, not rendered here. A contributed section is a whole
	// surface, often with state and client actions of its own, and two live
	// copies of one on a page is two editors over one record. The link opens
	// it on the tab it already lives on.
	if len(p.elsewhere(path)) > 0 {
		secs = append(secs, ui.Section{
			Title:    "Other settings",
			Subtitle: "Settings it keeps on other tabs. Each link opens the tab it is on.",
			Body: ui.Table{
				Source: appPanelsSource(path),
				RowKey: "href",
				Columns: []ui.Col{
					{Field: "title", Label: "Settings panel", Link: "href", Flex: 1},
					{Field: "tab", Label: "On tab", Mute: true},
				},
			},
		})
	}
	for i := range secs {
		secs[i].Group, secs[i].Wide = AppsTabGroup, true
	}
	return secs
}

// appIsHidden mirrors the dashboard's rule: an app that opts out of being
// listed is not administered here either, because it is not a surface anybody
// is being given access to.
func appIsHidden(wa WebApp) bool {
	type hidden interface{ WebHidden() bool }
	h, ok := wa.(hidden)
	return ok && h.WebHidden()
}

// appRow is one administrable app: what it is called, where it is mounted,
// and what it says it does. hidden marks an app that has a pane but no switch.
type appRow struct {
	path, name, desc string
	hidden           bool
}

// listableApps enumerates the compiled apps this tab administers, by name.
//
// The two exclusions are the same ones the tab has always made, and both
// matter more now that the tab can switch things off. The administrator panel
// is left out because disabling it would remove the only surface that can
// re-enable anything. Hidden apps are left out because they are not surfaces
// anybody was given access to in the first place — they are the plumbing other
// apps are built on (the account page, the monitor, the OpenAI-compatible
// endpoint), and a switch that could take the account page away while
// presenting itself as a list of apps would be a trap.
func listableApps() []appRow {
	var rows []appRow
	for _, wa := range AllWebApps() {
		if wa.WebPath() == "/admin" || appIsHidden(wa) {
			continue
		}
		rows = append(rows, appRow{path: wa.WebPath(), name: wa.WebName(), desc: wa.WebDesc()})
	}
	sort.Slice(rows, func(i, j int) bool { return strings.ToLower(rows[i].name) < strings.ToLower(rows[j].name) })
	return rows
}

// appsListSection is the tab's table: every app the deployment ships, then
// every app people here have built, one row each.
//
// A table because the question it answers is comparative (which of these are
// we running, and who can open them) and that is read down a column. A row
// opens the app's own page when there is something on it to set.
func appsListSection() ui.Section {
	return ui.Section{
		Title:    "Apps",
		Subtitle: "Every app on this deployment. Switch one off to take it away from everyone; open one to change its settings.",
		Detail: "Switching an app off takes its dashboard card away for everyone, and its pages and API answer 503 until it is switched back on. " +
			"It takes effect immediately, with no restart. Per-user grants are left untouched, so switching an app back on restores exactly the access it had. " +
			"The switch governs the app's web surface only: tools, scheduled tasks and routing an app registered at startup keep running. " +
			"The administrator panel and the framework's own internal apps (your account page, the monitor, the API endpoints) have no switch: " +
			"they are what you would need to get back. Apps people built with the Builder are switched off from their own page, by the owner's controls there.",
		Group: AppsTabGroup,
		Wide:  true,
		Body: ui.Table{
			Source:  "api/app-switches",
			RowKey:  "key",
			RowLink: "href",
			GroupBy: "group",
			Search:  true,
			Columns: []ui.Col{
				{Field: "name", Label: "App", Flex: 1},
				{Field: "owner", Label: "Built by", Mute: true},
				{Field: "state", Label: "State", Type: "badge", Badges: []ui.BadgeMapping{
					{Value: "enabled", Label: "Enabled", Color: "success"},
					{Value: "disabled", Label: "Disabled", Color: "danger"},
					{Value: "always_on", Label: "Always on", Color: "mute"},
				}},
				{Field: "access", Label: "Who can open it", Flex: 2, Mute: true},
				{Field: "settings", Label: "Settings", Mute: true},
				{Field: "desc", Flex: 3, Mute: true, Line: 2},
			},
			RowActions: []ui.RowAction{
				{Type: "toggle", Field: "enabled", Leading: true, OnlyIf: "switchable",
					PostTo: "api/app-switches?path={path}", Method: "POST"},
			},
			EmptyText: "No apps registered.",
		},
	}
}

// handleApps lists every app for the tab's table (GET) and flips a shipped one
// on or off (POST ?path=<mount>, body {"enabled": bool}).
func (a *AdminApp) handleApps(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodPost {
		path := strings.TrimSpace(r.URL.Query().Get("path"))
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request body", http.StatusBadRequest)
			return
		}
		// Only apps this tab actually offers. Without this the endpoint would
		// switch off anything with a mount prefix — including the hidden
		// framework apps the list deliberately withholds, which is exactly the
		// trap the list was shaped to avoid.
		if !isListableApp(path) {
			http.Error(w, "not an app that can be switched off: "+path, http.StatusBadRequest)
			return
		}
		if err := SetAppEnabled(a.db, path, req.Enabled); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		state := "disabled"
		if req.Enabled {
			state = "enabled"
		}
		Log("[admin] app %s %s by %s", path, state, AuthCurrentUser(r))
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "path": path, "enabled": req.Enabled})
		return
	}

	_ = json.NewEncoder(w).Encode(a.appRows(newPanes(r)))
}

// appRows is the table: the apps that ship, then the authored ones. A row
// links to the app's page only when the page has something to set; an app
// that claims nothing is fully described by its row.
func (a *AdminApp) appRows(p *panes) []map[string]any {
	access := newAppAccess(a.db)
	var out []map[string]any
	for _, rw := range p.apps() {
		state := "enabled"
		switch {
		case rw.hidden:
			state = "always_on"
		case !AppEnabled(a.db, rw.path):
			state = "disabled"
		}
		href := ""
		if p.claims(rw.path) {
			href = a.WebPath() + "/app?path=" + url.QueryEscape(rw.path)
		}
		out = append(out, map[string]any{
			"key":        rw.path,
			"group":      "Built in",
			"path":       rw.path,
			"name":       rw.name,
			"desc":       rw.desc,
			"state":      state,
			"switchable": !rw.hidden,
			"enabled":    state == "enabled",
			"access":     access.describe(rw.path),
			"settings":   p.settings(rw.path),
			"href":       href,
		})
	}
	for _, e := range p.authoredApps() {
		state := "enabled"
		if e.Disabled {
			state = "disabled"
		}
		href := ""
		if e.Page != nil {
			href = a.WebPath() + "/app?key=" + url.QueryEscape(e.Key)
		}
		out = append(out, map[string]any{
			"key":      "authored:" + e.Key,
			"group":    "Built here",
			"name":     e.Name,
			"owner":    e.Owner,
			"desc":     e.Desc,
			"state":    state,
			"access":   e.Access,
			"settings": e.Settings,
			"href":     href,
		})
	}
	if out == nil {
		out = []map[string]any{}
	}
	return out
}

// appPage is what an app's own page holds: its name for the header, its
// sections, and any head HTML its controls need.
type appPage struct {
	title    string
	sections []ui.Section
	head     string
}

// handleAppPage serves one app's page: ?path=<mount> for an app that ships,
// ?key=<owner>/<slug> for an authored one. It is an admin page like the tabs,
// with Apps marked in the menu and the back arrow returning to the table.
func (a *AdminApp) handleAppPage(w http.ResponseWriter, r *http.Request) {
	a.serveAdminPage(w, r, ui.SectionSlug(AppsTabGroup), func(p *panes) *appPage {
		if key := r.URL.Query().Get("key"); key != "" {
			e := p.authoredApp(key)
			if e == nil || e.Page == nil {
				return nil
			}
			secs := e.Page()
			for i := range secs {
				secs[i].Group = AppsTabGroup
			}
			return &appPage{title: e.Name, sections: secs, head: e.Head}
		}
		path := r.URL.Query().Get("path")
		app := p.find(path)
		if app == nil {
			return nil
		}
		return &appPage{title: app.WebName(), sections: p.page(path)}
	})
}

// isListableApp reports whether path names an app the Apps tab offers.
func isListableApp(path string) bool {
	for _, rw := range listableApps() {
		if rw.path == path {
			return true
		}
	}
	return false
}

// handleAppSummary answers one app's row.
//
// Live rather than baked into the section, so the access line is true when it
// is read rather than when the page was assembled — grants move, and a stale
// answer to "who can open this" is worse than no answer.
func (a *AdminApp) handleAppSummary(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	p := newPanes(r)
	app := p.find(path)
	if app == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// state_severity colours the state line: core/ui has no way to know which
	// of two words is the bad news, so the server says.
	state, severity := "Enabled", "ok"
	switch {
	case appIsHidden(app):
		// Said outright, so the missing switch above reads as deliberate.
		state = "Always on: part of the framework other apps use, so it has no switch"
	case !AppEnabled(a.db, path):
		state, severity = "Disabled: its pages and API answer 503", "bad"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"path":           path,
		"desc":           app.WebDesc(),
		"state":          state,
		"state_severity": severity,
		"access":         describeAppAccess(a.db, path),
		"controls":       p.controls(path),
	})
}

// describeAppAccess says who can open an app, in a sentence rather than a list
// of usernames that would run off the row.
//
// Admins are named separately because "everyone" would be wrong and "nobody"
// would be too: an admin reaches every app regardless of grants, so an app with
// no grants at all is still reachable by them, and an operator reading "nobody"
// and concluding the app is unused would be misled.
func describeAppAccess(db Database, path string) string { return newAppAccess(db).describe(path) }

// appAccess is every account's grants, resolved once, so the table can say
// who can open each app without resolving every account again per row.
type appAccess struct {
	open   bool // no accounts configured
	admins int
	grants [][]string // one per non-admin account
}

func newAppAccess(db Database) *appAccess {
	if db == nil || !AuthHasUsers(db) {
		return &appAccess{open: true}
	}
	x := &appAccess{}
	for _, u := range AuthListUsers(db) {
		if u.Admin {
			x.admins++
			continue
		}
		x.grants = append(x.grants, AuthResolveUserApps(db, u))
	}
	return x
}

func (x *appAccess) describe(path string) string {
	if x.open {
		return "everyone (no accounts configured on this deployment)"
	}
	granted := 0
	for _, apps := range x.grants {
		for _, p := range apps {
			if p == path || strings.HasPrefix(p, path+"/") {
				granted++
				break
			}
		}
	}
	if granted == 0 {
		return plural(x.admins, "admin") + " only: no other account has been granted it"
	}
	return plural(granted, "account") + " granted, plus " + plural(x.admins, "admin")
}

// describeAppControls says what this app has CLAIMED. Until an app declares
// its controls it claims nothing, which is the honest answer and not an error:
// the controls still work, on their own tabs, exactly as before.
func describeAppControls(r *http.Request, path string) string { return newPanes(r).controls(path) }

func (p *panes) controls(path string) string {
	var parts []string
	if n := len(RouteStagesForApp(path)); n > 0 {
		// The same dials as LLMs > LLM Routing: said here so a change in one
		// place is not mistaken for a second, app-local setting.
		parts = append(parts, plural(n, "routing dial")+" (Model routing, also on LLMs > LLM Routing)")
	}
	if n := len(TunablesForApp(path)); n > 0 {
		parts = append(parts, plural(n, "tunable")+" (Tuning, also on the Tuning tab)")
	}
	if n := len(p.onPage(path)); n > 0 {
		parts = append(parts, plural(n, "settings panel")+" (on this page)")
	}
	if n := len(p.elsewhere(path)); n > 0 {
		parts = append(parts, plural(n, "settings panel")+" on other tabs (linked from Other settings)")
	}
	if len(parts) == 0 {
		return "none declared: anything this app configures still lives on the LLMs, Tuning and Extensions tabs"
	}
	return strings.Join(parts, ", ")
}

// settings is the table's short count of what an app's page holds, empty when
// it holds nothing.
func (p *panes) settings(path string) string {
	var parts []string
	if n := len(RouteStagesForApp(path)); n > 0 {
		parts = append(parts, plural(n, "routing dial"))
	}
	if n := len(TunablesForApp(path)); n > 0 {
		parts = append(parts, plural(n, "tunable"))
	}
	if n := len(p.onPage(path)) + len(p.elsewhere(path)); n > 0 {
		parts = append(parts, plural(n, "panel"))
	}
	return strings.Join(parts, ", ")
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// findPaneApp resolves the app a section on this tab is about, hidden ones
// with a pane included.
//
// A named function so the thing that BUILDS the sections and the thing that
// ANSWERS them can be checked against each other. They disagreed: sections came
// from AllWebApps and the lookup read the direct-registration registry, which
// holds the admin panel and nothing else — and the admin panel is the one app
// listableApps excludes. Every section on the tab rendered a 404 under a
// correct heading, on every deployment. It resolves through paneApps for the
// same reason: the list the sections are built from is the list it answers.
//
// Nil when no pane is for that path, which is the caller's 404.
func findPaneApp(r *http.Request, path string) WebApp { return newPanes(r).find(path) }

func (p *panes) find(path string) WebApp {
	for _, rw := range p.apps() {
		if rw.path != path {
			continue
		}
		for _, wa := range AllWebApps() {
			if wa.WebPath() == path {
				return wa
			}
		}
	}
	return nil
}

// appPanelsSource is the source of one app's panel links. Named once so the
// table and the routability test agree on it.
func appPanelsSource(path string) string {
	return "api/app-panels?path=" + path
}

// handleAppPanels lists the admin sections an app has claimed on other tabs,
// each with the tab it lands on and a link that opens it there.
//
// The tab comes from sectionTab, the same rule the page applies when it files
// the section, so the link cannot name one tab while the section sits on
// another. The link names the tab as well as the section (ui.SectionOnTab):
// an app's pane and its panel usually share the app's name, and a bare slug
// would resolve to the pane the link is on.
func (a *AdminApp) handleAppPanels(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	type panel struct {
		Title string `json:"title"`
		Tab   string `json:"tab"`
		Href  string `json:"href"`
	}
	out := []panel{}
	for _, e := range newPanes(r).elsewhere(path) {
		tab := sectionTab(e.Section.Title, e.Section.Group)
		if tab == "" {
			tab = "General"
		}
		// An untitled section is the only thing on its tab (the prompt-block
		// editor), so the tab's name is what it is called and the tab alone
		// is its address.
		title := e.Section.Title
		if title == "" {
			title = tab
		}
		out = append(out, panel{Title: title, Tab: tab, Href: ui.SectionOnTab(tab, e.Section.Title)})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// panelsElsewhere is the admin sections an app has claimed that live on some
// OTHER tab. One filed under Apps is on the app's own page (onPage), so there
// is nothing to link to.
func panelsElsewhere(r *http.Request, path string) []AdminSectionEntry {
	return newPanes(r).elsewhere(path)
}

func (p *panes) elsewhere(path string) []AdminSectionEntry {
	if path == "" {
		return nil
	}
	var out []AdminSectionEntry
	for _, e := range p.entries {
		if e.App == path && sectionTab(e.Section.Title, e.Section.Group) != AppsTabGroup {
			out = append(out, e)
		}
	}
	return out
}

// onPage is the admin sections an app has filed under the Apps tab. They are
// drawn on the app's own page rather than on the tab, which holds only the
// table: a section there would put an index of sections back beside it.
func (p *panes) onPage(path string) []AdminSectionEntry {
	if path == "" {
		return nil
	}
	var out []AdminSectionEntry
	for _, e := range p.entries {
		if e.App == path && sectionTab(e.Section.Title, e.Section.Group) == AppsTabGroup {
			out = append(out, e)
		}
	}
	return out
}
