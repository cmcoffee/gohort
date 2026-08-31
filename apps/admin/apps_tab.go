// The Apps tab: admin organised by SUBJECT rather than only by mechanism.
//
// Everything configurable about an app is otherwise spread across tabs by the
// kind of thing it is — its tier is a route stage under LLMs, its knobs are
// tunables under Tuning, its own settings are a contributed section under
// Extensions. That grouping is genuinely useful ("show me all routing at once"
// is what you want when chasing a bill) and it stays. This is the other axis:
// one row per app, showing what belongs to THAT app.
//
// Custom apps land on this same tab without admin importing them: they arrive
// through the runtime AdminSectionSource that apps/customapps registers, under
// the same Group. Admin's own rows are appended before the registry's, so
// compiled apps come first and custom apps follow.
//
// See docs/apps-tab.md.
package admin

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// AppsTabGroup is the tab both admin and the custom-app source render into.
const AppsTabGroup = "Apps"

// appsTabSections builds one section per COMPILED app.
func (a *AdminApp) appsTabSections() []ui.Section {
	type row struct{ path, name, desc string }
	var rows []row
	for _, wa := range RegisteredWebApps() {
		if wa.WebPath() == "/admin" || appIsHidden(wa) {
			continue
		}
		rows = append(rows, row{path: wa.WebPath(), name: wa.WebName(), desc: wa.WebDesc()})
	}
	sort.Slice(rows, func(i, j int) bool { return strings.ToLower(rows[i].name) < strings.ToLower(rows[j].name) })

	out := make([]ui.Section, 0, len(rows))
	for _, rw := range rows {
		out = append(out, ui.Section{
			Title:    rw.name,
			Subtitle: rw.path,
			Group:    AppsTabGroup,
			Wide:     true,
			Body: ui.DisplayPanel{
				Source: "api/app-summary?path=" + rw.path,
				Pairs: []ui.DisplayPair{
					{Label: "Path", Field: "path", Mono: true},
					{Label: "What it is", Field: "desc"},
					{Label: "Who can open it", Field: "access"},
					{Label: "Its own controls", Field: "controls"},
				},
			},
		})
	}
	return out
}

// appIsHidden mirrors the dashboard's rule: an app that opts out of being
// listed is not administered here either, because it is not a surface anybody
// is being given access to.
func appIsHidden(wa WebApp) bool {
	type hidden interface{ WebHidden() bool }
	h, ok := wa.(hidden)
	return ok && h.WebHidden()
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
	var app WebApp
	for _, wa := range RegisteredWebApps() {
		if wa.WebPath() == path {
			app = wa
			break
		}
	}
	if app == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"path":     path,
		"desc":     app.WebDesc(),
		"access":   describeAppAccess(a.db, path),
		"controls": describeAppControls(r, path),
	})
}

// describeAppAccess says who can open an app, in a sentence rather than a list
// of usernames that would run off the row.
//
// Admins are named separately because "everyone" would be wrong and "nobody"
// would be too: an admin reaches every app regardless of grants, so an app with
// no grants at all is still reachable by them, and an operator reading "nobody"
// and concluding the app is unused would be misled.
func describeAppAccess(db Database, path string) string {
	if db == nil || !AuthHasUsers(db) {
		return "everyone (no accounts configured on this deployment)"
	}
	var granted, admins int
	for _, u := range AuthListUsers(db) {
		if u.Admin {
			admins++
			continue
		}
		for _, p := range AuthResolveUserApps(db, u) {
			if p == path || strings.HasPrefix(p, path+"/") {
				granted++
				break
			}
		}
	}
	switch {
	case granted == 0:
		return plural(admins, "admin") + " only — no other account has been granted it"
	default:
		return plural(granted, "account") + " granted, plus " + plural(admins, "admin")
	}
}

// describeAppControls says what this app has CLAIMED. Until an app declares
// its controls it claims nothing, which is the honest answer and not an error:
// the controls still work, on their own tabs, exactly as before.
func describeAppControls(r *http.Request, path string) string {
	var parts []string
	if n := len(RouteStagesForApp(path)); n > 0 {
		parts = append(parts, plural(n, "routing dial"))
	}
	if n := len(TunablesForApp(path)); n > 0 {
		parts = append(parts, plural(n, "tunable"))
	}
	if n := len(AdminSectionEntriesForApp(r, path)); n > 0 {
		parts = append(parts, plural(n, "settings panel"))
	}
	if len(parts) == 0 {
		return "none declared — anything this app configures still lives on the LLMs, Tuning and Extensions tabs"
	}
	return strings.Join(parts, ", ")
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}
