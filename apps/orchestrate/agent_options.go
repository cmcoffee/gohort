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
// surfaces offer: the user's own agents, plus Builder — the ONE
// framework seed that stays, since it's how users author agents, apps,
// and tools conversationally. Every other seed is retired for everyone
// (admins included): not selectable, existing seed sessions
// unreachable — users build their own via the wizard or clone the
// crafted seeds as templates. App Agents are technically seeds too
// (registered through seedAgents), but they're an app's own surface
// governed by their Hidden posture in agentPickerOptions — exempt.
// listAgents itself stays unfiltered: Builder runs, dispatch, seed
// shadows, and template cloning all still resolve seeds by ID.
func pickerAgents(agents []AgentRecord) []AgentRecord {
	out := make([]AgentRecord, 0, len(agents))
	for _, a := range agents {
		if isSeedID(a.ID) && a.ID != "seed-builder" {
			if _, isApp := appagents.AppAgentByID(a.ID); !isApp {
				continue
			}
			// Visible app agents are an app's own surface and stay; hidden
			// ones are internals. Dropped HERE, not just in agentPickerOptions,
			// because userCanSeeAgent validates the default-agent preference
			// against this filter — the display check alone let a hidden app
			// agent be persisted as someone's default.
			if hiddenAppAgent(a.ID) {
				continue
			}
		}
		out = append(out, a)
	}
	return out
}

// chatPickerAgents is what the chat page's agent picker offers: pickerAgents
// plus the app agents it leaves out for being Hidden. The chat picker is
// where an agent is looked at and set up (Edit, Tools, Memory, Rules,
// Security, its conversations), so every app agent belongs there, under its
// app. A hidden one cannot work without its app's tools, so its composer is
// locked with a pointer to the app (appAgentLocks). pickerAgents stays
// without them: it also decides what may be someone's default agent.
func chatPickerAgents(agents []AgentRecord) []AgentRecord {
	out := pickerAgents(agents)
	for _, a := range agents {
		if hiddenAppAgent(a.ID) && a.OwnedBy == "" {
			out = append(out, a)
		}
	}
	return out
}

// appAgentLocks maps each hidden app agent to the line shown in place of the
// composer while it is selected: it is talked to in its app.
func appAgentLocks() map[string]string {
	out := map[string]string{}
	for _, sp := range appagents.AppAgents() {
		if sp.Hidden {
			app := chFirst(sp.OwningApp, "its app")
			out[sp.ID] = chFirst(sp.Name, sp.ID) + " works inside " + app + ", with the tools " + app + " gives it: talk to it there. Its settings, tools, memory, rules and conversations are here."
		}
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
		// App agents get their own per-app group. Hidden ones are filtered
		// (or not) by the caller: chatPickerAgents keeps them.
		if spec, isApp := appagents.AppAgentByID(a.ID); isApp {
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
		opts = append(opts, ui.SelectOption{Value: a.ID, Label: a.Name, Group: agentGroup(a.ID, "App agents")})
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
	agents := chatPickerAgents(listAgents(udb, user))
	opts, cortex, subs := agentPickerOptions(agents)
	// The two Cortex maps ride along, so an agent Builder just created gets
	// its Cortex row without a reload. They were only ever written into the
	// page when it loaded, which left a new agent without one.
	writeJSON(w, map[string]any{"options": opts, "sub_agents": subs,
		"cortex_agents": cortex, "record_agents": recordAgentsFor(agents, cortex)})
}

// recordAgentsFor maps every agent that does NOT read its Cortex to its
// Cortex thread, which the panel pins read-only as that agent's record.
func recordAgentsFor(agents []AgentRecord, cortex map[string]string) map[string]string {
	out := map[string]string{}
	for _, a := range agents {
		if _, reads := cortex[a.ID]; !reads {
			out[a.ID] = cortexSessionID(a.ID)
		}
	}
	return out
}
