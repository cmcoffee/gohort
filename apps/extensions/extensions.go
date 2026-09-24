// Package extensions is the per-user "capability plane" — the surfaces through
// which a user's agents reach outward: their own API credentials, the tools
// they've had built, the global-tool catalog they opt into, and (rendered here,
// served from /account for OAuth redirect-URI stability) their identity
// connections. It is the user-namespace counterpart to the admin's global
// credential/tool management: everything here is scoped to the calling user.
//
// Reached as its own dashboard tile. Account keeps identity + preferences
// (password, timezone, inbound API keys); Extensions owns outward reach.
package extensions

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/promotion"
	"github.com/cmcoffee/gohort/core/ui"
)

func init() { RegisterApp(new(Extensions)) }

type Extensions struct {
	AppCore
}

func (T Extensions) Name() string         { return "extensions" }
func (T Extensions) SystemPrompt() string { return "" }

// StoreName keeps the app on the data bucket it was born with. Extensions was
// gateways; every credential, tool grant and skill anyone has saved lives under
// that name, and a bucket cannot be renamed in place, so the app follows the
// data rather than the other way round. See core.AppStoreName and the same
// choice in Scribe.
func (T Extensions) StoreName() string { return "gateways" }
func (T Extensions) Desc() string {
	return "Apps: the capabilities your agents draw on, credentials, tools, skills, connections."
}
func (T *Extensions) Init() error { return T.Flags.Parse() }
func (T *Extensions) Main() error {
	Log("gateways is a dashboard-only app. Start with: gohort serve")
	return nil
}

// The app is called what it has always been called on screen. The package,
// the path and the tab now agree with it; only the data bucket still says
// gateways, because a bucket cannot be renamed (see StoreName).
func (T *Extensions) WebPath() string { return "/extensions" }
func (T *Extensions) WebName() string { return "Extensions" }
func (T *Extensions) WebDesc() string {
	return "Credentials, tools, skills, and connections your agents draw on to do their work."
}

// HubTab puts Extensions on the shared top-nav tab row alongside Agents, Bridges,
// and Knowledge — it's the per-user capability surface those agents draw on, so
// it belongs in the same hub. Ordered after the others.
func (T *Extensions) HubTab() (string, int) { return "Extensions", 40 }

// legacyGatewaysPath is where this app lived until it was renamed. Links,
// bookmarks and stored per-user grants still name it, so the old prefix
// redirects here and the grants that named it move over once.
const legacyGatewaysPath = "/gateways"

func (T *Extensions) Routes() {
	RegisterLegacyMount(legacyGatewaysPath, T.WebPath())
	if AuthDB != nil {
		if adb := AuthDB(); adb != nil {
			MigrateAppPathGrants(adb, legacyGatewaysPath, T.WebPath())
		}
	}
	T.HandleFunc("/api/credentials", T.handleCredentials)
	T.HandleFunc("/api/tools", T.handleUserTools)
	T.HandleFunc("/api/tool-access", T.handleUserToolAccess)
	T.HandleFunc("/api/tool-categories", T.handleUserToolCategories)
	T.HandleFunc("/api/promotions", T.handlePromotions)
	// The ACL picker's candidate list, served HERE rather than borrowed from
	// another app. It is two lines over a core helper, and pointing the picker
	// at a sibling app's URL would make sharing a skill depend on that app
	// being installed and enabled — and on a relative path resolving against
	// whatever page happened to be open, which is how a picker 404s.
	T.HandleFunc("/api/user-candidates", T.handleUserCandidates)
	T.HandleFunc("/api/global-tools", T.handleGlobalTools)
	T.HandleFunc("/api/skills", T.handleUserSkills)
	T.HandleFunc("/api/skills/", T.handleUserSkillOne)
	T.HandleFunc("/api/skill-tools", T.handleSkillToolOptions)
	T.HandleFunc("/api/skill-collections", T.handleSkillCollectionOptions)
	T.HandleFunc("/api/skill-playbook", T.handleSkillPlaybookRule)
	T.HandleFunc("/skill-playbook", T.handleSkillPlaybookPage)
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
func (T *Extensions) handleCredentials(w http.ResponseWriter, r *http.Request) {
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
			Secured         bool   `json:"secured"`
			// Lending is the owner's standing answer to "may this be lent at
			// all", as against the two lists below, which are who it is lent
			// to today. A decision about the key, not about an occasion.
			Lending      string `json:"lending"`
			LendingLabel string `json:"lending_label"`
			// The two share lists the pickers edit, plus the one-line summary
			// the table column reads. Who a key reaches is the fact this page
			// exists to let someone control, so it belongs in the list and not
			// only inside an edit form.
			SharedReadOnly  []string `json:"shared_read_only"`
			SharedReadWrite []string `json:"shared_read_write"`
			SharedSummary   string   `json:"shared_summary"`
			// Handover state. A button that stays live after the ask reads as
			// having done nothing, and a second request would just overwrite
			// the first — so the row says it is waiting instead.
			HandoverPending bool `json:"handover_pending"`
			CanHandOver     bool `json:"can_hand_over"`
		}
		toRow := func(c SecureCredential) row {
			return row{
				Name: c.Name, Type: c.Type, BaseURL: c.BaseURL, ParamName: c.ParamName,
				Description: c.Description, RequiresConfirm: c.RequiresConfirm,
				HasSecret: c.Type != SecureCredNone, Disabled: c.Disabled,
				Secured:         c.Secured,
				SharedReadOnly:  nonNilList(c.SharedReadOnly),
				SharedReadWrite: nonNilList(c.SharedReadWrite),
				SharedSummary:   shareSummary(c),
				Lending:         c.Lending,
				LendingLabel:    lendingListLabel(c),
				HandoverPending: handoverPending(user, c.Name),
				CanHandOver:     !handoverPending(user, c.Name),
			}
		}
		// ?audit=<name> is the owner's own dispatch ledger for their own key.
		// Sharing a credential and then having no way to see what went out
		// through it would be the worst of both: the owner carries the far
		// end's attribution and cannot read the one record that says who
		// actually made each call.
		if name := strings.TrimSpace(r.URL.Query().Get("audit")); name != "" {
			writeJSON(w, credentialLedgerRows(user, name))
			return
		}
		// ?lent=1 is the other side of the share: what other people gave THIS
		// user. A share nobody can see is one they cannot use and cannot reason
		// about when a call goes out under somebody else's name.
		if strings.TrimSpace(r.URL.Query().Get("lent")) != "" {
			writeJSON(w, lentCredentialRows(user))
			return
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
		// The share lists, and ONLY the share lists. Each picker posts the whole
		// record back (record mode) and this reads two fields out of it, so a
		// picker save cannot rewrite a base URL and the ordinary edit form
		// cannot rewrite who the key reaches. Each door opens one thing.
		if strings.TrimSpace(r.URL.Query().Get("action")) == "share" {
			name := strings.TrimSpace(r.URL.Query().Get("name"))
			if name == "" {
				http.Error(w, "missing name", http.StatusBadRequest)
				return
			}
			var body struct {
				SharedReadOnly  []string `json:"shared_read_only"`
				SharedReadWrite []string `json:"shared_read_write"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if err := Secure().SetCredentialShares(user, name, body.SharedReadOnly, body.SharedReadWrite); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			Log("[extensions] user=%q shared credential %q with %d reader(s) and %d writer(s)",
				user, name, len(body.SharedReadOnly), len(body.SharedReadWrite))
			w.WriteHeader(http.StatusNoContent)
			return
		}
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
			Secured         bool   `json:"secured"`
			Lending         string `json:"lending"`
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
			Lending:         body.Lending,
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
		// Secured rides its own setter because Save deliberately PRESERVES the
		// flag from the stored record — the field is owned by this toggle, not
		// by the form body, so an edit that never mentions it cannot clear it.
		// Same shape as Disabled above.
		if err := Secure().SetSecuredOwned(user, c.Name, body.Secured); err != nil {
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
func (T *Extensions) handleUserTools(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	// The recipient list, and ONLY the recipient list. Handing a colleague a
	// tool is the owner's own rung: it puts the tool in their catalog to take,
	// and loads for their agents once they take it. Refused for a tool that
	// dispatches through a SECURED credential, because that key's access
	// follows the tools an administrator bound to it.
	if r.Method == http.MethodPost && strings.TrimSpace(r.URL.Query().Get("action")) == "share" {
		var body struct {
			SharedWith []string `json:"shared_with"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := SetPersistentTempToolSharedWith(AuthDB(), user, name, body.SharedWith); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		Log("[extensions] user=%q shared tool %q with %d user(s)", user, name, len(body.SharedWith))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	switch r.Method {
	case http.MethodGet:
		// Single-record fetch (?name=) — powers the "View" RecordView and the
		// "Set category" form prefill. Returns the raw TempTool, whose json tags
		// already expose every field (category, command_template, script_body,
		// actions, …). Scoped to this user's own tools, and it must read the SAME
		// three sources the row listing below builds from — pool, agent-scoped,
		// then session drafts — or View 404s on a row the table just rendered.
		// Agent-scoped was the miss: once an authored tool started committing to
		// the agent that asked for it rather than to a session pool, nearly every
		// row was agent-scoped and every View failed.
		// (Sources can't collide — the scoped/draft listers drop anything
		// shadowed by a committed tool of the same name — but the pool is the
		// source of truth, so it answers first regardless.)
		// ?share=<name> is the recipient list and nothing else. Its own door,
		// so the picker cannot rewrite a command template and the edit form
		// cannot rewrite who has the tool.
		if want := strings.TrimSpace(r.URL.Query().Get("share")); want != "" {
			for _, p := range LoadPersistentTempTools(AuthDB(), user) {
				if p.Tool.Name == want {
					writeJSON(w, map[string]any{"shared_with": nonNilList(p.SharedWith)})
					return
				}
			}
			writeJSON(w, map[string]any{"shared_with": []string{}})
			return
		}
		if name != "" {
			for _, p := range LoadPersistentTempTools(AuthDB(), user) {
				if p.Tool.Name == name {
					writeJSON(w, p.Tool)
					return
				}
			}
			for _, st := range ListScopedTools(user) {
				if st.Shadowed || st.Scope != ScopeAgentTool {
					continue
				}
				if st.Tool.Name == name {
					writeJSON(w, st.Tool)
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
			// Key is the ROW's identity, unique across every bucket a name can
			// live in. RowKey used to be the bare name, and a name can exist in
			// several buckets at once — the pool row plus an orphan stashed when
			// its agent was deleted, say. Two rows sharing one key made the UI's
			// row identity flicker between the records: enable the pool copy and
			// the re-render showed the orphan's Disabled badge, which read as
			// "every time I enable it, it gets disabled." Renaming the tool
			// "fixed" it by dodging the twin — the tell that found this.
			Key string `json:"key"`
			// Conflict marks every row whose NAME exists in more than one bucket,
			// so the twin is visible instead of mysterious.
			Conflict bool `json:"conflict"`
			// Shadows says whose same-named tool this one hides: one the user
			// was lent or added from the catalog, which their own copy beats.
			Shadows     string `json:"shadows,omitempty"`
			Name        string `json:"name"`
			Description string `json:"description,omitempty"`
			Mode        string `json:"mode,omitempty"`
			Credential  string `json:"credential,omitempty"`
			Category    string `json:"category,omitempty"`
			Missing     bool   `json:"missing"`
			Shared      bool   `json:"shared"`
			// Who the OWNER handed it to, as against Shared, which is the
			// deployment catalog an admin publishes into. Two different rungs
			// and two different columns.
			SharedWith string `json:"shared_with,omitempty"`
			LastUsed   string `json:"last_used,omitempty"`
			// User-managed governance flags (Extensions › Tools toggles).
			Locked      bool `json:"locked"`       // frozen — AI can't modify/delete
			Disabled    bool `json:"disabled"`     // off for every agent
			BuilderOnly bool `json:"builder_only"` // exposed to Builder only
			BoundOnly   bool `json:"bound_only"`   // hidden from agents; usable only where bound
			// Promotion (publish-to-catalog) request state for this tool.
			Requested  bool `json:"requested"`   // a promotion request is pending admin review
			CanRequest bool `json:"can_request"` // eligible to request: not already shared, none pending
			// A published tool's release: what everybody else runs. The row is
			// the owner's working copy, which they may have edited since; an
			// edit reaches nobody until an update is approved.
			Release         string `json:"release,omitempty"` // "Published v3", "... - your copy differs"
			Differs         bool   `json:"differs"`           // working copy is not the published version
			Diff            string `json:"diff,omitempty"`    // published -> working copy, field by field
			CanUpdate       bool   `json:"can_update"`        // differs, and no update request pending
			UpdateRequested bool   `json:"update_requested"`  // an update request is pending admin review
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
			// DisableOK: this row supports the Disable/Enable toggle. True for pool
			// tools (global governance) AND agent-scoped tools (non-destructive
			// per-agent off) — the two the endpoint honors. Lock / Builder-only stay
			// pool-only, so they keep their own OnlyIf:"pool" gate.
			DisableOK bool `json:"disable_ok"`
			// Agents / AgentList name every agent this tool is scoped to. One row
			// per TOOL, not per (tool, agent) pair — Access is where the
			// per-agent state is changed; this is the at-a-glance version.
			Agents    []string `json:"agents,omitempty"`
			AgentList string   `json:"agent_list,omitempty"`
			// Deletable = has a record of its own to delete (pool or orphan). A
			// session draft is excluded: Discard is its verb, and DELETE would
			// 404 on a tool that lives only in a chat session.
			Deletable bool `json:"deletable"`
			// Exportable = kept in the user's store (pool or agent-scoped), so
			// the per-user export can resolve it. A session draft and an orphan
			// are records awaiting a decision, not things to hand somebody.
			Exportable bool   `json:"exportable"`
			SessionID  string `json:"session_id,omitempty"` // for the keep/drop actions
			AgentID    string `json:"agent_id,omitempty"`
			// Group is the heading this row renders under (ui.Table group_by).
			Group string `json:"group"`
		}
		rows := []row{}
		// The user's own published tools, by name: their releases.
		released := map[string]ToolRelease{}
		for _, rel := range ToolReleases(AuthDB()) {
			if rel.Owner == user {
				released[rel.Name] = rel
			}
		}
		// Shared rows only: agent-scoped rows live in the same store now, but
		// they render under "Scoped Tools" (via ListScopedTools), not here.
		for _, p := range SharedUserTools(AuthDB(), user) {
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
			// A pending request on an unpublished tool asks to publish it; on a
			// published one it asks for the working copy to become the next
			// version. Two different badges.
			pending := PendingPromotion(AuthDB(), user, "tool", p.Tool.Name)
			r := row{
				Key:  "pool:" + p.Tool.Name,
				Name: p.Tool.Name, Description: p.Tool.Description, Mode: p.Tool.Mode,
				Credential: p.Tool.Credential, Category: p.Tool.Category,
				Missing: missing, Shared: p.Shared, LastUsed: last,
				SharedWith: sharedWithSummary(p.SharedWith),
				Locked:     p.Tool.Locked, Disabled: p.Tool.Disabled, BuilderOnly: p.Tool.BuilderOnly, BoundOnly: p.Tool.BoundOnly,
				Requested: !p.Shared && pending, CanRequest: !p.Shared && !pending,
				Pool: true, Deletable: true, DisableOK: true,
				Group: "All Agents (Global tools)",
			}
			if rel, ok := released[p.Tool.Name]; ok && p.Shared && rel.ID == p.ID {
				r.Release = "Published v" + strconv.Itoa(rel.Version)
				if !rel.Tool.SameDefinition(p.Tool) {
					r.Differs, r.Diff = true, rel.Tool.DefinitionDiff(p.Tool)
					r.Release += " - your copy differs"
				}
				r.UpdateRequested = pending
				r.CanUpdate = r.Differs && !pending
			}
			rows = append(rows, r)
		}
		// Agent-scoped tools after the global pool. Sections read: All Agents ->
		// Scoped Tools -> legacy session drafts -> Orphaned.
		//
		// A tool on several agents is listed ONCE, with the agents named in its
		// own column — the same shape the admin table uses. Listing it per agent
		// meant the same tool appeared two or three times with identical actions,
		// and "which agents?" is a property of one tool, not a reason to repeat
		// it. The authoritative per-agent state lives in Access, where it can be
		// changed; the column is the at-a-glance version.
		//
		// Shadowed rows are dropped: a draft already committed under the same
		// name would invite an action that does nothing.
		scoped := ListScopedTools(user)
		// A sub-agent is labelled with its parent so it doesn't read as a
		// top-level agent of the same name.
		agentLabel := func(st ScopedTool) string {
			n := strings.TrimSpace(st.AgentName)
			if n == "" {
				n = st.AgentID
			}
			if p := strings.TrimSpace(st.ParentName); p != "" {
				return p + " ▸ " + n
			}
			return n
		}
		byName := map[string]int{} // tool name -> index into rows
		for _, st := range scoped {
			if st.Shadowed || st.Scope != ScopeAgentTool {
				continue
			}
			agent := agentLabel(st)
			if i, seen := byName[st.Tool.Name]; seen {
				rows[i].Agents = append(rows[i].Agents, agent)
				rows[i].AgentList = strings.Join(rows[i].Agents, ", ")
				// Unconfirmed only while EVERY copy is unconfirmed: vouching for
				// it on one agent is vouching for the tool.
				rows[i].Trial = rows[i].Trial && st.Trial
				// Disabled only while EVERY copy is off — if it's live on any agent,
				// the row reads enabled (and Enable/Disable acts on all copies).
				rows[i].Disabled = rows[i].Disabled && st.Tool.Disabled
				continue
			}
			missing := false
			if cred := strings.TrimSpace(st.Tool.Credential); cred != "" && !strings.EqualFold(cred, "no_auth") {
				if _, found := Secure().Resolve(cred, user); !found {
					missing = true
				}
			}
			byName[st.Tool.Name] = len(rows)
			rows = append(rows, row{
				Key:  "scoped:" + st.AgentID + ":" + st.Tool.Name,
				Name: st.Tool.Name, Description: st.Tool.Description, Mode: st.Tool.Mode,
				Credential: st.Tool.Credential, Category: st.Tool.Category, Missing: missing,
				AgentTool: true, Trial: st.Trial, Disabled: st.Tool.Disabled, DisableOK: true,
				AgentID: st.AgentID, Agents: []string{agent}, AgentList: agent,
				Group: "Scoped Tools",
			})
		}
		// Legacy session drafts keep their per-conversation heading: they are
		// keyed to one session by definition, and they only exist until that
		// conversation is reopened and they migrate.
		for _, st := range scoped {
			if st.Shadowed || st.Scope != ScopeSessionTool {
				continue
			}
			agent := agentLabel(st)
			group := "Session drafts (legacy): " + agent
			if t := strings.TrimSpace(st.SessionTitle); t != "" {
				group += " - " + t
			}
			rows = append(rows, row{
				Key:  "session:" + st.SessionID + ":" + st.Tool.Name,
				Name: st.Tool.Name, Description: st.Tool.Description, Mode: st.Tool.Mode,
				Credential: st.Tool.Credential, Category: st.Tool.Category,
				Session: true, SessionID: st.SessionID, AgentID: st.AgentID, Group: group,
			})
		}
		// Reap expired unconfirmed tools before listing, so the page never shows
		// a row it is about to delete. Opening Extensions › Tools is the moment a user is
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
				Key:  "orphan:" + o.Tool.Name,
				Name: o.Tool.Name, Description: o.Tool.Description, Mode: o.Tool.Mode,
				Credential: o.Tool.Credential, Category: o.Tool.Category, Missing: missing,
				Orphan: true, Deletable: true,
				Group: "Orphaned Tools: agent " + former + " was deleted",
			})
		}
		// Name-conflict pass: the same name living in more than one bucket is
		// exactly the state that made the old shared RowKey lie, and it stays
		// confusing even with unique keys — a toggle on one row does not touch
		// its twin. Badge every copy so the user can see there IS a twin and
		// delete or re-home the stale one, instead of fighting a record they
		// cannot see.
		//
		// A tool somebody else offers under the same name is a twin too, one the
		// user cannot see from this list: a colleague's they were lent, or one
		// they added from the catalog. Their own copy wins (on its agents, for a
		// scoped one), so the taken tool silently stops running; the badge and
		// the note say so.
		{
			names := map[string]int{}
			for i := range rows {
				names[rows[i].Name]++
			}
			taken := map[string]string{} // name -> whose tool the user took or was lent
			for _, p := range PeerSharedToolsFor(AuthDB(), user) {
				taken[p.Tool.Name] = p.Owner
			}
			for _, p := range AdoptedToolsFor(AuthDB(), user) {
				taken[p.Tool.Name] = p.Owner // the one that would load, when both
			}
			for i := range rows {
				if names[rows[i].Name] > 1 {
					rows[i].Conflict = true
				}
				_, builtin := LookupChatTool(rows[i].Name)
				other, took := taken[rows[i].Name]
				switch {
				case builtin || IsReservedToolName(rows[i].Name):
					// A tool named after a built-in never loads: hydration drops
					// it so the built-in keeps its name. Silent unless said here.
					rows[i].Conflict = true
					rows[i].Shadows = "Named after a built-in tool, which runs instead; rename it to use this one"
				case took && (rows[i].Pool || rows[i].AgentTool):
					rows[i].Conflict = true
					rows[i].Shadows = "Runs instead of " + other + "'s tool of this name"
					if rows[i].AgentTool {
						rows[i].Shadows = "Runs instead of " + other + "'s tool of this name, on these agents"
					}
				}
				rows[i].Exportable = (rows[i].Pool || rows[i].AgentTool) && !rows[i].Session && !rows[i].Orphan
			}
		}
		// Re-heading by what a tool IS FOR rather than where its record lives,
		// and re-ordering so the grouping (which follows record order — the
		// server owns it) renders cleanly.
		//
		// Scope answered "who can use this?", which the Agents column already
		// answers per row; with forty tools the scope headings mostly said
		// "these forty are yours" and the list read as one wall. Category is
		// the axis that actually splits it, and it is the axis the tool picker
		// and each app's tool list already use — so a tool now sits under the
		// same heading everywhere.
		//
		// Two lifecycle buckets keep their own headings instead of being filed
		// by subject: an orphan and a legacy session draft are not kinds of
		// tool, they are records awaiting a decision (re-home, keep, discard).
		// Filing them by subject would scatter them and lose the only thing the
		// user needs to see. They sort last, after everything healthy.
		rank := func(r row) int {
			switch {
			case r.Orphan:
				return 3
			case r.Session:
				return 2
			case strings.TrimSpace(r.Category) == "":
				return 1 // uncategorized sits after the named categories
			default:
				return 0
			}
		}
		for i := range rows {
			if rows[i].Orphan || rows[i].Session {
				continue // keep the lifecycle heading built at construction
			}
			if c := strings.TrimSpace(rows[i].Category); c != "" {
				rows[i].Group = c
			} else {
				rows[i].Group = "Uncategorized"
			}
		}
		sort.SliceStable(rows, func(i, j int) bool {
			ri, rj := rank(rows[i]), rank(rows[j])
			if ri != rj {
				return ri < rj
			}
			gi, gj := strings.ToLower(rows[i].Group), strings.ToLower(rows[j].Group)
			if gi != gj {
				return gi < gj
			}
			return strings.ToLower(rows[i].Name) < strings.ToLower(rows[j].Name)
		})
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
			if ConfirmAgentTool == nil {
				http.Error(w, "confirm unavailable", http.StatusServiceUnavailable)
				return
			}
			// Vouching for a tool is not per-agent: the row is one TOOL, so
			// confirm clears the mark on every agent holding a copy. Confirming
			// one and leaving the others unconfirmed would let the reaper take
			// copies of a tool the user just approved.
			targets := []string{}
			if agentID != "" {
				targets = append(targets, agentID)
			} else {
				for _, st := range ListScopedTools(user) {
					if st.Scope == ScopeAgentTool && st.Tool.Name == name && st.Trial {
						targets = append(targets, st.AgentID)
					}
				}
			}
			if len(targets) == 0 {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			for _, id := range targets {
				if err := ConfirmAgentTool(AuthDB(), user, id, name); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
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
		// withdraw — the owner taking their published tool back out of the
		// deployment catalog. Publishing is an administrator's to grant;
		// withdrawing is the owner's to do. Everyone who added it stops
		// loading it, and the last approved version is kept.
		if action == "withdraw" {
			if err := SetPersistentTempToolShared(AuthDB(), user, name, false); err != nil {
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
			// Agent-scoped fallback. The tool has no pool record — it lives on one
			// or more agent records — so the pool lookup above legitimately misses
			// it. Category is a property of the TOOL, not of one attachment, so it
			// writes to every agent holding a copy; the same reasoning confirm uses
			// above, and leaving copies on other agents under the old label would
			// scatter one tool across two headings.
			//
			// set_category, disable, and enable apply to a scoped tool by writing
			// the changed field back onto the agent record(s) that hold it (the
			// same load→patch→Attach the pool path uses via UpdatePersistentTempTool,
			// but owner-scoped to the agent). disable/enable are non-destructive:
			// the tool stays bundled — its definition survives and Builder can still
			// repair it — it just stops loading into the agent's kit (see the
			// tool.Disabled skip in runner.go). Lock / Builder-only remain pool-only.
			switch action {
			case "set_category", "disable", "enable":
			default:
				http.Error(w, "this action applies to pool tools only: "+name+" is scoped to an agent", http.StatusBadRequest)
				return
			}
			if AttachToolToAgent == nil {
				http.Error(w, "agent-scoped update unavailable", http.StatusServiceUnavailable)
				return
			}
			var body struct {
				Category string `json:"category"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			cat := strings.TrimSpace(body.Category)
			updated := 0
			for _, st := range ListScopedTools(user) {
				if st.Shadowed || st.Scope != ScopeAgentTool || st.Tool.Name != name {
					continue
				}
				t := st.Tool // copy; Attach replaces the whole record on that agent
				switch action {
				case "set_category":
					t.Category = cat
				case "disable":
					t.Disabled = true
				case "enable":
					t.Disabled = false
				}
				if err := AttachToolToAgent(AuthDB(), user, st.AgentID, t); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				updated++
			}
			if updated == 0 {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNoContent)
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
		case "bound_only_on":
			tt.BoundOnly = true
			// Mutually exclusive with Builder-only, which is a different
			// statement: one reserves a tool for authoring, the other says it
			// belongs to whatever binds it. Holding both would leave the
			// selector describing a state no filter produces.
			tt.BuilderOnly = false
		case "bound_only_off":
			tt.BoundOnly = false
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
// delete, mirroring "Extensions › Tools". GET lists; POST ?action=enable|disable mutes/unmutes
// without a full round-trip; DELETE removes one.
func (T *Extensions) handleUserSkills(w http.ResponseWriter, r *http.Request) {
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
			if own, ok := findOwnSkill(user, id); ok {
				s := own.SkillRecord
				out := map[string]any{
					"id":           s.ID,
					"name":         s.Name,
					"description":  s.Description,
					"triggers":     strings.Join(s.Triggers, "\n"),
					"instructions": s.Instructions,
					// Two doors, both here: the rules as JSON to edit in
					// place (what the admin form offers), and the address of
					// the visual editor for anyone who would rather answer
					// questions than write braces.
					// Twice, deliberately: one copy is the read-only view,
					// the other is what the textarea edits once Edit JSON
					// is on. One field cannot be both without the form
					// echoing its own display back into the payload.
					"playbook_text": playbookText(s),
					"playbook_url":  playbookEditorURL(s.ID),
				}
				// ?view=form is the behaviour form's own load. It leaves
				// the grants out: a form posts back the whole record it
				// loaded, so a grant on the wire here would be written back
				// on Save as it stood when the row opened, undoing any chip
				// flipped since.
				if r.URL.Query().Get("view") != "form" {
					out["allowed_tools"] = nonNilStrings(s.AllowedTools)
					out["attached_collections"] = nonNilStrings(s.AttachedCollections)
					// The picker reads what it will post back, so the field
					// has to be on the wire or it opens empty and the first
					// save silently clears the share.
					out["allowed_users"] = nonNilStrings(s.AllowedUsers)
				}
				writeJSON(w, out)
				return
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
			// The rule count, and the editor it links to: the count is the
			// way in, rather than a separate control on every row.
			Playbook    string `json:"playbook"`
			PlaybookURL string `json:"playbook_url"`
			// Where this skill sits on the three rungs. A published skill has
			// left the author's pool, so without listing it here they would
			// watch it disappear and have no way back.
			Published      bool `json:"published"`
			PublishPending bool `json:"publish_pending"`
			CanPublish     bool `json:"can_publish"`
		}
		toSkillRow := func(s SkillRecord, published bool) row {
			updated := ""
			if !s.Updated.IsZero() {
				updated = s.Updated.Format("2006-01-02")
			}
			pending := !published && skillPublishPending(user, s.Name)
			return row{
				ID: s.ID, Name: s.Name, Description: s.Description,
				Triggers: len(s.Triggers), Disabled: s.Disabled, Updated: updated,
				Playbook: playbookCount(s), PlaybookURL: playbookEditorURL(s.ID),
				Published: published, PublishPending: pending,
				CanPublish: !published && !pending,
			}
		}
		// ?deployment=1 is what the deployment publishes, for everybody. These
		// activate on your turns whether or not you went looking for them, so
		// there is a page that says which.
		if strings.TrimSpace(r.URL.Query().Get("deployment")) != "" {
			rows := []row{}
			for _, s := range DeploymentSkills(AuthDB()) {
				rows = append(rows, toSkillRow(s, true))
			}
			writeJSON(w, rows)
			return
		}
		rows := []row{}
		for _, s := range LoadSkills(AuthDB(), user) {
			rows = append(rows, toSkillRow(s, false))
		}
		// Plus the ones this user published, which left their pool for the
		// deployment's. Still theirs to edit and to take back, and a muted one
		// is listed too: DeploymentSkills hides it from everybody's turns, and
		// hiding it from its author would leave no row to Enable it from.
		for _, s := range PublishedSkillsBy(AuthDB(), user) {
			rows = append(rows, toSkillRow(s, true))
		}
		writeJSON(w, rows)
	case http.MethodPost:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		action := strings.TrimSpace(r.URL.Query().Get("action"))
		// Taking a published skill back is the author's alone: nobody needs
		// permission to stop publishing something they wrote. It returns to
		// their own pool, without the bundled tools, which were dropped when it
		// went out and are not the record everybody has been using.
		if action == "unpublish" {
			if id == "" {
				http.Error(w, "missing id", http.StatusBadRequest)
				return
			}
			if err := NarrowSkillToOwner(AuthDB(), user, id); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// enable/disable — mute/unmute without touching the definition.
		if action == "enable" || action == "disable" {
			if id == "" {
				http.Error(w, "missing id", http.StatusBadRequest)
				return
			}
			found, ok := findOwnSkill(user, id)
			if !ok {
				http.Error(w, "skill not found", http.StatusNotFound)
				return
			}
			found.Disabled = (action == "disable")
			if _, err := found.save(); err != nil {
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
			Name         string  `json:"name"`
			Description  string  `json:"description"`
			Triggers     string  `json:"triggers"`
			Instructions string  `json:"instructions"`
			PlaybookText *string `json:"playbook_text"`
			// Pointers, so a form that did not carry a grant leaves it alone
			// while one that carried an empty list clears it. The chip
			// pickers post the whole record; the behaviour form posts neither.
			AllowedTools        *[]string `json:"allowed_tools"`
			AttachedCollections *[]string `json:"attached_collections"`
			// Who else may USE this skill. Same absent-means-unchanged rule as
			// the two above, so the behaviour form does not wipe a share it
			// never showed.
			AllowedUsers *[]string `json:"allowed_users"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		// A POINTER, so a form that did not carry the field (the Add form, a
		// chip picker) leaves the rules alone, while one that carried it empty
		// clears them. Validated here rather than at the store: the error has
		// to name the rule while the person still has it on screen.
		var playbook []PlaybookRule
		if body.PlaybookText != nil && strings.TrimSpace(*body.PlaybookText) != "" {
			if err := json.Unmarshal([]byte(*body.PlaybookText), &playbook); err != nil {
				http.Error(w, "playbook rules: not a JSON array: "+err.Error(), http.StatusBadRequest)
				return
			}
			if probs := (SkillRecord{Playbook: playbook}).PlaybookProblems(); len(probs) > 0 {
				http.Error(w, "playbook rules: "+strings.Join(probs, "; "), http.StatusBadRequest)
				return
			}
		}
		name := strings.TrimSpace(body.Name)
		if name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		// A create (no id) always lands in the caller's own pool.
		rec := ownedSkill{user: user}
		if id != "" {
			found, ok := findOwnSkill(user, id) // preserves Tools / AllowedTools / AttachedCollections
			if !ok {
				http.Error(w, "skill not found", http.StatusNotFound)
				return
			}
			rec = found
		}
		if rec.published && body.AllowedUsers != nil && len(*body.AllowedUsers) > 0 {
			http.Error(w, publishedShareRefusal, http.StatusBadRequest)
			return
		}
		// One name, one skill, per person: a create or a rename onto a name
		// they already have (here or published) is refused. Two of their
		// skills under one name is a name that picks neither, in this list and
		// in every agent that names it. An edit that keeps its name is not
		// checked, so a duplicate older data already holds can still be fixed.
		if id == "" || !strings.EqualFold(strings.TrimSpace(rec.Name), name) {
			if other, taken := ownSkillNamed(user, name, id); taken {
				http.Error(w, "you already have a skill called "+other.Name+"; pick another name, or edit that one", http.StatusConflict)
				return
			}
		}
		rec.Name = name
		rec.Description = strings.TrimSpace(body.Description)
		rec.Instructions = body.Instructions
		rec.Triggers = splitSkillTriggers(body.Triggers)
		if body.PlaybookText != nil {
			rec.Playbook = playbook // nil when the field came through blank — clears
		}
		if body.AllowedTools != nil {
			rec.AllowedTools = *body.AllowedTools
		}
		if body.AttachedCollections != nil {
			rec.AttachedCollections = *body.AttachedCollections
		}
		if body.AllowedUsers != nil {
			rec.AllowedUsers = *body.AllowedUsers
		}
		if _, err := rec.save(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPatch:
		// The grant pickers: each sends only its own field, so flipping one
		// chip cannot write back another picker's list, or the behaviour
		// form's text, as they stood when the row opened.
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		var body struct {
			AllowedTools        *[]string `json:"allowed_tools"`
			AttachedCollections *[]string `json:"attached_collections"`
			AllowedUsers        *[]string `json:"allowed_users"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if body.AllowedTools == nil && body.AttachedCollections == nil && body.AllowedUsers == nil {
			http.Error(w, "nothing to change", http.StatusBadRequest)
			return
		}
		rec, found := findOwnSkill(user, id)
		if !found {
			http.Error(w, "skill not found", http.StatusNotFound)
			return
		}
		if rec.published && body.AllowedUsers != nil && len(*body.AllowedUsers) > 0 {
			http.Error(w, publishedShareRefusal, http.StatusBadRequest)
			return
		}
		if body.AllowedTools != nil {
			rec.AllowedTools = *body.AllowedTools
		}
		if body.AttachedCollections != nil {
			rec.AttachedCollections = *body.AttachedCollections
		}
		if body.AllowedUsers != nil {
			rec.AllowedUsers = *body.AllowedUsers
		}
		if _, err := rec.save(); err != nil {
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

// ownedSkill is one of the caller's skills and which pool it is in. A
// published skill has left its author's pool for the deployment's, but it is
// still theirs to edit, mute and take back, so every lookup the author's own
// surfaces make has to see both pools. Looking only in the author's pool is
// what made Edit and Disable answer 404 on a row the same table had just
// listed.
type ownedSkill struct {
	SkillRecord
	user      string
	published bool
}

// publishedShareRefusal is the answer to naming recipients on a published
// skill: everybody already has it, and the publication drops the list anyway.
const publishedShareRefusal = "this skill is published to everybody; take it back first to share it with named people"

// findOwnSkill looks up id among user's skills: their own pool first, then the
// ones they published.
func findOwnSkill(user, id string) (ownedSkill, bool) {
	for _, s := range LoadSkills(AuthDB(), user) {
		if s.ID == id {
			return ownedSkill{SkillRecord: s, user: user}, true
		}
	}
	for _, s := range PublishedSkillsBy(AuthDB(), user) {
		if s.ID == id {
			return ownedSkill{SkillRecord: s, user: user, published: true}, true
		}
	}
	return ownedSkill{}, false
}

// ownSkillNamed returns the user's skill, other than except, that already
// answers to name, in either pool they write to.
func ownSkillNamed(user, name, except string) (SkillRecord, bool) {
	for _, s := range append(LoadSkills(AuthDB(), user), PublishedSkillsBy(AuthDB(), user)...) {
		if s.ID != except && strings.EqualFold(strings.TrimSpace(s.Name), strings.TrimSpace(name)) {
			return s, true
		}
	}
	return SkillRecord{}, false
}

// save writes the record back. SaveSkill itself sends a published one to the
// deployment pool, so there is one save whichever pool it came from.
func (o ownedSkill) save() (SkillRecord, error) {
	return SaveSkill(AuthDB(), o.user, o.SkillRecord)
}

// handlePromotions lets a user request that one of their OWN resources be
// published deployment-wide (bottom-up escalation — an admin approves it on the
// Administrator page). For a tool the request asks the admin to publish it to
// the global catalog, or, when it is published already, to make the owner's
// current copy its next version (core freezes that copy as the request is
// filed, and refuses a request with no change in it). POST
// ?kind=tool&name=<tool> with an optional JSON {note}; owner is the session
// user, who must own the tool.
func (T *Extensions) handlePromotions(w http.ResponseWriter, r *http.Request) {
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
	switch kind {
	case "tool", CredentialPromotionKind, SkillPromotionKind:
	default:
		http.Error(w, "only tool, credential and skill promotion are available", http.StatusBadRequest)
		return
	}
	if name == "" {
		http.Error(w, "missing name", http.StatusBadRequest)
		return
	}
	// Ownership: whatever is being handed over has to be the caller's.
	owns := false
	switch kind {
	case CredentialPromotionKind:
		_, owns = Secure().LoadUser(user, name)
	case SkillPromotionKind:
		// By NAME, which is what the request carries and what the admin reads.
		_, owns = FindSkillByName(AuthDB(), user, name)
	default:
		for _, p := range LoadPersistentTempTools(AuthDB(), user) {
			if p.Tool.Name == name {
				owns = true
				break
			}
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
//
// Adding a colleague's tool again when it is already added accepts their
// update: the copy the user runs becomes the colleague's current definition.
func (T *Extensions) handleGlobalTools(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		// "Added" means that owner's tool is what the user's agents load, not
		// merely that the name is on their list: an adoption is pinned to the
		// owner it was taken from.
		loadedFrom := map[string]string{}
		taken := map[string]LentTool{} // what the user's agents run, by name
		for _, p := range AdoptedToolsFor(AuthDB(), user) {
			loadedFrom[p.Tool.Name] = p.Owner
			taken[p.Tool.Name] = p
		}
		publishedBy := SharedToolOwners(AuthDB())
		versions := map[string]int{}
		for _, rel := range ToolReleases(AuthDB()) {
			versions[rel.Name] = rel.Version
		}
		// A tool already in the user's OWN pool (they authored it, and it may be
		// the one they published) is always active for them, and their own copy
		// wins over anything taken under the same name. Their own published tool
		// is not an offer to them at all, so it stays out of the catalog; a
		// same-named tool somebody ELSE offers is listed, marked as shadowed.
		// Hiding it left the user no way to see that a colleague's tool exists
		// under a name their own copy holds, or to remove an adoption their own
		// tool had quietly overtaken.
		own := map[string]bool{}
		for _, p := range LoadPersistentTempTools(AuthDB(), user) {
			own[p.Tool.Name] = true
		}
		type row struct {
			// Key is the row's identity: owner and name. A name can be offered
			// by more than one person (two colleagues lending tools of one
			// name, or a colleague and the deployment), and adoption is pinned
			// to whose it is, so every offer is its own row.
			Key         string `json:"key"`
			Name        string `json:"name"`
			Description string `json:"description,omitempty"`
			Mode        string `json:"mode,omitempty"`
			Credential  string `json:"credential,omitempty"`
			Adopted     bool   `json:"adopted"`
			Missing     bool   `json:"missing"`
			// Shadowed: the user has a tool of their own under this name, and
			// their own copy is the one their agents run (for a copy scoped to
			// some agents, on those agents).
			Shadowed bool `json:"shadowed"`
			// From names the colleague who handed it over, empty for one the
			// deployment publishes. Whose code you are about to run in your own
			// session is the first thing to know about it.
			From string `json:"from,omitempty"`
			// Owner is whose tool this row is, either way: what Add pins the
			// adoption to.
			Owner string `json:"owner"`
			// Version is a published tool's approved version ("v3"); a
			// colleague's tool has none.
			Version string `json:"version,omitempty"`
			// UpdateAvailable: the user took a colleague's tool, which runs as
			// the copy they took, and the colleague has changed it since. Diff
			// says how; Accept takes the new definition.
			UpdateAvailable bool   `json:"update_available"`
			Diff            string `json:"diff,omitempty"`
		}
		rows := []row{}
		missingCred := func(t TempTool) bool {
			cred := strings.TrimSpace(t.Credential)
			if cred == "" || strings.EqualFold(cred, "no_auth") {
				return false
			}
			_, found := Secure().Resolve(cred, user)
			return !found
		}
		listed := map[string]bool{} // owner + "/" + name
		// A colleague's tools first: the catalog is one list of things you may
		// take, and something handed to you personally is the more specific
		// entry.
		for _, p := range PeerSharedToolsFor(AuthDB(), user) {
			key := p.Owner + "/" + p.Tool.Name
			if listed[key] {
				continue
			}
			listed[key] = true
			r := row{
				Key:  key,
				Name: p.Tool.Name, Description: p.Tool.Description, Mode: p.Tool.Mode,
				Credential: p.Tool.Credential, Adopted: loadedFrom[p.Tool.Name] == p.Owner,
				Missing: missingCred(p.Tool), Shadowed: own[p.Tool.Name],
				From: p.Owner, Owner: p.Owner,
			}
			if t, ok := taken[p.Tool.Name]; ok && r.Adopted && t.Version == 0 && t.Update != nil {
				r.UpdateAvailable, r.Diff = true, t.Tool.DefinitionDiff(*t.Update)
			}
			rows = append(rows, r)
		}
		for _, p := range LoadSharedPersistentTempTools(AuthDB()) {
			owner := publishedBy[p.Tool.Name]
			// The user's own published tool is theirs already: it is listed
			// under "Extensions › Tools", not offered back to them here.
			if owner == user {
				continue
			}
			// Adopt-ACL: a restricted global tool only appears in the catalog for
			// users on its AllowedUsers list (empty = open to everyone). Keeps a
			// user from even seeing — let alone adopting — a tool not meant for them.
			if !CanAdoptGlobalTool(AuthDB(), user, p.Tool.Name) {
				continue
			}
			key := owner + "/" + p.Tool.Name
			if listed[key] {
				continue
			}
			listed[key] = true
			rows = append(rows, row{
				Key:  key,
				Name: p.Tool.Name, Description: p.Tool.Description, Mode: p.Tool.Mode,
				Credential: p.Tool.Credential, Adopted: owner != "" && loadedFrom[p.Tool.Name] == owner,
				Missing: missingCred(p.Tool), Shadowed: own[p.Tool.Name], Owner: owner,
				Version: "v" + strconv.Itoa(versions[p.Tool.Name]),
			})
		}
		writeJSON(w, rows)
	case http.MethodPost:
		// Accept the toggle either as a JSON body ({name, adopt}) or as query
		// params (?name=&adopt=true) — the latter lets a declarative table
		// RowAction button drive it with no client script.
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		owner := strings.TrimSpace(r.URL.Query().Get("owner"))
		adopt := r.URL.Query().Get("adopt") == "true"
		if name == "" {
			var body struct {
				Name  string `json:"name"`
				Owner string `json:"owner"`
				Adopt bool   `json:"adopt"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			name, owner, adopt = strings.TrimSpace(body.Name), strings.TrimSpace(body.Owner), body.Adopt
		}
		if err := SetGlobalToolAdopted(AuthDB(), user, name, owner, adopt); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// credentialFormFields is the shared field list for the "API credentials" add
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
		{Field: "requires_confirm", Label: "Require confirm before each call", Type: "toggle", Help: "When on, every agent call through this credential asks you to allow it first.",
			Detail: "Use it for anything that reaches real people or spends money."},
		{Field: "secured", Label: "Only tools that declare it", Type: "toggle",
			Help: "OFF: every one of your agents gets a fetch_url_<name> tool for this credential and can call the API directly. " +
				"ON: no such tool is generated, the credential is reachable only through tools you build that name it, so access follows those tools' scope rather than being open to everything you run. " +
				"The secret is never handed to tool code either way; calls are signed server-side."},
		{Field: "lending", Label: "May this be lent to other people?", Type: "select",
			Options: []ui.SelectOption{
				{Value: "", Label: "Not decided"},
				{Value: LendNone, Label: "Nobody — never lend this key"},
				{Value: LendRead, Label: "Readers only"},
				{Value: LendAny, Label: "Readers and writers"},
			},
			Help: "Your standing answer, as against who has it today.",
			Detail: "Sharing an agent asks about each credential it touches, and without this the answer is a decision you make afresh every time — so a key you would never lend is one careless pass through that flow away from being lent.\n\n" +
				"Nobody removes the lend options wherever they are offered, and refuses them wherever they are written. Readers only allows GET and HEAD through your key and refuses a write lend, which is the one that matters: what somebody writes through your credential arrives at the far end as YOU.\n\n" +
				"Tightening this takes back what it now forbids. Setting Nobody over a key two people hold takes it from both; narrowing to Readers only leaves them the key and takes the writing."},
		{Field: "description", Label: "Description", Type: "textarea", Rows: 2, Help: "Shown to your agents as the tool description."},
	}
}

// lendingListLabel is the policy as the credential list shows it. Blank for a
// key nobody could lend anyway, because a column repeating "not decided" down
// every row of a deployment that shares nothing is noise rather than a nudge.
func lendingListLabel(c SecureCredential) string {
	switch c.Lending {
	case LendNone:
		return "Never lent"
	case LendRead:
		return "Lent for reads"
	case LendAny:
		return "Lent either way"
	}
	return ""
}

// userSkillFormFields is the Add/Edit form for a user's own skill. BEHAVIOR
// fields only — a form-authored skill is a pure instruction packet. Bundled
// tools, tool grants (AllowedTools), and attached collections are NOT here:
// those ship code or grant capability and stay Builder-authored. An edit
// preserves them (the handler load-then-mutates).
// playbookEditorURL is the editor's address, absolute. Relative would resolve
// against whatever page is showing — and the hub links to /extensions with no
// trailing slash, so a relative href lands at the site root instead.
func playbookEditorURL(skillID string) string {
	return "/extensions/skill-playbook?id=" + url.QueryEscape(skillID)
}

// playbookText is the skill's rules as the JSON the form edits. Empty for a
// skill with none, so the textarea opens blank rather than showing "null".
func playbookText(s SkillRecord) string {
	if len(s.Playbook) == 0 {
		return ""
	}
	raw, err := json.MarshalIndent(s.Playbook, "", "  ")
	if err != nil {
		return ""
	}
	return string(raw)
}

// playbookCount is what the playbook link says: how many rules there are, or
// an invitation when there are none. A count rather than the rules
// themselves — the rules are long, and the place to read them in full is the
// editor the link goes to.
func playbookCount(s SkillRecord) string {
	switch n := len(s.Playbook); n {
	case 0:
		return "add"
	case 1:
		return "1 rule"
	default:
		return fmt.Sprintf("%d rules", n)
	}
}

func userSkillFormFields() []ui.FormField {
	return []ui.FormField{
		{Field: "name", Label: "Name", Placeholder: "Contract Reviewer", Help: "Shown to your agents; also the H2 header above the instructions when the skill is active."},
		{Field: "description", Label: "Description", Help: "One line: when this skill applies. The assistant reads it to decide relevance."},
		{Field: "triggers", Label: "Triggers", Type: "textarea", Rows: 3, Placeholder: "contract\n*.pdf", Help: "Substring patterns, or *.ext for attachments, ONE PER LINE. Any match activates the skill.",
			Detail: "Leave it blank to rely on the description instead."},
		{Field: "instructions", Label: "Instructions", Type: "textarea", Rows: 12, Help: "Markdown appended to the assistant's prompt while the skill is active.",
			Detail: "The approach, voice, or method it should apply."},
		// The rules as they stand, then the two ways to change them. Read-only
		// until asked: the common visit is to look, and a textarea full of
		// JSON invites an accidental edit to something the editor writes
		// correctly.
		// Reads exactly like Instructions above: a preview of the value with an
		// Edit button that opens it in a modal. The extra button beside Edit is
		// the other way to write the same rules — by answering questions
		// instead of typing JSON.
		{Field: "playbook_text", Label: "Playbook", Type: "textarea", Rows: 8,
			Placeholder: "No rules yet: use Playbook Editor, or Edit to type them.",
			Links:       []ui.FormFieldLink{{Label: "Playbook Editor", Field: "playbook_url", Target: "_blank"}},
			Help:        "Conditional rules the framework settles BEFORE the assistant answers.",
			Detail:      "The shape is \"establish Y first; if yes do Z, if no do U\". It is a JSON array, and each rule looks like {\"fact\": \"queue_draining\", \"how\": \"Read the consumer lag.\", \"then\": \"Look at the consumer.\", \"else\": \"Look at the broker.\"}.\n\nOptional keys: \"when\" takes a list of triggers; \"type\": \"choice\" takes \"values\" and \"cases\"; \"then_rule\" and \"else_rule\" nest one level."},
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

func (T *Extensions) servePage(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	sections := []ui.Section{
		{
			Title:    "API credentials",
			Wide:     true,
			Subtitle: "API keys you own and manage yourself.",
			Detail: "They live in your namespace: no other user can reach them, and they never appear on the admin page." +
				"By default every one of your agents gets a fetch_url_<name> tool for each; turn on \"Only tools that declare it\" to narrow a credential to the tools you build for it. " +
				"Secrets are stored encrypted and never shown to the assistant.",
			Body: ui.Stack{Children: []ui.Component{
				ui.Table{
					Source: "api/credentials",
					RowKey: "name",
					Columns: []ui.Col{
						{Field: "name", Flex: 1},
						{Field: "type", Mute: true},
						{Field: "base_url", Label: "Base URL", Mute: true, Flex: 2},
						// Which credentials are open to every agent is the fact
						// this page exists to let someone control, so it belongs
						// in the list rather than one edit form at a time.
						{Field: "secured", Label: "Reach", Type: "badge", Badges: []ui.BadgeMapping{
							{Value: true, Label: "Tools only", Color: "success"},
							{Value: false, Label: "All my agents", Color: "warning"},
						}},
						{Field: "lending_label", Label: "Lending", Mute: true},
						{Field: "shared_summary", Label: "Shared", Mute: true},
						{Field: "handover_pending", Label: "", Type: "badge", Badges: []ui.BadgeMapping{
							{Value: true, Label: "Handover pending", Color: "warning"},
						}},
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
						// Two grants, two pickers, because the risk is not the
						// same on both sides. Lending a key for reads hands
						// somebody data they could have asked you for. Lending
						// one that writes means the page, the ticket and the
						// comment all say YOU did it, and nothing downstream can
						// tell otherwise — so it is a separate decision, made
						// deliberately, and not a checkbox on the first one.
						// The other half of lending a key: what went out through
						// it, and who sent it. An owner answerable for calls made
						// under their name needs the one record that tells the
						// two apart.
						ui.Expand("Recent calls", ui.Table{
							Source: "api/credentials?audit={name}",
							RowKey: "when",
							Columns: []ui.Col{
								{Field: "when", Label: "When", Flex: 1},
								{Field: "who", Label: "Who", Flex: 1},
								{Field: "method", Label: "Method", Mute: true},
								{Field: "url", Label: "URL", Flex: 3, Mute: true},
								{Field: "outcome", Label: "Outcome", Flex: 1},
							},
							EmptyText: "Nothing has been sent through this credential yet.",
						}),
						ui.Expand("Share for reads", ui.ACLPicker(ui.ACLPickerConfig{
							OptionsSource: "api/user-candidates",
							RecordSource:  "api/credentials?name={name}",
							Field:         "shared_read_only",
							PostTo:        "api/credentials?action=share&name={name}",
							Method:        "POST",
							Noun:          "user",
							Intro: "They can read through your key: GET and HEAD, nothing else. " +
								"Your own use of it is unchanged. Every call is logged with their name against it.",
							EmptyText:  "No other users to share with yet.",
							Invalidate: []string{"api/credentials"},
						})),
						ui.Expand("Share for writes", ui.ACLPicker(ui.ACLPickerConfig{
							OptionsSource: "api/user-candidates",
							RecordSource:  "api/credentials?name={name}",
							Field:         "shared_read_write",
							PostTo:        "api/credentials?action=share&name={name}",
							Method:        "POST",
							Noun:          "user",
							Intro: "They can write through your key, and what they write arrives as YOU: " +
								"the page says you edited it, the ticket says you commented. " +
								"The ledger records who actually made each call, which is the only place the two can be told apart. Share this with people you would let post under your name.",
							EmptyText:  "No other users to share with yet.",
							Invalidate: []string{"api/credentials"},
						})),
						// Handing it over is a different ask from lending it, and
						// the form says so before the ask rather than after. Sharing
						// widens who may use something that stays yours; this ends
						// the ownership, and there is no way back from it that is
						// the former owner's to take.
						ui.ModalActionIf("Hand to the deployment", "can_hand_over", "", ui.FormPanel{
							SubmitLabel: "Ask an admin",
							PostURL:     "api/promotions?kind=credential&name={name}",
							Fields: []ui.FormField{
								{Type: "header", Label: "This stops being your credential",
									Help: "It becomes the deployment's, and taking it back is not yours to do.",
									Detail: "Sharing widens who may use a key that stays yours. This ends the ownership: the secret moves into the deployment's namespace and the credential lands SECURED, which means it has no user list at all — it is reachable only through the tools bound to it, and you reach it the same way everybody else does.\n\n" +
										"That is what lets a tuned agent be handed over as a resource rather than as a copy somebody has to reassemble: the key underneath belongs to the work instead of to a person.\n\n" +
										"The tools that already dispatch through it become its bindings. A tool that is still yours alone stays yours alone, so share each one from Tools for your colleagues to reach the key through it. Any tool that took the raw key into a script stops working, because a secured credential never hands the secret out.\n\n" +
										"Anyone you lent this key to loses their lend, and an admin has to agree before any of it happens."},
								{Field: "note", Type: "textarea", Rows: 3, Label: "Note for the admin (optional)",
									Placeholder: "What is this key for, and who needs to reach it?"},
							},
							Invalidate: []string{"api/credentials"},
						}),
						{Type: "button", Label: "Delete", Method: "DELETE",
							PostTo:     "api/credentials?name={name}",
							Variant:    "danger",
							Confirm:    "Delete this credential? Agents and tools using it stop working, and anyone you shared it with loses it.",
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
			Title:    "Shared with you",
			Wide:     true,
			Subtitle: "Keys other people lent you.",
			Detail: "You never see the secret. Each one appears to your agents as a fetch_url_<name> tool, and a key of your own with the same name wins over a lent one. " +
				"Reads-only means GET and HEAD; anything else is refused and the refusal is recorded. " +
				"Where the grant includes writes, what you send arrives at the far end as the person who lent it, under their name.",
			Body: ui.Table{
				Source: "api/credentials?lent=1",
				RowKey: "name",
				Columns: []ui.Col{
					{Field: "name", Flex: 1},
					{Field: "owner", Label: "Lent by"},
					{Field: "grant", Label: "You may", Flex: 1},
					{Field: "tool", Label: "Tool", Mute: true, Flex: 1},
					{Field: "base_url", Label: "Base URL", Mute: true, Flex: 2},
				},
				EmptyText: "Nobody has shared a credential with you.",
			},
		},
		{
			Title:    "Connected accounts",
			Wide:     true,
			Subtitle: "Integrations you authorize with your own account, reading or writing as you.",
			Detail:   "Your key is stored encrypted and is never shown to the assistant.",
			Body:     ui.Card{HTML: connectionsHTML},
		},
		{
			Title:    "Tools",
			Subtitle: "Everything built for you, grouped by category.",
			Detail:   "The category is the same heading a tool appears under in the tool picker and each app's tool list. Categories are assigned from the Categories list directly below this table: open one and tick its tools. Tools that have not claimed one sit under \"Uncategorized\".\n\nThe Agents column says who can use each tool, where blank means your global pool and every agent, and Access is where you change that.\n\nA tool of yours in the deployment catalog runs for everyone else as the version an administrator approved. Your edits change your own copy; Request update asks for them to become the next version.\n\nTools the assistant authored but nobody has vouched for are badged Unconfirmed, and are dropped automatically if left that way. \"Orphaned Tools\" lost their agent when it was deleted. Filter the list with the box above.",
			// Tools first, then the categories that head them. Categories used to
			// be their own rail section, which put the fix one navigation away
			// from the problem: you read "Uncategorized" in this table and had to
			// leave the page to do anything about it. A category exists only to be
			// a heading in the list above it, so it belongs under that list.
			Body: ui.Stack{Children: []ui.Component{importToolbar("Bring in tools or skills somebody exported"), ui.Table{
				Source:            "api/tools",
				RowKey:            "key",
				Search:            true,
				SearchPlaceholder: "Filter tools by name, agent, category…",
				// Rows arrive pre-ordered and grouped by CATEGORY (see the
				// regroup pass in the GET handler), with the lifecycle buckets —
				// legacy session drafts, then orphans — sorted last. "What is
				// this tool for?" is how a forty-row list is actually read;
				// "which agent has it?" is the Agents column. Grouping follows
				// record order, so the server owns it.
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
						{Value: true, Label: "In the catalog", Color: "info"},
					}},
					// Which version everybody else runs, and whether this copy
					// has moved on from it.
					{Field: "release", Label: "", Mute: true, Flex: 1},
					{Field: "shared_with", Label: "", Mute: true, Flex: 1},
					{Field: "requested", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Publish requested", Color: "warning"},
					}},
					{Field: "update_requested", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Update requested", Color: "warning"},
					}},
					{Field: "conflict", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Name conflict", Color: "danger"},
					}},
					{Field: "shadows", Label: "", Mute: true, Flex: 1},
					{Field: "missing", Label: "Deps", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "⚠ missing", Color: "danger"},
					}},
					{Field: "locked", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "🔒 Locked", Color: "info"},
					}},
					{Field: "disabled", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Disabled", Color: "danger"},
					}},
					{Field: "bound_only", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Bound only", Color: "info"},
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
					// Which agents a scoped tool is on. Blank for pool tools (every
					// agent) and orphans (none) — the group heading already says so.
					{Field: "agent_list", Label: "Agents", Mute: true, Flex: 1},
					{Field: "description", Mute: true, Flex: 1},
				},
				RowActions: []ui.RowAction{
					{Type: "button", Label: "Export", Method: "client",
						PostTo: "export_tool", OnlyIf: "exportable"},
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
					// (Set category moved out of the rows: the Categories section's
					// category-first picker is the assignment surface — one list to
					// tick beats opening forty rows, and two surfaces for one label
					// invited the "Calendar" vs "calendars" split. The set_category
					// API action stays for Builder and compatibility.)
					// Request to publish — ask an admin to Share this tool to the
					// deployment-wide catalog. Only when it isn't already shared and
					// has no request pending (can_request).
					// The owner's own rung, beside the request for the wider one.
					// Handing somebody a tool is yours to do; putting it in the
					// deployment catalog is an admin's.
					ui.ExpandIf("Share with users", "pool", "", ui.ACLPicker(ui.ACLPickerConfig{
						OptionsSource: "api/user-candidates",
						RecordSource:  "api/tools?share={name}",
						Field:         "shared_with",
						PostTo:        "api/tools?action=share&name={name}",
						Method:        "POST",
						Noun:          "user",
						Intro: "They can take this tool into their own catalog, and it runs in THEIR session against their own credentials. " +
							"Nothing loads for their agents until they take it: a share is an offer, not a push.",
						EmptyText:  "No other users to share with yet.",
						Invalidate: []string{"api/tools"},
					})),
					ui.ModalActionIf("Request to publish", "can_request", "", ui.FormPanel{
						SubmitLabel: "Send request",
						PostURL:     "api/promotions?kind=tool&name={name}",
						Fields: []ui.FormField{
							{Field: "note", Type: "textarea", Rows: 3, Label: "Note for the admin (optional)",
								Placeholder: "Why should this tool be in the shared catalog?"},
						},
						Invalidate: []string{"api/tools"},
					}),
					// A published tool's edits reach only its owner's agents;
					// everyone else runs the approved version. These say what
					// changed and ask for it to become the next version, which
					// an admin reviews against the same diff.
					ui.ExpandIf("What changed", "differs", "", ui.RecordView{
						Pairs: []ui.DisplayPair{
							{Label: "Published version -> your copy", Field: "diff", Block: true},
						},
					}),
					ui.ModalActionIf("Request update", "can_update", "", ui.FormPanel{
						SubmitLabel: "Send request",
						PostURL:     "api/promotions?kind=tool&name={name}",
						Fields: []ui.FormField{
							{Field: "note", Type: "textarea", Rows: 3, Label: "Note for the admin (optional)",
								Placeholder: "What changed, and why everyone should get it?"},
						},
						Invalidate: []string{"api/tools"},
					}),
					{Type: "button", Label: "Withdraw", Method: "POST",
						PostTo:  "api/tools?action=withdraw&name={name}",
						OnlyIf:  "shared",
						Confirm: "Take this tool out of the deployment catalog? Everyone who added it stops loading it. You keep your own copy.",
						Variant: "danger"},
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
						PostTo:     "api/tools?action=confirm&name={name}",
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
						PostTo: "api/tools?action=disable&name={name}", HideIf: "disabled", OnlyIf: "disable_ok"},
					{Type: "button", Label: "Enable", Method: "POST",
						PostTo: "api/tools?action=enable&name={name}", OnlyIf: "disabled"},
					// Builder-only moved into the Access modal (a pill alongside the
					// other access controls) — it is an access statement, and two
					// surfaces for one flag is how toggles fight each other. The
					// row badge stays as the at-a-glance state; the API actions
					// stay for compatibility.
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
				// Sub-heading for the categories block. Card is the escape hatch for
				// a heading the framework doesn't model; it borrows the two section
				// classes so this reads as a section within the section rather than
				// a stray second table.
				ui.Card{HTML: `<div class="ui-section-h" style="margin-top:1.6rem">Categories</div>` +
					`<div class="ui-section-sub">The headings used above, and the same ones the tool picker and each app's tool list use. ` +
					`Open one to tick the tools that belong in it, or start a new one and fill it in the same step. ` +
					`A tool holds one category, so filing it here moves it out of wherever it was.</div>`},
				ui.Table{
					Source: "api/tool-categories",
					RowKey: "name",
					Columns: []ui.Col{
						{Field: "name", Flex: 1},
						// The members ARE the category — show the names as pills
						// rather than a count plus a comma-joined mutter. A count
						// column earns its place when the list is too long to show;
						// these lists are a handful of tools, and the names answer
						// the only question anyone brings here ("what's in it?").
						{Field: "tools", Label: "Members", Flex: 3, Type: "pills"},
					},
					RowActions: []ui.RowAction{
						// Category-first assignment: the whole point. Picking from
						// one list beats opening each tool and setting a label.
						ui.Expand("Choose tools", ui.ACLPicker(ui.ACLPickerConfig{
							OptionsSource: "api/tool-categories?options=1",
							RecordSource:  "api/tool-categories?name={name}",
							Field:         "tools",
							PostTo:        "api/tool-categories?name={name}",
							Noun:          "tool",
							Intro:         "Tick the tools that belong under this heading. Unticking one clears its category: it does not delete anything.",
							EmptyText:     "You have no tools yet.",
							// Filing a tool changes the heading it sits under in the
							// table above, which is now on screen at the same time.
							Invalidate: []string{"api/tools", "api/tool-categories"},
						})),
					},
					EmptyText: "No categories yet. Add one below and tick the tools that belong in it.",
				},
				ui.ModalButton{
					Label:    "Add category",
					Title:    "New category",
					Subtitle: "Name it, then tick the tools that belong in it. A category exists because tools point at it: an empty one has nothing to show.",
					Width:    "560px",
					Body: ui.FormPanel{
						// The name is a field of this form, so it travels in the body.
						PostURL:     "api/tool-categories",
						SubmitLabel: "Create category",
						Fields: []ui.FormField{
							{Field: "name", Type: "text", Label: "Category name",
								Placeholder: "e.g. Calendar, Moltbook, Research",
								Suggestions: knownToolCategories(AuthDB(), user),
								Help:        "Reuse an existing name to add to that category, or type a new one."},
							// Ticked, not typed: these are tools that already
							// exist, and a name that misses files nothing under
							// the category — which then does not appear at all,
							// because a category exists only where tools point
							// at it. The failure is a category that seems not to
							// have saved.
							{Field: "tools", Type: "checklist", Label: "Tools",
								Options:     userToolCheckOptions(user),
								Placeholder: "(you have no tools to file yet)",
								Help:        "Tick what belongs under this heading. At least one.",
								Detail:      "A category with nothing pointing at it has nothing to show. You can change the set later from Choose tools."},
						},
						Invalidate: []string{"api/tool-categories", "api/tools"},
					},
				},
			}},
		},
		{
			Title:    "Skills",
			Subtitle: "Behavior packs your agents draw on.",
			Detail: "A skill is instructions the assistant applies when its triggers or description match the turn. Author or edit one right here (name, triggers, instructions, the tools it may call and the collections it may search), or ask Builder in Agents for skills that ship their own code.\n\n" +
				"Open a skill to give it a playbook: conditional rules (\"establish Y first; if yes do Z, if no do U\") that the framework runs and settles before the assistant answers. Disable to mute a skill without losing it; delete to retire it.\n\n" +
				"A skill reaches other people on three rungs: yours alone, shared with people you name, or published to the whole deployment. The first two are your own call; the third is an admin's, and a skill you published is listed here with a Deployment-wide badge and a Take back button.",
			Body: ui.Stack{Children: []ui.Component{
				importToolbar("Bring in skills or tools somebody exported"),
				ui.Table{
					Source: "api/skills",
					RowKey: "id",
					Columns: []ui.Col{
						{Field: "name", Flex: 1},
						{Field: "description", Mute: true, Flex: 2},
						{Field: "triggers", Label: "Triggers", Mute: true},
						// How many conditional rules this skill carries, and the
						// way into them: the count is the link.
						{Field: "playbook", Label: "Playbook", Link: "playbook_url", Mute: true},
						{Field: "published", Label: "", Type: "badge", Badges: []ui.BadgeMapping{
							{Value: true, Label: "Deployment-wide", Color: "info"},
						}},
						{Field: "publish_pending", Label: "", Type: "badge", Badges: []ui.BadgeMapping{
							{Value: true, Label: "Publish requested", Color: "warning"},
						}},
						{Field: "disabled", Label: "Status", Type: "dot", Badges: []ui.BadgeMapping{
							{Value: true, Label: "Disabled", Color: "danger"},
							{Value: false, Label: "Active", Color: "success"},
						}},
					},
					RowActions: []ui.RowAction{
						// Edit the skill's behavior fields. Source prefills; the id
						// rides in the PostURL so the handler load-then-mutates
						// (preserving any Builder-authored tools/grants).
						ui.Expand("Edit", ui.Stack{Children: []ui.Component{
							ui.FormPanel{
								Source:      "api/skills?id={id}&view=form",
								PostURL:     "api/skills?id={id}",
								SubmitLabel: "Save skill",
								Fields:      userSkillFormFields(),
								Invalidate:  []string{"api/skills"},
								// The last few edits, with a read-only preview
								// of each. {id} is filled in when the row
								// expands, the same as the urls above it.
								HistoryURL:   "api/skills/{id}/revisions",
								HistoryLabel: "Version history",
							},
							// The two grants, as pickers rather than typed
							// names. Their own controls, posting the record
							// back on each flip: a chip is a decision, and
							// making it wait for a Save button underneath a
							// long form is how it gets lost.
							ui.Card{HTML: `<div style="font-size:0.78rem;color:var(--text-mute);text-transform:uppercase;letter-spacing:0.04em;margin-top:0.8rem">Allowed tools</div><div style="font-size:0.75rem;color:var(--text-mute)">Tools the assistant may call while this skill is in use. None selected means it uses whatever the agent already has.</div>`},
							ui.ChipPicker{
								OptionsSource: "api/skill-tools",
								RecordSource:  "api/skills?id={id}",
								Field:         "allowed_tools",
								PostTo:        "api/skills?id={id}",
								Method:        "PATCH",
								NameField:     "name",
								LabelField:    "name",
								DescField:     "description",
							},
							ui.Card{HTML: `<div style="font-size:0.78rem;color:var(--text-mute);text-transform:uppercase;letter-spacing:0.04em;margin-top:0.8rem">Attached collections</div><div style="font-size:0.75rem;color:var(--text-mute)">Document collections this skill can search. They stay out of scope on turns the skill is not in use.</div>`},
							ui.ChipPicker{
								OptionsSource: "api/skill-collections",
								RecordSource:  "api/skills?id={id}",
								Field:         "attached_collections",
								PostTo:        "api/skills?id={id}",
								Method:        "PATCH",
								NameField:     "id",
								LabelField:    "name",
								DescField:     "description",
							},
							// Peer sharing: named people, not everybody. Widening
							// anything to the whole deployment is an
							// administrator's decision; who you hand a skill to
							// is yours.
							ui.Card{HTML: `<div style="font-size:0.78rem;color:var(--text-mute);text-transform:uppercase;letter-spacing:0.04em;margin-top:0.8rem">Shared with</div><div style="font-size:0.75rem;color:var(--text-mute)">Other users who may use this skill. Empty means private to you. They get the behaviour, not the authorship: it activates on their turns and they cannot edit or delete it. Bundled tools do not travel, because that would run your code in their session. Attached collections do travel as references, and each resolves only for someone who can already read it.</div>`},
							ui.ACLPicker(ui.ACLPickerConfig{
								OptionsSource: "api/user-candidates",
								RecordSource:  "api/skills?id={id}",
								Field:         "allowed_users",
								PostTo:        "api/skills?id={id}",
								Method:        "PATCH",
								Noun:          "user",
								Intro:         "Users who may use this skill.",
								EmptyText:     "No other users to share with yet.",
							}),
						}}),
						// A published skill lives in the deployment's list, not the
						// user's own, so the per-user export cannot reach it.
						{Type: "button", Label: "Export", Method: "client",
							PostTo: "export_skill", HideIf: "published"},
						{Type: "button", Label: "Disable", Method: "POST",
							PostTo:     "api/skills?action=disable&id={id}",
							HideIf:     "disabled",
							Optimistic: true},
						{Type: "button", Label: "Enable", Method: "POST",
							PostTo:     "api/skills?action=enable&id={id}",
							OnlyIf:     "disabled",
							Optimistic: true},
						// The third rung. Sharing to named people is the author's
						// own call; reaching every account in the deployment is
						// an administrator's.
						ui.ModalActionIf("Publish deployment-wide", "can_publish", "", ui.FormPanel{
							SubmitLabel: "Ask an admin",
							PostURL:     "api/promotions?kind=skill&name={name}",
							Fields: []ui.FormField{
								{Type: "header", Label: "Everybody's turns, not just yours",
									Help: "It stays yours to edit and to take back, and an admin decides whether it goes out.",
									Detail: "A published skill moves out of your own list into the deployment's, where the classifier can activate it on any user's turn. Your name stays on it, you keep editing it, and Take back returns it to you without asking anybody.\n\n" +
										"Its bundled tools do not go with it. Everything true of that for one recipient is more true for every account at once: it would run your scripts in every session in the deployment, under each person's own credentials, skipping the rung a tool has to pass to reach even one other user.\n\n" +
										"Attached collections travel as references and resolve for whoever can already read them, so promote the collection too if everybody is meant to have it. Anyone you had shared this with keeps it by having it deployment-wide instead."},
								{Field: "note", Type: "textarea", Rows: 3, Label: "Note for the admin (optional)",
									Placeholder: "Who is this for, and when should it fire?"},
							},
							Invalidate: []string{"api/skills"},
						}),
						{Type: "button", Label: "Take back", Method: "POST",
							PostTo:     "api/skills?action=unpublish&id={id}",
							OnlyIf:     "published",
							Confirm:    "Take this skill back from the deployment? It returns to your own skills and stops activating on other people's turns.",
							Optimistic: true},
						{Type: "button", Label: "Delete", Method: "DELETE",
							PostTo:     "api/skills?id={id}",
							Variant:    "danger",
							HideIf:     "published",
							Confirm:    "Delete this skill? The definition is gone for good.",
							Optimistic: true},
					},
					EmptyText: "No skills yet. Add one below, or ask Builder in Agents to author one for you.",
				},
				ui.ModalButton{
					Label:    "Add skill",
					Title:    "New skill",
					Subtitle: "A behavior pack: instructions your agents apply when the triggers match. For a skill that ships code or grants tools, use Builder instead.",
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
			Title:    "Skills the deployment publishes",
			Subtitle: "Behaviour packs that apply to everybody, including you.",
			Detail: "These activate on your turns when their triggers match, the same as your own, whether or not you went looking for them. A skill of your own with the same name is tried first.\n\n" +
				"They carry no code: a published skill's bundled tools stay with its author. Its attached collections are references, and each one only answers for people who can already read it.\n\n" +
				"An author publishes one by asking an admin; the ones you published are listed with your own skills above, where you can edit or take them back.",
			Body: ui.Table{
				Source: "api/skills?deployment=1",
				RowKey: "id",
				Columns: []ui.Col{
					{Field: "name", Flex: 1},
					{Field: "description", Mute: true, Flex: 2},
					{Field: "triggers", Label: "Triggers", Mute: true},
					{Field: "updated", Label: "Updated", Mute: true},
				},
				EmptyText: "The deployment publishes no skills.",
			},
		},
		{
			Title:    "Global tools",
			Subtitle: "Shared tools your deployment publishes.",
			Detail: "Add the ones you want and they become available to your agents; remove any you do not use.\n\n" +
				"A published tool runs as the version an administrator approved, and a new version reaches you when one is approved. " +
				"A tool a colleague shared with you runs as the copy you added: when they change it, the row says an update is available, and nothing changes for you until you accept it.",
			Body: ui.Table{
				Source: "api/global-tools",
				RowKey: "key",
				Columns: []ui.Col{
					{Field: "name", Flex: 1},
					{Field: "mode", Mute: true},
					{Field: "version", Label: "Version", Mute: true},
					{Field: "adopted", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Added", Color: "success"},
					}},
					{Field: "update_available", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Update available", Color: "warning"},
					}},
					{Field: "shadowed", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Shadowed by your own tool", Color: "warning"},
					}},
					{Field: "from", Label: "From", Mute: true},
					{Field: "missing", Label: "Deps", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "⚠ missing", Color: "danger"},
					}},
					{Field: "description", Mute: true, Flex: 2},
				},
				RowActions: []ui.RowAction{
					{Type: "button", Label: "Add", Method: "POST",
						PostTo:     "api/global-tools?name={name}&owner={owner}&adopt=true",
						HideIf:     "adopted",
						Optimistic: true},
					{Type: "button", Label: "Remove", Method: "POST",
						PostTo:     "api/global-tools?name={name}&adopt=false",
						OnlyIf:     "adopted",
						Optimistic: true},
					// A colleague's newer definition: read it, then take it or
					// keep running the copy already added.
					ui.ExpandIf("What changed", "update_available", "", ui.RecordView{
						Pairs: []ui.DisplayPair{
							{Label: "Your copy -> theirs now", Field: "diff", Block: true},
						},
					}),
					{Type: "button", Label: "Accept update", Method: "POST",
						PostTo:  "api/global-tools?name={name}&owner={owner}&adopt=true",
						OnlyIf:  "update_available",
						Confirm: "Switch to their current version of this tool? Your agents run it from now on."},
				},
				EmptyText: "No global tools published yet. When your deployment shares one, it appears here to add.",
			},
		},
	}
	// App-contributed sections (core/sections). Extensions is where a
	// user's own reusable things live, and the things they build are not
	// all this app's to know about — a machine belongs to orchestrate and
	// renders here without this file learning what one is.
	head := ui.NewHead().ClientAction("tool_access_pills", toolAccessPillsJS).
		// Export and Import go through the shared bundle client (core
		// ArtifactClientJS) against the person's own account endpoints.
		JS(ArtifactClientJS).
		ClientAction("export_tool", `function(ctx){ window.gohortArtifacts.exportAction('tool', 'name', 'name')(ctx); }`).
		ClientAction("export_skill", `function(ctx){ window.gohortArtifacts.exportAction('skill', 'id', 'name')(ctx); }`).
		ClientAction("extensions_import", `function(){
  window.gohortArtifacts.importFlow({
    previewURL: '/account/api/artifacts/preview',
    importURL: '/account/api/artifacts/import',
    invalidate: ['api/tools', 'api/skills'],
    subtitle: 'Everything lands in your own account for review: tools wait for an administrator to approve them, skills arrive switched off. A name you already have is skipped.'
  });
}`)
	for _, e := range ExtensionSectionEntries() {
		if e.Build == nil {
			continue
		}
		if sec, ok := e.Build(r, user); ok {
			sections = append(sections, sec)
			if strings.TrimSpace(e.Head) != "" {
				head = head.HTML(e.Head)
			}
		}
	}

	ui.Page{
		Title:     "Extensions",
		ShowTitle: true,
		BackURL:   "/",
		Nav:       HubNav("/extensions"), // shared hub tabs, Extensions active
		// Full width. Extensions › Tools is the widest table in the product — name,
		// category, mode, agents, last-used and eight status badges — and at
		// 1200px the name column ellipsizes while badges wrap, which is most of
		// why the list is hard to scan. SectionNav shows one section at a time,
		// so nothing else is competing for the space. "100%" rather than a
		// bigger fixed cap: the tables are the content here, and a laptop and a
		// wide monitor should both use what they have.
		MaxWidth:   "100%",
		SectionNav: true, // left-rail sub-nav: one section (credentials/tools/…) at a time
		Sections:   sections,
		// App-specific behavior stays in the app: core/ui supplies the generic
		// pill renderer (uiRenderScopePills), this supplies the endpoint it
		// talks to. No copy of the renderer lives here.
		Head: head,
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

func (T *Extensions) handleUserToolAccess(w http.ResponseWriter, r *http.Request) {
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
		// Under nests a SUB-AGENT's pill beneath its parent's. Each pill is
		// still its own toggle: a sub-agent runs its own turns off its own
		// tools, and picking up the parent's kit is opt-in per sub-agent, so
		// switching the parent on grants the child nothing by itself.
		type pill struct {
			Key   string `json:"key"`
			Label string `json:"label"`
			On    bool   `json:"on"`
			Under string `json:"under,omitempty"`
		}
		out := map[string]any{}
		st, found := prov.State(db, user, name)
		items := []pill{}
		// The pill list comes from the provider whether or not the tool is
		// currently in any scope: an orphan or an unkept draft needs the SAME
		// set of targets to be re-homed onto, and it is the only place that
		// knows the parent/child shape. Falling back to the agents that already
		// hold something keeps the picker usable if the provider yields none.
		subs := false
		for _, a := range st.Agents {
			if a.ParentID != "" {
				subs = true
			}
			items = append(items, pill{Key: a.ID, Label: a.Name, On: a.On, Under: a.ParentID})
		}
		if len(items) == 0 {
			seen := map[string]bool{}
			for _, d := range ListScopedTools(user) {
				if d.AgentID == "" || seen[d.AgentID] {
					continue
				}
				seen[d.AgentID] = true
				items = append(items, pill{Key: d.AgentID, Label: d.AgentName})
			}
		}
		// Builder-only rides the Access menu as the first pill — it IS an access
		// statement ("only the authoring surface sees this"), so it belongs with
		// the other access controls rather than as a standalone row button two
		// clicks away from them. Builder itself is never a pill (it reads the
		// whole pool by identity); this is the one Builder-shaped control that
		// is real. Prepended AFTER the fallback above so the "no items → offer
		// re-home targets" trigger keeps meaning what it says.
		builderOnly, boundOnly := false, false
		if row, ok := UserToolByName(db, user, name); ok {
			builderOnly, boundOnly = row.Tool.BuilderOnly, row.Tool.BoundOnly
		}
		if builderOnly {
			// While Builder-only is ON, the selector below it is dead weight:
			// every agent pill is overridden, and a wall of toggles that do
			// nothing invites clicking them to find out. Return ONLY the
			// Builder-only pill — turning it off re-loads the full selector
			// (the renderer re-GETs after every toggle).
			out["items"] = []pill{{Key: "builder_only", Label: "Builder-only (authoring)", On: true}}
			out["note"] = "Builder-only: hidden from every agent except Builder. Turn this off to choose which agents get the tool."
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, out)
			return
		}
		if boundOnly {
			// Same reasoning as Builder-only above: while this is on, every
			// agent pill below is overridden, and a wall of toggles that do
			// nothing invites clicking them to find out.
			out["items"] = []pill{{Key: "bound_only", Label: "Bound targets only", On: true}}
			out["note"] = "Bound targets only: hidden from your agents. The tool stays available wherever it is " +
				"explicitly attached (a Servitor system's Tools list, for instance), and Builder still loads it, " +
				"so it can be tested and fixed. Turn this off to offer it to agents."
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, out)
			return
		}
		items = append([]pill{
			{Key: "builder_only", Label: "Builder-only (authoring)", On: builderOnly},
			{Key: "bound_only", Label: "Bound targets only", On: boundOnly},
		}, items...)
		out["primary"] = map[string]any{"label": "All my agents", "on": found && st.Global}
		switch {
		case !found:
			// Nothing exists to toggle OFF — the first pill switched ON is what
			// keeps (or re-homes) the tool.
			out["note"] = "Not kept yet: switch on an agent (or All my agents) to keep it."
			for _, o := range LoadOrphanedTempTools(db, user) {
				if o.Tool.Name == name {
					out["note"] = "Orphaned: the agent that held this tool was deleted. Switch on an agent (or All my agents) to re-home it, or Delete to discard."
					break
				}
			}
		case st.Global:
			out["note"] = "Shared with every one of your agents. Turn an agent off to deny it there, or turn All my agents off to keep it only on the agents left on."
		default:
			out["note"] = "Available only on the agents switched on. Turn on All my agents to share it with every agent you own."
		}
		if subs {
			out["note"] = out["note"].(string) +
				" Indented pills are sub-agents: each is its own switch, so turning a parent on does not give its sub-agents the tool."
		}
		if len(st.Missing) > 0 {
			out["note"] = "⚠ Missing dependency: " + strings.Join(st.Missing, ", ") + ". " + out["note"].(string)
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
		// Builder-only is a FLAG on the tool record, not a scope transition, so
		// it is handled before the provider. Written via the direct updater:
		// AdminPersistTempTool deliberately preserves this flag from the stored
		// copy (so a Builder re-persist can't clear it), which means writing it
		// through a re-persist path would be silently stomped — the exact trap
		// the enable toggle fell into elsewhere.
		// Both flags are handled here, before the provider, for the same reason:
		// they live ON the tool record rather than being scope transitions, so
		// falling through would look up an AGENT by that name and report
		// `agent "bound_only" not found` — a pill that renders, posts, and fails
		// on a lookup it was never meant to reach.
		if target == "builder_only" || target == "bound_only" {
			row, ok := UserToolByName(db, user, name)
			if !ok {
				// An ORPHAN — a committed tool that lost its agent — is not in
				// the persistent pool, so the lookup missed and the click
				// reported "tool not found" on a tool sitting right there in the
				// list. Homing it to the user-wide pool first is what the
				// operator was otherwise made to do by hand: pick "All my
				// agents", then come back and set the flag.
				//
				// Especially wrong for bound-only, whose whole point is that the
				// tool belongs to a BINDING rather than to any agent. Requiring
				// it to be given to every agent first, so it can then be taken
				// away from all of them, is the opposite of the intent.
				if AdminRehomeOrphanTool != nil {
					for _, o := range LoadOrphanedTempTools(db, user) {
						if o.Tool.Name != name {
							continue
						}
						if err := AdminRehomeOrphanTool(db, user, name, "global"); err != nil {
							http.Error(w, "could not adopt this tool: "+err.Error(), http.StatusBadRequest)
							return
						}
						row, ok = UserToolByName(db, user, name)
						break
					}
				}
			}
			if !ok {
				http.Error(w, "tool not found", http.StatusNotFound)
				return
			}
			if target == "builder_only" {
				row.Tool.BuilderOnly = body.On
			} else {
				row.Tool.BoundOnly = body.On
			}
			// Mutually exclusive: one reserves a tool for authoring, the other
			// says it belongs to whatever binds it. Holding both would leave the
			// selector describing a state no filter produces.
			if body.On {
				if target == "builder_only" {
					row.Tool.BoundOnly = false
				} else {
					row.Tool.BuilderOnly = false
				}
			}
			if !UpdatePersistentTempTool(db, user, row.Tool) {
				http.Error(w, "update failed", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
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

// toolAccessPillsJS drives the Access pill list on Extensions › Tools. The generic
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

// knownToolCategories lists the category names worth offering when someone
// files a tool: the admin-defined ones (a ToolGroup record carries the
// model-facing description, so these are the "real" categories) plus every
// label this user's own tools already claim — a category with no record is
// still a live heading, since the presentation layer falls back to the claimed
// label. Sorted, de-duplicated case-insensitively.
//
// Offered, never enforced. Claiming a NEW name has to stay one keystroke away
// or the categories people actually want never get created; the suggestion
// list only exists so the same category doesn't get coined three times with
// three spellings.
// toolPick is one tool as a picker offers it: its name, and where it sits now.
type toolPick struct{ Name, Desc string }

// userPickableTools is every tool this user could file under a category, sorted
// by name, with the category it currently belongs to folded into the
// description — so moving one between categories is a visible choice rather
// than a silent steal.
//
// One walk, two renderings: the live options endpoint the row picker fetches,
// and the checklist on the create form. They named the same set through two
// pieces of code before, which is how a tool ends up offered in one place and
// missing from the other.
func userPickableTools(user, exceptCategory string) []toolPick {
	var out []toolPick
	seen := map[string]bool{}
	add := func(n, desc, cat string) {
		if n == "" || seen[n] {
			return
		}
		seen[n] = true
		if c := strings.TrimSpace(cat); c != "" && !strings.EqualFold(c, exceptCategory) {
			desc = strings.TrimSpace("currently in " + c + " - " + desc)
		}
		out = append(out, toolPick{Name: n, Desc: desc})
	}
	// Shared rows here; agent-scoped rows below (same unified store — the seen
	// map would dedup either way, but keep the sourcing symmetric with the
	// category listers).
	for _, p := range SharedUserTools(AuthDB(), user) {
		add(p.Tool.Name, p.Tool.Description, p.Tool.Category)
	}
	for _, st := range ListScopedTools(user) {
		if st.Shadowed || st.Scope != ScopeAgentTool {
			continue
		}
		add(st.Tool.Name, st.Tool.Description, st.Tool.Category)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// userToolCheckOptions is that list as a form checklist.
func userToolCheckOptions(user string) []ui.SelectOption {
	var out []ui.SelectOption
	for _, t := range userPickableTools(user, "") {
		out = append(out, ui.SelectOption{Value: t.Name, Label: t.Name, Help: firstLineOf(t.Desc)})
	}
	return out
}

// firstLineOf clips a tool description to its lede: a checklist row is one
// line, and a paragraph in it pushes the next tool off the screen.
func firstLineOf(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			if len(ln) > 120 {
				return ln[:120] + "…"
			}
			return ln
		}
	}
	return ""
}

func knownToolCategories(db Database, user string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		k := strings.ToLower(name)
		if seen[k] {
			return
		}
		seen[k] = true
		out = append(out, name)
	}
	for _, g := range LoadToolGroups(db) {
		add(g.Name)
	}
	if user != "" {
		// Shared rows here; agent-scoped rows contribute via ListScopedTools
		// below (same unified store, split to avoid double-adding).
		for _, p := range SharedUserTools(db, user) {
			add(p.Tool.Category)
		}
		for _, st := range ListScopedTools(user) {
			if st.Scope == ScopeAgentTool {
				add(st.Tool.Category)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i]) < strings.ToLower(out[j])
	})
	return out
}

// setToolCategoryFor files ONE of the user's tools under a category (empty
// clears the claim). Works in either scope: a pool tool is updated in place, an
// agent-scoped tool is rewritten on EVERY agent holding a copy — category is a
// property of the tool, not of one attachment, and leaving copies behind under
// the old label would scatter one tool across two headings.
//
// Shared by the per-row Set category action and the category member picker, so
// filing one tool and filing twenty take exactly the same path.
func setToolCategoryFor(user, name, category string) error {
	db := AuthDB()
	for _, p := range LoadPersistentTempTools(db, user) {
		if p.Tool.Name != name {
			continue
		}
		t := p.Tool
		t.Category = category
		if !UpdatePersistentTempTool(db, user, t) {
			return fmt.Errorf("could not update %q", name)
		}
		return nil
	}
	if AttachToolToAgent == nil {
		return fmt.Errorf("agent-scoped update unavailable")
	}
	updated := 0
	for _, st := range ListScopedTools(user) {
		if st.Shadowed || st.Scope != ScopeAgentTool || st.Tool.Name != name {
			continue
		}
		t := st.Tool
		t.Category = category
		if err := AttachToolToAgent(db, user, st.AgentID, t); err != nil {
			return err
		}
		updated++
	}
	if updated == 0 {
		return fmt.Errorf("no tool named %q", name)
	}
	return nil
}

// userToolCategories returns the user's categories with their members, derived
// from what each tool claims. A category is not a stored entity — it exists
// because tools point at it — so this IS the category list; there is nothing
// else to read. Sorted by name; the members of each are sorted too.
func userToolCategories(user string) map[string][]string {
	out := map[string][]string{}
	add := func(cat, name string) {
		cat = strings.TrimSpace(cat)
		if cat == "" || name == "" {
			return
		}
		out[cat] = append(out[cat], name)
	}
	// Shared rows here; agent-scoped rows contribute via ListScopedTools below
	// (same unified store, split so a scoped tool isn't counted twice).
	for _, p := range SharedUserTools(AuthDB(), user) {
		add(p.Tool.Category, p.Tool.Name)
	}
	seen := map[string]bool{}
	for _, st := range ListScopedTools(user) {
		if st.Shadowed || st.Scope != ScopeAgentTool || seen[st.Tool.Name] {
			continue
		}
		seen[st.Tool.Name] = true
		add(st.Tool.Category, st.Tool.Name)
	}
	for k := range out {
		sort.Strings(out[k])
	}
	return out
}

// handleUserToolCategories backs the category member picker: the surface that
// asks "which tools belong under this heading?" rather than making you open
// forty tools and answer it one at a time.
//
//	GET  /api/tool-categories            → [{name, count, tools[]}] for the table
//	GET  /api/tool-categories?name=X     → {name, tools[]} (picker's current selection)
//	GET  /api/tool-categories?options=1  → [{value,label,desc}] candidate tools
//	POST /api/tool-categories?name=X     → body {tools:[...]} — file exactly these
//
// The POST is a SET operation, not an append: tools added to the list claim the
// category, tools dropped from it have their claim cleared. A tool holds one
// category, so moving it here moves it out of wherever it was — which is the
// behavior the picker's checkboxes imply, and the reason this is a set.
func (T *Extensions) handleUserToolCategories(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	switch r.Method {
	case http.MethodGet:
		if r.URL.Query().Get("options") != "" {
			type opt struct {
				Value string `json:"value"`
				Label string `json:"label"`
				Desc  string `json:"desc,omitempty"`
			}
			opts := []opt{}
			for _, t := range userPickableTools(user, name) {
				opts = append(opts, opt{Value: t.Name, Label: t.Name, Desc: t.Desc})
			}
			writeJSON(w, opts)
			return
		}
		cats := userToolCategories(user)
		if name != "" {
			// Record mode: the picker reads its current selection from here.
			// An unknown name is a category being created — empty, not 404.
			writeJSON(w, map[string]any{"name": name, "tools": cats[name]})
			return
		}
		type catRow struct {
			Name  string   `json:"name"`
			Count int      `json:"count"`
			Tools []string `json:"tools"`
			List  string   `json:"tool_list,omitempty"`
		}
		rows := []catRow{}
		for n, tools := range cats {
			rows = append(rows, catRow{Name: n, Count: len(tools), Tools: tools, List: strings.Join(tools, ", ")})
		}
		sort.Slice(rows, func(i, j int) bool {
			return strings.ToLower(rows[i].Name) < strings.ToLower(rows[j].Name)
		})
		writeJSON(w, rows)
	case http.MethodPost:
		var body struct {
			Name  string   `json:"name"`
			Tools []string `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		// The per-row picker names the category in the query; the "Add
		// category" form types it into a field, so it arrives in the body. An
		// unsubstituted "{name}" placeholder is never a category: it filed
		// tools under a category literally called that.
		if name == "" || strings.HasPrefix(name, "{") {
			name = strings.TrimSpace(body.Name)
		}
		if name == "" || strings.HasPrefix(name, "{") {
			http.Error(w, "missing name", http.StatusBadRequest)
			return
		}
		want := map[string]bool{}
		for _, t := range body.Tools {
			if t = strings.TrimSpace(t); t != "" {
				want[t] = true
			}
		}
		// File everything ticked, and clear the claim on anything that was in
		// this category and is no longer ticked. Only THIS category's former
		// members are cleared — a tool that lives elsewhere is untouched.
		var failed []string
		for t := range want {
			if err := setToolCategoryFor(user, t, name); err != nil {
				failed = append(failed, t+" ("+err.Error()+")")
			}
		}
		for _, t := range userToolCategories(user)[name] {
			if want[t] {
				continue
			}
			if err := setToolCategoryFor(user, t, ""); err != nil {
				failed = append(failed, t+" ("+err.Error()+")")
			}
		}
		if len(failed) > 0 {
			// Partial success is the honest report: the rest DID move.
			http.Error(w, "some tools could not be filed: "+strings.Join(failed, "; "), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// nonNilStrings keeps a chip picker from reading null as "unset": an empty
// list is a real answer (no tools chosen), and JSON null is not.
func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// handleSkillToolOptions lists the tools a skill may be given: the registered
// catalog minus the framework's own plumbing, plus the caller's OWN authored
// tools. Scoped to the caller — this is their skill pool, not everyone's.
func (T *Extensions) handleSkillToolOptions(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	type entry struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Source      string `json:"source"`
	}
	out, seen := []entry{}, map[string]bool{}
	add := func(name, desc, source string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, entry{Name: name, Description: desc, Source: source})
	}
	for _, t := range RegisteredChatTools() {
		// Framework tools are the round's own plumbing, never a capability a
		// skill grants; offering them is offering a choice that does nothing.
		if IsFrameworkTool(t) {
			continue
		}
		add(t.Name(), t.Desc(), "builtin")
	}
	for _, t := range LoadPersistentTempTools(AuthDB(), user) {
		add(t.Tool.Name, t.Tool.Description, "yours")
	}
	writeJSON(w, out)
}

// handleSkillCollectionOptions lists the document collections the caller can
// attach to a skill: their own, plus any deployment-wide ones.
func (T *Extensions) handleSkillCollectionOptions(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	type entry struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	out := []entry{}
	for _, c := range ListCollections(UserDB(CollectionsDB(), user), user) {
		out = append(out, entry{ID: c.ID, Name: c.Name, Description: c.Description})
	}
	writeJSON(w, out)
}

// handleUserCandidates serves the ACL-picker candidate list ([{value,label}])
// for the "Shared with" picker on a skill. Any authenticated user may share
// what they own, so every approved user is a candidate; this is the fleet-wide
// list, not the admin-only one.
func (T *Extensions) handleUserCandidates(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(UserCandidatesJSON(AuthDB(), user))
}

// nonNilList keeps an empty array an array in JSON: an ACLPicker handed null
// where it expected a list renders as though the record had no field rather
// than no members.
func nonNilList(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// shareSummary is the one line the credential list shows about who else can use
// a key, phrased so that reads and writes never read the same.
func shareSummary(c SecureCredential) string {
	switch r, w := len(c.SharedReadOnly), len(c.SharedReadWrite); {
	case r == 0 && w == 0:
		return "Just you"
	case w == 0:
		return fmt.Sprintf("%d reading", r)
	case r == 0:
		return fmt.Sprintf("%d writing as you", w)
	default:
		return fmt.Sprintf("%d reading, %d writing as you", r, w)
	}
}

// lentCredentialRows describes the credentials other people have lent to this
// user, for the section that only exists once somebody has.
type lentCredentialRow struct {
	Name    string `json:"name"`
	Owner   string `json:"owner"`
	BaseURL string `json:"base_url"`
	Grant   string `json:"grant"`
	Tool    string `json:"tool"`
}

func lentCredentialRows(user string) []lentCredentialRow {
	out := []lentCredentialRow{}
	for _, c := range Secure().SharedWithUser(user) {
		grant := "Reads only"
		if credentialLentForWrites(c, user) {
			grant = "Reads and writes as them"
		}
		out = append(out, lentCredentialRow{
			Name: c.Name, Owner: c.Owner, BaseURL: c.BaseURL,
			Grant: grant, Tool: "fetch_url_" + c.Name,
		})
	}
	return out
}

// credentialLentForWrites reads the grant off the record rather than asking
// core for a second opinion: the lists are the grant.
func credentialLentForWrites(c SecureCredential, user string) bool {
	for _, u := range c.SharedReadWrite {
		if u == user {
			return true
		}
	}
	return false
}

// credentialLedgerRow is one line of a credential's dispatch history, as its
// OWNER reads it.
type credentialLedgerRow struct {
	When    string `json:"when"`
	Who     string `json:"who"`
	Method  string `json:"method"`
	URL     string `json:"url"`
	Outcome string `json:"outcome"`
}

// credentialLedgerRows renders the owner's own ledger. Scoped to their
// namespace by construction: LoadAudit is keyed by owner, so asking for a name
// they do not own reads an empty ring rather than somebody else's history.
func credentialLedgerRows(user, name string) []credentialLedgerRow {
	out := []credentialLedgerRow{}
	for _, e := range Secure().LoadAudit(user, name) {
		who := e.DispatchedBy
		switch {
		case who == "":
			// Blank is not the owner. A row from before the ledger recorded a
			// caller, or a call with no session behind it, and saying "you"
			// would be inventing the one fact this column exists to carry.
			who = "unrecorded"
		case who == user:
			who = "you"
		}
		out = append(out, credentialLedgerRow{
			When:    e.Timestamp.Local().Format("Jan 2 15:04"),
			Who:     who,
			Method:  e.Method,
			URL:     e.URL,
			Outcome: ledgerOutcome(e),
		})
	}
	return out
}

// ledgerOutcome distinguishes the three things that can happen to a call, which
// a status code alone does not: sent and answered, sent and rejected, and never
// sent at all.
func ledgerOutcome(e SecureAPIAuditEntry) string {
	if e.Status == 0 {
		if e.Error != "" {
			return "Not sent: " + e.Error
		}
		return "Not sent"
	}
	if e.Status >= 200 && e.Status < 300 {
		return fmt.Sprintf("%d", e.Status)
	}
	return fmt.Sprintf("%d (refused by the API)", e.Status)
}

// handoverPending reports whether this credential is already waiting on an
// admin. A button that stays live after the ask reads as having done nothing,
// and a second request would overwrite the first with a new note rather than
// queueing behind it.
func handoverPending(user, name string) bool {
	req, ok := promotion.GetPromotionRequest(AuthDB(), promotion.RequestKey(CredentialPromotionKind, user, name))
	return ok && req.State == promotion.PromotionPendingState
}

// skillPublishPending reports whether this skill is already waiting on an
// admin, so the ask does not stay live and read as having done nothing. Keyed
// on the NAME, which is what the request is filed under.
func skillPublishPending(user, name string) bool {
	req, ok := promotion.GetPromotionRequest(AuthDB(), promotion.RequestKey(SkillPromotionKind, user, name))
	return ok && req.State == promotion.PromotionPendingState
}

// sharedWithSummary is the one line the tool list shows about who else has a
// tool, phrased so it never reads like the deployment catalog beside it.
func sharedWithSummary(users []string) string {
	switch len(users) {
	case 0:
		return ""
	case 1:
		return "Shared with " + users[0]
	default:
		return fmt.Sprintf("Shared with %d people", len(users))
	}
}

// importToolbar is the Import button over a list of the person's own things:
// preview a bundle, then land it in their account (extensions_import).
func importToolbar(title string) ui.Component {
	return ui.Toolbar{Actions: []ui.ToolbarAction{{
		Label: "Import…", Title: title, Method: "client", URL: "extensions_import",
	}}}
}
