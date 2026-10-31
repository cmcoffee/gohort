package admin

import (
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/gohort/core/ui"
)

// flowsSection is Admin > Extensions > Flows: saved flows, each wiring
// connectors together and given to agents as a tool.
func (a *AdminApp) flowsSection() ui.Section {
	form := func(submit string) ui.FormPanel {
		return ui.FormPanel{
			Assist:      true,
			PostURL:     "api/flows",
			TestURL:     "api/flows/test",
			TestLabel:   "Run on the sample",
			SubmitLabel: submit,
			Fields:      flowFormFields(),
		}
	}
	edit := form("Save flow")
	edit.Source = "api/flows?name={name}"
	edit.PostURL = "api/flows?name={name}"
	return ui.Section{
		Title:    "Flows",
		Subtitle: "Connectors wired together, the same steps every time: one connector's answer is the next one's question. Each flow is a tool you can give an agent.",
		Detail: "A flow takes inputs (what the agent gives it), then runs its steps in order. Each step asks one connector for one contract, such as image.generate or speech.synthesize, and feeds the contract's inputs from the flow's inputs or an earlier step's outputs. The last step's outputs are what the flow gives back: text to the agent, files saved in its workspace.\n\n" +
			"Write what feeds a step one per line, as \"input = value\". {input.subject} is the flow's input subject, {draw.image} the image output of the step with id draw, and anything else is the value itself. In text, a reference can sit inside a sentence: \"prompt = A watercolor of {input.subject}\".\n\n" +
			"Saving checks the whole flow fits together: every step's connector gives its contract, every input a contract needs is fed, and by a value of the right type (text, number, json, file). A flow that cannot run is refused with what is wrong, not saved to fail later.\n\n" +
			"Nothing in a flow decides. An agent decides whether to run one: give it the tool flow_<name> on the agent's tools. Reach stays with the connectors, so a step's connector must be approved before the flow runs, and each call is counted and priced on that connector's row.",
		Body: ui.Stack{Children: []ui.Component{
			ui.Table{
				Source: "api/flows",
				RowKey: "name",
				Columns: []ui.Col{
					{Field: "name", Flex: 2},
					{Field: "tool", Label: "Agent tool", Mute: true, Flex: 2},
					{Field: "line", Label: "", Mute: true, Flex: 4, Line: 2},
					{Field: "status", Label: "", Mute: true, Flex: 4, Line: 2},
				},
				RowActions: []ui.RowAction{
					ui.Expand("Edit", edit),
					{Type: "button", Label: "Test", Method: "POST",
						PostTo: "api/flows/test?name={name}", ConfirmField: "test_confirm"},
					{Type: "button", Label: "Delete", Method: "DELETE",
						PostTo:  "api/flows?name={name}",
						Confirm: "Delete this flow? Agents given its tool lose it.",
						Variant: "danger"},
				},
				EmptyText: "No flows yet. Add one to wire connectors together, then give it to an agent as a tool.",
			},
			ui.ModalButton{
				Label: "Add a flow", Title: "Add a flow", Variant: "primary", Width: "760px",
				Subtitle: "Name it, say what it does, list its inputs, then its steps in order.",
				Body:     form("Add flow"),
			},
		}},
	}
}

// flowFormFields are the fields of a flow's form.
func flowFormFields() []ui.FormField {
	yesNo := []ui.SelectOption{{Value: "", Label: "No"}, {Value: "yes", Label: "Yes"}}
	types := []ui.SelectOption{
		{Value: "text", Label: "Text"}, {Value: "number", Label: "Number"},
		{Value: "json", Label: "JSON"}, {Value: "file", Label: "File"},
	}
	var contracts []ui.SelectOption
	for _, n := range service.Names() {
		contracts = append(contracts, ui.SelectOption{Value: n, Label: n})
	}
	var services []ui.SelectOption
	for _, c := range ListConnectors(RootDB) {
		if c.Kind == service.Kind {
			services = append(services, ui.SelectOption{Value: c.Name, Label: c.Name})
		}
	}
	return []ui.FormField{
		{Field: "name", Label: "Name", Placeholder: "illustrate", Required: true,
			Help: "Lower case letters, digits and _. Agents are given it as the tool flow_<name>."},
		{Field: "description", Label: "What it does", Type: "textarea", Rows: 2, Required: true,
			Placeholder: "Draws what a voice note describes.",
			Help:        "The agent reads this to decide when to run the flow."},
		{Field: "inputs", Label: "Inputs", Type: "rows", AddLabel: "Add an input",
			Help: "What the agent gives the flow. A file input is a file in the agent's workspace.",
			Columns: []ui.FormField{
				{Field: "name", Label: "Name", Type: "text", Placeholder: "note", Width: 2},
				{Field: "type", Label: "Type", Type: "select", Options: types, Width: 1},
				{Field: "required", Label: "Required", Type: "select", Options: yesNo, Width: 1},
				{Field: "description", Label: "Told to the agent as", Type: "text", OwnLine: true,
					Placeholder: "The voice note to draw."},
			}},
		{Field: "steps", Label: "Steps", Type: "rows", AddLabel: "Add a step",
			Help: "In the order they run. A later step names an earlier one's output by its id: {listen.text}.",
			Columns: []ui.FormField{
				{Field: "id", Label: "Id", Type: "text", Placeholder: "listen", Width: 1},
				{Field: "connector", Label: "Connector", Type: "combo", Options: services, Width: 2},
				{Field: "capability", Label: "Contract", Type: "select", Options: contracts, Width: 2},
				{Field: "with", Label: "Fed with, one per line", Type: "textarea", OwnLine: true, Rows: 3,
					Placeholder: "audio = {input.note}"},
			}},
		{Field: "starts", Label: "Starts when", Type: "select", Options: monitorOptions(),
			Help:   "A monitor whose fire runs the flow with no agent: a watch noticing a change, a webhook, a timer.",
			Detail: "The event's text goes into the input named below, the flow runs, and its message.send step sends what it made. A flow started this way must have a message.send step, and no required input but that one.\n\nIf the flow cannot run (past its runs for the day, or a step fails), the monitor delivers the event its own way, with why. One flow per monitor."},
		{Field: "starts_input", Label: "Its text goes into", Placeholder: "subject", ShowWhen: "starts",
			Help: "A text input of the flow."},
		{Field: "event_runs_per_day", Label: "Event runs a day", Type: "number", Min: 0, ShowWhen: "starts",
			Help: "At most this many runs a day start from the event; 0 means 24. Each run spends on its connectors with nobody watching."},
		{Field: "test", Label: "Sample inputs", Type: "textarea", Rows: 3,
			Placeholder: "subject = a lighthouse at dusk",
			Help:        "One per line, for Test. A file input left out gets the sample picture or sound its step's contract tests with."},
	}
}

// monitorOptions are every user's monitors, to start a flow from.
func monitorOptions() []ui.SelectOption {
	out := []ui.SelectOption{{Value: "", Label: "Only when an agent runs it"}}
	for _, m := range ListAllEventMonitors(RootDB) {
		out = append(out, ui.SelectOption{Value: m.Owner + ":" + m.Name, Label: m.Name + " (" + m.Owner + ", " + m.Kind + ")"})
	}
	return out
}
