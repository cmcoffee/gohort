package admin

import (
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/sourcehooks"
)

// registerSourceHooksRoutes wires the source hooks API under the admin sub-mux.
func (a *AdminApp) registerSourceHooksRoutes(sub *http.ServeMux) {
	// Source hooks — curated external sources (PubMed, OpenAlex, EDGAR,
	// custom APIs / RAG). GET lists; POST upserts (or ?action=expose|hide
	// toggles LLM-tool exposure); DELETE removes. A hook with
	// expose_to_llm=true is auto-surfaced as a per-hook agent tool
	// (BuildSourceHookAgentToolDefs, wired in the orchestrate runner).
	sub.HandleFunc("/api/source-hooks", jsonapi.Methods{
		"GET":    jsonapi.Get(a.db, a.handleSourceHooks, a.gate()),
		"POST":   a.sourceHookPost(),
		"DELETE": jsonapi.Delete(a.db, a.handleSourceHookDelete, a.gate()),
	}.Serve)

}

// findSourceHook is the hook named, matched without regard to case.
func findSourceHook(name string) (SourceHook, bool) {
	for _, h := range RegisteredSourceHooks() {
		if strings.EqualFold(h.Name, name) {
			return h, true
		}
	}
	return SourceHook{}, false
}

// handleSourceHooks is the hooks as the table shows them, or with ?name= one
// hook in its editable shape for the Edit form's pre-fill, its secret blanked
// (the form's auth_key Help says leave blank to keep it).
func (a *AdminApp) handleSourceHooks(c *jsonapi.Call) (any, error) {
	if one := strings.TrimSpace(c.R.URL.Query().Get("name")); one != "" {
		h, ok := findSourceHook(one)
		if !ok {
			return nil, jsonapi.NotFound("hook not found")
		}
		h.AuthKey = "" // never expose the stored secret to the edit form
		return h, nil
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
		// Picked as the search engine, it answers web_search and
		// nothing else: no topic routing, no tool of its own.
		if sourcehooks.IsSearchEngine(h) {
			eff = "web_search (the search engine)"
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
	return out, nil
}

// sourceHookPost tells /api/source-hooks' two POST shapes apart: a toggle
// names ?action=, the upsert sends the whole hook.
func (a *AdminApp) sourceHookPost() http.HandlerFunc {
	toggle := jsonapi.Post(a.db, a.handleSourceHookToggle, a.gate())
	save := jsonapi.Post(a.db, a.handleSourceHookSave, a.gate())
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("action") != "" {
			toggle(w, r)
			return
		}
		save(w, r)
	}
}

// handleSourceHookToggle exposes, hides, enables or disables the ?name= hook.
func (a *AdminApp) handleSourceHookToggle(c *jsonapi.Call, _ struct{}) (any, error) {
	q := c.R.URL.Query()
	action := q.Get("action")
	name := strings.TrimSpace(q.Get("name"))
	if name == "" {
		return nil, jsonapi.BadRequest("missing name")
	}
	h, ok := findSourceHook(name)
	if !ok {
		return nil, jsonapi.NotFound("hook not found")
	}
	target := &h
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
		return nil, jsonapi.BadRequest("action must be expose|hide|enable|disable")
	}
	SaveSourceHook(a.db, *target)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// sourceHookSave is the source hook form.
type sourceHookSave struct {
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

// handleSourceHookSave creates or saves a hook.
func (a *AdminApp) handleSourceHookSave(c *jsonapi.Call, req sourceHookSave) (any, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, jsonapi.BadRequest("name required")
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
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleSourceHookDelete removes the ?name= hook.
func (a *AdminApp) handleSourceHookDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	name := strings.TrimSpace(c.R.URL.Query().Get("name"))
	if name == "" {
		return nil, jsonapi.BadRequest("missing name")
	}
	DeleteSourceHook(a.db, name)
	return jsonapi.Empty(http.StatusNoContent), nil
}
