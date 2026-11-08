package admin

import (
	"github.com/cmcoffee/gohort/core/ui"
)

// importExportSections closes the Integrations tab: bringing an extension in
// from a file, and taking everything out as one.
func (a *AdminApp) importExportSections() []ui.Section {
	return []ui.Section{{
		Title:    "Import and export",
		Subtitle: "Bring in an extension someone exported, or take everything out as one file.",
		Detail: "A bundle carries connectors, tools, API credentials, skills, pipelines, agents and the rest, as recipes. Import previews what it would add and what it depends on before anything lands, and everything lands as a DRAFT: connectors unapproved, tools pending, credentials inert. A name that already exists is skipped.\n\n" +
			"No secret ever travels: a credential arrives with its settings and asks for its secret here.",
		Body: ui.Toolbar{
			Actions: []ui.ToolbarAction{
				{Label: "Import a bundle…", Method: "client", URL: "artifacts_import_preview"},
				{Label: "Export everything", Method: "client", URL: "artifacts_export_all"},
			},
		},
	}}
}

// extensionsSections is the extensions part of the admin page: MCP Servers, MCP Tools (exposed to external clients), Bridges, Connectors, Other integrations, Templates.
func (a *AdminApp) extensionsSections() []ui.Section {
	return []ui.Section{
		{
			Title:    "MCP Servers",
			Subtitle: "Remote Model Context Protocol (MCP) servers this server connects to, such as Confluence.",
			Detail:   "The gohort SERVER connects to them over HTTP.\n\n\"Expose tools\" registers each server's tools as <name>.<tool> for agents. \"Expose as a reference source\" makes it selectable in writer and research source pickers.\n\nBearer tokens are stored encrypted; secure_api mode mints and refreshes an OAuth2 bearer per request from an API Credential. Test verifies reachability and auth before you enable.",
			Body: ui.Stack{
				Children: []ui.Component{
					ui.Table{
						Source: "api/mcp-servers",
						RowKey: "name",
						Columns: []ui.Col{
							{Field: "name", Flex: 1},
							{Field: "url", Mute: true, Flex: 1},
							{Field: "auth_mode", Label: "Auth", Mute: true},
							{
								Field: "expose_tools", Type: "badge", Label: "Tools",
								Badges: []ui.BadgeMapping{
									{Value: true, Label: "Exposed", Color: "success"},
									{Value: false, Label: "Off", Color: "mute"},
								},
							},
							{
								Field: "expose_reference", Type: "badge", Label: "Reference",
								Badges: []ui.BadgeMapping{
									{Value: true, Label: "Source", Color: "success"},
									{Value: false, Label: "Off", Color: "mute"},
								},
							},
							{
								Field: "enabled", Type: "badge",
								Badges: []ui.BadgeMapping{
									{Value: true, Label: "Enabled", Color: "success"},
									{Value: false, Label: "Disabled", Color: "danger"},
								},
							},
							{
								Field: "connected", Type: "badge", Label: "Conn",
								Badges: []ui.BadgeMapping{
									{Value: true, Label: "Connected", Color: "success"},
									{Value: false, Label: "-", Color: "mute"},
								},
							},
						},
						RowActions: []ui.RowAction{
							ui.Expand("Edit", ui.FormPanel{
								Assist:      true,
								Source:      "api/mcp-servers?name={name}",
								PostURL:     "api/mcp-servers",
								TestURL:     "api/mcp-servers/test",
								TestLabel:   "Test connection",
								SubmitLabel: "Save changes",
								Fields:      mcpServerFormFields(),
							}),
							// Enable/Disable — neutral (label-only). State color
							// lives on the "Enabled/Disabled" badge, not the button.
							{Type: "button", Label: "Enable",
								PostTo: "api/mcp-servers?action=enable&name={name}",
								Method: "POST", HideIf: "enabled"},
							{Type: "button", Label: "Disable",
								PostTo: "api/mcp-servers?action=disable&name={name}",
								Method: "POST",
								OnlyIf: "enabled"},
							// Connect (oauth servers only): a GET button that opens
							// the start endpoint in a new tab; it 302-redirects to the
							// hosted login. Authorizes the CURRENT user.
							{Type: "button", Label: "Connect",
								Method:         "GET",
								PostTo:         "api/mcp-servers/oauth/start?name={name}",
								RedirectTarget: "_blank",
								OnlyIf:         "is_oauth",
								Variant:        "primary"},
							{Type: "button", Label: "Delete",
								PostTo:  "api/mcp-servers?name={name}",
								Method:  "DELETE",
								Confirm: "Delete this MCP server? Its encrypted token goes with it. Already-registered tools stay until the next restart but stop working.",
								Variant: "danger"},
						},
						EmptyText: "No MCP servers configured. Add one with the button below.",
					},
					ui.ModalButton{
						Label:    "Add MCP server",
						Title:    "Add MCP server",
						Subtitle: "Point at a remote MCP server's Streamable-HTTP endpoint. Test before enabling.",
						Variant:  "primary",
						Width:    "640px",
						Body: ui.FormPanel{
							Assist:      true,
							PostURL:     "api/mcp-servers",
							TestURL:     "api/mcp-servers/test",
							TestLabel:   "Test connection",
							SubmitLabel: "Add server",
							Fields:      mcpServerFormFields(),
						},
					},
				},
			},
		},
		{
			Title:    "MCP Tools (exposed to external clients)",
			Subtitle: "App-contributed tools on gohort's own inbound MCP endpoint (/mcp/).",
			Detail:   "This is what an external MCP client, say Claude Desktop authenticated with a bridge key, can call to drive your apps. Each tool is OFF by default: expose only the ones you want reachable from outside. The built-in ask_agent and recent_results tools are always available.",
			Body: ui.Stack{
				Children: []ui.Component{
					ui.Table{
						Source: "api/mcp-tools",
						RowKey: "name",
						Columns: []ui.Col{
							{Field: "name", Flex: 1},
							{Field: "description", Mute: true, Flex: 2},
						},
						RowActions: []ui.RowAction{
							// One On/Off switch per tool: checked = exposed. Flips
							// the exposure via a {exposed:bool} POST.
							{Type: "toggle", Field: "exposed", Label: "Exposed",
								PostTo: "api/mcp-tools?name={name}", Method: "POST"},
						},
						EmptyText: "No app MCP tools registered. Apps register them via core.RegisterMCPTool (see apps/guides).",
					},
				},
			},
		},
		{
			Title:    "Bridges",
			Subtitle: "Credential-polling bridges agents have created, across ALL users.",
			Detail:   "A bridge polls an API on a schedule via a registered credential and wakes an agent when the response changes.\n\nPause is the kill switch: a paused bridge stops polling and agents cannot resume it themselves; only this table and the owner's console can. Which SERVICES a bridge may call is governed separately, per credential, under APIs, where \"Require confirm before each call\" escalates every call to an in-chat approval.",
			Body: ui.Table{
				Source: "/orchestrate/api/console/bridges",
				RowKey: "name",
				Columns: []ui.Col{
					{Field: "name", Flex: 1},
					{Field: "owner", Mute: true},
					{Field: "credential", Mute: true},
					{Field: "detail", Mute: true, Flex: 2},
					{
						Field: "state", Type: "badge",
						Badges: []ui.BadgeMapping{
							{Value: "active", Label: "Active", Color: "success"},
							{Value: "paused", Label: "Paused", Color: "warning"},
						},
					},
					{Field: "last_fired", Label: "Last fired", Mute: true},
				},
				RowActions: []ui.RowAction{
					{Type: "button", Label: "Pause",
						PostTo: "/orchestrate/api/console/bridges/pause?owner={owner}&name={name}",
						Method: "POST", HideIf: "_paused"},
					{Type: "button", Label: "Resume",
						PostTo: "/orchestrate/api/console/bridges/resume?owner={owner}&name={name}",
						Method: "POST", OnlyIf: "_paused", Variant: "primary"},
					{Type: "button", Label: "Delete",
						PostTo:  "/orchestrate/api/console/bridges/delete?owner={owner}&name={name}",
						Method:  "POST",
						Confirm: "Delete this bridge? Its schedule is cancelled; the agent and credential it used are untouched.",
						Variant: "danger"},
				},
				EmptyText: "No bridges yet. Agents create these (via the bridge tool) to watch an API and wake an agent on change; they appear here for enable/disable the moment one exists.",
			},
		},
		{
			Title:    "Connectors",
			Subtitle: "The REST services gohort reaches for a job: search, pictures, speech, embeddings, page rendering, mail, conversions, message sends. Each is reached through the pages that reference it; edit, test or remove it here.",
			Detail: "A connector is how gohort calls one outside service: its address and key, the requests it makes, how each reply is read, and the job it does for gohort (its contract). It comes from a template (Admin > Services > Templates), from an imported bundle, or from the assistant.\n\n" +
				"It is reached through the pages that reference it: choosing it on the page that uses it (Web Search, Media Generation, Research Sources, ...) and saving. Its row says which pages reference it, or that none does; one no page references makes no calls. One referenced cannot be deleted: take it off those pages first. A change made by someone who is not an administrator holds it, so no call reaches it, until an administrator saves a page with it chosen.\n\n" +
				"Settings on its row are its values and its key, the same form the page that chooses it shows. Edit opens how it runs: its spec beside an assistant, with the job it must do; a connector a setup form made (ComfyUI, A1111) opens that form. When its spec does not validate, Edit is Fix. Endpoints lists each request it makes, what it is sent and what is read back. Test makes the smallest real call and runs its contract's check.\n\n" +
				"What gohort does in Go (the LLM providers, SMTP, the browser, DuckDuckGo) is set up on its own page, peers under Admin > Integrations > Peers, and the integrations of other kinds are listed below.",
			Body: ui.Stack{
				Children: []ui.Component{
					ui.Table{
						Source:  "api/connectors",
						RowKey:  "name",
						GroupBy: "group",
						// Many connectors in many groups: each band folds to
						// its heading and summary, and starts folded: the heading says
						// how many it holds and how many have an error.
						GroupFold:         true,
						GroupOpenField:    "group_open",
						GroupSummaryField: "group_summary",
						// The summary says what the connector reaches; it and
						// what is wrong take the second line at full width.
						Columns: connectorColumns(),
						RowActions: append(append([]ui.RowAction{
							ui.ExpandIf("Endpoints", "endpoints", "", ui.Table{
								Source: "api/connectors/endpoints?name={name}",
								RowKey: "name",
								Columns: []ui.Col{
									{Field: "address", Label: "Endpoint", Flex: 3},
									{Field: "used_by", Label: "Used by", Mute: true, Flex: 2},
									{Field: "sends", Label: "", Mute: true, Flex: 4, Line: 2},
									{Field: "reads", Label: "", Mute: true, Flex: 4, Line: 2},
								},
								EmptyText: "This service has no endpoints.",
							}),
							connectorSettingsAction(),
							ui.ModalActionIf("Access", "has_access", "", ui.ACLPicker(ui.ACLPickerConfig{
								OptionsSource: "api/user-candidates",
								RecordSource:  "api/connectors/access?name={name}",
								Field:         "allowed_users",
								PostTo:        "api/connectors/access?name={name}",
								Method:        "POST",
								Noun:          "user",
								Intro: "Which users' agents may use this connection through their tools: an image or music tool, a Research Source. Empty = all users. " +
									"Each user can then switch it off for any of their agents on the agent editor, under External credentials. " +
									"A page that has it serve the whole instance (an LLM tier, Web Search, Embeddings) serves every user whatever this says.",
								EmptyText: "No other users to grant yet.",
							})),
						}, connectorEditRowActions()...),
							connectorTestAction(),
							ui.RowAction{Type: "button", Label: "Check health", Method: "POST",
								PostTo: "api/connectors/health?name={name}", OnlyIf: "has_health"},
							ui.RowAction{Type: "button", Label: "Export", Method: "client",
								PostTo: "connectors_export"},
							ui.RowAction{Type: "button", Label: "Delete",
								PostTo:  "api/connectors?name={name}",
								Method:  "DELETE",
								OnlyIf:  "deletable",
								Confirm: "Delete this connector?",
								Variant: "danger"},
						),
						EmptyText: "No connectors yet. Add one from Admin > Services > Templates, or import a bundle.",
					},
					// Import: "Import bundle…" opens a file picker, then a
					// PREVIEW modal (dry-run via /api/artifacts/preview): what
					// the bundle carries, what would import vs skip, and any
					// unmet references; nothing lands until the admin confirms.
					// Everything reconstitutes as a draft, a name that already
					// exists is skipped, and no secret ever travels.
					//
					// Export: per-row Export grabs one connector; this grabs
					// every connector, of every kind, as one secret-free
					// gohort.bundle/v1.
					ui.Toolbar{
						Actions: []ui.ToolbarAction{
							{Label: "Export all connectors", Method: "client", URL: "connectors_export_all"},
						},
					},
				},
			},
		},
		{
			Title:    "Other integrations",
			Subtitle: "MCP servers, desktop programs, watches, messaging relays, bots and peer relays: each moves to a page of its own later, and until then is approved here.",
			Detail: "These are not connectors (a connector is a REST service gohort calls for a job): an MCP server's tools, a program on someone's machine, a poll that wakes an agent, a messaging relay or bot, an adapted System Hook, a peer's relay. Each keeps working as it did and moves to a page of its own in a later step.\n\n" +
				"Approve to MATERIALIZE one: its tools register for agents, and a remote_mcp connector becomes an enabled MCP server, which also appears under MCP Servers above. The assistant drafts them and never handles a secret: auth is a referenced API credential or per-user OAuth. Nothing runs until you approve, and Delete tears it down.",
			Body: ui.Table{
				Source:            "api/connectors/other",
				RowKey:            "name",
				GroupBy:           "group",
				GroupFold:         true,
				GroupOpenField:    "group_open",
				GroupSummaryField: "group_summary",
				Columns:           connectorColumns(),
				RowActions: []ui.RowAction{
					{Type: "button", Label: "Approve",
						PostTo: "api/connectors?action=approve&name={name}",
						Method: "POST", OnlyIf: "can_approve", Variant: "primary",
						Invalidate: []string{"api/connectors/other"}},
					{Type: "button", Label: "Unapprove",
						PostTo: "api/connectors?action=unapprove&name={name}",
						Method: "POST", OnlyIf: "can_unapprove",
						Invalidate: []string{"api/connectors/other"}},
					{Type: "button", Label: "Webhook…", Method: "client",
						PostTo: "connector_webhook", OnlyIf: "webhook"},
					{Type: "button", Label: "Edit spec", Method: "client",
						PostTo: "connector_edit_spec"},
					{Type: "button", Label: "Export", Method: "client",
						PostTo: "connectors_export"},
					{Type: "button", Label: "Delete",
						PostTo:     "api/connectors?name={name}",
						Method:     "DELETE",
						OnlyIf:     "deletable",
						Confirm:    "Delete this and tear down what it brought up (for remote_mcp, remove its MCP server)?",
						Variant:    "danger",
						Invalidate: []string{"api/connectors/other"}},
				},
				EmptyText: "None. The assistant drafts these with the connector tool; approve them here to make their tools available to agents.",
			},
		},
		a.templatesSection(),
	}
}

// mcpServerFormFields is the shared field list for the MCP Servers add
// (modal) and edit (row Expand) forms. Auth-specific inputs collapse via
// ShowWhen so a bearer token field doesn't surface in secure_api mode.
// The token is a password that stays blank on edit — leaving it blank
// keeps the stored token.
func mcpServerFormFields() []ui.FormField {
	return []ui.FormField{
		{Field: "ident", Type: "header", Label: "Server"},
		{Field: "name", Label: "Name", Placeholder: "confluence", Help: "[a-z0-9_-]. Namespaces its tools as <name>.<tool>. Re-using a name updates that server."},
		{Field: "url", Label: "Endpoint URL (https)", Placeholder: "https://mcp.example.com/mcp", Help: "The remote MCP server's Streamable-HTTP endpoint."},

		{Field: "auth_hdr", Type: "header", Label: "Authentication"},
		{Field: "auth_mode", Label: "Auth mode", Type: "select", Options: []ui.SelectOption{
			{Value: "", Label: "None (public)"},
			{Value: "bearer", Label: "Bearer token (static)"},
			{Value: "secure_api", Label: "SecureAPI OAuth2 credential"},
			{Value: "oauth", Label: "OAuth 2.1 hosted login (per-user)"},
		}},
		{Field: "token", Label: "Bearer token", Type: "password", ShowWhen: "auth_mode:bearer", Help: "Stored encrypted. Leave blank when editing to keep the existing token."},
		{Field: "secure_cred", Label: "SecureAPI credential name", Placeholder: "confluence_oauth", ShowWhen: "auth_mode:secure_api", Help: "An OAuth2 credential configured under API Credentials. Its bearer token is minted/refreshed per request."},
		{Field: "oauth_note", Type: "header", Label: "Hosted login: Save first, then click Connect on the server's row to authorize. Each user connects their own account, from here or from Extensions → Connections. The callback host must be https or localhost. With a pre-registered client, register both redirect URIs listed under Client ID below.", ShowWhen: "auth_mode:oauth"},
		{Field: "oauth_client_id", Label: "Client ID (only if no auto-registration)", ShowWhen: "auth_mode:oauth", Help: "Leave it BLANK for the normal flow, where gohort auto-registers a client.",
			Detail: "That is Dynamic Client Registration. Fill this in ONLY when the provider does not support auto-registration: pre-register an OAuth app at the provider and paste the issued client_id here.\n\nRegister BOTH redirect URIs on it. <this host>/admin/api/mcp-servers/oauth/callback serves the Connect button on this page, and <this host>/account/mcp/callback serves every user connecting their own account from Extensions or a chat prompt.\n\nThey are different paths because the admin area is admin-only, so a non-admin cannot complete a consent that lands there. A provider that has only the first will reject the second with \"the app's callback URL is invalid\"."},
		{Field: "oauth_client_secret", Label: "Client secret (optional)", Type: "password", ShowWhen: "auth_mode:oauth;oauth_client_id", Help: "Only for a manual Client ID that the provider made confidential.",
			Detail: "Stored encrypted. Leave it blank to keep the existing one, and blank for public PKCE clients."},
		{Field: "oauth_authorize_url", Label: "Authorize URL (only if no discovery)", Placeholder: "https://provider/oauth/authorize", ShowWhen: "auth_mode:oauth", Help: "Leave blank to auto-discover. Set only for a provider that doesn't publish .well-known OAuth metadata."},
		{Field: "oauth_token_url", Label: "Token URL (only if no discovery)", Placeholder: "https://provider/oauth/token", ShowWhen: "auth_mode:oauth", Help: "Leave blank to auto-discover. Pair with Authorize URL."},
		{Field: "oauth_scopes", Label: "Scopes (optional)", Placeholder: "files.read folders.read", ShowWhen: "auth_mode:oauth", Help: "Space-separated OAuth scopes. Leave blank to use what discovery advertises."},
		{Field: "oauth_audience", Label: "Audience (Auth0/Okta providers)", Placeholder: "api.atlassian.com", ShowWhen: "auth_mode:oauth", Help: "For Auth0 and Okta-style servers. Leave it blank for normal MCP servers.",
			Detail: "Atlassian, for example, needs api.atlassian.com. When set it is sent instead of the RFC 8707 resource indicator, which those providers ignore."},

		{Field: "expose_hdr", Type: "header", Label: "Exposure"},
		{Field: "expose_tools", Label: "Expose tools to agents", Type: "toggle", Help: "Register the server's tools as <name>.<tool> in the agent catalog."},
		{Field: "one_tool", Label: "Offer as one tool", Type: "toggle", ShowWhen: "expose_tools",
			Help:   "Agents see one tool named for the server, with an action per tool, instead of each tool on its own.",
			Detail: "For a server with many tools, which otherwise fill an agent's catalog. It takes effect on the next turn, and an agent whose tool list names the server's tools either way keeps them."},
		{Field: "expose_reference", Label: "Expose as a reference source", Type: "toggle", Help: "Make the server selectable in writer/research source pickers (uses the Search tool below)."},
		{Field: "search_tool", Label: "Search tool name", Placeholder: "search", ShowWhen: "expose_reference", Help: "MCP tool called for reference lookups. Defaults to 'search'."},

		{Field: "enabled", Label: "Enabled", Type: "toggle", Help: "Connect on startup and on save. Disable to suspend without deleting."},
	}
}

// templatesSection is one list of every template: recipes that integrate a
// service without Go (core/recipes: built-in, imported or saved here).
//
// The "What goes in" checklist lists every artifact in the store. It is read
// when the dialog opens (api/templates/pieces), not here: building it here
// read every artifact of every kind for every user on every administrator
// page, which was most of the second each page took.
func (a *AdminApp) templatesSection() ui.Section {
	return ui.Section{
		Title:    "Templates",
		Subtitle: "Ready-made ways to add a service to gohort, by what it does. Add… on one asks a few questions and adds the connector (and anything else it needs) as a draft for review.",
		Detail: "A template is a recipe, not something installed: adding from it makes the connector, and the template itself stays here for the next time. It asks what it needs (a site address, your email, an API token), fills the answers in, and adds its pieces through the same importer as a bundle file: credentials disabled until you test them, tools pending approval. " +
			"A secret you give goes straight into its secret store and never into a tool or a file.\n\n" +
			"The pages that use a connector offer the same templates at the end of their list, under \"+ Add another…\": what is added there joins that list. Import a template file someone shared, or save one from things you have built, turning the values that differ between deployments (an address, an account) into questions. Export any template to share it.\n\n" +
			"Builder can do both halves for you: ask it to integrate a service, then to make that a template. It only saves a template when an administrator asks, and stops to show you the title, pieces and questions before it does.",
		Body: ui.Stack{Children: []ui.Component{
			ui.Toolbar{Actions: []ui.ToolbarAction{
				{Label: "Import a template file…", Method: "client", URL: "template_import"},
			}},
			templateSaveButton(),
			ui.Table{
				Source:    "api/templates",
				RowKey:    "id",
				GroupBy:   "category",
				GroupFold: true,
				Columns: []ui.Col{
					{Field: "title", Flex: 2},
					{Field: "source", Label: "", Mute: true},
					{Field: "description", Mute: true, Flex: 4, Line: 2},
					{Field: "contains", Label: "", Mute: true, Flex: 4, Line: 2},
				},
				RowActions: []ui.RowAction{
					{Type: "button", Label: "Add…", Method: "client", PostTo: "template_install", Variant: "primary", OnlyIf: "_recipe"},
					{Type: "button", Label: "Export", Method: "client", PostTo: "template_export", Compact: true, OnlyIf: "_recipe"},
					{Type: "button", Label: "Delete", Variant: "danger", Compact: true, OnlyIf: "_imported",
						PostTo: "api/templates/delete?id={id}", Confirm: "Delete this template? What was already added from it stays."},
				},
				EmptyText: "No templates.",
			},
		}},
	}
}

// templateSaveButton is making a new template from what has been built: pick
// what goes in, then turn the values that differ between deployments into
// questions.
func templateSaveButton() ui.ModalButton {
	yesNo := []ui.SelectOption{{Value: "", Label: "No"}, {Value: "yes", Label: "Yes"}}
	return ui.ModalButton{
		Label: "Make a new one from what you have built…", Title: "Save as a template", Align: "left",
		Subtitle: "Pick what goes in, then turn the values that differ between deployments into questions.",
		Width:    "720px",
		Body: ui.FormPanel{
			Assist:  true,
			PostURL: "api/templates/save", SubmitLabel: "Save template",
			Invalidate: []string{"api/templates"},
			Fields: []ui.FormField{
				{Field: "title", Label: "Title", Type: "text", Required: true, Placeholder: "Acme wiki"},
				{Field: "id", Label: "Id", Type: "text", Placeholder: "acme-wiki",
					Help: "Lowercase letters, digits and dashes. Left empty, it is made from the title."},
				{Field: "description", Label: "What it sets up", Type: "text"},
				{Field: "category", Label: "Category", Type: "text", Placeholder: "Project tracking"},
				{Field: "setup_notes", Label: "Setup notes", Type: "textarea", Rows: 3,
					Help: "Shown when someone adds it: where to get a token, what to enable afterwards."},
				{Field: "pieces", Label: "What goes in", Type: "checklist", OptionsSource: "api/templates/pieces", Required: true,
					Help: "What each needs comes along: a tool brings the credential it runs on, including a person's own key, which is why those are not listed on their own. No secret is ever included."},
				{Field: "questions", Label: "Questions", Type: "rows", AddLabel: "Add a question",
					Help: "Each value is replaced by the answer wherever it appears. A secret question asks for a credential's secret instead, which never travels.",
					Columns: []ui.FormField{
						{Field: "name", Label: "Name", Type: "text", Placeholder: "site", Width: 2},
						{Field: "label", Label: "Asked as", Type: "text", Placeholder: "Wiki address", Width: 3},
						{Field: "required", Label: "Required", Type: "select", Options: yesNo, Width: 1},
						{Field: "secret", Label: "Secret", Type: "select", Options: yesNo, Width: 1},
						{Field: "value", Label: "Value to replace", Type: "text", OwnLine: true, HideWhen: "secret:yes",
							Placeholder: "https://wiki.acme.example"},
						{Field: "kind", Label: "Kind", Type: "select", OwnLine: true, HideWhen: "secret:yes", Options: []ui.SelectOption{
							{Value: "", Label: "Text"}, {Value: "url", Label: "An https address"},
							{Value: "http_url", Label: "An http or https address (a server on your network)"},
							{Value: "long", Label: "Long text (asked in a multi-line box)"},
						}},
						{Field: "credential", Label: "The credential whose secret it asks for", Type: "text", OwnLine: true, ShowWhen: "secret:yes",
							Placeholder: "wiki"},
						{Field: "help", Label: "Help", Type: "text", OwnLine: true},
					}},
			},
		},
	}
}

// connectorColumns are a connector row's columns, in both lists.
// connectorSettingsAction is a connector row's Settings: its values and its
// key, the form the page that chooses it shows. Every table listing
// connectors uses this one, so a connector's settings are one form.
func connectorSettingsAction() ui.RowAction {
	return ui.ModalActionIf("Settings", "has_settings", "", ui.FormPanel{
		Source:      "api/connectors/settings?name={name}",
		PostURL:     "api/connectors/settings?name={name}",
		Method:      "POST",
		SubmitLabel: "Save",
		Invalidate:  connectorListSources,
		Fields:      connectionSettingsFields(),
	})
}

// connectorEditRowActions are a connector row's one editor, by the flags
// connectorEditors sets: Edit setup for its setup form (ComfyUI, A1111), Edit
// for its spec, or Fix. Either editor sits in the row's Advanced menu: each
// changes how the connector calls its service, and beside Settings (its key
// and model) a plain Edit read as the place to change them. Every row keeps
// Settings as its obvious button. Fix stays in view: it shows only when the
// connector is broken, which is when it is wanted.
func connectorEditRowActions() []ui.RowAction {
	spec := connectorEditAction("Edit", "edit_spec", "What should change in how this service is called, and why?")
	spec.Group = "Advanced"
	return []ui.RowAction{
		// Shown only when the template it was made from now writes something
		// different: a job it learned since (Gemini's editing), a fix.
		{Type: "button", Label: "Update from template", Method: "POST", PostTo: "api/connectors/update-template?name={name}",
			OnlyIf: "template_update", Variant: "primary", Invalidate: connectorListSources,
			Confirm: "Update this connector to what its template writes now? Its settings and key are kept. A change made to its spec by hand (Advanced, ✨ Edit) is replaced."},
		{Type: "button", Label: "Edit setup", Method: "client", PostTo: "configure_backend", OnlyIf: "edit_form", Group: "Advanced"},
		spec,
		connectorEditAction("Fix", "needs_fix", "Why is this service failing, and what would fix it?"),
	}
}

// connectorTestAction is a connector row's Test: the smallest real call, and
// its contract's check.
func connectorTestAction() ui.RowAction {
	return ui.RowAction{Type: "button", Label: "Test", Method: "POST",
		PostTo: "api/connectors/test?name={name}", OnlyIf: "testable", ConfirmField: "test_confirm"}
}

// connectorListSources are the tables that list connectors. A change made
// from either one's row reloads both, since a page can hold both.
var connectorListSources = []string{"api/connectors", mediaBackendsSource, costBySourceSource}

func connectorColumns() []ui.Col {
	return []ui.Col{
		{Field: "title", Label: "Name", Flex: 2},
		{Field: "kind", Label: "Type", Mute: true},
		{Field: "owner", Label: "Drafted by", Mute: true},
		{
			Field: "state", Type: "badge",
			Badges: []ui.BadgeMapping{
				{Value: "approved", Label: "Approved", Color: "success"},
				{Value: "pending", Label: "Pending", Color: "warning"},
				{Value: "referenced", Label: "Referenced", Color: "success"},
				{Value: "unreferenced", Label: "Not referenced", Color: "mute"},
				{Value: "changed", Label: "Changed", Color: "warning"},
			},
		},
		{Field: "summary", Label: "", Mute: true, Flex: 4, Line: 2},
		{Field: "key", Label: "", Mute: true, Flex: 3, Line: 2},
		{Field: "last_error", Label: "", Mute: true, Flex: 3, Line: 2},
		{Field: "check", Label: "", Flex: 4, Line: 2},
		{Field: "status", Label: "", Mute: true, Flex: 4, Line: 2},
	}
}
