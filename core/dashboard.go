package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	httppprof "net/http/pprof"
	"sort"
	"strings"

	"github.com/cmcoffee/gohort/core/ui"

	"github.com/cmcoffee/gohort/core/netgate"
	"github.com/cmcoffee/gohort/core/notices"
	"github.com/cmcoffee/gohort/core/webui"
)

// ServeDashboard starts the unified web dashboard on the given address.
// It discovers all registered WebApps, initializes them, and mounts
// them under their prefix paths with a landing page at /.
type dashApp struct {
	name     string
	desc     string
	path     string
	order    int    // explicit sort key for dynamic cards; 0 means "ask app for WebOrder"
	app      WebApp // nil for cards from DashboardCardSource
	group    string // the Customize page's heading for it
	groupAt  int    // where that heading comes among the others; 0 reads as 50
	pinnable bool   // shown only when the viewer asks for it
}

func ServeDashboard(addr string) error {
	WebListenAddr = addr
	DefaultPleaseWait()

	mux := http.NewServeMux()

	var apps []dashApp

	webApps := AllWebApps()

	// First pass: initialize all web app databases so cross-app
	// lookups via FindAgent see a fully wired agent.
	for _, wa := range webApps {
		if agent, ok := wa.(Agent); ok && SetupWebAgentFunc != nil {
			SetupWebAgentFunc(agent)
		}
	}

	// Pre-initialize the scheduler DB so apps can call ScheduleTask during RegisterRoutes.
	PreInitScheduler()

	// Second pass: register routes now that all agents are ready.
	for _, wa := range webApps {
		prefix := wa.WebPath()
		// SimpleWebApp path — framework owns the sub-mux + mount.
		// App's Routes() just registers via T.HandleFunc against
		// the pre-wired AppCore.webMux.
		if simple, ok := wa.(SimpleWebApp); ok {
			sub := NewWebUI(wa, prefix, AppUIAssets{})
			if a, ok := wa.(interface{ Get() *AppCore }); ok {
				a.Get().SetWebMux(sub, prefix)
			}
			simple.Routes()
			MountSubMux(mux, prefix, sub)
		} else {
			wa.RegisterRoutes(mux, prefix)
		}
		// Apps implementing WebHidden get routes but no dashboard card.
		type hidden interface{ WebHidden() bool }
		if h, ok := wa.(hidden); ok && h.WebHidden() {
			Log("  Registered (hidden): %s -> %s/\n", wa.WebName(), prefix)
			continue
		}
		apps = append(apps, dashApp{
			name: wa.WebName(),
			desc: wa.WebDesc(),
			path: prefix,
			app:  wa,
		})
		Log("  Registered: %s -> %s/\n", wa.WebName(), prefix)
	}

	// Legacy mounts, AFTER the loop above: an app declares its old path from
	// inside Routes(), so mounting these any earlier mounts an empty map and
	// the old path 404s — which is the one thing a legacy mount exists to
	// prevent, and it fails exactly where nobody is looking, on the links that
	// were already out in the world.
	mountLegacyRedirects(mux)

	// All apps are registered — start the global scheduler so handlers added
	// during RegisterRoutes are in place before any tasks can fire.
	StartGlobalScheduler(AppContext())

	// Connect configured remote MCP servers and register their tools
	// (and, later, reference sources). Non-blocking: each server is
	// brought up in the background, so a slow/dead endpoint never stalls
	// startup.
	MCP().Reload()

	// Re-materialize approved connectors (LLM-drafted "bridge types") now that
	// their target subsystems (MCP) are up. Idempotent; a remote_mcp connector
	// already persists an enabled MCP server, so this mainly refreshes state and
	// re-surfaces any last materialize error.
	ReloadApprovedConnectors(RootDB)

	// Sort by WebOrder (if implemented), then alphabetically.
	sort.Slice(apps, func(i, j int) bool {
		oi, oj := 50, 50
		if o, ok := apps[i].app.(WebAppOrder); ok {
			oi = o.WebOrder()
		}
		if o, ok := apps[j].app.(WebAppOrder); ok {
			oj = o.WebOrder()
		}
		if oi != oj {
			return oi < oj
		}
		return apps[i].name < apps[j].name
	})

	// The dashboard's own endpoints; each handler is documented where it is
	// defined, below ServeDashboard.
	host := dashboardHost{apps: apps}
	mux.HandleFunc("/", host.handleRoot)
	mux.HandleFunc("/dashboard/customize", host.handleCustomize)
	mux.HandleFunc("/api/dashboard/items", host.handleDashboardItems)
	mux.HandleFunc("/api/dashboard/show", host.handleDashboardShow)
	mux.HandleFunc("/api/dashboard/move", host.handleDashboardMove)
	mux.HandleFunc("/api/live", host.handleLive)
	mux.HandleFunc("/debug/pprof/", handlePprof)
	mux.HandleFunc("/api/notify-preference", handleNotifyPreference)
	// Notifications are deployment-wide, not one app's: anything that runs on
	// its own can have something to tell the owner, and the place to find that
	// out must not be inside whichever app happened to write it.
	mux.HandleFunc("/api/notifications", handleNotifications)
	mux.HandleFunc("/api/notifications/read", handleNotificationRead)
	mux.HandleFunc("/api/notifications/dismiss", handleNotificationDismiss)
	mux.HandleFunc("/api/notifications/forward", handleNotificationForward)
	mux.HandleFunc("/api/access", host.handleAccess)

	// Mount the shared declarative-UI runtime (CSS + JS at /_ui/*).
	// Apps that render via core/ui depend on these endpoints — register
	// once here so every app can use ui.Page.ServeHTTP without touching
	// the mux directly.
	uiMountRuntime(mux)
	// The runtime CSS/JS are static framework assets (no user data, identical for
	// everyone, already visible in every browser) that EVERY page loads — mark
	// them public so an anonymous surface (e.g. a public custom app served at a
	// capability URL) can boot its page instead of getting bounced to /login.
	RegisterPublicPath("/_ui/ui.css")
	RegisterPublicPath("/_ui/ui.js")
	// The home-screen manifest and icons: a phone fetches them without the
	// session (and before sign-in), and they carry nothing but the mark.
	for _, p := range webui.AppIconPaths {
		RegisterPublicPath(p)
	}
	// Wire the active-theme lookup to the stored deployment setting, so every
	// un-pinned page renders in the admin-selected theme (falls back to the
	// ui default when unset).
	ui.RegisterThemeResolver(func() string { return AuthGetUITheme(AuthDB()) })

	// Authentication endpoints.
	if AuthDB != nil {
		db := AuthDB()
		mux.HandleFunc("/login", LoginHandler(db))
		mux.HandleFunc("/logout", LogoutHandler(db))
		mux.HandleFunc("/signup", SignupHandler(db))
		mux.HandleFunc("/forgot", ForgotHandler(db))
		mux.HandleFunc("/reset", ResetHandler(db))
	}

	// Desktop bridge — gohort-desktop / gohort-bridge opens a
	// WebSocket here, announces its locally-installed tools, and the
	// orchestrate runner exposes those to the LLM as local.<name>
	// tools (see core/desktop_bridge.go). Marked public so the
	// headless daemon's X-API-Key request (no cookie) reaches the
	// handler; DesktopBridgeUserOf re-validates — cookie session
	// first, then the API key — and rejects an unauthenticated
	// connection itself, so the path is never actually unguarded.
	RegisterPublicPath("/api/desktop/ws")
	mux.HandleFunc("/api/desktop/ws", HandleDesktopBridge(DesktopBridgeUserOf))

	// Cookie-authed key provisioning: the desktop client (logged in via
	// the webview) mints/fetches its bridge key here, instead of the user
	// pasting one in. NOT a public path — cookie auth only, since it issues
	// a credential. (Core-owned; phantom no longer owns the bridge key.)
	mux.HandleFunc("/api/desktop/key", HandleDesktopKey)

	// Resource sharing: the surface a PEER gohort instance calls to use this
	// one's infrastructure. Public paths because they carry their own
	// credential (X-Gohort-Peer-Key / Bearer) and must NOT fall through to
	// cookie auth — a peer key is a capability grant, not a user session, and
	// nothing here consults AuthCurrentUser. See core/peer_key.go.
	RegisterPublicPath("/api/peer/manifest")
	mux.HandleFunc("/api/peer/manifest", HandlePeerManifest)
	// Mounted at the OpenAI path so a peer points its ordinary embedding config
	// at <base>/api/peer/v1 and needs no gohort-specific client at all.
	RegisterPublicPath("/api/peer/v1/embeddings")
	mux.HandleFunc("/api/peer/v1/embeddings", HandlePeerEmbeddings)
	// Public in the session-auth sense ONLY: the peer key is the credential, and
	// HandlePeerInvestigate authenticates it before anything else. Without this
	// the session layer 401s a peer with a bare "unauthorized" that never
	// reaches — and so never mentions — the key.
	RegisterPublicPath("/api/peer/v1/investigate")
	mux.HandleFunc("/api/peer/v1/investigate", HandlePeerInvestigate)
	// Same reasoning: peer-key authenticated, so it must bypass session auth.
	RegisterPublicPath("/api/peer/v1/knowledge")
	mux.HandleFunc("/api/peer/v1/knowledge", HandlePeerKnowledge)
	// The command transport. Peer-key authenticated, so session auth must not
	// see it first.
	RegisterPublicPath("/api/peer/v1/exec")
	mux.HandleFunc("/api/peer/v1/exec", HandlePeerExec)
	// Renders for a peer. A1111-shaped so the far side drives it with an
	// ordinary rest_image connector rather than a bespoke client.
	RegisterPublicPath("/api/peer/v1/images/render")
	mux.HandleFunc("/api/peer/v1/images/render", HandlePeerImageRender)
	// Speech-to-text for a peer, at the OpenAI path for the same reason as
	// embeddings: the far side points its ordinary TranscribeConfig at
	// <base>/api/peer/v1 and needs no gohort-specific client.
	RegisterPublicPath("/api/peer/v1/audio/transcriptions")
	mux.HandleFunc("/api/peer/v1/audio/transcriptions", HandlePeerTranscribe)
	// Search in the SearXNG JSON shape, so the far side drives it with an
	// ordinary searxng-provider config and no client of its own.
	RegisterPublicPath("/api/peer/v1/search")
	mux.HandleFunc("/api/peer/v1/search", HandlePeerSearch)
	// Page rendering on this instance's headless browser.
	RegisterPublicPath("/api/peer/v1/browse")
	mux.HandleFunc("/api/peer/v1/browse", HandlePeerBrowse)
	// Inference on this instance's local model, at the OpenAI chat path so the
	// far side points an ordinary llama.cpp provider config at
	// <base>/api/peer/v1 and every existing code path keeps working.
	RegisterPublicPath("/api/peer/v1/chat/completions")
	mux.HandleFunc("/api/peer/v1/chat/completions", HandlePeerChatCompletions)
	// The model picker on the far side asks for this list before it can offer
	// a choice.
	RegisterPublicPath("/api/peer/v1/models")
	mux.HandleFunc("/api/peer/v1/models", HandlePeerModels)
	// The prompt wording tuned here for the model it lends, read by a peer
	// whose worker is that model.
	RegisterPublicPath("/api/peer/v1/prompts")
	mux.HandleFunc("/api/peer/v1/prompts", handlePeerPrompts)

	// Credential exchange. Public like the rest of the peer surface, and
	// unauthenticated in the peerAuthorize sense on purpose: the credential
	// being presented is in the body, and a peer whose access token has just
	// expired has nothing to authenticate WITH until this answers.
	RegisterPublicPath("/api/peer/v1/token")
	mux.HandleFunc("/api/peer/v1/token", HandlePeerToken)

	// Restore persisted queue items after all apps are initialized
	// so handlers are registered.
	QueueRestore()

	// Every app has registered by now, so a claim naming none of them can be
	// reported rather than silently producing a control with no home.
	reportUnknownAppClaims()

	PleaseWait.Hide()
	scheme := "http"
	if TLSEnabled() {
		scheme = "https"
	}
	Log("Oddjob Dashboard: %s://%s\n", scheme, addr)

	return ListenAndServeTLS(addr, dashboardChain(mux))
}

// dashboardChain wraps the dashboard mux in the middleware every request goes
// through, innermost first. Split out of ServeDashboard so the order, and the
// fact that each layer is present at all, can be tested without a listener.
func dashboardChain(mux http.Handler) http.Handler {
	var handler http.Handler = accessLogMiddleware(mux)
	if AuthDB != nil {
		handler = AuthMiddleware(AuthDB(), handler)
	}
	// Outside the auth middleware on purpose: whether an app is switched on is
	// a fact about the DEPLOYMENT, not about the caller, so it is settled
	// before anything asks who is calling — and therefore ahead of every
	// documented way past the per-user grant (public paths, internal calls,
	// the deployment API key, an install with no accounts). See
	// AppAvailabilityMiddleware.
	handler = AppAvailabilityMiddleware(handler)
	// Every request body is capped, ahead of anything that might read one. A
	// route that legitimately takes more (a streamed upload) raises its own
	// cap with netgate.RaiseBodyLimit before reading.
	handler = netgate.LimitRequestBody(handler, netgate.DefaultBodyLimit)
	// Outermost: baseline security headers on every response (incl. auth
	// redirects and error pages).
	return securityHeadersMiddleware(handler)
}

// dashboardHost serves the dashboard's own endpoints: the app cards, the
// global live view and the per-request access flags all read the apps the
// dashboard was built with, so they hang off one value rather than closing
// over a local of ServeDashboard.
type dashboardHost struct {
	apps []dashApp
}

// cards is every card this viewer may see: the ones the dashboard shows by
// default (apps they can reach, and what card sources contribute), and the
// ones it shows only when they ask for it (dashboardPinSource: their own
// custom apps, their unpublished agents).
func (d dashboardHost) cards(r *http.Request) (defaults, pinnable []dashApp) {
	visible := make([]dashApp, 0, len(d.apps))
	for _, a := range d.apps {
		if ra, ok := a.app.(WebAppRestricted); ok && ra.WebRestricted(r) {
			continue
		}
		// Switched off deployment-wide by an administrator: no card, for
		// anybody. The routes are refused too (AppAvailabilityMiddleware); a
		// card that leads to a 503 is worse than no card.
		if !AppEnabledHere(a.path) {
			continue
		}
		// Per-user app access (skip admin app, it has its own gating).
		if a.path != adminAppPath && !UserHasAppAccess(r, a.path) {
			continue
		}
		visible = append(visible, a)
	}
	// Pull dynamic cards (e.g. one-per-exposed-agent) from any
	// WebApp that implements DashboardCardSource. We walk the
	// original d.apps slice (visible OR not) because a source may
	// be hidden itself (WebHidden) yet still contribute cards —
	// the d.apps/agents directory is the canonical case.
	for _, a := range d.apps {
		src, ok := a.app.(DashboardCardSource)
		if !ok {
			continue
		}
		// A disabled host takes its dynamic cards with it. The cards live
		// under the host's mount prefix, so leaving them would draw tiles
		// whose every link the availability gate refuses.
		if !AppEnabledHere(a.path) {
			continue
		}
		for _, c := range src.DashboardCards(r) {
			visible = append(visible, dashApp{
				name:    c.Name,
				desc:    c.Desc,
				path:    c.Path,
				order:   c.Order,
				app:     nil, // no underlying WebApp; live-view lookups skip it
				group:   chooseStr(c.Group, "More"),
				groupAt: c.GroupOrder,
			})
		}
	}
	for i := range visible {
		if visible[i].app != nil {
			visible[i].group = "Apps"
		}
	}
	for _, a := range d.apps {
		src, ok := a.app.(dashboardPinSource)
		if !ok || !AppEnabledHere(a.path) {
			continue
		}
		for _, c := range src.DashboardPinnable(r) {
			pinnable = append(pinnable, dashApp{name: c.Name, desc: c.Desc, path: c.Path, order: c.Order, group: chooseStr(c.Group, "More"), groupAt: c.GroupOrder})
		}
	}
	return visible, pinnable
}

// handleRoot is the dashboard page: the app cards this viewer may see plus
// the dynamic cards any DashboardCardSource contributes, in declared order.
func (d dashboardHost) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	defaults, pinnable := d.cards(r)
	prefs := loadDashPrefs(AuthCurrentUser(r))
	visible := applyDashPrefs(defaults, pinnable, prefs)
	sortDashDefault(visible)
	orderDash(visible, prefs.Order)
	sortDashGroups(visible)
	lastDash(visible)
	// Notices from any app with something the viewer must act on. Walked over
	// the ORIGINAL list, like card sources, so a hidden app can still speak —
	// but never a switched-off one, whose links would land on the 503 the
	// availability gate answers with.
	var notices []DashboardNotice
	for _, a := range d.apps {
		src, ok := a.app.(DashboardNoticeSource)
		if !ok || !AppEnabledHere(a.path) {
			continue
		}
		notices = append(notices, src.DashboardNotices(r)...)
	}
	serve_dashboard(w, r, visible, notices)
}

// handleLive is the global live view. Deep links are gated HERE rather than
// in the client: whether this viewer may re-enter the owning app is a
// server-side fact (per-user grants + the app's own WebRestricted), and the
// browser has no business guessing it. An entry the viewer can't follow gets
// its url/path cleared, which is exactly the signal the live pill reads to
// fall back to the Monitor.
func (d dashboardHost) handleLive(w http.ResponseWriter, r *http.Request) {
	entries := AllLiveSessions()
	viewer := AuthCurrentUser(r)
	for i := range entries {
		// Label masking runs FIRST and unconditionally — it is about who
		// the work belongs to, not about whether this viewer can navigate
		// to it. The access checks below only decide whether the row gets
		// a link; an entry with no way back still renders its label.
		entries[i].Label = entries[i].MaskedLabel(viewer)
		entries[i].applyOwnerDestination(viewer)
		prefix := liveEntryAppPath(entries[i])
		if prefix == "" {
			continue // offers no way back; already Monitor-only
		}
		if !userCanReachApp(r, d.apps, prefix) {
			entries[i].URL, entries[i].Path = "", ""
			continue
		}
		entries[i].Href = entries[i].ResolveHref()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entries)
}

// handlePprof is admin-gated pprof — live runtime introspection WITHOUT
// restarting. The key one for diagnosing a hang ("a run shows running but
// nothing is happening — no GPU, no tool, no output"):
// GET /debug/pprof/goroutine?debug=2 dumps every goroutine's stack, which
// names the wedged handler / blocked channel in seconds instead of an
// elimination exercise. Gated to admins on top of the AuthMiddleware login,
// since pprof exposes internal state.
func handlePprof(w http.ResponseWriter, r *http.Request) {
	if AuthDB == nil || !AuthIsAdmin(AuthDB(), r) {
		http.Error(w, "admin only", http.StatusForbidden)
		return
	}
	switch strings.TrimPrefix(r.URL.Path, "/debug/pprof/") {
	case "cmdline":
		httppprof.Cmdline(w, r)
	case "profile":
		httppprof.Profile(w, r)
	case "symbol":
		httppprof.Symbol(w, r)
	case "trace":
		httppprof.Trace(w, r)
	default:
		httppprof.Index(w, r) // index + named profiles (goroutine, heap, ...)
	}
}

// handleNotifyPreference is the persistent notify preference: GET returns
// it, POST toggles and persists.
func handleNotifyPreference(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	username := AuthCurrentUser(r)
	if username == "" || AuthDB == nil {
		json.NewEncoder(w).Encode(map[string]bool{"notify": false})
		return
	}
	db := AuthDB()
	if r.Method == http.MethodPost {
		var req struct {
			Notify bool `json:"notify"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		AuthSetNotifyDefault(db, username, req.Notify)
		json.NewEncoder(w).Encode(map[string]bool{"notify": req.Notify})
		return
	}
	json.NewEncoder(w).Encode(map[string]bool{"notify": AuthGetNotifyDefault(db, username)})
}

// --- Notifications ---------------------------------------------------------
//
// One-way: what something told the owner while they were not watching. The
// store is core/notices; these are the deployment-wide endpoints behind the
// dashboard's bell, deliberately NOT inside an app. An app writes a notice with
// whatever it knows; the owner reads them all in one place.

// noticeDB is where notices live. RootDB when a host has set one, which every
// real deployment does; nil is a host that never wired storage, and reading
// nothing is the right answer there rather than a panic.
func noticeDB() notices.Store {
	if RootDB == nil {
		return nil
	}
	return RootDB
}

func handleNotifications(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	user := AuthCurrentUser(r)
	if user == "" {
		json.NewEncoder(w).Encode(map[string]any{"unread": 0, "notices": []any{}})
		return
	}
	list := notices.List(noticeDB(), user)
	unread := 0
	type row struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Body  string `json:"body,omitempty"`
		Kind  string `json:"kind"`
		Count int    `json:"count"`
		When  string `json:"when"`
		Read  bool   `json:"read"`
	}
	out := make([]row, 0, len(list))
	for _, n := range list {
		if !n.Read {
			unread++
		}
		out = append(out, row{
			ID: n.ID, Title: n.Title, Body: n.Body, Kind: n.Kind, Count: n.Count,
			When: n.Last.In(UserLocation(user)).Format("Jan 2 3:04 PM"), Read: n.Read,
		})
	}
	json.NewEncoder(w).Encode(map[string]any{"unread": unread, "notices": out})
}

// handleNotificationRead marks one read, or all of them when no id is given.
// Reading never resets a count: how often a thing has happened stays true after
// you have read about it, and that number is the only evidence that says
// chronic rather than one-off.
func handleNotificationRead(w http.ResponseWriter, r *http.Request) {
	user := AuthCurrentUser(r)
	if user == "" {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}
	if id := strings.TrimSpace(r.URL.Query().Get("id")); id != "" {
		notices.MarkRead(noticeDB(), user, id)
	} else {
		notices.MarkAllRead(noticeDB(), user)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleNotificationDismiss deletes one. It comes back if the thing happens
// again, which is right for a condition and is why this is not a mute.
func handleNotificationDismiss(w http.ResponseWriter, r *http.Request) {
	user := AuthCurrentUser(r)
	if user == "" {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}
	// No id clears the list, mirroring read: the two bulk actions take the same
	// shape so neither needs its own endpoint or its own argument to remember.
	if id := strings.TrimSpace(r.URL.Query().Get("id")); id != "" {
		notices.Remove(noticeDB(), user, id)
	} else {
		notices.RemoveAll(noticeDB(), user)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleNotificationForward reads or sets where notices go beyond being kept.
//
// It reports whether each transport can actually deliver, because an option
// that silently does nothing is worse than one that is not offered: the owner
// turns it on, believes they will be told, and is not.
func handleNotificationForward(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	user := AuthCurrentUser(r)
	if user == "" || AuthDB == nil {
		json.NewEncoder(w).Encode(map[string]any{"where": ""})
		return
	}
	if r.Method == http.MethodPost {
		AuthSetNotifyForward(AuthDB(), user, strings.TrimSpace(r.URL.Query().Get("where")))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	phoneOK := false
	if NoticePhoneReady != nil {
		phoneOK = NoticePhoneReady(user)
	}
	json.NewEncoder(w).Encode(map[string]any{
		"where": AuthGetNotifyForward(AuthDB(), user),
		// Mail only reaches a username that IS an address, the existing
		// contract of NotifyUser everywhere else.
		"email_ok": EmailConfigured() && strings.Contains(user, "@"),
		"phone_ok": phoneOK,
	})
}

// handleAccess is the per-request access flags. Apps implement WebAppAccess
// to expose named boolean flags (e.g. "techwriter": true/false).
func (d dashboardHost) handleAccess(w http.ResponseWriter, r *http.Request) {
	flags := make(map[string]bool)
	for _, a := range d.apps {
		if aa, ok := a.app.(WebAppAccess); ok {
			// A disabled app's flag reads false regardless of what its own
			// check would say: the flag exists so another app can decide
			// whether to offer a link, and the answer is no.
			flags[aa.WebAccessKey()] = AppEnabledHere(a.path) && aa.WebAccessCheck(r)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(flags)
}

// Dated is the minimal interface a history record must satisfy.
type Dated interface {
	GetDate() string
}

// HistoryHandlers returns HTTP handler functions for list, detail, and delete
// operations on a database history table. The summarize function converts a
// full record into whatever summary struct the list endpoint should return.
// R must implement Dated so results can be sorted newest-first.
func HistoryHandlers[R Dated, S any](db func() Database, table string, summarize func(R) S) (list, detail, delete_handler http.HandlerFunc) {
	list = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		store := db()
		if store == nil {
			fmt.Fprint(w, "[]")
			return
		}
		type entry struct {
			date string
			item S
		}
		var entries []entry
		for _, key := range store.Keys(table) {
			var rec R
			if store.Get(table, key, &rec) {
				entries = append(entries, entry{date: rec.GetDate(), item: summarize(rec)})
			}
		}
		if entries == nil {
			fmt.Fprint(w, "[]")
			return
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].date > entries[j].date })
		items := make([]S, len(entries))
		for i, e := range entries {
			items[i] = e.item
		}
		data, _ := json.Marshal(items)
		w.Write(data)
	}

	detail = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		id := strings.TrimPrefix(r.URL.Path, "/api/history/")
		store := db()
		if id == "" || store == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var rec R
		if !store.Get(table, id, &rec) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		data, _ := json.Marshal(rec)
		w.Write(data)
	}

	delete_handler = func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/history/delete/")
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		if store := db(); store != nil {
			store.Unset(table, id)
		}
		w.WriteHeader(http.StatusNoContent)
	}

	return
}

// --- Choosing what the dashboard shows -------------------------------------
//
// Each person picks for themselves: hide a card they never use, or put one of
// their own custom apps or unpublished agents on their dashboard. Nobody
// else's dashboard changes. An app hidden here is still reachable at its
// address and from its app's own pages; this is only what the front page
// offers.

// dashboardPinSource is a WebApp with cards the dashboard shows only when a
// person asks for them, unlike DashboardCardSource's, which it shows by
// default.
type dashboardPinSource interface {
	DashboardPinnable(r *http.Request) []DashboardCard
}

// dashPrefs is one person's choice: cards hidden that would show, and cards
// shown that would not, by path.
type dashPrefs struct {
	Hidden []string `json:"hidden,omitempty"`
	Shown  []string `json:"shown,omitempty"`
	// Order is the person's own order of cards, by path; a card not in it
	// keeps its default place after the ones that are.
	Order []string `json:"order,omitempty"`
}

const dashPrefsTable = "dashboard_prefs"

func loadDashPrefs(user string) dashPrefs {
	var p dashPrefs
	if user == "" || AuthDB == nil {
		return p
	}
	if db := AuthDB(); db != nil {
		db.Get(dashPrefsTable, user, &p)
	}
	return p
}

func saveDashPrefs(user string, p dashPrefs) {
	if user == "" || AuthDB == nil {
		return
	}
	if db := AuthDB(); db != nil {
		db.Set(dashPrefsTable, user, p)
	}
}

func hasPath(list []string, path string) bool {
	for _, p := range list {
		if p == path {
			return true
		}
	}
	return false
}

func withoutPath(list []string, path string) []string {
	out := list[:0:0]
	for _, p := range list {
		if p != path {
			out = append(out, p)
		}
	}
	return out
}

// applyDashPrefs is what one person's dashboard shows: the defaults they did
// not hide, then the pinnable cards they asked for.
func applyDashPrefs(defaults, pinnable []dashApp, p dashPrefs) []dashApp {
	out := make([]dashApp, 0, len(defaults))
	seen := map[string]bool{}
	for _, a := range defaults {
		if hasPath(p.Hidden, a.path) {
			continue
		}
		seen[a.path] = true
		out = append(out, a)
	}
	for _, a := range pinnable {
		if hasPath(p.Shown, a.path) && !seen[a.path] {
			seen[a.path] = true
			out = append(out, a)
		}
	}
	return out
}

// dashItem is one row of the Customize page: a card, under the heading of
// its kind (Apps, My apps, Agents), with a note on where it goes or who sees
// it where the row alone would not say.
type dashItem struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Desc    string `json:"desc,omitempty"`
	Group   string `json:"group"`
	Section string `json:"section"`
	Shown   bool   `json:"shown"`
	Note    string `json:"note,omitempty"`
}

// handleDashboardItems lists every card this viewer may put on their
// dashboard, grouped by kind and in their dashboard's order within each
// group, and whether it is there now. GET.
func (d dashboardHost) handleDashboardItems(w http.ResponseWriter, r *http.Request) {
	user := AuthCurrentUser(r)
	if user == "" {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	p := loadDashPrefs(user)
	var items []dashItem
	for _, a := range d.orderedCards(r, p) {
		it := dashItem{Path: a.path, Name: a.name, Desc: a.desc, Group: a.group, Section: dashSection(a), Shown: !hasPath(p.Hidden, a.path), Note: dashPlace(a)}
		if a.pinnable {
			it.Shown, it.Note = hasPath(p.Shown, a.path), "only on your dashboard"
		}
		items = append(items, it)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"records": items})
}

// dashPlace says where on the dashboard a card is drawn when that is not
// the plain grid: the featured spot, or the Orchestrator group. The
// arrows only move a card within its place, so the row has to say what
// that place is.
func dashPlace(a dashApp) string {
	switch dashSection(a) {
	case "Featured":
		return "featured, at the top"
	case "Orchestrator":
		return "in the Orchestrator group"
	}
	return ""
}

// orderedCards is every card the viewer may place, shown or not, in the
// order the Customize page lists them: by group (Apps first, then each
// source's group in its declared order), by place within the group, then
// the viewer's own order, then the default one.
func (d dashboardHost) orderedCards(r *http.Request, p dashPrefs) []dashApp {
	defaults, pinnable := d.cards(r)
	var all []dashApp
	seen := map[string]bool{}
	for _, a := range defaults {
		if !seen[a.path] {
			seen[a.path] = true
			all = append(all, a)
		}
	}
	for _, a := range pinnable {
		if !seen[a.path] {
			seen[a.path] = true
			a.pinnable = true
			all = append(all, a)
		}
	}
	sortDashDefault(all)
	orderDash(all, p.Order)
	sortDashGroups(all)
	lastDash(all)
	return all
}

// lastDash keeps the administrator's card at the end, on the dashboard and
// on the Customize page, however the other cards are arranged: it is the
// way out of trouble, and it stays where it has always been, after
// everything else, whatever apps are on or off and whatever a person pins.
func lastDash(list []dashApp) {
	sort.SliceStable(list, func(i, j int) bool { return !isLastDash(list[i]) && isLastDash(list[j]) })
}

func isLastDash(a dashApp) bool { return a.path == adminAppPath }

// sortDashGroups puts cards of a kind together, keeping their order within
// it: the dashboard's own apps, then each source's group in its declared
// order, each group by place. The dashboard and the Customize page both
// sort by this, so the grid reads the way the page that arranges it does.
func sortDashGroups(list []dashApp) {
	sort.SliceStable(list, func(i, j int) bool {
		if gi, gj := dashGroupRank(list[i]), dashGroupRank(list[j]); gi != gj {
			return gi < gj
		}
		if list[i].group != list[j].group {
			return list[i].group < list[j].group
		}
		return dashSectionRank(list[i]) < dashSectionRank(list[j])
	})
}

// dashGroupRank orders the Customize page's groups: the dashboard's own apps
// first, then the card sources' groups by their declared order.
func dashGroupRank(a dashApp) int {
	if a.app != nil {
		return -1
	}
	if a.groupAt != 0 {
		return a.groupAt
	}
	return 50
}

// handleDashboardMove moves a card one place up or down within its section
// of the viewer's dashboard. POST ?path=<card path>&dir=up|down.
func (d dashboardHost) handleDashboardMove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := AuthCurrentUser(r)
	if user == "" {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	path, dir := strings.TrimSpace(r.URL.Query().Get("path")), r.URL.Query().Get("dir")
	p := loadDashPrefs(user)
	all := d.orderedCards(r, p)
	i := -1
	for k, a := range all {
		if a.path == path {
			i = k
		}
	}
	if i < 0 {
		http.Error(w, "no such card for you", http.StatusNotFound)
		return
	}
	step := 1
	if dir == "up" {
		step = -1
	}
	// The next card in the same group and place; a card cannot leave either,
	// since the dashboard draws each place on its own.
	j := i + step
	for j >= 0 && j < len(all) && (all[j].group != all[i].group || dashSection(all[j]) != dashSection(all[i])) {
		j += step
	}
	// The administrator's card is not moved and not moved past: it is last.
	if j >= 0 && j < len(all) && !isLastDash(all[i]) && !isLastDash(all[j]) {
		all[i], all[j] = all[j], all[i]
	}
	p.Order = p.Order[:0:0]
	for _, a := range all {
		p.Order = append(p.Order, a.path)
	}
	saveDashPrefs(user, p)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// dashSection is the part of the dashboard a card is drawn in (see
// serve_dashboard): a featured app on its own, the Orchestrator group, or the
// rest. A card is ordered within its section.
func dashSection(a dashApp) string {
	if _, ok := a.app.(WebAppHubTab); ok {
		return "Orchestrator"
	}
	if f, ok := a.app.(WebAppFeatured); ok && f.WebFeatured() {
		return "Featured"
	}
	return "Your dashboard"
}

func dashSectionRank(a dashApp) int {
	switch dashSection(a) {
	case "Featured":
		return 0
	case "Orchestrator":
		return 1
	}
	return 2
}

// sortDashDefault is the order cards have before anyone changes it: their
// declared order, then by name.
func sortDashDefault(list []dashApp) {
	rank := func(a dashApp) int {
		if a.order != 0 {
			return a.order
		}
		if o, ok := a.app.(WebAppOrder); ok {
			return o.WebOrder()
		}
		return 50
	}
	sort.SliceStable(list, func(i, j int) bool {
		if oi, oj := rank(list[i]), rank(list[j]); oi != oj {
			return oi < oj
		}
		return list[i].name < list[j].name
	})
}

// orderDash puts cards in the person's own order: the ones it names first, in
// its order, the rest after in the order they were.
func orderDash(list []dashApp, order []string) {
	if len(order) == 0 {
		return
	}
	pos := make(map[string]int, len(order))
	for i, p := range order {
		pos[p] = i
	}
	rank := func(a dashApp) int {
		if i, ok := pos[a.path]; ok {
			return i
		}
		return len(order)
	}
	sort.SliceStable(list, func(i, j int) bool { return rank(list[i]) < rank(list[j]) })
}

// handleDashboardShow puts a card on this viewer's dashboard or takes it off.
// POST ?path=<card path> with {"shown": bool}. Only a card they may see.
func (d dashboardHost) handleDashboardShow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := AuthCurrentUser(r)
	if user == "" {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	var body struct {
		Shown bool `json:"shown"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		http.Error(w, "send {\"shown\": true|false}", http.StatusBadRequest)
		return
	}
	defaults, pinnable := d.cards(r)
	isDefault, isPinnable := false, false
	for _, a := range defaults {
		isDefault = isDefault || a.path == path
	}
	for _, a := range pinnable {
		isPinnable = isPinnable || a.path == path
	}
	if !isDefault && !isPinnable {
		http.Error(w, "no such card for you", http.StatusNotFound)
		return
	}
	p := loadDashPrefs(user)
	if isDefault {
		p.Hidden = withoutPath(p.Hidden, path)
		if !body.Shown {
			p.Hidden = append(p.Hidden, path)
		}
	} else {
		p.Shown = withoutPath(p.Shown, path)
		if body.Shown {
			p.Shown = append(p.Shown, path)
		}
	}
	saveDashPrefs(user, p)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"shown": body.Shown})
}

// handleCustomize is the page where a person picks what their dashboard shows.
func (d dashboardHost) handleCustomize(w http.ResponseWriter, r *http.Request) {
	if AuthCurrentUser(r) == "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	ui.Page{
		Title:     "Customize dashboard",
		ShowTitle: true,
		BackURL:   "/",
		MaxWidth:  "820px",
		Sections: []ui.Section{{
			Title:    "What your dashboard shows",
			Subtitle: "Switch a card off to hide it, or on to add it, and move it with the arrows. Only your dashboard changes: a hidden app is still there at its address, and an agent switched on here opens for you alone.",
			Body: ui.Table{
				Source:  "/api/dashboard/items",
				RowKey:  "path",
				GroupBy: "group",
				Columns: []ui.Col{
					{Field: "name", Flex: 2},
					{Field: "note", Flex: 2, Mute: true},
					{Field: "desc", Flex: 5, Mute: true, Line: 2},
				},
				RowActions: []ui.RowAction{
					{Type: "button", Label: "▲", Compact: true, PostTo: "/api/dashboard/move?path={path}&dir=up"},
					{Type: "button", Label: "▼", Compact: true, PostTo: "/api/dashboard/move?path={path}&dir=down"},
					{Type: "toggle", Field: "shown", PostTo: "/api/dashboard/show?path={path}"},
				},
				EmptyText: "Nothing to show yet.",
			},
		}},
	}.ServeHTTP(w, r)
}
