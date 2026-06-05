// Package admin provides an Administrator web panel for managing users,
// viewing system status, and configuring web server settings from the browser.
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
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
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"username": user.Username,
				"admin":    user.Admin,
				"pending":  user.Pending,
				"apps":     apps,
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
			// Single-record fetch for the declarative edit form's Source.
			// Returns one credential as an object (secret never included —
			// the password field stays blank on edit, which keeps the
			// stored secret unchanged unless the admin types a new one).
			if name := strings.TrimSpace(r.URL.Query().Get("name")); name != "" {
				w.Header().Set("Content-Type", "application/json")
				for _, c := range Secure().List() {
					if c.Name == name {
						json.NewEncoder(w).Encode(c)
						return
					}
				}
				http.Error(w, "not found", http.StatusNotFound)
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
				case "restrict", "lock", "hide":
					// Restrict the credential to wrapped tools only —
					// the generic call_<name> direct tool stops
					// appearing in any agent's catalog (chat, phantom,
					// anywhere). Approved temp tools that reference the
					// credential by name still dispatch. ("lock"/"hide"
					// kept as aliases for older clients still POSTing
					// the old verbs.)
					if err := Secure().SetRestricted(name, true); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
				case "open", "unlock", "show":
					// Re-expose the call_<name> direct tool. Used to
					// undo a "restrict" — usually because the LLM
					// needs to improvise against the API again to
					// discover a new shape worth wrapping.
					if err := Secure().SetRestricted(name, false); err != nil {
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
					http.Error(w, "action must be enable|disable|restrict|open|test", http.StatusBadRequest)
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
				// OAuth2 (type == "oauth2").
				Grant       string `json:"grant"`
				TokenURL    string `json:"token_url"`
				ClientID    string `json:"client_id"`
				Scope       string `json:"scope"`
				JWTIssuer   string `json:"jwt_issuer"`
				JWTSubject  string `json:"jwt_subject"`
				JWTAudience string `json:"jwt_audience"`
				JWTKeyID    string `json:"jwt_key_id"`
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
				Grant:             strings.TrimSpace(req.Grant),
				TokenURL:          strings.TrimSpace(req.TokenURL),
				ClientID:          strings.TrimSpace(req.ClientID),
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
					if c.ClientID == "" {
						c.ClientID = existing.ClientID
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
				default:
					http.Error(w, "action must be expose|hide", http.StatusBadRequest)
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
			// convention — re-saving the form shouldn't wipe the secret).
			authKey := strings.TrimSpace(req.AuthKey)
			if authKey == "" || authKey == "(configured)" {
				for _, h := range RegisteredSourceHooks() {
					if strings.EqualFold(h.Name, name) {
						authKey = h.AuthKey
						break
					}
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
				ExposeToLLM:     req.ExposeToLLM,
				ToolName:        strings.TrimSpace(req.ToolName),
				ToolDescription: strings.TrimSpace(req.ToolDescription),
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
			// Persistent + pending pools are stored per-user at the
			// kvlite layer, but admins see the whole deployment —
			// the tool-groups registry walks all users; this page
			// should match. Each entry surfaces with an "owner"
			// badge so the admin can tell whose tool is whose.
			store := RootDB
			if store == nil {
				store = a.db
			}
			type pendingWithOwner struct {
				Owner string `json:"owner"`
				PendingTempTool
			}
			type activeWithOwner struct {
				Owner string `json:"owner"`
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
						active = append(active, activeWithOwner{Owner: u, PersistentTempTool: p})
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
			type bundledWithOwner struct {
				Owner   string   `json:"owner"`
				Agent   string   `json:"agent"`
				AgentID string   `json:"agent_id"`
				Tool    TempTool `json:"tool"`
			}
			var bundled []bundledWithOwner
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
						ID    string
						Name  string
						Tools []TempTool
					}
					if !udb.Get("orchestrate_agents", key, &rec) {
						continue
					}
					for _, t := range rec.Tools {
						bundled = append(bundled, bundledWithOwner{
							Owner: u.Username, Agent: rec.Name, AgentID: rec.ID, Tool: t,
						})
					}
				}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"pending": pending,
				"active":  active,
				"bundled": bundled,
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

	// Watchers: GET lists all (admin sees every owner's), PATCH updates
	// editable fields on one, POST toggles enabled, DELETE removes.
	sub.HandleFunc("/api/watchers", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			// Look up the next scheduled fire per watcher so the UI can
			// show "next fire" without each row paying a separate fetch.
			nextFire := map[string]string{}
			for _, t := range ListScheduledTasks("watcher.poll") {
				var p struct {
					WatcherID string `json:"watcher_id"`
				}
				if json.Unmarshal(t.Payload, &p) == nil && p.WatcherID != "" {
					nextFire[p.WatcherID] = t.RunAt
				}
			}
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
				lastFiredISO := ""
				if !x.LastFiredAt.IsZero() {
					lastFiredISO = x.LastFiredAt.UTC().Format(time.RFC3339)
				}
				createdISO := ""
				if !x.CreatedAt.IsZero() {
					createdISO = x.CreatedAt.UTC().Format(time.RFC3339)
				}
				status := "idle"
				lastTrigger := ""
				lastReply := ""
				lastError := ""
				if n := len(x.Results); n > 0 {
					last := x.Results[n-1]
					lastTrigger = truncStr(last.Trigger, 240)
					lastReply = truncStr(last.Reply, 240)
					lastError = last.Error
					if last.Error != "" {
						status = "error"
					} else {
						status = "ok"
					}
				}
				if !x.Enabled {
					status = "disabled"
				}
				out = append(out, map[string]any{
					"id":                  x.ID,
					"name":                x.Name,
					"description":         x.Description,
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
					"created_at":          createdISO,
					"last_fired_at":       lastFiredISO,
					"next_fire_at":        nextFire[x.ID],
					"last_result_body":    x.LastResultBody,
					"results_count":       len(x.Results),
					"status":              status,
					"last_trigger":        lastTrigger,
					"last_reply":          lastReply,
					"last_error":          lastError,
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
				Description         *string `json:"description,omitempty"`
				ActionPrompt        *string `json:"action_prompt,omitempty"`
				IntervalSec         *int    `json:"interval_sec,omitempty"`
				Evaluator           *string `json:"evaluator,omitempty"`
				EvaluatorScript     *string `json:"evaluator_script,omitempty"`
				DeliveryPrefix      *string `json:"delivery_prefix,omitempty"`
				DeliveryPrefixUnset bool    `json:"delivery_prefix_unset,omitempty"`
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
			if req.Description != nil {
				watcher.Description = *req.Description
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

	// Per-watcher fire history. Returns the WatcherResult ring buffer
	// newest-first so the admin UI can render it as a sub-table inside
	// the watcher's expand panel.
	sub.HandleFunc("/api/watchers/results", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		watcher, ok := LoadWatcher(id)
		if !ok {
			http.Error(w, "watcher not found", http.StatusNotFound)
			return
		}
		out := make([]map[string]any, 0, len(watcher.Results))
		for i := len(watcher.Results) - 1; i >= 0; i-- {
			res := watcher.Results[i]
			status := "ok"
			if res.Error != "" {
				status = "error"
			}
			out = append(out, map[string]any{
				"idx":         i,
				"timestamp":   res.Timestamp.UTC().Format(time.RFC3339),
				"trigger":     res.Trigger,
				"reply":       res.Reply,
				"reply_short": truncStr(res.Reply, 160),
				"error":       res.Error,
				"status":      status,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
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
	})
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

