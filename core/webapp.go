package core

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/core/ui"
)

// uiMountRuntime exposes core/ui's MountRuntime under a stable name so
// the mux setup in ServeDashboard can wire it without forcing every
// reader to know the package layout.
func uiMountRuntime(mux *http.ServeMux) { ui.MountRuntime(mux) }

// WebApp is implemented by agents that can serve a web UI.
// The central web server discovers these and mounts them under their prefix.
//
// New apps should consider implementing SimpleWebApp instead — its
// Routes() method drops the mux/prefix boilerplate. WebApp stays
// supported indefinitely for apps that need fine-grained control
// over their sub-mux (e.g. custom access wrappers).
type WebApp interface {
	WebPath() string // URL prefix, e.g. "/myapp"
	WebName() string // Display name for dashboard
	WebDesc() string // Short description
	RegisterRoutes(mux *http.ServeMux, prefix string)
}

// SimpleWebApp is the streamlined alternative to WebApp for apps
// that don't need direct mux/prefix access. Implementations write:
//
//	func (T *MyApp) Routes() {
//	    T.HandleFunc("/", T.handlePage)
//	    T.HandleFunc("/api/foo", T.handleFoo)
//	}
//
// The framework handles sub-mux creation, prefix stripping, and
// MountSubMux automatically. T.HandleFunc / T.Handle on AppCore
// register against the app's pre-wired sub-mux.
//
// SimpleWebApp takes precedence over WebApp when both are
// implemented — Routes() is called and RegisterRoutes is skipped.
type SimpleWebApp interface {
	WebPath() string
	WebName() string
	WebDesc() string
	Routes()
}

// WebAppOrder is optionally implemented by WebApps to control their
// position on the dashboard. Lower values appear first. Apps without
// this default to 50.
type WebAppOrder interface {
	WebOrder() int
}

// WebAppFeatured is optionally implemented by a WebApp that should render
// as a large, full-width hero card at the top of the dashboard — the
// primary entry point that demands attention, set apart from the regular
// app grid. Combine with a low WebOrder so it sorts first.
type WebAppFeatured interface {
	WebFeatured() bool
}

// WebAppWide is optionally implemented by a WebApp whose dashboard card
// should span the full grid width but keep the REGULAR card height — a
// "double" button, not a hero. Use for a utility entry (e.g. Administrator)
// that belongs on its own row, typically pinned to the bottom via WebOrder.
type WebAppWide interface {
	WebWide() bool
}

// WebAppRestricted is optionally implemented by WebApps that should be
// hidden from certain requests (e.g. IP-based access control).
type WebAppRestricted interface {
	WebRestricted(r *http.Request) bool
}

// WebAppAccess is optionally implemented by WebApps that expose access
// flags to other apps via /api/access (e.g. "techwriter": true/false).
type WebAppAccess interface {
	WebAccessKey() string                // JSON key name
	WebAccessCheck(r *http.Request) bool // returns the flag value for this request
}

// DashboardCard is a single tile rendered on the gohort dashboard.
// Most tiles come from registered WebApps (one per app); apps that
// contribute MULTIPLE tiles (e.g. one per published agent) return
// them via DashboardCardSource.
type DashboardCard struct {
	Name  string // tile heading
	Desc  string // one-line description
	Path  string // href; rendered as "<Path>/" (leading slash, no trailing)
	Order int    // sort key — lower first; defaults to 50 when zero
}

// DashboardCardSource is implemented by WebApps that contribute
// EXTRA dashboard tiles beyond their own (name, desc, path). The
// framework calls this on every dashboard render, so the returned
// list can change at runtime (e.g. as admins publish/unpublish
// agents). Return nil when there are no extras for this request.
//
// Per-request access checks are the source's responsibility — the
// framework does NOT apply UserHasAppAccess to dynamic cards.
type DashboardCardSource interface {
	DashboardCards(r *http.Request) []DashboardCard
}

// GrantableApp is one entry in the admin user-apps permission picker.
// Dynamic apps (like exposed agents under /agents/<slug>) implement
// GrantableAppListSource to surface their per-slug paths as grantable
// apps. Without this, admins can't flip per-user access on them
// because they aren't in any static registry.
type GrantableApp struct {
	Path string // app path used in user.Apps grants (e.g. "/agents/researcher-bot")
	Name string // display name in the picker
}

// GrantableAppListSource is implemented by apps that contribute extra
// grantable entries to the admin user-apps picker. The framework
// queries every registered app for this interface and unions the
// results into the picker's list. Returns nil/empty when the source
// has no grantable apps right now (no exposed agents, for example).
type GrantableAppListSource interface {
	ListGrantableApps() []GrantableApp
}

var (
	webAppMu          sync.Mutex
	registeredWebApps []WebApp
)

// RegisterWebApp registers a web-capable agent for the central server.
func RegisterWebApp(app WebApp) {
	webAppMu.Lock()
	defer webAppMu.Unlock()
	registeredWebApps = append(registeredWebApps, app)
}

// reportUnknownAppClaims warns about controls claiming an app that is not
// registered, once, at startup.
//
// A typo in a claim is a control that VANISHES from the app view while still
// working on its mechanism tab — the quietest possible failure, because the
// dial is fine and only its home is wrong. Nothing can be done about it
// automatically (dropping the claim and guessing are both worse than saying
// so), and saying so costs one line at boot.
//
// Runs after every init has registered, which is why it is called from the
// serve path rather than from an init of its own. Unexported: the only
// caller is ServeDashboard, three functions down, and core is at its export
// ceiling — a symbol nobody outside needs should not spend one of the seats.
func reportUnknownAppClaims() {
	known := map[string]bool{}
	for _, wa := range RegisteredWebApps() {
		known[wa.WebPath()] = true
	}
	warn := func(kind, name, claim string) {
		Warn("[apps] %s %q claims app %q, which no registered app serves — it will not appear under any app (its own tab still shows it)",
			kind, name, claim)
	}
	for _, st := range ListRouteStages() {
		if st.App != "" && !known[st.App] {
			warn("route stage", st.Key, st.App)
		}
	}
	for _, tn := range AllTunableSpecs() {
		if tn.App != "" && !known[tn.App] {
			warn("tunable", tn.Key, tn.App)
		}
	}
	// Admin sections are not checked here: the runtime sources that produce
	// some of them take a request, and there is none at boot. They are checked
	// where they are rendered instead, which is the only place they exist.
}

// legacyRedirects maps an app's OLD mount prefix to its current one.
var (
	legacyRedirectMu sync.Mutex
	legacyRedirects  = map[string]string{}
)

// RegisterLegacyMount keeps an app's former path answering after it moves.
//
// A mount is not a private detail: it is in bookmarks, in capability links
// somebody was handed, and in every place a URL was pasted. Renaming one
// without this turns all of those into 404s, which reads to whoever holds one
// as the app having been deleted rather than moved.
//
// 308, not 302: the method and body survive, so a POST to an old API path
// lands as a POST. A 302 would turn it into a GET and the caller would see a
// silent no-op instead of a redirect.
func RegisterLegacyMount(oldPrefix, newPrefix string) {
	oldPrefix = strings.TrimSuffix(strings.TrimSpace(oldPrefix), "/")
	newPrefix = strings.TrimSuffix(strings.TrimSpace(newPrefix), "/")
	if oldPrefix == "" || newPrefix == "" || oldPrefix == newPrefix {
		return
	}
	legacyRedirectMu.Lock()
	legacyRedirects[oldPrefix] = newPrefix
	legacyRedirectMu.Unlock()
}

func mountLegacyRedirects(mux *http.ServeMux) {
	legacyRedirectMu.Lock()
	defer legacyRedirectMu.Unlock()
	for oldPrefix, newPrefix := range legacyRedirects {
		from, to := oldPrefix, newPrefix
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			target := to + strings.TrimPrefix(r.URL.Path, from)
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusPermanentRedirect)
		})
		mux.Handle(from, h)
		mux.Handle(from+"/", h)
		// The old subtree has to stay past the cookie gate too, or an
		// anonymous capability link redirects to a login page instead of to
		// where it moved — which is the one case this exists for.
		RegisterPublicPath(from + "/")
		Log("  Legacy mount: %s/ -> %s/\n", from, to)
	}
}

// RegisteredWebApps returns all registered web apps.
func RegisteredWebApps() []WebApp {
	webAppMu.Lock()
	defer webAppMu.Unlock()
	return registeredWebApps
}

// WebAppHubTab is optionally implemented by a WebApp that should appear as a tab
// in the shared top-nav "hub" — the persistent Page.Nav tab row rendered on the
// header of every hub member, so a user can move between the related surfaces
// (Agency, Bridges, Knowledge, …). Label is the tab text (it may differ from
// WebName — e.g. the Agency app shows as "Agents"); Order sorts the tabs, lower
// first. Purely opt-in: only apps that implement this appear as tabs.
type WebAppHubTab interface {
	HubTab() (label string, order int)
}

// hubTabOrder returns an app's HubTab order, or a large sentinel when it isn't a
// hub member (or is nil). Used to order the dashboard's orchestrator-family
// cluster in lockstep with the tab row.
func hubTabOrder(app WebApp) int {
	if ht, ok := app.(WebAppHubTab); ok {
		_, order := ht.HubTab()
		return order
	}
	return 1 << 30
}

// HubNav builds the shared hub tab row for a page, marking the tab whose app
// path equals activePath as the current one. Every hub member sets
// Page.Nav = HubNav(<its WebPath>), so the same tabs render on all of them,
// single-sourced from the registry — adding a member is one HubTab() method, no
// per-page duplication. Empty when no app opts in.
func HubNav(activePath string) []ui.NavLink {
	type tab struct {
		label, path string
		order       int
	}
	var tabs []tab
	// Enumerate every web-capable component the same way the dashboard does —
	// explicit WebApps PLUS registered Apps/Agents that implement WebApp (most
	// apps, incl. Agency/Bridges/Knowledge, register via RegisterApp, not
	// RegisterWebApp) — deduped by path. Collect the ones opting into the hub.
	// Timed per SOURCE. Everything in this function reads as free — three slice
	// walks, a map check, a constant-returning interface method — and it was
	// measured at 1.97 SECONDS on a live page. Something behind one of these
	// interface calls is not what it appears to be, and the only honest way to
	// find out which is to time them separately rather than reason about the
	// bodies again.
	navStart := time.Now()
	var webAppsAt, appsAt, agentsAt time.Duration

	seen := make(map[string]bool)
	consider := func(wa WebApp) {
		if seen[wa.WebPath()] {
			return
		}
		seen[wa.WebPath()] = true
		if ht, ok := wa.(WebAppHubTab); ok {
			label, order := ht.HubTab()
			tabs = append(tabs, tab{label: label, path: wa.WebPath(), order: order})
		}
	}
	for _, wa := range RegisteredWebApps() {
		consider(wa)
	}
	webAppsAt = time.Since(navStart)
	for _, a := range RegisteredApps() {
		if wa, ok := a.(WebApp); ok {
			consider(wa)
		}
	}
	appsAt = time.Since(navStart)
	for _, a := range RegisteredAgents() {
		if wa, ok := a.(WebApp); ok {
			consider(wa)
		}
	}
	agentsAt = time.Since(navStart)
	if agentsAt > 250*time.Millisecond {
		Log("[hubnav] slow: %s total — webapps %s, apps %s, agents %s (counts %d/%d/%d)",
			agentsAt.Round(time.Millisecond), webAppsAt.Round(time.Millisecond),
			(appsAt - webAppsAt).Round(time.Millisecond), (agentsAt - appsAt).Round(time.Millisecond),
			len(RegisteredWebApps()), len(RegisteredApps()), len(RegisteredAgents()))
	}
	sort.SliceStable(tabs, func(i, j int) bool { return tabs[i].order < tabs[j].order })
	out := make([]ui.NavLink, 0, len(tabs))
	for _, t := range tabs {
		out = append(out, ui.NavLink{Label: t.label, URL: t.path, Active: t.path == activePath})
	}
	return out
}
