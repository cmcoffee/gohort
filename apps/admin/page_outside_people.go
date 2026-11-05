package admin

import "github.com/cmcoffee/gohort/core/ui"

// outsidePeopleAdminTable is every owner's people outside gohort, served by
// the agents app (apps/orchestrate/outside_people.go) at absolute paths, as
// the Bridges section is. The per-person routes there accept the person's
// owner or an administrator.
func outsidePeopleAdminTable() ui.Table {
	const api = "/orchestrate/api"
	view := ui.Stack{Children: []ui.Component{
		ui.Table{
			Source:    api + "/outside-people/notes?id={id}",
			RowKey:    "id",
			EmptyText: "The agent has saved nothing about them.",
			Columns:   []ui.Col{{Field: "note", Label: "What the agent saved", Flex: 4}, {Field: "when", Label: "Saved", Flex: 1}},
		},
		ui.Table{
			Source:    api + "/outside-people/conversations?id={id}",
			RowKey:    "ref",
			EmptyText: "No conversations.",
			Columns:   []ui.Col{{Field: "title", Label: "Conversation", Flex: 3}, {Field: "last", Label: "Last", Flex: 1}, {Field: "messages", Label: "Messages", Flex: 1}},
			RowActions: []ui.RowAction{ui.Expand("Read", ui.DisplayPanel{
				Source: api + "/outside-people/conversation?ref={ref}",
				Pairs:  []ui.DisplayPair{{Label: "Transcript", Field: "transcript", Block: true}},
			})},
		},
	}}
	return ui.Table{
		Source:    api + "/outside-people/all",
		RowKey:    "id",
		EmptyText: "No agent has served anyone outside gohort yet.",
		Columns: []ui.Col{
			{Field: "owner", Label: "Owner", Flex: 1},
			{Field: "agent", Label: "Agent", Flex: 1},
			{Field: "person", Label: "Person", Flex: 2},
			{Field: "through", Label: "Through", Flex: 1},
			{Field: "seen", Label: "Last seen", Flex: 2},
			{Field: "activity", Label: "Activity", Flex: 2},
		},
		RowActions: []ui.RowAction{
			ui.ModalAction("View", view),
			{Type: "button", Label: "Clear memory", PostTo: api + "/outside-people/clear?id={id}",
				Confirm: "Clear what the agent remembers about this person? Their conversations stay."},
			{Type: "button", Label: "Delete", Variant: "danger", PostTo: api + "/outside-people/delete?id={id}",
				Confirm: "Remove this person entirely: their memory and conversations, from every one of this owner's agents they reached. This cannot be undone."},
		},
	}
}
