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
	T.HandleFunc("/api/tool-access", T.handleUserToolAccess)
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
		// Auto-enable on finishing a draft. A draft_api_credential lands
		// DISABLED with a placeholder secret, and Save deliberately preserves
		// Disabled — so pasting the secret here would leave the credential
		// silently disabled and unusable, forcing the user to discover a
		// SEPARATE Enable step (a real point of confusion). If this record was a
		// pending draft (disabled, no real secret) and the user is now providing
		// one, flip it live so saving the secret is all it takes.
		_, wasEnabled, hadSecret := Secure().CredentialStatusOwned(user, c.Name)
		if err := Secure().Save(c, body.Secret); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !wasEnabled && !hadSecret && strings.TrimSpace(body.Secret) != "" {
			if err := Secure().SetDisabledOwned(user, c.Name, false); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
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
		// actions, …). Scoped to this user's own tools: their pool first, then
		// their session drafts so View works on a draft row instead of 404ing.
		// (The two can't collide — the draft lister already drops any draft
		// shadowed by a committed tool of the same name — but the pool is the
		// source of truth, so it answers first regardless.)
		if name != "" {
			for _, p := range LoadPersistentTempTools(AuthDB(), user) {
				if p.Tool.Name == name {
					writeJSON(w, p.Tool)
					return
				}
			}
			for _, d := range ListSessionDrafts(user) {
				if d.Shadowed {
					continue
				}
				if d.Tool.Name == name {
					writeJSON(w, d.Tool)
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
			// User-managed governance flags (My tools toggles).
			Locked      bool `json:"locked"`       // frozen — AI can't modify/delete
			Disabled    bool `json:"disabled"`     // off for every agent
			BuilderOnly bool `json:"builder_only"` // exposed to Builder only
			// Promotion (publish-to-catalog) request state for this tool.
			Requested  bool `json:"requested"`   // a promotion request is pending admin review
			CanRequest bool `json:"can_request"` // eligible to request: not already shared, none pending
			// Session drafts — tools the assistant authored mid-conversation with
			// persist=false. They are real and callable for the life of their chat
			// session, but they vanish when it is deleted, and they were only ever
			// visible from inside that session's Tools modal. Listing them here is
			// the difference between "what has been built for me?" and "what have I
			// kept?" — a user could otherwise lose work they never knew existed.
			Pool      bool `json:"pool"`       // lives in the persistent pool
			Session   bool `json:"session"`    // draft, not kept anywhere
			AgentTool bool `json:"agent_tool"` // bundled onto an agent record
			Orphan    bool `json:"orphan"`     // owning agent was deleted
			Trial     bool `json:"trial"`      // authored mid-chat, unconfirmed
			// Deletable = has a record of its own to delete (pool or orphan). A
			// session draft is excluded: Discard is its verb, and DELETE would
			// 404 on a tool that lives only in a chat session.
			Deletable bool   `json:"deletable"`
			SessionID string `json:"session_id,omitempty"` // for the keep/drop actions
			AgentID   string `json:"agent_id,omitempty"`
			// Group is the heading this row renders under (ui.Table group_by).
			Group string `json:"group"`
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
				Locked: p.Tool.Locked, Disabled: p.Tool.Disabled, BuilderOnly: p.Tool.BuilderOnly,
				Requested: pending, CanRequest: !p.Shared && !pending,
				Pool: true, Deletable: true,
				Group: "All Agents (Global tools)",
			})
		}
		// Agent-scoped tools after the global pool. Sections read: All Agents ->
		// Scoped Tools (per agent) -> legacy session drafts -> Orphaned.
		//
		// Two passes, because grouping follows RECORD order: pass one emits
		// every agent-scoped tool, pass two the legacy drafts. The lister sorts
		// by agent, so agents stay grouped within each pass.
		//
		// Shadowed rows are dropped: a draft already committed under the same
		// name would invite an action that does nothing.
		scoped := ListScopedTools(user)
		for _, pass := range []string{ScopeAgentTool, ScopeSessionTool} {
			for _, st := range scoped {
				if st.Shadowed || st.Scope != pass {
					continue
				}
				agent := strings.TrimSpace(st.AgentName)
				if agent == "" {
					agent = st.AgentID
				}
				// One "Scoped Tools" family, named per agent so you can still see
				// which. An unconfirmed tool sits with its agent rather than in its
				// own section — the Unconfirmed badge already distinguishes it, and
				// splitting scattered one agent's tools across two headings.
				group := "Scoped Tools — " + agent
				if st.Scope == ScopeSessionTool {
					// Legacy: session scope is retired, so these exist only until the
					// conversation holding them is reopened and they migrate.
					group = "Session drafts (legacy) — " + agent
					if t := strings.TrimSpace(st.SessionTitle); t != "" {
						group += " · " + t
					}
				}
				missing := false
				if cred := strings.TrimSpace(st.Tool.Credential); cred != "" && !strings.EqualFold(cred, "no_auth") {
					if _, found := Secure().Resolve(cred, user); !found {
						missing = true
					}
				}
				rows = append(rows, row{
					Name: st.Tool.Name, Description: st.Tool.Description, Mode: st.Tool.Mode,
					Credential: st.Tool.Credential, Category: st.Tool.Category, Missing: missing,
					Session:   st.Scope == ScopeSessionTool,
					AgentTool: st.Scope == ScopeAgentTool,
					Trial:     st.Trial,
					SessionID: st.SessionID, AgentID: st.AgentID, Group: group,
				})
			}
		}
		// Reap expired unconfirmed tools before listing, so the page never shows
		// a row it is about to delete. Opening My tools is the moment a user is
		// looking at exactly this, which makes it the honest place to sweep.
		if ReapTrialTools != nil {
			if n := ReapTrialTools(AuthDB(), user); n > 0 {
				Log("[gateways] reaped %d unconfirmed tool(s) for %s", n, user)
			}
		}
		// Orphans last: a tool whose agent was deleted is still the user's, and it
		// was captured precisely so it wouldn't vanish with the record — but it is
		// attached to nothing, so it needs re-homing (Access) or deleting. Without
		// this it was visible only on the admin page.
		for _, o := range LoadOrphanedTempTools(AuthDB(), user) {
			former := strings.TrimSpace(o.FormerAgentName)
			if former == "" {
				former = o.FormerAgentID
			}
			missing := false
			if cred := strings.TrimSpace(o.Tool.Credential); cred != "" && !strings.EqualFold(cred, "no_auth") {
				if _, found := Secure().Resolve(cred, user); !found {
					missing = true
				}
			}
			rows = append(rows, row{
				Name: o.Tool.Name, Description: o.Tool.Description, Mode: o.Tool.Mode,
				Credential: o.Tool.Credential, Category: o.Tool.Category, Missing: missing,
				Orphan: true, Deletable: true,
				Group: "Orphaned Tools — agent " + former + " was deleted",
			})
		}
		writeJSON(w, rows)
	case http.MethodPost:
		// Governance toggles + category, all on the user's OWN tool record (own
		// namespace, nothing shared):
		//   set_category — claim/clear a grouping label (see Tool.Category).
		//   lock / unlock — freeze the definition against AI modify/delete.
		//   disable / enable — hide from / restore to every agent's catalog.
		//   builder_only_on / builder_only_off — expose to Builder agent only.
		//   keep_draft / drop_draft — resolve a session draft (see below).
		action := r.URL.Query().Get("action")
		if name == "" {
			http.Error(w, "missing name", http.StatusBadRequest)
			return
		}
		// Session-draft actions run BEFORE the pool lookup: a draft is by
		// definition not in the persistent pool, so that lookup would 404 it.
		// confirm — the user vouching for a trial tool the assistant authored.
		// It doesn't move the tool; it only clears the unconfirmed mark.
		if action == "confirm" {
			agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
			if agentID == "" {
				for _, st := range ListScopedTools(user) {
					if st.Scope == ScopeAgentTool && st.Tool.Name == name {
						agentID = st.AgentID
						break
					}
				}
			}
			if agentID == "" {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			if ConfirmAgentTool == nil {
				http.Error(w, "confirm unavailable", http.StatusServiceUnavailable)
				return
			}
			if err := ConfirmAgentTool(AuthDB(), user, agentID, name); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if action == "keep_draft" || action == "drop_draft" {
			sid := strings.TrimSpace(r.URL.Query().Get("session_id"))
			if sid == "" {
				http.Error(w, "missing session_id", http.StatusBadRequest)
				return
			}
			// Resolve from the session that owns it, not by name alone: two
			// sessions can hold different drafts under the same name.
			found := false
			for _, d := range ListSessionDrafts(user) {
				if !d.Shadowed && d.SessionID == sid && d.Tool.Name == name {
					found = true
					break
				}
			}
			if !found {
				http.Error(w, "no session draft "+name+" in that session", http.StatusNotFound)
				return
			}
			if action == "drop_draft" {
				RemoveSessionTempTool(AuthDB(), sid, name)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			// Where a kept draft lands — the user-wide pool (every agent) or the
			// agent whose session built it. This is the same session-vs-global
			// choice the in-chat Tools modal offers, routed through the same
			// implementation so both behave identically (including stripping a
			// now-redundant agent copy on a global keep). Default global: it is
			// the answer for most keeps and the one with no agent dependency.
			target := ScopeTargetGlobal
			if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("target")), ScopeTargetAgent) {
				target = ScopeTargetAgent
			}
			agentID := ""
			for _, d := range ListSessionDrafts(user) {
				if d.SessionID == sid && d.Tool.Name == name {
					agentID = d.AgentID
					break
				}
			}
			if _, err := PromoteScopedTool(user, agentID, sid, name, target); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
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
		switch action {
		case "set_category":
			// Free-form: a tool may coin a new category by name; the admin
			// ToolGroup registry only supplies optional descriptions for
			// categories that have a registered entry. The label lives on the
			// user's own tool record, so this touches nothing outside their
			// namespace — no per-user group store needed.
			var body struct {
				Category string `json:"category"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			tt.Category = strings.TrimSpace(body.Category)
		case "lock":
			tt.Locked = true
		case "unlock":
			tt.Locked = false
		case "disable":
			tt.Disabled = true
		case "enable":
			tt.Disabled = false
		case "builder_only_on":
			tt.BuilderOnly = true
		case "builder_only_off":
			tt.BuilderOnly = false
		default:
			http.Error(w, "unknown action", http.StatusBadRequest)
			return
		}
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
		// An orphan isn't in the pool, so the pool delete would 404 it — discard
		// it from the orphan store instead. Checked first because that is the
		// only place it lives.
		for _, o := range LoadOrphanedTempTools(AuthDB(), user) {
			if o.Tool.Name == name {
				if !RemoveOrphanedTempTool(AuthDB(), user, name) {
					http.Error(w, "not found", http.StatusNotFound)
					return
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
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
		// A tool already in the user's OWN pool (they authored it, and it may be
		// the one they shared) is always active for them — "adopting" it from the
		// catalog is a no-op, and a same-named global tool would just collide. So
		// hide anything the user already has by name from the catalog.
		own := map[string]bool{}
		for _, p := range LoadPersistentTempTools(AuthDB(), user) {
			own[p.Tool.Name] = true
		}
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
			// Own tool (or a same-named one already in the pool) — not a catalog
			// candidate; it's shown under "My tools" instead.
			if own[p.Tool.Name] {
				continue
			}
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
			Subtitle: "Everything built for you. \"All Agents\" is your global pool — every agent can use it. \"Scoped Tools\" live on one agent's record; ones the assistant authored but nobody has vouched for are badged Unconfirmed, and are dropped automatically if left that way. \"Orphaned Tools\" lost their agent when it was deleted. Access sets which of your agents can use a tool — switching one on also re-homes an orphan. Filter the list with the box above.",
			Body: ui.Table{
				Source:            "api/tools",
				RowKey:            "name",
				Search:            true,
				SearchPlaceholder: "Filter tools by name, agent, category…",
				// Rows arrive pre-ordered (pool, then per-agent tools, then that
				// agent's session drafts) and render under a heading per scope —
				// "which agent is this attached to?" is how this list is actually
				// read. Grouping follows record order, so the server owns it.
				GroupBy: "group",
				Columns: []ui.Col{
					// Tool names run long (create_apple_calendar_event) and this row
					// carries several status badges, so give the name the largest
					// share and keep the mute description narrow — otherwise the name
					// ellipsizes.
					{Field: "name", Flex: 3},
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
					{Field: "locked", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "🔒 Locked", Color: "info"},
					}},
					{Field: "disabled", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Disabled", Color: "danger"},
					}},
					{Field: "builder_only", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Builder-only", Color: "warning"},
					}},
					// Session drafts are the one row type here that is NOT kept —
					// badge it plainly rather than letting it read as pool membership.
					{Field: "session", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Session draft", Color: "warning"},
					}},
					{Field: "agent_tool", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "On agent", Color: "info"},
					}},
					{Field: "orphan", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Orphaned", Color: "danger"},
					}},
					{Field: "trial", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Unconfirmed", Color: "warning"},
					}},
					{Field: "description", Mute: true, Flex: 1},
				},
				RowActions: []ui.RowAction{
					// View the full tool definition (read parity with the admin's
					// tool RecordView, scoped to the user's own pool). Source fetches
					// the single record so heavy fields (script body, command
					// template, actions) don't bloat the list payload.
					ui.ExpandIf("View", "", "", ui.RecordView{
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
					ui.ExpandIf("Set category", "pool", "", ui.FormPanel{
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
					// Session drafts: keep moves the tool into the pool (where every
					// control above starts applying); discard throws it away. Both
					// only appear on draft rows, and every pool-only action below is
					// hidden from them — a draft has no pool record to lock, disable,
					// publish or delete, so those would just fail confusingly.
					// Access — which of the user's OWN agents can use this tool,
					// with "All my agents" as one more chip (the user-wide pool).
					// The same control serves a session draft: picking anything is
					// what KEEPS it, so a draft doesn't need its own verb.
					// Access — the pill list: "All my agents" (the shared pool every
					// agent draws from) plus one pill per agent the user owns.
					// Same control the admin page used to carry, now where it
					// belongs: an admin has no business choosing which of your
					// agents load your own tool.
					{Type: "button", Label: "Access", Method: "client",
						PostTo: "tool_access_pills"},
					// Confirm — vouch for a tool the assistant authored. Only on
					// unconfirmed rows; it clears the mark without moving the tool.
					{Type: "button", Label: "Confirm", Method: "POST",
						PostTo:     "api/tools?action=confirm&name={name}&agent_id={agent_id}",
						OnlyIf:     "trial",
						Optimistic: true},
					{Type: "button", Label: "Discard", Method: "POST",
						PostTo:     "api/tools?action=drop_draft&name={name}&session_id={session_id}",
						OnlyIf:     "session",
						Confirm:    "Discard this draft? It disappears from the chat session that built it.",
						Variant:    "danger",
						Optimistic: true},
					// Lock freezes the definition — the assistant can't modify or
					// delete a locked tool (unlock first). Running is unaffected.
					{Type: "button", Label: "Lock", Method: "POST",
						PostTo: "api/tools?action=lock&name={name}", HideIf: "locked", OnlyIf: "pool"},
					{Type: "button", Label: "Unlock", Method: "POST",
						PostTo: "api/tools?action=unlock&name={name}", OnlyIf: "locked"},
					// Disable hides the tool from every agent's catalog (Builder still
					// loads it to test/fix). Enable restores it.
					{Type: "button", Label: "Disable", Method: "POST",
						PostTo: "api/tools?action=disable&name={name}", HideIf: "disabled", OnlyIf: "pool"},
					{Type: "button", Label: "Enable", Method: "POST",
						PostTo: "api/tools?action=enable&name={name}", OnlyIf: "disabled"},
					// Builder-only exposes the tool to the Builder agent only.
					{Type: "button", Label: "Builder-only", Method: "POST",
						PostTo: "api/tools?action=builder_only_on&name={name}", HideIf: "builder_only", OnlyIf: "pool"},
					{Type: "button", Label: "All agents", Method: "POST",
						PostTo: "api/tools?action=builder_only_off&name={name}", OnlyIf: "builder_only"},
					// Delete is hidden while locked — unlock first.
					{Type: "button", Label: "Delete", Method: "DELETE",
						PostTo:     "api/tools?name={name}",
						Variant:    "danger",
						HideIf:     "locked",
						OnlyIf:     "deletable",
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
		// App-specific behavior stays in the app: core/ui supplies the generic
		// pill renderer (uiRenderScopePills), this supplies the endpoint it
		// talks to. No copy of the renderer lives here.
		Head: ui.NewHead().ClientAction("tool_access_pills", toolAccessPillsJS),
	}.ServeHTTP(w, r)
}

// handleUserToolAccess is the user's own tool-scope control: which of THEIR
// agents can use a tool, plus an "all agents" option for the user-wide pool.
//
// This is tier 2 of tool access, and it belongs to the user. Tier 1 — which
// USERS may reach a shared tool — stays on the admin page, where the per-agent
// pill editor used to live confusingly alongside it. An admin has no business
// deciding which of someone's own agents load their own tool.
//
// GET  ?name=  → {agents: [{id,name}], attached: [ids]} for the chip picker.
//
//	"global" is offered as a pseudo-agent meaning the user-wide
//	pool (every agent), because that is how it reads to a user:
//	one more place the tool can be turned on.
//
// POST ?name=  → {agents: [ids]} — the full desired selection. The handler
//
//	diffs against current state and applies one toggle per change
//	through the shared ScopeProvider, so this behaves exactly like
//	the admin control it replaces.
//
// scopeAllAgents is the ScopeProvider's target for the user-wide pool. It rides
// in the chip list as a pseudo-agent because that is how it reads to a user:
// one more place a tool can be switched on, not a separate concept.
const scopeAllAgents = "global"

func (T *Gateways) handleUserToolAccess(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		http.Error(w, "missing name", http.StatusBadRequest)
		return
	}
	prov, ok := ScopeProviderFor("tool")
	if !ok {
		http.Error(w, "tool scope unavailable", http.StatusServiceUnavailable)
		return
	}
	db := AuthDB()

	switch r.Method {
	case http.MethodGet:
		// Shape for uiRenderScopePills: a PRIMARY pill (the user-wide pool —
		// "All my agents", which is also what puts a tool in the shared list
		// every agent draws from) plus one pill per agent the user owns.
		type pill struct {
			Key   string `json:"key"`
			Label string `json:"label"`
			On    bool   `json:"on"`
		}
		out := map[string]any{}
		st, found := prov.State(db, user, name)
		items := []pill{}
		if found {
			out["primary"] = map[string]any{"label": "All my agents", "on": st.Global}
			for _, a := range st.Agents {
				items = append(items, pill{Key: a.ID, Label: a.Name, On: a.On})
			}
			if st.Global {
				out["note"] = "Shared with every one of your agents. Turn an agent off to deny it there, or turn All my agents off to keep it only on the agents left on."
			} else {
				out["note"] = "Available only on the agents switched on. Turn on All my agents to share it with every agent you own."
			}
			if len(st.Missing) > 0 {
				out["note"] = "⚠ Missing dependency: " + strings.Join(st.Missing, ", ") + ". " + out["note"].(string)
			}
		} else {
			// Neither a session draft nor an orphan is in any scope yet, so there
			// is nothing to toggle OFF — the first pill switched ON is what keeps
			// (or re-homes) it. Both need the same pill list, so build it from the
			// user's agents rather than from the tool's own state.
			out["primary"] = map[string]any{"label": "All my agents", "on": false}
			seen := map[string]bool{}
			for _, d := range ListScopedTools(user) {
				if d.AgentID == "" || seen[d.AgentID] {
					continue
				}
				seen[d.AgentID] = true
				items = append(items, pill{Key: d.AgentID, Label: d.AgentName})
			}
			out["note"] = "Not kept yet — switch on an agent (or All my agents) to keep it."
			for _, o := range LoadOrphanedTempTools(db, user) {
				if o.Tool.Name == name {
					out["note"] = "Orphaned — the agent that held this tool was deleted. Switch on an agent (or All my agents) to re-home it, or Delete to discard."
					break
				}
			}
		}
		out["items"] = items
		// No-store: the pills re-GET immediately after each toggle to re-render,
		// and a cached body makes a just-toggled pill snap back.
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, out)

	case http.MethodPost:
		// One toggle: {target, on}. target "global" = the user-wide pool,
		// otherwise an agent id. Same contract as the admin scope endpoint, so
		// the shared pill renderer drives both unchanged.
		var body struct {
			Target string `json:"target"`
			On     bool   `json:"on"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
			return
		}
		target := strings.TrimSpace(body.Target)
		if target == "" {
			http.Error(w, "missing target", http.StatusBadRequest)
			return
		}
		if _, found := prov.State(db, user, name); !found {
			// Nothing exists to toggle, so switching a pill ON is what keeps or
			// re-homes it. Switching one OFF is a no-op.
			if !body.On {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			// Orphan first: it is already a committed tool that simply lost its
			// agent, so it re-homes rather than promotes.
			for _, o := range LoadOrphanedTempTools(db, user) {
				if o.Tool.Name != name {
					continue
				}
				if AdminRehomeOrphanTool == nil {
					http.Error(w, "re-homing unavailable", http.StatusServiceUnavailable)
					return
				}
				if err := AdminRehomeOrphanTool(db, user, name, target); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			sid, agentID := "", ""
			for _, d := range ListScopedTools(user) {
				if d.Scope == ScopeSessionTool && d.Tool.Name == name {
					sid, agentID = d.SessionID, d.AgentID
					break
				}
			}
			if sid == "" {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			promoteTarget := ScopeTargetGlobal
			if target != scopeAllAgents {
				promoteTarget = ScopeTargetAgent
				agentID = target // keep it on the agent whose pill was switched on
			}
			if _, err := PromoteScopedTool(user, agentID, sid, name, promoteTarget); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err := prov.Set(db, user, name, target, body.On); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// toolAccessPillsJS drives the Access pill list on My tools. The generic
// renderer lives in core/ui (uiRenderScopePills); this only knows which
// endpoint to talk to — the app-specific half, per the extension-registry rule.
const toolAccessPillsJS = `function(ctx){
  var r = (ctx && ctx.record) || {};
  var name = r.name;
  if(!name){ window.uiAlert && window.uiAlert('No tool selected.'); return; }
  var reload = ctx && ctx.reload;
  var qs = 'name=' + encodeURIComponent(name);
  window.uiOpenSimpleModal({
    title: 'Access: ' + name,
    width: '560px',
    mount: function(body){
      var host = document.createElement('div');
      body.appendChild(host);
      window.uiRenderScopePills(host, {
        load: function(){
          return fetch('api/tool-access?' + qs, {cache:'no-store'}).then(function(res){
            if(!res.ok) return res.text().then(function(t){ throw new Error(t || ('HTTP ' + res.status)); });
            return res.json();
          });
        },
        toggle: function(key, on){
          var target = (key === '__primary__') ? 'global' : key;
          return fetch('api/tool-access?' + qs, {
            method: 'POST',
            headers: {'Content-Type':'application/json'},
            body: JSON.stringify({ target: target, on: on })
          }).then(function(res){
            if(!res.ok) return res.text().then(function(t){ throw new Error(t || ('HTTP ' + res.status)); });
            if(reload) reload();
          });
        }
      });
    }
  });
}`
