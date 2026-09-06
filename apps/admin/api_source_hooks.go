package admin

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// registerSourceHooksRoutes wires the source hooks API under the admin sub-mux.
func (a *AdminApp) registerSourceHooksRoutes(sub *http.ServeMux) {
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

}
