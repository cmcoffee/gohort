package admin

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// registerGroupsRoutes wires the groups API under the admin sub-mux.
func (a *AdminApp) registerGroupsRoutes(sub *http.ServeMux) {
	// Tool Groups: admin-curated bundles of chat tools that the runtime
	// catalog rewriter can collapse into one expandable entry. GET
	// lists all groups; POST upserts (id empty = create, present = update);
	// DELETE removes by id. The /registry sub-endpoint returns the
	// global ChatTool registry so the member picker has options.
	sub.HandleFunc("/api/tool-groups", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			// Wrap each group with is_builtin so the per-row UI can
			// branch — admin-curated groups get Delete, framework
			// defaults get Revert (which drops the shadow but
			// preserves the in-code definition).
			groups := LoadToolGroups(a.db)
			type wire struct {
				ToolGroup
				IsBuiltin bool `json:"is_builtin"`
			}
			out := make([]wire, 0, len(groups))
			for _, g := range groups {
				out = append(out, wire{ToolGroup: g, IsBuiltin: IsBuiltinToolGroupID(g.ID)})
			}
			_ = json.NewEncoder(w).Encode(out)
		case http.MethodPost:
			var req ToolGroup
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
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
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(saved)
		case http.MethodDelete:
			id := strings.TrimSpace(r.URL.Query().Get("id"))
			if id == "" {
				http.Error(w, "missing id", http.StatusBadRequest)
				return
			}
			if err := DeleteToolGroup(a.db, id); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// App groups: bundle apps (by web path) so an admin can grant a whole
	// set to a user in one assignment. List (GET) / upsert (POST) / delete
	// (DELETE ?id=). Mirrors the tool-groups endpoint shape.
	sub.HandleFunc("/api/app-groups", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(LoadAppGroups(a.db))
		case http.MethodPost:
			var req AppGroup
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			saved, err := SaveAppGroup(a.db, req)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(saved)
		case http.MethodDelete:
			id := strings.TrimSpace(r.URL.Query().Get("id"))
			if id == "" {
				http.Error(w, "missing id", http.StatusBadRequest)
				return
			}
			if err := DeleteAppGroup(a.db, id); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Single-record GET for the per-row editor (ChipPicker.RecordSource /
	// FormPanel.Source fetch one group by id).
	sub.HandleFunc("/api/app-groups/", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/app-groups/")
		g, ok := LoadAppGroup(a.db, id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(g)
	})

	// Single-record GET for the per-row editor: returns the full
	// ToolGroup JSON for the given id. POST/PUT/DELETE on individual
	// records go through the list endpoint above (body carries the id);
	// this trailing-slash variant exists so ChipPicker.RecordSource and
	// FormPanel.Source can fetch one group cleanly.
	sub.HandleFunc("/api/tool-groups/", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/api/tool-groups/")
		// /api/tool-groups/registry is a sibling, handled below — let it
		// route there rather than 404 here.
		if rest == "registry" {
			http.NotFound(w, r) // ServeMux's longest-prefix wins; this branch shouldn't fire
			return
		}
		// /api/tool-groups/{id}/members — the Categories pills editor
		// (GET options+selection, POST the new member set).
		if id, found := strings.CutSuffix(rest, "/members"); found {
			a.handleToolGroupMembers(w, r, id)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		g, ok := LoadToolGroup(a.db, rest)
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(g)
	})

	// Per-field LLM suggest for the Tool Groups editor. Same
	// {field, hint, record} → {value} shape as the agent-editor's
	// suggest. Builds a prompt that includes the group's name and
	// member tool descriptions so the LLM can synthesize a description
	// the agent's catalog will actually find useful.
	sub.HandleFunc("/api/tool-groups/suggest", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		a.handleToolGroupSuggest(w, r)
	})

	// Auto-create: admin picks members, LLM proposes name +
	// description, server saves. The minimal-friction path — most
	// of the time the LLM names a bundle better than the admin
	// would anyway, since the LLM is the one who'll have to call
	// the group later. Admin can rename via the per-row editor.
	sub.HandleFunc("/api/tool-groups/auto-create", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		a.handleToolGroupAutoCreate(w, r)
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
	sub.HandleFunc("/api/tool-groups/registry", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		type entry struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Source      string `json:"source"` // "builtin" | "temp"
		}
		excludeGrouped := r.URL.Query().Get("exclude_grouped") == "true"
		exceptGroup := strings.TrimSpace(r.URL.Query().Get("except_group"))

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
		if extra := strings.TrimSpace(r.URL.Query().Get("exclude")); extra != "" {
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
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})

}
