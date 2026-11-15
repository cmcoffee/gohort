package orchestrate

import (
	"net/http"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/gettingstarted"
)

// registerAssistantStep adds the Agents step: an assistant of their own, for
// whoever can open Agents. Done once they own a top-level agent, the same
// test that sends a newcomer to the first-run wizard (needsFirstRunSetup),
// whose door the link is. Registered once the app knows where it is mounted
// (links.go), so the link and the access check follow the mount.
func registerAssistantStep() {
	mount := orchMount
	gettingstarted.Register(gettingstarted.Step{
		ID: "assistant", Order: 20,
		Title: "Create your assistant",
		Help:  "The agent you talk to. It hands work to the others you add later.",
		Link:  orchPath(""),
		Applies: func(r *http.Request, user string) bool {
			return UserHasAppAccess(r, mount)
		},
		Done: func(user string) bool {
			if orchestrateBaseDB == nil {
				return true
			}
			return !needsFirstRunSetup(listAgents(UserDB(orchestrateBaseDB, user), user), user)
		},
	})
}
