package orchestrate

// Listing a user's agents for a picker, on behalf of packages that cannot.
//
// core cannot reach an AgentRecord, and the apps that need to OFFER a choice of
// agent are not agent-aware either: a knowledge collection choosing who is in
// charge of it, an admin naming the agent behind a publish destination. Without
// a seam each of them falls back to a free-text box, which is how a config
// field ends up holding a name nobody ever checked.

import (
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// registerAgentNamer installs the closure core calls to list a user's agents.
// Call once at startup.
//
// App-owned agents are left out, and top-level agents the person hid. An app
// agent is the app's, not the person's, and offering one invites naming
// something whose behaviour belongs to a surface the person does not control.
func registerAgentNamer(app *OrchestrateApp) {
	RegisterAgentNamer(func(user string) []ui.SelectOption {
		if app == nil || app.DB == nil {
			return nil
		}
		return agentNameOptions(listAgents(UserDB(app.DB, user), user))
	})
}

// agentNameOptions is the picker's list: each top-level agent, with its
// sub-agents indented under it.
//
// Sub-agents used to be missing altogether. A sub-agent is pinned hidden (it
// has no way in of its own), and the list left out every hidden agent, so the
// one a family keeps for a job of its own, the collection's curator or the
// agent that publishes, could not be picked for it. They are listed by their
// own name, indented rather than labelled with the parent, since a picker
// that saves the label (Publishing) looks the agent up by that name.
func agentNameOptions(agents []AgentRecord) []ui.SelectOption {
	isTop := map[string]bool{}
	for _, a := range agents {
		if !isAppAgent(a.ID) && a.OwnedBy == "" && !a.Hidden {
			isTop[a.ID] = true
		}
	}
	subs := map[string][]AgentRecord{}
	for _, a := range agents {
		if a.OwnedBy != "" && isTop[a.OwnedBy] && !isAppAgent(a.ID) {
			subs[a.OwnedBy] = append(subs[a.OwnedBy], a)
		}
	}
	var out []ui.SelectOption
	for _, a := range agents {
		if !isTop[a.ID] {
			continue
		}
		out = append(out, ui.SelectOption{Value: a.ID, Label: chFirst(a.Name, a.ID)})
		kids := subs[a.ID]
		sort.SliceStable(kids, func(i, j int) bool {
			return strings.ToLower(chFirst(kids[i].Name, kids[i].ID)) < strings.ToLower(chFirst(kids[j].Name, kids[j].ID))
		})
		for _, k := range kids {
			out = append(out, ui.SelectOption{Value: k.ID, Label: chFirst(k.Name, k.ID), Indent: 1})
		}
	}
	return out
}
