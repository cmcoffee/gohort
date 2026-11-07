package admin

import (
	"github.com/cmcoffee/gohort/core/ui"
)

// skillsSections is the skills part of the admin page: Skills, Pipelines, Agent Capabilities — Outward & Spending.
func (a *AdminApp) skillsSections() []ui.Section {
	return []ui.Section{
		{
			Title:    "Skills",
			Subtitle: "Domain packs the assistant draws on: instructions plus optional knowledge sources.",
			Detail:   "The sources are attached collections and research sources (named by their tool names). The LLM reaches a skill via read_skill (pull its approach), skill_knowledge_search (search its sources, collections and research sources merged) and skill_knowledge_fetch_doc.\n\nA skill with Triggers also auto-injects its instructions when they match the turn, for example *.pdf. No activation and no sub-agents: stateless calls.\n\nBuilder is the canonical authoring path; this surface manages what is authored. Disabled skills are hidden from the LLM. Export a skill, or all of them, as a portable bundle: instructions and bundled tool scripts travel inline, secrets never do, and imports land disabled for review.",
			Body: ui.Stack{Children: []ui.Component{ui.Table{
				Source: "api/skills",
				RowKey: "id",
				Columns: []ui.Col{
					{Field: "name", Flex: 1},
					{Field: "description", Flex: 2, Mute: true},
					{
						Field: "disabled", Label: "Status", Type: "dot",
						// Green = active, red = disabled. Hover
						// tooltip carries the word so screen
						// readers / colorblind users still see it.
						Badges: []ui.BadgeMapping{
							{Value: true, Label: "Disabled", Color: "danger"},
							{Value: false, Label: "Active", Color: "success"},
						},
					},
					// The cases in a few words; blank unless the skill has them.
					{Field: "conditions", Label: "Conditions", Mute: true},
				},
				RowActions: []ui.RowAction{
					ui.Expand("Edit", ui.Stack{
						Children: []ui.Component{
							ui.FormPanel{
								Assist:  true,
								Source:  "api/skills/{id}?view=form",
								PostURL: "api/skills",
								Method:  "POST",
								Fields: []ui.FormField{
									{Field: "name", Type: "text", Label: "Name"},
									{Field: "description", Type: "textarea", Label: "Description", Rows: 2,
										Help:   "One-sentence \"use when…\" hint. Write it as a decision shape, not a label.",
										Detail: "It surfaces in the LLM's \"Available skills\" prompt block, so the model can judge when to call read_skill or skill_knowledge_search."},
									{Field: "triggers", Type: "tags", Label: "Triggers (optional)",
										Help:   "When ANY trigger matches the turn, the skill's instructions inject automatically.",
										Detail: "The injection is deterministic. A pattern with * or ?, such as *.pdf, matches attachment filenames; anything else is a case-insensitive substring of the message.\n\nLeave it empty for a knowledge skill the LLM reaches for explicitly via skill_knowledge_search."},
									{Field: "instructions", Type: "sections", SectionsAllowFree: true, Label: "Instructions (markdown)", Rows: 6,
										Help:   "The skill's approach: the lens for applying its knowledge.",
										Detail: "It is returned by read_skill, attached to the first skill_knowledge_search result, and injected when a trigger matches."},
									skillConditionsField(),
								},
							},
							// Allowed tools — picker from the registered
							// tool pool. Same pattern Tool Groups uses;
							// avoids typos and exposes the user to what's
							// actually available. Posts independently of
							// the FormPanel above (the chip click immediately
							// updates the record).
							ui.Card{HTML: `<div style="font-size:var(--fs-xs, 0.78rem);color:#8b949e;text-transform:uppercase;letter-spacing:0.04em">Allowed tools</div><div style="font-size:var(--fs-xs, 0.75rem);color:#6e7681">Tools the LLM may call while this skill is active. Skills with no selection inherit the agent's normal tool set.</div>`},
							ui.ChipPicker{
								OptionsSource: "api/tool-groups/registry",
								RecordSource:  "api/skills/{id}",
								Field:         "allowed_tools",
								// PATCH sends this list alone; the handler
								// decodes it onto the stored skill.
								PostTo:     "api/skills?id={id}",
								Method:     "PATCH",
								NameField:  "name",
								LabelField: "name",
								DescField:  "description",
							},
							// Attached collections — picker from the
							// current user's Document Collections. Same
							// pattern; flipping a chip POSTs the full
							// record back. Empty list = no extra corpus
							// injected when this skill activates. The
							// list endpoint lives at api/collections
							// (admin-side read view; create/edit/delete
							// happens on the Knowledge surface).
							ui.Card{HTML: `<div style="font-size:var(--fs-xs, 0.78rem);color:#8b949e;text-transform:uppercase;letter-spacing:0.04em">Attached collections</div><div style="font-size:var(--fs-xs, 0.75rem);color:#6e7681">Document Collections merged into RAG recall while this skill is active. Manage the collections themselves on the Knowledge page.</div>`},
							ui.ChipPicker{
								OptionsSource: "api/collections",
								RecordSource:  "api/skills/{id}",
								Field:         "attached_collections",
								// PATCH sends this list alone; the handler
								// decodes it onto the stored skill.
								PostTo:     "api/skills?id={id}",
								Method:     "PATCH",
								NameField:  "id",
								LabelField: "name",
								DescField:  "description",
							},
						},
					}),
					// Active skill → "Disable" button; disabled skill →
					// "Enable" button. Partial-update via the
					// ?action=enable|disable query param so the rest
					// of the record stays intact.
					{Type: "button", Label: "Disable",
						PostTo: "api/skills?action=disable&id={id}",
						Method: "POST",
						HideIf: "disabled"},
					{Type: "button", Label: "Enable",
						PostTo: "api/skills?action=enable&id={id}",
						Method: "POST",
						OnlyIf: "disabled"},
					{Type: "button", Label: "Export", Method: "client",
						PostTo: "skills_export", Compact: true},
					{Type: "button", Label: "Delete",
						PostTo:  "api/skills?id={id}",
						Method:  "DELETE",
						Confirm: "Delete this skill? The definition is gone for good; Builder will need to re-author if you want it back.",
						Variant: "danger"},
				},
				EmptyText: "No skills defined. Talk to Builder in Agents to author one: \"create a skill called X that fires when…\".",
			},
				// Export all skills (every owner) as one bundle.
				ui.Toolbar{
					Actions: []ui.ToolbarAction{
						{Label: "Export all skills", Method: "client", URL: "skills_export_all"},
					},
				},
			}},
		},
		{
			Title:    "Agent Capabilities: Outward & Spending",
			Subtitle: "The blast radius of each agent: what it can do that reaches REAL PEOPLE or COSTS MONEY.",
			Detail:   "Read-only, derived live from each agent's bound channels, its messaging tools, and the paid credentials its attached tools dispatch through. Agents with no outward or spending reach are omitted, so this list IS the surface to watch.",
			Body: ui.Table{
				Source: "/orchestrate/api/capabilities",
				RowKey: "agent_id",
				Columns: []ui.Col{
					{Field: "agent", Label: "Agent", Flex: 1},
					{Field: "message_summary", Label: "Can message (people)", Flex: 2},
					{Field: "spend_summary", Label: "Can spend (paid APIs)", Flex: 2},
				},
				EmptyText: "No agent has outward or spending capability: none can text people, send email, or spend through a paid credential.",
			},
		},
	}
}

// skillConditionsField is the conditions list on the skill editor, the same
// list the Extensions skill form offers: the first row is the If (when it
// holds, the skill applies as written), a row after it an Else if with its
// own condition and instructions, or the Else, which closes the list.
func skillConditionsField() ui.FormField {
	return ui.FormField{
		Field: "conditions", Type: "rows", Label: "Conditions (optional)",
		AddLabel:       "+ Add condition",
		Placeholder:    "None: the skill applies whenever it is used.",
		AddBlockedWhen: "kind:else",
		AddBlockedNote: "The Else is last. To add another condition, change it to Else if.",
		Help:           "Add one when this skill is right only in some cases. The first is the If: when it holds, the skill applies as written. Add more for Else if (another case, with its own instructions) or Else (everything the cases above do not cover).",
		Detail:         "The framework settles the conditions before the skill is handed over, and the agent gets only the case that applies. A lookup is answered by a short check with the skill's tools (one check for all of them); a mention is decided from the message. With no Else, the skill does not apply when no condition holds. Give the skill Triggers so this happens before the agent answers on matching messages.",
		Columns: []ui.FormField{
			// The top line holds the two choices; the question, or the words,
			// gets a line of its own, so it is never the box squeezed beside
			// a hidden column.
			{Field: "kind", Label: "Case", Type: "select", Width: 1, FirstDefault: "if", Default: "else_if", Options: []ui.SelectOption{
				{Value: "if", Label: "If"}, {Value: "else_if", Label: "Else if"}, {Value: "else", Label: "Else"},
			}},
			{Field: "check", Label: "Decided by", Type: "select", Width: 2, HideWhen: "kind:else", Options: []ui.SelectOption{
				{Value: "lookup", Label: "Looking it up"}, {Value: "mentions", Label: "The message mentioning"},
			}},
			{Field: "question", Label: "Question", OwnLine: true, HideWhen: "kind:else||check:mentions",
				Placeholder: "Is the customer on version 4 or later?"},
			{Field: "mentions", Label: "Words", OwnLine: true, HideWhen: "kind:else||check:!mentions",
				Placeholder: "refund, chargeback, *.pdf"},
			{Field: "how", Label: "How to check", OwnLine: true, HideWhen: "kind:else||check:mentions",
				Placeholder: "Look up the account with the CRM tool and read its app version."},
			{Field: "instructions", Label: "Instructions for this case", Type: "sections", OwnLine: true, HideWhen: "kind:if",
				Placeholder: "Empty: use Edit to write what the agent does in this case."},
		},
	}
}
