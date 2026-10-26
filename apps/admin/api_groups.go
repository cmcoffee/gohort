package admin

import (
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// registerGroupsRoutes wires the groups API under the admin sub-mux.
func (a *AdminApp) registerGroupsRoutes(sub *http.ServeMux) {
	// Tool Groups: admin-curated bundles of chat tools that the runtime
	// catalog rewriter can collapse into one expandable entry. GET
	// lists all groups; POST upserts (id empty = create, present = update);
	// DELETE removes by id. The /registry sub-endpoint returns the
	// global ChatTool registry so the member picker has options.
	sub.HandleFunc("/api/tool-groups", jsonapi.Methods{
		"GET":    jsonapi.Get(a.db, a.handleToolGroups, a.gate()),
		"POST":   jsonapi.Post(a.db, a.handleToolGroupSave, a.gate()),
		"DELETE": jsonapi.Post(a.db, a.handleToolGroupDelete, a.gate()),
	}.Serve)

	// App groups: bundle apps (by web path) so an admin can grant a whole
	// set to a user in one assignment. List (GET) / upsert (POST) / delete
	// (DELETE ?id=). Mirrors the tool-groups endpoint shape.
	sub.HandleFunc("/api/app-groups", jsonapi.Methods{
		"GET":    jsonapi.Get(a.db, a.handleAppGroups, a.gate()),
		"POST":   jsonapi.Post(a.db, a.handleAppGroupSave, a.gate()),
		"DELETE": jsonapi.Post(a.db, a.handleAppGroupDelete, a.gate()),
	}.Serve)

	// Single-record GET for the per-row editor (ChipPicker.RecordSource /
	// FormPanel.Source fetch one group by id).
	sub.HandleFunc("/api/app-groups/", jsonapi.Get(a.db, a.handleAppGroup, a.gate()))

	// Single-record GET for the per-row editor: returns the full
	// ToolGroup JSON for the given id. POST/PUT/DELETE on individual
	// records go through the list endpoint above (body carries the id);
	// this trailing-slash variant exists so ChipPicker.RecordSource and
	// FormPanel.Source can fetch one group cleanly. /{id}/members is the
	// Categories pills editor (GET options+selection, POST the new member
	// set).
	one := jsonapi.Get(a.db, a.handleToolGroup, a.gate())
	members := jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleToolGroupMembers, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleToolGroupMembersSave, a.gate()),
	}
	sub.HandleFunc("/api/tool-groups/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/members") {
			members.Serve(w, r)
			return
		}
		one(w, r)
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
	sub.HandleFunc("/api/tool-groups/registry", jsonapi.Get(a.db, a.handleToolGroupRegistry, a.gate()))
}

// handleToolGroups is every tool group. Each is wrapped with is_builtin so
// the per-row UI can branch: admin-curated groups get Delete, framework
// defaults get Revert (which drops the shadow but preserves the in-code
// definition).
func (a *AdminApp) handleToolGroups(c *jsonapi.Call) (any, error) {
	groups := LoadToolGroups(a.db)
	type wire struct {
		ToolGroup
		IsBuiltin bool `json:"is_builtin"`
	}
	out := make([]wire, 0, len(groups))
	for _, g := range groups {
		out = append(out, wire{ToolGroup: g, IsBuiltin: IsBuiltinToolGroupID(g.ID)})
	}
	return out, nil
}

// handleToolGroupSave creates a tool group, or saves the one it names.
func (a *AdminApp) handleToolGroupSave(c *jsonapi.Call, req ToolGroup) (any, error) {
	// Membership is no longer edited from the Categories UI (custom
	// tools self-claim via Tool.Category; built-in members are
	// framework-defined). A name/description save omits Members, so
	// PRESERVE the stored member list rather than blank it — otherwise
	// renaming a category would drop the built-in Web Media grouping.
	if req.ID != "" && len(req.Members) == 0 {
		if existing, ok := LoadToolGroup(a.db, req.ID); ok {
			req.Members = existing.Members
		}
	}
	saved, err := SaveToolGroup(a.db, req)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return saved, nil
}

// handleToolGroupDelete removes the ?id= tool group, or reverts a built-in
// to its framework definition.
func (a *AdminApp) handleToolGroupDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	id := strings.TrimSpace(c.R.URL.Query().Get("id"))
	if id == "" {
		return nil, jsonapi.BadRequest("missing id")
	}
	if err := DeleteToolGroup(a.db, id); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleToolGroup is one tool group.
func (a *AdminApp) handleToolGroup(c *jsonapi.Call) (any, error) {
	g, ok := LoadToolGroup(a.db, strings.TrimPrefix(c.R.URL.Path, "/api/tool-groups/"))
	if !ok {
		return nil, jsonapi.NotFound("no such tool group")
	}
	return g, nil
}

// handleAppGroups is every app group.
func (a *AdminApp) handleAppGroups(c *jsonapi.Call) (any, error) {
	return LoadAppGroups(a.db), nil
}

// handleAppGroupSave creates an app group, or saves the one it names.
func (a *AdminApp) handleAppGroupSave(c *jsonapi.Call, req AppGroup) (any, error) {
	saved, err := SaveAppGroup(a.db, req)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return saved, nil
}

// handleAppGroupDelete removes the ?id= app group.
func (a *AdminApp) handleAppGroupDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	id := strings.TrimSpace(c.R.URL.Query().Get("id"))
	if id == "" {
		return nil, jsonapi.BadRequest("missing id")
	}
	if err := DeleteAppGroup(a.db, id); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleAppGroup is one app group.
func (a *AdminApp) handleAppGroup(c *jsonapi.Call) (any, error) {
	g, ok := LoadAppGroup(a.db, strings.TrimPrefix(c.R.URL.Path, "/api/app-groups/"))
	if !ok {
		return nil, jsonapi.NotFound("no such app group")
	}
	return g, nil
}

// handleToolGroupRegistry is every tool a group may be made of; the route's
// comment says what it holds and how to narrow it.
func (a *AdminApp) handleToolGroupRegistry(c *jsonapi.Call) (any, error) {
	type entry struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Source      string `json:"source"` // "builtin" | "temp"
	}
	excludeGrouped := c.R.URL.Query().Get("exclude_grouped") == "true"
	exceptGroup := strings.TrimSpace(c.R.URL.Query().Get("except_group"))

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
	if extra := strings.TrimSpace(c.R.URL.Query().Get("exclude")); extra != "" {
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
	return out, nil
}
