package admin

import (
	"github.com/cmcoffee/gohort/core/ui"
)

// skillsSections is the skills part of the admin page: Skills, Pipelines, Agent Capabilities — Outward & Spending.
func (a *AdminApp) skillsSections() []ui.Section {
	return []ui.Section{
		{
			Title:    "Skills",
			Subtitle: "Domain packs the assistant draws on in its own context — instructions plus optional knowledge sources (attached collections and/or source-hooks). The LLM reaches a skill via read_skill (pull its approach), skill_knowledge_search (search its sources — collections + source-hooks merged) and skill_knowledge_fetch_doc. A skill with Triggers also auto-injects its instructions when they match the turn (e.g. *.pdf). No activation, no sub-agents — stateless calls. Builder is the canonical authoring path; this surface manages what's authored. Disabled skills are hidden from the LLM. Export a skill (or all skills) as a portable bundle — instructions and bundled tool scripts travel inline, secrets never do; imports land disabled for review.",
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
				},
				RowActions: []ui.RowAction{
					ui.Expand("Edit", ui.Stack{
						Children: []ui.Component{
							ui.FormPanel{
								Source:  "api/skills/{id}",
								PostURL: "api/skills",
								Method:  "POST",
								Fields: []ui.FormField{
									{Field: "name", Type: "text", Label: "Name"},
									{Field: "description", Type: "textarea", Label: "Description", Rows: 2,
										Help: "One-sentence \"use when…\" hint. Surfaces in the LLM's \"Available skills\" prompt block so it judges when to read_skill / skill_knowledge_search. Write it as a decision shape, not a label."},
									{Field: "triggers", Type: "tags", Label: "Triggers (optional)",
										Help: "When ANY trigger matches the turn, the skill's instructions inject automatically (deterministic). A pattern with * or ? (e.g. *.pdf) matches attachment filenames; anything else is a case-insensitive substring of the message. Leave empty for a knowledge skill the LLM reaches for explicitly via skill_knowledge_search."},
									{Field: "instructions", Type: "textarea", Label: "Instructions (markdown)", Rows: 10,
										Help: "The skill's approach. Returned by read_skill, attached to the first skill_knowledge_search result, and injected when a trigger matches — the lens for applying the skill's knowledge."},
								},
							},
							// Allowed tools — picker from the registered
							// tool pool. Same pattern Tool Groups uses;
							// avoids typos and exposes the user to what's
							// actually available. Posts independently of
							// the FormPanel above (the chip click immediately
							// updates the record).
							ui.Card{HTML: `<div style="font-size:0.78rem;color:#8b949e;text-transform:uppercase;letter-spacing:0.04em">Allowed tools</div><div style="font-size:0.75rem;color:#6e7681">Tools the LLM may call while this skill is active. Skills with no selection inherit the agent's normal tool set.</div>`},
							ui.ChipPicker{
								OptionsSource: "api/tool-groups/registry",
								RecordSource:  "api/skills/{id}",
								Field:         "allowed_tools",
								PostTo:        "api/skills",
								Method:        "POST",
								NameField:     "name",
								LabelField:    "name",
								DescField:     "description",
							},
							// Attached collections — picker from the
							// current user's Document Collections. Same
							// pattern; flipping a chip POSTs the full
							// record back. Empty list = no extra corpus
							// injected when this skill activates. The
							// list endpoint lives at api/collections
							// (admin-side read view; create/edit/delete
							// happens on the Knowledge surface).
							ui.Card{HTML: `<div style="font-size:0.78rem;color:#8b949e;text-transform:uppercase;letter-spacing:0.04em">Attached collections</div><div style="font-size:0.75rem;color:#6e7681">Document Collections merged into RAG recall while this skill is active. Manage the collections themselves on the Knowledge page.</div>`},
							ui.ChipPicker{
								OptionsSource: "api/collections",
								RecordSource:  "api/skills/{id}",
								Field:         "attached_collections",
								PostTo:        "api/skills",
								Method:        "POST",
								NameField:     "id",
								LabelField:    "name",
								DescField:     "description",
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
				EmptyText: "No skills defined. Talk to Builder in Agents to author one — \"create a skill called X that fires when…\".",
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
			Title:    "Pipelines",
			Subtitle: "Declarative multi-stage workflows authored in Agents (the pipeline tool, or via Builder). This surface lists every user's pipelines and lets you inspect the stages or delete a definition. Deleting one also drops it from any agent it was attached to.",
			Body: ui.Table{
				Source:       "api/pipelines",
				RecordsField: "pipelines",
				RowKey:       "id",
				Columns: []ui.Col{
					{Field: "owner", Label: "Owner", Flex: 1, Mute: true},
					{Field: "name", Label: "Name", Flex: 1},
					{Field: "description", Label: "Description", Flex: 2, Mute: true},
					{Field: "stages", Label: "Stages", Flex: 0},
				},
				RowActions: []ui.RowAction{
					ui.Expand("View", ui.JSONView{Field: "detail", Title: "Definition"}),
					// Scope pill — Global (all agents run it) + per-agent
					// attach, mirroring the tool scope control.
					{Type: "button", Label: "Access", Method: "client",
						PostTo: "pipeline_scope_manage"},
					{Type: "button", Label: "Delete",
						PostTo:     "api/pipelines?id={id}",
						Method:     "DELETE",
						Confirm:    "Delete this pipeline definition? It's removed for the owning user and detached from any agent that used it. Authoring it again means re-creating the stages.",
						Variant:    "danger",
						Optimistic: true},
				},
				EmptyText: "No pipelines defined. They're authored in Agents via the pipeline tool or Builder.",
			},
		},
		{
			Title:    "Agent Capabilities — Outward & Spending",
			Subtitle: "The blast radius of each agent: what it can do that reaches REAL PEOPLE or COSTS MONEY. Read-only, derived live from each agent's bound channels, its messaging tools, and the paid credentials its attached tools dispatch through. Agents with no outward or spending reach are omitted — so this list IS the surface to watch.",
			Body: ui.Table{
				Source: "/orchestrate/api/capabilities",
				RowKey: "agent_id",
				Columns: []ui.Col{
					{Field: "agent", Label: "Agent", Flex: 1},
					{Field: "message_summary", Label: "Can message (people)", Flex: 2},
					{Field: "spend_summary", Label: "Can spend (paid APIs)", Flex: 2},
				},
				EmptyText: "No agent has outward or spending capability — none can text people, send email, or spend through a paid credential.",
			},
		},
	}
}
