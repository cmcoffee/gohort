// Package admin provides an Administrator web panel for managing users,
// viewing system status, and configuring web server settings from the browser.
package admin

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/webui"
)

func init() {
	RegisterWebApp(&AdminApp{})
}

// AdminApp implements WebApp for the administrator panel.
type AdminApp struct {
	db Database
}

func (a *AdminApp) WebPath() string { return "/admin" }
func (a *AdminApp) WebName() string { return "Administrator" }
func (a *AdminApp) WebDesc() string { return "User management, sessions, and system status" }
func (a *AdminApp) WebOrder() int   { return 99 }

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

	// Main page.
	sub.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		webui.WriteHTML(w, webui.RenderPage(webui.PageOpts{
			Title:    "Administrator",
			AppName:  "Administrator",
			Prefix:   prefix,
			BodyHTML: adminBody,
			AppCSS:   adminCSS,
			AppJS:    adminJS,
		}))
	})

	// API: list users.
	sub.HandleFunc("/api/users", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			w.Write(UserListJSON(a.db))
		case http.MethodPost:
			a.handleAddUser(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// API: user operations (update/delete/apps).
	sub.HandleFunc("/api/users/", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/api/users/")
		// Check for /api/users/{user}/{action}
		if parts := strings.SplitN(rest, "/", 2); len(parts) == 2 {
			username := parts[0]
			action := parts[1]
			switch action {
			case "apps":
				if r.Method == http.MethodPut {
					a.handleUpdateUserApps(w, r, username)
				} else {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				}
			case "approve":
				if r.Method == http.MethodPost {
					a.handleApproveUser(w, r, username)
				} else {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				}
			case "reject":
				if r.Method == http.MethodPost {
					a.handleRejectUser(w, r, username)
				} else {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				}
			case "data":
				if r.Method == http.MethodGet {
					a.handleUserDataSummary(w, r, username)
				} else {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				}
			case "data-action":
				if r.Method == http.MethodPost {
					a.handleUserDataAction(w, r, username)
				} else {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				}
			default:
				http.NotFound(w, r)
			}
			return
		}
		username := rest
		if username == "" {
			http.Error(w, "username required", http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodPut:
			a.handleUpdateUser(w, r, username)
		case http.MethodDelete:
			a.handleDeleteUser(w, r, username)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// API: list available apps.
	sub.HandleFunc("/api/apps", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		a.handleListApps(w, r)
	})

	// API: current user identity.
	sub.HandleFunc("/api/whoami", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		username := AuthCurrentUser(r)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"username": username})
	})

	// API: system status.
	sub.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		a.handleStatus(w, r)
	})

	// API: settings (signup toggle, etc.).
	sub.HandleFunc("/api/settings", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			a.handleGetSettings(w, r)
		case http.MethodPut:
			a.handleUpdateSettings(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

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

func (a *AdminApp) handleAddUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Admin    bool   `json:"admin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Password == "" {
		http.Error(w, "email and password required", http.StatusBadRequest)
		return
	}
	if _, exists := AuthGetUser(a.db, req.Username); exists {
		http.Error(w, "user already exists", http.StatusConflict)
		return
	}
	AuthSetUser(a.db, req.Username, req.Password, req.Admin)

	// Prevent the current admin from being locked out.
	current := AuthCurrentUser(r)
	Log("[admin] user %q created user %q (admin=%v)", current, req.Username, req.Admin)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "created"})
}

func (a *AdminApp) handleUpdateUser(w http.ResponseWriter, r *http.Request, username string) {
	var req struct {
		Password string `json:"password,omitempty"`
		Admin    *bool  `json:"admin,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	user, ok := AuthGetUser(a.db, username)
	if !ok {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	admin := user.Admin
	if req.Admin != nil {
		admin = *req.Admin
	}

	// Prevent removing admin from yourself.
	current := AuthCurrentUser(r)
	if current == username && !admin {
		http.Error(w, "cannot remove admin from yourself", http.StatusBadRequest)
		return
	}

	AuthSetUser(a.db, username, req.Password, admin)
	Log("[admin] user %q updated user %q", current, username)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
}

func (a *AdminApp) handleApproveUser(w http.ResponseWriter, r *http.Request, username string) {
	user, ok := AuthGetUser(a.db, username)
	if !ok {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	if !user.Pending {
		http.Error(w, "user is not pending", http.StatusBadRequest)
		return
	}
	AuthApproveUser(a.db, username)
	current := AuthCurrentUser(r)
	Log("[admin] user %q approved %q", current, username)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "approved"})
}

func (a *AdminApp) handleRejectUser(w http.ResponseWriter, r *http.Request, username string) {
	user, ok := AuthGetUser(a.db, username)
	if !ok {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	if !user.Pending {
		http.Error(w, "user is not pending", http.StatusBadRequest)
		return
	}
	AuthRejectUser(a.db, username)
	current := AuthCurrentUser(r)
	Log("[admin] user %q rejected %q", current, username)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "rejected"})
}

func (a *AdminApp) handleDeleteUser(w http.ResponseWriter, r *http.Request, username string) {
	// Prevent deleting yourself.
	current := AuthCurrentUser(r)
	if current == username {
		http.Error(w, "cannot delete yourself", http.StatusBadRequest)
		return
	}
	if _, ok := AuthGetUser(a.db, username); !ok {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	// Refuse to delete if any registered app still has data for this user
	// unless the caller confirms. The admin UI pre-runs reassign/purge
	// via /data-action before this call.
	if r.URL.Query().Get("force") != "1" {
		for _, h := range RegisteredUserDataHandlers() {
			sum := h.Describe(username)
			for _, n := range sum.Counts {
				if n > 0 {
					http.Error(w, "user still has app data; resolve via data-action or pass ?force=1", http.StatusConflict)
					return
				}
			}
		}
	}

	AuthDeleteUser(a.db, username)
	Log("[admin] user %q deleted user %q", current, username)

	w.WriteHeader(http.StatusNoContent)
}

// handleUserDataSummary returns the per-app data footprint for a user so
// the admin UI can offer reassign/purge before deletion.
func (a *AdminApp) handleUserDataSummary(w http.ResponseWriter, r *http.Request, username string) {
	handlers := RegisteredUserDataHandlers()
	out := make([]UserDataSummary, 0, len(handlers))
	for _, h := range handlers {
		out = append(out, h.Describe(username))
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// handleUserDataAction runs reassign/anonymize/purge on a single app's
// data for a single user. Body: {"app":"codewriter","action":"reassign","target":"other@example.com"}.
func (a *AdminApp) handleUserDataAction(w http.ResponseWriter, r *http.Request, username string) {
	var req struct {
		App    string `json:"app"`
		Action string `json:"action"`
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.App == "" || req.Action == "" {
		http.Error(w, "app and action required", http.StatusBadRequest)
		return
	}
	var handler UserDataHandler
	for _, h := range RegisteredUserDataHandlers() {
		if h.AppName() == req.App {
			handler = h
			break
		}
	}
	if handler == nil {
		http.Error(w, "unknown app", http.StatusNotFound)
		return
	}
	var err error
	switch req.Action {
	case "reassign":
		if req.Target == "" {
			http.Error(w, "target required for reassign", http.StatusBadRequest)
			return
		}
		if _, ok := AuthGetUser(a.db, req.Target); !ok {
			http.Error(w, "target user not found", http.StatusNotFound)
			return
		}
		err = handler.Reassign(username, req.Target)
	case "anonymize":
		err = handler.Anonymize(username)
	case "purge":
		err = handler.Purge(username)
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	current := AuthCurrentUser(r)
	Log("[admin] user %q ran %s/%s on %q", current, req.App, req.Action, username)
	w.WriteHeader(http.StatusNoContent)
}

func (a *AdminApp) handleStatus(w http.ResponseWriter, r *http.Request) {
	var allow_signup bool
	a.db.Get("web_config", "allow_signup", &allow_signup)
	status := map[string]interface{}{
		"tls_enabled":     TLSEnabled(),
		"tls_self_signed": TLSSelfSigned,
		"auth_enabled":    AuthHasUsers(a.db),
		"user_count":      len(AuthListUsers(a.db)),
		"active_sessions": len(AllLiveSessions()),
		"allow_signup":    allow_signup,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (a *AdminApp) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	var allow_signup bool
	var session_days, max_attempts, lockout_minutes int
	var service_name, external_url, notify_from string
	a.db.Get("web_config", "allow_signup", &allow_signup)
	a.db.Get("web_config", "session_days", &session_days)
	a.db.Get("web_config", "max_login_attempts", &max_attempts)
	a.db.Get("web_config", "lockout_minutes", &lockout_minutes)
	a.db.Get("web_config", "service_name", &service_name)
	a.db.Get("web_config", "external_url", &external_url)
	a.db.Get("web_config", "notify_from", &notify_from)
	if session_days == 0 {
		session_days = 7
	}
	if max_attempts == 0 {
		max_attempts = 5
	}
	if lockout_minutes == 0 {
		lockout_minutes = 15
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"allow_signup":       allow_signup,
		"session_days":       session_days,
		"max_login_attempts": max_attempts,
		"lockout_minutes":    lockout_minutes,
		"service_name":       service_name,
		"external_url":       external_url,
		"notify_from":        notify_from,
		"default_apps":       AuthGetDefaultApps(a.db),
	})
}

func (a *AdminApp) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AllowSignup      *bool     `json:"allow_signup,omitempty"`
		SessionDays      *int      `json:"session_days,omitempty"`
		MaxLoginAttempts *int      `json:"max_login_attempts,omitempty"`
		LockoutMinutes   *int      `json:"lockout_minutes,omitempty"`
		ServiceName      *string   `json:"service_name,omitempty"`
		ExternalURL      *string   `json:"external_url,omitempty"`
		NotifyFrom       *string   `json:"notify_from,omitempty"`
		DefaultApps      *[]string `json:"default_apps,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	current := AuthCurrentUser(r)
	if req.AllowSignup != nil {
		a.db.Set("web_config", "allow_signup", *req.AllowSignup)
		Log("[admin] user %q set allow_signup=%v", current, *req.AllowSignup)
	}
	if req.SessionDays != nil && *req.SessionDays >= 1 && *req.SessionDays <= 90 {
		a.db.Set("web_config", "session_days", *req.SessionDays)
		Log("[admin] user %q set session_days=%d", current, *req.SessionDays)
	}
	if req.MaxLoginAttempts != nil && *req.MaxLoginAttempts >= 1 && *req.MaxLoginAttempts <= 100 {
		a.db.Set("web_config", "max_login_attempts", *req.MaxLoginAttempts)
		Log("[admin] user %q set max_login_attempts=%d", current, *req.MaxLoginAttempts)
	}
	if req.LockoutMinutes != nil && *req.LockoutMinutes >= 1 && *req.LockoutMinutes <= 1440 {
		a.db.Set("web_config", "lockout_minutes", *req.LockoutMinutes)
		Log("[admin] user %q set lockout_minutes=%d", current, *req.LockoutMinutes)
	}
	if req.ServiceName != nil {
		a.db.Set("web_config", "service_name", *req.ServiceName)
		Log("[admin] user %q set service_name=%q", current, *req.ServiceName)
	}
	if req.ExternalURL != nil {
		a.db.Set("web_config", "external_url", *req.ExternalURL)
		Log("[admin] user %q set external_url=%q", current, *req.ExternalURL)
	}
	if req.NotifyFrom != nil {
		a.db.Set("web_config", "notify_from", *req.NotifyFrom)
		Log("[admin] user %q set notify_from=%q", current, *req.NotifyFrom)
	}
	if req.DefaultApps != nil {
		AuthSetDefaultApps(a.db, *req.DefaultApps)
		Log("[admin] user %q set default_apps=%v", current, *req.DefaultApps)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
}

// handleListApps returns all registered web apps (excluding admin) for the
// app assignment UI.
func (a *AdminApp) handleListApps(w http.ResponseWriter, r *http.Request) {
	type appInfo struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	var apps []appInfo
	for _, wa := range RegisteredWebApps() {
		if wa.WebPath() == "/admin" {
			continue
		}
		apps = append(apps, appInfo{Path: wa.WebPath(), Name: wa.WebName()})
	}
	for _, ag := range RegisteredApps() {
		if wa, ok := ag.(WebApp); ok && wa.WebPath() != "/admin" {
			apps = append(apps, appInfo{Path: wa.WebPath(), Name: wa.WebName()})
		}
	}
	for _, ag := range RegisteredAgents() {
		if wa, ok := ag.(WebApp); ok && wa.WebPath() != "/admin" {
			apps = append(apps, appInfo{Path: wa.WebPath(), Name: wa.WebName()})
		}
	}
	// Deduplicate.
	seen := make(map[string]bool)
	var unique []appInfo
	for _, ap := range apps {
		if !seen[ap.Path] {
			seen[ap.Path] = true
			unique = append(unique, ap)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(unique)
}

// handleUpdateUserApps sets the allowed apps for a specific user.
func (a *AdminApp) handleUpdateUserApps(w http.ResponseWriter, r *http.Request, username string) {
	var req struct {
		Apps []string `json:"apps"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if _, ok := AuthGetUser(a.db, username); !ok {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	AuthSetUserApps(a.db, username, req.Apps)
	current := AuthCurrentUser(r)
	Log("[admin] user %q set apps for %q: %v", current, username, req.Apps)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
}

// --- UI ---

const adminCSS = `
.admin-container {
  max-width: 800px; margin: 0 auto; padding: 2rem 1rem;
}
.section {
  background: #161b22; border: 1px solid #30363d; border-radius: 8px;
  padding: 1.5rem; margin-bottom: 1.5rem;
}
.section h2 {
  font-size: 1.1rem; color: #f0f6fc; margin-bottom: 1rem;
  padding-bottom: 0.5rem; border-bottom: 1px solid #21262d;
}
.user-table {
  width: 100%; border-collapse: collapse;
}
.user-table th {
  text-align: left; padding: 0.5rem 0.75rem; color: #8b949e;
  font-size: 0.8rem; font-weight: 600; text-transform: uppercase;
  border-bottom: 1px solid #21262d;
}
.user-table td {
  padding: 0.6rem 0.75rem; border-bottom: 1px solid #21262d;
  font-size: 0.9rem;
}
.user-table tr:last-child td { border-bottom: none; }
.badge {
  font-size: 0.7rem; padding: 0.15rem 0.5rem; border-radius: 10px;
  font-weight: 600;
}
.badge-admin { background: #388bfd26; color: #58a6ff; }
.badge-user { background: #3fb95026; color: #3fb950; }
.badge-pending { background: #d2992226; color: #d29922; }
.btn {
  padding: 0.4rem 0.8rem; border-radius: 6px; border: 1px solid #30363d;
  background: #21262d; color: #c9d1d9; font-size: 0.8rem; cursor: pointer;
  transition: border-color 0.2s;
}
.btn:hover { border-color: #58a6ff; }
.btn-danger { border-color: #da3633; color: #f85149; }
.btn-danger:hover { background: #da363340; }
.btn-primary {
  background: #238636; border-color: #2ea043; color: #fff;
  font-weight: 600;
}
.btn-primary:hover { background: #2ea043; }
.add-form {
  display: flex; gap: 0.5rem; flex-wrap: wrap; align-items: flex-end;
  margin-top: 1rem; padding-top: 1rem; border-top: 1px solid #21262d;
}
.add-form .field { display: flex; flex-direction: column; gap: 0.25rem; }
.add-form label { font-size: 0.75rem; color: #8b949e; }
.add-form input[type="text"],
.add-form input[type="password"] {
  padding: 0.4rem 0.6rem; background: #0d1117;
  border: 1px solid #30363d; border-radius: 6px;
  color: #c9d1d9; font-size: 0.85rem;
}
.add-form input:focus { outline: none; border-color: #58a6ff; }
.checkbox-label {
  display: flex; align-items: center; gap: 0.4rem;
  font-size: 0.85rem; color: #c9d1d9; cursor: pointer;
  padding-bottom: 0.35rem;
}
.status-grid {
  display: grid; grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
  gap: 1rem;
}
.status-card {
  background: #0d1117; border: 1px solid #21262d; border-radius: 6px;
  padding: 1rem; text-align: center;
}
.status-card .value { font-size: 1.5rem; font-weight: 700; color: #f0f6fc; }
.status-card .label { font-size: 0.75rem; color: #8b949e; margin-top: 0.3rem; }
.actions { display: flex; gap: 0.4rem; }
.current-user { color: #8b949e; font-size: 0.75rem; font-style: italic; }
.setting-row { padding: 0.4rem 0; }
.toggle-label {
  display: flex; align-items: center; gap: 0.5rem;
  font-size: 0.9rem; color: #c9d1d9; cursor: pointer;
}
.toggle-label input[type="checkbox"] {
  width: 1rem; height: 1rem; accent-color: #58a6ff; cursor: pointer;
}
.setting-desc {
  display: block; font-size: 0.8rem; color: #8b949e;
  margin-top: 0.3rem; margin-left: 1.5rem;
}
.app-chips {
  display: flex; flex-wrap: wrap; gap: 0.3rem; margin-top: 0.2rem;
}
.app-chip {
  font-size: 0.7rem; padding: 0.15rem 0.45rem; border-radius: 4px;
  background: #30363d; color: #c9d1d9;
}
.app-chip.default { background: #1f6feb33; color: #58a6ff; }
.app-select-panel {
  margin-top: 0.5rem; padding: 0.75rem;
  background: #0d1117; border: 1px solid #21262d; border-radius: 6px;
}
.app-select-panel label {
  display: flex; align-items: center; gap: 0.4rem;
  font-size: 0.85rem; color: #c9d1d9; cursor: pointer;
  padding: 0.2rem 0;
}
.app-select-panel input[type="checkbox"] {
  accent-color: #58a6ff; cursor: pointer;
}
.default-apps-panel {
  margin-top: 0.75rem;
}
.default-apps-panel .app-select-panel {
  margin-top: 0.3rem;
}
`

const adminBody = `
<div class="admin-container">
  <div class="section" style="display:flex;align-items:center;gap:0.75rem;margin-bottom:0.5rem">
    <span class="app-title" style="font-size:1.4rem">Administrator</span>
  </div>
  <div class="section">
    <h2>System Status</h2>
    <div id="status-grid" class="status-grid"></div>
  </div>
  <div class="section">
    <h2>Settings</h2>
    <div class="setting-row">
      <label class="toggle-label">
        <input type="checkbox" id="toggle-signup" onchange="toggleSignup(this.checked)">
        <span>Allow New User Signup</span>
      </label>
      <span class="setting-desc">When enabled, a sign-up link appears on the login page for new users to create their own accounts.</span>
    </div>
    <div class="setting-row">
      <label style="font-size:0.9rem;color:#c9d1d9">Session Length (days)</label>
      <span class="setting-desc">How long login sessions last before requiring re-authentication (1-90).</span>
      <input type="number" id="session-days" min="1" max="90" value="7"
        style="margin-top:0.3rem;width:5rem;padding:0.4rem 0.6rem;background:#0d1117;border:1px solid #30363d;border-radius:6px;color:#c9d1d9;font-size:0.85rem"
        onchange="updateSetting('session_days', parseInt(this.value))">
    </div>
    <div class="setting-row" style="display:flex;gap:1.5rem;flex-wrap:wrap">
      <div>
        <label style="font-size:0.9rem;color:#c9d1d9">Max Login Attempts</label>
        <span class="setting-desc">Failed attempts before IP lockout (1-100).</span>
        <input type="number" id="max-attempts" min="1" max="100" value="5"
          style="margin-top:0.3rem;width:5rem;padding:0.4rem 0.6rem;background:#0d1117;border:1px solid #30363d;border-radius:6px;color:#c9d1d9;font-size:0.85rem"
          onchange="updateSetting('max_login_attempts', parseInt(this.value))">
      </div>
      <div>
        <label style="font-size:0.9rem;color:#c9d1d9">Lockout Duration (minutes)</label>
        <span class="setting-desc">How long an IP is locked out (1-1440).</span>
        <input type="number" id="lockout-minutes" min="1" max="1440" value="15"
          style="margin-top:0.3rem;width:5rem;padding:0.4rem 0.6rem;background:#0d1117;border:1px solid #30363d;border-radius:6px;color:#c9d1d9;font-size:0.85rem"
          onchange="updateSetting('lockout_minutes', parseInt(this.value))">
      </div>
    </div>
    <div class="setting-row">
      <label style="font-size:0.9rem;color:#c9d1d9">Service Name</label>
      <span class="setting-desc">Name used in notification email subjects (default: Gohort).</span>
      <input type="text" id="service-name" placeholder="Gohort"
        style="margin-top:0.3rem;width:15rem;padding:0.4rem 0.6rem;background:#0d1117;border:1px solid #30363d;border-radius:6px;color:#c9d1d9;font-size:0.85rem"
        onchange="updateSetting('service_name', this.value)">
    </div>
    <div class="setting-row">
      <label style="font-size:0.9rem;color:#c9d1d9">External URL</label>
      <span class="setting-desc">Public-facing URL for notification links. Leave blank to use listen address.</span>
      <input type="text" id="external-url" placeholder="https://gohort.example.com"
        style="margin-top:0.3rem;width:20rem;padding:0.4rem 0.6rem;background:#0d1117;border:1px solid #30363d;border-radius:6px;color:#c9d1d9;font-size:0.85rem"
        onchange="updateSetting('external_url', this.value)">
    </div>
    <div class="setting-row">
      <label style="font-size:0.9rem;color:#c9d1d9">Notification From Address</label>
      <span class="setting-desc">From address for notification emails (default: uses mail config).</span>
      <input type="text" id="notify-from" placeholder="notifications@example.com"
        style="margin-top:0.3rem;width:20rem;padding:0.4rem 0.6rem;background:#0d1117;border:1px solid #30363d;border-radius:6px;color:#c9d1d9;font-size:0.85rem"
        onchange="updateSetting('notify_from', this.value)">
    </div>
    <div class="setting-row default-apps-panel">
      <span style="font-size:0.9rem;color:#c9d1d9">Default Apps for New Users</span>
      <span class="setting-desc">Apps assigned to new users who sign up. Users with no custom assignments use these defaults.</span>
      <div id="default-apps-list" class="app-select-panel"></div>
    </div>
  </div>
  <div class="section">
    <h2>User Management</h2>
    <table class="user-table">
      <thead><tr>
        <th>Email</th><th>Role</th><th>Apps</th><th>Actions</th>
      </tr></thead>
      <tbody id="user-list"></tbody>
    </table>
    <div class="add-form">
      <div class="field">
        <label>Email</label>
        <input type="text" id="new-username" placeholder="email">
      </div>
      <div class="field">
        <label>Password</label>
        <input type="password" id="new-password" placeholder="password">
      </div>
      <div class="field">
        <label class="checkbox-label">
          <input type="checkbox" id="new-admin"> Admin
        </label>
      </div>
      <button class="btn btn-primary" onclick="addUser()">Add User</button>
    </div>
  </div>
</div>
`

const adminJS = `
var currentUser = '';
var allApps = [];

function loadApps() {
  return fetch('api/apps').then(function(r){ return r.json(); }).then(function(apps){
    allApps = apps || [];
  });
}

function loadUsers() {
  fetch('api/users').then(function(r) {
    if (r.status === 401) { window.location = '/login'; return; }
    if (r.status === 403) { document.body.innerHTML = '<p style="padding:2rem;color:#f85149">Admin access required.</p>'; return; }
    return r.json();
  }).then(function(users) {
    if (!users) return;
    var tbody = document.getElementById('user-list');
    var html = '';

    // Sort: pending users first, then by username.
    users.sort(function(a, b) {
      if (a.pending !== b.pending) return a.pending ? -1 : 1;
      return a.username.localeCompare(b.username);
    });

    for (var i = 0; i < users.length; i++) {
      var u = users[i];
      var badge;
      if (u.pending) {
        badge = '<span class="badge badge-pending">pending</span>';
      } else if (u.admin) {
        badge = '<span class="badge badge-admin">admin</span>';
      } else {
        badge = '<span class="badge badge-user">user</span>';
      }
      var you = (u.username === currentUser) ? ' <span class="current-user">(you)</span>' : '';

      // App chips.
      var appsHtml = '';
      if (u.pending) {
        appsHtml = '<span class="app-chip default">pending</span>';
      } else if (u.admin) {
        appsHtml = '<span class="app-chip default">all apps</span>';
      } else if (u.apps && u.apps.length > 0) {
        for (var j = 0; j < u.apps.length; j++) {
          var name = appName(u.apps[j]);
          appsHtml += '<span class="app-chip">' + name + '</span>';
        }
      } else {
        appsHtml = '<span class="app-chip default">defaults</span>';
      }

      var actions = '<div class="actions">';
      if (u.pending) {
        actions += '<button class="btn btn-primary" onclick="approveUser(\'' + u.username + '\')">Approve</button>';
        actions += '<button class="btn btn-danger" onclick="rejectUser(\'' + u.username + '\')">Reject</button>';
      } else {
        actions += '<button class="btn" onclick="changePassword(\'' + u.username + '\')">Password</button>';
        if (!u.admin) {
          actions += '<button class="btn" onclick="editApps(\'' + u.username + '\')">Apps</button>';
        }
        if (u.username !== currentUser) {
          actions += '<button class="btn" onclick="toggleAdmin(\'' + u.username + '\',' + !u.admin + ')">' + (u.admin ? 'Demote' : 'Promote') + '</button>';
          actions += '<button class="btn btn-danger" onclick="deleteUser(\'' + u.username + '\')">Delete</button>';
        }
      }
      actions += '</div>';
      html += '<tr><td>' + u.username + you + '</td><td>' + badge + '</td><td><div class="app-chips">' + appsHtml + '</div></td><td>' + actions + '</td></tr>';
    }
    tbody.innerHTML = html;
  });
}

function appName(path) {
  for (var i = 0; i < allApps.length; i++) {
    if (allApps[i].path === path) return allApps[i].name;
  }
  return path;
}

function loadStatus() {
  fetch('api/status').then(function(r) {
    if (r.status === 401) return;
    return r.json();
  }).then(function(s) {
    if (!s) return;
    var grid = document.getElementById('status-grid');
    grid.innerHTML =
      statusCard(s.user_count, 'Users') +
      statusCard(s.active_sessions, 'Active Sessions') +
      statusCard(s.tls_enabled ? 'Yes' : 'No', 'TLS Enabled') +
      statusCard(s.auth_enabled ? 'Yes' : 'No', 'Auth Enabled');
    var cb = document.getElementById('toggle-signup');
    if (cb) cb.checked = !!s.allow_signup;
  });
}

function loadSettings() {
  fetch('api/settings').then(function(r){ return r.json(); }).then(function(s){
    if (!s) return;
    setField('session-days', s.session_days || 7);
    setField('max-attempts', s.max_login_attempts || 5);
    setField('lockout-minutes', s.lockout_minutes || 15);
    setField('service-name', s.service_name || '');
    setField('external-url', s.external_url || '');
    setField('notify-from', s.notify_from || '');
    var defaults = s.default_apps || [];
    renderAppCheckboxes('default-apps-list', defaults, function(apps){
      fetch('api/settings', {
        method: 'PUT',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({default_apps: apps})
      });
    });
  });
}

function setField(id, val) {
  var el = document.getElementById(id);
  if (el) el.value = val;
}

function updateSetting(key, val) {
  var payload = {};
  payload[key] = val;
  fetch('api/settings', {
    method: 'PUT',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify(payload)
  });
}

function renderAppCheckboxes(containerId, selected, onChange) {
  var container = document.getElementById(containerId);
  if (!container || !allApps.length) return;
  var html = '';
  for (var i = 0; i < allApps.length; i++) {
    var ap = allApps[i];
    var checked = selected.indexOf(ap.path) !== -1 ? ' checked' : '';
    html += '<label><input type="checkbox" value="' + ap.path + '"' + checked + '> ' + ap.name + '</label>';
  }
  container.innerHTML = html;
  var inputs = container.querySelectorAll('input[type="checkbox"]');
  for (var j = 0; j < inputs.length; j++) {
    inputs[j].addEventListener('change', function(){
      var sel = [];
      var boxes = container.querySelectorAll('input[type="checkbox"]');
      for (var k = 0; k < boxes.length; k++) {
        if (boxes[k].checked) sel.push(boxes[k].value);
      }
      onChange(sel);
    });
  }
}

function statusCard(value, label) {
  return '<div class="status-card"><div class="value">' + value + '</div><div class="label">' + label + '</div></div>';
}

function toggleSignup(enabled) {
  fetch('api/settings', {
    method: 'PUT',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({allow_signup: enabled})
  });
}

function addUser() {
  var username = document.getElementById('new-username').value.trim();
  var password = document.getElementById('new-password').value;
  var admin = document.getElementById('new-admin').checked;
  if (!username || !password) { alert('Email and password required.'); return; }
  fetch('api/users', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({username: username, password: password, admin: admin})
  }).then(function(r) {
    if (!r.ok) return r.text().then(function(t) { alert(t); });
    document.getElementById('new-username').value = '';
    document.getElementById('new-password').value = '';
    document.getElementById('new-admin').checked = false;
    loadUsers();
    loadStatus();
  });
}

function changePassword(username) {
  var pw = prompt('New password for ' + username + ':');
  if (!pw) return;
  fetch('api/users/' + encodeURIComponent(username), {
    method: 'PUT',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({password: pw})
  }).then(function(r) {
    if (!r.ok) return r.text().then(function(t) { alert(t); });
    alert('Password updated.');
  });
}

function editApps(username) {
  // Fetch current user data to get their apps.
  fetch('api/users').then(function(r){ return r.json(); }).then(function(users){
    var user = null;
    for (var i = 0; i < users.length; i++) {
      if (users[i].username === username) { user = users[i]; break; }
    }
    if (!user) return;

    var selected = user.apps || [];
    var html = '<div style="padding:0.5rem 0;font-size:0.85rem;color:#8b949e;margin-bottom:0.3rem">Apps for ' + username + ' (empty = use defaults):</div>';
    for (var i = 0; i < allApps.length; i++) {
      var ap = allApps[i];
      var checked = selected.indexOf(ap.path) !== -1 ? ' checked' : '';
      html += '<label style="display:flex;align-items:center;gap:0.4rem;padding:0.2rem 0;font-size:0.85rem;color:#c9d1d9;cursor:pointer">';
      html += '<input type="checkbox" value="' + ap.path + '"' + checked + ' style="accent-color:#58a6ff;cursor:pointer"> ' + ap.name + '</label>';
    }
    html += '<div style="margin-top:0.5rem;display:flex;gap:0.4rem">';
    html += '<button class="btn btn-primary" id="save-user-apps">Save</button>';
    html += '<button class="btn" id="cancel-user-apps">Cancel</button>';
    html += '</div>';

    // Show inline panel below the user row.
    var existing = document.getElementById('edit-apps-panel');
    if (existing) existing.remove();
    var panel = document.createElement('tr');
    panel.id = 'edit-apps-panel';
    panel.innerHTML = '<td colspan="4"><div class="app-select-panel">' + html + '</div></td>';

    // Find the user row and insert after it.
    var rows = document.getElementById('user-list').querySelectorAll('tr');
    for (var j = 0; j < rows.length; j++) {
      if (rows[j].querySelector('td') && rows[j].querySelector('td').textContent.indexOf(username) !== -1) {
        rows[j].after(panel);
        break;
      }
    }

    document.getElementById('save-user-apps').onclick = function(){
      var boxes = panel.querySelectorAll('input[type="checkbox"]');
      var apps = [];
      for (var k = 0; k < boxes.length; k++) {
        if (boxes[k].checked) apps.push(boxes[k].value);
      }
      fetch('api/users/' + encodeURIComponent(username) + '/apps', {
        method: 'PUT',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({apps: apps})
      }).then(function(r){
        if (!r.ok) return r.text().then(function(t) { alert(t); });
        panel.remove();
        loadUsers();
      });
    };
    document.getElementById('cancel-user-apps').onclick = function(){ panel.remove(); };
  });
}

function toggleAdmin(username, makeAdmin) {
  fetch('api/users/' + encodeURIComponent(username), {
    method: 'PUT',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({admin: makeAdmin})
  }).then(function(r) {
    if (!r.ok) return r.text().then(function(t) { alert(t); });
    loadUsers();
  });
}

function approveUser(username) {
  if (!confirm('Approve ' + username + '?')) return;
  fetch('api/users/' + encodeURIComponent(username) + '/approve', {
    method: 'POST'
  }).then(function(r) {
    if (!r.ok) return r.text().then(function(t) { alert(t); });
    loadUsers();
    loadStatus();
  });
}

function rejectUser(username) {
  if (!confirm('Reject and remove ' + username + '?')) return;
  fetch('api/users/' + encodeURIComponent(username) + '/reject', {
    method: 'POST'
  }).then(function(r) {
    if (!r.ok) return r.text().then(function(t) { alert(t); });
    loadUsers();
    loadStatus();
  });
}

function deleteUser(username) {
  fetch('api/users/' + encodeURIComponent(username) + '/data').then(function(r) {
    return r.ok ? r.json() : [];
  }).then(function(summary) {
    var hasData = summary.some(function(s) {
      return Object.values(s.counts || {}).some(function(n) { return n > 0; });
    });
    if (!hasData) {
      if (!confirm('Delete user ' + username + '? They have no app data.')) return;
      return doDeleteUser(username);
    }
    showUserDataModal(username, summary);
  });
}

function showUserDataModal(username, summary) {
  var lines = summary.filter(function(s) {
    return Object.values(s.counts || {}).some(function(n) { return n > 0; });
  }).map(function(s) {
    var parts = [];
    for (var k in s.counts) { if (s.counts[k] > 0) parts.push(s.counts[k] + ' ' + k); }
    return s.app + ': ' + parts.join(', ') + ' (' + (s.actions || []).join('/') + ')';
  }).join('\n');

  var msg = 'User ' + username + ' has app data:\n\n' + lines + '\n\n' +
    'Handle each app before deleting. For each app, enter one of:\n' +
    '  reassign:target@example.com\n' +
    '  purge\n' +
    '  skip (leaves data in place; delete will be blocked)\n\n' +
    'Type "cancel" at any prompt to abort.';
  if (!confirm(msg)) return;

  var actions = [];
  for (var i = 0; i < summary.length; i++) {
    var s = summary[i];
    var total = 0;
    for (var k in s.counts) total += s.counts[k];
    if (total === 0) continue;
    var ans = prompt(s.app + ' (' + total + ' items, actions: ' + (s.actions || []).join('/') + '):', 'reassign:');
    if (ans === null || ans === 'cancel') return;
    ans = ans.trim();
    if (ans === '' || ans === 'skip') continue;
    if (ans.indexOf('reassign:') === 0) {
      actions.push({app: s.app, action: 'reassign', target: ans.substring(9).trim()});
    } else if (ans === 'purge' || ans === 'anonymize') {
      actions.push({app: s.app, action: ans});
    } else {
      alert('Unrecognized: ' + ans);
      return;
    }
  }

  runUserDataActions(username, actions, 0);
}

function runUserDataActions(username, actions, idx) {
  if (idx >= actions.length) {
    if (!confirm('All actions complete. Delete user ' + username + ' now?')) return;
    return doDeleteUser(username);
  }
  fetch('api/users/' + encodeURIComponent(username) + '/data-action', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify(actions[idx])
  }).then(function(r) {
    if (!r.ok) return r.text().then(function(t) { alert('Failed on ' + actions[idx].app + ': ' + t); });
    runUserDataActions(username, actions, idx + 1);
  });
}

function doDeleteUser(username) {
  return fetch('api/users/' + encodeURIComponent(username), {
    method: 'DELETE'
  }).then(function(r) {
    if (!r.ok) return r.text().then(function(t) { alert(t); });
    loadUsers();
    loadStatus();
  });
}

document.addEventListener('DOMContentLoaded', function() {
  loadApps().then(function(){
    fetch('api/whoami').then(function(r){ return r.json(); }).then(function(d){
      currentUser = d.username || '';
      loadUsers();
    }).catch(function() { loadUsers(); });
    loadStatus();
    loadSettings();
  });
});
`
