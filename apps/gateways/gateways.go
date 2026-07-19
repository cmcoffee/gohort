// Package gateways is the per-user "capability plane" — the surfaces through
// which a user's agents reach outward: their own API credentials, the tools
// they've had built, the global-tool catalog they opt into, and (rendered here,
// served from /account for OAuth redirect-URI stability) their identity
// connections. It is the user-namespace counterpart to the admin's global
// credential/tool management: everything here is scoped to the calling user.
//
// Reached as its own dashboard tile. Account keeps identity + preferences
// (password, timezone, inbound API keys); Gateways owns outward reach.
package gateways

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

func init() { RegisterApp(new(Gateways)) }

type Gateways struct {
	AppCore
}

func (T Gateways) Name() string         { return "gateways" }
func (T Gateways) SystemPrompt() string { return "" }
func (T Gateways) Desc() string         { return "Apps: your agents' outward reach — credentials, tools, connections." }
func (T *Gateways) Init() error         { return T.Flags.Parse() }
func (T *Gateways) Main() error {
	Log("gateways is a dashboard-only app. Start with: gohort serve")
	return nil
}

func (T *Gateways) WebPath() string { return "/gateways" }
func (T *Gateways) WebName() string { return "Gateways" }
func (T *Gateways) WebDesc() string {
	return "Credentials, tools, and connections your agents use to reach the outside world."
}

// HubTab puts Gateways on the shared top-nav tab row alongside Agents, Bridges,
// and Knowledge — it's the per-user capability surface those agents draw on, so
// it belongs in the same hub. Ordered after the others.
func (T *Gateways) HubTab() (string, int) { return "Gateways", 40 }

func (T *Gateways) Routes() {
	T.HandleFunc("/api/credentials", T.handleCredentials)
	T.HandleFunc("/api/tools", T.handleUserTools)
	T.HandleFunc("/api/global-tools", T.handleGlobalTools)
	T.HandleFunc("/", T.servePage)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// handleCredentials is the user's OWN API-credential CRUD — the per-user
// counterpart to the admin credential list. Every op is scoped to
// Owner == currentUser: the store keys user-owned creds by (owner, name)
// (Secure().ListUser / LoadUser / DeleteUser / Save with Owner set), so these
// live in the user's namespace and never appear on the admin page. Only the
// simple key-based types are offered here; OAuth2 stays admin-managed. Secrets
// are never returned — GET reports has_secret only.
func (T *Gateways) handleCredentials(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		type row struct {
			Name            string `json:"name"`
			Type            string `json:"type"`
			BaseURL         string `json:"base_url"`
			ParamName       string `json:"param_name,omitempty"`
			Description     string `json:"description,omitempty"`
			RequiresConfirm bool   `json:"requires_confirm"`
			HasSecret       bool   `json:"has_secret"`
		}
		rows := []row{}
		for _, c := range Secure().ListUser(user) {
			rows = append(rows, row{
				Name: c.Name, Type: c.Type, BaseURL: c.BaseURL, ParamName: c.ParamName,
				Description: c.Description, RequiresConfirm: c.RequiresConfirm,
				HasSecret: c.Type != SecureCredNone,
			})
		}
		writeJSON(w, rows)
	case http.MethodPost:
		var body struct {
			Name            string `json:"name"`
			Type            string `json:"type"`
			BaseURL         string `json:"base_url"`
			ParamName       string `json:"param_name"`
			Description     string `json:"description"`
			Secret          string `json:"secret"`
			RequiresConfirm bool   `json:"requires_confirm"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		// Only the simple key-based types are user-managed; OAuth2 (and its
		// admin-only client config) stays on the admin surface.
		switch body.Type {
		case SecureCredBearer, SecureCredHeader, SecureCredQuery, SecureCredBasicAuth, SecureCredNone:
		default:
			http.Error(w, "type must be bearer, header, query, basic_auth, or none", http.StatusBadRequest)
			return
		}
		// Owner = the calling user: Save keys this into the user's namespace, so
		// it can never touch a global (admin) credential of the same name.
		c := SecureCredential{
			Name:            strings.TrimSpace(body.Name),
			Type:            body.Type,
			BaseURL:         strings.TrimSpace(body.BaseURL),
			ParamName:       strings.TrimSpace(body.ParamName),
			Description:     strings.TrimSpace(body.Description),
			RequiresConfirm: body.RequiresConfirm,
			Owner:           user,
		}
		if err := Secure().Save(c, body.Secret); err != nil {
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
		if err := Secure().DeleteUser(user, name); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleUserTools is the user's OWN persistent-tool surface — the counterpart to
// the admin persistent-tools page, scoped to the calling user's pool. GET lists
// the user's active tools (authored via chat/Builder); DELETE removes one (the
// break-glass "this tool misbehaves, drop it" control). Authoring stays in chat —
// a tool is a script or API definition, not a hand-filled form — so this surface
// is view + delete, not create.
func (T *Gateways) handleUserTools(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		type row struct {
			Name        string `json:"name"`
			Description string `json:"description,omitempty"`
			Mode        string `json:"mode,omitempty"`
			Credential  string `json:"credential,omitempty"`
			Missing     bool   `json:"missing"`
			Shared      bool   `json:"shared"`
			LastUsed    string `json:"last_used,omitempty"`
		}
		rows := []row{}
		for _, p := range LoadPersistentTempTools(AuthDB(), user) {
			// Dependency check resolves in the USER's namespace (a tool may lean
			// on the user's own credential, which the global-only CredentialStatus
			// wouldn't find).
			missing := false
			if cred := strings.TrimSpace(p.Tool.Credential); cred != "" && !strings.EqualFold(cred, "no_auth") {
				if _, found := Secure().Resolve(cred, user); !found {
					missing = true
				}
			}
			last := ""
			if !p.LastUsedAt.IsZero() {
				last = p.LastUsedAt.Format("2006-01-02")
			}
			rows = append(rows, row{
				Name: p.Tool.Name, Description: p.Tool.Description, Mode: p.Tool.Mode,
				Credential: p.Tool.Credential, Missing: missing, Shared: p.Shared, LastUsed: last,
			})
		}
		writeJSON(w, rows)
	case http.MethodDelete:
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		if name == "" {
			http.Error(w, "missing name", http.StatusBadRequest)
			return
		}
		if err := DeletePersistentTempTool(AuthDB(), user, name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleGlobalTools is the global-tool OPT-IN catalog. Global (Shared) tools are
// published by an admin and no longer auto-load; a user picks the ones they want
// from this catalog and they load for that user's agents. GET lists the catalog
// with an adopted flag; POST {name, adopt} adds/removes one from the user's
// adoption list. Enforcement (which shared tools actually load) lives in the
// runner + operator-wake tool-load paths.
func (T *Gateways) handleGlobalTools(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		adopted := LoadAdoptedGlobalTools(AuthDB(), user)
		type row struct {
			Name        string `json:"name"`
			Description string `json:"description,omitempty"`
			Mode        string `json:"mode,omitempty"`
			Credential  string `json:"credential,omitempty"`
			Adopted     bool   `json:"adopted"`
			Missing     bool   `json:"missing"`
		}
		rows := []row{}
		for _, p := range LoadSharedPersistentTempTools(AuthDB()) {
			missing := false
			if cred := strings.TrimSpace(p.Tool.Credential); cred != "" && !strings.EqualFold(cred, "no_auth") {
				if _, found := Secure().Resolve(cred, user); !found {
					missing = true
				}
			}
			rows = append(rows, row{
				Name: p.Tool.Name, Description: p.Tool.Description, Mode: p.Tool.Mode,
				Credential: p.Tool.Credential, Adopted: adopted[p.Tool.Name], Missing: missing,
			})
		}
		writeJSON(w, rows)
	case http.MethodPost:
		var body struct {
			Name  string `json:"name"`
			Adopt bool   `json:"adopt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := SetGlobalToolAdopted(AuthDB(), user, strings.TrimSpace(body.Name), body.Adopt); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (T *Gateways) servePage(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	sections := []ui.Section{
		{
			Title:    "My API credentials",
			Subtitle: "API keys you own and manage yourself. They live in your namespace — only your agents can dispatch through them, and they never appear on the admin page. Secrets are stored encrypted and never shown to the assistant.",
			Body:     ui.Card{HTML: credentialsHTML},
		},
		{
			Title:    "Connected accounts",
			Subtitle: "Integrations you authorize with your own account (read or write as you). Your key is stored encrypted and never shown to the assistant.",
			Body:     ui.Card{HTML: connectionsHTML},
		},
		{
			Title:    "My tools",
			Subtitle: "Tools you've had the assistant build for you, live in your own pool. Ask in chat to create or change one; delete here to retire any that misbehave.",
			Body:     ui.Card{HTML: userToolsHTML},
		},
		{
			Title:    "Global tools",
			Subtitle: "Shared tools your deployment publishes. Add the ones you want and they become available to your agents; remove any you don't use.",
			Body:     ui.Card{HTML: globalToolsHTML},
		},
	}
	ui.Page{
		Title:     "Gateways",
		ShowTitle: true,
		BackURL:   "/",
		Nav:       HubNav("/gateways"), // shared hub tabs, Gateways active
		MaxWidth:  "640px",
		Sections:  sections,
	}.ServeHTTP(w, r)
}
