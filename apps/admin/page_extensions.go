package admin

import (
	"github.com/cmcoffee/gohort/core/ui"
)

// extensionsSections is the extensions part of the admin page: MCP Servers, MCP Tools (exposed to external clients), Bridges, Extensions, Templates, Connectors, Catalog.
func (a *AdminApp) extensionsSections() []ui.Section {
	return []ui.Section{
		{
			Title:    "MCP Servers",
			Subtitle: "Remote Model Context Protocol servers (e.g. Confluence) the gohort SERVER connects to over HTTP. \"Expose tools\" registers each server's tools as <name>.<tool> for agents; \"Expose as a reference source\" makes it selectable in writer/research source pickers. Bearer tokens are stored encrypted; secure_api mode mints + refreshes an OAuth2 bearer per request from an API Credential. Test verifies reachability + auth before you enable.",
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
									{Value: false, Label: "—", Color: "mute"},
								},
							},
						},
						RowActions: []ui.RowAction{
							ui.Expand("Edit", ui.FormPanel{
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
			Subtitle: "App-contributed tools on gohort's OWN inbound MCP endpoint (/mcp/) — what an external MCP client (e.g. Claude Desktop, authenticated with a bridge key) can call to drive your apps. Each tool is OFF by default; expose only the ones you want reachable from outside. The built-in ask_agent / recent_results tools are always available.",
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
			Subtitle: "Credential-polling bridges agents have created (poll an API on a schedule via a registered credential, wake an agent when the response changes) — across ALL users. Pause is the kill switch: a paused bridge stops polling and agents cannot resume it themselves; only this table and the owner's console can. Which SERVICES a bridge may call is governed separately, per credential, under APIs (\"Require confirm before each call\" escalates every call to an in-chat approval).",
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
			Title:    "Extensions",
			Subtitle: "Every capability you can add from a template — connectors (service bridges) and tools (model-callable actions) — in one catalog. Pick one to author it from its fields; it lands in its own section for approval (a connector under Connectors, a tool under Persistent Tools). Templates ease authoring — they grant no new power: the same credential binding and approval still apply.",
			Body: ui.Table{
				Source: "api/extensions",
				RowKey: "name",
				Columns: []ui.Col{
					{Field: "label", Flex: 1},
					{
						Field: "target", Label: "Kind", Type: "badge",
						Badges: []ui.BadgeMapping{
							{Value: "connector", Label: "Connector", Color: "info"},
							{Value: "tool", Label: "Tool", Color: "success"},
						},
					},
					{Field: "category", Mute: true},
					{Field: "description", Mute: true, Flex: 2},
				},
				RowActions: []ui.RowAction{
					{Type: "button", Label: "Add", Method: "client",
						PostTo: "add_extension", Variant: "primary"},
				},
				EmptyText: "No extension templates registered.",
			},
		},
		{
			Title:    "Templates",
			Subtitle: "Ready-made blueprints for connectors and tools — declare “what options are needed” and the framework builds the rest. “Add” opens a form to fill in your specifics; the result lands as a draft connector or a pending tool for review. New backends/tools of a known shape are just declarations (no code).",
			Body: ui.Table{
				Source: "api/all-templates",
				RowKey: "id",
				Columns: []ui.Col{
					{Field: "label", Flex: 1},
					{Field: "target", Label: "Kind", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: "connector", Label: "Connector", Color: "mute"},
						{Value: "tool", Label: "Tool", Color: "mute"},
					}},
					{Field: "category", Mute: true},
					{Field: "description", Mute: true, Flex: 2},
				},
				RowActions: []ui.RowAction{
					{Type: "button", Label: "Add", Method: "client", PostTo: "template_add", Variant: "primary"},
				},
				EmptyText: "No templates registered.",
			},
		},
		{
			Title:    "Connectors",
			Subtitle: "Bridge types drafted by the assistant (via the connector tool) and awaiting your approval — e.g. a calendar or CRM exposed through its MCP server. Approve to MATERIALIZE the capability: its tools register for agents (a remote_mcp connector becomes an enabled MCP server, which also appears under MCP Servers above). The assistant never handles a secret — auth is a referenced API credential or per-user OAuth. Nothing runs until you approve; Delete tears the capability down.",
			Body: ui.Stack{
				Children: []ui.Component{
					ui.Table{
						Source: "api/connectors",
						RowKey: "name",
						Columns: []ui.Col{
							{Field: "name", Flex: 1},
							{Field: "kind", Label: "Type", Mute: true},
							{Field: "summary", Mute: true, Flex: 2},
							{Field: "owner", Label: "Drafted by", Mute: true},
							{
								Field: "approved", Type: "badge",
								Badges: []ui.BadgeMapping{
									{Value: true, Label: "Approved", Color: "success"},
									{Value: false, Label: "Pending", Color: "warning"},
								},
							},
						},
						RowActions: []ui.RowAction{
							{Type: "button", Label: "Approve",
								PostTo: "api/connectors?action=approve&name={name}",
								Method: "POST", HideIf: "approved", Variant: "primary"},
							{Type: "button", Label: "Unapprove",
								PostTo: "api/connectors?action=unapprove&name={name}",
								Method: "POST", OnlyIf: "approved"},
							{Type: "button", Label: "Configure", Method: "client",
								PostTo: "configure_backend", OnlyIf: "configurable"},
							{Type: "button", Label: "Edit spec", Method: "client",
								PostTo: "connector_edit_spec", Compact: true},
							{Type: "button", Label: "Export", Method: "client",
								PostTo: "connectors_export", Compact: true},
							{Type: "button", Label: "Delete",
								PostTo:  "api/connectors?name={name}",
								Method:  "DELETE",
								Confirm: "Delete this connector and tear down its capability (for remote_mcp, remove its MCP server)?",
								Variant: "danger"},
						},
						EmptyText: "No connectors yet. The assistant drafts these with the connector tool; approve them here to make their tools available to agents.",
					},
					// Import — "Import bundle…" opens a file picker, then a
					// PREVIEW modal (dry-run via /api/artifacts/preview): what
					// the bundle carries, what would import vs skip, and any
					// unmet references — nothing lands until the admin confirms.
					// The unified importer accepts ANY artifact bundle, a legacy
					// connector pack, or a single artifact; everything
					// reconstitutes as a DRAFT (connectors unapproved, tools
					// pending, credentials inert, skills disabled; collections
					// land user-scoped and re-embed their corpus in the
					// background). A name that already exists is skipped, and no
					// secret ever travels.
					//
					// Export — per-row Export (in the table above) grabs one
					// connector; these buttons grab whole sets as one secret-free
					// gohort.bundle/v1. "Export everything" spans every artifact
					// type (connectors + tools + future types).
					ui.Toolbar{
						Actions: []ui.ToolbarAction{
							{Label: "Import bundle…", Method: "client", URL: "artifacts_import_preview"},
							{Label: "Export all connectors", Method: "client", URL: "connectors_export_all"},
							{Label: "Export everything", Method: "client", URL: "artifacts_export_all"},
						},
					},
				},
			},
		},
		{
			Title:    "Catalog",
			Subtitle: "Ready-made connectors, tools, API credentials, and agents you can install with one click. Installing runs the SAME import as a bundle file: everything lands as a DRAFT for review — connectors unapproved, tools pending, credentials inert — so nothing goes live until you approve it in the sections above.",
			Body: ui.Table{
				Source: "api/catalog",
				RowKey: "id",
				Columns: []ui.Col{
					{Field: "title", Flex: 1},
					{Field: "category", Mute: true},
					{Field: "summary", Label: "Installs", Mute: true, Flex: 1},
					{Field: "description", Mute: true, Flex: 2},
				},
				RowActions: []ui.RowAction{
					{Type: "button", Label: "Install", Method: "POST", Variant: "primary",
						PostTo: "api/catalog?action=install&id={id}",
						// An install runs the same importer a file import
						// does, and lands the same drafts: connectors
						// unapproved, tools pending, credentials inert. Each
						// is a section of its own, and reviewing them is the
						// next thing you do — the file-import path has said
						// so since it existed, and this one did not.
						Invalidate: []string{"api/connectors", "api/persistent-tools", "api/secure-api", "api/skills"},
						Confirm:    "Install this catalog entry? Its artifacts are added as drafts (pending review) in the sections above — nothing goes live until you approve it."},
				},
				EmptyText: "The catalog is empty.",
			},
		},
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
		{Field: "oauth_client_id", Label: "Client ID (only if no auto-registration)", ShowWhen: "auth_mode:oauth", Help: "Leave BLANK for the normal flow: gohort auto-registers a client (Dynamic Client Registration). Fill this ONLY when the provider doesn't support auto-registration — pre-register an OAuth app at the provider and paste the issued client_id here. Register BOTH redirect URIs on it: <this host>/admin/api/mcp-servers/oauth/callback for the Connect button on this page, and <this host>/account/mcp/callback for every user connecting their own account from Extensions or a chat prompt. They are different paths because the admin area is admin-only — a non-admin cannot complete a consent that lands there — and a provider that has only the first will reject the second with \"the app's callback URL is invalid\"."},
		{Field: "oauth_client_secret", Label: "Client secret (optional)", Type: "password", ShowWhen: "auth_mode:oauth", Help: "Only for a manual Client ID that the provider made confidential. Stored encrypted; leave blank to keep the existing one (and blank for public PKCE clients)."},
		{Field: "oauth_authorize_url", Label: "Authorize URL (only if no discovery)", Placeholder: "https://provider/oauth/authorize", ShowWhen: "auth_mode:oauth", Help: "Leave blank to auto-discover. Set only for a provider that doesn't publish .well-known OAuth metadata."},
		{Field: "oauth_token_url", Label: "Token URL (only if no discovery)", Placeholder: "https://provider/oauth/token", ShowWhen: "auth_mode:oauth", Help: "Leave blank to auto-discover. Pair with Authorize URL."},
		{Field: "oauth_scopes", Label: "Scopes (optional)", Placeholder: "files.read folders.read", ShowWhen: "auth_mode:oauth", Help: "Space-separated OAuth scopes. Leave blank to use what discovery advertises."},
		{Field: "oauth_audience", Label: "Audience (Auth0/Okta providers)", Placeholder: "api.atlassian.com", ShowWhen: "auth_mode:oauth", Help: "For Auth0/Okta-style servers (e.g. Atlassian needs api.atlassian.com). When set it is sent instead of the RFC 8707 resource indicator, which those providers ignore. Leave blank for normal MCP servers."},

		{Field: "expose_hdr", Type: "header", Label: "Exposure"},
		{Field: "expose_tools", Label: "Expose tools to agents", Type: "toggle", Help: "Register the server's tools as <name>.<tool> in the agent catalog."},
		{Field: "expose_reference", Label: "Expose as a reference source", Type: "toggle", Help: "Make the server selectable in writer/research source pickers (uses the Search tool below)."},
		{Field: "search_tool", Label: "Search tool name", Placeholder: "search", Help: "MCP tool called for reference lookups. Only used when 'Expose as a reference source' is on. Defaults to 'search'."},

		{Field: "enabled", Label: "Enabled", Type: "toggle", Help: "Connect on startup and on save. Disable to suspend without deleting."},
	}
}
