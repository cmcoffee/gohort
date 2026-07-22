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
func (T Gateways) Desc() string {
	return "Apps: the capabilities your agents draw on — credentials, tools, skills, connections."
}
func (T *Gateways) Init() error { return T.Flags.Parse() }
func (T *Gateways) Main() error {
	Log("gateways is a dashboard-only app. Start with: gohort serve")
	return nil
}

// WebPath stays /gateways (the URL slug) so existing links/bookmarks keep
// working; the user-facing NAME is "Extensions", matching the admin group.
func (T *Gateways) WebPath() string { return "/gateways" }
func (T *Gateways) WebName() string { return "Extensions" }
func (T *Gateways) WebDesc() string {
	return "Credentials, tools, skills, and connections your agents draw on to do their work."
}

// HubTab puts Extensions on the shared top-nav tab row alongside Agents, Bridges,
// and Knowledge — it's the per-user capability surface those agents draw on, so
// it belongs in the same hub. Ordered after the others.
func (T *Gateways) HubTab() (string, int) { return "Extensions", 40 }

func (T *Gateways) Routes() {
	T.HandleFunc("/api/credentials", T.handleCredentials)
	T.HandleFunc("/api/tools", T.handleUserTools)
	T.HandleFunc("/api/promotions", T.handlePromotions)
	T.HandleFunc("/api/global-tools", T.handleGlobalTools)
	T.HandleFunc("/api/skills", T.handleUserSkills)
	T.HandleFunc("/api/pipelines", T.handleUserPipelines)
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
			Disabled        bool   `json:"disabled"`
		}
		toRow := func(c SecureCredential) row {
			return row{
				Name: c.Name, Type: c.Type, BaseURL: c.BaseURL, ParamName: c.ParamName,
				Description: c.Description, RequiresConfirm: c.RequiresConfirm,
				HasSecret: c.Type != SecureCredNone, Disabled: c.Disabled,
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
		// enable/disable — mute/unmute a credential without editing it. A
		// disabled credential drops out of the agent tool catalog until re-
		// enabled. Owner-scoped via SetDisabledOwned, so it only ever touches
		// the user's own credential, never a global one of the same name.
		if action := strings.TrimSpace(r.URL.Query().Get("action")); action == "enable" || action == "disable" {
			name := strings.TrimSpace(r.URL.Query().Get("name"))
			if name == "" {
				http.Error(w, "missing name", http.StatusBadRequest)
				return
			}
			if err := Secure().SetDisabledOwned(user, name, action == "disable"); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
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
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	switch r.Method {
	case http.MethodGet:
		// Single-record fetch (?name=) — powers the "View" RecordView and the
		// "Set category" form prefill. Returns the raw TempTool, whose json tags
		// already expose every field (category, command_template, script_body,
		// actions, …). Own pool only, so a user can only inspect their own tools.
		if name != "" {
			for _, p := range LoadPersistentTempTools(AuthDB(), user) {
				if p.Tool.Name == name {
					writeJSON(w, p.Tool)
					return
				}
			}
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		type row struct {
			Name        string `json:"name"`
			Description string `json:"description,omitempty"`
			Mode        string `json:"mode,omitempty"`
			Credential  string `json:"credential,omitempty"`
			Category    string `json:"category,omitempty"`
			Missing     bool   `json:"missing"`
			Shared      bool   `json:"shared"`
			LastUsed    string `json:"last_used,omitempty"`
			// Promotion (publish-to-catalog) request state for this tool.
			Requested  bool `json:"requested"`   // a promotion request is pending admin review
			CanRequest bool `json:"can_request"` // eligible to request: not already shared, none pending
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
			// A shared tool is never "requested" (sharing fulfills the request), so
			// suppress the badge even if a stale pending row survives.
			pending := !p.Shared && PendingPromotion(AuthDB(), user, "tool", p.Tool.Name)
			rows = append(rows, row{
				Name: p.Tool.Name, Description: p.Tool.Description, Mode: p.Tool.Mode,
				Credential: p.Tool.Credential, Category: p.Tool.Category,
				Missing: missing, Shared: p.Shared, LastUsed: last,
				Requested: pending, CanRequest: !p.Shared && !pending,
			})
		}
		writeJSON(w, rows)
	case http.MethodPost:
		// set_category — the user CLAIMS (or clears) a grouping label on their
		// OWN tool. Free-form: a tool may coin a new category by name; the admin
		// ToolGroup registry only supplies optional descriptions for categories
		// that have a registered entry. Because the label lives on the user's own
		// tool record, this touches nothing outside their namespace — no per-user
		// group store needed. See Tool.Category.
		if r.URL.Query().Get("action") != "set_category" {
			http.Error(w, "unknown action", http.StatusBadRequest)
			return
		}
		if name == "" {
			http.Error(w, "missing name", http.StatusBadRequest)
			return
		}
		var body struct {
			Category string `json:"category"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		var tt *TempTool
		for _, p := range LoadPersistentTempTools(AuthDB(), user) {
			if p.Tool.Name == name {
				t := p.Tool // copy; UpdatePersistentTempTool replaces the whole TempTool
				tt = &t
				break
			}
		}
		if tt == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		tt.Category = strings.TrimSpace(body.Category)
		if !UpdatePersistentTempTool(AuthDB(), user, *tt) {
			http.Error(w, "update failed", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
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

// handleUserSkills is the user's OWN skills surface — the per-user counterpart to
// the admin Skills section, scoped to the calling user's pool. Skills are behavior
// packets the assistant draws on in its own context (see the admin section for the
// full model). Authoring stays in Builder/chat — a skill is instructions plus
// optional knowledge, not a hand-filled form — so this surface is view + toggle +
// delete, mirroring "My tools". GET lists; POST ?action=enable|disable mutes/unmutes
// without a full round-trip; DELETE removes one.
func (T *Gateways) handleUserSkills(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		// Single-record fetch (?id=) — prefills the Edit form with the BEHAVIOR
		// fields the user may edit (triggers flattened to newline-separated
		// text). Builder-managed fields (bundled Tools, AllowedTools,
		// AttachedCollections) are intentionally omitted — this surface edits
		// behavior, not capability grants.
		if id := strings.TrimSpace(r.URL.Query().Get("id")); id != "" {
			for _, s := range LoadSkills(AuthDB(), user) {
				if s.ID == id {
					writeJSON(w, map[string]any{
						"id":           s.ID,
						"name":         s.Name,
						"description":  s.Description,
						"triggers":     strings.Join(s.Triggers, "\n"),
						"instructions": s.Instructions,
					})
					return
				}
			}
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		// Wire shape omits the embedding (a large float32 array the UI never
		// needs) and the instructions/tools bodies (managed in Builder).
		type row struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description,omitempty"`
			Triggers    int    `json:"triggers"`
			Disabled    bool   `json:"disabled"`
			Updated     string `json:"updated,omitempty"`
		}
		rows := []row{}
		for _, s := range LoadSkills(AuthDB(), user) {
			updated := ""
			if !s.Updated.IsZero() {
				updated = s.Updated.Format("2006-01-02")
			}
			rows = append(rows, row{
				ID: s.ID, Name: s.Name, Description: s.Description,
				Triggers: len(s.Triggers), Disabled: s.Disabled, Updated: updated,
			})
		}
		writeJSON(w, rows)
	case http.MethodPost:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		action := strings.TrimSpace(r.URL.Query().Get("action"))
		// enable/disable — mute/unmute without touching the definition.
		if action == "enable" || action == "disable" {
			if id == "" {
				http.Error(w, "missing id", http.StatusBadRequest)
				return
			}
			var found *SkillRecord
			for _, s := range LoadSkills(AuthDB(), user) {
				if s.ID == id {
					dup := s
					found = &dup
					break
				}
			}
			if found == nil {
				http.Error(w, "skill not found", http.StatusNotFound)
				return
			}
			found.Disabled = (action == "disable")
			if _, err := SaveSkill(AuthDB(), user, *found); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// Save — create (no ?id=) or edit (?id=) a skill's BEHAVIOR fields
		// directly (name, description, triggers, instructions). An edit
		// load-then-mutates so Builder-managed capability fields (bundled Tools,
		// AllowedTools, AttachedCollections) are preserved. This form CANNOT set
		// those grants — a form-authored skill is pure behavior; anything that
		// ships code or grants tools stays in Builder. Own namespace only.
		var body struct {
			Name         string `json:"name"`
			Description  string `json:"description"`
			Triggers     string `json:"triggers"`
			Instructions string `json:"instructions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(body.Name)
		if name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		var rec SkillRecord
		if id != "" {
			found := false
			for _, s := range LoadSkills(AuthDB(), user) {
				if s.ID == id {
					rec = s // preserve Tools / AllowedTools / AttachedCollections
					found = true
					break
				}
			}
			if !found {
				http.Error(w, "skill not found", http.StatusNotFound)
				return
			}
		}
		rec.Name = name
		rec.Description = strings.TrimSpace(body.Description)
		rec.Instructions = body.Instructions
		rec.Triggers = splitSkillTriggers(body.Triggers)
		if _, err := SaveSkill(AuthDB(), user, rec); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		if !DeleteSkill(AuthDB(), user, id) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleUserPipelines is the user's OWN pipelines surface — the per-user
// counterpart to the admin Pipelines section, scoped to the calling user's pool.
// A pipeline is a declarative multi-stage workflow authored in Agents (the
// pipeline tool / Builder), so — like skills and tools — this surface is view +
// delete, not authoring. GET lists (with the full definition for the stage
// inspector); DELETE retires one.
//
// Storage note: pipelines live in ORCHESTRATE's per-app bucket, not this app's,
// so we scope UserDB off RootDB.Bucket("orchestrate") — the same base the admin
// Pipelines section reaches into. This couples to orchestrate's app name, which
// is where the data genuinely lives; the alternative (a cross-app pipeline API)
// isn't worth it for a read/delete view.
func (T *Gateways) handleUserPipelines(w http.ResponseWriter, r *http.Request) {
	base := RootDB
	if base == nil {
		http.Error(w, "pipeline store unavailable", http.StatusServiceUnavailable)
		return
	}
	user, udb, ok := RequireUser(w, r, base.Bucket("orchestrate"))
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		// Detail carries the full definition so the row's "View" expand can show
		// the stages without a second fetch (mirrors the admin section).
		type wire struct {
			ID          string      `json:"id"`
			Name        string      `json:"name"`
			Description string      `json:"description,omitempty"`
			Stages      int         `json:"stages"`
			Detail      PipelineDef `json:"detail"`
		}
		defs := ListPipelineDefs(udb, user)
		out := make([]wire, 0, len(defs))
		for _, d := range defs {
			out = append(out, wire{ID: d.ID, Name: d.Name, Description: d.Description, Stages: len(d.Stages), Detail: d})
		}
		writeJSON(w, map[string]any{"pipelines": out})
	case http.MethodDelete:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		if _, found := LoadPipelineDef(udb, user, id); !found {
			http.NotFound(w, r)
			return
		}
		DeletePipelineDef(udb, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handlePromotions lets a user request that one of their OWN resources be
// published deployment-wide (bottom-up escalation — an admin approves it on the
// Administrator page). Today only tool promotion is wired: the request asks the
// admin to Share the tool to the global catalog. POST ?kind=tool&name=<tool>
// with an optional JSON {note}; owner is the session user, who must own the tool.
func (T *Gateways) handlePromotions(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if kind != "tool" {
		http.Error(w, "only tool promotion is available", http.StatusBadRequest)
		return
	}
	if name == "" {
		http.Error(w, "missing name", http.StatusBadRequest)
		return
	}
	// Ownership: the tool must be in the caller's own persistent pool.
	owns := false
	for _, p := range LoadPersistentTempTools(AuthDB(), user) {
		if p.Tool.Name == name {
			owns = true
			break
		}
	}
	if !owns {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Note string `json:"note"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body) // note is optional
	if err := CreatePromotionRequest(AuthDB(), user, kind, name, body.Note); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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

// userSkillFormFields is the Add/Edit form for a user's own skill. BEHAVIOR
// fields only — a form-authored skill is a pure instruction packet. Bundled
// tools, tool grants (AllowedTools), and attached collections are NOT here:
// those ship code or grant capability and stay Builder-authored. An edit
// preserves them (the handler load-then-mutates).
func userSkillFormFields() []ui.FormField {
	return []ui.FormField{
		{Field: "name", Label: "Name", Placeholder: "Contract Reviewer", Help: "Shown to your agents; also the H2 header above the instructions when the skill is active."},
		{Field: "description", Label: "Description", Help: "One line — when this skill applies. The assistant reads it to decide relevance."},
		{Field: "triggers", Label: "Triggers", Type: "textarea", Rows: 3, Placeholder: "contract\n*.pdf", Help: "Substring patterns (or *.ext for attachments), ONE PER LINE. Any match activates the skill. Leave blank to rely on the description."},
		{Field: "instructions", Label: "Instructions", Type: "textarea", Rows: 12, Help: "Markdown appended to the assistant's prompt while the skill is active — the approach, voice, or method it should apply."},
	}
}

// splitSkillTriggers parses the triggers textarea (one pattern per line) into
// the stored slice, trimming blanks. Newline-only split so a pattern may
// itself contain a comma.
func splitSkillTriggers(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			out = append(out, ln)
		}
	}
	return out
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
						{Field: "disabled", Label: "Status", Type: "dot", Badges: []ui.BadgeMapping{
							{Value: true, Label: "Disabled", Color: "danger"},
							{Value: false, Label: "Active", Color: "success"},
						}},
					},
					RowActions: []ui.RowAction{
						ui.Expand("Edit", ui.FormPanel{
							Source:      "api/credentials?name={name}",
							PostURL:     "api/credentials",
							SubmitLabel: "Save changes",
							Fields:      credentialFormFields(),
						}),
						// Mute/unmute without editing — a disabled credential drops
						// out of the agent tool catalog until re-enabled.
						{Type: "button", Label: "Disable", Method: "POST",
							PostTo:     "api/credentials?action=disable&name={name}",
							HideIf:     "disabled",
							Optimistic: true},
						{Type: "button", Label: "Enable", Method: "POST",
							PostTo:     "api/credentials?action=enable&name={name}",
							OnlyIf:     "disabled",
							Optimistic: true},
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
			Title:    "Connected accounts",
			Wide:     true,
			Subtitle: "Integrations you authorize with your own account (read or write as you). Your key is stored encrypted and never shown to the assistant.",
			Body:     ui.Card{HTML: connectionsHTML},
		},
		{
			Title:    "My tools",
			Subtitle: "Tools you've had the assistant build for you, in your own pool. Ask in chat to create or change one; delete here to retire any that misbehave.",
			Body: ui.Table{
				Source: "api/tools",
				RowKey: "name",
				Columns: []ui.Col{
					{Field: "name", Flex: 1},
					{Field: "category", Label: "Category", Mute: true},
					{Field: "mode", Mute: true},
					{Field: "shared", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Shared", Color: "info"},
					}},
					{Field: "requested", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Publish requested", Color: "warning"},
					}},
					{Field: "missing", Label: "Deps", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "⚠ missing", Color: "danger"},
					}},
					{Field: "description", Mute: true, Flex: 2},
				},
				RowActions: []ui.RowAction{
					// View the full tool definition (read parity with the admin's
					// tool RecordView, scoped to the user's own pool). Source fetches
					// the single record so heavy fields (script body, command
					// template, actions) don't bloat the list payload.
					ui.Expand("View", ui.RecordView{
						Source: "api/tools?name={name}",
						Pairs: []ui.DisplayPair{
							{Label: "Name", Field: "name", Mono: true},
							{Label: "Category", Field: "category"},
							{Label: "Description", Field: "description"},
							{Label: "Mode", Field: "mode"},
							{Label: "Method", Field: "method", Mono: true},
							{Label: "Command / URL template", Field: "command_template", Mono: true, Block: true},
							{Label: "Body template", Field: "body_template", Mono: true, Block: true},
							{Label: "Script name", Field: "script_name", Mono: true},
							{Label: "Script body", Field: "script_body", Block: true},
							{Label: "Credential", Field: "credential", Mono: true},
							{Label: "Response pipe", Field: "response_pipe", Mono: true, Block: true},
							// Toolbox-mode tools bundle several endpoints under one
							// name — list each sub-action. Empty for non-toolbox tools.
							{Label: "Actions", Field: "actions", Items: []ui.DisplayPair{
								{Field: "name", Mono: true},
								{Label: "method", Field: "method", Mono: true},
								{Label: "url", Field: "url_template", Mono: true},
								{Label: "desc", Field: "description"},
							}},
						},
					}),
					// Set category — the user claims a grouping label on their own
					// tool. Free-form; Source prefills the current value.
					ui.Expand("Set category", ui.FormPanel{
						Source:      "api/tools?name={name}",
						PostURL:     "api/tools?action=set_category&name={name}",
						SubmitLabel: "Save category",
						Fields: []ui.FormField{
							{Field: "category", Type: "text", Label: "Category",
								Placeholder: "e.g. Acme API, Research, Messaging",
								Help:        "Groups this tool under a header in the tool picker and your list. Leave blank to fall back to its capability label."},
						},
						Invalidate: []string{"api/tools"},
					}),
					// Request to publish — ask an admin to Share this tool to the
					// deployment-wide catalog. Only when it isn't already shared and
					// has no request pending (can_request).
					ui.ModalActionIf("Request to publish", "can_request", "", ui.FormPanel{
						SubmitLabel: "Send request",
						PostURL:     "api/promotions?kind=tool&name={name}",
						Fields: []ui.FormField{
							{Field: "note", Type: "textarea", Rows: 3, Label: "Note for the admin (optional)",
								Placeholder: "Why should this tool be in the shared catalog?"},
						},
						Invalidate: []string{"api/tools"},
					}),
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
			Title:    "My skills",
			Subtitle: "Behavior packs your agents draw on — instructions the assistant applies when a skill's triggers or description match the turn. Author or edit one right here (name, triggers, instructions), or ask Builder in Agents for skills that ship code or grant tools. Disable to mute a skill without losing it; delete to retire it.",
			Body: ui.Stack{Children: []ui.Component{
				ui.Table{
					Source: "api/skills",
					RowKey: "id",
					Columns: []ui.Col{
						{Field: "name", Flex: 1},
						{Field: "description", Mute: true, Flex: 2},
						{Field: "triggers", Label: "Triggers", Mute: true},
						{Field: "disabled", Label: "Status", Type: "dot", Badges: []ui.BadgeMapping{
							{Value: true, Label: "Disabled", Color: "danger"},
							{Value: false, Label: "Active", Color: "success"},
						}},
					},
					RowActions: []ui.RowAction{
						// Edit the skill's behavior fields. Source prefills; the id
						// rides in the PostURL so the handler load-then-mutates
						// (preserving any Builder-authored tools/grants).
						ui.Expand("Edit", ui.FormPanel{
							Source:      "api/skills?id={id}",
							PostURL:     "api/skills?id={id}",
							SubmitLabel: "Save skill",
							Fields:      userSkillFormFields(),
							Invalidate:  []string{"api/skills"},
						}),
						{Type: "button", Label: "Disable", Method: "POST",
							PostTo:     "api/skills?action=disable&id={id}",
							HideIf:     "disabled",
							Optimistic: true},
						{Type: "button", Label: "Enable", Method: "POST",
							PostTo:     "api/skills?action=enable&id={id}",
							OnlyIf:     "disabled",
							Optimistic: true},
						{Type: "button", Label: "Delete", Method: "DELETE",
							PostTo:     "api/skills?id={id}",
							Variant:    "danger",
							Confirm:    "Delete this skill? The definition is gone for good.",
							Optimistic: true},
					},
					EmptyText: "No skills yet. Add one below, or ask Builder in Agents to author one for you.",
				},
				ui.ModalButton{
					Label:    "Add skill",
					Title:    "New skill",
					Subtitle: "A behavior pack — instructions your agents apply when the triggers match. For a skill that ships code or grants tools, use Builder instead.",
					Variant:  "primary",
					Width:    "640px",
					Body: ui.FormPanel{
						PostURL:     "api/skills",
						SubmitLabel: "Create skill",
						Fields:      userSkillFormFields(),
						Invalidate:  []string{"api/skills"},
					},
				},
			}},
		},
		{
			Title:    "My pipelines",
			Subtitle: "Declarative multi-stage workflows your agents run — authored in Agents (the pipeline tool or Builder). Expand one to inspect its stages; delete to retire a definition (it also detaches from any agent that used it).",
			Body: ui.Table{
				Source:       "api/pipelines",
				RecordsField: "pipelines",
				RowKey:       "id",
				Columns: []ui.Col{
					{Field: "name", Flex: 1},
					{Field: "description", Mute: true, Flex: 2},
					{Field: "stages", Label: "Stages"},
				},
				RowActions: []ui.RowAction{
					ui.Expand("View", ui.JSONView{Field: "detail", Title: "Definition"}),
					{Type: "button", Label: "Delete", Method: "DELETE",
						PostTo:     "api/pipelines?id={id}",
						Variant:    "danger",
						Confirm:    "Delete this pipeline definition? It's removed and detached from any agent that used it; re-authoring means re-creating the stages.",
						Optimistic: true},
				},
				EmptyText: "No pipelines yet. Ask Builder in Agents to author one, or use the pipeline tool.",
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
	}
	ui.Page{
		Title:      "Extensions",
		ShowTitle:  true,
		BackURL:    "/",
		Nav:        HubNav("/gateways"), // shared hub tabs, Extensions active
		MaxWidth:   "1200px",            // wide, admin-style: full-width tables in the content pane
		SectionNav: true,                // left-rail sub-nav: one section (credentials/tools/…) at a time
		Sections:   sections,
	}.ServeHTTP(w, r)
}
