package admin

import (
	"github.com/cmcoffee/gohort/core/ui"
)

// toolsSections is the tools part of the admin page: Persistent Tools (Pending), Global Tools, Agent-Scoped Tools, Orphaned Tools, Categories.
func (a *AdminApp) toolsSections() []ui.Section {
	return []ui.Section{
		{
			Title:    "Persistent Tools (Pending)",
			Subtitle: "LLM-discovered API patterns awaiting your approval. Approve to make permanent; reject to discard. The description is the LLM's own summary of what the tool does.",
			Body: ui.Table{
				Source:       "api/persistent-tools",
				RecordsField: "pending",
				// The records' actual TempTool fields live under .tool.* —
				// the wrapper carries `requested_at` etc on the outer
				// object. Use dotted paths to surface tool fields.
				RowKey: "tool.name",
				Columns: []ui.Col{
					{Field: "tool.name", Flex: 1},
					{Field: "owner", Flex: 0, Mute: true},
					{Field: "tool.description", Flex: 2, Mute: true},
				},
				RowActions: []ui.RowAction{
					ui.Expand("View", ui.RecordView{
						Pairs: []ui.DisplayPair{
							{Label: "Name", Field: "tool.name", Mono: true},
							{Label: "Owner", Field: "owner"},
							{Label: "Description", Field: "tool.description"},
							{Label: "Mode", Field: "tool.mode"},
							{Label: "Method", Field: "tool.method", Mono: true},
							{Label: "Command / URL template", Field: "tool.command_template", Mono: true, Block: true},
							{Label: "Body template", Field: "tool.body_template", Mono: true, Block: true},
							{Label: "Script name", Field: "tool.script_name", Mono: true},
							{Label: "Script body", Field: "tool.script_body", Block: true},
							// Helper files (imported modules, sourced scripts) the
							// entry script bundles — so a reviewer sees the whole
							// tool, not just its entry point. Empty for single-file
							// tools.
							{Label: "Bundled files", Field: "tool.workspace_files", Items: []ui.DisplayPair{
								{Field: "path", Mono: true},
							}},
							{Label: "Credential", Field: "tool.credential", Mono: true},
							{Label: "Hook capabilities", Field: "tool.hook_capabilities", Mono: true},
							{Label: "Raw network", Field: "tool.raw_network"},
							{Label: "State path", Field: "tool.state_path", Mono: true},
							{Label: "Response pipe", Field: "tool.response_pipe", Mono: true, Block: true},
							// Toolbox-mode tools bundle several endpoints under one
							// name — list each sub-action so the reviewer sees the
							// whole surface, not just the wrapper. Empty for non-
							// toolbox tools (the list pair renders nothing).
							{Label: "Actions", Field: "tool.actions", Items: []ui.DisplayPair{
								{Field: "name", Mono: true},
								{Label: "method", Field: "method", Mono: true},
								{Label: "url", Field: "url_template", Mono: true},
								{Label: "body", Field: "body_template", Mono: true, Block: true},
								{Label: "response_pipe", Field: "response_pipe", Mono: true},
								{Label: "disabled", Field: "disabled"},
								{Label: "desc", Field: "description"},
							}},
							{Label: "Requested at", Field: "requested_at", Format: "reltime"},
							{Label: "From session", Field: "requested_session", Mono: true},
						},
					}),
					{Type: "button", Label: "Approve",
						PostTo: "api/persistent-tools?action=approve&name={tool.name}&owner={owner}",
						Method: "POST", Variant: "success",
						Optimistic: true},
					{Type: "button", Label: "Reject",
						PostTo: "api/persistent-tools?action=reject&name={tool.name}&owner={owner}",
						Method: "POST", Variant: "warning",
						Confirm:    "Reject this pending tool? It'll be discarded.",
						Optimistic: true},
				},
				EmptyText: "No pending tools.",
			},
		},
		{
			Title:    "Global Tools",
			Subtitle: "User-wide tools — available to ALL of the owner's agents. \"Access\" opens the pill editor: descope a tool down to specific agents, or disable it per agent. Share publishes the tool to the deployment-wide catalog, where each user OPTS IN from their Extensions page (it no longer auto-loads for everyone); Unshare pulls it from the catalog. Delete revokes immediately. Export a tool (or all) as a portable bundle. A ⚠ badge marks a tool whose credential dependency is missing.",
			Body: ui.Stack{Children: []ui.Component{
				ui.Table{
					Source:       "api/persistent-tools",
					RecordsField: "active",
					RowKey:       "tool.name",
					Columns: []ui.Col{
						{Field: "tool.name", Flex: 1},
						{Field: "owner", Flex: 0, Mute: true},
						{Field: "tool.category", Flex: 0, Label: "Category", Mute: true},
						{Field: "has_missing", Flex: 0, Label: "Deps", Type: "badge", Badges: []ui.BadgeMapping{
							{Value: true, Label: "⚠ missing", Color: "danger"},
						}},
						{Field: "shared", Flex: 0, Label: "Shared", Type: "badge", Badges: []ui.BadgeMapping{
							{Value: true, Label: "Shared", Color: "success"},
						}},
						{Field: "tool.description", Flex: 2, Mute: true},
						{Field: "last_used_at", Format: "reltime", Mute: true},
					},
					RowActions: []ui.RowAction{
						ui.Expand("View", ui.RecordView{
							Pairs: []ui.DisplayPair{
								{Label: "Name", Field: "tool.name", Mono: true},
								{Label: "Owner", Field: "owner"},
								{Label: "Category", Field: "tool.category"},
								{Label: "Description", Field: "tool.description"},
								{Label: "Mode", Field: "tool.mode"},
								{Label: "Method", Field: "tool.method", Mono: true},
								{Label: "Command / URL template", Field: "tool.command_template", Mono: true, Block: true},
								{Label: "Body template", Field: "tool.body_template", Mono: true, Block: true},
								{Label: "Script name", Field: "tool.script_name", Mono: true},
								{Label: "Script body", Field: "tool.script_body", Block: true},
								// Helper files bundled with the entry script (imported
								// modules, sourced scripts). Empty for single-file tools.
								{Label: "Bundled files", Field: "tool.workspace_files", Items: []ui.DisplayPair{
									{Field: "path", Mono: true},
								}},
								{Label: "Credential", Field: "tool.credential", Mono: true},
								{Label: "Hook capabilities", Field: "tool.hook_capabilities", Mono: true},
								{Label: "Raw network", Field: "tool.raw_network"},
								{Label: "State path", Field: "tool.state_path", Mono: true},
								{Label: "Response pipe", Field: "tool.response_pipe", Mono: true, Block: true},
								// Toolbox-mode tools bundle several endpoints under one
								// name — list each sub-action so the whole surface is
								// visible on expand. Empty for non-toolbox tools.
								{Label: "Actions", Field: "tool.actions", Items: []ui.DisplayPair{
									{Field: "name", Mono: true},
									{Label: "method", Field: "method", Mono: true},
									{Label: "url", Field: "url_template", Mono: true},
									{Label: "body", Field: "body_template", Mono: true, Block: true},
									{Label: "response_pipe", Field: "response_pipe", Mono: true},
									{Label: "disabled", Field: "disabled"},
									{Label: "desc", Field: "description"},
								}},
								{Label: "Approved at", Field: "approved_at", Format: "reltime"},
								{Label: "Last used", Field: "last_used_at", Format: "reltime"},
							},
						}),
						// Share to all users / pull back. Mirror approve/reject: the
						// visible button is the action NOT yet taken (Share when
						// private, Unshare when shared).
						{Type: "button", Label: "Share",
							PostTo:     "api/persistent-tools?action=share&name={tool.name}&owner={owner}",
							Method:     "POST",
							HideIf:     "shared",
							Optimistic: true},
						{Type: "button", Label: "Unshare",
							PostTo:     "api/persistent-tools?action=unshare&name={tool.name}&owner={owner}",
							Method:     "POST",
							OnlyIf:     "shared",
							Optimistic: true},
						// Access — tier-1 user ACL: which USERS may adopt this SHARED
						// tool from their Extensions catalog. Symmetric with the API
						// credential "Access" button. Only meaningful once shared, so
						// it appears on a shared row only. (Tier 2 — which of a user's
						// OWN agents load it — is the user's per-agent Tools choice in
						// the agent editor, not an admin per-agent list here.)
						ui.ModalActionIf("Access", "shared", "", ui.ACLPicker(ui.ACLPickerConfig{
							OptionsSource: "api/user-candidates",
							RecordSource:  "api/persistent-tools?allowed_users={tool.name}&owner={owner}",
							Field:         "allowed_users",
							PostTo:        "api/persistent-tools?action=set_allowed_users&name={tool.name}&owner={owner}",
							Method:        "POST",
							Noun:          "user",
							Intro:         "Which users may adopt this shared tool from their Extensions catalog. Empty = every user. Each user then chooses which of their agents load it.",
							EmptyText:     "No other users to grant yet.",
						})),
						// Configure — re-open a template-authored tool in the generic
						// form to edit it (resolved via provenance). Shown only when
						// the tool carries a template name; the edit preserves its
						// share + adopt-ACL state.
						{Type: "button", Label: "Configure", Method: "client",
							PostTo: "configure_tool", OnlyIf: "tool.template", Compact: true},
						{Type: "button", Label: "Export", Method: "client",
							PostTo: "tools_export", Compact: true},
						{Type: "button", Label: "Delete",
							PostTo:     "api/persistent-tools?name={tool.name}&owner={owner}",
							Method:     "DELETE",
							Variant:    "danger",
							Confirm:    "Delete this active tool? The LLM will lose access immediately.",
							Optimistic: true},
					},
					EmptyText: "No active persistent tools.",
				},
				// Add a tool from a template + export all as one bundle.
				ui.Toolbar{
					Actions: []ui.ToolbarAction{
						{Label: "Add tool from template…", Method: "client", URL: "add_tool_from_template"},
						{Label: "Export all tools", Method: "client", URL: "tools_export_all"},
					},
				},
			}},
		},
		{
			Title:    "Agent-Scoped Tools",
			Subtitle: "Tools that live on a single agent's record — authored by that agent for itself, or built for it by the Builder. Scoped to the agent(s) shown — a tool on several agents is listed once, with all of them; they don't appear in the shared pool. \"Promote to Global\" moves one into its owner's user-wide pool, where it can be shared and its per-USER access set. Which of a user's own agents load a tool is their choice in the agent editor, not an admin control. A ⚠ badge marks a missing credential dependency.",
			Body: ui.Table{
				Source:       "api/persistent-tools",
				RecordsField: "bundled",
				RowKey:       "tool.name",
				Columns: []ui.Col{
					{Field: "tool.name", Flex: 1},
					{Field: "agent", Flex: 1, Label: "Agents", Mute: true},
					{Field: "owner", Flex: 0, Mute: true},
					{Field: "tool.category", Flex: 0, Label: "Category", Mute: true},
					{Field: "has_missing", Flex: 0, Label: "Deps", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "⚠ missing", Color: "danger"},
					}},
					{Field: "tool.description", Flex: 2, Mute: true},
				},
				RowActions: []ui.RowAction{
					ui.Expand("View", ui.RecordView{
						Pairs: []ui.DisplayPair{
							{Label: "Name", Field: "tool.name", Mono: true},
							{Label: "Agents", Field: "agent"},
							{Label: "Owner", Field: "owner"},
							{Label: "Category", Field: "tool.category"},
							{Label: "Description", Field: "tool.description"},
							{Label: "Mode", Field: "tool.mode"},
							{Label: "Method", Field: "tool.method", Mono: true},
							{Label: "Command / URL template", Field: "tool.command_template", Mono: true, Block: true},
							{Label: "Body template", Field: "tool.body_template", Mono: true, Block: true},
							{Label: "Script name", Field: "tool.script_name", Mono: true},
							{Label: "Script body", Field: "tool.script_body", Block: true},
							{Label: "Credential", Field: "tool.credential", Mono: true},
							{Label: "Hook capabilities", Field: "tool.hook_capabilities", Mono: true},
						},
					}),
					// Promote — move this agent-scoped tool into the owner's
					// user-wide pool, where the tier-1 user ACL ("Access" on
					// Global Tools) governs who may adopt it.
					//
					// This used to be an "Access" button opening the per-AGENT
					// pill editor, which is the wrong control at this level:
					// admin governs WHICH USERS may reach a tool, while which of
					// a user's own agents load it is that user's choice in their
					// agent editor. Global Tools' Access says exactly this in its
					// own comment; this row was the last place still contradicting
					// it. Promote is the on-ramp to that model, not a second one.
					{Type: "button", Label: "Promote to Global", Method: "client",
						PostTo: "tool_promote_global"},
				},
				EmptyText: "No agent-scoped tools.",
			},
		},
		{
			Title:    "Orphaned Tools",
			Subtitle: "Formerly agent-scoped tools whose owning agent was deleted. They were captured so they aren't silently lost. Promote one to your user-wide pool to keep it (then use \"Access\" on Global Tools to place it), or Delete to discard. A ⚠ badge marks a missing credential dependency.",
			Body: ui.Table{
				Source:       "api/persistent-tools",
				RecordsField: "orphaned",
				RowKey:       "tool.name",
				Columns: []ui.Col{
					{Field: "tool.name", Flex: 1},
					{Field: "former_agent_name", Flex: 1, Label: "Former agent", Mute: true},
					{Field: "owner", Flex: 0, Mute: true},
					{Field: "has_missing", Flex: 0, Label: "Deps", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "⚠ missing", Color: "danger"},
					}},
					{Field: "orphaned_at", Format: "reltime", Mute: true},
				},
				RowActions: []ui.RowAction{
					ui.Expand("View", ui.RecordView{
						Pairs: []ui.DisplayPair{
							{Label: "Name", Field: "tool.name", Mono: true},
							{Label: "Former agent", Field: "former_agent_name"},
							{Label: "Owner", Field: "owner"},
							{Label: "Description", Field: "tool.description"},
							{Label: "Mode", Field: "tool.mode"},
							{Label: "Command / URL template", Field: "tool.command_template", Mono: true, Block: true},
							{Label: "Script body", Field: "tool.script_body", Block: true},
							{Label: "Credential", Field: "tool.credential", Mono: true},
							{Label: "Orphaned at", Field: "orphaned_at", Format: "reltime"},
						},
					}),
					{Type: "button", Label: "Promote to global",
						PostTo:     "api/persistent-tools?action=orphan_promote&name={tool.name}&owner={owner}",
						Method:     "POST",
						Variant:    "success",
						Confirm:    "Adopt this orphaned tool into your user-wide pool?",
						Optimistic: true},
					{Type: "button", Label: "Delete",
						PostTo:     "api/persistent-tools?action=orphan_delete&name={tool.name}&owner={owner}",
						Method:     "POST",
						Variant:    "danger",
						Confirm:    "Discard this orphaned tool permanently?",
						Optimistic: true},
				},
				EmptyText: "No orphaned tools.",
			},
		},
		{
			Title:    "Categories",
			Subtitle: "Give a group of tools a named category — the heading they appear under in the tool picker and each app's tool list. Tools CLAIM a category themselves (custom tools via their own setting in Gateways/Builder; built-in tools are framework-assigned). Define the name + description here, and use Members to stamp the claim onto your custom tools as pills instead of editing each tool by hand.",
			Body: ui.Stack{
				Children: []ui.Component{
					// Table of existing groups with per-row editor + delete.
					ui.Table{
						Source: "api/tool-groups",
						RowKey: "id",
						Columns: []ui.Col{
							{Field: "name", Flex: 1},
							{Field: "description", Flex: 2, Mute: true},
						},
						RowActions: []ui.RowAction{
							// Edit the category definition (name + the description
							// the model reads). Built-in members stay framework-
							// defined; the POST handler preserves the legacy member
							// list across a name/description save.
							ui.Expand("Edit", ui.FormPanel{
								Source:  "api/tool-groups/{id}",
								PostURL: "api/tool-groups",
								Method:  "POST",
								Fields: []ui.FormField{
									{Field: "name", Type: "text", Label: "Name"},
									{Field: "description", Type: "textarea", Label: "Description", Rows: 3,
										Help: "Shown to the model as the category's purpose (it appears in the tool catalog). Write it as a decision shape: when tools under this heading should come into play."},
								},
							}),
							// Members — bulk-edit which of YOUR custom tools claim
							// this category, as removable pills. Adding a pill
							// stamps the tool's own Category field with this
							// category's name; removing clears it (self-claim
							// model unchanged, just edited in one place instead
							// of per-tool). Built-in tools are framework-assigned
							// and don't appear.
							ui.Expand("Members", ui.ChipPicker{
								Mode:          "attach",
								OptionsSource: "api/tool-groups/{id}/members",
								AttachedField: "selected",
								SaveKey:       "members",
								PostTo:        "api/tool-groups/{id}/members",
								Noun:          "tool",
								DescField:     "desc",
								MetaFields:    []string{"scope"},
								Intro:         "Your custom tools claiming this category. Add a tool to stamp its Category with this name; remove to clear the claim.",
								EmptyText:     "No custom tools yet — author one via Builder or Gateways first.",
							}),
							// Access for the whole category at once. Same pill
							// control every other scoped thing uses, over the
							// union of the tools claiming this category — so
							// granting a category to an agent grants each of its
							// tools, and a category whose tools disagree says
							// Custom rather than picking one and calling it the
							// group's answer.
							{Type: "button", Label: "Access", Method: "client",
								PostTo: "category_scope_manage"},
							// Admin-curated categories: Delete drops the row.
							{Type: "button", Label: "Delete",
								PostTo:  "api/tool-groups?id={id}",
								Method:  "DELETE",
								Confirm: "Delete this category? Tools that claim it fall back to their capability label; the tools themselves are unaffected.",
								Variant: "danger",
								HideIf:  "is_builtin"},
							// Framework-default categories: Revert drops the admin
							// shadow so the in-code default surfaces again.
							{Type: "button", Label: "Revert",
								PostTo:  "api/tool-groups?id={id}",
								Method:  "DELETE",
								Confirm: "Revert this framework-default category to its in-code defaults? Any admin edits you made are discarded.",
								OnlyIf:  "is_builtin"},
						},
						EmptyText: "No categories yet. Add one to give a group of tools a shared heading + description.",
					},
					ui.ModalButton{
						Label:    "Add category",
						Title:    "New category",
						Subtitle: "A named heading tools can claim. Define the name + the description the model reads; tools reference it by name.",
						Variant:  "primary",
						Width:    "560px",
						Body: ui.FormPanel{
							PostURL:     "api/tool-groups",
							SubmitLabel: "Create category",
							Fields: []ui.FormField{
								{Field: "name", Type: "text", Label: "Name", Placeholder: "e.g. Web Media, Acme API"},
								{Field: "description", Type: "textarea", Label: "Description", Rows: 3,
									Help: "What the model should understand this category to cover."},
							},
						},
					},
				},
			},
		},
	}
}
