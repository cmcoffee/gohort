// Package admin provides an Administrator web panel for managing users,
// viewing system status, and configuring web server settings from the browser.
package admin

import (
	"encoding/json"
	"net/http"

	. "github.com/cmcoffee/gohort/core"
)

func writeTestResult(w http.ResponseWriter, ok bool, message, errMsg string) {
	w.Header().Set("Content-Type", "application/json")
	body := map[string]any{"ok": ok}
	if message != "" {
		body["message"] = message
	}
	if errMsg != "" {
		body["error"] = errMsg
	}
	json.NewEncoder(w).Encode(body)
}

func init() {
	RegisterWebApp(&AdminApp{})
	// Tool-group editor's ✨ Suggest button dispatches here. Worker
	// tier (private routing) so the prompt + member descriptions
	// stay local. Registered so the admin routing table surfaces
	// the stage like the other LLM-touching endpoints.
	RegisterRouteStage(RouteStage{
		Key:     "admin.tool_groups.suggest",
		Label:   "Admin: Tool Groups field-suggest",
		Default: "worker",
		Group:   "Admin",
		Private: true,
	})
}

// AdminApp implements WebApp for the administrator panel.
type AdminApp struct {
	db Database
}

func (a *AdminApp) WebPath() string { return "/admin" }
func (a *AdminApp) WebName() string { return "Administrator" }
func (a *AdminApp) WebDesc() string { return "User management, sessions, and system status" }
func (a *AdminApp) WebOrder() int   { return 99 }

// WebWide renders Administrator as a full-width row at regular height — a
// "double" button sitting on its own at the bottom of the dashboard.
func (a *AdminApp) WebWide() bool { return true }

// WebRestricted hides the admin card from non-admin users or disallowed IPs.
func (a *AdminApp) WebRestricted(r *http.Request) bool {
	if a.db == nil {
		return true
	}
	// If no users configured (auth disabled), hide admin panel.
	if !AuthHasUsers(a.db) {
		return true
	}
	// IP allowlist check.
	if !IsAdminAllowed(r) {
		return true
	}
	return !AuthIsAdmin(a.db, r)
}

func (a *AdminApp) RegisterRoutes(mux *http.ServeMux, prefix string) {
	// Grab the database from SetupWebAgentFunc's wiring. The admin app
	// isn't an Agent, so we use AuthDB which is set by the main app.
	if AuthDB != nil {
		a.db = AuthDB()
	}

	sub := http.NewServeMux()

	// Admin page — framework-rendered (core/ui). Lives at /admin/ (root).
	// Every section is declarative now; the old hand-rolled /admin/legacy
	// surface has been retired.
	sub.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		// First run: an install with no LLM provider cannot do anything, so
		// walk the first admin through connecting one instead of dropping
		// them into a settings page with fifty sections. Dismissable, and
		// self-disabling the moment a provider is saved.
		if systemNeedsSetup(a.db) && !AuthGetFirstRunDismissed(AuthDB(), AuthCurrentUser(r)) {
			http.Redirect(w, r, a.setupWizardPath(), http.StatusFound)
			return
		}
		a.serveNewAdminPage(w, r)
	})

	// First-run wizard (and its skip affordance).
	sub.HandleFunc("/setup", a.handleSetupWizard)

	// API: resource-sharing keys — mint/list, then toggle/delete per key.
	// These administer the grants; the peer-facing surface they authorize is
	// served by core at /api/peer/* and never touches this app. The exact
	// /mint pattern wins over the /api/peer-keys/ subtree that serves {id}:
	// net/http picks the LONGEST matching pattern, so order is cosmetic.
	sub.HandleFunc("/api/peer-keys", a.handlePeerKeys)
	sub.HandleFunc("/api/peer-keys/mint", a.handlePeerKeyMint)
	sub.HandleFunc("/api/peer-keys/", a.handlePeerKeyItem)

	// API: peers this instance borrows FROM — the consuming side. Adding one
	// probes it immediately, so a peer in the table is a peer that answered.
	sub.HandleFunc("/api/peers", a.handlePeers)
	sub.HandleFunc("/api/peers/add", a.handlePeerAdd)
	sub.HandleFunc("/api/peers/", a.handlePeerItem)

	// Apps tab: the enable/disable switchboard and the per-app summary rows
	// (apps_tab.go).
	sub.HandleFunc("/api/apps", a.handleApps)
	sub.HandleFunc("/api/app-summary", a.handleAppSummary)

	// The rest of the API, one file per area. Each register*Routes lives in
	// api_<area>.go beside the handlers it wires; registration order does
	// not matter to the mux.
	a.registerUsersRoutes(sub)
	a.registerSystemRoutes(sub)
	a.registerMaintenanceRoutes(sub)
	a.registerCostRoutes(sub)
	a.registerLLMRoutes(sub)
	a.registerMediaRoutes(sub)
	a.registerTemplatesRoutes(sub)
	a.registerNetConfigRoutes(sub)
	a.registerCredentialsRoutes(sub)
	a.registerMCPRoutes(sub)
	a.registerConnectorsRoutes(sub)
	a.registerArtifactsRoutes(sub)
	a.registerSourceHooksRoutes(sub)
	a.registerToolsRoutes(sub)
	a.registerSkillsRoutes(sub)
	a.registerGroupsRoutes(sub)

	// Gate the entire sub-mux behind IP allowlist + admin check.
	gated := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsAdminAllowed(r) {
			http.NotFound(w, r)
			return
		}
		if a.db != nil && AuthHasUsers(a.db) && !AuthIsAdmin(a.db, r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		sub.ServeHTTP(w, r)
	})

	if prefix != "" {
		mux.Handle(prefix+"/", http.StripPrefix(prefix, gated))
	} else {
		mux.Handle("/", gated)
	}
}

// requireAdmin checks admin status and returns 403 if not.
func (a *AdminApp) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if a.db == nil {
		http.Error(w, "database not available", http.StatusInternalServerError)
		return false
	}
	if AuthHasUsers(a.db) && !AuthIsAdmin(a.db, r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

// --- API Handlers ---

// --- DB Browser ---

// truncStr returns s clipped to n runes with an ellipsis appended when
// trimmed. Used for row-level summary fields so the table response stays
// small without losing the head of long replies/triggers.
func truncStr(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
