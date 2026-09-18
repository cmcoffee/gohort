package orchestrate

// Listing a user's agents for a picker, on behalf of packages that cannot.
//
// core cannot reach an AgentRecord, and the apps that need to OFFER a choice of
// agent are not agent-aware either: a knowledge collection choosing who is in
// charge of it, an admin naming the agent behind a publish destination. Without
// a seam each of them falls back to a free-text box, which is how a config
// field ends up holding a name nobody ever checked.

import (
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// registerAgentNamer installs the closure core calls to list a user's agents.
// Call once at startup.
//
// Hidden and app-owned agents are left out, matching what the machine editor's
// own picker offers. An app agent is the app's, not the person's, and offering
// one invites naming something whose behaviour belongs to a surface the person
// does not control.
func registerAgentNamer(app *OrchestrateApp) {
	RegisterAgentNamer(func(user string) []ui.SelectOption {
		if app == nil || app.DB == nil {
			return nil
		}
		udb := UserDB(app.DB, user)
		var out []ui.SelectOption
		for _, a := range listAgents(udb, user) {
			if isAppAgent(a.ID) || a.Hidden {
				continue
			}
			out = append(out, ui.SelectOption{Value: a.ID, Label: chFirst(a.Name, a.ID)})
		}
		return out
	})
}
