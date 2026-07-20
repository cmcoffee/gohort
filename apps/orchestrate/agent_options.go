package orchestrate

import (
	"net/http"
	"sort"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
	"github.com/cmcoffee/gohort/core/ui"
)

// agentPickerRow is a scratch row used only while partitioning the agent picker.
type agentPickerRow struct {
	ID    string
	Name  string
	Order int
	App   string // owning app, for App Agents grouping/sort
}

// agentPickerBuiltInOrder pins the built-in seeds' order in the picker. seed-kb
// is intentionally absent — a clone-only TEMPLATE kept out of the picker.
var agentPickerBuiltInOrder = map[string]int{
	"seed-chat":     0,
	"seed-builder":  1,
	"seed-research": 2,
}

// pickerAgents filters a listAgents result down to what the picker
// surfaces should offer this user. Non-admins no longer see the
// framework seeds — they build their own agents via the wizard (or
// clone the crafted seeds as templates), so the retiring seeds stop
// being selectable; existing seed sessions become unreachable, which
// is the intended "non-usable" posture. Admins keep the seeds for
// framework maintenance. App Agents are technically seeds too
// (registered through seedAgents), but they're an app's own surface
// governed by their Hidden posture in agentPickerOptions — exempt.
// listAgents itself stays unfiltered: Builder runs, dispatch, seed
// shadows, and template cloning all still resolve seeds by ID.
func pickerAgents(agents []AgentRecord, admin bool) []AgentRecord {
	if admin {
		return agents
	}
	out := make([]AgentRecord, 0, len(agents))
	for _, a := range agents {
		if isSeedID(a.ID) {
			if _, isApp := appagents.AppAgentByID(a.ID); !isApp {
				continue
			}
		}
		out = append(out, a)
	}
	return out
}

// agentPickerOptions builds the Agency agent-picker's GROUPED options — Built-in
// / Conversation Agents / Specialized Agents / one group per owning app — plus
// the cortex-session map and the sub-agents-by-parent map. The "— select agent —"
// placeholder is NOT included; callers prepend their own.
//
// This is the SINGLE source for the picker grouping. The chat page renders from
// it initially AND the SSE-driven refreshAgentDropdown rebuilds from the same
// grouped options (via /api/agent-options) — so a Builder action no longer
// collapses the 4-way grouping into Built-in/Custom ("separators disappear").
func agentPickerOptions(agents []AgentRecord) (opts []ui.SelectOption, cortex map[string]string, subs map[string][]map[string]string) {
	cortex = map[string]string{}
	subs = map[string][]map[string]string{}
	var builtIns, conversation, customs, appAgents []agentPickerRow
	for _, a := range agents {
		if a.Cortex {
			cortex[a.ID] = cortexSessionID(a.ID)
		}
		// Sub-agents (OwnedBy set) live in the secondary picker, not the main one.
		if a.OwnedBy != "" {
			subs[a.OwnedBy] = append(subs[a.OwnedBy], map[string]string{"id": a.ID, "name": a.Name})
			continue
		}
		// Clone-only template seeds (seed-kb) are Builder's raw material, never
		// directly runnable — out of every group.
		if isCloneOnlySeed(a.ID) {
			continue
		}
		// App agents get their own per-app group; a Hidden app agent stays out.
		if spec, isApp := appagents.AppAgentByID(a.ID); isApp {
			if a.Hidden {
				continue
			}
			appAgents = append(appAgents, agentPickerRow{ID: a.ID, Name: a.Name, App: spec.OwningApp})
		} else if ord, ok := agentPickerBuiltInOrder[a.ID]; ok {
			builtIns = append(builtIns, agentPickerRow{ID: a.ID, Name: a.Name, Order: ord})
		} else if a.Cortex {
			conversation = append(conversation, agentPickerRow{ID: a.ID, Name: a.Name})
		} else {
			customs = append(customs, agentPickerRow{ID: a.ID, Name: a.Name})
		}
	}
	sort.Slice(builtIns, func(i, j int) bool { return builtIns[i].Order < builtIns[j].Order })
	sort.Slice(conversation, func(i, j int) bool { return conversation[i].Name < conversation[j].Name })
	sort.Slice(customs, func(i, j int) bool { return customs[i].Name < customs[j].Name })
	sort.Slice(appAgents, func(i, j int) bool {
		if appAgents[i].App != appAgents[j].App {
			return appAgents[i].App < appAgents[j].App
		}
		return appAgents[i].Name < appAgents[j].Name
	})
	for _, a := range builtIns {
		opts = append(opts, ui.SelectOption{Value: a.ID, Label: a.Name, Group: "Built-in"})
	}
	for _, a := range conversation {
		opts = append(opts, ui.SelectOption{Value: a.ID, Label: a.Name, Group: "Conversation Agents"})
	}
	for _, a := range customs {
		opts = append(opts, ui.SelectOption{Value: a.ID, Label: a.Name, Group: "Specialized Agents"})
	}
	for _, a := range appAgents {
		group := a.App
		if group == "" {
			group = "App Agents"
		}
		opts = append(opts, ui.SelectOption{Value: a.ID, Label: a.Name, Group: group})
	}
	return opts, cortex, subs
}

// handleAgentPickerOptions returns the grouped agent-picker options + sub-agents
// map so the client rebuilds the dropdown with the SAME grouping the initial
// server paint used. GET /api/agent-options.
func (T *OrchestrateApp) handleAgentPickerOptions(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	opts, _, subs := agentPickerOptions(pickerAgents(listAgents(udb, user), AuthIsAdmin(AuthDB(), r)))
	writeJSON(w, map[string]any{"options": opts, "sub_agents": subs})
}
