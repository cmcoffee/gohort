package admin

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
)

// registerToolsRoutes wires the tools API under the admin sub-mux.
func (a *AdminApp) registerToolsRoutes(sub *http.ServeMux) {
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
					// Shared rows only — agent-scoped rows in the same unified
					// store render in the "bundled" section below, not as
					// user-wide pool tools.
					for _, p := range SharedUserTools(a.db, u) {
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
			// Agent-bundled tools — rows in each user's unified tool store
			// whose ScopeAgents restricts them to specific agents (the
			// flattened replacement for the old embedded AgentRecord.Tools
			// copies). Read-only here: they're removed via Builder, not the
			// admin. Surface each with its carrying agents so nothing is
			// invisible in the DB.
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
			var bundled []bundledWithOwner
			// One unified store per user: a tool is either shared (listed under
			// "active" above) or scoped, so the old global-supersedes-scoped
			// duplicate handling is gone by construction. Each scoped row is
			// one entry listing every carrying agent; agent names resolve from
			// the owner's agent records.
			orchestrateBase := a.db.Bucket("orchestrate")
			for _, u := range AuthListUsers(a.db) {
				udb := UserDB(orchestrateBase, u.Username)
				if udb == nil {
					continue
				}
				// id → record probe for name resolution + the visibility rule
				// below. Minimal struct — gob matches by field NAME, so ID /
				// Name / Hidden / OwnedBy decode out of the full AgentRecord.
				type agentProbe struct {
					ID      string
					Name    string
					Hidden  bool
					OwnedBy string
				}
				probes := map[string]agentProbe{}
				for _, key := range udb.Keys("orchestrate_agents") {
					var rec agentProbe
					if udb.Get("orchestrate_agents", key, &rec) {
						probes[rec.ID] = rec
					}
				}
				for _, p := range LoadPersistentTempTools(a.db, u.Username) {
					if len(p.ScopeAgents) == 0 {
						continue // shared — already listed under "active"
					}
					entry := bundledWithOwner{Owner: u.Username, Tool: p.Tool}
					for _, id := range p.ScopeAgents {
						rec, ok := probes[id]
						if !ok {
							continue // scoped to an agent that no longer exists
						}
						// App-specific agents (Guide Author, Servitor
						// Investigator, …) are Hidden, and sub-agents carry
						// OwnedBy — both have curated, purpose-built kits and
						// aren't user tool-scope targets. Keep them out of
						// this list; only top-level user-managed agents show.
						// The app-agent test asks the REGISTRY, because this
						// walk reads raw shadow rows: a shadow written before
						// a spec flipped to Hidden still decodes Hidden=false.
						if _, isApp := appagents.AppAgentByID(rec.ID); isApp || rec.Hidden || rec.OwnedBy != "" {
							continue
						}
						entry.Agents = append(entry.Agents, bundledAgent{ID: rec.ID, Name: rec.Name})
					}
					if len(entry.Agents) == 0 {
						continue // no visible carriers — nothing to show
					}
					m := toolMissingDeps(p.Tool)
					entry.Missing, entry.HasMissing = m, len(m) > 0
					bundled = append(bundled, entry)
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

}
