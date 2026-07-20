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
		toRow := func(c SecureCredential) row {
			return row{
				Name: c.Name, Type: c.Type, BaseURL: c.BaseURL, ParamName: c.ParamName,
				Description: c.Description, RequiresConfirm: c.RequiresConfirm,
				HasSecret: c.Type != SecureCredNone,
			}
		}
		// ?name=<name> returns the SINGLE record — the edit form's Source. The
		// secret is never included, so leaving the form's secret field blank keeps
		// the stored value.
		if name := strings.TrimSpace(r.URL.Query().Get("name")); name != "" {
			c, found := Secure().LoadUser(user, name)
			if !found {
				http.Error(w, "no such credential", http.StatusNotFound)
				return
			}
			writeJSON(w, toRow(c))
			return
		}
		rows := []row{}
		for _, c := range Secure().ListUser(user) {
			rows = append(rows, toRow(c))
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
			// Adopt-ACL: a restricted global tool only appears in the catalog for
			// users on its AllowedUsers list (empty = open to everyone). Keeps a
			// user from even seeing — let alone adopting — a tool not meant for them.
			if !CanAdoptGlobalTool(AuthDB(), user, p.Tool.Name) {
				continue
			}
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
		// Accept the toggle either as a JSON body ({name, adopt}) or as query
		// params (?name=&adopt=true) — the latter lets a declarative table
		// RowAction button drive it with no client script.
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		adopt := r.URL.Query().Get("adopt") == "true"
		if name == "" {
			var body struct {
				Name  string `json:"name"`
				Adopt bool   `json:"adopt"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			name, adopt = strings.TrimSpace(body.Name), body.Adopt
		}
		if err := SetGlobalToolAdopted(AuthDB(), user, name, adopt); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// credentialFormFields is the shared field list for the "My API credentials" add
// (modal) and edit (row Expand) forms — the user-namespace counterpart to the
// admin credential form, trimmed to the simple key-based types (no OAuth2, which
// stays admin-managed). Type-specific inputs collapse via ShowWhen; the secret is
// a password that stays blank on edit (leaving it blank keeps the stored value).
func credentialFormFields() []ui.FormField {
	return []ui.FormField{
		{Field: "name", Label: "Name", Placeholder: "github_api", Help: "snake_case. Becomes fetch_url_<name> for your agents. Re-using a name updates that credential."},
		{Field: "type", Label: "Type", Type: "select", Options: []ui.SelectOption{
			{Value: "bearer", Label: "Bearer (Authorization: Bearer ...)"},
			{Value: "header", Label: "Custom header"},
			{Value: "query", Label: "Query param"},
			{Value: "basic_auth", Label: "HTTP Basic (user:pass)"},
			{Value: "none", Label: "No auth (public API)"},
		}},
		{Field: "param_name", Label: "Header / Param name", Placeholder: "X-Api-Key or api_key", ShowWhen: "type:header|query"},
		{Field: "base_url", Label: "Base URL", Placeholder: "https://api.example.com", Help: "The server this credential talks to. Requests are allowed only under this host."},
		{Field: "secret", Label: "Secret / token / password", Type: "password", ShowWhen: "type:bearer|header|query|basic_auth", Help: "Stored encrypted, never shown to the assistant. Leave blank when editing to keep the stored value."},
		{Field: "requires_confirm", Label: "Require confirm before each call", Type: "toggle", Help: "When on, every agent call through this credential asks you to allow it first. Use for anything that reaches real people or spends money."},
		{Field: "description", Label: "Description", Type: "textarea", Rows: 2, Help: "Shown to your agents as the tool description."},
	}
}

func (T *Gateways) servePage(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	sections := []ui.Section{
		{
			Title:    "My API credentials",
			Wide:     true,
			Subtitle: "API keys you own and manage yourself. They live in your namespace — only your agents can dispatch through them (as fetch_url_<name>), and they never appear on the admin page. Secrets are stored encrypted and never shown to the assistant.",
			Body: ui.Stack{Children: []ui.Component{
				ui.Table{
					Source: "api/credentials",
					RowKey: "name",
					Columns: []ui.Col{
						{Field: "name", Flex: 1},
						{Field: "type", Mute: true},
						{Field: "base_url", Label: "Base URL", Mute: true, Flex: 2},
					},
					RowActions: []ui.RowAction{
						ui.Expand("Edit", ui.FormPanel{
							Source:      "api/credentials?name={name}",
							PostURL:     "api/credentials",
							SubmitLabel: "Save changes",
							Fields:      credentialFormFields(),
						}),
						{Type: "button", Label: "Delete", Method: "DELETE",
							PostTo:     "api/credentials?name={name}",
							Variant:    "danger",
							Confirm:    "Delete this credential? Agents and tools using it stop working.",
							Optimistic: true},
					},
					EmptyText: "No credentials yet. Add one to let your agents call an API as you.",
				},
				ui.ModalButton{
					Label:    "Add credential",
					Title:    "Add API credential",
					Subtitle: "Pick a type. Bearer / header / query / basic attach a static secret; \"No auth\" is for a public API.",
					Variant:  "primary",
					Width:    "560px",
					Body: ui.FormPanel{
						PostURL:     "api/credentials",
						SubmitLabel: "Create credential",
						Fields:      credentialFormFields(),
					},
				},
			}},
		},
		{
			Title:    "My tools",
			Subtitle: "Tools you've had the assistant build for you, in your own pool. Ask in chat to create or change one; delete here to retire any that misbehave.",
			Body: ui.Table{
				Source: "api/tools",
				RowKey: "name",
				Columns: []ui.Col{
					{Field: "name", Flex: 1},
					{Field: "mode", Mute: true},
					{Field: "shared", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Shared", Color: "info"},
					}},
					{Field: "missing", Label: "Deps", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "⚠ missing", Color: "danger"},
					}},
					{Field: "description", Mute: true, Flex: 2},
				},
				RowActions: []ui.RowAction{
					{Type: "button", Label: "Delete", Method: "DELETE",
						PostTo:     "api/tools?name={name}",
						Variant:    "danger",
						Confirm:    "Delete this tool? Agents using it lose it.",
						Optimistic: true},
				},
				EmptyText: "No tools yet. Ask the assistant in chat to build one for you.",
			},
		},
		{
			Title:    "Global tools",
			Subtitle: "Shared tools your deployment publishes. Add the ones you want and they become available to your agents; remove any you don't use.",
			Body: ui.Table{
				Source: "api/global-tools",
				RowKey: "name",
				Columns: []ui.Col{
					{Field: "name", Flex: 1},
					{Field: "mode", Mute: true},
					{Field: "adopted", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Added", Color: "success"},
					}},
					{Field: "missing", Label: "Deps", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "⚠ missing", Color: "danger"},
					}},
					{Field: "description", Mute: true, Flex: 2},
				},
				RowActions: []ui.RowAction{
					{Type: "button", Label: "Add", Method: "POST",
						PostTo:     "api/global-tools?name={name}&adopt=true",
						HideIf:     "adopted",
						Optimistic: true},
					{Type: "button", Label: "Remove", Method: "POST",
						PostTo:     "api/global-tools?name={name}&adopt=false",
						OnlyIf:     "adopted",
						Optimistic: true},
				},
				EmptyText: "No global tools published yet. When your deployment shares one, it appears here to add.",
			},
		},
		{
			Title:    "Connected accounts",
			Wide:     true,
			Subtitle: "Integrations you authorize with your own account (read or write as you). Your key is stored encrypted and never shown to the assistant.",
			Body:     ui.Card{HTML: connectionsHTML},
		},
	}
	ui.Page{
		Title:     "Gateways",
		ShowTitle: true,
		BackURL:   "/",
		Nav:       HubNav("/gateways"), // shared hub tabs, Gateways active
		MaxWidth:  "1200px",            // wide, admin-style: full-width tables in a two-column grid
		Grid:      true,                // two-column section grid (Wide sections span both)
		Sections:  sections,
	}.ServeHTTP(w, r)
}
