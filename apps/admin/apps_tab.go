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

// appsTabSections builds the availability switchboard plus one section per
// COMPILED app.
func (a *AdminApp) appsTabSections() []ui.Section {
	rows := listableApps()
	out := make([]ui.Section, 0, len(rows)+1)
	out = append(out, appsAvailabilitySection())
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
					{Label: "State", Field: "state", StatusField: "state_severity"},
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

// appRow is one administrable app: what it is called, where it is mounted,
// and what it says it does.
type appRow struct{ path, name, desc string }

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
	for _, wa := range RegisteredWebApps() {
		if wa.WebPath() == "/admin" || appIsHidden(wa) {
			continue
		}
		rows = append(rows, appRow{path: wa.WebPath(), name: wa.WebName(), desc: wa.WebDesc()})
	}
	sort.Slice(rows, func(i, j int) bool { return strings.ToLower(rows[i].name) < strings.ToLower(rows[j].name) })
	return rows
}

// appsAvailabilitySection is the switchboard: every app the deployment ships,
// with one switch each.
//
// A table rather than a switch on each app's own section below, because the
// question this answers is comparative — which of these are we running — and
// that is a thing you read down a column, not by scrolling through a dozen
// cards. The per-app sections still report their own state, so an operator who
// arrives at one directly is not left guessing.
func appsAvailabilitySection() ui.Section {
	return ui.Section{
		Title: "Enabled apps",
		Subtitle: "Switch an app off to take it off this deployment: its dashboard card disappears for everyone " +
			"and its pages and API answer 503 until it is switched back on. Takes effect immediately — no restart. " +
			"Per-user grants are left untouched, so switching an app back on restores exactly the access it had. " +
			"This governs the app's web surface only; tools, scheduled tasks and routing an app registered at " +
			"startup keep running. The administrator panel and the framework's own internal apps (your account " +
			"page, the monitor, the API endpoints) are not listed — they are what you would need to get back.",
		Group: AppsTabGroup,
		Wide:  true,
		Body: ui.Table{
			Source: "api/apps",
			RowKey: "path",
			Columns: []ui.Col{
				{Field: "name", Label: "App", Flex: 1},
				{Field: "path", Label: "Path", Flex: 1, Mute: true},
				{Field: "enabled", Label: "State", Type: "badge", Badges: []ui.BadgeMapping{
					{Value: true, Label: "Enabled", Color: "success"},
					{Value: false, Label: "Disabled", Color: "danger"},
				}},
				{Field: "desc", Label: "What it is", Flex: 3, Mute: true},
			},
			RowActions: []ui.RowAction{
				{Type: "toggle", Field: "enabled", Leading: true,
					PostTo: "api/apps?path={path}", Method: "POST"},
			},
			EmptyText: "No apps registered.",
		},
	}
}

// handleApps lists the administrable apps (GET) and flips one on or off
// (POST ?path=<mount>, body {"enabled": bool}).
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

	rows := listableApps()
	out := make([]map[string]any, 0, len(rows))
	for _, rw := range rows {
		out = append(out, map[string]any{
			"path":    rw.path,
			"name":    rw.name,
			"desc":    rw.desc,
			"enabled": AppEnabled(a.db, rw.path),
		})
	}
	_ = json.NewEncoder(w).Encode(out)
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
	// state_severity colours the state line: core/ui has no way to know which
	// of two words is the bad news, so the server says.
	state, severity := "Enabled", "ok"
	if !AppEnabled(a.db, path) {
		state, severity = "Disabled — its pages and API answer 503", "bad"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"path":           path,
		"desc":           app.WebDesc(),
		"state":          state,
		"state_severity": severity,
		"access":         describeAppAccess(a.db, path),
		"controls":       describeAppControls(r, path),
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
