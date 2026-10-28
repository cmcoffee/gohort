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

// agentNameOptions is the picker's list: the top-level agents, then each one's
// sub-agents under a heading naming the parent.
//
// Sub-agents used to be missing altogether. A sub-agent is pinned hidden (it
// has no way in of its own), and the list left out every hidden agent, so the
// one a family keeps for a job of its own, the collection's curator or the
// agent that publishes, could not be picked for it. They are listed by their
// own name, the parent in the group rather than the label, since a picker
// that saves the label (Publishing) looks the agent up by that name.
func agentNameOptions(agents []AgentRecord) []ui.SelectOption {
	top := map[string]AgentRecord{}
	var out, subs []ui.SelectOption
	for _, a := range agents {
		if isAppAgent(a.ID) || a.OwnedBy != "" || a.Hidden {
			continue
		}
		top[a.ID] = a
		out = append(out, ui.SelectOption{Value: a.ID, Label: chFirst(a.Name, a.ID)})
	}
	for _, a := range agents {
		parent, ok := top[a.OwnedBy]
		if a.OwnedBy == "" || !ok || isAppAgent(a.ID) {
			continue
		}
		subs = append(subs, ui.SelectOption{Value: a.ID, Label: chFirst(a.Name, a.ID),
			Group: chFirst(parent.Name, parent.ID) + "'s sub-agents"})
	}
	sort.SliceStable(subs, func(i, j int) bool {
		if subs[i].Group != subs[j].Group {
			return strings.ToLower(subs[i].Group) < strings.ToLower(subs[j].Group)
		}
		return strings.ToLower(subs[i].Label) < strings.ToLower(subs[j].Label)
	})
	return append(out, subs...)
}
