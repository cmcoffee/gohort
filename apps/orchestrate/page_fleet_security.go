// What holds for EVERY agent, in one place.
//
// The per-agent Security page answers "what can this one do". It cannot
// answer "what is true of all of them", and an owner with a dozen agents had
// to visit a dozen pages to find out - or to change one thing everywhere.
//
// A fleet value here is a DEFAULT, not a ceiling. An agent may override it in
// either direction, including looser: "no agent reaches the network except
// this one" is an ordinary thing to want, and a ceiling model forbids it. What
// makes that safe is that an override is visible AS an override on the agent
// that carries it, never silent.
//
// Only the decisions that genuinely fall back are here. A contact policy, a
// delegation policy and a tool's ask-mark are each read agent-first and then
// fleet-wide, so a value set here reaches an agent that has decided nothing.
// The rest of an agent's settings are fields on its own record with no
// fallback to fall back TO, and listing them here would be a page of controls
// that look like defaults and are not.

package orchestrate

import (
	"net/http"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// fleetSecurityID is the reserved id in the agent path that means "all of
// them". Not an agent, and an agent cannot be named it: the route checks this
// before it looks anything up.
const fleetSecurityID = "all"

func (T *OrchestrateApp) renderFleetSecurity(w http.ResponseWriter, r *http.Request, user string, udb Database) {
	// scope=fleet on every source: these are the rows that bind every agent,
	// and mixing an agent's own into a page about all of them is the confusion
	// the per-agent page was split to end.
	decisions := func(kind string) string {
		return T.WebPrefix() + "/api/console/permissions?kind=" + kind + "&scope=fleet"
	}
	policyURL := T.WebPrefix() + "/api/console/permissions/policy?id={_id}"
	removeURL := T.WebPrefix() + "/api/console/permissions/remove?id={_id}"
	ladder := func() []ui.RowAction {
		return []ui.RowAction{{
			Type: "segmented", Field: "_policy", PostTo: policyURL,
			Options: permissionLadder(),
		}, {
			Type: "button", Label: "Remove", Variant: "danger", OnlyIf: "_managed",
			PostTo:  removeURL,
			Confirm: "Forget this decision entirely? Every agent returns to deciding it for itself.",
		}}
	}
	band := func(group, kind, empty string) ui.Section {
		return ui.Section{
			Group:    group,
			Title:    "Applies to every agent",
			Subtitle: "Read where an agent has decided nothing of its own. An agent that has is not changed by this.",
			Body: ui.Table{
				Source:     decisions(kind),
				RowKey:     "_id",
				EmptyText:  empty,
				Columns:    []ui.Col{{Field: "Who", Label: ""}, {Field: "Detail", Label: "", Mute: true}},
				RowActions: ladder(),
			},
		}
	}

	page := ui.Page{
		Title:     "Security: all agents",
		ShowTitle: true,
		BackURL:   T.WebPrefix() + "/",
		MaxWidth:  "980px",
		Tabbed:    true,
		Nav:       HubNav("/orchestrate"),
		Sections: []ui.Section{
			{
				Group:    "Tools",
				Title:    "Tools that ask before every call",
				Subtitle: "Set here, a tool stops and asks on every agent that holds it.",
				Detail: "An agent can still be given its own answer, which wins. This is the value an agent reads when it has none.\\n\\n" +
					"A tool listed here is asking; one that is not runs without asking unless some agent says otherwise. Clear a row to stop it asking everywhere.",
				Body: ui.Table{
					Source:     decisions("tools"),
					RowKey:     "_id",
					EmptyText:  "No tool asks on every agent. Each is decided per agent, or not at all.",
					Columns:    []ui.Col{{Field: "Who", Label: "Tool"}, {Field: "Detail", Label: "", Mute: true}},
					RowActions: ladder(),
				},
			},
			{
				Group:    "Workspace",
				Title:    "Network access from the workspace",
				Subtitle: "What an agent uses when it has not decided for itself.",
				Detail: "An agent may override this either way, including looser. A default that could only tighten would forbid \"no agent reaches the network except this one\", which is an ordinary thing to want; what makes it safe is that an override shows AS an override on the agent carrying it.\n\n" +
					"Leaving this unset is not the same as blocking: unset, an agent that has decided nothing gets the framework's own answer, which is allowed.",
				Body: ui.FormPanel{
					Source:  T.WebPrefix() + "/api/console/fleet-defaults",
					PostURL: T.WebPrefix() + "/api/console/fleet-defaults",
					Method:  "PATCH",
					Fields: []ui.FormField{
						{Field: "workspace_network", Type: "select", Label: "By default",
							Options: []ui.SelectOption{
								{Value: "", Label: "Not set (allowed)"},
								{Value: "on", Label: "Allowed"},
								{Value: "off", Label: "Blocked"},
							},
							Help: "Read by every agent that has not answered this itself."},
					},
				},
			},
			band("Access", "access", "No contact decision binds every agent."),
			band("Delegation", "delegation", "No delegation decision binds every agent."),
			{
				Group:    "Requests",
				Title:    "Waiting on you",
				Subtitle: "Across every agent. A run has stopped and is holding for an answer.",
				Body: ui.Table{
					Source:    T.WebPrefix() + "/api/console/permissions?kind=requests",
					RowKey:    "_id",
					EmptyText: "Nothing is waiting.",
					Columns: []ui.Col{
						{Field: "Who", Label: ""},
						{Field: "Detail", Label: "", Mute: true},
						{Field: "Requested", Label: "Asked", Mute: true},
					},
				},
			},
		},
	}
	page.ServeHTTP(w, r)
}
