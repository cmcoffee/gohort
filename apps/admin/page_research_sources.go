package admin

import (
	"github.com/cmcoffee/gohort/core/ui"
)

// researchSourcesSections is the Research Sources part of the admin page:
// the search connectors referenced for research and for agents.
func (a *AdminApp) researchSourcesSections() []ui.Section {
	return []ui.Section{{
		Title:    "Research Sources",
		Subtitle: "Search connectors that research asks on its topics, and that agents can be given as tools.",
		Detail: "A source is a search connector referenced here: research asks it on the topics you give, or on every run, and given a tool name, it is a tool agents have by that name, listed under Research sources in each agent's tools, where it can be taken off an agent. A skill may also grant it by that name. Its answers are kept in a cache, so the same or a near query is not asked twice.\n\n" +
			"A source turned off is not asked. The connector itself, how the service is reached and how its results are read, stays under Connectors, where it is tested and fixed. Templates has research sources ready to add: PubMed, OpenAlex, SEC EDGAR, Europe PMC, Semantic Scholar, CORE, CourtListener and Westlaw.\n\n" +
			"The connector Web Search is set to is not asked here as well: it already answers web_search.",
		Body: ui.Stack{Children: []ui.Component{
			ui.Table{
				Source: "api/research-sources",
				RowKey: "connector",
				Columns: []ui.Col{
					{Field: "connector", Flex: 2},
					{Field: "on", Type: "badge", Label: "", Badges: []ui.BadgeMapping{
						{Value: true, Label: "On", Color: "success"},
						{Value: false, Label: "Off", Color: "mute"},
					}},
					{Field: "use", Label: "", Flex: 4, Line: 2},
					{Field: "summary", Label: "", Mute: true, Flex: 4, Line: 2},
				},
				RowActions: []ui.RowAction{
					ui.Expand("Edit", ui.FormPanel{
						Source:      "api/research-sources?name={connector}",
						PostURL:     "api/research-sources",
						SubmitLabel: "Save",
						Fields:      researchSourceFields(true),
					}),
					{Type: "button", Label: "Turn on", PostTo: "api/research-sources?action=on&name={connector}",
						Method: "POST", HideIf: "on", Variant: "success"},
					{Type: "button", Label: "Turn off", PostTo: "api/research-sources?action=off&name={connector}",
						Method: "POST", OnlyIf: "on"},
					{Type: "button", Label: "Remove", PostTo: "api/research-sources?name={connector}", Method: "DELETE",
						Confirm: "Stop asking this connector as a research source? The connector stays under Connectors.", Variant: "danger"},
				},
				EmptyText: "No research sources. Add a search connector as one below, or add one from Templates first.",
			},
			ui.ModalButton{
				Label:    "Add source",
				Title:    "Add a research source",
				Subtitle: "Pick a search connector and say when it is asked.",
				Variant:  "primary",
				Width:    "640px",
				Body: ui.FormPanel{
					PostURL:     "api/research-sources",
					SubmitLabel: "Add source",
					Fields:      researchSourceFields(false),
				},
			},
		}},
	}}
}

// researchSourceFields is a source's form: its connector (fixed once it is
// one), when research asks it, and whether agents can.
func researchSourceFields(editing bool) []ui.FormField {
	connector := ui.FormField{Field: "connector", Label: "Search connector", Type: "select", Options: append(researchSourceOptions(), addAnother("research")),
		Required: true, Help: "A connector that searches; + Add another... adds one from a template (PubMed, OpenAlex, CourtListener, ...)."}
	if editing {
		connector = ui.FormField{Field: "connector", Label: "Search connector", Type: "readonly"}
	}
	return []ui.FormField{
		connector,
		{Field: "topics", Label: "Topics", Type: "tags", Placeholder: "legal, medical, financial",
			Help: "The research topics it is asked on."},
		{Field: "always", Label: "Ask on every research run", Type: "toggle"},
		{Field: "offer", Label: "Offer to agents", Type: "toggle",
			Help: "It becomes a tool agents have, by the name below: listed under Research sources in each agent's tools, where it can be taken off an agent."},
		{Field: "tool_name", Label: "Name agents know it by", Placeholder: "pubmed_search", ShowWhen: "offer",
			Help: "Lowercase letters, digits and _. A skill grants it as a tool of its own by this name."},
		{Field: "tool_description", Label: "When agents should ask it", Type: "textarea", Rows: 3, ShowWhen: "offer",
			Help: "What it covers, and when to choose it over web_search."},
		{Field: "off", Label: "On", Type: "toggle", Invert: true, Help: "Off keeps the source and asks nothing of it."},
	}
}
