package admin

import (
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// registerToolsRoutes wires the tools API under the admin sub-mux.
func (a *AdminApp) registerToolsRoutes(sub *http.ServeMux) {
	// Persistent tools (created via create_temp_tool with persist=true).
	// GET returns {pending: [...], active: [...]} for the current user.
	// POST/DELETE mutate per the action query param. Each entry includes
	// the full command_template so the admin can spot anything fishy
	// before approving — that visibility is the whole point of the
	// approval queue.
	sub.HandleFunc("/api/persistent-tools", jsonapi.Methods{
		"GET":    jsonapi.Get(a.db, a.handlePersistentTools, a.gate()),
		"POST":   jsonapi.Post(a.db, a.changePersistentTool, a.gate()),
		"DELETE": jsonapi.Delete(a.db, a.deletePersistentTool, a.gate()),
	}.Serve)

	// Tool scope — the pill control's data + toggles. GET ?name=&owner=
	// returns the ToolScopeState (global flag + per-agent on/off + missing
	// deps). POST ?name=&owner= with body {target, on} applies one toggle
	// (target "global" or an agent id). Both delegate to orchestrate.
	sub.HandleFunc("/api/tool-scope", jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleToolScope, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleToolScopeSet, a.gate()),
	}.Serve)

}

// handlePersistentTools answers GET /api/persistent-tools: every user's
// tools by where they live, or one tool's access list for the picker.
func (a *AdminApp) handlePersistentTools(c *jsonapi.Call) (any, error) {
	username, err := skillUser(c)
	if err != nil {
		return nil, err
	}
	ov := &toolsOverview{a: a, r: c.R, username: username}
	if acl, ok := ov.picker(); ok {
		return acl, nil
	}
	ov.pools()
	ov.scoped()
	ov.orphans()
	return ov.reply(), nil
}

// pendingWithOwner is a tool waiting for approval, with whose it is.
type pendingWithOwner struct {
	Owner string `json:"owner"`
	PendingTempTool
}

// activeWithOwner is a pool tool with whose it is and what it lacks.
type activeWithOwner struct {
	Owner      string   `json:"owner"`
	Missing    []string `json:"missing,omitempty"`
	HasMissing bool     `json:"has_missing"`
	PersistentTempTool
}

// bundledAgent is one agent a scoped tool is on.
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

// orphanWithOwner is an orphaned tool with whose it is.
type orphanWithOwner struct {
	Owner string `json:"owner"`
	OrphanedTempTool
	Missing    []string `json:"missing,omitempty"`
	HasMissing bool     `json:"has_missing"`
}

// toolsOverview is one GET of Admin › Tools: every user's tools by where
// they live (the pending queue, the shared pool, the tools scoped to agents,
// the orphans), or a single tool's access list for the picker. Each field is
// a local of listPersistentTools, which is now the order of the phases.
type toolsOverview struct {
	a        *AdminApp
	r        *http.Request
	username string
	pending  []pendingWithOwner
	active   []activeWithOwner
	bundled  []bundledWithOwner
	orphaned []orphanWithOwner
}

// picker is the single-tool access list the ACLPicker asks for, when the
// request is for one.
func (ov *toolsOverview) picker() (any, bool) {
	a := ov.a
	r := ov.r
	username := ov.username
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
		return map[string]any{"allowed_users": allowed}, true
	}
	return nil, false
}

// Flag a tool whose credential dependency doesn't resolve, for a
// row badge. Cheap (a registry lookup); no-auth / credential-less
// tools return nil.
func toolMissingDeps(t TempTool) []string {
	cred := strings.TrimSpace(t.Credential)
	if cred == "" || strings.EqualFold(cred, "no_auth") {
		return nil
	}
	if exists, _, _ := Secure().CredentialStatus(cred); !exists {
		return []string{"credential:" + cred}
	}
	return nil
}

func (ov *toolsOverview) pools() {
	a := ov.a
	username := ov.username
	// Persistent + pending pools are stored per-user at the
	// kvlite layer, but admins see the whole deployment —
	// the tool-groups registry walks all users; this page
	// should match. Each entry surfaces with an "owner"
	// badge so the admin can tell whose tool is whose.
	store := RootDB
	if store == nil {
		store = a.db
	}
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
				ov.pending = append(ov.pending, pendingWithOwner{Owner: u, PendingTempTool: p})
			}
			// Shared rows only — agent-scoped rows in the same unified
			// store render in the "bundled" section below, not as
			// user-wide pool tools.
			for _, p := range SharedUserTools(a.db, u) {
				m := toolMissingDeps(p.Tool)
				ov.active = append(ov.active, activeWithOwner{Owner: u, Missing: m, HasMissing: len(m) > 0, PersistentTempTool: p})
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
		ov.pending = nil
		ov.active = nil
	}
}

// Agent-bundled tools — rows in each user's unified tool store
// whose ScopeAgents restricts them to specific agents (the
// flattened replacement for the old embedded AgentRecord.Tools
// copies). Read-only here: they're removed via Builder, not the
// admin. Surface each with its carrying agents so nothing is
// invisible in the DB.
func (ov *toolsOverview) scoped() {
	a := ov.a
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
			ov.bundled = append(ov.bundled, entry)
		}
	}
	// Comma-join each row's agent names for the display column.
	for i := range ov.bundled {
		names := make([]string, 0, len(ov.bundled[i].Agents))
		for _, ag := range ov.bundled[i].Agents {
			names = append(names, ag.Name)
		}
		ov.bundled[i].Agent = strings.Join(names, ", ")
	}
}

// Orphaned tools — formerly agent-scoped, captured when their
// owning agent was deleted. Walk every user's orphan pool.
func (ov *toolsOverview) orphans() {
	a := ov.a
	for _, u := range AuthListUsers(a.db) {
		for _, o := range LoadOrphanedTempTools(a.db, u.Username) {
			m := toolMissingDeps(o.Tool)
			ov.orphaned = append(ov.orphaned, orphanWithOwner{Owner: u.Username, OrphanedTempTool: o, Missing: m, HasMissing: len(m) > 0})
		}
	}
}

// reply is the overview, each list as the table reads it.
func (ov *toolsOverview) reply() map[string]any {
	return map[string]any{
		"pending":  ov.pending,
		"active":   ov.active,
		"bundled":  ov.bundled,
		"orphaned": ov.orphaned,
	}
}

// changePersistentTool answers POST: one action on one user's tool, named by ?action=.
func (a *AdminApp) changePersistentTool(c *jsonapi.Call, acl *toolACL) (any, error) {
	username, err := skillUser(c)
	if err != nil {
		return nil, err
	}
	r := c.R
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
		return nil, jsonapi.BadRequest("missing name")
	}
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
		// {allowed_users:[...]} record whole). An empty list opens the tool
		// to all users, so no body is refused rather than read as one.
		if acl == nil {
			return nil, jsonapi.BadRequest("bad request")
		}
		err = SetPersistentTempToolAllowedUsers(a.db, owner, name, acl.AllowedUsers)
	case "orphan_promote":
		if AdminRehomeOrphanTool == nil {
			return nil, jsonapi.Status(http.StatusServiceUnavailable, "unavailable (orchestrate not wired)")
		}
		err = AdminRehomeOrphanTool(a.db, owner, name, "global")
	case "orphan_attach":
		agentID := strings.TrimSpace(r.URL.Query().Get("agent"))
		if agentID == "" {
			return nil, jsonapi.BadRequest("orphan_attach requires agent")
		}
		if AdminRehomeOrphanTool == nil {
			return nil, jsonapi.Status(http.StatusServiceUnavailable, "unavailable (orchestrate not wired)")
		}
		err = AdminRehomeOrphanTool(a.db, owner, name, agentID)
	case "orphan_delete":
		if !RemoveOrphanedTempTool(a.db, owner, name) {
			return nil, jsonapi.NotFound("no orphaned tool named " + name)
		}
	default:
		return nil, jsonapi.BadRequest("action must be approve|reject|share|unshare|set_allowed_users|orphan_promote|orphan_attach|orphan_delete")
	}
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// toolACL is the access list the ACLPicker posts with set_allowed_users.
type toolACL struct {
	AllowedUsers []string `json:"allowed_users"`
}

// deletePersistentTool answers DELETE: removes one user's tool from their pool.
func (a *AdminApp) deletePersistentTool(c *jsonapi.Call, _ struct{}) (any, error) {
	username, err := skillUser(c)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(c.R.URL.Query().Get("name"))
	owner := strings.TrimSpace(c.R.URL.Query().Get("owner"))
	if owner == "" {
		owner = username
	}
	if name == "" {
		return nil, jsonapi.BadRequest("missing name")
	}
	if err := DeletePersistentTempTool(a.db, owner, name); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// toolScopeArgs is what a tool-scope request names: whose, which, and the
// scope backend its ?kind= selects (tool | pipeline | credential), dispatched
// through the core registry the orchestrate app wires.
func toolScopeArgs(c *jsonapi.Call) (owner, name, kind string, prov ScopeProvider, err error) {
	q := c.R.URL.Query()
	name = strings.TrimSpace(q.Get("name"))
	owner = strings.TrimSpace(q.Get("owner"))
	if owner == "" {
		owner = c.User
	}
	if name == "" {
		return "", "", "", ScopeProvider{}, jsonapi.BadRequest("missing name")
	}
	kind = strings.TrimSpace(q.Get("kind"))
	if kind == "" {
		kind = "tool"
	}
	prov, ok := ScopeProviderFor(kind)
	if !ok {
		return "", "", "", ScopeProvider{}, jsonapi.Status(http.StatusServiceUnavailable, "unavailable (scope kind "+kind+" not wired)")
	}
	return owner, name, kind, prov, nil
}

// handleToolScope is the scope pill's state for one tool.
func (a *AdminApp) handleToolScope(c *jsonapi.Call) (any, error) {
	owner, name, kind, prov, err := toolScopeArgs(c)
	if err != nil {
		return nil, err
	}
	st, ok := prov.State(a.db, owner, name)
	if !ok {
		return nil, jsonapi.NotFound(kind + " not found")
	}
	// The scope-pill modal re-GETs this exact URL immediately after
	// each toggle POST to re-render. A cached (stale) response makes
	// the pill snap back to its pre-toggle state ("won't uncheck"), so
	// force a fresh read every time.
	c.W.Header().Set("Cache-Control", "no-store")
	return st, nil
}

// handleToolScopeSet applies one toggle: target "global" or an agent id.
func (a *AdminApp) handleToolScopeSet(c *jsonapi.Call, body struct {
	Target string `json:"target"`
	On     bool   `json:"on"`
}) (any, error) {
	owner, name, _, prov, err := toolScopeArgs(c)
	if err != nil {
		return nil, err
	}
	body.Target = strings.TrimSpace(body.Target)
	if body.Target == "" {
		return nil, jsonapi.BadRequest("target is required")
	}
	if err := prov.Set(a.db, owner, name, body.Target, body.On); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}
