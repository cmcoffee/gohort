// Package admin provides an Administrator web panel for managing users,
// viewing system status, and configuring web server settings from the browser.
package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
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

// RegisterRoutes configures the administrative web interface and API endpoints.
// It sets up a sub-mux with routes for user management, system settings,
// cost tracking, and vector statistics, then prepares a gated handler to
// be mounted to the provided mux under the specified prefix.
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

	// Labels of registered cost-record scanners. Lets the admin UI
	// show which apps are feeding the chart without hardcoding any
	// private-app names into the public admin code.
	sub.HandleFunc("/api/cost-sources", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(RegisteredCostSources())
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

	// Scheduled tasks: list pending tasks (GET) or delete by ID (DELETE ?id=xxx).
	sub.HandleFunc("/api/scheduled-tasks", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(ListScheduledTasks(""))
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

	// Snapshot of the vector index for the admin UI: total chunks,
	// embedded vs empty, breakdown by source. One-pass scan; cheap
	// enough to call on page load.
	sub.HandleFunc("/api/vector-stats", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(VectorStats(a.db.Bucket("knowledge")))
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
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(Secure().LoadAudit(name))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(Secure().List())
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
				default:
					http.Error(w, "action must be enable|disable", http.StatusBadRequest)
					return
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			var req struct {
				Name              string   `json:"name"`
				Type              string   `json:"type"`
				AllowedURLPattern string   `json:"allowed_url_pattern"`
				ParamName         string   `json:"param_name"`
				Description       string   `json:"description"`
				RequiresConfirm   bool     `json:"requires_confirm"`
				Secret            string   `json:"secret"`
				AllowedMethods    []string `json:"allowed_methods"`
				DeniedURLPatterns []string `json:"denied_url_patterns"`
				MaxCallsPerDay    int      `json:"max_calls_per_day"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
				return
			}
			c := SecureCredential{
				Name:              strings.TrimSpace(req.Name),
				Type:              strings.TrimSpace(req.Type),
				AllowedURLPattern: strings.TrimSpace(req.AllowedURLPattern),
				ParamName:         strings.TrimSpace(req.ParamName),
				Description:       strings.TrimSpace(req.Description),
				RequiresConfirm:   req.RequiresConfirm,
				AllowedMethods:    req.AllowedMethods,
				DeniedURLPatterns: req.DeniedURLPatterns,
				MaxCallsPerDay:    req.MaxCallsPerDay,
			}
			if err := Secure().Save(c, req.Secret); err != nil {
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
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"pending": LoadPendingTempTools(a.db, username),
				"active":  LoadPersistentTempTools(a.db, username),
			})
		case http.MethodPost:
			action := r.URL.Query().Get("action")
			name := strings.TrimSpace(r.URL.Query().Get("name"))
			if name == "" {
				http.Error(w, "missing name", http.StatusBadRequest)
				return
			}
			var err error
			switch action {
			case "approve":
				err = ApprovePendingTempTool(a.db, username, name)
			case "reject":
				err = RejectPendingTempTool(a.db, username, name)
			default:
				http.Error(w, "action must be approve|reject", http.StatusBadRequest)
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
			if err := DeletePersistentTempTool(a.db, username, name); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Watchers: GET lists all (admin sees every owner's), PATCH updates
	// editable fields on one, POST toggles enabled, DELETE removes.
	sub.HandleFunc("/api/watchers", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			ws := ListWatchers("")
			out := make([]map[string]any, 0, len(ws))
			for _, x := range ws {
				eval := x.Evaluator
				if eval == "" {
					eval = "llm"
				}
				prefix := ""
				if x.DeliveryPrefixSet {
					prefix = x.DeliveryPrefix
				}
				argsJSON, _ := json.Marshal(x.ToolArgs)
				out = append(out, map[string]any{
					"id":                  x.ID,
					"name":                x.Name,
					"owner":               x.Owner,
					"enabled":             x.Enabled,
					"tool_name":           x.ToolName,
					"tool_args":           string(argsJSON),
					"interval_sec":        x.IntervalSec,
					"action_prompt":       x.ActionPrompt,
					"evaluator":           eval,
					"evaluator_script":    x.EvaluatorScript,
					"delivery_prefix":     prefix,
					"delivery_prefix_set": x.DeliveryPrefixSet,
					"target":              x.Target,
					"fire_count":          x.FireCount,
					"last_fired_at":       x.LastFiredAt.Format("2006-01-02T15:04:05Z"),
					"last_result_body":    x.LastResultBody,
					"results":             x.Results,
				})
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(out)

		case http.MethodPatch:
			id := strings.TrimSpace(r.URL.Query().Get("id"))
			if id == "" {
				http.Error(w, "missing id", http.StatusBadRequest)
				return
			}
			var req struct {
				ActionPrompt       *string `json:"action_prompt,omitempty"`
				IntervalSec        *int    `json:"interval_sec,omitempty"`
				Evaluator          *string `json:"evaluator,omitempty"`
				EvaluatorScript    *string `json:"evaluator_script,omitempty"`
				DeliveryPrefix     *string `json:"delivery_prefix,omitempty"`
				DeliveryPrefixUnset bool   `json:"delivery_prefix_unset,omitempty"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			watcher, ok := LoadWatcher(id)
			if !ok {
				http.Error(w, "watcher not found", http.StatusNotFound)
				return
			}
			if req.ActionPrompt != nil {
				watcher.ActionPrompt = *req.ActionPrompt
			}
			if req.IntervalSec != nil {
				if *req.IntervalSec < 60 {
					http.Error(w, "interval_sec must be >= 60", http.StatusBadRequest)
					return
				}
				watcher.IntervalSec = *req.IntervalSec
			}
			if req.Evaluator != nil {
				v := strings.ToLower(strings.TrimSpace(*req.Evaluator))
				if v != "llm" && v != "script" && v != "raw" {
					http.Error(w, "evaluator must be llm|script|raw", http.StatusBadRequest)
					return
				}
				watcher.Evaluator = v
			}
			if req.EvaluatorScript != nil {
				watcher.EvaluatorScript = *req.EvaluatorScript
			}
			if req.DeliveryPrefixUnset {
				watcher.DeliveryPrefixSet = false
				watcher.DeliveryPrefix = ""
			} else if req.DeliveryPrefix != nil {
				watcher.DeliveryPrefixSet = true
				watcher.DeliveryPrefix = *req.DeliveryPrefix
			}
			if err := SaveWatcher(watcher); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			// Re-schedule with the new interval if enabled.
			if watcher.Enabled {
				_ = SetWatcherEnabled(watcher.ID, true)
			}
			w.WriteHeader(http.StatusNoContent)

		case http.MethodPost:
			id := strings.TrimSpace(r.URL.Query().Get("id"))
			action := r.URL.Query().Get("action")
			if id == "" {
				http.Error(w, "missing id", http.StatusBadRequest)
				return
			}
			switch action {
			case "enable":
				if err := SetWatcherEnabled(id, true); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
			case "disable":
				if err := SetWatcherEnabled(id, false); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
			default:
				http.Error(w, "action must be enable|disable", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)

		case http.MethodDelete:
			id := strings.TrimSpace(r.URL.Query().Get("id"))
			if id == "" {
				http.Error(w, "missing id", http.StatusBadRequest)
				return
			}
			if err := DeleteWatcher(id); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
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
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"allow_signup":          allow_signup,
		"session_days":          session_days,
		"max_login_attempts":    max_attempts,
		"lockout_minutes":       lockout_minutes,
		"service_name":          service_name,
		"external_url":          external_url,
		"notify_from":           notify_from,
		"default_apps":          AuthGetDefaultApps(a.db),
		"ollama_proxy_enabled":  ollama_proxy_enabled,
		"ollama_proxy_port":     ollama_proxy_port,
		"ollama_proxy_url":      proxy_url,
		"ollama_active":         ollama_active,
		"fetch_cache_quota_mb":  fetch_cache_quota_mb,
	})
}

func (a *AdminApp) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AllowSignup         *bool     `json:"allow_signup,omitempty"`
		SessionDays         *int      `json:"session_days,omitempty"`
		MaxLoginAttempts    *int      `json:"max_login_attempts,omitempty"`
		LockoutMinutes      *int      `json:"lockout_minutes,omitempty"`
		ServiceName         *string   `json:"service_name,omitempty"`
		ExternalURL         *string   `json:"external_url,omitempty"`
		NotifyFrom          *string   `json:"notify_from,omitempty"`
		DefaultApps         *[]string `json:"default_apps,omitempty"`
		OllamaProxyEnabled  *bool     `json:"ollama_proxy_enabled,omitempty"`
		OllamaProxyPort     *int      `json:"ollama_proxy_port,omitempty"`
		FetchCacheQuotaMB   *int      `json:"fetch_cache_quota_mb,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
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
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(daily)
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
/* user-table-wrap gives the table a horizontal scroll container
   instead of forcing the whole admin page to overflow when a username
   + chips + buttons push past the viewport width on mobile. At narrow
   widths (≤640px) the table switches to a stacked card layout where
   each row becomes a labeled vertical block. */
.user-table-wrap {
  width: 100%; overflow-x: auto; -webkit-overflow-scrolling: touch;
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
  font-size: 0.9rem; word-break: break-word;
}
.user-table tr:last-child td { border-bottom: none; }
@media (max-width: 640px) {
  /* Stack the table: each row becomes a card, each cell a labeled
     block. Keeps all data readable on narrow screens without
     horizontal scroll. */
  .user-table thead { display: none; }
  .user-table, .user-table tbody, .user-table tr, .user-table td {
    display: block; width: 100%;
  }
  .user-table tr {
    border: 1px solid #21262d; border-radius: 6px;
    margin-bottom: 0.5rem; padding: 0.25rem 0.5rem;
  }
  .user-table td {
    border: none; padding: 0.35rem 0.25rem;
  }
  .user-table td:first-child { font-weight: 600; color: #f0f6fc; }
}
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
.actions { display: flex; gap: 0.4rem; flex-wrap: wrap; }
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
.db-browser {
  display: flex; gap: 0.75rem; margin-top: 0.5rem; min-height: 200px;
}
.db-pane {
  display: flex; flex-direction: column; min-width: 0;
}
.db-pane-label {
  font-size: 0.72rem; color: #8b949e; text-transform: uppercase;
  letter-spacing: 0.05em; margin-bottom: 0.35rem; white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.db-list {
  background: #0d1117; border: 1px solid #30363d; border-radius: 6px;
  overflow-y: auto; max-height: 380px; flex: 1;
}
.db-item {
  padding: 0.35rem 0.6rem; font-size: 0.8rem; color: #c9d1d9;
  border-bottom: 1px solid #161b22; cursor: pointer;
  word-break: break-all; line-height: 1.4;
}
.db-item:last-child { border-bottom: none; }
.db-item:hover { background: #21262d; }
.db-item.active { background: #1f3047; color: #79c0ff; }
.db-empty { padding: 0.5rem 0.6rem; font-size: 0.8rem; color: #8b949e; font-style: italic; }
.db-record {
  background: #0d1117; border: 1px solid #30363d; border-radius: 6px;
  padding: 0.6rem 0.75rem; overflow: auto; max-height: 380px;
  font-size: 0.78rem; color: #c9d1d9; margin: 0;
  white-space: pre; font-family: monospace; line-height: 1.5;
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
    <div class="setting-row">
      <label style="font-size:0.9rem;color:#c9d1d9">Fetch Cache Quota (MB per user)</label>
      <span class="setting-desc">Maximum disk used by fetch_url's auto-cache (.fetch_cache/ in each user's workspace). When fetch_url retrieves binary content or text that exceeds the inline cap, it writes the full content here so the LLM can recover via read_file or attach_file. LRU-by-mtime evicts oldest cache files when over quota. Default 100 MB. Range 0–10240.</span>
      <input type="number" id="fetch-cache-quota-mb" min="0" max="10240" value="100"
        style="margin-top:0.3rem;width:6rem;padding:0.4rem 0.6rem;background:#0d1117;border:1px solid #30363d;border-radius:6px;color:#c9d1d9;font-size:0.85rem"
        onchange="updateSetting('fetch_cache_quota_mb', parseInt(this.value)||0)">
    </div>
    <div class="setting-row default-apps-panel">
      <span style="font-size:0.9rem;color:#c9d1d9">Default Apps for New Users</span>
      <span class="setting-desc">Apps assigned to new users who sign up. Users with no custom assignments use these defaults.</span>
      <div id="default-apps-list" class="app-select-panel"></div>
    </div>
  </div>
  <div class="section">
    <h2>Cost Rates</h2>
    <div class="setting-row">
      <span class="setting-desc">Dollar pricing for LLM tokens and search-API calls. Used to compute the per-run cost estimate shown in log lines and on history pages. Rates are per-1,000 tokens for LLMs, per-call for search. Leave blank (or zero) to disable cost estimates — runs will log "rates not configured" instead of a dollar figure.</span>
    </div>
    <div class="setting-row" style="display:flex;gap:1.5rem;flex-wrap:wrap">
      <div>
        <label style="font-size:0.9rem;color:#c9d1d9">Worker Input $/1K</label>
        <span class="setting-desc">Primary LLM input tokens (e.g. Gemini Flash: 0.075).</span>
        <input type="number" id="cost-worker-in" step="0.0001" min="0" placeholder="0"
          style="margin-top:0.3rem;width:8rem;padding:0.4rem 0.6rem;background:#0d1117;border:1px solid #30363d;border-radius:6px;color:#c9d1d9;font-size:0.85rem"
          onchange="updateCostRate('worker_input_per_1k', parseFloat(this.value)||0)">
      </div>
      <div>
        <label style="font-size:0.9rem;color:#c9d1d9">Worker Output $/1K</label>
        <span class="setting-desc">Primary LLM output tokens (e.g. Gemini Flash: 0.30).</span>
        <input type="number" id="cost-worker-out" step="0.0001" min="0" placeholder="0"
          style="margin-top:0.3rem;width:8rem;padding:0.4rem 0.6rem;background:#0d1117;border:1px solid #30363d;border-radius:6px;color:#c9d1d9;font-size:0.85rem"
          onchange="updateCostRate('worker_output_per_1k', parseFloat(this.value)||0)">
      </div>
    </div>
    <div class="setting-row" style="display:flex;gap:1.5rem;flex-wrap:wrap">
      <div>
        <label style="font-size:0.9rem;color:#c9d1d9">Lead Input $/1K</label>
        <span class="setting-desc">Precision LLM input tokens (e.g. Claude Sonnet: 3.00).</span>
        <input type="number" id="cost-lead-in" step="0.0001" min="0" placeholder="0"
          style="margin-top:0.3rem;width:8rem;padding:0.4rem 0.6rem;background:#0d1117;border:1px solid #30363d;border-radius:6px;color:#c9d1d9;font-size:0.85rem"
          onchange="updateCostRate('lead_input_per_1k', parseFloat(this.value)||0)">
      </div>
      <div>
        <label style="font-size:0.9rem;color:#c9d1d9">Lead Output $/1K</label>
        <span class="setting-desc">Precision LLM output tokens (e.g. Claude Sonnet: 15.00).</span>
        <input type="number" id="cost-lead-out" step="0.0001" min="0" placeholder="0"
          style="margin-top:0.3rem;width:8rem;padding:0.4rem 0.6rem;background:#0d1117;border:1px solid #30363d;border-radius:6px;color:#c9d1d9;font-size:0.85rem"
          onchange="updateCostRate('lead_output_per_1k', parseFloat(this.value)||0)">
      </div>
    </div>
    <div class="setting-row" style="display:flex;gap:1.5rem;flex-wrap:wrap">
      <div>
        <label style="font-size:0.9rem;color:#c9d1d9">Search $/call</label>
        <span class="setting-desc">Per-query cost of the search API in use (e.g. Serper: 0.0003).</span>
        <input type="number" id="cost-search" step="0.00001" min="0" placeholder="0"
          style="margin-top:0.3rem;width:8rem;padding:0.4rem 0.6rem;background:#0d1117;border:1px solid #30363d;border-radius:6px;color:#c9d1d9;font-size:0.85rem"
          onchange="updateCostRate('search_per_call', parseFloat(this.value)||0)">
      </div>
      <div>
        <label style="font-size:0.9rem;color:#c9d1d9">Image $/call</label>
        <span class="setting-desc">Per-image generation cost (e.g. Gemini Imagen: 0.04; DALL-E 3 1792x1024 standard: 0.08).</span>
        <input type="number" id="cost-image" step="0.001" min="0" placeholder="0"
          style="margin-top:0.3rem;width:8rem;padding:0.4rem 0.6rem;background:#0d1117;border:1px solid #30363d;border-radius:6px;color:#c9d1d9;font-size:0.85rem"
          onchange="updateCostRate('image_per_call', parseFloat(this.value)||0)">
      </div>
    </div>
  </div>
  <div class="section">
    <h2>Cost History (Last 30 Days)</h2>
    <div class="setting-row">
      <span class="setting-desc">Per-day dollar spend across every app that records usage, for the trailing 30-day window ending today. Days with no activity render as empty bars. Priced at the current Cost Rates above — changing rates updates the chart on next load.</span>
    </div>
    <div class="setting-row" style="display:flex;gap:0.5rem;align-items:center;flex-wrap:wrap;margin-bottom:0">
      <span style="font-size:0.8rem;color:#8b949e">Contributing apps:</span>
      <span id="cost-sources" style="font-size:0.8rem;color:#c9d1d9"></span>
    </div>
    <div id="cost-history-container" style="background:#0d1117;border:1px solid #30363d;border-radius:6px;padding:1rem;margin-top:0.5rem;position:relative">
      <svg id="cost-chart" width="100%" height="220" viewBox="0 0 600 220" preserveAspectRatio="none" style="display:block"></svg>
      <div id="cost-chart-empty" style="display:none;color:#8b949e;text-align:center;padding:60px 20px;font-size:0.9rem">No cost data in the last 30 days.</div>
      <div id="cost-tooltip" style="display:none;position:absolute;background:#161b22;border:1px solid #30363d;border-radius:6px;padding:0.55rem 0.75rem;font-size:0.8rem;color:#c9d1d9;pointer-events:none;z-index:10;line-height:1.4;box-shadow:0 4px 12px rgba(0,0,0,0.4);min-width:180px"></div>
    </div>
    <div id="cost-chart-summary" style="margin-top:0.5rem;font-size:0.85rem;color:#c9d1d9"></div>
  </div>
  <div class="section">
    <h2>LLM Routing</h2>
    <div class="setting-row">
      <span class="setting-desc">Control which LLM handles each pipeline stage. <strong>Lead</strong> uses the precision (remote) LLM. <strong>Worker</strong> uses the local model. <strong>Worker (Thinking)</strong> enables extended reasoning on the local model. <strong>*</strong> marks the stage default. <strong>Budget</strong> sets the thinking token limit for that stage (0 = use the stage default).</span>
    </div>
    <div id="routing-list" style="display:flex;flex-direction:column;gap:0.4rem;margin-top:0.25rem"></div>
  </div>
  <div class="section">
    <h2>Worker LLM Thinking</h2>
    <div class="setting-row">
      <span class="setting-desc">Default thinking settings for the worker (local) LLM. Per-route overrides in the Routing table above take precedence. <strong>Budget 0</strong> = unlimited (model decides). Changes take effect on the next request — no restart required.</span>
    </div>
    <div class="setting-row" style="display:flex;align-items:center;gap:1rem;flex-wrap:wrap">
      <label style="font-size:0.9rem;color:#c9d1d9">Thinking</label>
      <select id="worker-think-enabled" onchange="saveWorkerThinking()" style="background:#161b22;border:1px solid #30363d;color:#c9d1d9;border-radius:4px;padding:4px 8px;font-size:0.85rem;cursor:pointer">
        <option value="off">Off</option>
        <option value="on">On</option>
      </select>
      <label style="font-size:0.9rem;color:#c9d1d9">Default Budget (tokens)</label>
      <input type="number" id="worker-think-budget" min="0" step="1024" placeholder="dynamic"
        style="width:8rem;padding:0.35rem 0.5rem;background:#0d1117;border:1px solid #30363d;border-radius:6px;color:#c9d1d9;font-size:0.85rem"
        onchange="saveWorkerThinking()">
    </div>
  </div>
  <div class="section" id="local-scheduler-section" style="display:none">
    <h2>Local Model Scheduler</h2>
    <div class="setting-row">
      <span class="setting-desc">Control how many concurrent requests each local model backend processes. Default 1 (strict serial). Raise only if your backend truly supports parallel requests. Requests are fair-queued across caller sessions. Requires restart to apply.</span>
    </div>
    <div class="setting-row" style="display:flex;align-items:center;gap:1rem;flex-wrap:wrap;margin-bottom:0.5rem">
      <label style="font-size:0.9rem;color:#c9d1d9;white-space:nowrap">Ollama</label>
      <input type="number" id="sched-ollama-mp" min="1" max="16" step="1" placeholder="1"
        style="width:6rem;padding:0.35rem 0.5rem;background:#0d1117;border:1px solid #30363d;border-radius:6px;color:#c9d1d9;font-size:0.85rem"
        onchange="saveLocalScheduler()">
    </div>
    <div class="setting-row" style="display:flex;align-items:center;gap:1rem;flex-wrap:wrap;margin-bottom:0.5rem">
      <label style="font-size:0.9rem;color:#c9d1d9;white-space:nowrap">llama.cpp</label>
      <input type="number" id="sched-llamacpp-mp" min="1" max="16" step="1" placeholder="1"
        style="width:6rem;padding:0.35rem 0.5rem;background:#0d1117;border:1px solid #30363d;border-radius:6px;color:#c9d1d9;font-size:0.85rem"
        onchange="saveLocalScheduler()">
    </div>
    <div class="setting-row">
      <span style="font-size:0.8rem;color:#8b949e">Changes require restart to apply.</span>
    </div>
  </div>
  <div class="section" id="ollama-proxy-section" style="display:none">
    <h2>Ollama Proxy</h2>
    <div class="setting-row">
      <span class="setting-desc">Expose gohort as a fair-queued Ollama endpoint on a dedicated plain-HTTP port. Point Ollama clients here instead of directly at Ollama — they will see <strong>gohort</strong> as the model name and share the scheduler slot budget with internal pipeline calls. Requires restart to take effect when the port changes.</span>
    </div>
    <div class="setting-row" style="display:flex;align-items:center;gap:0.75rem;flex-wrap:wrap">
      <label style="font-size:0.9rem;color:#c9d1d9;white-space:nowrap">Proxy Port</label>
      <input type="number" id="ollama-proxy-port" min="1" max="65535" placeholder="e.g. 11435"
        style="width:7rem;padding:0.3rem 0.5rem;background:#0d1117;border:1px solid #30363d;border-radius:6px;color:#c9d1d9;font-size:0.9rem"
        onchange="saveOllamaProxyPort(this.value)">
      <span style="font-size:0.8rem;color:#8b949e">Requires restart to apply port change.</span>
    </div>
    <div class="setting-row">
      <label class="toggle-label">
        <input type="checkbox" id="toggle-ollama-proxy" onchange="toggleOllamaProxy(this.checked)">
        <span>Enable Ollama Proxy</span>
      </label>
    </div>
    <div id="ollama-proxy-url-row" class="setting-row" style="display:none">
      <label style="font-size:0.9rem;color:#c9d1d9">Proxy Endpoint</label>
      <span class="setting-desc">Set this as the Ollama base URL in your client. Model name: <code>gohort</code> or <code>gohort:latest</code>.</span>
      <div style="display:flex;align-items:center;gap:0.5rem;margin-top:0.3rem">
        <code id="ollama-proxy-url" style="background:#0d1117;border:1px solid #30363d;border-radius:6px;padding:0.3rem 0.6rem;font-size:0.85rem;color:#79c0ff"></code>
        <button onclick="copyProxyURL()" style="padding:0.3rem 0.6rem;background:#21262d;border:1px solid #30363d;border-radius:6px;color:#c9d1d9;font-size:0.8rem;cursor:pointer">Copy</button>
      </div>
    </div>
  </div>
  <div class="section">
    <h2>Vector Index</h2>
    <div class="setting-row">
      <span class="setting-desc">Snapshot of the semantic-search index. Chunks are written automatically as records (research/debate/answer) are produced.</span>
    </div>
    <div id="vector-stats" style="font-size:0.8rem;color:#8b949e;margin-top:0.25rem;font-family:monospace;line-height:1.6"></div>
  </div>
  <div class="section" id="maintenance-section" style="display:none">
    <h2>Maintenance</h2>
    <div class="setting-row">
      <span class="setting-desc">One-shot repair utilities. Each runs synchronously and may take a while on large record sets.</span>
    </div>
    <div id="maintenance-list"></div>
  </div>
  <div class="section">
    <h2>Scheduled Tasks</h2>
    <div class="setting-row">
      <span class="setting-desc">All pending deferred jobs across every app. Delete a task to cancel it; the scheduler will pick up the next due task automatically.</span>
    </div>
    <div id="scheduled-tasks-list" style="display:flex;flex-direction:column;gap:0.4rem;margin-top:0.5rem"></div>
    <div id="scheduled-tasks-empty" style="font-size:0.85rem;color:#8b949e;margin-top:0.5rem">No pending tasks.</div>
  </div>
  <div class="section">
    <h2>API Credentials</h2>
    <div class="setting-row">
      <span class="setting-desc">Bearer tokens / API keys / basic auth credentials available to the chat LLM as auto-generated <code>call_&lt;name&gt;</code> tools. The LLM never sees the secret value — it's injected server-side. The Allowed URL pattern is the linchpin safety property: requests to URLs that don't match are rejected before any header is attached. <code>*</code> matches up to next slash; <code>**</code> matches arbitrary chars. Audit log shows the last 50 calls per credential.</span>
    </div>
    <div id="secure-api-list" style="display:flex;flex-direction:column;gap:0.5rem;margin-top:0.4rem"></div>
    <div id="secure-api-empty" style="font-size:0.85rem;color:#8b949e">No API credentials registered.</div>

    <h3 style="margin-top:1rem;font-size:0.95rem;color:#c9d1d9">Add credential</h3>
    <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(200px,1fr));gap:0.6rem;margin-top:0.4rem">
      <div>
        <label style="font-size:0.85rem;color:#c9d1d9;display:block;margin-bottom:0.2rem">Name</label>
        <input type="text" id="cred-name" placeholder="github_api"
          style="width:100%;padding:0.4rem 0.6rem;background:#0d1117;border:1px solid #30363d;border-radius:5px;color:#c9d1d9;font-size:0.85rem">
        <span class="setting-desc">snake_case. Becomes <code>call_&lt;name&gt;</code> in the LLM catalog.</span>
      </div>
      <div>
        <label style="font-size:0.85rem;color:#c9d1d9;display:block;margin-bottom:0.2rem">Type</label>
        <select id="cred-type" onchange="onCredTypeChange()"
          style="width:100%;padding:0.4rem 0.6rem;background:#0d1117;border:1px solid #30363d;border-radius:5px;color:#c9d1d9;font-size:0.85rem">
          <option value="bearer">Bearer (Authorization: Bearer ...)</option>
          <option value="header">Custom header</option>
          <option value="query">Query param</option>
          <option value="basic_auth">HTTP Basic (user:pass)</option>
        </select>
      </div>
      <div id="cred-param-wrap" style="display:none">
        <label style="font-size:0.85rem;color:#c9d1d9;display:block;margin-bottom:0.2rem">Header / Param name</label>
        <input type="text" id="cred-param" placeholder="X-Api-Key or api_key"
          style="width:100%;padding:0.4rem 0.6rem;background:#0d1117;border:1px solid #30363d;border-radius:5px;color:#c9d1d9;font-size:0.85rem">
      </div>
    </div>
    <div style="margin-top:0.5rem">
      <label style="font-size:0.85rem;color:#c9d1d9;display:block;margin-bottom:0.2rem">Allowed URL pattern</label>
      <input type="text" id="cred-pattern" placeholder="https://api.github.com/**"
        style="width:100%;max-width:500px;padding:0.4rem 0.6rem;background:#0d1117;border:1px solid #30363d;border-radius:5px;color:#c9d1d9;font-size:0.85rem">
      <span class="setting-desc">Use <code>*</code> for path-segment wildcards or <code>**</code> for full subtree.</span>
    </div>
    <div style="margin-top:0.5rem">
      <label style="font-size:0.85rem;color:#c9d1d9;display:block;margin-bottom:0.2rem">Description (optional)</label>
      <input type="text" id="cred-desc" placeholder="GitHub personal access token (read-only repo scope)"
        style="width:100%;max-width:500px;padding:0.4rem 0.6rem;background:#0d1117;border:1px solid #30363d;border-radius:5px;color:#c9d1d9;font-size:0.85rem">
      <span class="setting-desc">Shown to the LLM in the tool description so it knows what this credential is for.</span>
    </div>
    <div style="margin-top:0.5rem">
      <label style="font-size:0.85rem;color:#c9d1d9;display:block;margin-bottom:0.2rem">Secret value</label>
      <input type="password" id="cred-secret" placeholder="paste token / key / user:pass — leave blank to keep existing"
        style="width:100%;max-width:500px;padding:0.4rem 0.6rem;background:#0d1117;border:1px solid #30363d;border-radius:5px;color:#c9d1d9;font-size:0.85rem">
      <span class="setting-desc">Stored encrypted. The UI never re-displays this value. Leave blank when updating an existing credential to preserve the stored secret — only required on first save.</span>
    </div>
    <div style="margin-top:0.5rem">
      <label class="toggle-label" style="font-size:0.85rem">
        <input type="checkbox" id="cred-confirm">
        <span>Require user confirmation per call</span>
      </label>
      <span class="setting-desc">For high-blast-radius credentials (write APIs, billing). Each LLM call surfaces an approval prompt.</span>
    </div>
    <div style="margin-top:0.5rem">
      <label style="font-size:0.85rem;color:#c9d1d9;display:block;margin-bottom:0.2rem">Allowed HTTP methods</label>
      <input type="text" id="cred-methods" placeholder="GET, POST, PUT (blank = all allowed)"
        style="width:100%;max-width:500px;padding:0.4rem 0.6rem;background:#0d1117;border:1px solid #30363d;border-radius:5px;color:#c9d1d9;font-size:0.85rem">
      <span class="setting-desc">Comma-separated. Blank = no method restriction. Use "GET, HEAD" for read-only credentials. Methods outside the list are rejected before any HTTP request fires.</span>
    </div>
    <div style="margin-top:0.5rem">
      <label style="font-size:0.85rem;color:#c9d1d9;display:block;margin-bottom:0.2rem">Denied URL patterns</label>
      <input type="text" id="cred-denied-urls" placeholder="https://api.vapi.ai/billing/**, https://api.vapi.ai/account/**"
        style="width:100%;max-width:500px;padding:0.4rem 0.6rem;background:#0d1117;border:1px solid #30363d;border-radius:5px;color:#c9d1d9;font-size:0.85rem">
      <span class="setting-desc">Comma-separated glob patterns applied AFTER the allowed-URL pattern. Carve out specific endpoints to block (billing, account-management, expensive operations) while leaving the rest of the API accessible.</span>
    </div>
    <div style="margin-top:0.5rem">
      <label style="font-size:0.85rem;color:#c9d1d9;display:block;margin-bottom:0.2rem">Daily call cap</label>
      <input type="number" id="cred-max-calls" min="0" max="100000" placeholder="0 (unlimited)"
        style="margin-top:0.1rem;width:8rem;padding:0.4rem 0.6rem;background:#0d1117;border:1px solid #30363d;border-radius:6px;color:#c9d1d9;font-size:0.85rem">
      <span class="setting-desc">Successful calls per rolling 24h. Beyond this the dispatcher rejects with a clear "cap reached" error. 0 = unlimited (legacy default). Useful for cost-incurring APIs (Vapi minutes, paid LLMs, Twilio messages).</span>
    </div>
    <div style="margin-top:0.7rem">
      <button onclick="saveCredential()" style="padding:0.45rem 0.9rem;background:#238636;border:1px solid #2ea043;border-radius:6px;color:#fff;font-size:0.85rem;cursor:pointer">Save credential</button>
    </div>
  </div>
  <div class="section">
    <h2>Persistent Tools</h2>
    <div class="setting-row">
      <span class="setting-desc">Tools the LLM has defined via create_temp_tool with persist=true. Each runs a shell command; review the command template carefully before approving. Approving makes the tool available to the LLM in every future chat session for the user. Active tools can be deleted at any time to revoke them.</span>
    </div>
    <h3 style="margin-top:0.6rem;font-size:0.95rem;color:#c9d1d9">Pending approval</h3>
    <div id="pending-tools-list" style="display:flex;flex-direction:column;gap:0.5rem;margin-top:0.4rem"></div>
    <div id="pending-tools-empty" style="font-size:0.85rem;color:#8b949e">No pending tools.</div>
    <h3 style="margin-top:1rem;font-size:0.95rem;color:#c9d1d9">Active</h3>
    <div id="active-tools-list" style="display:flex;flex-direction:column;gap:0.5rem;margin-top:0.4rem"></div>
    <div id="active-tools-empty" style="font-size:0.85rem;color:#8b949e">No active persistent tools.</div>
  </div>
  <div class="section">
    <h2>Watchers</h2>
    <div class="setting-row">
      <span class="setting-desc">Long-running observers the LLM has minted. Each repeats a captured tool call every N seconds and on result change runs an evaluator (llm / script / raw) to decide whether to alert. Edit the script + interval + action_prompt inline; toggle enabled / delete from here too. Tool name &amp; args are read-only — changing what's being watched is a delete + recreate operation in chat.</span>
    </div>
    <div id="watchers-list" style="display:flex;flex-direction:column;gap:0.6rem;margin-top:0.5rem"></div>
    <div id="watchers-empty" style="font-size:0.85rem;color:#8b949e">No watchers defined.</div>
  </div>
  <div class="section">
    <h2>Database Browser</h2>
    <div class="setting-row">
      <span class="setting-desc">Read-only view of the server database. Click a table to list its keys, click a key to inspect the record.</span>
    </div>
    <div class="db-browser">
      <div class="db-pane" style="width:180px;flex-shrink:0">
        <div class="db-pane-label">Tables</div>
        <div class="db-list" id="db-tables-list"><div class="db-empty">Loading…</div></div>
      </div>
      <div class="db-pane" id="db-keys-pane" style="width:200px;flex-shrink:0;display:none">
        <div class="db-pane-label" id="db-keys-label">Keys</div>
        <div class="db-list" id="db-keys-list"></div>
      </div>
      <div class="db-pane" id="db-record-pane" style="flex:1;display:none">
        <div class="db-pane-label" id="db-record-label">Record</div>
        <pre class="db-record" id="db-record-view"></pre>
      </div>
    </div>
  </div>
  <div class="section">
    <h2>User Management</h2>
    <div class="user-table-wrap">
      <table class="user-table">
        <thead><tr>
          <th>Email</th><th>Role</th><th>Apps</th><th>Actions</th>
        </tr></thead>
        <tbody id="user-list"></tbody>
      </table>
    </div>
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
    setField('fetch-cache-quota-mb', s.fetch_cache_quota_mb || 100);
    var defaults = s.default_apps || [];
    renderAppCheckboxes('default-apps-list', defaults, function(apps){
      fetch('api/settings', {
        method: 'PUT',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({default_apps: apps})
      });
    });
    // Ollama proxy section — only shown when Ollama is the active provider.
    if (s.ollama_active) {
      var sec = document.getElementById('ollama-proxy-section');
      if (sec) sec.style.display = '';
      var portEl = document.getElementById('ollama-proxy-port');
      if (portEl && s.ollama_proxy_port) portEl.value = s.ollama_proxy_port;
      var cb = document.getElementById('toggle-ollama-proxy');
      if (cb) cb.checked = !!s.ollama_proxy_enabled;
      var urlEl = document.getElementById('ollama-proxy-url');
      if (urlEl) urlEl.textContent = s.ollama_proxy_url || '';
      var urlRow = document.getElementById('ollama-proxy-url-row');
      if (urlRow) urlRow.style.display = (s.ollama_proxy_enabled && s.ollama_proxy_url) ? '' : 'none';
    }
    // Local scheduler section — shown when Ollama is active (llama.cpp always available).
    loadLocalScheduler();
  });
}

function saveOllamaProxyPort(val) {
  var port = parseInt(val, 10);
  if (isNaN(port) || port < 1 || port > 65535) return;
  updateSetting('ollama_proxy_port', port);
}

function toggleOllamaProxy(enabled) {
  updateSetting('ollama_proxy_enabled', enabled);
  var urlRow = document.getElementById('ollama-proxy-url-row');
  if (urlRow) {
    var urlEl = document.getElementById('ollama-proxy-url');
    urlRow.style.display = (enabled && urlEl && urlEl.textContent) ? '' : 'none';
  }
}

function copyProxyURL() {
  var el = document.getElementById('ollama-proxy-url');
  if (!el) return;
  navigator.clipboard.writeText(el.textContent).catch(function() {
    var ta = document.createElement('textarea');
    ta.value = el.textContent;
    document.body.appendChild(ta);
    ta.select();
    document.execCommand('copy');
    document.body.removeChild(ta);
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

function loadCostRates() {
  fetch('api/cost-rates').then(function(r){
    if (!r.ok) return null;
    return r.json();
  }).then(function(rates){
    if (!rates) return;
    // When rates.configured is true, render every value including zero —
    // $0.00 is a legitimate rate (free local worker). When false, leave
    // inputs blank so "never configured" isn't confused with "set to 0."
    var byId = {
      'cost-worker-in':  rates.worker_input_per_1k,
      'cost-worker-out': rates.worker_output_per_1k,
      'cost-lead-in':    rates.lead_input_per_1k,
      'cost-lead-out':   rates.lead_output_per_1k,
      'cost-search':     rates.search_per_call,
      'cost-image':      rates.image_per_call
    };
    Object.keys(byId).forEach(function(id){
      var el = document.getElementById(id);
      if (!el) return;
      if (rates.configured) {
        el.value = byId[id];
      }
    });
  });
}

function updateCostRate(key, val) {
  var payload = {};
  payload[key] = val;
  fetch('api/cost-rates', {
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

function loadCostSources() {
  fetch('api/cost-sources').then(function(r){
    if (!r.ok) return null;
    return r.json();
  }).then(function(sources){
    var el = document.getElementById('cost-sources');
    if (!el) return;
    if (!sources || sources.length === 0) {
      el.innerHTML = '<span style="color:#8b949e;font-style:italic">none registered</span>';
      return;
    }
    // Render as pill-style badges so the list of contributing apps is
    // visually distinct from the prose above.
    el.innerHTML = sources.map(function(s){
      return '<span style="display:inline-block;padding:0.15rem 0.55rem;background:#21262d;border:1px solid #30363d;border-radius:999px;font-family:monospace;font-size:0.75rem;color:#c9d1d9">' + s + '</span>';
    }).join(' ');
  });
}

function loadRouting() {
  fetch('api/routing').then(function(r){ return r.json(); }).then(function(stages) {
    var el = document.getElementById('routing-list');
    if (!el || !stages || stages.length === 0) return;
    var html = '';
    var tierOpts = [
      {val:'lead',            label:'Lead'},
      {val:'worker',          label:'Worker'},
      {val:'worker (thinking)',label:'Worker (Thinking)'},
    ];
    // Stable sort by group, preserving registration order within each group.
    var groupOrder = [];
    var grouped = {};
    for (var i = 0; i < stages.length; i++) {
      var g = stages[i].group || '';
      if (!grouped[g]) { grouped[g] = []; groupOrder.push(g); }
      grouped[g].push(stages[i]);
    }
    var sorted = [];
    for (var gi = 0; gi < groupOrder.length; gi++) {
      var gs = grouped[groupOrder[gi]];
      for (var si = 0; si < gs.length; si++) sorted.push(gs[si]);
    }
    var lastGroup = '';
    for (var i = 0; i < sorted.length; i++) {
      var s = sorted[i];
      var group = s.group || '';
      if (group !== lastGroup) {
        html += '<div style="margin-top:0.6rem;padding:0.2rem 0 0.2rem;font-size:0.72rem;font-weight:600;letter-spacing:0.06em;text-transform:uppercase;color:#58a6ff;border-bottom:1px solid #1f6feb">'
          + escapeHtml(group) + '</div>';
        lastGroup = group;
      }
      var def = s.default || 'lead';
      var kid = s.key.replace(/\./g, '-');
      var budgetPlaceholder = s.default_budget > 0 ? 'default: ' + s.default_budget : 'dynamic';
      html += '<div style="display:flex;align-items:center;gap:0.6rem;padding:0.35rem 0;border-bottom:1px solid #21262d;flex-wrap:wrap">';
      if (s.private) {
        // Private stage: locked to worker tier only, but still has budget control.
        html += '<span style="font-size:0.75rem;font-weight:600;color:#f0883e;background:#2d1f0e;border:1px solid #5a3a15;border-radius:3px;padding:1px 6px;white-space:nowrap">Private</span>';
        var privateOpts = [
          {val:'worker',          label:'Worker'},
          {val:'worker (thinking)',label:'Worker (Thinking)'},
        ];
        html += '<span style="flex:1;min-width:8rem;font-size:0.82rem;color:#c9d1d9">' + escapeHtml(s.label) + '</span>';
        html += '<select id="rtier-' + kid + '" onchange="saveRouting(\'' + escapeHtml(s.key) + '\')" style="width:11rem;background:#161b22;border:1px solid #30363d;color:#c9d1d9;border-radius:4px;padding:3px 6px;font-size:0.8rem;cursor:pointer">';
        for (var j = 0; j < privateOpts.length; j++) {
          var isSel = privateOpts[j].val === s.value ? ' selected' : '';
          html += '<option value="' + privateOpts[j].val + '"' + isSel + '>' + privateOpts[j].label + '</option>';
        }
        html += '</select>';
        html += '<span style="display:flex;align-items:center;gap:0.3rem">'
          + '<span style="font-size:0.78rem;color:#8b949e">Budget</span>'
          + '<input type="number" id="rbudget-' + kid + '" value="' + (s.think_budget > 0 ? s.think_budget : '') + '" min="0" step="1024" placeholder="' + budgetPlaceholder + '"'
          + ' onchange="saveRouting(\'' + escapeHtml(s.key) + '\')"'
          + ' style="width:5.5rem;background:#0d1117;border:1px solid #30363d;border-radius:4px;color:#c9d1d9;font-size:0.78rem;padding:3px 5px">'
          + '</span>';
      } else {
        html += '<span style="flex:1;min-width:8rem;font-size:0.82rem;color:#c9d1d9">' + escapeHtml(s.label) + '</span>';
        html += '<select id="rtier-' + kid + '" onchange="saveRouting(\'' + escapeHtml(s.key) + '\')" style="width:10rem;background:#161b22;border:1px solid #30363d;color:#c9d1d9;border-radius:4px;padding:3px 6px;font-size:0.8rem;cursor:pointer">';
        for (var j = 0; j < tierOpts.length; j++) {
          var isSel = tierOpts[j].val === s.value ? ' selected' : '';
          var lbl = (tierOpts[j].val === def ? '* ' : '') + tierOpts[j].label;
          html += '<option value="' + tierOpts[j].val + '"' + isSel + '>' + lbl + '</option>';
        }
        html += '</select>';
        html += '<span style="display:flex;align-items:center;gap:0.3rem">'
          + '<span style="font-size:0.78rem;color:#8b949e">Budget</span>'
          + '<input type="number" id="rbudget-' + kid + '" value="' + (s.think_budget > 0 ? s.think_budget : '') + '" min="0" step="1024" placeholder="' + budgetPlaceholder + '"'
          + ' onchange="saveRouting(\'' + escapeHtml(s.key) + '\')"'
          + ' style="width:5.5rem;background:#0d1117;border:1px solid #30363d;border-radius:4px;color:#c9d1d9;font-size:0.78rem;padding:3px 5px">'
          + '</span>';
      }
      html += '</div>';
    }
    el.innerHTML = html;
  });
}

function saveRouting(key) {
  var kid = key.replace(/\./g, '-');
  var tierEl = document.getElementById('rtier-' + kid);
  var budgetEl = document.getElementById('rbudget-' + kid);
  if (!tierEl) return;
  var tier = tierEl.value;
  var budget = budgetEl ? parseInt(budgetEl.value, 10) || 0 : 0;
  fetch('api/routing', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({key: key, value: tier, think_budget: budget}),
  });
}

function loadWorkerThinking() {
  fetch('api/worker-thinking').then(function(r){ return r.json(); }).then(function(d) {
    var en = document.getElementById('worker-think-enabled');
    var bud = document.getElementById('worker-think-budget');
    if (en) en.value = d.enabled ? 'on' : 'off';
    if (bud) bud.value = d.budget > 0 ? d.budget : '';
  });
}

function saveWorkerThinking() {
  var en = document.getElementById('worker-think-enabled');
  var bud = document.getElementById('worker-think-budget');
  fetch('api/worker-thinking', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({
      enabled: en ? en.value === 'on' : false,
      budget: bud ? parseInt(bud.value, 10) || 0 : 0,
    }),
  });
}

function loadLocalScheduler() {
  fetch('api/local-scheduler').then(function(r){ return r.json(); }).then(function(d) {
    var sec = document.getElementById('local-scheduler-section');
    var ollamaInput = document.getElementById('sched-ollama-mp');
    var llamacppInput = document.getElementById('sched-llamacpp-mp');
    if (!sec) return;
    sec.style.display = '';
    if (ollamaInput) ollamaInput.value = d.ollama_max_parallel || 1;
    if (llamacppInput) llamacppInput.value = d.llamacpp_max_parallel || 1;
  });
}

function saveLocalScheduler() {
  var ollamaInput = document.getElementById('sched-ollama-mp');
  var llamacppInput = document.getElementById('sched-llamacpp-mp');
  var ollamaVal = parseInt(ollamaInput.value, 10) || 1;
  var llamacppVal = parseInt(llamacppInput.value, 10) || 1;
  if (ollamaVal < 1) ollamaVal = 1;
  if (ollamaVal > 16) ollamaVal = 16;
  if (llamacppVal < 1) llamacppVal = 1;
  if (llamacppVal > 16) llamacppVal = 16;
  fetch('api/local-scheduler', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({
      ollama_max_parallel:   ollamaVal,
      llamacpp_max_parallel: llamacppVal,
    }),
  });
}

function loadVectorStats() {
  fetch('api/vector-stats').then(function(r){
    if (!r.ok) return null;
    return r.json();
  }).then(function(s){
    var el = document.getElementById('vector-stats');
    if (!el || !s) return;
    if (s.total === 0) {
      el.innerHTML = '<span style="color:#8b949e;font-style:italic">Index empty — chunks will appear as new records are produced.</span>';
      return;
    }
    var parts = [];
    parts.push('Total chunks: ' + s.total);
    parts.push('Embedded: ' + s.embedded);
    if (s.empty > 0) {
      parts.push('<span style="color:#f0883e">Empty (embed failed): ' + s.empty + '</span>');
    }
    var bySrc = [];
    if (s.by_source) {
      Object.keys(s.by_source).sort().forEach(function(k){
        bySrc.push(k + '=' + s.by_source[k]);
      });
    }
    var line1 = parts.join('  |  ');
    var line2 = bySrc.length ? 'By source: ' + bySrc.join(', ') : '';
    el.innerHTML = line1 + (line2 ? '<br>' + line2 : '');
  });
}

function loadMaintenanceFuncs() {
  fetch('api/maintenance').then(function(r){ return r.json(); }).then(function(items){
    if (!items || items.length === 0) return;
    var section = document.getElementById('maintenance-section');
    if (section) section.style.display = '';
    var list = document.getElementById('maintenance-list');
    if (!list) return;
    list.innerHTML = '';
    items.forEach(function(item) {
      var row = document.createElement('div');
      row.style.cssText = 'margin-bottom:1rem';
      var desc = document.createElement('div');
      desc.className = 'setting-desc';
      desc.style.marginBottom = '0.5rem';
      desc.textContent = item.Desc || item.Label;
      var btnRow = document.createElement('div');
      btnRow.style.cssText = 'display:flex;gap:0.75rem;align-items:center;flex-wrap:wrap';
      var btn = document.createElement('button');
      btn.textContent = item.Label;
      btn.style.cssText = 'padding:0.45rem 0.9rem;background:#238636;border:1px solid #2ea043;border-radius:6px;color:#fff;font-size:0.85rem;cursor:pointer';
      var status = document.createElement('span');
      status.style.cssText = 'font-size:0.85rem;color:#8b949e';
      btn.onclick = function() {
        btn.disabled = true;
        btn.style.opacity = '0.6';
        status.textContent = 'Running...';
        status.style.color = '#8b949e';
        fetch('api/maintenance?key=' + encodeURIComponent(item.Label), {method: 'POST'}).then(function(r){
          if (!r.ok) throw new Error('HTTP ' + r.status);
          return r.json();
        }).then(function(result){
          btn.disabled = false;
          btn.style.opacity = '1';
          var n = result.fixed || 0;
          status.textContent = n === 0 ? 'Done — no records needed correction.' : 'Corrected ' + n + ' record' + (n === 1 ? '' : 's') + '.';
          status.style.color = n === 0 ? '#8b949e' : '#2ea043';
        }).catch(function(err){
          btn.disabled = false;
          btn.style.opacity = '1';
          status.textContent = 'Failed: ' + err.message;
          status.style.color = '#f85149';
        });
      };
      btnRow.appendChild(btn);
      btnRow.appendChild(status);
      row.appendChild(desc);
      row.appendChild(btnRow);
      list.appendChild(row);
    });
  }).catch(function(){});
}

function loadCostHistory() {
  fetch('api/cost-history?days=30').then(function(r){
    if (!r.ok) return null;
    return r.json();
  }).then(function(days){
    var svg = document.getElementById('cost-chart');
    var empty = document.getElementById('cost-chart-empty');
    if (!days || days.length === 0) {
      if (svg) svg.style.display = 'none';
      if (empty) empty.style.display = 'block';
      return;
    }
    // Check if every day is zero — no runs in the window even though
    // records might exist further back. Show empty state instead of a
    // flat chart that reads as broken.
    var anyNonZero = false;
    for (var i = 0; i < days.length; i++) {
      if (days[i].run_count > 0) { anyNonZero = true; break; }
    }
    if (!anyNonZero) {
      if (svg) svg.style.display = 'none';
      if (empty) empty.style.display = 'block';
      if (empty) empty.textContent = 'No cost data in the last 30 days.';
      return;
    }
    if (empty) empty.style.display = 'none';
    if (svg) svg.style.display = 'block';
    renderCostChart(days);
  });
}

function formatNum(n) {
  // Thousands-separator for readability in the tooltip.
  return (n || 0).toString().replace(/\B(?=(\d{3})+(?!\d))/g, ',');
}

function showCostTooltip(e) {
  var tip = document.getElementById('cost-tooltip');
  if (!tip) return;
  var d = JSON.parse(e.currentTarget.getAttribute('data-day'));
  var runLabel = d.run_count === 1 ? '1 run' : d.run_count + ' runs';
  tip.innerHTML =
    '<div style="font-weight:600;color:#f0f6fc;margin-bottom:0.35rem">' + d.date + '</div>' +
    '<div style="color:#58a6ff;font-size:1rem;margin-bottom:0.35rem">$' + d.cost.toFixed(4) + '</div>' +
    '<div style="color:#8b949e;margin-bottom:0.35rem">' + runLabel + '</div>' +
    '<div style="border-top:1px solid #30363d;padding-top:0.35rem;font-family:monospace;font-size:0.75rem">' +
    'Worker in: ' + formatNum(d.worker_input) + '<br>' +
    'Worker out: ' + formatNum(d.worker_output) + '<br>' +
    'Lead in:  ' + formatNum(d.lead_input) + '<br>' +
    'Lead out: ' + formatNum(d.lead_output) + '<br>' +
    'Searches: ' + formatNum(d.search_calls) + '<br>' +
    'Images:   ' + formatNum(d.image_calls) +
    '</div>';
  tip.style.display = 'block';
  moveCostTooltip(e);
}

function moveCostTooltip(e) {
  var tip = document.getElementById('cost-tooltip');
  var container = document.getElementById('cost-history-container');
  if (!tip || !container) return;
  var box = container.getBoundingClientRect();
  var tw = tip.offsetWidth;
  var th = tip.offsetHeight;
  // Prefer right-of-cursor; flip to left if it would overflow the
  // container's right edge. Vertical: prefer below-cursor, flip up
  // near the bottom edge.
  var x = e.clientX - box.left + 12;
  var y = e.clientY - box.top + 12;
  if (x + tw + 8 > box.width) x = e.clientX - box.left - tw - 12;
  if (y + th + 8 > box.height) y = e.clientY - box.top - th - 12;
  tip.style.left = Math.max(4, x) + 'px';
  tip.style.top = Math.max(4, y) + 'px';
}

function hideCostTooltip() {
  var tip = document.getElementById('cost-tooltip');
  if (tip) tip.style.display = 'none';
}

function renderCostChart(days) {
  var svg = document.getElementById('cost-chart');
  if (!svg) return;
  while (svg.firstChild) svg.removeChild(svg.firstChild);
  var W = 600, H = 220;
  var padL = 55, padR = 10, padT = 15, padB = 30;
  var plotW = W - padL - padR;
  var plotH = H - padT - padB;
  var ns = 'http://www.w3.org/2000/svg';

  // Determine max for y-axis scaling. Treat a flat-zero dataset as $0.01
  // so the bars aren't invisible when rates haven't been configured.
  var maxCost = 0;
  for (var i = 0; i < days.length; i++) {
    if (days[i].cost > maxCost) maxCost = days[i].cost;
  }
  if (maxCost === 0) maxCost = 0.01;

  // Gridlines + y-axis labels (5 ticks 0..max).
  for (var t = 0; t <= 4; t++) {
    var y = padT + (plotH * t / 4);
    var val = maxCost * (1 - t / 4);
    var line = document.createElementNS(ns, 'line');
    line.setAttribute('x1', padL);
    line.setAttribute('x2', W - padR);
    line.setAttribute('y1', y);
    line.setAttribute('y2', y);
    line.setAttribute('stroke', '#30363d');
    line.setAttribute('stroke-dasharray', '2,3');
    svg.appendChild(line);
    var label = document.createElementNS(ns, 'text');
    label.setAttribute('x', padL - 6);
    label.setAttribute('y', y + 4);
    label.setAttribute('text-anchor', 'end');
    label.setAttribute('font-size', '10');
    label.setAttribute('fill', '#8b949e');
    label.textContent = '$' + val.toFixed(val < 0.1 ? 4 : 2);
    svg.appendChild(label);
  }

  // Bars + per-bar tooltip (native SVG <title>).
  var barW = plotW / days.length;
  var gap = Math.min(4, barW * 0.2);
  // Target ~6 x-axis labels across the window so MM-DD ticks don't
  // collide. labelStep spaces them; a minimum 2-bar gap before the
  // final "today" label avoids the previous modulo tick bumping into
  // it (the 30-bar case put labels at indices 28 and 29 otherwise).
  var labelStep = Math.max(1, Math.floor(days.length / 6));
  var minGapBars = 2;
  var lastLabeledIdx = -minGapBars;
  for (var i = 0; i < days.length; i++) {
    var d = days[i];
    var h = (d.cost / maxCost) * plotH;
    var x = padL + i * barW + gap / 2;
    var y = padT + plotH - h;
    var w = barW - gap;
    var rect = document.createElementNS(ns, 'rect');
    rect.setAttribute('x', x);
    rect.setAttribute('y', y);
    rect.setAttribute('width', w);
    rect.setAttribute('height', h);
    rect.setAttribute('fill', '#58a6ff');
    rect.setAttribute('rx', '1');
    // Widen the hit target for short/zero bars so there's always
    // something clickable under the mouse even on empty days.
    rect.setAttribute('data-day', JSON.stringify(d));
    rect.style.cursor = 'pointer';
    rect.addEventListener('mouseenter', showCostTooltip);
    rect.addEventListener('mousemove', moveCostTooltip);
    rect.addEventListener('mouseleave', hideCostTooltip);
    svg.appendChild(rect);
    // Invisible full-height capture rect — even zero-cost days have a
    // hover target spanning the plot area. Placed on top of the bar so
    // it catches the mouse regardless of bar height.
    var hover = document.createElementNS(ns, 'rect');
    hover.setAttribute('x', x);
    hover.setAttribute('y', padT);
    hover.setAttribute('width', w);
    hover.setAttribute('height', plotH);
    hover.setAttribute('fill', 'transparent');
    hover.setAttribute('data-day', JSON.stringify(d));
    hover.style.cursor = 'pointer';
    hover.addEventListener('mouseenter', showCostTooltip);
    hover.addEventListener('mousemove', moveCostTooltip);
    hover.addEventListener('mouseleave', hideCostTooltip);
    svg.appendChild(hover);

    var isModuloTick = (i % labelStep === 0);
    var isLast = (i === days.length - 1);
    var shouldLabel = false;
    if (isLast) {
      // Always show today's date; if the previous modulo tick is too
      // close, that one gets dropped below instead.
      shouldLabel = true;
    } else if (isModuloTick && (days.length - 1 - i) >= minGapBars) {
      shouldLabel = true;
    }
    if (shouldLabel && i - lastLabeledIdx >= minGapBars) {
      var text = document.createElementNS(ns, 'text');
      text.setAttribute('x', x + w / 2);
      text.setAttribute('y', H - 10);
      text.setAttribute('text-anchor', 'middle');
      text.setAttribute('font-size', '10');
      text.setAttribute('fill', '#8b949e');
      text.textContent = d.date.slice(5);
      svg.appendChild(text);
      lastLabeledIdx = i;
    }
  }

  // Summary line below the chart.
  var total = 0, runs = 0;
  for (var k = 0; k < days.length; k++) {
    total += days[k].cost;
    runs += days[k].run_count;
  }
  var summary = document.getElementById('cost-chart-summary');
  if (summary) {
    var todayCost = days[days.length - 1].cost;
    summary.textContent = 'Last ' + days.length + ' days: $' + total.toFixed(4) +
      ' across ' + runs + ' run' + (runs === 1 ? '' : 's') +
      '  ·  Today: $' + todayCost.toFixed(4) + '.';
  }
}

// --- Scheduled Tasks ---

var _taskExpanded = false;

function loadScheduledTasks() {
  fetch('api/scheduled-tasks').then(function(r){ return r.json(); }).then(function(tasks){
    var list = document.getElementById('scheduled-tasks-list');
    var empty = document.getElementById('scheduled-tasks-empty');
    if (!tasks || !tasks.length) {
      list.innerHTML = '';
      if (empty) empty.style.display = '';
      return;
    }
    if (empty) empty.style.display = 'none';
    _taskExpanded = false;
    var html = '';
    for (var i = 0; i < tasks.length; i++) {
      var t = tasks[i];
      var runAt = t.run_at || '';
      var when = '';
      if (runAt) {
        var d = new Date(runAt);
        when = d.toLocaleString();
      }
      var payloadPreview = '';
      try { payloadPreview = JSON.stringify(t.payload).substring(0, 120); } catch(e) { payloadPreview = '[parse error]'; }
      var fullPayload = escapeHtml(JSON.stringify(t.payload, null, 2));
      html += '<div class="task-row" style="display:flex;align-items:center;gap:0.75rem;padding:0.5rem 0.75rem;background:#0d1117;border:1px solid #30363d;border-radius:6px;font-size:0.85rem;cursor:pointer" onclick="toggleTaskDetail(this, \'' + escapeHtml(t.id) + '\')">'
        + '<span style="color:#8b949e;min-width:160px;font-family:monospace;font-size:0.8rem">' + escapeHtml(when) + '</span>'
        + '<span style="color:#58a6ff;min-width:100px">' + escapeHtml(t.kind) + '</span>'
        + '<span style="flex:1;color:#c9d1d9;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">' + escapeHtml(payloadPreview) + '</span>'
        + '<span style="color:#8b949e;font-size:0.75rem;width:1.2rem;display:inline-block;text-align:center;font-family:monospace" id="chevron-' + escapeHtml(t.id) + '">&gt;</span>'
        + '<button class="btn btn-danger" style="padding:0.25rem 0.5rem;font-size:0.8rem" onclick="event.stopPropagation();deleteScheduledTask(\'' + escapeHtml(t.id) + '\')">Delete</button>'
        + '</div>'
        + '<div class="task-detail" id="detail-' + escapeHtml(t.id) + '" style="display:none;padding:0.75rem;background:#0d1117;border-radius:0 0 6px 6px;margin-top:-6px;margin-bottom:0.4rem;border:1px solid #30363d;border-top:none;cursor:pointer" onclick="toggleTaskDetail(this, \'' + escapeHtml(t.id) + '\')">'
        + '<pre style="margin:0;font-size:0.78rem;color:#c9d1d9;white-space:pre-wrap;word-break:break-all;max-height:300px;overflow-y:auto;font-family:monospace;cursor:pointer;">' + fullPayload + '</pre>'
        + '</div>';
    }
    list.innerHTML = html;
  }).catch(function() {
    var list = document.getElementById('scheduled-tasks-list');
    if (list) list.innerHTML = '<div style="color:#f85149;font-size:0.85rem">Failed to load scheduled tasks.</div>';
  });
}

function toggleTaskDetail(row, id) {
  var detail = document.getElementById('detail-' + id);
  var chevron = document.getElementById('chevron-' + id);
  if (!detail) return;
  if (detail.style.display === 'block') {
    detail.style.display = 'none';
    chevron.textContent = '>';
  } else {
    detail.style.display = 'block';
    chevron.textContent = 'v';
  }
}

function deleteScheduledTask(id) {
  if (!confirm('Cancel this scheduled task?')) return;
  fetch('api/scheduled-tasks?id=' + encodeURIComponent(id), {method: 'DELETE'})
    .then(function(){ loadScheduledTasks(); })
    .catch(function(){ loadScheduledTasks(); });
}

// --- Secure API Credentials ---

function onCredTypeChange() {
  var type = document.getElementById('cred-type').value;
  var wrap = document.getElementById('cred-param-wrap');
  wrap.style.display = (type === 'header' || type === 'query') ? '' : 'none';
}

function saveCredential() {
  var methodsRaw = document.getElementById('cred-methods').value.trim();
  var deniedRaw = document.getElementById('cred-denied-urls').value.trim();
  var maxCallsRaw = document.getElementById('cred-max-calls').value.trim();
  var payload = {
    name: document.getElementById('cred-name').value.trim(),
    type: document.getElementById('cred-type').value,
    allowed_url_pattern: document.getElementById('cred-pattern').value.trim(),
    param_name: document.getElementById('cred-param').value.trim(),
    description: document.getElementById('cred-desc').value.trim(),
    requires_confirm: document.getElementById('cred-confirm').checked,
    secret: document.getElementById('cred-secret').value,
    allowed_methods: methodsRaw ? methodsRaw.split(',').map(function(s){return s.trim().toUpperCase();}).filter(function(s){return s;}) : [],
    denied_url_patterns: deniedRaw ? deniedRaw.split(',').map(function(s){return s.trim();}).filter(function(s){return s;}) : [],
    max_calls_per_day: maxCallsRaw ? parseInt(maxCallsRaw, 10) || 0 : 0
  };
  if (!payload.name || !payload.allowed_url_pattern) {
    alert('Name and allowed_url_pattern are required.');
    return;
  }
  fetch('api/secure-api', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify(payload)
  }).then(function(r){
    if (!r.ok) return r.text().then(function(t){ alert('save failed: ' + t); });
    document.getElementById('cred-name').value = '';
    document.getElementById('cred-pattern').value = '';
    document.getElementById('cred-param').value = '';
    document.getElementById('cred-desc').value = '';
    document.getElementById('cred-secret').value = '';
    document.getElementById('cred-confirm').checked = false;
    loadSecureAPICredentials();
  }).catch(function(e){ alert('save failed: ' + e); });
}

function loadSecureAPICredentials() {
  fetch('api/secure-api').then(function(r){ return r.json(); }).then(function(creds){
    var list = document.getElementById('secure-api-list');
    var empty = document.getElementById('secure-api-empty');
    list.innerHTML = '';
    if (!creds || !creds.length) {
      list.style.display = 'none';
      empty.style.display = '';
      return;
    }
    list.style.display = '';
    empty.style.display = 'none';
    creds.forEach(function(c){ list.appendChild(renderCredentialCard(c)); });
  }).catch(function(){});
}

function renderCredentialCard(c) {
  var card = document.createElement('div');
  card.style.cssText = 'background:#161b22;border:1px solid #30363d;border-radius:6px;padding:0.7rem 0.85rem';
  var head = document.createElement('div');
  head.style.cssText = 'display:flex;justify-content:space-between;align-items:flex-start;gap:0.6rem;margin-bottom:0.3rem;flex-wrap:wrap';
  var titleWrap = document.createElement('div');
  var title = document.createElement('div');
  title.style.cssText = 'font-weight:600;color:#c9d1d9;font-size:0.95rem';
  title.textContent = 'call_' + c.name;
  titleWrap.appendChild(title);
  var meta = document.createElement('div');
  meta.style.cssText = 'font-size:0.75rem;color:#8b949e;margin-top:0.15rem';
  var bits = [c.type];
  if (c.disabled) bits.push('DISABLED');
  if (c.requires_confirm) bits.push('requires confirm');
  if (c.allowed_methods && c.allowed_methods.length) bits.push(c.allowed_methods.join('/'));
  if (c.max_calls_per_day) bits.push('cap ' + c.max_calls_per_day + '/day');
  if (c.denied_url_patterns && c.denied_url_patterns.length) bits.push(c.denied_url_patterns.length + ' denied pattern' + (c.denied_url_patterns.length === 1 ? '' : 's'));
  if (c.last_used_at && c.last_used_at !== '0001-01-01T00:00:00Z') {
    bits.push('last used ' + relTime(c.last_used_at));
  } else {
    bits.push('never used');
  }
  meta.textContent = bits.join(' • ');
  if (c.disabled) {
    card.style.opacity = '0.55';
  }
  titleWrap.appendChild(meta);
  head.appendChild(titleWrap);
  var btns = document.createElement('div');
  btns.style.cssText = 'display:flex;gap:0.4rem';
  var editBtn = document.createElement('button');
  editBtn.textContent = 'Edit';
  editBtn.style.cssText = 'padding:0.35rem 0.7rem;background:#21262d;border:1px solid #30363d;border-radius:5px;color:#c9d1d9;font-size:0.8rem;cursor:pointer';
  editBtn.onclick = function(){ editCredential(c); };
  btns.appendChild(editBtn);
  var toggleBtn = document.createElement('button');
  toggleBtn.textContent = c.disabled ? 'Enable' : 'Disable';
  toggleBtn.style.cssText = 'padding:0.35rem 0.7rem;background:#21262d;border:1px solid ' + (c.disabled ? '#2ea043' : '#d29922') + ';border-radius:5px;color:' + (c.disabled ? '#56d364' : '#d29922') + ';font-size:0.8rem;cursor:pointer';
  toggleBtn.onclick = function(){ toggleCredential(c.name, c.disabled); };
  btns.appendChild(toggleBtn);
  var auditBtn = document.createElement('button');
  auditBtn.textContent = 'Audit';
  auditBtn.style.cssText = 'padding:0.35rem 0.7rem;background:#21262d;border:1px solid #30363d;border-radius:5px;color:#c9d1d9;font-size:0.8rem;cursor:pointer';
  auditBtn.onclick = function(){ showAuditLog(c.name); };
  btns.appendChild(auditBtn);
  var delBtn = document.createElement('button');
  delBtn.textContent = 'Delete';
  delBtn.style.cssText = 'padding:0.35rem 0.7rem;background:#21262d;border:1px solid #f85149;border-radius:5px;color:#f85149;font-size:0.8rem;cursor:pointer';
  delBtn.onclick = function(){ deleteCredential(c.name); };
  btns.appendChild(delBtn);
  head.appendChild(btns);
  card.appendChild(head);

  if (c.description) {
    var desc = document.createElement('div');
    desc.style.cssText = 'font-size:0.85rem;color:#c9d1d9;margin-bottom:0.3rem';
    desc.textContent = c.description;
    card.appendChild(desc);
  }
  var url = document.createElement('div');
  url.style.cssText = 'font-size:0.78rem;color:#8b949e;font-family:ui-monospace,Menlo,monospace';
  url.textContent = 'allowed: ' + c.allowed_url_pattern;
  if (c.param_name) {
    url.textContent += ' • param: ' + c.param_name;
  }
  card.appendChild(url);
  return card;
}

function deleteCredential(name) {
  if (!confirm('Delete credential "' + name + '"? The LLM will lose access immediately.')) return;
  fetch('api/secure-api?name=' + encodeURIComponent(name), {method: 'DELETE'})
    .then(function(r){
      if (!r.ok) return r.text().then(function(t){ alert('delete failed: ' + t); });
      loadSecureAPICredentials();
    });
}

function editCredential(c) {
  // Repopulate the "Add credential" form with the existing record so
  // the operator can tweak the URL pattern, description, type, or
  // confirm-on-call flag without re-entering everything. Secret is
  // intentionally blank — the backend preserves the existing secret
  // when this field is empty on update; paste a new value here only
  // when rotating the key.
  document.getElementById('cred-name').value = c.name || '';
  document.getElementById('cred-type').value = c.type || 'bearer';
  document.getElementById('cred-pattern').value = c.allowed_url_pattern || '';
  document.getElementById('cred-param').value = c.param_name || '';
  document.getElementById('cred-desc').value = c.description || '';
  document.getElementById('cred-confirm').checked = !!c.requires_confirm;
  document.getElementById('cred-secret').value = '';
  document.getElementById('cred-methods').value = (c.allowed_methods || []).join(', ');
  document.getElementById('cred-denied-urls').value = (c.denied_url_patterns || []).join(', ');
  document.getElementById('cred-max-calls').value = c.max_calls_per_day || 0;
  onCredTypeChange();
  // Scroll the form into view so the operator sees what they're editing.
  var anchor = document.getElementById('cred-name');
  if (anchor && anchor.scrollIntoView) anchor.scrollIntoView({behavior: 'smooth', block: 'center'});
  anchor.focus();
}

function toggleCredential(name, currentlyDisabled) {
  var action = currentlyDisabled ? 'enable' : 'disable';
  fetch('api/secure-api?action=' + action + '&name=' + encodeURIComponent(name), {method: 'POST'})
    .then(function(r){
      if (!r.ok) return r.text().then(function(t){ alert(action + ' failed: ' + t); });
      loadSecureAPICredentials();
    });
}

function showAuditLog(name) {
  fetch('api/secure-api?audit=' + encodeURIComponent(name)).then(function(r){ return r.json(); }).then(function(entries){
    if (!entries || !entries.length) {
      alert('No audit entries for ' + name + ' yet.');
      return;
    }
    var lines = entries.map(function(e){
      var s = e.timestamp + '  ' + e.method + ' ' + e.url + '  → ';
      if (e.error) s += 'ERROR: ' + e.error;
      else s += e.status + ' (' + e.response_bytes + ' bytes)';
      return s;
    });
    alert('Audit log for ' + name + ':\n\n' + lines.join('\n'));
  });
}

// --- Persistent Tools ---

function loadPersistentTools() {
  fetch('api/persistent-tools').then(function(r){ return r.json(); }).then(function(d){
    renderPendingTools(d.pending || []);
    renderActiveTools(d.active || []);
  }).catch(function(){
    renderPendingTools([]);
    renderActiveTools([]);
  });
}

function renderPendingTools(pending) {
  var list = document.getElementById('pending-tools-list');
  var empty = document.getElementById('pending-tools-empty');
  list.innerHTML = '';
  if (!pending.length) {
    list.style.display = 'none';
    empty.style.display = '';
    return;
  }
  list.style.display = '';
  empty.style.display = 'none';
  pending.forEach(function(p){
    list.appendChild(renderToolCard(p.tool, {
      meta: 'Requested ' + relTime(p.requested_at),
      pending: true
    }));
  });
}

function renderActiveTools(active) {
  var list = document.getElementById('active-tools-list');
  var empty = document.getElementById('active-tools-empty');
  list.innerHTML = '';
  if (!active.length) {
    list.style.display = 'none';
    empty.style.display = '';
    return;
  }
  list.style.display = '';
  empty.style.display = 'none';
  active.forEach(function(p){
    var meta = 'Approved ' + relTime(p.approved_at);
    if (p.last_used_at && p.last_used_at !== '0001-01-01T00:00:00Z') {
      meta += ' • last used ' + relTime(p.last_used_at);
    } else {
      meta += ' • never used';
    }
    list.appendChild(renderToolCard(p.tool, {meta: meta, pending: false}));
  });
}

function renderToolCard(tool, opts) {
  var card = document.createElement('div');
  card.style.cssText = 'background:#161b22;border:1px solid #30363d;border-radius:6px;padding:0.7rem 0.85rem';
  var head = document.createElement('div');
  head.style.cssText = 'display:flex;justify-content:space-between;align-items:flex-start;gap:0.6rem;margin-bottom:0.4rem;flex-wrap:wrap';
  var titleWrap = document.createElement('div');
  var title = document.createElement('div');
  title.style.cssText = 'font-weight:600;color:#c9d1d9;font-size:0.95rem';
  title.textContent = tool.name;
  titleWrap.appendChild(title);
  var meta = document.createElement('div');
  meta.style.cssText = 'font-size:0.75rem;color:#8b949e;margin-top:0.15rem';
  meta.textContent = opts.meta || '';
  titleWrap.appendChild(meta);
  head.appendChild(titleWrap);
  var btns = document.createElement('div');
  btns.style.cssText = 'display:flex;gap:0.4rem';
  if (opts.pending) {
    var approveBtn = document.createElement('button');
    approveBtn.textContent = 'Approve';
    approveBtn.style.cssText = 'padding:0.35rem 0.7rem;background:#238636;border:1px solid #2ea043;border-radius:5px;color:#fff;font-size:0.8rem;cursor:pointer';
    approveBtn.onclick = function(){ persistentToolAction('approve', tool.name); };
    btns.appendChild(approveBtn);
    var rejectBtn = document.createElement('button');
    rejectBtn.textContent = 'Reject';
    rejectBtn.style.cssText = 'padding:0.35rem 0.7rem;background:#21262d;border:1px solid #30363d;border-radius:5px;color:#c9d1d9;font-size:0.8rem;cursor:pointer';
    rejectBtn.onclick = function(){ persistentToolAction('reject', tool.name); };
    btns.appendChild(rejectBtn);
  } else {
    var delBtn = document.createElement('button');
    delBtn.textContent = 'Delete';
    delBtn.style.cssText = 'padding:0.35rem 0.7rem;background:#21262d;border:1px solid #f85149;border-radius:5px;color:#f85149;font-size:0.8rem;cursor:pointer';
    delBtn.onclick = function(){ deletePersistentTool(tool.name); };
    btns.appendChild(delBtn);
  }
  head.appendChild(btns);
  card.appendChild(head);

  var desc = document.createElement('div');
  desc.style.cssText = 'font-size:0.85rem;color:#c9d1d9;margin-bottom:0.45rem;line-height:1.45';
  desc.textContent = tool.description;
  card.appendChild(desc);

  var cmdLabel = document.createElement('div');
  cmdLabel.style.cssText = 'font-size:0.7rem;color:#8b949e;text-transform:uppercase;letter-spacing:0.04em;margin-bottom:0.15rem';
  cmdLabel.textContent = 'Command template';
  card.appendChild(cmdLabel);

  var cmd = document.createElement('pre');
  cmd.style.cssText = 'background:#0d1117;border:1px solid #30363d;border-radius:4px;padding:0.4rem 0.6rem;font-family:ui-monospace,Menlo,monospace;font-size:0.8rem;color:#c9d1d9;white-space:pre-wrap;word-break:break-word;margin:0';
  cmd.textContent = tool.command_template;
  card.appendChild(cmd);

  if (tool.params && Object.keys(tool.params).length) {
    var paramsLabel = document.createElement('div');
    paramsLabel.style.cssText = 'font-size:0.7rem;color:#8b949e;text-transform:uppercase;letter-spacing:0.04em;margin:0.5rem 0 0.15rem';
    paramsLabel.textContent = 'Params';
    card.appendChild(paramsLabel);
    var pl = document.createElement('div');
    pl.style.cssText = 'font-size:0.78rem;color:#c9d1d9;font-family:ui-monospace,Menlo,monospace;line-height:1.5';
    Object.keys(tool.params).forEach(function(k){
      var p = tool.params[k];
      var line = document.createElement('div');
      line.textContent = k + ' (' + (p.type||'?') + ') — ' + (p.description||'');
      pl.appendChild(line);
    });
    card.appendChild(pl);
  }
  return card;
}

function persistentToolAction(action, name) {
  if (action === 'reject' && !confirm('Reject "' + name + '"? It will be removed from the pending queue.')) return;
  fetch('api/persistent-tools?action=' + action + '&name=' + encodeURIComponent(name), {method: 'POST'})
    .then(function(r){
      if (!r.ok) return r.text().then(function(t){ alert(action + ' failed: ' + t); });
      loadPersistentTools();
    })
    .catch(function(e){ alert(action + ' failed: ' + e); });
}

function deletePersistentTool(name) {
  if (!confirm('Delete "' + name + '"? The LLM will lose access to it in future sessions immediately.')) return;
  fetch('api/persistent-tools?name=' + encodeURIComponent(name), {method: 'DELETE'})
    .then(function(r){
      if (!r.ok) return r.text().then(function(t){ alert('delete failed: ' + t); });
      loadPersistentTools();
    })
    .catch(function(e){ alert('delete failed: ' + e); });
}

function relTime(iso) {
  if (!iso) return 'unknown';
  var t = new Date(iso).getTime();
  if (!t) return iso;
  var s = Math.round((Date.now() - t) / 1000);
  if (s < 60) return s + 's ago';
  if (s < 3600) return Math.round(s/60) + 'm ago';
  if (s < 86400) return Math.round(s/3600) + 'h ago';
  return Math.round(s/86400) + 'd ago';
}

// --- Watchers ---

function loadWatchers() {
  fetch('api/watchers').then(function(r) { return r.json(); }).then(function(d) {
    renderWatchers(d || []);
  }).catch(function() {
    renderWatchers([]);
  });
}

function renderWatchers(ws) {
  var list = document.getElementById('watchers-list');
  var empty = document.getElementById('watchers-empty');
  if (!ws || !ws.length) {
    if (list) list.innerHTML = '';
    if (empty) empty.style.display = 'block';
    return;
  }
  if (empty) empty.style.display = 'none';
  ws.sort(function(a,b) { return (a.name||'').localeCompare(b.name||''); });
  var html = '';
  for (var i = 0; i < ws.length; i++) {
    var w = ws[i];
    var id = w.id;
    var safeID = id.replace(/[^a-zA-Z0-9_-]/g, '_');
    var enabledLabel = w.enabled ? 'Enabled' : 'Disabled';
    var enabledColor = w.enabled ? '#3fb950' : '#8b949e';
    var lastFired = w.last_fired_at && w.last_fired_at !== '0001-01-01T00:00:00Z'
      ? '<span style="color:#8b949e;font-size:0.78rem">last: ' + escapeHtml(w.last_fired_at) + '</span>'
      : '';
    html += '<div style="background:#0d1117;border:1px solid #30363d;border-radius:6px;padding:0.6rem 0.75rem">';
    html += '<div style="display:flex;flex-wrap:wrap;align-items:center;gap:0.6rem;margin-bottom:0.5rem">';
    html += '<strong style="color:#c9d1d9">' + escapeHtml(w.name) + '</strong>';
    html += '<span style="color:' + enabledColor + ';font-size:0.78rem">' + enabledLabel + '</span>';
    html += '<span style="color:#8b949e;font-size:0.78rem">owner=' + escapeHtml(w.owner||'-') + '</span>';
    html += '<span style="color:#8b949e;font-size:0.78rem">tool=' + escapeHtml(w.tool_name||'-') + '</span>';
    html += '<span style="color:#8b949e;font-size:0.78rem">fires=' + w.fire_count + '</span>';
    html += lastFired;
    html += '<span style="margin-left:auto;display:flex;gap:0.4rem">';
    if (w.enabled) {
      html += '<button class="btn" onclick="watcherToggle(\'' + id + '\', false)" style="padding:2px 8px;font-size:0.78rem">Disable</button>';
    } else {
      html += '<button class="btn" onclick="watcherToggle(\'' + id + '\', true)" style="padding:2px 8px;font-size:0.78rem">Enable</button>';
    }
    html += '<button class="btn" onclick="watcherSave(\'' + id + '\')" style="padding:2px 8px;font-size:0.78rem">Save</button>';
    html += '<button class="btn btn-danger" onclick="watcherDelete(\'' + id + '\')" style="padding:2px 8px;font-size:0.78rem">Delete</button>';
    html += '</span>';
    html += '</div>';

    html += '<div style="display:grid;grid-template-columns:auto 1fr;gap:0.4rem 0.6rem;font-size:0.82rem;align-items:start">';

    html += '<label style="color:#8b949e;align-self:center">Args (read-only)</label>';
    html += '<code style="background:#161b22;border:1px solid #30363d;border-radius:4px;padding:3px 6px;color:#c9d1d9;overflow-x:auto;display:block">' + escapeHtml(w.tool_args||'{}') + '</code>';

    html += '<label style="color:#8b949e;align-self:center">Interval (sec)</label>';
    html += '<input type="number" min="60" id="w-int-' + safeID + '" value="' + (w.interval_sec||60) + '" style="background:#161b22;border:1px solid #30363d;border-radius:4px;color:#c9d1d9;padding:3px 6px;width:6rem">';

    html += '<label style="color:#8b949e;align-self:center">Evaluator</label>';
    html += '<select id="w-eval-' + safeID + '" style="background:#161b22;border:1px solid #30363d;border-radius:4px;color:#c9d1d9;padding:3px 6px;width:8rem">';
    var modes = ['llm','script','raw'];
    for (var m = 0; m < modes.length; m++) {
      html += '<option value="' + modes[m] + '"' + (w.evaluator === modes[m] ? ' selected' : '') + '>' + modes[m] + '</option>';
    }
    html += '</select>';

    html += '<label style="color:#8b949e;align-self:start;padding-top:4px">Action prompt</label>';
    html += '<textarea id="w-prompt-' + safeID + '" rows="2" style="background:#161b22;border:1px solid #30363d;border-radius:4px;color:#c9d1d9;padding:4px 6px;font-family:inherit;width:100%;resize:vertical">' + escapeHtml(w.action_prompt||'') + '</textarea>';

    html += '<label style="color:#8b949e;align-self:start;padding-top:4px">Script (python)</label>';
    html += '<textarea id="w-script-' + safeID + '" rows="6" style="background:#0d1117;border:1px solid #30363d;border-radius:4px;color:#c9d1d9;padding:4px 6px;font-family:monospace;font-size:0.78rem;width:100%;resize:vertical">' + escapeHtml(w.evaluator_script||'') + '</textarea>';

    html += '<label style="color:#8b949e;align-self:center">Delivery prefix</label>';
    var prefVal = w.delivery_prefix_set ? w.delivery_prefix : '';
    var prefPlaceholder = w.delivery_prefix_set ? '(empty = no prefix)' : '(default tag)';
    html += '<div style="display:flex;gap:0.4rem;align-items:center">';
    html += '<input type="text" id="w-prefix-' + safeID + '" value="' + escapeHtml(prefVal) + '" placeholder="' + prefPlaceholder + '" style="background:#161b22;border:1px solid #30363d;border-radius:4px;color:#c9d1d9;padding:3px 6px;flex:1">';
    html += '<label style="color:#8b949e;font-size:0.78rem;display:flex;align-items:center;gap:0.3rem"><input type="checkbox" id="w-prefix-set-' + safeID + '"' + (w.delivery_prefix_set ? ' checked' : '') + '> override</label>';
    html += '</div>';

    html += '</div>';

    if (w.last_result_body) {
      html += '<details style="margin-top:0.5rem"><summary style="cursor:pointer;color:#8b949e;font-size:0.78rem">cached response (' + (w.last_result_body.length) + ' chars)</summary>';
      html += '<pre style="background:#0d1117;border:1px solid #30363d;border-radius:4px;padding:6px 8px;color:#c9d1d9;font-size:0.75rem;max-height:200px;overflow:auto;margin-top:0.3rem">' + escapeHtml(w.last_result_body) + '</pre>';
      html += '</details>';
    }
    if (w.results && w.results.length) {
      html += '<details style="margin-top:0.3rem"><summary style="cursor:pointer;color:#8b949e;font-size:0.78rem">recent fires (' + w.results.length + ')</summary>';
      html += '<div style="background:#0d1117;border:1px solid #30363d;border-radius:4px;padding:6px 8px;color:#c9d1d9;font-size:0.75rem;max-height:240px;overflow:auto;margin-top:0.3rem">';
      var rs = w.results.slice().reverse();
      for (var k = 0; k < rs.length && k < 20; k++) {
        var rr = rs[k];
        html += '<div style="margin-bottom:0.5rem;border-bottom:1px solid #21262d;padding-bottom:0.4rem">';
        html += '<div style="color:#8b949e;font-size:0.7rem">' + escapeHtml(rr.timestamp||'') + '</div>';
        if (rr.error) html += '<div style="color:#f85149">ERROR: ' + escapeHtml(rr.error) + '</div>';
        if (rr.reply) html += '<div style="white-space:pre-wrap">' + escapeHtml(rr.reply) + '</div>';
        html += '</div>';
      }
      html += '</div></details>';
    }

    html += '</div>';
  }
  list.innerHTML = html;
}

function watcherSave(id) {
  var safeID = id.replace(/[^a-zA-Z0-9_-]/g, '_');
  var body = {
    interval_sec:     parseInt(document.getElementById('w-int-' + safeID).value, 10) || 60,
    action_prompt:    document.getElementById('w-prompt-' + safeID).value,
    evaluator:        document.getElementById('w-eval-' + safeID).value,
    evaluator_script: document.getElementById('w-script-' + safeID).value,
  };
  var prefSet = document.getElementById('w-prefix-set-' + safeID).checked;
  var prefVal = document.getElementById('w-prefix-' + safeID).value;
  if (prefSet) {
    body.delivery_prefix = prefVal;
  } else {
    body.delivery_prefix_unset = true;
  }
  fetch('api/watchers?id=' + encodeURIComponent(id), {
    method: 'PATCH',
    headers: {'Content-Type':'application/json'},
    body: JSON.stringify(body),
  }).then(function(r) {
    if (!r.ok) {
      r.text().then(function(t) { alert('Save failed: ' + t); });
      return;
    }
    loadWatchers();
  });
}

function watcherToggle(id, enable) {
  fetch('api/watchers?id=' + encodeURIComponent(id) + '&action=' + (enable ? 'enable' : 'disable'), {
    method: 'POST',
  }).then(function(r) {
    if (!r.ok) {
      r.text().then(function(t) { alert('Toggle failed: ' + t); });
      return;
    }
    loadWatchers();
  });
}

function watcherDelete(id) {
  if (!confirm('Delete this watcher?')) return;
  fetch('api/watchers?id=' + encodeURIComponent(id), {
    method: 'DELETE',
  }).then(function(r) {
    if (!r.ok) {
      r.text().then(function(t) { alert('Delete failed: ' + t); });
      return;
    }
    loadWatchers();
  });
}

// --- DB Browser ---

var dbActiveTable = '';

function loadDBTables() {
  fetch('api/db/tables').then(function(r) { return r.json(); }).then(function(tables) {
    var list = document.getElementById('db-tables-list');
    if (!tables || !tables.length) {
      list.innerHTML = '<div class="db-empty">No tables found.</div>';
      return;
    }
    var html = '';
    for (var i = 0; i < tables.length; i++) {
      html += '<div class="db-item" id="dbtbl-' + i + '" onclick="selectDBTable(' + escapeHtml(JSON.stringify(tables[i])) + ', this)">'
            + escapeHtml(tables[i]) + '</div>';
    }
    list.innerHTML = html;
  }).catch(function() {
    var list = document.getElementById('db-tables-list');
    if (list) list.innerHTML = '<div class="db-empty">Failed to load tables.</div>';
  });
}

function selectDBTable(table, el) {
  dbActiveTable = table;
  document.querySelectorAll('#db-tables-list .db-item').forEach(function(e) { e.classList.remove('active'); });
  if (el) el.classList.add('active');
  var keyPane = document.getElementById('db-keys-pane');
  var recPane = document.getElementById('db-record-pane');
  keyPane.style.display = '';
  recPane.style.display = 'none';
  document.getElementById('db-keys-label').textContent = table;
  var keyList = document.getElementById('db-keys-list');
  keyList.innerHTML = '<div class="db-empty">Loading…</div>';
  fetch('api/db/keys?table=' + encodeURIComponent(table)).then(function(r) { return r.json(); }).then(function(keys) {
    if (!keys || !keys.length) {
      keyList.innerHTML = '<div class="db-empty">No keys.</div>';
      return;
    }
    var html = '';
    for (var i = 0; i < keys.length; i++) {
      html += '<div class="db-item" id="dbkey-' + i + '" onclick="loadDBRecord(' + escapeHtml(JSON.stringify(keys[i])) + ', this)">'
            + escapeHtml(keys[i]) + '</div>';
    }
    keyList.innerHTML = html;
  }).catch(function() {
    keyList.innerHTML = '<div class="db-empty">Failed to load keys.</div>';
  });
}

function loadDBRecord(key, el) {
  document.querySelectorAll('#db-keys-list .db-item').forEach(function(e) { e.classList.remove('active'); });
  if (el) el.classList.add('active');
  var recPane = document.getElementById('db-record-pane');
  var view = document.getElementById('db-record-view');
  recPane.style.display = '';
  document.getElementById('db-record-label').textContent = key;
  view.textContent = 'Loading…';
  fetch('api/db/record?table=' + encodeURIComponent(dbActiveTable) + '&key=' + encodeURIComponent(key))
    .then(function(r) {
      if (!r.ok) return r.text().then(function(t) { throw new Error(t); });
      return r.json();
    }).then(function(v) {
      view.textContent = JSON.stringify(v, null, 2);
    }).catch(function(err) {
      view.textContent = 'Error: ' + err.message;
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
    loadCostRates();
    loadCostSources();
    loadCostHistory();
    loadRouting();
    loadWorkerThinking();
    loadVectorStats();
    loadMaintenanceFuncs();
    loadDBTables();
    loadScheduledTasks();
    loadSecureAPICredentials();
    loadPersistentTools();
    loadWatchers();
    // Refresh scheduled tasks every 30s so cancelled tasks disappear without reload.
    setInterval(loadScheduledTasks, 30000);
    // Refresh persistent tools every 30s so newly-queued requests
    // appear without a manual reload.
    setInterval(loadPersistentTools, 30000);
    // Refresh watchers every 30s so fire counts + last_fired_at stay current.
    setInterval(loadWatchers, 30000);
    // Refresh the cost chart every minute so runs that finish while
    // the admin page is open show up without a manual reload. One
    // small JSON fetch per tick — cheap.
    setInterval(loadCostHistory, 60000);
  });
});
`
