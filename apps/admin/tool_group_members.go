// Category membership editor — the pills behind Categories › Members.
//
// A tool "belongs" to a category by CLAIMING it (Tool.Category on the tool's
// own record) — there is no member list to edit on the category itself. That
// self-claim model is right for authoring (Builder/Gateways set it at create
// time), but curating from the Categories surface meant opening every tool and
// retyping the label by hand. This endpoint lets the ChipPicker edit the same
// claims in bulk: adding a pill stamps the tool's Category with the group's
// name; removing a pill clears it (only when it currently claims this group).
// Built-in registered tools are framework-assigned and don't appear here —
// the options are the user's own custom tools (user-wide pool + agent-bundled
// via the core seams).

package admin

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// handleToolGroupMembers serves GET/POST /api/tool-groups/{id}/members.
//
// GET → {records: [{name, desc, scope}], selected: [names]} — the ChipPicker
// attach-mode contract (OptionsSource carries the current selection).
// POST {members: [names]} → applies the claim diff and returns the fresh GET
// shape so the picker re-renders without a second fetch.
func (a *AdminApp) handleToolGroupMembers(w http.ResponseWriter, r *http.Request, groupID string) {
	g, ok := LoadToolGroup(a.db, groupID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	user := AuthCurrentUser(r)

	type memberOpt struct {
		Name  string `json:"name"`
		Desc  string `json:"desc,omitempty"`
		Scope string `json:"scope,omitempty"`
	}
	buildState := func() ([]memberOpt, []string) {
		seen := map[string]bool{}
		var opts []memberOpt
		var selected []string
		add := func(t TempTool, scope string) {
			if t.Name == "" || seen[t.Name] {
				return
			}
			seen[t.Name] = true
			opts = append(opts, memberOpt{Name: t.Name, Desc: firstDescLine(t.Description), Scope: scope})
			if strings.EqualFold(strings.TrimSpace(t.Category), g.Name) {
				selected = append(selected, t.Name)
			}
		}
		for _, p := range LoadPersistentTempTools(AuthDB(), user) {
			add(p.Tool, "user-wide")
		}
		if ListUserAgentTools != nil {
			for _, t := range ListUserAgentTools(RootDB, user) {
				add(t, "agent-scoped")
			}
		}
		sort.Slice(opts, func(i, j int) bool { return opts[i].Name < opts[j].Name })
		return opts, selected
	}

	switch r.Method {
	case http.MethodGet:
		// fallthrough to the shared response below

	case http.MethodPost:
		var req struct {
			Members []string `json:"members"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		want := map[string]bool{}
		for _, n := range req.Members {
			if n = strings.TrimSpace(n); n != "" {
				want[n] = true
			}
		}
		// User-wide pool: stamp additions, clear removals. Only a claim that
		// currently points at THIS group is cleared — a tool claiming a
		// different category is simply not selected here, never rewritten.
		poolNames := map[string]bool{}
		for _, p := range LoadPersistentTempTools(AuthDB(), user) {
			poolNames[p.Tool.Name] = true
			claims := strings.EqualFold(strings.TrimSpace(p.Tool.Category), g.Name)
			switch {
			case want[p.Tool.Name] && !claims:
				p.Tool.Category = g.Name
			case !want[p.Tool.Name] && claims:
				p.Tool.Category = ""
			default:
				continue
			}
			if err := AdminPersistTempTool(AuthDB(), user, p.Tool); err != nil {
				Log("[admin.categories] set category %q on pool tool %q failed: %v", g.Name, p.Tool.Name, err)
			}
		}
		// Agent-bundled: same diff via the core seams. Pool names are already
		// handled above — the pool copy wins runtime resolution, so its claim
		// is the one that matters when both homes exist.
		if ListUserAgentTools != nil && FindUserAgentTool != nil && AttachToolToAgent != nil {
			for _, t := range ListUserAgentTools(RootDB, user) {
				if poolNames[t.Name] {
					continue
				}
				claims := strings.EqualFold(strings.TrimSpace(t.Category), g.Name)
				if want[t.Name] == claims {
					continue
				}
				tt, agentID, found := FindUserAgentTool(RootDB, user, t.Name)
				if !found {
					continue
				}
				if want[t.Name] {
					tt.Category = g.Name
				} else {
					tt.Category = ""
				}
				if err := AttachToolToAgent(RootDB, user, agentID, tt); err != nil {
					Log("[admin.categories] set category %q on agent tool %q failed: %v", g.Name, tt.Name, err)
				}
			}
		}
		Log("[admin.categories] user %q updated members of category %q (%d selected)", user, g.Name, len(want))

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	opts, selected := buildState()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"records":  opts,
		"selected": selected,
	})
}

// firstDescLine returns the first non-empty line of a tool description,
// clipped for the picker's attach-row description.
func firstDescLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			if len(ln) > 140 {
				ln = ln[:140] + "…"
			}
			return ln
		}
	}
	return ""
}
