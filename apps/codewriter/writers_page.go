// The Writers management surface: create a writer, bind it to an agent, and
// build its foundation.
//
// A separate page rather than another modal in the panel. The panel's libraries
// (values, contexts, templates) are things you reach for MID-WRITE, so they
// belong next to the editor; a writer is set up once and then chosen from a
// dropdown for months. Putting its editor in the writing surface would add a
// permanent control to the place where the work happens, for a task nobody does
// while working.
package codewriter

import (
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

func (T *CodeWriterAgent) handleWritersPage(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	// The agent choices, resolved server-side at render. Reached through the
	// core seam rather than by importing orchestrate: an app depending on
	// another app is the shape this framework spends its effort avoiding.
	agents := []ui.SelectOption{{Value: "", Label: "— no agent —"}}
	for _, a := range ListAgentsFor(user) {
		label := a.Name
		if strings.TrimSpace(a.Description) != "" {
			label += " — " + a.Description
		}
		agents = append(agents, ui.SelectOption{Value: a.ID, Label: label})
	}
	// Says WHY the list is empty, which is otherwise indistinguishable from
	// having no agents: one is a deployment without the Agents app, the other
	// is a user who has not made one yet, and they need different actions.
	agentHelp := "The agent that supplies this writer's knowledge foundation. Pick one that already knows the domain — its memory, attached sources and collections are what the foundation is drawn from."
	if !AgentAskReady() {
		agentHelp = "Agent dispatch is not available in this deployment, so a foundation can't be generated — you can still write one by hand below."
	} else if len(agents) == 1 {
		agentHelp = "You have no agents yet. Create one in Agents first, then come back and bind it here — or write the foundation by hand below."
	}

	page := ui.Page{
		Title:     "Writers",
		ShowTitle: true,
		BackURL:   "/codewriter",
		Sections: []ui.Section{
			{
				Title:    "Writers",
				Subtitle: "A writer is a named configuration CodeWriter works under. Its knowledge foundation is standing context for everything written while it's selected, and it is treated as authoritative: where the foundation and the model's general knowledge disagree, the foundation wins and the reply says so.",
				Body: ui.Table{
					Source: "api/writers",
					RowKey: "id",
					Columns: []ui.Col{
						{Field: "name", Label: "Name"},
						{Field: "description", Label: "Domain"},
						{Field: "brief_date", Label: "Foundation built"},
					},
					EmptyText: "No writers yet. Add one below, bind it to an agent, then press Build foundation.",
					RowActions: []ui.RowAction{
						{Type: "button", Label: "Build foundation", Method: "POST", PostTo: "api/writer/{id}/refresh",
							Confirm: "Ask this writer's agent to (re)write its knowledge foundation? This runs the agent and replaces the stored foundation."},
						{Type: "button", Label: "Delete", Method: "DELETE", PostTo: "api/writer/{id}",
							Confirm: "Delete this writer? Snippets written under it are not affected."},
					},
				},
			},
			{
				Title:    "Add or edit a writer",
				Subtitle: "Saving with an existing name's ID updates it. Leave the foundation blank and press Build foundation on the row above to have the agent write it.",
				Body: ui.FormPanel{
					Source: "api/writers",
					Method: "POST",
					Fields: []ui.FormField{
						{Field: "id", Label: "ID", Type: "text",
							Help: "Leave blank to create a new writer. Paste an existing ID to edit that one."},
						{Field: "name", Label: "Name", Type: "text", Required: true,
							Placeholder: "Acme API client"},
						{Field: "description", Label: "Domain", Type: "text",
							Placeholder: "The Acme billing API and our client conventions",
							Help:        "One line naming what this writer writes. It's also what the agent is asked to write the foundation about, so be specific."},
						{Field: "agent_id", Label: "Foundation agent", Type: "select",
							Options: agents, Help: agentHelp},
						{Field: "brief", Label: "Knowledge foundation", Type: "textarea",
							Placeholder: "Interfaces, schemas, conventions, required patterns, and the mistakes people actually make.",
							Help:        "Editable by hand — the agent's draft is a starting point, and whoever owns these conventions gets the last word. Saving with this blank keeps the stored foundation rather than erasing it."},
					},
				},
			},
		},
	}
	page.ServeHTTP(w, r)
}
