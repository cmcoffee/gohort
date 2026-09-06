package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	httppprof "net/http/pprof"
	"sort"
	"strings"

	"github.com/cmcoffee/gohort/core/ui"
)

// ServeDashboard starts the unified web dashboard on the given address.
// It discovers all registered WebApps, initializes them, and mounts
// them under their prefix paths with a landing page at /.
type dashApp struct {
	name  string
	desc  string
	path  string
	order int    // explicit sort key for dynamic cards; 0 means "ask app for WebOrder"
	app   WebApp // nil for cards from DashboardCardSource
}

func ServeDashboard(addr string) error {
	WebListenAddr = addr
	DefaultPleaseWait()

	mux := http.NewServeMux()

	var apps []dashApp

	// Collect all web-capable components: explicitly registered WebApps
	// plus any registered Agent/App that implements the WebApp interface.
	seen := make(map[string]bool)
	var webApps []WebApp
	for _, wa := range RegisteredWebApps() {
		if !seen[wa.WebPath()] {
			seen[wa.WebPath()] = true
			webApps = append(webApps, wa)
		}
	}
	for _, a := range RegisteredApps() {
		if wa, ok := a.(WebApp); ok && !seen[wa.WebPath()] {
			seen[wa.WebPath()] = true
			webApps = append(webApps, wa)
		}
	}
	for _, a := range RegisteredAgents() {
		if wa, ok := a.(WebApp); ok && !seen[wa.WebPath()] {
			seen[wa.WebPath()] = true
			webApps = append(webApps, wa)
		}
	}

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

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		visible := make([]dashApp, 0, len(apps))
		for _, a := range apps {
			if ra, ok := a.app.(WebAppRestricted); ok && ra.WebRestricted(r) {
				continue
			}
			// Per-user app access (skip admin app, it has its own gating).
			if a.path != "/admin" && !UserHasAppAccess(r, a.path) {
				continue
			}
			visible = append(visible, a)
		}
		// Pull dynamic cards (e.g. one-per-exposed-agent) from any
		// WebApp that implements DashboardCardSource. We walk the
		// original apps slice (visible OR not) because a source may
		// be hidden itself (WebHidden) yet still contribute cards —
		// the apps/agents directory is the canonical case.
		for _, a := range apps {
			src, ok := a.app.(DashboardCardSource)
			if !ok {
				continue
			}
			for _, c := range src.DashboardCards(r) {
				visible = append(visible, dashApp{
					name:  c.Name,
					desc:  c.Desc,
					path:  c.Path,
					order: c.Order,
					app:   nil, // no underlying WebApp; live-view lookups skip it
				})
			}
		}
		// Stable re-sort so dynamic cards land in their declared order.
		sort.Slice(visible, func(i, j int) bool {
			oi, oj := visible[i].order, visible[j].order
			if oi == 0 {
				if o, ok := visible[i].app.(WebAppOrder); ok {
					oi = o.WebOrder()
				} else {
					oi = 50
				}
			}
			if oj == 0 {
				if o, ok := visible[j].app.(WebAppOrder); ok {
					oj = o.WebOrder()
				} else {
					oj = 50
				}
			}
			if oi != oj {
				return oi < oj
			}
			return visible[i].name < visible[j].name
		})
		serve_dashboard(w, r, visible)
	})

	// Global live view endpoint for the dashboard.
	//
	// Deep links are gated HERE rather than in the client: whether this
	// viewer may re-enter the owning app is a server-side fact (per-user
	// grants + the app's own WebRestricted), and the browser has no
	// business guessing it. An entry the viewer can't follow gets its
	// url/path cleared, which is exactly the signal the live pill reads
	// to fall back to the Monitor.
	mux.HandleFunc("/api/live", func(w http.ResponseWriter, r *http.Request) {
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
			if !userCanReachApp(r, apps, prefix) {
				entries[i].URL, entries[i].Path = "", ""
				continue
			}
			entries[i].Href = entries[i].ResolveHref()
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
	})

	// Admin-gated pprof — live runtime introspection WITHOUT restarting. The
	// key one for diagnosing a hang ("a run shows running but nothing is
	// happening — no GPU, no tool, no output"): GET /debug/pprof/goroutine?debug=2
	// dumps every goroutine's stack, which names the wedged handler / blocked
	// channel in seconds instead of an elimination exercise. Gated to admins on
	// top of the AuthMiddleware login, since pprof exposes internal state.
	mux.HandleFunc("/debug/pprof/", func(w http.ResponseWriter, r *http.Request) {
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
	})

	// Persistent notify preference: GET returns it, POST toggles and persists.
	mux.HandleFunc("/api/notify-preference", func(w http.ResponseWriter, r *http.Request) {
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
	})

	// Per-request access flags. Apps implement WebAppAccess to expose
	// named boolean flags (e.g. "techwriter": true/false).
	mux.HandleFunc("/api/access", func(w http.ResponseWriter, r *http.Request) {
		flags := make(map[string]bool)
		for _, a := range apps {
			if aa, ok := a.app.(WebAppAccess); ok {
				flags[aa.WebAccessKey()] = aa.WebAccessCheck(r)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(flags)
	})

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
	Log("Gohort Dashboard: %s://%s\n", scheme, addr)

	var handler http.Handler = accessLogMiddleware(mux)
	if AuthDB != nil {
		handler = AuthMiddleware(AuthDB(), handler)
	}
	// Outermost: baseline security headers on every response (incl. auth
	// redirects and error pages).
	handler = securityHeadersMiddleware(handler)
	return ListenAndServeTLS(addr, handler)
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
