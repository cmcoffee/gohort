// Package admin provides an Administrator web panel for managing users,
// viewing system status, and configuring web server settings from the browser.
package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// probeEmbeddingModels fetches and parses a model list from an
// embedding endpoint's discovery URL. shape controls how to interpret
// the response body:
//   - "openai":  {"data": [{"id": "..."}, ...]}  (OpenAI, vLLM, llama.cpp /v1/models, hf-tei /models)
//   - "ollama":  {"models": [{"name": "..."}, ...]}  (Ollama /api/tags)
//
// Returns an empty slice on any error so the caller can quietly fall
// back to the next probe form without surfacing an error to the UI.
func probeEmbeddingModels(ctx context.Context, url, apiKey, shape string) []string {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	switch shape {
	case "openai":
		var body struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return nil
		}
		out := make([]string, 0, len(body.Data))
		for _, m := range body.Data {
			if m.ID != "" {
				out = append(out, m.ID)
			}
		}
		return out
	case "ollama":
		var body struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return nil
		}
		out := make([]string, 0, len(body.Models))
		for _, m := range body.Models {
			if m.Name != "" {
				out = append(out, m.Name)
			}
		}
		return out
	}
	return nil
}

// writeTestResult is the shared response shape for connectivity-test
// endpoints wired into FormPanel.TestURL buttons. Always responds 200
// — the ok flag carries pass/fail so the client can render success or
// error inline next to the Test button without HTTP-status branching.
// templateSchemaMap is the payload the generic renderer consumes for both Add
// (values nil, create=true) and Configure (values from ReadValues, create=false).
func templateSchemaMap(t Template, vals map[string]any, connector string, create bool) map[string]any {
	target := t.Target
	if target == "" {
		target = TargetConnector
	}
	return map[string]any{
		"template":  t.Name,
		"label":     t.Label,
		"category":  t.Category,
		"target":    target,
		"fields":    t.Fields,
		"detect":    t.HasDetect(),
		"values":    vals,
		"connector": connector,
		"create":    create,
	}
}

// nestComfyWorkflow renders a spec for the Edit-spec view with comfy_workflow as
// a NESTED JSON object instead of an escaped string, so the graph is readable.
// Returns (indented, true) only when it nested a string-held workflow; otherwise
// (nil, false) to fall back to plain indentation (non-image specs, or a workflow
// already stored as an object).
func nestComfyWorkflow(spec []byte) ([]byte, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(spec, &m) != nil {
		return nil, false
	}
	wf, ok := m["comfy_workflow"]
	if !ok {
		return nil, false
	}
	var inner string
	if json.Unmarshal(wf, &inner) != nil || !json.Valid([]byte(inner)) {
		return nil, false // not a JSON-string-holding-JSON
	}
	m["comfy_workflow"] = json.RawMessage(inner)
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, false
	}
	return out, true
}

// stringifyComfyWorkflow is the save-path inverse: if comfy_workflow was edited as
// a nested object (from nestComfyWorkflow's view), fold it back into a string
// (pretty-printed) so it matches the spec's string field. An already-string (or
// absent) workflow is left untouched.
func stringifyComfyWorkflow(body []byte) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return body
	}
	wf, ok := m["comfy_workflow"]
	if !ok {
		return body
	}
	t := bytes.TrimSpace(wf)
	if len(t) == 0 || t[0] == '"' {
		return body // already a JSON string
	}
	strified, err := json.Marshal(PrettyComfyJSON(string(t)))
	if err != nil {
		return body
	}
	m["comfy_workflow"] = json.RawMessage(strified)
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

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

// RegisterRoutes configures the administrative web interface and API endpoints.
// It sets up a sub-mux with routes for user management, system settings,
// cost tracking, and vector statistics, then prepares a gated handler to
// be mounted to the provided mux under the specified prefix.
// saveCredWithPassword saves a secure-API credential, plus the oauth2
// password-grant SECOND secret (the resource-owner password) when the grant is
// password. Empty password = keep the existing one, mirroring Save's
// empty-means-keep secret semantics.
func saveCredWithPassword(c SecureCredential, secret, password string) error {
	if err := Secure().Save(c, secret); err != nil {
		return err
	}
	if c.Grant == OAuthGrantPassword {
		return Secure().SavePassword(c.Name, password)
	}
	return nil
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
		a.serveNewAdminPage(w, r)
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

	// API: user candidates for ACL pickers ([{value,label}], approved users only).
	// Shared source for every ui.ACLPicker on this page (credential + tool access).
	sub.HandleFunc("/api/user-candidates", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(UserCandidatesJSON(a.db))
	})

	// API: feature access — the admin gate on outward-facing surfaces a user can
	// expose through their own keys (the OpenAI /v1 endpoint is the first). Reads
	// the generic shareable-feature registry; writes the per-feature allow-list.
	// GET (list) rows one per registered feature; GET ?feature= one policy record
	// for the ACLPicker; POST ?feature= sets its allowed_users. Generic over the
	// registry — an app declares a feature, this renders and stores it, with no
	// per-feature code here.
	sub.HandleFunc("/api/feature-access", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		feature := strings.TrimSpace(r.URL.Query().Get("feature"))
		switch r.Method {
		case http.MethodGet:
			if feature != "" {
				p := LoadFeaturePolicy(a.db, feature)
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"feature": feature, "allowed_users": p.AllowedUsers})
				return
			}
			type row struct {
				Feature      string   `json:"feature"` // RowKey
				Label        string   `json:"label"`
				Desc         string   `json:"desc"`
				AllowedUsers []string `json:"allowed_users,omitempty"`
				Access       string   `json:"access"` // human summary for the table
			}
			rows := []row{}
			for _, f := range ShareableFeatures() {
				p := LoadFeaturePolicy(a.db, f.Key)
				access := "All users"
				if len(p.AllowedUsers) > 0 {
					access = fmt.Sprintf("%d user(s)", len(p.AllowedUsers))
				}
				rows = append(rows, row{Feature: f.Key, Label: f.Label, Desc: f.Desc, AllowedUsers: p.AllowedUsers, Access: access})
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(rows)
		case http.MethodPost:
			if feature == "" || !IsShareableFeature(feature) {
				http.Error(w, "unknown feature", http.StatusBadRequest)
				return
			}
			var body struct {
				AllowedUsers []string `json:"allowed_users"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			SetFeatureAllowedUsers(a.db, feature, body.AllowedUsers)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// API: user-owned credentials — the governance view over the user plane. The
	// admin API Credentials page shows only GLOBAL creds; this surfaces the ones
	// users create for themselves, with owner-aware revoke (disable) + delete.
	sub.HandleFunc("/api/user-credentials", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			type row struct {
				ID       string `json:"id"` // owner/name — unique across users (RowKey)
				Owner    string `json:"owner"`
				Name     string `json:"name"`
				Type     string `json:"type"`
				Disabled bool   `json:"disabled"`
				Secured  bool   `json:"secured"`
			}
			rows := []row{}
			for _, c := range Secure().ListAllUserOwned() {
				rows = append(rows, row{ID: c.Owner + "/" + c.Name, Owner: c.Owner, Name: c.Name, Type: c.Type, Disabled: c.Disabled, Secured: c.Secured})
			}
			sort.Slice(rows, func(i, j int) bool {
				if rows[i].Owner != rows[j].Owner {
					return rows[i].Owner < rows[j].Owner
				}
				return rows[i].Name < rows[j].Name
			})
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(rows)
		case http.MethodPost:
			owner := strings.TrimSpace(r.URL.Query().Get("owner"))
			name := strings.TrimSpace(r.URL.Query().Get("name"))
			if owner == "" || name == "" {
				http.Error(w, "owner and name required", http.StatusBadRequest)
				return
			}
			var err error
			switch r.URL.Query().Get("action") {
			case "disable":
				err = Secure().SetDisabledOwned(owner, name, true)
			case "enable":
				err = Secure().SetDisabledOwned(owner, name, false)
			default:
				http.Error(w, "action must be enable|disable", http.StatusBadRequest)
				return
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			owner := strings.TrimSpace(r.URL.Query().Get("owner"))
			name := strings.TrimSpace(r.URL.Query().Get("name"))
			if owner == "" || name == "" {
				http.Error(w, "owner and name required", http.StatusBadRequest)
				return
			}
			if err := Secure().DeleteUser(owner, name); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// API: user-owned agents — the governance view over peer-shared agents. Agents
	// live in per-user UDBs, so the admin app enumerates them through an orchestrate
	// hook (nil until orchestrate's init runs). Revoke clears an agent's recipient
	// list without deleting the agent.
	sub.HandleFunc("/api/user-agents", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			rows := []UserOwnedAgentRow{}
			if AdminListUserOwnedAgents != nil {
				rows = AdminListUserOwnedAgents(a.db)
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(rows)
		case http.MethodPost:
			action := r.URL.Query().Get("action")
			owner := strings.TrimSpace(r.URL.Query().Get("owner"))
			id := strings.TrimSpace(r.URL.Query().Get("id"))
			if owner == "" || id == "" {
				http.Error(w, "owner and id required", http.StatusBadRequest)
				return
			}
			var hook func(Database, string, string) error
			switch action {
			case "revoke_share":
				hook = AdminRevokeAgentShare
			case "publish": // admin "delegate to users" — flip Exposed on
				hook = AdminPublishAgent
			default:
				http.Error(w, "action must be revoke_share|publish", http.StatusBadRequest)
				return
			}
			if hook == nil {
				http.Error(w, "unavailable (orchestrate not wired)", http.StatusServiceUnavailable)
				return
			}
			if err := hook(a.db, owner, id); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// API: promotion requests — the bottom-up publish queue. Users request that
	// their own resource be published deployment-wide; the admin approves (runs the
	// kind-specific side effect) or denies. Today only tool promotion is wired:
	// approve = Share the tool to the global catalog.
	sub.HandleFunc("/api/promotions", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(ListPromotionRequests(a.db, true)) // pending only — the actionable queue
		case http.MethodPost:
			action := r.URL.Query().Get("action")
			id := strings.TrimSpace(r.URL.Query().Get("id"))
			if id == "" {
				http.Error(w, "missing id", http.StatusBadRequest)
				return
			}
			req, ok := GetPromotionRequest(a.db, id)
			if !ok {
				http.NotFound(w, r)
				return
			}
			switch action {
			case "approve":
				// Kind-specific side effect BEFORE marking approved, so a failure
				// leaves the request pending (re-approvable).
				switch req.Kind {
				case "tool":
					if err := SetPersistentTempToolShared(a.db, req.Owner, req.Name, true); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
				default:
					http.Error(w, "approving "+req.Kind+" promotions is not supported yet", http.StatusBadRequest)
					return
				}
				if err := SetPromotionRequestState(a.db, id, PromotionApprovedState, AuthCurrentUser(r)); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
			case "deny":
				if err := SetPromotionRequestState(a.db, id, PromotionDeniedState, AuthCurrentUser(r)); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
			default:
				http.Error(w, "action must be approve|deny", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// API: global-tool adoptions — who has pulled each shared tool into their
	// fleet, so the admin can see blast radius before revoking one and force-remove
	// a specific user's adoption. One row per (tool, adopter); a stale row is an
	// adoption whose tool has since left the shared catalog.
	sub.HandleFunc("/api/tool-adoptions", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			shared := map[string]bool{}
			for _, p := range LoadSharedPersistentTempTools(a.db) {
				shared[p.Tool.Name] = true
			}
			type row struct {
				ID    string `json:"id"` // tool/user — unique (RowKey)
				Tool  string `json:"tool"`
				User  string `json:"user"`
				Stale bool   `json:"stale"`
			}
			rows := []row{}
			for _, u := range AuthListUsers(a.db) {
				for name := range LoadAdoptedGlobalTools(a.db, u.Username) {
					rows = append(rows, row{ID: name + "/" + u.Username, Tool: name, User: u.Username, Stale: !shared[name]})
				}
			}
			sort.Slice(rows, func(i, j int) bool {
				if rows[i].Tool != rows[j].Tool {
					return rows[i].Tool < rows[j].Tool
				}
				return rows[i].User < rows[j].User
			})
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(rows)
		case http.MethodPost:
			user := strings.TrimSpace(r.URL.Query().Get("user"))
			name := strings.TrimSpace(r.URL.Query().Get("name"))
			if user == "" || name == "" {
				http.Error(w, "user and name required", http.StatusBadRequest)
				return
			}
			if r.URL.Query().Get("action") != "unadopt" {
				http.Error(w, "action must be unadopt", http.StatusBadRequest)
				return
			}
			if err := SetGlobalToolAdopted(a.db, user, name, false); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
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
			case "groups":
				if r.Method == http.MethodPut {
					a.handleUpdateUserGroups(w, r, username)
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
			case "reset-password":
				if r.Method == http.MethodPost {
					a.handleResetPassword(w, r, username)
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
		case http.MethodGet:
			// Return the single-user summary so the framework's
			// ChipPicker (apps) and any other per-user component can
			// fetch the current state without scanning the full list.
			user, ok := AuthGetUser(a.db, username)
			if !ok {
				http.Error(w, "user not found", http.StatusNotFound)
				return
			}
			apps := user.Apps
			if apps == nil {
				apps = []string{}
			}
			groups := user.Groups
			if groups == nil {
				groups = []string{}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"username": user.Username,
				"admin":    user.Admin,
				"pending":  user.Pending,
				"apps":     apps,
				"groups":   groups,
			})
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

	// System Dependencies — probes optional host binaries (ffmpeg, pdftotext,
	// bwrap, …) so the admin panel can show what's installed and what each gates.
	sub.HandleFunc("/api/dependencies", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CheckDependencies())
	})

	// Vector Index snapshot — backs the admin DisplayPanel (page.go).
	sub.HandleFunc("/api/vector-stats", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		a.handleVectorStats(w, r)
	})

	// Per-kind breakdown (research / debate / lcm / collections / …) with doc +
	// chunk counts — the legible view of what's in the index, vs the opaque
	// per-source id dump.
	sub.HandleFunc("/api/vector-stats/by-kind", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		a.handleVectorStatsByKind(w, r)
	})

	// API: settings (signup toggle, etc.).
	sub.HandleFunc("/api/settings", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			a.handleGetSettings(w, r)
		case http.MethodPut, http.MethodPost:
			// FormPanel auto-save defaults to POST; accept both so an
			// in-place edit form saves without a per-field Method override.
			a.handleUpdateSettings(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	// API: revert retrieval/limit tunables to their code defaults.
	sub.HandleFunc("/api/settings/reset-tunables", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		a.handleResetTunables(w, r)
	})

	// API: cost rates — dollar pricing for per-run LLM + search usage
	// telemetry. Shared between --setup (writes to the same kvlite
	// bucket via core.SaveCostRatesToDB) and this admin page; either
	// path writes the same record and updates live via SetCostRates.
	sub.HandleFunc("/api/cost-rates", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			a.handleGetCostRates(w, r)
		case http.MethodPut:
			a.handleUpdateCostRates(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Per-day cost history for the admin chart. Aggregates across every
	// spend-bearing record type whose package registered a scanner at
	// init time via core.RegisterCostRecordScanner. Apps plug in their
	// own record sources — admin stays generic.
	sub.HandleFunc("/api/cost-history", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		a.handleCostHistory(w, r)
	})
	sub.HandleFunc("/api/cost-by-source", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		a.handleCostBySource(w, r)
	})

	// Embeddings config — GET returns current settings, POST persists +
	// reinstalls the live config so the next ingestion/search call picks
	// up the new endpoint/model without a restart.
	sub.HandleFunc("/api/embeddings", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method == http.MethodPost {
			var req EmbeddingConfig
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if a.db != nil {
				a.db.Set(EmbeddingTable, "current", req)
			}
			SetEmbeddingConfig(req)
			Log("[admin] user %q updated embeddings config (enabled=%v endpoint=%q model=%q)",
				AuthCurrentUser(r), req.Enabled, req.Endpoint, req.Model)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var cfg EmbeddingConfig
		if a.db != nil {
			a.db.Get(EmbeddingTable, "current", &cfg)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cfg)
	})

	// Embeddings connectivity test — POSTs current form values (NOT the
	// saved DB record), runs a one-shot Embed() against them, returns
	// {ok, message|error} for inline display in the admin FormPanel.
	sub.HandleFunc("/api/embeddings/test", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req EmbeddingConfig
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeTestResult(w, false, "", "invalid request body")
			return
		}
		if !req.Enabled {
			writeTestResult(w, false, "", "embeddings are disabled — flip the toggle on first")
			return
		}
		if req.Endpoint == "" {
			writeTestResult(w, false, "", "endpoint is required")
			return
		}
		// Temporarily swap in the form's working config for this one call
		// without persisting. Restore on exit so a failed test doesn't
		// poison live state.
		prev := GetEmbeddingConfig()
		SetEmbeddingConfig(req)
		defer SetEmbeddingConfig(prev)
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		vec, err := Embed(ctx, "hello from gohort admin connectivity test")
		if err != nil {
			writeTestResult(w, false, "", err.Error())
			return
		}
		modelLabel := req.Model
		if modelLabel == "" {
			modelLabel = "server default"
		}
		writeTestResult(w, true, fmt.Sprintf("OK — %d-dim embedding from %s", len(vec), modelLabel), "")
	})

	// /api/embeddings/models — probe the saved embedding endpoint for
	// available models. Returns chip-shaped JSON (id/name/value) so
	// FormField.ChipsSource can render a click-to-fill row above the
	// model input. Tries OpenAI-style /models first (works for OpenAI,
	// vLLM, llama.cpp, hf-tei), falls back to Ollama's /api/tags by
	// transforming the endpoint base. Empty array on either reach
	// failure — the field stays manually editable, no UI error.
	sub.HandleFunc("/api/embeddings/models", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		var cfg EmbeddingConfig
		if a.db != nil {
			a.db.Get(EmbeddingTable, "current", &cfg)
		}
		w.Header().Set("Content-Type", "application/json")
		if cfg.Endpoint == "" {
			_, _ = w.Write([]byte("[]"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		// Try OpenAI-compat /models first.
		base := strings.TrimRight(cfg.Endpoint, "/")
		names := probeEmbeddingModels(ctx, base+"/models", cfg.APIKey, "openai")
		if len(names) == 0 {
			// Fall back to Ollama /tags. Strip /v1 if present so we
			// don't end up with /v1/tags (Ollama only serves
			// /api/tags, never under /v1). The base already includes
			// /api for canonical Ollama configs.
			tagsBase := strings.TrimSuffix(base, "/v1")
			names = probeEmbeddingModels(ctx, tagsBase+"/tags", cfg.APIKey, "ollama")
		}
		out := make([]map[string]string, 0, len(names))
		for _, n := range names {
			out = append(out, map[string]string{"id": n, "name": n, "value": n})
		}
		_ = json.NewEncoder(w).Encode(out)
	})

	// Audio transcription (STT) — GET/POST. POST persists + reinstalls
	// the live TranscribeConfig so the next Transcribe() call picks up
	// the new endpoint/model/key without a restart.
	sub.HandleFunc("/api/transcribe", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method == http.MethodPost {
			var req TranscribeConfig
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if a.db != nil {
				a.db.Set(TranscribeTable, "current", req)
			}
			SetTranscribeConfig(req)
			Log("[admin] user %q updated transcribe config (enabled=%v endpoint=%q model=%q)",
				AuthCurrentUser(r), req.Enabled, req.Endpoint, req.Model)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var cfg TranscribeConfig
		if a.db != nil {
			a.db.Get(TranscribeTable, "current", &cfg)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cfg)
	})

	// Image generation — provider + api_key live in per-key rows under
	// ImageTable. Shape mirrors the legacy --setup wiring so existing
	// installs read/write the same kvlite keys.
	sub.HandleFunc("/api/image-gen", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method == http.MethodPost {
			var req struct {
				Provider string `json:"provider"`
				APIKey   string `json:"api_key"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if a.db != nil {
				a.db.Set(ImageTable, "provider", req.Provider)
				a.db.Set(ImageTable, "api_key", req.APIKey)
			}
			Log("[admin] user %q updated image-gen config (provider=%q)",
				AuthCurrentUser(r), req.Provider)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var provider, key string
		if a.db != nil {
			a.db.Get(ImageTable, "provider", &provider)
			a.db.Get(ImageTable, "api_key", &key)
		}
		if provider == "" {
			provider = "gemini"
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"provider": provider, "api_key": key})
	})

	// --- connector templates (the generic Add/Configure renderer) -----------
	//
	// One data-driven surface for every backend: a template declares its fields +
	// value↔spec mapping + optional Detect, and these endpoints render/save it. No
	// per-backend admin code (that's the point).

	// List templates for the Add menu (optional ?category=).
	sub.HandleFunc("/api/connector-templates", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		cat := strings.TrimSpace(r.URL.Query().Get("category"))
		type row struct {
			Name        string `json:"name"`
			Label       string `json:"label"`
			Category    string `json:"category"`
			Description string `json:"description"`
		}
		var out []row
		for _, t := range ConnectorTemplates() {
			if cat != "" && t.Category != cat {
				continue
			}
			out = append(out, row{t.Name, t.Label, t.Category, t.Description})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})

	// Field schema for one template (Add mode — no values).
	sub.HandleFunc("/api/connector-template", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		t, ok := GetConnectorTemplate(r.URL.Query().Get("name"))
		if !ok {
			http.Error(w, "no such template", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(templateSchemaMap(t, nil, "", true))
	})

	// Auto-detect (Detect hook) — powers the panel's Detect button.
	sub.HandleFunc("/api/connector-detect", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		t, ok := GetConnectorTemplate(r.URL.Query().Get("template"))
		if !ok || !t.HasDetect() {
			http.Error(w, "template has no detect", http.StatusBadRequest)
			return
		}
		var req struct {
			Values map[string]any `json:"values"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		vals, warns, err := t.Detect(req.Values)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"values": vals, "warnings": warns})
	})

	// Configure an existing connector (GET: schema + current values) / Save
	// (POST: create or edit). Save MERGES onto the existing spec (forward-compat).
	sub.HandleFunc("/api/connector-config", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			c, ok := GetConnector(RootDB, strings.TrimSpace(r.URL.Query().Get("connector")))
			if !ok {
				http.Error(w, "no such connector", http.StatusNotFound)
				return
			}
			t, ok := TemplateForConnector(c)
			if !ok {
				http.Error(w, "this connector has no config template", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(templateSchemaMap(t, t.ReadValues(c.Spec), c.Name, false))
		case http.MethodPost:
			var req struct {
				Template   string         `json:"template"`
				Connector  string         `json:"connector"`
				Name       string         `json:"name"`
				Values     map[string]any `json:"values"`
				SetDefault bool           `json:"set_default"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			t, ok := GetConnectorTemplate(req.Template)
			if !ok {
				http.Error(w, "no such template", http.StatusBadRequest)
				return
			}
			raw, _, berr := t.BuildSpec(req.Values)
			if berr != nil {
				http.Error(w, berr.Error(), http.StatusBadRequest)
				return
			}
			if req.Connector != "" {
				// Edit: merge onto the existing spec so unknown fields survive.
				c, ok := GetConnector(RootDB, req.Connector)
				if !ok {
					http.Error(w, "no such connector", http.StatusNotFound)
					return
				}
				c.Spec = MergeSpec(c.Spec, raw)
				if err := SaveConnector(RootDB, c); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				Log("[admin] user %q configured backend %q via template %q", AuthCurrentUser(r), req.Connector, req.Template)
			} else {
				// Create + approve (admin is the approver).
				name := strings.TrimSpace(req.Name)
				if name == "" {
					http.Error(w, "name is required", http.StatusBadRequest)
					return
				}
				if _, exists := GetConnector(RootDB, name); exists {
					http.Error(w, "a connector named "+name+" already exists", http.StatusBadRequest)
					return
				}
				c := Connector{Name: name, Kind: t.Kind, Template: t.Name, Owner: AuthCurrentUser(r), Desc: t.Label + " backend", Spec: raw}
				if err := SaveConnector(RootDB, c); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				if err := ApproveConnector(RootDB, name); err != nil {
					http.Error(w, "created but approve failed: "+err.Error(), http.StatusBadRequest)
					return
				}
				if req.SetDefault && a.db != nil && t.Category == "Image generation" {
					a.db.Set(ImageTable, "provider", name)
				}
				Log("[admin] user %q added backend %q via template %q", AuthCurrentUser(r), name, req.Template)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// --- extensions (the umbrella catalog spanning every template target) ----
	//
	// One browse surface over AllTemplates(): connector templates and tool
	// templates side by side, each carrying its Target so the catalog facets on
	// it. Add still routes by target — the schema endpoint stamps `target`, and
	// the generic renderer POSTs to api/connector-config or api/tool-config
	// accordingly. Discovery is unified here; governance stays split.

	sub.HandleFunc("/api/extensions", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		cat := strings.TrimSpace(r.URL.Query().Get("category"))
		type row struct {
			Name        string `json:"name"`
			Label       string `json:"label"`
			Target      string `json:"target"`
			Category    string `json:"category"`
			Description string `json:"description"`
		}
		var out []row
		for _, t := range AllTemplates() {
			if cat != "" && t.Category != cat {
				continue
			}
			out = append(out, row{t.Name, t.Label, t.Target, t.Category, t.Description})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})

	// Field schema for one extension template (Add mode). Takes ?target= &name=
	// so a single client action can open the Add form for either target.
	sub.HandleFunc("/api/extension-template", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		target := strings.TrimSpace(r.URL.Query().Get("target"))
		if target == "" {
			target = TargetConnector
		}
		t, ok := GetTemplate(target, r.URL.Query().Get("name"))
		if !ok {
			http.Error(w, "no such template", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(templateSchemaMap(t, nil, "", true))
	})

	// All templates (connector + tool) for the Templates catalog browse. Each row's
	// id is "<target>/<name>" so the two namespaces don't collide.
	sub.HandleFunc("/api/all-templates", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		type row struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Label       string `json:"label"`
			Category    string `json:"category"`
			Target      string `json:"target"`
			Description string `json:"description"`
		}
		var out []row
		for _, target := range []string{TargetConnector, TargetTool} {
			for _, t := range Templates(target) {
				out = append(out, row{target + "/" + t.Name, t.Name, t.Label, t.Category, target, t.Description})
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})

	// --- tool templates (same generic renderer, tool target) ----------------
	//
	// The tool artifact is a TempTool that routes through the tool governance
	// (AdminPersistTempTool), NOT SaveConnector — a template eases authoring, it
	// grants no new power.

	sub.HandleFunc("/api/tool-templates", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		type row struct {
			Name        string `json:"name"`
			Label       string `json:"label"`
			Category    string `json:"category"`
			Description string `json:"description"`
		}
		var out []row
		for _, t := range Templates(TargetTool) {
			out = append(out, row{t.Name, t.Label, t.Category, t.Description})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})

	sub.HandleFunc("/api/tool-template", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		t, ok := GetTemplate(TargetTool, r.URL.Query().Get("name"))
		if !ok {
			http.Error(w, "no such tool template", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(templateSchemaMap(t, nil, "", true))
	})

	sub.HandleFunc("/api/tool-detect", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		t, ok := GetTemplate(TargetTool, r.URL.Query().Get("template"))
		if !ok || !t.HasDetect() {
			http.Error(w, "template has no detect", http.StatusBadRequest)
			return
		}
		var req struct {
			Values map[string]any `json:"values"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		vals, warns, err := t.Detect(req.Values)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"values": vals, "warnings": warns})
	})

	// Tools from a template: POST creates (or, in edit mode, reconfigures) a
	// TempTool in an owner's pool; GET returns the prefilled schema to re-open an
	// installed tool in the generic form — the tool half of the connector
	// "Configure" round-trip, resolved through provenance (TemplateForTool).
	sub.HandleFunc("/api/tool-config", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			toolName := strings.TrimSpace(r.URL.Query().Get("tool"))
			owner := strings.TrimSpace(r.URL.Query().Get("owner"))
			if owner == "" {
				owner = AuthCurrentUser(r)
			}
			var tt TempTool
			found := false
			for _, p := range LoadPersistentTempTools(a.db, owner) {
				if p.Tool.Name == toolName {
					tt, found = p.Tool, true
					break
				}
			}
			if !found {
				http.Error(w, "no such tool", http.StatusNotFound)
				return
			}
			t, ok := TemplateForTool(tt)
			if !ok {
				http.Error(w, "this tool has no config template", http.StatusBadRequest)
				return
			}
			raw, _ := json.Marshal(tt)
			m := templateSchemaMap(t, t.ReadValues(raw), tt.Name, false)
			m["owner"] = owner // echoed back on save so the edit targets the right pool
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(m)
		case http.MethodPost:
			var req struct {
				Template  string         `json:"template"`
				Name      string         `json:"name"`
				Connector string         `json:"connector"` // edit: the existing tool id, carried in the generic identity slot
				Owner     string         `json:"owner"`
				Values    map[string]any `json:"values"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			t, ok := GetTemplate(TargetTool, req.Template)
			if !ok {
				http.Error(w, "no such tool template", http.StatusBadRequest)
				return
			}
			// Create carries a Name; the generic form's edit path leaves Name blank
			// and carries the existing tool id in the identity slot instead.
			editing := false
			name := strings.TrimSpace(req.Name)
			if name == "" {
				name = strings.TrimSpace(req.Connector)
				editing = name != ""
			}
			if name == "" {
				http.Error(w, "name is required", http.StatusBadRequest)
				return
			}
			raw, _, berr := t.BuildSpec(req.Values)
			if berr != nil {
				http.Error(w, berr.Error(), http.StatusBadRequest)
				return
			}
			var tt TempTool
			if err := json.Unmarshal(raw, &tt); err != nil {
				http.Error(w, "template produced an invalid tool", http.StatusBadRequest)
				return
			}
			tt.Name = name
			tt.Template = req.Template // provenance
			owner := strings.TrimSpace(req.Owner)
			if owner == "" {
				owner = AuthCurrentUser(r)
			}
			// Edit preserves the tool's share + adopt-ACL state; create mints a
			// fresh pool entry.
			persist := AdminPersistTempTool
			verb := "added"
			if editing {
				persist = AdminReconfigureTempTool
				verb = "reconfigured"
			}
			if err := persist(a.db, owner, tt); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			Log("[admin] user %q %s tool %q for %q via template %q", AuthCurrentUser(r), verb, name, owner, req.Template)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// STT connectivity test — GET {endpoint}/models with auth header so
	// the operator can confirm reachability + credentials without needing
	// a sample audio file. Both whisper.cpp and the real OpenAI API
	// expose /models on the OpenAI-compatible base; a 2xx means the
	// endpoint is reachable and (if a key was provided) accepts it.
	sub.HandleFunc("/api/transcribe/test", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req TranscribeConfig
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeTestResult(w, false, "", "invalid request body")
			return
		}
		if !req.Enabled {
			writeTestResult(w, false, "", "transcription is disabled — flip the toggle on first")
			return
		}
		if req.Endpoint == "" {
			writeTestResult(w, false, "", "endpoint is required")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		// probeURL: GET with optional bearer header. Returns the response status
		// or any transport error. Closing the body inline so callers don't have to.
		probeURL := func(url string) (int, error) {
			httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
			if err != nil {
				return 0, err
			}
			if req.APIKey != "" {
				httpReq.Header.Set("Authorization", "Bearer "+req.APIKey)
			}
			resp, err := (&http.Client{}).Do(httpReq)
			if err != nil {
				return 0, err
			}
			defer resp.Body.Close()
			return resp.StatusCode, nil
		}
		// Try /models first (OpenAI-compatible servers expose this and a
		// 200 also validates the bearer key). Fall back to the endpoint
		// root for servers like whisper.cpp that only expose the
		// transcription path and serve an HTML index at /.
		base := strings.TrimRight(req.Endpoint, "/")
		modelsURL := base + "/models"
		status, err := probeURL(modelsURL)
		if err != nil {
			writeTestResult(w, false, "", "reach failed: "+err.Error())
			return
		}
		switch {
		case status >= 200 && status < 300:
			writeTestResult(w, true, fmt.Sprintf("Endpoint reachable + /models OK (HTTP %d)", status), "")
			return
		case status == 401 || status == 403:
			writeTestResult(w, false, "", fmt.Sprintf("HTTP %d — endpoint reached but rejected the API key", status))
			return
		}
		// /models 404/405 → fall back to a plain GET on the endpoint root.
		// Strip any trailing /v1 (or /api) so we hit the actual host root.
		rootBase := base
		for _, suffix := range []string{"/v1", "/api"} {
			if strings.HasSuffix(rootBase, suffix) {
				rootBase = rootBase[:len(rootBase)-len(suffix)]
				break
			}
		}
		rootStatus, err := probeURL(rootBase + "/")
		if err != nil {
			writeTestResult(w, false, "", fmt.Sprintf("HTTP %d at %s, and root probe failed: %s", status, modelsURL, err.Error()))
			return
		}
		if rootStatus >= 200 && rootStatus < 500 {
			writeTestResult(w, true, fmt.Sprintf("Endpoint reachable (HTTP %d at root; %d at /models — server doesn't expose /models, fine for whisper.cpp)", rootStatus, status), "")
			return
		}
		writeTestResult(w, false, "", fmt.Sprintf("HTTP %d at root, HTTP %d at /models", rootStatus, status))
	})

	// Image gen connectivity test — same shape as STT but per-provider
	// (each has its own models URL convention). Validates the API key
	// is recognized; doesn't actually generate an image (which would
	// cost money on every test click).
	sub.HandleFunc("/api/image-gen/test", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Provider string `json:"provider"`
			APIKey   string `json:"api_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeTestResult(w, false, "", "invalid request body")
			return
		}
		if req.Provider == "" || req.Provider == "none" {
			writeTestResult(w, false, "", "pick a provider first")
			return
		}
		// Connector-backed provider (rest_image: ComfyUI / A1111 / custom): it
		// carries its own SecureAPI credential, so there's no API key to probe
		// here. Report that it's live rather than falling into the built-in
		// gemini/openai key test (which would reject the name as "unknown").
		if ImageBackendRegistered(req.Provider) {
			writeTestResult(w, true, "Connector backend “"+req.Provider+"” is approved and active. It uses its own credential — generate an image to verify it end to end.", "")
			return
		}
		// Fall back to the matching LLM provider's key when blank (same
		// rule the GenerateImage runtime uses).
		key := req.APIKey
		if key == "" && a.db != nil {
			switch req.Provider {
			case "gemini":
				a.db.Get(LLMTable, "api_key", &key) // reuse if Gemini is also worker provider
			case "openai":
				a.db.Get(LLMTable, "api_key", &key)
			}
		}
		if key == "" {
			writeTestResult(w, false, "", "no API key — set one here, or set the matching LLM provider's key")
			return
		}
		var url string
		var authHeader, authPrefix string
		switch req.Provider {
		case "openai":
			url = "https://api.openai.com/v1/models"
			authHeader, authPrefix = "Authorization", "Bearer "
		case "gemini":
			// Gemini takes the key as ?key= rather than a header.
			url = "https://generativelanguage.googleapis.com/v1beta/models?key=" + key
		default:
			writeTestResult(w, false, "", "unknown provider: "+req.Provider)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			writeTestResult(w, false, "", err.Error())
			return
		}
		if authHeader != "" {
			httpReq.Header.Set(authHeader, authPrefix+key)
		}
		resp, err := (&http.Client{}).Do(httpReq)
		if err != nil {
			writeTestResult(w, false, "", "reach failed: "+err.Error())
			return
		}
		defer resp.Body.Close()
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			writeTestResult(w, true, fmt.Sprintf("%s reachable + key accepted (HTTP %d)", req.Provider, resp.StatusCode), "")
		case resp.StatusCode == 401 || resp.StatusCode == 403:
			writeTestResult(w, false, "", fmt.Sprintf("HTTP %d — %s rejected the API key", resp.StatusCode, req.Provider))
		default:
			writeTestResult(w, false, "", fmt.Sprintf("HTTP %d from %s", resp.StatusCode, req.Provider))
		}
	})

	// Web search — per-key rows under SearchTable (provider, api_key,
	// endpoint). Same shape as --setup.
	sub.HandleFunc("/api/web-search", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method == http.MethodPost {
			var req struct {
				Provider string `json:"provider"`
				APIKey   string `json:"api_key"`
				Endpoint string `json:"endpoint"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if a.db != nil {
				a.db.Set(SearchTable, "provider", req.Provider)
				a.db.Set(SearchTable, "api_key", req.APIKey)
				a.db.Set(SearchTable, "endpoint", req.Endpoint)
			}
			Log("[admin] user %q updated web-search config (provider=%q)",
				AuthCurrentUser(r), req.Provider)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var provider, key, endpoint string
		if a.db != nil {
			a.db.Get(SearchTable, "provider", &provider)
			a.db.Get(SearchTable, "api_key", &key)
			a.db.Get(SearchTable, "endpoint", &endpoint)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"provider": provider, "api_key": key, "endpoint": endpoint,
		})
	})

	// Web search connectivity test — temporarily swap in the form's
	// working WebSearchConfig via LoadWebSearchConfigFunc, run a one-
	// shot WebSearch("gohort connectivity test") call, restore the
	// loader on exit. Empty result counts as failure (most providers
	// return SOMETHING for any term; an empty result implies a config
	// problem rather than a genuinely empty corpus).
	sub.HandleFunc("/api/web-search/test", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req WebSearchConfig
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeTestResult(w, false, "", "invalid request body")
			return
		}
		if req.Provider == "" {
			writeTestResult(w, false, "", "provider is required")
			return
		}
		orig := LoadWebSearchConfigFunc
		LoadWebSearchConfigFunc = func() WebSearchConfig { return req }
		defer func() { LoadWebSearchConfigFunc = orig }()
		out := WebSearch("gohort connectivity test")
		if strings.TrimSpace(out) == "" {
			writeTestResult(w, false, "", "no results returned — check provider/key/endpoint")
			return
		}
		// Trim the result to a short preview so the inline UI doesn't
		// overflow with a wall of links.
		preview := strings.TrimSpace(out)
		if len(preview) > 80 {
			preview = preview[:80] + "…"
		}
		writeTestResult(w, true, fmt.Sprintf("OK via %s — %d chars returned", req.Provider, len(out)), "")
	})

	// Mail / SMTP — per-key rows under MailTable. Password is masked in
	// GET via the placeholder convention (FormPanel password field with
	// "(configured)" placeholder) — but for simplicity here we return
	// the stored password as-is; the admin field is type:password so it
	// renders masked on screen.
	sub.HandleFunc("/api/mail", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method == http.MethodPost {
			var req MailConfig
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if a.db != nil {
				a.db.Set(MailTable, "server", req.Server)
				a.db.Set(MailTable, "from", req.From)
				a.db.Set(MailTable, "recipient", req.Recipient)
				a.db.Set(MailTable, "username", req.Username)
				a.db.Set(MailTable, "password", req.Password)
			}
			Log("[admin] user %q updated mail config (server=%q from=%q)",
				AuthCurrentUser(r), req.Server, req.From)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var cfg MailConfig
		if a.db != nil {
			a.db.Get(MailTable, "server", &cfg.Server)
			a.db.Get(MailTable, "from", &cfg.From)
			a.db.Get(MailTable, "username", &cfg.Username)
			a.db.Get(MailTable, "password", &cfg.Password)
			a.db.Get(MailTable, "recipient", &cfg.Recipient)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cfg)
	})

	// Mail connectivity test — sends a real email to the recipient
	// using the form's current (possibly unsaved) MailConfig. Mirrors
	// the "Send Test Email" flow in --setup.
	sub.HandleFunc("/api/mail/test", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req MailConfig
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeTestResult(w, false, "", "invalid request body")
			return
		}
		to := req.Recipient
		if to == "" {
			writeTestResult(w, false, "", "set a Default Recipient first; test mail needs an address")
			return
		}
		orig := LoadMailConfigFunc
		LoadMailConfigFunc = func() MailConfig { return req }
		defer func() { LoadMailConfigFunc = orig }()
		if err := SendNotification(to, "Gohort Admin Test Email",
			"This is a test from the gohort admin UI.\n\nIf you received this, mail is configured correctly.\n"); err != nil {
			writeTestResult(w, false, "", err.Error())
			return
		}
		writeTestResult(w, true, fmt.Sprintf("Test email sent to %s", to), "")
	})

	// Network timeouts — per-key rows under NetworkTable.
	sub.HandleFunc("/api/network", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method == http.MethodPost {
			var req struct {
				ConnectTimeoutSeconds int `json:"connect_timeout_seconds"`
				RequestTimeoutSeconds int `json:"request_timeout_seconds"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if a.db != nil {
				if req.ConnectTimeoutSeconds > 0 {
					a.db.Set(NetworkTable, "connect_timeout_seconds", req.ConnectTimeoutSeconds)
				}
				if req.RequestTimeoutSeconds > 0 {
					a.db.Set(NetworkTable, "request_timeout_seconds", req.RequestTimeoutSeconds)
				}
			}
			Log("[admin] user %q updated network timeouts (connect=%ds request=%ds)",
				AuthCurrentUser(r), req.ConnectTimeoutSeconds, req.RequestTimeoutSeconds)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var connectSec, requestSec int
		if a.db != nil {
			a.db.Get(NetworkTable, "connect_timeout_seconds", &connectSec)
			a.db.Get(NetworkTable, "request_timeout_seconds", &requestSec)
		}
		if connectSec <= 0 {
			connectSec = 10
		}
		if requestSec <= 0 {
			requestSec = 15
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int{
			"connect_timeout_seconds": connectSec,
			"request_timeout_seconds": requestSec,
		})
	})

	// Agent-loop tuning — history-budget cap (and future LLM-retry
	// knobs). Mirrors /api/network's GET-current/POST-new shape so the
	// admin FormPanel can save without app-specific JS.
	sub.HandleFunc("/api/agent-loop-tuning", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method == http.MethodPost {
			var req struct {
				HistoryBudgetPercent int `json:"history_budget_percent"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if err := SaveAgentLoopTuningToDB(a.db, AgentLoopTuning{
				HistoryBudgetPercent: req.HistoryBudgetPercent,
			}); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			Log("[admin] user %q updated agent-loop tuning (history_budget_percent=%d)",
				AuthCurrentUser(r), GetAgentLoopTuning().HistoryBudgetPercent)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		cur := GetAgentLoopTuning()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int{
			"history_budget_percent": cur.HistoryBudgetPercent,
		})
	})

	// List registered maintenance functions (GET) or run one by key (POST ?key=<key>).
	sub.HandleFunc("/api/maintenance", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(ListMaintenanceFuncs())
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		key := r.URL.Query().Get("key")
		if key == "" {
			http.Error(w, "missing key", http.StatusBadRequest)
			return
		}
		count := RunMaintenanceFunc(r.Context(), key)
		if count < 0 {
			http.Error(w, "unknown maintenance function", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int{"fixed": count})
	})

	// List every recorded migration marker across all apps + owners.
	// Read-only; markers are written by MigrationRunner.Once when each
	// migration fires. Operators clear markers manually (delete the row
	// from the DB) to force a re-run after fixing a panic.
	sub.HandleFunc("/api/migrations", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		markers := ListMigrationMarkers()
		rows := make([]map[string]any, 0, len(markers))
		for _, m := range markers {
			owner := m.Owner
			if owner == "" {
				owner = "(global)"
			}
			var ranAt any
			if !m.RanAt.IsZero() {
				ranAt = m.RanAt
			}
			rows = append(rows, map[string]any{
				"key":     m.Key(),
				"app":     m.App,
				"name":    m.Name,
				"owner":   owner,
				"ran_at":  ranAt,
				"changed": m.Changed,
				"error":   m.Error,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rows)
	})

	// Scheduled tasks: list pending tasks (GET) or delete by ID (DELETE ?id=xxx).
	sub.HandleFunc("/api/scheduled-tasks", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			// Enrich each row with a human label (monitor name + agent, etc.)
			// via the task-describer registry, so the table can show WHAT a task
			// is rather than a bare kind + uuid. Kinds without a describer get an
			// empty detail (the id + kind still render).
			type schedTaskRow struct {
				ScheduledTask
				Detail string `json:"detail"`
			}
			tasks := ListScheduledTasks("")
			rows := make([]schedTaskRow, 0, len(tasks))
			for _, t := range tasks {
				rows = append(rows, schedTaskRow{ScheduledTask: t, Detail: DescribeTask(t)})
			}
			json.NewEncoder(w).Encode(rows)
			return
		}
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		UnscheduleTask(id)
		w.WriteHeader(http.StatusNoContent)
	})

	// Secure API credentials. GET lists metadata (no secrets). POST
	// upserts a credential (consumes the secret). DELETE removes one.
	// GET ?audit=NAME returns recent calls for that credential.
	sub.HandleFunc("/api/secure-api", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			if name := r.URL.Query().Get("audit"); name != "" {
				// owner selects the namespace: empty = the global credential of
				// that name (the admin page's usual subject), a username = that
				// user's own credential. Without it the audit view could only
				// ever read the global ring, which is the bare-name collision
				// this keying fixes — a per-user audit view passes owner here.
				owner := strings.TrimSpace(r.URL.Query().Get("owner"))
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(Secure().LoadAudit(owner, name))
				return
			}
			// Declaring-tools list for a credential — what a secured cred is
			// locked to, and what a scoped one dispatches through.
			if name := strings.TrimSpace(r.URL.Query().Get("tools")); name != "" {
				refs := []CredentialToolRef{}
				if CredentialToolsResolver != nil {
					if r := CredentialToolsResolver(name); r != nil {
						refs = r
					}
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(refs)
				return
			}
			// Tool-binding rows for a SECURED credential: what's approved, what's
			// bound (auto-resolved from its declaration), and what's been revoked
			// (a durable deny). Drives the Bindings expand + its Approve/Revoke
			// actions. Each row carries the credential name so a row action can
			// address both cred + tool.
			if name := strings.TrimSpace(r.URL.Query().Get("bindings")); name != "" {
				type bindingRow struct {
					Cred     string `json:"cred"`
					Tool     string `json:"tool"`
					Status   string `json:"status"`
					Approved bool   `json:"_approved,omitempty"`
					Revoked  bool   `json:"_revoked,omitempty"`
				}
				rows := []bindingRow{}
				if c, ok := Secure().Load(name); ok {
					for _, t := range c.ApprovedToolBindings {
						rows = append(rows, bindingRow{Cred: name, Tool: t, Status: "bound", Approved: true})
					}
					for _, t := range c.RevokedToolBindings {
						rows = append(rows, bindingRow{Cred: name, Tool: t, Status: "revoked", Revoked: true})
					}
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(rows)
				return
			}
			// Single-record fetch for the declarative edit form's Source.
			// Returns one credential as an object (secret never included —
			// the password field stays blank on edit, which keeps the
			// stored secret unchanged unless the admin types a new one).
			if name := strings.TrimSpace(r.URL.Query().Get("name")); name != "" {
				w.Header().Set("Content-Type", "application/json")
				for _, c := range Secure().ListWithPending() {
					if c.Name == name {
						json.NewEncoder(w).Encode(c)
						return
					}
				}
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			// List — enrich each SECURED credential with whether it's orphaned
			// (locked but no tool uses it → unreachable), for the dead-cred badge.
			type credRow struct {
				SecureCredential
				Orphaned bool `json:"orphaned,omitempty"`
				// AccessLevel is what the admin picks from the segmented pill:
				// "open" (generic call tool + auto-route + per-agent scope) or
				// "secured" (tool-locked; off auto-route + scope).
				AccessLevel string `json:"access_level"`
				// Namespace classifies the credential: "global" (Owner empty — this
				// admin surface manages it) vs "user" (owned by a user's namespace).
				Namespace string `json:"namespace"`
			}
			creds := Secure().ListWithPending()
			rows := make([]credRow, len(creds))
			for i, c := range creds {
				orphaned := false
				if c.Secured && CredentialToolsResolver != nil {
					orphaned = len(CredentialToolsResolver(c.Name)) == 0
				}
				level := "open"
				if c.Secured {
					level = "secured"
				}
				ns := "global"
				if c.Owner != "" {
					ns = "user"
				}
				rows[i] = credRow{SecureCredential: c, Orphaned: orphaned, AccessLevel: level, Namespace: ns}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(rows)
		case http.MethodPost:
			// Two POST shapes: the upsert body (full credential) and the
			// toggle action (?action=enable|disable&name=X). Distinguish
			// by query param.
			if action := r.URL.Query().Get("action"); action != "" {
				name := strings.TrimSpace(r.URL.Query().Get("name"))
				if name == "" {
					http.Error(w, "missing name", http.StatusBadRequest)
					return
				}
				switch action {
				case "enable":
					if err := Secure().SetDisabled(name, false); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
				case "disable":
					if err := Secure().SetDisabled(name, true); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
				case "secure":
					// Tool-lock: reachable only through the tools that already
					// declare it, off the auto-route + auto-catalog, and no NEW
					// tool may declare it. Access follows those tools' scope, so
					// the per-agent scope surface no longer applies.
					if err := Secure().SetSecured(name, true); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
				case "unsecure":
					// Release the tool-lock — the credential goes back to the
					// normal (Open) scoped/auto-routable model.
					if err := Secure().SetSecured(name, false); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
				case "access":
					// One idempotent "set lockdown level" from the segmented pill:
					// "open" (unlocked) or "secured" (tool-locked).
					var body struct {
						AccessLevel string `json:"access_level"`
					}
					_ = json.NewDecoder(r.Body).Decode(&body)
					var secure bool
					switch strings.TrimSpace(body.AccessLevel) {
					case "open":
						secure = false
					case "secured":
						secure = true
					default:
						http.Error(w, "access_level must be open|secured", http.StatusBadRequest)
						return
					}
					if err := Secure().SetSecured(name, secure); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
				case "approve_binding":
					// Admin approves a tool's request to bind this secured cred —
					// the tool can then dispatch through it (secret server-side), and
					// access follows the tool's own scope. Clears any revoke tombstone.
					tool := strings.TrimSpace(r.URL.Query().Get("tool"))
					if tool == "" {
						http.Error(w, "missing tool", http.StatusBadRequest)
						return
					}
					if err := Secure().ApproveToolBinding(name, tool); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
				case "revoke_binding":
					// Admin revokes a binding — tombstoned, so dispatch refuses it
					// (the tool stays, but can't reach the cred until re-approved).
					tool := strings.TrimSpace(r.URL.Query().Get("tool"))
					if tool == "" {
						http.Error(w, "missing tool", http.StatusBadRequest)
						return
					}
					if err := Secure().RevokeToolBinding(name, tool); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
				case "test":
					// Mint-and-discard an OAuth token to verify the config +
					// secret before relying on the credential. Returns the
					// outcome (incl. the provider's error on failure) so the
					// admin / LLM-assisted setup can iterate.
					msg, terr := Secure().TestMintToken(name)
					w.Header().Set("Content-Type", "application/json")
					if terr != nil {
						json.NewEncoder(w).Encode(map[string]any{"ok": false, "message": terr.Error()})
						return
					}
					json.NewEncoder(w).Encode(map[string]any{"ok": true, "message": msg})
					return
				default:
					http.Error(w, "action must be enable|disable|secure|unsecure|access|approve_binding|revoke_binding|test", http.StatusBadRequest)
					return
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			var req struct {
				Name              string   `json:"name"`
				Type              string   `json:"type"`
				AllowedURLPattern string   `json:"allowed_url_pattern"`
				BaseURL           string   `json:"base_url"`
				AllowedEndpoints  []string `json:"allowed_endpoints"`
				AllowedUsers      []string `json:"allowed_users"`
				ParamName         string   `json:"param_name"`
				Description       string   `json:"description"`
				CredScope         string   `json:"cred_scope"`
				RequiresConfirm   bool     `json:"requires_confirm"`
				InsecureSkipTLS   bool     `json:"insecure_skip_tls"`
				Secret            string   `json:"secret"`
				Password          string   `json:"password"` // password-grant resource-owner password (the 2nd secret)
				AllowedMethods    []string `json:"allowed_methods"`
				DeniedURLPatterns []string `json:"denied_url_patterns"`
				MaxCallsPerDay    int      `json:"max_calls_per_day"`
				CostPerCall       float64  `json:"cost_per_call"`
				// OAuth2 (type == "oauth2").
				Grant        string `json:"grant"`
				TokenURL     string `json:"token_url"`
				AuthorizeURL string `json:"authorize_url"`
				ClientID     string `json:"client_id"`
				Username     string `json:"username"` // password grant: resource-owner username
				Scope        string `json:"scope"`
				JWTIssuer    string `json:"jwt_issuer"`
				JWTSubject   string `json:"jwt_subject"`
				JWTAudience  string `json:"jwt_audience"`
				JWTKeyID     string `json:"jwt_key_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
				return
			}
			c := SecureCredential{
				Name:              strings.TrimSpace(req.Name),
				Type:              strings.TrimSpace(req.Type),
				AllowedURLPattern: strings.TrimSpace(req.AllowedURLPattern),
				BaseURL:           strings.TrimSpace(req.BaseURL),
				AllowedEndpoints:  req.AllowedEndpoints,
				AllowedUsers:      req.AllowedUsers,
				ParamName:         strings.TrimSpace(req.ParamName),
				Description:       strings.TrimSpace(req.Description),
				CredScope:         strings.TrimSpace(req.CredScope),
				RequiresConfirm:   req.RequiresConfirm,
				InsecureSkipTLS:   req.InsecureSkipTLS,
				AllowedMethods:    req.AllowedMethods,
				DeniedURLPatterns: req.DeniedURLPatterns,
				MaxCallsPerDay:    req.MaxCallsPerDay,
				CostPerCall:       req.CostPerCall,
				Grant:             strings.TrimSpace(req.Grant),
				TokenURL:          strings.TrimSpace(req.TokenURL),
				AuthorizeURL:      strings.TrimSpace(req.AuthorizeURL),
				ClientID:          strings.TrimSpace(req.ClientID),
				Username:          strings.TrimSpace(req.Username),
				Scope:             strings.TrimSpace(req.Scope),
				JWTIssuer:         strings.TrimSpace(req.JWTIssuer),
				JWTSubject:        strings.TrimSpace(req.JWTSubject),
				JWTAudience:       strings.TrimSpace(req.JWTAudience),
				JWTKeyID:          strings.TrimSpace(req.JWTKeyID),
			}
			// Preserve the OAuth config of an existing credential when the
			// caller (e.g. the admin just adding the secret to a Builder
			// draft) doesn't resend it. Lets the admin complete a draft by
			// pasting only the secret.
			if c.Type == SecureCredOAuth2 {
				if existing, ok := Secure().Load(c.Name); ok && existing.Type == SecureCredOAuth2 {
					if c.Grant == "" {
						c.Grant = existing.Grant
					}
					if c.TokenURL == "" {
						c.TokenURL = existing.TokenURL
					}
					if c.AuthorizeURL == "" {
						c.AuthorizeURL = existing.AuthorizeURL
					}
					if c.ClientID == "" {
						c.ClientID = existing.ClientID
					}
					if c.Username == "" {
						c.Username = existing.Username
					}
					if c.Scope == "" {
						c.Scope = existing.Scope
					}
					if c.JWTIssuer == "" {
						c.JWTIssuer = existing.JWTIssuer
					}
					if c.JWTSubject == "" {
						c.JWTSubject = existing.JWTSubject
					}
					if c.JWTAudience == "" {
						c.JWTAudience = existing.JWTAudience
					}
					if c.JWTKeyID == "" {
						c.JWTKeyID = existing.JWTKeyID
					}
				}
			}
			if err := saveCredWithPassword(c, req.Secret, req.Password); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			name := strings.TrimSpace(r.URL.Query().Get("name"))
			if name == "" {
				http.Error(w, "missing name", http.StatusBadRequest)
				return
			}
			if err := Secure().Delete(name); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Inline "Test token" for an oauth2 credential. The declarative form's
	// TestURL POSTs its current working state (the full record + secret);
	// we mint-and-discard a token from it and return {ok,message} / {ok,error}
	// so the operator can verify the config before relying on it. Works
	// pre-save (uses the typed secret) and on edit (falls back to the stored
	// secret when the password field is left blank).
	sub.HandleFunc("/api/secure-api/test", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			SecureCredential
			Secret string `json:"secret"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		msg, err := Secure().TestMintFromPosted(body.SecureCredential, body.Secret)
		if err != nil {
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "message": msg})
	})

	// Remote MCP servers — the SERVER-SIDE Model Context Protocol client.
	// GET lists configs (tokens excluded); GET ?name=X returns one for the
	// edit form; POST upserts (or ?action=enable|disable&name=X toggles);
	// DELETE removes. Any mutation calls MCP().Reload() so the change
	// (connect/disconnect, tool registration) takes effect live.
	sub.HandleFunc("/api/mcp-servers", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			if name := strings.TrimSpace(r.URL.Query().Get("name")); name != "" {
				if c, ok := MCP().Load(name); ok {
					json.NewEncoder(w).Encode(c) // token is not part of the struct
					return
				}
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			// List rows carry derived flags: is_oauth (so the Connect
			// button shows only for oauth servers) and connected (this
			// admin's own per-user connection status).
			user := AuthCurrentUser(r)
			type mcpRow struct {
				MCPServerConfig
				IsOAuth   bool `json:"is_oauth"`
				Connected bool `json:"connected"`
			}
			var rows []mcpRow
			for _, c := range MCP().List() {
				rows = append(rows, mcpRow{c, c.AuthMode == MCPAuthOAuth, MCP().Connected(user, c.Name)})
			}
			json.NewEncoder(w).Encode(rows)
		case http.MethodPost:
			if action := r.URL.Query().Get("action"); action != "" {
				name := strings.TrimSpace(r.URL.Query().Get("name"))
				if name == "" {
					http.Error(w, "missing name", http.StatusBadRequest)
					return
				}
				c, ok := MCP().Load(name)
				if !ok {
					http.Error(w, "not found", http.StatusNotFound)
					return
				}
				switch action {
				case "enable":
					c.Enabled = true
				case "disable":
					c.Enabled = false
				default:
					http.Error(w, "action must be enable|disable", http.StatusBadRequest)
					return
				}
				if err := MCP().Save(c, ""); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				MCP().Reload()
				w.WriteHeader(http.StatusNoContent)
				return
			}
			var body struct {
				MCPServerConfig
				Token             string `json:"token"`
				OAuthClientSecret string `json:"oauth_client_secret"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
				return
			}
			if err := MCP().Save(body.MCPServerConfig, body.Token); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			// Manual OAuth client secret (non-DCR fallback) — stored encrypted,
			// separately from the config; blank keeps any existing one.
			MCP().SetOAuthClientSecret(body.Name, body.OAuthClientSecret)
			MCP().Reload()
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			name := strings.TrimSpace(r.URL.Query().Get("name"))
			if name == "" {
				http.Error(w, "missing name", http.StatusBadRequest)
				return
			}
			if err := MCP().Delete(name); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Inbound MCP tool governance: which app-contributed MCP tools are exposed on
	// gohort's own /mcp/ endpoint to external clients. GET lists every registered
	// tool with its exposed state; POST?action=expose|hide&name= flips one. Tools
	// default OFF so adding an app tool never silently widens the surface.
	sub.HandleFunc("/api/mcp-tools", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(MCPAppToolStatuses())
		case http.MethodPost:
			name := strings.TrimSpace(r.URL.Query().Get("name"))
			if name == "" {
				http.Error(w, "missing name", http.StatusBadRequest)
				return
			}
			if _, ok := LookupMCPTool(name); !ok {
				http.Error(w, "no such MCP tool", http.StatusNotFound)
				return
			}
			// The On/Off toggle posts {exposed: bool}.
			var body struct {
				Exposed bool `json:"exposed"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
				return
			}
			SetMCPAppToolExposed(name, body.Exposed)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Inline "Test connection" for an MCP server. The declarative form
	// POSTs its current working state (config + token); we connect,
	// initialize, and tools/list, returning {ok,message}/{ok,error} so the
	// operator can verify reachability + auth before enabling. Never echoes
	// the token.
	// Per-user OAuth connect (hosted MCP servers): start 302-redirects the
	// browser to the authorization server; callback redeems the code. See
	// mcp_oauth.go.
	sub.HandleFunc("/api/mcp-servers/oauth/start", a.handleMCPOAuthStart)
	sub.HandleFunc("/api/mcp-servers/oauth/callback", a.handleMCPOAuthCallback)
	sub.HandleFunc("/api/mcp-servers/test", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			MCPServerConfig
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		msg, err := MCP().Test(body.MCPServerConfig, body.Token)
		if err != nil {
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "message": msg})
	})

	// Connectors — "bridge types" drafted by an authoring agent (the connector
	// tool) and awaiting admin approval, e.g. a calendar/CRM exposed through its
	// MCP server. GET lists; POST?action=approve|unapprove&name toggles; DELETE
	// removes + tears down. Approve MATERIALIZES the underlying capability (for
	// remote_mcp: an enabled MCP server), so its tools go live for agents. The
	// LLM never handles a secret — auth is a referenced credential or per-user
	// oauth; approval is the human gate.
	sub.HandleFunc("/api/connectors", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			type connRow struct {
				Name         string `json:"name"`
				Kind         string `json:"kind"`
				Summary      string `json:"summary"`
				Owner        string `json:"owner"`
				Approved     bool   `json:"approved"`
				LastError    string `json:"last_error"`
				Template     string `json:"template,omitempty"` // provenance (which template authored it)
				IsImage      bool   `json:"is_image"`           // rest_image → image-section toolbar pick
				Configurable bool   `json:"configurable"`       // resolves to a template → gets "Configure" (incl. imports)
			}
			var rows []connRow
			for _, c := range ListConnectors(RootDB) {
				_, canConfig := TemplateForConnector(c)
				rows = append(rows, connRow{c.Name, c.Kind, ConnectorSummary(c), c.Owner, c.Approved, c.LastError, c.Template, c.Kind == RestImageConnectorKind, canConfig})
			}
			json.NewEncoder(w).Encode(rows)
		case http.MethodPost:
			name := strings.TrimSpace(r.URL.Query().Get("name"))
			if name == "" {
				http.Error(w, "missing name", http.StatusBadRequest)
				return
			}
			var err error
			switch r.URL.Query().Get("action") {
			case "approve":
				err = ApproveConnector(RootDB, name)
			case "unapprove":
				err = UnapproveConnector(RootDB, name)
			case "update_spec":
				// Replace the connector's kind-specific Spec with the posted JSON.
				// SaveConnector re-validates against the kind and, if the connector
				// is approved, re-materializes so an edit (e.g. a rest_image
				// backend's default_steps / submit_body) takes effect immediately.
				body, rerr := io.ReadAll(io.LimitReader(r.Body, 1<<20))
				if rerr != nil {
					http.Error(w, "read error", http.StatusBadRequest)
					return
				}
				if !json.Valid(body) {
					http.Error(w, "spec must be valid JSON", http.StatusBadRequest)
					return
				}
				c, ok := GetConnector(RootDB, name)
				if !ok {
					http.Error(w, "no connector named "+name, http.StatusNotFound)
					return
				}
				// If comfy_workflow was edited as a nested object (the readable
				// Edit-spec view), fold it back into the spec's string field.
				c.Spec = json.RawMessage(stringifyComfyWorkflow(body))
				err = SaveConnector(RootDB, c)
			default:
				http.Error(w, "action must be approve|unapprove|update_spec", http.StatusBadRequest)
				return
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			name := strings.TrimSpace(r.URL.Query().Get("name"))
			if name == "" {
				http.Error(w, "missing name", http.StatusBadRequest)
				return
			}
			if err := DeleteConnector(RootDB, name); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Connector spec — GET the current kind-specific Spec as pretty JSON for the
	// admin's inline spec editor (the "Edit spec" row action). Paired with the
	// update_spec POST above. Read-only; no secret is ever in a Spec.
	sub.HandleFunc("/api/connectors/spec", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		c, ok := GetConnector(RootDB, name)
		if !ok {
			http.Error(w, "no connector named "+name, http.StatusNotFound)
			return
		}
		spec := "{}"
		if len(c.Spec) > 0 {
			// Show a rest_image comfy_workflow as a NESTED object (readable) rather
			// than an escaped string; other kinds just get plain indentation.
			if nested, ok := nestComfyWorkflow(c.Spec); ok {
				spec = string(nested)
			} else {
				var pretty bytes.Buffer
				if json.Indent(&pretty, c.Spec, "", "  ") == nil {
					spec = pretty.String()
				} else {
					spec = string(c.Spec)
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"name": c.Name, "kind": c.Kind, "approved": c.Approved, "spec": spec,
		})
	})

	// Connector export — download a portable, secret-free JSON pack. ?name=<n>
	// exports one connector; omit name to export ALL as one bundle. Auth
	// references (credential names) travel; secrets never do. Content-Disposition
	// makes the browser download it.
	sub.HandleFunc("/api/connectors/export", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		var names []string
		if name != "" {
			names = []string{name}
		}
		pack, err := ExportConnectorPack(RootDB, names...)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		filename := "connectors.connector.json"
		if name != "" {
			filename = name + ".connector.json"
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(pack)
	})

	// Connector import — accept a pack (the JSON produced by export) and
	// reconstitute its connectors as new DRAFTS owned by the admin. Governance
	// re-applies: remote_mcp / desktop_* land unapproved; an existing name is
	// skipped, never overwritten. Returns the import summary as JSON.
	sub.HandleFunc("/api/connectors/import", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Accept either a raw pack body or {"pack":"<json string>"} from a form.
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		data := bytes.TrimSpace(body)
		var wrap struct {
			Pack string `json:"pack"`
		}
		if json.Unmarshal(data, &wrap) == nil && strings.TrimSpace(wrap.Pack) != "" {
			data = []byte(strings.TrimSpace(wrap.Pack))
		}
		res, err := ImportConnectorPack(RootDB, data, AuthCurrentUser(r))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
	})

	// Artifact export — the UNIFIED, cross-type download. Builds a
	// gohort.bundle/v1 carrying any registered artifact (connector, tool, …).
	// An individual export is just a one-item bundle:
	//   ?type=<t>&name=<n>[&owner=<u>]  → one artifact (owner scopes tools)
	//   ?all=<t1,t2>                    → every artifact of those types
	//   (no params)                     → everything
	// Auth references travel; secrets never do. Content-Disposition downloads it.
	sub.HandleFunc("/api/artifacts/export", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		typ := strings.TrimSpace(q.Get("type"))
		name := strings.TrimSpace(q.Get("name"))
		// Dependency closure is ON by default — a 1-item export carries the
		// credentials/tools it references so it installs cleanly elsewhere. The
		// UI's "Include dependencies" checkbox sends deps=0 to opt out (a bare
		// export of exactly the selection, for a target that already has them).
		includeDeps := true
		switch strings.ToLower(strings.TrimSpace(q.Get("deps"))) {
		case "0", "false", "no", "none", "off":
			includeDeps = false
		}
		exportSels := func(sels []ArtifactSel) (ArtifactBundle, error) {
			if includeDeps {
				return ExportArtifactBundle(RootDB, sels)
			}
			return ExportArtifactBundleShallow(RootDB, sels)
		}
		var (
			bundle   ArtifactBundle
			err      error
			filename = "gohort-bundle.json"
		)
		switch {
		case typ != "" && name != "":
			// Per-user types (tools, skills) need an owner; when the query
			// omits it, default to the requesting admin — the per-row export
			// buttons on surfaces that only list the requester's own pool
			// (Skills) don't have an owner field to send. Global types ignore
			// Owner entirely, so the default is inert for them.
			owner := strings.TrimSpace(q.Get("owner"))
			if owner == "" {
				owner = AuthCurrentUser(r)
			}
			bundle, err = exportSels([]ArtifactSel{{Type: typ, Name: name, Owner: owner}})
			filename = name + ".gohort.json"
		case strings.TrimSpace(q.Get("all")) != "":
			bundle, err = exportSels(ArtifactSelectionForTypes(RootDB, strings.Split(q.Get("all"), ",")...))
		default:
			bundle, err = exportSels(ArtifactSelectionForTypes(RootDB))
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(bundle)
	})

	// Artifact import — accept a gohort.bundle/v1 (or a legacy connector pack, or
	// a bare single artifact) and reconstitute every artifact as a DRAFT owned by
	// the importing admin: connectors land unapproved, tools land in the pending
	// pool. Nothing goes live without a separate approval. Returns the per-
	// artifact import summary as JSON. POST-only.
	sub.HandleFunc("/api/artifacts/import", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		data, err := readArtifactBundleBody(r)
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		res, err := ImportArtifactBundle(RootDB, data, AuthCurrentUser(r))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// message drives the form's post-submit note: counts plus any
		// unmet-dependency warnings, so an import that leaves a tool wired to a
		// missing credential says so instead of looking like a clean success.
		_ = json.NewEncoder(w).Encode(struct {
			ArtifactImportResult
			Message string `json:"message"`
		}{res, res.Summary()})
	})

	// Artifact import PREVIEW — the dry-run twin of /api/artifacts/import.
	// Same body shapes, same auth, writes NOTHING: returns what the bundle
	// carries, what would import vs skip, and any unmet references, so the
	// admin sees exactly what a bundle does before committing to it.
	sub.HandleFunc("/api/artifacts/preview", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		data, err := readArtifactBundleBody(r)
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		res, err := PreviewArtifactBundle(RootDB, data, AuthCurrentUser(r))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
	})

	// Catalog — a curated, in-tree set of ready-made artifact bundles (the
	// offline precursor to a marketplace). GET lists entries (metadata + what
	// each installs); POST ?action=install&id=<id> installs one through the
	// unified importer, so its artifacts land as DRAFTS for review — connectors
	// unapproved, tools pending, credentials inert. Same governance as a file
	// import; nothing a catalog install brings in goes live unreviewed.
	sub.HandleFunc("/api/catalog", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ListCatalog())
		case http.MethodPost:
			if r.URL.Query().Get("action") != "install" {
				http.Error(w, "action must be install", http.StatusBadRequest)
				return
			}
			id := strings.TrimSpace(r.URL.Query().Get("id"))
			if id == "" {
				http.Error(w, "missing id", http.StatusBadRequest)
				return
			}
			res, err := InstallCatalogEntry(RootDB, id, AuthCurrentUser(r))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			// Same message/warnings envelope as a file import — a catalog install
			// runs the same importer, so an unmet reference surfaces the same way
			// (a modal on the Install button rather than a form note).
			_ = json.NewEncoder(w).Encode(struct {
				ArtifactImportResult
				Message string `json:"message"`
			}{res, res.Summary()})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Source hooks — curated external sources (PubMed, OpenAlex, EDGAR,
	// custom APIs / RAG). GET lists; POST upserts (or ?action=expose|hide
	// toggles LLM-tool exposure); DELETE removes. A hook with
	// expose_to_llm=true is auto-surfaced as a per-hook agent tool
	// (BuildSourceHookAgentToolDefs, wired in the orchestrate runner).
	sub.HandleFunc("/api/source-hooks", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			// GET-one (?name=X) backs the Edit form's pre-fill: return the
			// raw hook in its editable shape with the secret blanked (the
			// form's auth_key Help says leave blank to keep it). The list
			// form (no name) returns the display rows below.
			if one := strings.TrimSpace(r.URL.Query().Get("name")); one != "" {
				for _, h := range RegisteredSourceHooks() {
					if strings.EqualFold(h.Name, one) {
						h.AuthKey = "" // never expose the stored secret to the edit form
						w.Header().Set("Content-Type", "application/json")
						json.NewEncoder(w).Encode(h)
						return
					}
				}
				http.Error(w, "hook not found", http.StatusNotFound)
				return
			}
			hooks := RegisteredSourceHooks()
			type row struct {
				Name            string   `json:"name"`
				Type            string   `json:"type"`
				Endpoint        string   `json:"endpoint"`
				AuthType        string   `json:"auth_type"`
				HasAuth         bool     `json:"has_auth"`
				QueryParam      string   `json:"query_param"`
				ResultsPath     string   `json:"results_path"`
				TitleField      string   `json:"title_field"`
				URLField        string   `json:"url_field"`
				SnippetField    string   `json:"snippet_field"`
				ContentField    string   `json:"content_field"`
				Domains         []string `json:"domains"`
				TriggerDomains  []string `json:"trigger_domains"`
				AlwaysActive    bool     `json:"always_active"`
				ExposeToLLM     bool     `json:"expose_to_llm"`
				Disabled        bool     `json:"disabled"`
				ToolName        string   `json:"tool_name"`
				EffectiveTool   string   `json:"effective_tool"`
				ToolDescription string   `json:"tool_description"`
			}
			out := make([]row, 0, len(hooks))
			for _, h := range hooks {
				eff := strings.TrimSpace(h.ToolName)
				if eff == "" {
					// Display approximation of the derived name (the real
					// derivation lives in sourceHookToAgentToolDef).
					eff = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(h.Name), " ", "_")) + "_search"
				}
				out = append(out, row{
					Name: h.Name, Type: string(h.Type), Endpoint: h.Endpoint,
					AuthType: string(h.AuthType), HasAuth: strings.TrimSpace(h.AuthKey) != "",
					QueryParam: h.QueryParam, ResultsPath: h.ResultsPath,
					TitleField: h.TitleField, URLField: h.URLField,
					SnippetField: h.SnippetField, ContentField: h.ContentField,
					Domains: h.Domains, TriggerDomains: h.TriggerDomains,
					AlwaysActive: h.AlwaysActive, ExposeToLLM: h.ExposeToLLM,
					Disabled: h.Disabled,
					ToolName: h.ToolName, EffectiveTool: eff, ToolDescription: h.ToolDescription,
				})
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(out)
		case http.MethodPost:
			// Toggle LLM exposure: ?action=expose|hide&name=X.
			if action := r.URL.Query().Get("action"); action != "" {
				name := strings.TrimSpace(r.URL.Query().Get("name"))
				if name == "" {
					http.Error(w, "missing name", http.StatusBadRequest)
					return
				}
				var target *SourceHook
				for _, h := range RegisteredSourceHooks() {
					if strings.EqualFold(h.Name, name) {
						hh := h
						target = &hh
						break
					}
				}
				if target == nil {
					http.Error(w, "hook not found", http.StatusNotFound)
					return
				}
				switch action {
				case "expose":
					target.ExposeToLLM = true
				case "hide":
					target.ExposeToLLM = false
				case "enable":
					// The review gate for imported hooks: enabling is the
					// admin's explicit "I've looked" — the hook starts
					// receiving traffic (topic routing, tools) from here.
					target.Disabled = false
				case "disable":
					target.Disabled = true
				default:
					http.Error(w, "action must be expose|hide|enable|disable", http.StatusBadRequest)
					return
				}
				SaveSourceHook(a.db, *target)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			var req struct {
				Name            string   `json:"name"`
				Type            string   `json:"type"`
				Endpoint        string   `json:"endpoint"`
				AuthType        string   `json:"auth_type"`
				AuthKey         string   `json:"auth_key"`
				QueryParam      string   `json:"query_param"`
				ResultsPath     string   `json:"results_path"`
				TitleField      string   `json:"title_field"`
				URLField        string   `json:"url_field"`
				SnippetField    string   `json:"snippet_field"`
				ContentField    string   `json:"content_field"`
				Domains         []string `json:"domains"`
				TriggerDomains  []string `json:"trigger_domains"`
				AlwaysActive    bool     `json:"always_active"`
				MaxRPS          int      `json:"max_rps"`
				CostPerCall     float64  `json:"cost_per_call"`
				ExposeToLLM     bool     `json:"expose_to_llm"`
				ToolName        string   `json:"tool_name"`
				ToolDescription string   `json:"tool_description"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
				return
			}
			name := strings.TrimSpace(req.Name)
			if name == "" {
				http.Error(w, "name required", http.StatusBadRequest)
				return
			}
			// Preserve an existing encrypted secret when the auth_key field
			// is left blank on an edit (matches the password-placeholder
			// convention — re-saving the form shouldn't wipe the secret), and
			// the Disabled mute — the edit form doesn't carry it, and saving
			// an imported hook's field mappings must not silently enable it
			// (Enable is its own explicit action).
			authKey := strings.TrimSpace(req.AuthKey)
			disabled := false
			for _, h := range RegisteredSourceHooks() {
				if strings.EqualFold(h.Name, name) {
					if authKey == "" || authKey == "(configured)" {
						authKey = h.AuthKey
					}
					disabled = h.Disabled
					break
				}
			}
			h := SourceHook{
				Name:            name,
				Type:            SourceHookType(strings.TrimSpace(req.Type)),
				Endpoint:        strings.TrimSpace(req.Endpoint),
				AuthType:        SourceHookAuth(strings.TrimSpace(req.AuthType)),
				AuthKey:         authKey,
				QueryParam:      strings.TrimSpace(req.QueryParam),
				ResultsPath:     strings.TrimSpace(req.ResultsPath),
				TitleField:      strings.TrimSpace(req.TitleField),
				URLField:        strings.TrimSpace(req.URLField),
				SnippetField:    strings.TrimSpace(req.SnippetField),
				ContentField:    strings.TrimSpace(req.ContentField),
				Domains:         req.Domains,
				TriggerDomains:  req.TriggerDomains,
				AlwaysActive:    req.AlwaysActive,
				MaxRPS:          req.MaxRPS,
				CostPerCall:     req.CostPerCall,
				ExposeToLLM:     req.ExposeToLLM,
				ToolName:        strings.TrimSpace(req.ToolName),
				ToolDescription: strings.TrimSpace(req.ToolDescription),
				Disabled:        disabled,
			}
			SaveSourceHook(a.db, h)
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			name := strings.TrimSpace(r.URL.Query().Get("name"))
			if name == "" {
				http.Error(w, "missing name", http.StatusBadRequest)
				return
			}
			DeleteSourceHook(a.db, name)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Persistent tools (created via create_temp_tool with persist=true).
	// GET returns {pending: [...], active: [...]} for the current user.
	// POST/DELETE mutate per the action query param. Each entry includes
	// the full command_template so the admin can spot anything fishy
	// before approving — that visibility is the whole point of the
	// approval queue.
	sub.HandleFunc("/api/persistent-tools", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		username := AuthCurrentUser(r)
		if username == "" {
			http.Error(w, "no user identity", http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodGet:
			// Single-tool adopt-ACL fetch for the ACLPicker: ?allowed_users=<name>&owner=<u>
			// returns just {allowed_users:[...]} for that tool (the picker reads the
			// array and posts it back via action=set_allowed_users).
			if toolName := strings.TrimSpace(r.URL.Query().Get("allowed_users")); toolName != "" {
				owner := strings.TrimSpace(r.URL.Query().Get("owner"))
				if owner == "" {
					owner = username
				}
				var allowed []string
				for _, p := range LoadPersistentTempTools(a.db, owner) {
					if p.Tool.Name == toolName {
						allowed = p.AllowedUsers
						break
					}
				}
				if allowed == nil {
					allowed = []string{}
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"allowed_users": allowed})
				return
			}
			// Persistent + pending pools are stored per-user at the
			// kvlite layer, but admins see the whole deployment —
			// the tool-groups registry walks all users; this page
			// should match. Each entry surfaces with an "owner"
			// badge so the admin can tell whose tool is whose.
			store := RootDB
			if store == nil {
				store = a.db
			}
			// Flag a tool whose credential dependency doesn't resolve, for a
			// row badge. Cheap (a registry lookup); no-auth / credential-less
			// tools return nil.
			toolMissingDeps := func(t TempTool) []string {
				cred := strings.TrimSpace(t.Credential)
				if cred == "" || strings.EqualFold(cred, "no_auth") {
					return nil
				}
				if exists, _, _ := Secure().CredentialStatus(cred); !exists {
					return []string{"credential:" + cred}
				}
				return nil
			}
			type pendingWithOwner struct {
				Owner string `json:"owner"`
				PendingTempTool
			}
			type activeWithOwner struct {
				Owner      string   `json:"owner"`
				Missing    []string `json:"missing,omitempty"`
				HasMissing bool     `json:"has_missing"`
				PersistentTempTool
			}
			var pending []pendingWithOwner
			var active []activeWithOwner
			if store != nil {
				// Pending pool keys live in a separate table; load
				// both per-user pools then merge with owner attribution.
				seen := map[string]bool{}
				addUser := func(u string) {
					if u == "" || seen[u] {
						return
					}
					seen[u] = true
					for _, p := range LoadPendingTempTools(a.db, u) {
						pending = append(pending, pendingWithOwner{Owner: u, PendingTempTool: p})
					}
					for _, p := range LoadPersistentTempTools(a.db, u) {
						m := toolMissingDeps(p.Tool)
						active = append(active, activeWithOwner{Owner: u, Missing: m, HasMissing: len(m) > 0, PersistentTempTool: p})
					}
				}
				// Walk both tables — usernames may exist in one and not
				// the other depending on approval state.
				for _, u := range store.Keys("persistent_temp_tools") {
					addUser(u)
				}
				for _, u := range store.Keys("pending_temp_tools") {
					addUser(u)
				}
				// Ensure the calling admin is always represented even
				// when they have no pool yet (so the page renders
				// instead of erroring on a fresh deployment).
				addUser(username)
			} else {
				pending = nil
				active = nil
			}
			// Agent-bundled tools — authored via add_tool, they ride
			// inside an agent record's .Tools (NOT the temp-tool pools),
			// so they never surfaced on this page before ("hidden tools").
			// Read-only here: they're removed via Builder, not the admin.
			// Walk every user's agent records and surface each bundled tool
			// with its owning agent so nothing is invisible in the DB.
			type bundledAgent struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			// One row per (owner, tool): a tool scoped to several agents is listed
			// ONCE with all of them, not duplicated per agent. Agent is the
			// comma-joined names for the column; Agents is the structured list.
			type bundledWithOwner struct {
				Owner      string         `json:"owner"`
				Agent      string         `json:"agent"`
				Agents     []bundledAgent `json:"agents"`
				Missing    []string       `json:"missing,omitempty"`
				HasMissing bool           `json:"has_missing"`
				Tool       TempTool       `json:"tool"`
			}
			// Global SUPERSEDES agent scope in this listing. A tool that
			// lives in the owner's global pool AND on an agent record (a
			// leftover copy from before promotion, or a name collision)
			// belongs under Global — every one of the owner's agents already
			// sees it there, so also listing it as "agent-scoped" is a
			// confusing duplicate that invites descoping the wrong copy.
			// Skip those names below; they reappear as agent-scoped only when
			// the Global pill is turned off (demoteGlobalToScoped bundles the
			// copies back). Mirrors captureOrphanedTools, which drops the same
			// global-covered names from the orphan store on agent delete.
			globalByOwner := map[string]map[string]bool{}
			for _, aw := range active {
				m := globalByOwner[aw.Owner]
				if m == nil {
					m = map[string]bool{}
					globalByOwner[aw.Owner] = m
				}
				m[aw.Tool.Name] = true
			}
			var bundled []bundledWithOwner
			// Group by (owner, tool name) so a tool scoped to N agents is one row
			// listing all N — not N duplicate rows sharing the same tool.name key.
			type bundleKey struct{ owner, name string }
			bundleIdx := map[bundleKey]int{}
			orchestrateBase := a.db.Bucket("orchestrate")
			for _, u := range AuthListUsers(a.db) {
				udb := UserDB(orchestrateBase, u.Username)
				if udb == nil {
					continue
				}
				for _, key := range udb.Keys("orchestrate_agents") {
					// Minimal struct — gob matches by field NAME, so ID /
					// Name / Tools decode out of the full AgentRecord and
					// the rest is ignored. TempTool is a core type.
					var rec struct {
						ID      string
						Name    string
						Hidden  bool
						OwnedBy string
						Tools   []TempTool
					}
					if !udb.Get("orchestrate_agents", key, &rec) {
						continue
					}
					// App-specific agents (Guide Author, Servitor Investigator, …)
					// are Hidden, and sub-agents carry OwnedBy — both have curated,
					// purpose-built kits and aren't user tool-scope targets. Keep
					// them out of this list; only top-level user-managed agents show.
					if rec.Hidden || rec.OwnedBy != "" {
						continue
					}
					for _, t := range rec.Tools {
						if globalByOwner[u.Username][t.Name] {
							continue // global copy supersedes this agent-scoped duplicate
						}
						bk := bundleKey{u.Username, t.Name}
						i, ok := bundleIdx[bk]
						if !ok {
							m := toolMissingDeps(t)
							bundled = append(bundled, bundledWithOwner{
								Owner: u.Username, Missing: m, HasMissing: len(m) > 0, Tool: t,
							})
							i = len(bundled) - 1
							bundleIdx[bk] = i
						}
						bundled[i].Agents = append(bundled[i].Agents, bundledAgent{ID: rec.ID, Name: rec.Name})
					}
				}
			}
			// Comma-join each row's agent names for the display column.
			for i := range bundled {
				names := make([]string, 0, len(bundled[i].Agents))
				for _, ag := range bundled[i].Agents {
					names = append(names, ag.Name)
				}
				bundled[i].Agent = strings.Join(names, ", ")
			}
			// Orphaned tools — formerly agent-scoped, captured when their
			// owning agent was deleted. Walk every user's orphan pool.
			type orphanWithOwner struct {
				Owner string `json:"owner"`
				OrphanedTempTool
				Missing    []string `json:"missing,omitempty"`
				HasMissing bool     `json:"has_missing"`
			}
			var orphaned []orphanWithOwner
			for _, u := range AuthListUsers(a.db) {
				for _, o := range LoadOrphanedTempTools(a.db, u.Username) {
					m := toolMissingDeps(o.Tool)
					orphaned = append(orphaned, orphanWithOwner{Owner: u.Username, OrphanedTempTool: o, Missing: m, HasMissing: len(m) > 0})
				}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"pending":  pending,
				"active":   active,
				"bundled":  bundled,
				"orphaned": orphaned,
			})
		case http.MethodPost:
			// owner query param tells the handler which user's pool
			// to mutate. Falls back to the calling admin (back-compat
			// with old URLs that didn't pass owner) but ANY admin can
			// approve/reject any user's tools — the pools are
			// admin-managed, not user-owned in the auth-policy sense.
			action := r.URL.Query().Get("action")
			name := strings.TrimSpace(r.URL.Query().Get("name"))
			owner := strings.TrimSpace(r.URL.Query().Get("owner"))
			if owner == "" {
				owner = username
			}
			if name == "" {
				http.Error(w, "missing name", http.StatusBadRequest)
				return
			}
			var err error
			switch action {
			case "approve":
				err = ApprovePendingTempTool(a.db, owner, name)
			case "reject":
				err = RejectPendingTempTool(a.db, owner, name)
			case "share":
				err = SetPersistentTempToolShared(a.db, owner, name, true)
			case "unshare":
				err = SetPersistentTempToolShared(a.db, owner, name, false)
			case "set_allowed_users":
				// Body carries the full ACL array (the ACLPicker posts the fetched
				// {allowed_users:[...]} record whole). Empty = open to all users.
				var body struct {
					AllowedUsers []string `json:"allowed_users"`
				}
				if derr := json.NewDecoder(r.Body).Decode(&body); derr != nil {
					http.Error(w, "bad request", http.StatusBadRequest)
					return
				}
				err = SetPersistentTempToolAllowedUsers(a.db, owner, name, body.AllowedUsers)
			case "orphan_promote":
				if AdminRehomeOrphanTool == nil {
					http.Error(w, "unavailable (orchestrate not wired)", http.StatusServiceUnavailable)
					return
				}
				err = AdminRehomeOrphanTool(a.db, owner, name, "global")
			case "orphan_attach":
				agentID := strings.TrimSpace(r.URL.Query().Get("agent"))
				if agentID == "" {
					http.Error(w, "orphan_attach requires agent", http.StatusBadRequest)
					return
				}
				if AdminRehomeOrphanTool == nil {
					http.Error(w, "unavailable (orchestrate not wired)", http.StatusServiceUnavailable)
					return
				}
				err = AdminRehomeOrphanTool(a.db, owner, name, agentID)
			case "orphan_delete":
				if !RemoveOrphanedTempTool(a.db, owner, name) {
					http.Error(w, "no orphaned tool named "+name, http.StatusNotFound)
					return
				}
			default:
				http.Error(w, "action must be approve|reject|share|unshare|set_allowed_users|orphan_promote|orphan_attach|orphan_delete", http.StatusBadRequest)
				return
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			name := strings.TrimSpace(r.URL.Query().Get("name"))
			owner := strings.TrimSpace(r.URL.Query().Get("owner"))
			if owner == "" {
				owner = username
			}
			if name == "" {
				http.Error(w, "missing name", http.StatusBadRequest)
				return
			}
			if err := DeletePersistentTempTool(a.db, owner, name); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Tool scope — the pill control's data + toggles. GET ?name=&owner=
	// returns the ToolScopeState (global flag + per-agent on/off + missing
	// deps). POST ?name=&owner= with body {target, on} applies one toggle
	// (target "global" or an agent id). Both delegate to orchestrate.
	sub.HandleFunc("/api/tool-scope", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		username := AuthCurrentUser(r)
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		owner := strings.TrimSpace(r.URL.Query().Get("owner"))
		if owner == "" {
			owner = username
		}
		if name == "" {
			http.Error(w, "missing name", http.StatusBadRequest)
			return
		}
		// kind selects the scope backend (tool | pipeline | credential),
		// dispatched through the core registry the orchestrate app wires.
		kind := strings.TrimSpace(r.URL.Query().Get("kind"))
		if kind == "" {
			kind = "tool"
		}
		prov, ok := ScopeProviderFor(kind)
		if !ok {
			http.Error(w, "unavailable (scope kind "+kind+" not wired)", http.StatusServiceUnavailable)
			return
		}
		switch r.Method {
		case http.MethodGet:
			st, ok := prov.State(a.db, owner, name)
			if !ok {
				http.Error(w, kind+" not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			// The scope-pill modal re-GETs this exact URL immediately after
			// each toggle POST to re-render. A cached (stale) response makes
			// the pill snap back to its pre-toggle state ("won't uncheck"), so
			// force a fresh read every time.
			w.Header().Set("Cache-Control", "no-store")
			json.NewEncoder(w).Encode(st)
		case http.MethodPost:
			var body struct {
				Target string `json:"target"`
				On     bool   `json:"on"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad request body", http.StatusBadRequest)
				return
			}
			body.Target = strings.TrimSpace(body.Target)
			if body.Target == "" {
				http.Error(w, "target is required", http.StatusBadRequest)
				return
			}
			if err := prov.Set(a.db, owner, name, body.Target, body.On); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Skills: conditional prompt addendums that auto-activate based
	// on the user's message. GET lists all the admin's skills; POST
	// upserts (id empty = create, present = update) — Builder
	// authors most of these but the admin UI is the canonical
	// "list/edit/toggle/delete" surface. DELETE drops one by id.
	// Pipelines — declarative multi-stage workflows (core.PipelineDef),
	// stored per-user in orchestrate. Admins see the whole deployment:
	// the list walks every user's store and attributes each pipeline to
	// its owner. GET lists; DELETE ?id= removes one (pipeline IDs are
	// UUIDs, so the owner is resolved by scanning — no owner param needed).
	// Authoring lives in Agency (the pipeline tool / Builder); this is a
	// read + prune surface, mirroring the Skills section.
	sub.HandleFunc("/api/pipelines", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		// Pipelines are stored in orchestrate's per-app bucket
		// (get_agentstore("orchestrate") = global.db.Bucket("orchestrate")),
		// then per-user via UserDB — NOT in RootDB like skills/temp-tools
		// (those resolve to RootDB internally). So admin must reach into
		// the same bucket orchestrate writes to; a.db (= global.db / RootDB)
		// alone misses them. This couples admin to orchestrate's app name,
		// which is acceptable: admin is the deployment console.
		orchestrateBase := a.db.Bucket("orchestrate")
		switch r.Method {
		case http.MethodGet:
			type wire struct {
				ID          string      `json:"id"`
				Owner       string      `json:"owner"`
				Name        string      `json:"name"`
				Description string      `json:"description"`
				Stages      int         `json:"stages"`
				Detail      PipelineDef `json:"detail"`
			}
			var out []wire
			for _, u := range AuthListUsers(a.db) {
				udb := UserDB(orchestrateBase, u.Username)
				if udb == nil {
					continue
				}
				for _, d := range ListPipelineDefs(udb, u.Username) {
					out = append(out, wire{
						ID: d.ID, Owner: u.Username, Name: d.Name,
						Description: d.Description, Stages: len(d.Stages), Detail: d,
					})
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"pipelines": out})
		case http.MethodDelete:
			id := strings.TrimSpace(r.URL.Query().Get("id"))
			if id == "" {
				http.Error(w, "id required", http.StatusBadRequest)
				return
			}
			for _, u := range AuthListUsers(a.db) {
				udb := UserDB(orchestrateBase, u.Username)
				if udb == nil {
					continue
				}
				if _, ok := LoadPipelineDef(udb, u.Username, id); ok {
					DeletePipelineDef(udb, id)
					Log("[admin] %q deleted pipeline %s (owner=%q)", AuthCurrentUser(r), id, u.Username)
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"deleted": id})
					return
				}
			}
			http.NotFound(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	sub.HandleFunc("/api/skills", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		username := AuthCurrentUser(r)
		if username == "" {
			http.Error(w, "no user identity", http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			skills := LoadSkills(a.db, username)
			// Strip the embedding from the wire payload — it's a
			// large float32 array that the admin UI doesn't need
			// and would just bloat the response.
			type wire struct {
				ID                  string   `json:"id"`
				Name                string   `json:"name"`
				Description         string   `json:"description"`
				Triggers            []string `json:"triggers"`
				AllowedTools        []string `json:"allowed_tools"`
				AttachedCollections []string `json:"attached_collections"`
				Instructions        string   `json:"instructions"`
				Disabled            bool     `json:"disabled"`
				Updated             string   `json:"updated"`
			}
			out := make([]wire, 0, len(skills))
			for _, s := range skills {
				out = append(out, wire{
					ID: s.ID, Name: s.Name,
					Description: s.Description,
					Triggers:    s.Triggers, AllowedTools: s.AllowedTools,
					AttachedCollections: s.AttachedCollections,
					Instructions:        s.Instructions, Disabled: s.Disabled,
					Updated: s.Updated.Format("2006-01-02 15:04:05"),
				})
			}
			json.NewEncoder(w).Encode(out)
		case http.MethodPost:
			// Partial-update mode: ?action=enable|disable just flips
			// the Disabled flag and persists. Used by the per-row
			// toggle button so a quick mute doesn't require a full
			// record round-trip. The full POST body path below
			// remains for the Edit form.
			if action := strings.TrimSpace(r.URL.Query().Get("action")); action == "enable" || action == "disable" {
				id := strings.TrimSpace(r.URL.Query().Get("id"))
				if id == "" {
					http.Error(w, "missing id", http.StatusBadRequest)
					return
				}
				var found *SkillRecord
				for _, s := range LoadSkills(a.db, username) {
					if s.ID == id {
						copy := s
						found = &copy
						break
					}
				}
				if found == nil {
					http.Error(w, "skill not found", http.StatusNotFound)
					return
				}
				found.Disabled = (action == "disable")
				if _, err := SaveSkill(a.db, username, *found); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			var body SkillRecord
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
				return
			}
			if strings.TrimSpace(body.Name) == "" {
				http.Error(w, "name is required", http.StatusBadRequest)
				return
			}
			if strings.TrimSpace(body.Description) == "" {
				http.Error(w, "description is required", http.StatusBadRequest)
				return
			}
			// If ID is set, preserve fields that the Edit form doesn't
			// surface from the prior record. Disabled has its own
			// dedicated toggle endpoint (?action=enable|disable), so
			// the full-body PATH from the Edit form / ChipPicker
			// never represents a deliberate Disabled change — always
			// preserve it from the prior.
			if body.ID != "" {
				for _, prior := range LoadSkills(a.db, username) {
					if prior.ID == body.ID {
						body.Created = prior.Created
						body.Disabled = prior.Disabled
						break
					}
				}
			}
			saved, err := SaveSkill(a.db, username, body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(saved)
		case http.MethodDelete:
			id := strings.TrimSpace(r.URL.Query().Get("id"))
			if id == "" {
				http.Error(w, "missing id", http.StatusBadRequest)
				return
			}
			if !DeleteSkill(a.db, username, id) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Per-skill GET — backs the row-expand FormPanel.Source. POST /
	// DELETE go through the list endpoint above (body / query
	// carries the id). Trailing-slash form so FormPanel.Source can
	// template "api/skills/{id}" cleanly.
	sub.HandleFunc("/api/skills/", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		username := AuthCurrentUser(r)
		if username == "" {
			http.Error(w, "no user identity", http.StatusUnauthorized)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/skills/")
		id = strings.Trim(id, "/")
		if id == "" || strings.Contains(id, "/") {
			http.NotFound(w, r)
			return
		}
		for _, s := range LoadSkills(a.db, username) {
			if s.ID == id {
				w.Header().Set("Content-Type", "application/json")
				// Strip embedding from the wire payload — same as list.
				type wire struct {
					ID                  string   `json:"id"`
					Name                string   `json:"name"`
					Description         string   `json:"description"`
					Triggers            []string `json:"triggers"`
					AllowedTools        []string `json:"allowed_tools"`
					AttachedCollections []string `json:"attached_collections"`
					Instructions        string   `json:"instructions"`
					Disabled            bool     `json:"disabled"`
				}
				_ = json.NewEncoder(w).Encode(wire{
					ID: s.ID, Name: s.Name, Description: s.Description,
					Triggers: s.Triggers, AllowedTools: s.AllowedTools,
					AttachedCollections: s.AttachedCollections,
					Instructions:        s.Instructions, Disabled: s.Disabled,
				})
				return
			}
		}
		http.NotFound(w, r)
	})

	// Collections (admin-side): GET returns the current user's
	// Document Collections as a lightweight picker payload so the
	// Skills editor can offer an attached_collections ChipPicker
	// alongside allowed_tools. Mirrors the per-user scope orchestrate
	// uses (UserDB under the orchestrate bucket) — admin doesn't own
	// collection storage, it just exposes a read view. List-only by
	// design: create/edit/delete still happen on the Collections page
	// in orchestrate.
	sub.HandleFunc("/api/collections", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		username := AuthCurrentUser(r)
		if username == "" {
			http.Error(w, "no user identity", http.StatusUnauthorized)
			return
		}
		orchestrateBase := a.db.Bucket("orchestrate")
		udb := UserDB(orchestrateBase, username)
		type entry struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		out := []entry{}
		if udb != nil {
			for _, c := range ListCollections(udb, username) {
				out = append(out, entry{ID: c.ID, Name: c.Name, Description: c.Description})
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})

	// Tool Groups: admin-curated bundles of chat tools that the runtime
	// catalog rewriter can collapse into one expandable entry. GET
	// lists all groups; POST upserts (id empty = create, present = update);
	// DELETE removes by id. The /registry sub-endpoint returns the
	// global ChatTool registry so the member picker has options.
	sub.HandleFunc("/api/tool-groups", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			// Wrap each group with is_builtin so the per-row UI can
			// branch — admin-curated groups get Delete, framework
			// defaults get Revert (which drops the shadow but
			// preserves the in-code definition).
			groups := LoadToolGroups(a.db)
			type wire struct {
				ToolGroup
				IsBuiltin bool `json:"is_builtin"`
			}
			out := make([]wire, 0, len(groups))
			for _, g := range groups {
				out = append(out, wire{ToolGroup: g, IsBuiltin: IsBuiltinToolGroupID(g.ID)})
			}
			_ = json.NewEncoder(w).Encode(out)
		case http.MethodPost:
			var req ToolGroup
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			// Membership is no longer edited from the Categories UI (custom
			// tools self-claim via Tool.Category; built-in members are
			// framework-defined). A name/description save omits Members, so
			// PRESERVE the stored member list rather than blank it — otherwise
			// renaming a category would drop the built-in Web Media grouping.
			if req.ID != "" && len(req.Members) == 0 {
				if existing, ok := LoadToolGroup(a.db, req.ID); ok {
					req.Members = existing.Members
				}
			}
			saved, err := SaveToolGroup(a.db, req)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(saved)
		case http.MethodDelete:
			id := strings.TrimSpace(r.URL.Query().Get("id"))
			if id == "" {
				http.Error(w, "missing id", http.StatusBadRequest)
				return
			}
			if err := DeleteToolGroup(a.db, id); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	// App groups: bundle apps (by web path) so an admin can grant a whole
	// set to a user in one assignment. List (GET) / upsert (POST) / delete
	// (DELETE ?id=). Mirrors the tool-groups endpoint shape.
	sub.HandleFunc("/api/app-groups", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(LoadAppGroups(a.db))
		case http.MethodPost:
			var req AppGroup
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			saved, err := SaveAppGroup(a.db, req)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(saved)
		case http.MethodDelete:
			id := strings.TrimSpace(r.URL.Query().Get("id"))
			if id == "" {
				http.Error(w, "missing id", http.StatusBadRequest)
				return
			}
			if err := DeleteAppGroup(a.db, id); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	// Single-record GET for the per-row editor (ChipPicker.RecordSource /
	// FormPanel.Source fetch one group by id).
	sub.HandleFunc("/api/app-groups/", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/app-groups/")
		g, ok := LoadAppGroup(a.db, id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(g)
	})
	// Single-record GET for the per-row editor: returns the full
	// ToolGroup JSON for the given id. POST/PUT/DELETE on individual
	// records go through the list endpoint above (body carries the id);
	// this trailing-slash variant exists so ChipPicker.RecordSource and
	// FormPanel.Source can fetch one group cleanly.
	sub.HandleFunc("/api/tool-groups/", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/api/tool-groups/")
		// /api/tool-groups/registry is a sibling, handled below — let it
		// route there rather than 404 here.
		if rest == "registry" {
			http.NotFound(w, r) // ServeMux's longest-prefix wins; this branch shouldn't fire
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		g, ok := LoadToolGroup(a.db, rest)
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(g)
	})
	// Per-field LLM suggest for the Tool Groups editor. Same
	// {field, hint, record} → {value} shape as the agent-editor's
	// suggest. Builds a prompt that includes the group's name and
	// member tool descriptions so the LLM can synthesize a description
	// the agent's catalog will actually find useful.
	sub.HandleFunc("/api/tool-groups/suggest", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		a.handleToolGroupSuggest(w, r)
	})
	// Auto-create: admin picks members, LLM proposes name +
	// description, server saves. The minimal-friction path — most
	// of the time the LLM names a bundle better than the admin
	// would anyway, since the LLM is the one who'll have to call
	// the group later. Admin can rename via the per-row editor.
	sub.HandleFunc("/api/tool-groups/auto-create", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		a.handleToolGroupAutoCreate(w, r)
	})
	// Tool registry — every tool name + description the member-picker
	// should be able to offer. Merges two sources:
	//
	//   1. Globally-registered ChatTools (built-in static registry).
	//   2. Persistent temp tools across ALL users (admin-wide view —
	//      since groups are deployment-wide, any user's temp tool is
	//      a valid grouping target as long as the name is stable).
	//
	// Deduped by name; first occurrence wins. Temp tools tagged with
	// `source: "temp"` so the UI can distinguish if it wants to.
	//
	// Query params:
	//
	//   exclude_grouped=true   Drop tools that are members of any
	//                          existing group. Used by the create
	//                          form's chip picker so admin can only
	//                          select ungrouped tools — prevents
	//                          accidental overlap into multiple
	//                          groups when authoring a new one.
	//   except_group=<id>      Allow members of the given group
	//                          through despite exclude_grouped.
	//                          Used by the per-group editor so the
	//                          group's current members stay
	//                          visible (and toggleable) while the
	//                          rest of the already-grouped surface
	//                          stays hidden.
	sub.HandleFunc("/api/tool-groups/registry", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		type entry struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Source      string `json:"source"` // "builtin" | "temp"
		}
		excludeGrouped := r.URL.Query().Get("exclude_grouped") == "true"
		exceptGroup := strings.TrimSpace(r.URL.Query().Get("except_group"))

		// Build the set of names that should be hidden when
		// exclude_grouped=true: union of all groups' members minus
		// the except_group's members. Empty when not filtering.
		hidden := map[string]bool{}
		if excludeGrouped {
			for _, g := range LoadToolGroups(a.db) {
				if g.ID == exceptGroup {
					continue
				}
				for _, m := range g.Members {
					hidden[m] = true
				}
			}
		}
		// Explicit exclude list — comma-separated names that the
		// caller wants suppressed regardless of group membership.
		// Used by surfaces where a specific tool is framework-
		// managed and shouldn't be admin-selectable.
		if extra := strings.TrimSpace(r.URL.Query().Get("exclude")); extra != "" {
			for _, n := range strings.Split(extra, ",") {
				n = strings.TrimSpace(n)
				if n != "" {
					hidden[n] = true
				}
			}
		}

		seen := map[string]bool{}
		out := make([]entry, 0, 64)
		add := func(name, desc, source string) {
			if name == "" || seen[name] || hidden[name] {
				return
			}
			seen[name] = true
			out = append(out, entry{Name: name, Description: desc, Source: source})
		}
		for _, t := range RegisteredChatTools() {
			// Framework tools (agents, plan_set, respond_directly,
			// ask_user, expand_tool_group, etc.) are never admin-
			// groupable — they're wired into the round-shape, not
			// user-facing capability. Hide them from the picker so
			// the admin doesn't accidentally select them for a
			// group that would never apply.
			if IsFrameworkTool(t) {
				continue
			}
			add(t.Name(), t.Desc(), "builtin")
		}
		// Walk every user's persistent temp tools. Lives in RootDB
		// (see tempToolStore), keyed by username; one Get per user
		// returns their full pool. Cheap at gohort scale.
		store := RootDB
		if store == nil {
			store = a.db
		}
		if store != nil {
			for _, username := range store.Keys("persistent_temp_tools") {
				var pool []PersistentTempTool
				if !store.Get("persistent_temp_tools", username, &pool) {
					continue
				}
				for _, p := range pool {
					add(p.Tool.Name, p.Tool.Description, "temp")
				}
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})

	// LLM routing: GET returns all stages + current values, POST updates one.
	sub.HandleFunc("/api/routing", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		type stageEntry struct {
			Key           string `json:"key"`
			Label         string `json:"label"`
			Value         string `json:"value"`
			Default       string `json:"default"`
			ThinkBudget   int    `json:"think_budget"`
			DefaultBudget int    `json:"default_budget"`
			Group         string `json:"group"`
			Private       bool   `json:"private"`
		}
		if r.Method == http.MethodPost {
			var req struct {
				Key         string `json:"key"`
				Value       string `json:"value"`
				ThinkBudget int    `json:"think_budget"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			allowed := map[string]bool{"lead": true, "worker": true, "worker (thinking)": true}
			if !allowed[req.Value] {
				http.Error(w, "invalid value", http.StatusBadRequest)
				return
			}
			// Private stages can't route to lead, but allow worker ↔ worker (thinking).
			if IsPrivateStage(req.Key) && req.Value == "lead" {
				http.Error(w, "private stage — cannot route to lead", http.StatusForbidden)
				return
			}
			if a.db != nil {
				a.db.Set(RoutingTable, req.Key, req.Value)
				if req.ThinkBudget > 0 {
					a.db.Set(RoutingTable, req.Key+".think_budget", req.ThinkBudget)
				} else {
					a.db.Unset(RoutingTable, req.Key+".think_budget")
				}
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		stages := ListRouteStages()
		out := make([]stageEntry, len(stages))
		for i, s := range stages {
			val := ""
			if a.db != nil {
				a.db.Get(RoutingTable, s.Key, &val)
			}
			if val == "" {
				val = s.Default
			}
			if val == "" {
				val = "lead"
			}
			def := s.Default
			if def == "" {
				def = "lead"
			}
			var thinkBudget int
			if a.db != nil {
				a.db.Get(RoutingTable, s.Key+".think_budget", &thinkBudget)
			}
			group := s.Group
			if group == "" {
				parts := strings.SplitN(s.Key, ".", 2)
				group = strings.Title(parts[0])
			}
			out[i] = stageEntry{Key: s.Key, Label: s.Label, Value: val, Default: def, ThinkBudget: thinkBudget, DefaultBudget: s.DefaultBudget, Group: group, Private: s.Private}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})

	// Worker LLM thinking defaults: GET returns current settings, POST updates.
	sub.HandleFunc("/api/worker-thinking", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method == http.MethodPost {
			var req struct {
				Enabled bool `json:"enabled"`
				Budget  int  `json:"budget"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if a.db != nil {
				a.db.Set(LLMTable, "disable_thinking", !req.Enabled)
				if req.Budget > 0 {
					a.db.Set(LLMTable, "thinking_budget", req.Budget)
				} else {
					a.db.Unset(LLMTable, "thinking_budget")
				}
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var disabled bool
		var budget int
		if a.db != nil {
			a.db.Get(LLMTable, "disable_thinking", &disabled)
			a.db.Get(LLMTable, "thinking_budget", &budget)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"enabled": !disabled,
			"budget":  budget,
		})
	})

	// Worker LLM (primary / local) connection + thinking config. Mirrors the
	// --setup "Primary Provider" step. Writes llm_config. The API key is
	// CryptSet only when a new value is typed (blank = keep existing) and is
	// never returned by GET. Parallel-request caps live in the separate Local
	// Model Scheduler section. Takes effect on restart (the shared LLM is built
	// once at startup).
	sub.HandleFunc("/api/worker-llm", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		a.handleLLMConfig(w, r, LLMTable, true)
	})
	// Lead LLM (precision / remote) connection + thinking config. Mirrors the
	// --setup "Precision LLM" step. Writes lead_llm_config; an empty provider
	// means "use the primary worker". Same key-masking + restart semantics.
	sub.HandleFunc("/api/lead-llm", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		a.handleLLMConfig(w, r, LeadLLMTable, false)
	})

	// Local model scheduler: GET returns max parallel for Ollama and llama.cpp,
	// POST updates both values. Requires restart to apply.
	sub.HandleFunc("/api/local-scheduler", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method == http.MethodPost {
			var req struct {
				OllamaMaxParallel   int `json:"ollama_max_parallel"`
				LlamacppMaxParallel int `json:"llamacpp_max_parallel"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if a.db != nil {
				if req.OllamaMaxParallel < 1 {
					req.OllamaMaxParallel = 1
				}
				if req.LlamacppMaxParallel < 1 {
					req.LlamacppMaxParallel = 1
				}
				a.db.Set(LLMTable, "ollama_max_parallel", req.OllamaMaxParallel)
				a.db.Set(LLMTable, "llamacpp_max_parallel", req.LlamacppMaxParallel)
			}
			// Parallel caps are read when the LLM client is built, so a reload
			// re-applies them live (no restart).
			if err := ReloadLLMs(); err != nil {
				Log("[admin] LLM reload after scheduler save failed: %v", err)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var ollamaMP, llamacppMP int
		if a.db != nil {
			a.db.Get(LLMTable, "ollama_max_parallel", &ollamaMP)
			a.db.Get(LLMTable, "llamacpp_max_parallel", &llamacppMP)
		}
		if ollamaMP < 1 {
			ollamaMP = 1
		}
		if llamacppMP < 1 {
			llamacppMP = 1
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ollama_max_parallel":   ollamaMP,
			"llamacpp_max_parallel": llamacppMP,
		})
	})

	// API: database browser.
	sub.HandleFunc("/api/db/tables", a.handleDBTables)
	sub.HandleFunc("/api/db/keys", a.handleDBKeys)
	sub.HandleFunc("/api/db/record", a.handleDBRecord)

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
		Invite   bool   `json:"invite"` // send a registration link instead of setting a password
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" {
		http.Error(w, "email required", http.StatusBadRequest)
		return
	}
	if _, exists := AuthGetUser(a.db, req.Username); exists {
		http.Error(w, "user already exists", http.StatusConflict)
		return
	}
	current := AuthCurrentUser(r)

	// Invite: create the account with no password and hand back a one-time link
	// the user clicks to set their own. Emailed when mail is configured; always
	// returned so the admin can copy it manually.
	if req.Invite {
		link, err := AuthCreateInvite(a.db, req.Username, req.Admin)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		emailed := false
		if EmailConfigured() {
			body := fmt.Sprintf("You've been invited to %s. Click the link below to set your password and activate your account:\n\n%s\n", ServiceName(), link)
			if SendNotification(req.Username, "["+ServiceName()+"] You've been invited", body) == nil {
				emailed = true
			}
		}
		Log("[admin] user %q invited %q (admin=%v, emailed=%v)", current, req.Username, req.Admin, emailed)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"status": "invited", "link": link, "emailed": emailed})
		return
	}

	// Direct: set a password now.
	if req.Password == "" {
		http.Error(w, "password required (or choose to send an invite link)", http.StatusBadRequest)
		return
	}
	AuthSetUser(a.db, req.Username, req.Password, req.Admin)
	Log("[admin] user %q created user %q (admin=%v)", current, req.Username, req.Admin)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "created"})
}

// handleResetPassword resets a user's password: mode "set" (admin types a new
// one) or "link" (default — email/return a one-time reset link). Owner-of-the-
// deployment authority (admin-gated by the router).
func (a *AdminApp) handleResetPassword(w http.ResponseWriter, r *http.Request, username string) {
	var req struct {
		Mode     string `json:"mode"`
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req) // empty body → default to link
	if _, ok := AuthGetUser(a.db, username); !ok {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	current := AuthCurrentUser(r)
	if req.Mode == "set" {
		if len(req.Password) < 6 || len(req.Password) > 128 {
			http.Error(w, "Password must be 6–128 characters.", http.StatusBadRequest)
			return
		}
		if !AuthAdminSetPassword(a.db, username, req.Password) {
			http.Error(w, "could not set password", http.StatusInternalServerError)
			return
		}
		Log("[admin] user %q set password for %q", current, username)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"status": "password_set"})
		return
	}
	// Link mode.
	link, ok := AuthIssueResetLink(a.db, username)
	if !ok {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	emailed := false
	if EmailConfigured() {
		body := fmt.Sprintf("A password reset was requested for your %s account. Click the link below to set a new password:\n\n%s\n\nIf you didn't expect this, contact an administrator.", ServiceName(), link)
		if SendNotification(username, "["+ServiceName()+"] Password reset", body) == nil {
			emailed = true
		}
	}
	Log("[admin] user %q issued reset link for %q (emailed=%v)", current, username, emailed)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "link_sent", "link": link, "emailed": emailed})
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

// handleVectorStats returns a snapshot of the semantic-search index for the
// admin Vector Index panel: total chunks, how many carry an embedding, how
// many failed to embed, and a per-source breakdown.
func (a *AdminApp) handleVectorStats(w http.ResponseWriter, r *http.Request) {
	db := VectorDB
	if db == nil {
		db = RootDB
	}
	var total, embedded, empty int
	bySource := map[string]int{}
	if db != nil {
		for _, k := range db.Keys(EmbeddedChunks) {
			var c EmbeddedChunk
			if !db.Get(EmbeddedChunks, k, &c) {
				continue
			}
			total++
			if len(c.Vector) > 0 {
				embedded++
			} else {
				empty++
			}
			src := c.Source
			if src == "" {
				src = "(unspecified)"
			}
			bySource[src]++
		}
	}
	srcs := make([]string, 0, len(bySource))
	for s := range bySource {
		srcs = append(srcs, s)
	}
	sort.Strings(srcs)
	var b strings.Builder
	for _, s := range srcs {
		fmt.Fprintf(&b, "%s: %d\n", s, bySource[s])
	}
	byText := strings.TrimSpace(b.String())
	if byText == "" {
		byText = "(no chunks yet)"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"total":          total,
		"embedded":       embedded,
		"empty":          empty,
		"by_source_text": byText,
	})
}

// handleVectorStatsByKind aggregates the index by SOURCE KIND — the prefix
// before the first ':' (research / debate / answer / lcm / collections / …) —
// reporting a document count (distinct full sources) and a chunk count per kind.
// The raw per-source ids are opaque; grouping by kind is what's actually legible.
func (a *AdminApp) handleVectorStatsByKind(w http.ResponseWriter, r *http.Request) {
	db := VectorDB
	if db == nil {
		db = RootDB
	}
	type kindAgg struct {
		docs   map[string]bool
		chunks int
	}
	agg := map[string]*kindAgg{}
	if db != nil {
		for _, k := range db.Keys(EmbeddedChunks) {
			var c EmbeddedChunk
			if !db.Get(EmbeddedChunks, k, &c) {
				continue
			}
			src := c.Source
			if src == "" {
				src = "(unspecified)"
			}
			kind := src
			if i := strings.IndexByte(src, ':'); i >= 0 {
				kind = src[:i]
			}
			ka := agg[kind]
			if ka == nil {
				ka = &kindAgg{docs: map[string]bool{}}
				agg[kind] = ka
			}
			ka.docs[src] = true
			ka.chunks++
		}
	}
	rows := make([]map[string]any, 0, len(agg))
	for kind, ka := range agg {
		rows = append(rows, map[string]any{
			"kind":      kind,
			"label":     vectorKindLabel(kind),
			"documents": len(ka.docs),
			"chunks":    ka.chunks,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i]["chunks"].(int) > rows[j]["chunks"].(int) })
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rows)
}

// vectorKindLabel maps a source-kind prefix to a human label; unknown kinds
// pass through unchanged.
func vectorKindLabel(kind string) string {
	switch strings.ToLower(kind) {
	case "research":
		return "Research reports"
	case "debate":
		return "Debates"
	case "answer":
		return "Answers"
	case "lcm":
		return "Conversation history"
	case "collection", "collections":
		return "Document collections"
	case "skill", "skills":
		return "Skills"
	case "hook", "source-hook", "sourcehook":
		return "Source hooks"
	case "(unspecified)":
		return "Unspecified"
	}
	return kind
}

// handleLLMConfig is the shared GET/POST handler for the Worker (llm_config) and
// Lead (lead_llm_config) LLM sections. worker=true includes the worker-only
// fields (context size, request timeout). The API key is CryptSet only when a
// new value is supplied (blank on the form = keep the existing key) and is
// masked — never returned — on GET. Mirrors the --setup save in config.go.
func (a *AdminApp) handleLLMConfig(w http.ResponseWriter, r *http.Request, table string, worker bool) {
	if a.db == nil {
		http.Error(w, "no database", http.StatusInternalServerError)
		return
	}
	if r.Method == http.MethodPost {
		var req struct {
			Provider             string `json:"provider"`
			Model                string `json:"model"`
			APIKey               string `json:"api_key"`
			Endpoint             string `json:"endpoint"`
			ContextSize          int    `json:"context_size"`
			RequestTimeout       int    `json:"request_timeout_seconds"`
			NativeTools          bool   `json:"native_tools"`
			DisableThinking      bool   `json:"disable_thinking"`
			ThinkingBudget       int    `json:"thinking_budget"`
			NoThinkUseKwarg      bool   `json:"no_think_use_kwarg"`
			NoThinkSendBudget    bool   `json:"no_think_send_budget"`
			NoThinkBudget        int    `json:"no_think_budget"`
			NoThinkPrependSystem bool   `json:"no_think_prepend_system"`
			NoThinkPrependUser   bool   `json:"no_think_prepend_user"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		a.db.Set(table, "provider", req.Provider)
		a.db.Set(table, "model", req.Model)
		a.db.Set(table, "endpoint", req.Endpoint)
		a.db.Set(table, "native_tools", req.NativeTools)
		a.db.Set(table, "disable_thinking", req.DisableThinking)
		a.db.Set(table, "thinking_budget", req.ThinkingBudget)
		a.db.Set(table, "no_think_configured", true)
		a.db.Set(table, "no_think_use_kwarg", req.NoThinkUseKwarg)
		a.db.Set(table, "no_think_send_budget", req.NoThinkSendBudget)
		a.db.Set(table, "no_think_budget", req.NoThinkBudget)
		a.db.Set(table, "no_think_prepend_system", req.NoThinkPrependSystem)
		a.db.Set(table, "no_think_prepend_user", req.NoThinkPrependUser)
		if worker {
			a.db.Set(table, "context_size", req.ContextSize)
			a.db.Set(table, "request_timeout_seconds", req.RequestTimeout)
		}
		if req.APIKey != "" {
			a.db.CryptSet(table, "api_key", req.APIKey)
		}
		// Apply live — rebuild the shared LLMs from the new config so the change
		// takes effect without a restart. Best-effort: on a bad config the prior
		// LLMs stay active and we log it (the config is still saved).
		if err := ReloadLLMs(); err != nil {
			Log("[admin] LLM reload after %s save failed (config saved; prior LLM still active): %v", table, err)
		}
		Log("[admin] user %q updated %s (provider=%q model=%q)", AuthCurrentUser(r), table, req.Provider, req.Model)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// GET — current values; api_key is masked (never returned).
	var provider, model, endpoint string
	var contextSize, reqTimeout, noThinkBudget int
	var nativeTools, disableThinking, ntPrependSys, ntPrependUser bool
	ntKwarg, ntBudget := true, true // defaults when no-think was never configured
	thinkingBudget := 4096          // default budget when unset (matches config.go)
	a.db.Get(table, "provider", &provider)
	a.db.Get(table, "model", &model)
	a.db.Get(table, "endpoint", &endpoint)
	a.db.Get(table, "native_tools", &nativeTools)
	a.db.Get(table, "disable_thinking", &disableThinking)
	a.db.Get(table, "thinking_budget", &thinkingBudget)
	var ntConfigured bool
	a.db.Get(table, "no_think_configured", &ntConfigured)
	if ntConfigured {
		a.db.Get(table, "no_think_use_kwarg", &ntKwarg)
		a.db.Get(table, "no_think_send_budget", &ntBudget)
		a.db.Get(table, "no_think_prepend_system", &ntPrependSys)
		a.db.Get(table, "no_think_prepend_user", &ntPrependUser)
	}
	a.db.Get(table, "no_think_budget", &noThinkBudget)
	out := map[string]any{
		"provider":                provider,
		"model":                   model,
		"endpoint":                endpoint,
		"native_tools":            nativeTools,
		"disable_thinking":        disableThinking,
		"thinking_budget":         thinkingBudget,
		"no_think_use_kwarg":      ntKwarg,
		"no_think_send_budget":    ntBudget,
		"no_think_budget":         noThinkBudget,
		"no_think_prepend_system": ntPrependSys,
		"no_think_prepend_user":   ntPrependUser,
		"api_key":                 "", // masked; blank on the form means "keep existing"
	}
	if worker {
		a.db.Get(table, "context_size", &contextSize)
		a.db.Get(table, "request_timeout_seconds", &reqTimeout)
		out["context_size"] = contextSize
		out["request_timeout_seconds"] = reqTimeout
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func (a *AdminApp) handleStatus(w http.ResponseWriter, r *http.Request) {
	var allow_signup bool
	a.db.Get(WebTable, "allow_signup", &allow_signup)
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
	var allow_signup, ollama_proxy_enabled bool
	var session_days, max_attempts, lockout_minutes, ollama_proxy_port, fetch_cache_quota_mb int
	var service_name, external_url, notify_from string
	a.db.Get(WebTable, "allow_signup", &allow_signup)
	a.db.Get(WebTable, "session_days", &session_days)
	a.db.Get(WebTable, "max_login_attempts", &max_attempts)
	a.db.Get(WebTable, "lockout_minutes", &lockout_minutes)
	a.db.Get(WebTable, "service_name", &service_name)
	a.db.Get(WebTable, "external_url", &external_url)
	a.db.Get(WebTable, "notify_from", &notify_from)
	a.db.Get(WebTable, "ollama_proxy_enabled", &ollama_proxy_enabled)
	a.db.Get(WebTable, "ollama_proxy_port", &ollama_proxy_port)
	a.db.Get(WebTable, "fetch_cache_quota_mb", &fetch_cache_quota_mb)
	if fetch_cache_quota_mb == 0 {
		fetch_cache_quota_mb = 100
	}
	if session_days == 0 {
		session_days = 7
	}
	if max_attempts == 0 {
		max_attempts = 5
	}
	if lockout_minutes == 0 {
		lockout_minutes = 15
	}
	// Build the proxy URL from the configured port and external host (if set).
	var proxy_url string
	if ollama_proxy_port > 0 {
		host := "localhost"
		if external_url != "" {
			// Strip scheme and path, keep just the hostname.
			h := strings.TrimRight(external_url, "/")
			h = strings.TrimPrefix(h, "https://")
			h = strings.TrimPrefix(h, "http://")
			if slash := strings.Index(h, "/"); slash >= 0 {
				h = h[:slash]
			}
			if colon := strings.Index(h, ":"); colon >= 0 {
				h = h[:colon]
			}
			if h != "" {
				host = h
			}
		}
		proxy_url = fmt.Sprintf("http://%s:%d", host, ollama_proxy_port)
	}
	// Only expose proxy config when Ollama is the active provider.
	ollama_active := OllamaBackendFunc != nil
	if ollama_active {
		_, m, _ := OllamaBackendFunc()
		ollama_active = m != ""
	}
	ui_theme := AuthGetUITheme(a.db)
	if ui_theme == "" {
		ui_theme = "indigo"
	}
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]interface{}{
		"allow_signup":         allow_signup,
		"session_days":         session_days,
		"max_login_attempts":   max_attempts,
		"lockout_minutes":      lockout_minutes,
		"service_name":         service_name,
		"external_url":         external_url,
		"notify_from":          notify_from,
		"default_apps":         AuthGetDefaultApps(a.db),
		"ollama_proxy_enabled": ollama_proxy_enabled,
		"ollama_proxy_port":    ollama_proxy_port,
		"ollama_proxy_url":     proxy_url,
		"ollama_active":        ollama_active,
		"fetch_cache_quota_mb": fetch_cache_quota_mb,
		"channel_wake_rules":   AuthGetChannelWakeRules(a.db),
		"ui_theme":             ui_theme,
		"doc_brand":            AuthGetDocBrand(a.db),
		"site_name":            AuthGetSiteName(a.db),
		"timezone":             DeploymentTimezoneName(a.db),
	}
	// Tunables — effective values (stored override or spec default), generated
	// from the registry so a newly-registered knob surfaces here automatically.
	for _, s := range AllTunableSpecs() {
		if s.Kind == KindBool {
			resp[s.Key] = TunableEffectiveValue(s.Key) != 0 // toggle reads a bool
		} else {
			resp[s.Key] = TunableEffectiveValue(s.Key)
		}
	}
	json.NewEncoder(w).Encode(resp)
}

func (a *AdminApp) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AllowSignup        *bool     `json:"allow_signup,omitempty"`
		SessionDays        *int      `json:"session_days,omitempty"`
		MaxLoginAttempts   *int      `json:"max_login_attempts,omitempty"`
		LockoutMinutes     *int      `json:"lockout_minutes,omitempty"`
		ServiceName        *string   `json:"service_name,omitempty"`
		ExternalURL        *string   `json:"external_url,omitempty"`
		NotifyFrom         *string   `json:"notify_from,omitempty"`
		DefaultApps        *[]string `json:"default_apps,omitempty"`
		OllamaProxyEnabled *bool     `json:"ollama_proxy_enabled,omitempty"`
		OllamaProxyPort    *int      `json:"ollama_proxy_port,omitempty"`
		FetchCacheQuotaMB  *int      `json:"fetch_cache_quota_mb,omitempty"`
		ChannelWakeRules   *string   `json:"channel_wake_rules,omitempty"`
		UITheme            *string   `json:"ui_theme,omitempty"`
		DocBrand           *string   `json:"doc_brand,omitempty"`
		SiteName           *string   `json:"site_name,omitempty"`
		Timezone           *string   `json:"timezone,omitempty"`
	}
	// Read the body once: the static settings decode into the typed struct
	// above, the tunables come off the same bytes as a generic map (validated
	// against the registry below).
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	current := AuthCurrentUser(r)
	if req.AllowSignup != nil {
		a.db.Set(WebTable, "allow_signup", *req.AllowSignup)
		Log("[admin] user %q set allow_signup=%v", current, *req.AllowSignup)
	}
	if req.SessionDays != nil && *req.SessionDays >= 1 && *req.SessionDays <= 90 {
		a.db.Set(WebTable, "session_days", *req.SessionDays)
		Log("[admin] user %q set session_days=%d", current, *req.SessionDays)
	}
	if req.MaxLoginAttempts != nil && *req.MaxLoginAttempts >= 1 && *req.MaxLoginAttempts <= 100 {
		a.db.Set(WebTable, "max_login_attempts", *req.MaxLoginAttempts)
		Log("[admin] user %q set max_login_attempts=%d", current, *req.MaxLoginAttempts)
	}
	if req.LockoutMinutes != nil && *req.LockoutMinutes >= 1 && *req.LockoutMinutes <= 1440 {
		a.db.Set(WebTable, "lockout_minutes", *req.LockoutMinutes)
		Log("[admin] user %q set lockout_minutes=%d", current, *req.LockoutMinutes)
	}
	if req.ServiceName != nil {
		a.db.Set(WebTable, "service_name", *req.ServiceName)
		Log("[admin] user %q set service_name=%q", current, *req.ServiceName)
	}
	if req.ExternalURL != nil {
		a.db.Set(WebTable, "external_url", *req.ExternalURL)
		Log("[admin] user %q set external_url=%q", current, *req.ExternalURL)
	}
	if req.NotifyFrom != nil {
		a.db.Set(WebTable, "notify_from", *req.NotifyFrom)
		Log("[admin] user %q set notify_from=%q", current, *req.NotifyFrom)
	}
	if req.DefaultApps != nil {
		AuthSetDefaultApps(a.db, *req.DefaultApps)
		Log("[admin] user %q set default_apps=%v", current, *req.DefaultApps)
	}
	if req.OllamaProxyEnabled != nil {
		a.db.Set(WebTable, "ollama_proxy_enabled", *req.OllamaProxyEnabled)
		Log("[admin] user %q set ollama_proxy_enabled=%v", current, *req.OllamaProxyEnabled)
	}
	if req.OllamaProxyPort != nil && *req.OllamaProxyPort >= 0 && *req.OllamaProxyPort <= 65535 {
		a.db.Set(WebTable, "ollama_proxy_port", *req.OllamaProxyPort)
		Log("[admin] user %q set ollama_proxy_port=%d", current, *req.OllamaProxyPort)
	}
	if req.FetchCacheQuotaMB != nil && *req.FetchCacheQuotaMB >= 0 && *req.FetchCacheQuotaMB <= 10240 {
		a.db.Set(WebTable, "fetch_cache_quota_mb", *req.FetchCacheQuotaMB)
		Log("[admin] user %q set fetch_cache_quota_mb=%d", current, *req.FetchCacheQuotaMB)
	}
	if req.ChannelWakeRules != nil {
		AuthSetChannelWakeRules(a.db, strings.TrimSpace(*req.ChannelWakeRules))
		Log("[admin] user %q updated channel_wake_rules (%d chars)", current, len(strings.TrimSpace(*req.ChannelWakeRules)))
	}
	if req.UITheme != nil {
		// Validate against the theme registry (core/ui) so a typo can't blank
		// the whole UI — and so it stays correct as themes are added.
		if t := strings.TrimSpace(*req.UITheme); ui.IsValidTheme(t) {
			AuthSetUITheme(a.db, t)
			Log("[admin] user %q set ui_theme=%q", current, t)
		} else {
			Log("[admin] user %q tried to set unknown ui_theme=%q — ignored", current, t)
		}
	}
	if req.DocBrand != nil {
		AuthSetDocBrand(a.db, strings.TrimSpace(*req.DocBrand)) // also syncs PDFBranding live
		Log("[admin] user %q set doc_brand=%q", current, strings.TrimSpace(*req.DocBrand))
	}
	if req.SiteName != nil {
		AuthSetSiteName(a.db, strings.TrimSpace(*req.SiteName))
		Log("[admin] user %q set site_name=%q", current, strings.TrimSpace(*req.SiteName))
	}
	if req.Timezone != nil {
		// Validate against the zone resolver so a typo can't strand the
		// deployment in the wrong zone on next boot. Blank clears the override
		// (back to host zone). Stored as the canonical IANA name. Takes effect
		// on restart — reassigning time.Local live would race concurrent
		// formatting.
		if tz := strings.TrimSpace(*req.Timezone); tz == "" {
			a.db.Set(WebTable, TimezoneKey, "")
			Log("[admin] user %q cleared timezone (host zone; applies on restart)", current)
		} else if _, iana, err := ResolveZone(tz); err != nil {
			Log("[admin] user %q tried to set unknown timezone=%q — ignored", current, tz)
		} else {
			a.db.Set(WebTable, TimezoneKey, iana)
			Log("[admin] user %q set timezone=%q (applies on restart)", current, iana)
		}
	}
	// Tunables — validated against the registry, so adding a knob needs no
	// change here. A present numeric key within its spec's [Min, Max] is
	// stored as float64 (TuneInt casts); out-of-range or non-numeric is
	// silently ignored. Invalidate the cache once if anything changed.
	var generic map[string]any
	if json.Unmarshal(raw, &generic) == nil {
		tuned := false
		for _, s := range AllTunableSpecs() {
			v, ok := generic[s.Key]
			if !ok {
				continue
			}
			// Numbers decode as float64; a KindBool toggle POSTs true/false.
			var f float64
			switch val := v.(type) {
			case float64:
				f = val
			case bool:
				if s.Kind != KindBool {
					continue // a bool for a non-bool knob is malformed; ignore
				}
				if val {
					f = 1
				}
			default:
				continue
			}
			if f < s.Min || f > s.Max {
				continue
			}
			a.db.Set(WebTable, s.Key, f)
			Log("[admin] user %q set %s=%g", current, s.Key, f)
			tuned = true
		}
		if tuned {
			InvalidateTunables()
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
}

// handleResetTunables clears every retrieval/limit tunable override so the
// getters fall back to their code defaults. Backs the "Revert to defaults"
// button on the admin Retrieval & limits panel.
func (a *AdminApp) handleResetTunables(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Optional ?category=X scopes the revert to one section's knobs; absent,
	// every tunable resets. Either way the getters fall back to spec defaults.
	cat := r.URL.Query().Get("category")
	for _, s := range AllTunableSpecs() {
		if cat == "" || s.Category == cat {
			a.db.Unset(WebTable, s.Key)
		}
	}
	InvalidateTunables()
	Log("[admin] user %q reverted tunables to defaults (category=%q)", AuthCurrentUser(r), cat)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "reset"})
}

// handleGetCostRates returns the currently configured dollar-rate values
// for LLM + search usage telemetry. Rates are stored in the kvlite DB
// under the "cost_rates" bucket by both --setup and this page; the
// per-run log line formats "est. $X.XXXX" using these values. The
// `configured` flag distinguishes "all zeros because never set" from
// "operator explicitly set everything to zero" so the client can
// render blank inputs in the first case and "0" in the second.
func (a *AdminApp) handleGetCostRates(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	rates := GetCostRates()
	json.NewEncoder(w).Encode(struct {
		CostRates
		Configured bool `json:"configured"`
	}{rates, RatesConfigured()})
}

// handleUpdateCostRates accepts a partial or full CostRates JSON body
// and merges it with the current rates, persisting the result and
// installing it live via SetCostRates. Partial update semantics (each
// field is a pointer) so the form can PUT a single field without
// re-sending the rest.
func (a *AdminApp) handleUpdateCostRates(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WorkerInputPer1K  *float64 `json:"worker_input_per_1k,omitempty"`
		WorkerOutputPer1K *float64 `json:"worker_output_per_1k,omitempty"`
		LeadInputPer1K    *float64 `json:"lead_input_per_1k,omitempty"`
		LeadOutputPer1K   *float64 `json:"lead_output_per_1k,omitempty"`
		SearchPerCall     *float64 `json:"search_per_call,omitempty"`
		ImagePerCall      *float64 `json:"image_per_call,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	rates := GetCostRates()
	current := AuthCurrentUser(r)
	if req.WorkerInputPer1K != nil {
		rates.WorkerInputPer1K = *req.WorkerInputPer1K
		Log("[admin] user %q set worker_input_per_1k=%g", current, *req.WorkerInputPer1K)
	}
	if req.WorkerOutputPer1K != nil {
		rates.WorkerOutputPer1K = *req.WorkerOutputPer1K
		Log("[admin] user %q set worker_output_per_1k=%g", current, *req.WorkerOutputPer1K)
	}
	if req.LeadInputPer1K != nil {
		rates.LeadInputPer1K = *req.LeadInputPer1K
		Log("[admin] user %q set lead_input_per_1k=%g", current, *req.LeadInputPer1K)
	}
	if req.LeadOutputPer1K != nil {
		rates.LeadOutputPer1K = *req.LeadOutputPer1K
		Log("[admin] user %q set lead_output_per_1k=%g", current, *req.LeadOutputPer1K)
	}
	if req.SearchPerCall != nil {
		rates.SearchPerCall = *req.SearchPerCall
		Log("[admin] user %q set search_per_call=%g", current, *req.SearchPerCall)
	}
	if req.ImagePerCall != nil {
		rates.ImagePerCall = *req.ImagePerCall
		Log("[admin] user %q set image_per_call=%g", current, *req.ImagePerCall)
	}
	if err := SaveCostRatesToDB(a.db, rates); err != nil {
		http.Error(w, "save failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	SetCostRates(rates)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rates)
}

// handleCostHistory returns per-day cost aggregation across every
// spend-bearing record type whose package registered a scanner via
// core.RegisterCostRecordScanner. Scanner authors are responsible for
// avoiding double-counting (e.g., skipping records whose Usage is
// already included in a parent record's totals).
//
// Query params:
//
//	days=<n>  trailing window ending today (default 30; 0 = all data)
//
// The chart consumes this directly: each DailyCost row prices the
// day's usage at current CostRates, so rate changes propagate
// immediately without re-scanning.
func (a *AdminApp) handleCostHistory(w http.ResponseWriter, r *http.Request) {
	days := 30
	if s := r.URL.Query().Get("days"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			days = n
		}
	}
	records := CollectAllUsage()
	daily := AggregateDailyCost(records, days)
	// Fold metered source-hook / credential spend (cost hooks) into each day's
	// total. A day that had ONLY external spend (no LLM usage) won't have a row
	// from AggregateDailyCost, so append one for it.
	ext := CostExternalDaily(days)
	seen := map[string]int{}
	for i := range daily {
		seen[daily[i].Date] = i
	}
	for date, cost := range ext {
		if i, ok := seen[date]; ok {
			daily[i].ExternalCost = cost
			daily[i].Cost += cost
		} else {
			daily = append(daily, DailyCost{Date: date, Cost: cost, ExternalCost: cost})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(daily)
}

// handleCostBySource returns each metered source's total spend over the
// window — the admin "Cost by source" breakdown table.
func (a *AdminApp) handleCostBySource(w http.ResponseWriter, r *http.Request) {
	days := 30
	if s := r.URL.Query().Get("days"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			days = n
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"items": CostBySource(days)})
}

// handleListApps returns all registered web apps (excluding admin and
// any app that implements WebHidden() returning true) for the app
// assignment UI. Hidden apps are routing-only surfaces (e.g. the
// /agents/ umbrella that fans out to per-slug exposed agents) — they
// shouldn't appear as togglable items in the per-user / default-apps
// pickers.
func (a *AdminApp) handleListApps(w http.ResponseWriter, r *http.Request) {
	type appInfo struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	type hidden interface{ WebHidden() bool }
	isHidden := func(wa WebApp) bool {
		if h, ok := wa.(hidden); ok && h.WebHidden() {
			return true
		}
		return false
	}
	var apps []appInfo
	for _, wa := range RegisteredWebApps() {
		if wa.WebPath() == "/admin" || isHidden(wa) {
			continue
		}
		apps = append(apps, appInfo{Path: wa.WebPath(), Name: wa.WebName()})
	}
	for _, ag := range RegisteredApps() {
		if wa, ok := ag.(WebApp); ok && wa.WebPath() != "/admin" && !isHidden(wa) {
			apps = append(apps, appInfo{Path: wa.WebPath(), Name: wa.WebName()})
		}
	}
	for _, ag := range RegisteredAgents() {
		if wa, ok := ag.(WebApp); ok && wa.WebPath() != "/admin" && !isHidden(wa) {
			apps = append(apps, appInfo{Path: wa.WebPath(), Name: wa.WebName()})
		}
	}
	// Dynamic grantable apps — surfaces (like orchestrate) that
	// produce one logical "app" per record (e.g. each exposed agent
	// at /agents/<slug>) implement GrantableAppListSource so we can
	// surface them in the user-apps picker. Without this, admins
	// can't grant per-agent access through the standard permission UI.
	for _, ag := range RegisteredApps() {
		if src, ok := ag.(GrantableAppListSource); ok {
			for _, ga := range src.ListGrantableApps() {
				apps = append(apps, appInfo{Path: ga.Path, Name: ga.Name})
			}
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

// handleUpdateUserGroups sets the assigned app-group IDs for a specific user.
// Each group expands to its member apps at access-check time.
func (a *AdminApp) handleUpdateUserGroups(w http.ResponseWriter, r *http.Request, username string) {
	var req struct {
		Groups []string `json:"groups"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if _, ok := AuthGetUser(a.db, username); !ok {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	AuthSetUserGroups(a.db, username, req.Groups)
	current := AuthCurrentUser(r)
	Log("[admin] user %q set groups for %q: %v", current, username, req.Groups)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
}

// --- DB Browser ---

func (a *AdminApp) handleDBTables(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tables := a.db.Tables()
	sort.Strings(tables)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tables)
}

func (a *AdminApp) handleDBKeys(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	table := r.URL.Query().Get("table")
	if table == "" {
		http.Error(w, "table required", http.StatusBadRequest)
		return
	}
	keys := a.db.Keys(table)
	sort.Strings(keys)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(keys)
}

func (a *AdminApp) handleDBRecord(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	table := r.URL.Query().Get("table")
	key := r.URL.Query().Get("key")
	if table == "" || key == "" {
		http.Error(w, "table and key required", http.StatusBadRequest)
		return
	}

	// DBase.Get calls Critical(err) on decode failure, which kills the server.
	// Bypass the wrapper by accessing the underlying kvlite.Store directly so
	// we can probe multiple concrete types without a fatal on type mismatch.
	dbase, ok := a.db.(*DBase)
	if !ok {
		http.Error(w, "unsupported database type", http.StatusInternalServerError)
		return
	}

	val, found := dbProbeRecord(dbase.Store, table, key)
	if !found {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	b, err := json.MarshalIndent(val, "", "  ")
	if err != nil {
		http.Error(w, "marshal error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

// dbProbeRecord tries to decode a kvlite record into the first matching
// primitive type. For complex/struct values it returns a descriptive
// placeholder. Uses Store.Get directly to avoid the Critical(err) wrapper.
func dbProbeRecord(store interface {
	Get(table, key string, output interface{}) (bool, error)
}, table, key string) (interface{}, bool) {
	// Ordered by how commonly these appear in settings/routing/config tables.
	probes := []interface{}{
		new(string),
		new(bool),
		new(int),
		new(int64),
		new(float64),
		new([]string),
		new([]byte),
	}
	for _, ptr := range probes {
		found, err := store.Get(table, key, ptr)
		if !found {
			return nil, false
		}
		if err != nil {
			continue
		}
		// Dereference the pointer to get the concrete value.
		switch v := ptr.(type) {
		case *string:
			return *v, true
		case *bool:
			return *v, true
		case *int:
			return *v, true
		case *int64:
			return *v, true
		case *float64:
			return *v, true
		case *[]string:
			return *v, true
		case *[]byte:
			return *v, true
		}
	}
	// Value exists but is a struct type — return a placeholder rather than crashing.
	return map[string]string{"_type": "struct", "_note": "binary-encoded struct; map probe not supported"}, true
}

// readArtifactBundleBody reads an artifact-bundle request body, accepting
// either the raw bundle JSON or the {"pack":"<json string>"} wrapper a form
// file-field posts. Shared by the import and preview endpoints so both accept
// exactly the same shapes. The 64 MB cap exists for collection bundles: they
// carry a corpus's chunk text (recipe-only bundles are kilobytes), and the
// {"pack"} wrapper's JSON-string escaping roughly doubles the wire size.
func readArtifactBundleBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<26))
	if err != nil {
		return nil, err
	}
	data := bytes.TrimSpace(body)
	var wrap struct {
		Pack string `json:"pack"`
	}
	if json.Unmarshal(data, &wrap) == nil && strings.TrimSpace(wrap.Pack) != "" {
		data = []byte(strings.TrimSpace(wrap.Pack))
	}
	return data, nil
}

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
