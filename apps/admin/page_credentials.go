package admin

import (
	"github.com/cmcoffee/gohort/core/ui"
)

// credentialsSections is the credentials part of the admin page: API Credentials.
func (a *AdminApp) credentialsSections() []ui.Section {
	return []ui.Section{
		{
			Title:    "API Credentials",
			Subtitle: "Secure-API credentials the LLM can call via tools. The LLM never sees the secret — it's injected server-side, and the Allowed URL pattern rejects off-target requests before the secret is attached. \"Secure\" hides the direct call_<name> tool but leaves wrapped temp tools working; \"Disable\" suspends the credential entirely. OAuth2 credentials mint + refresh their own bearer token; a \"Needs secret\" badge marks a Builder-authored draft awaiting its client secret.",
			Body: ui.Stack{
				Children: []ui.Component{
					ui.Table{
						Source: "api/secure-api",
						RowKey: "name",
						Columns: []ui.Col{
							{Field: "name", Flex: 1},
							{Field: "type", Mute: true},
							// Namespace: global (this admin surface) vs user-owned.
							// Every credential here is global today; the badge makes
							// the classification visible as user namespaces land.
							{
								Field: "namespace", Type: "badge",
								Badges: []ui.BadgeMapping{
									{Value: "global", Label: "Global", Color: "mute"},
									{Value: "user", Label: "User-owned", Color: "info"},
								},
							},
							// Status badges — at-a-glance current state.
							{
								Field: "disabled", Type: "badge",
								Badges: []ui.BadgeMapping{
									{Value: true, Label: "Disabled", Color: "danger"},
									{Value: false, Label: "Enabled", Color: "success"},
								},
							},
							// Lockdown level (open / secured) is shown and set by the
							// segmented pill in the row actions below — no separate
							// state badge needed here.
							// Dead-credential warning — locked and reached by
							// nothing (no declaring tool AND no connector). Only
							// renders when set.
							{
								Field: "orphaned", Type: "badge",
								Badges: []ui.BadgeMapping{
									{Value: true, Label: "⚠ Nothing uses this", Color: "danger"},
								},
							},
							// Owned by another configuration, not by this page.
							// The peer key is the case: peering writes it when a
							// peer is added and deletes it when the peer is
							// forgotten, so editing it here is a change the next
							// sync overwrites. It stays LISTED because somebody
							// asking "what holds this key" has to be able to
							// find it — a record that exists and appears nowhere
							// is its own kind of lie — but it should not read as
							// an ordinary credential.
							{
								Field: "managed", Type: "badge",
								Badges: []ui.BadgeMapping{
									{Value: "peer", Label: "Managed by Peers", Color: "info"},
								},
							},
							// Pending = oauth2 draft missing its secret.
							// Only renders the badge when true (mute/blank
							// otherwise keeps non-oauth rows uncluttered).
							{
								Field: "pending", Type: "badge",
								Badges: []ui.BadgeMapping{
									{Value: true, Label: "Needs secret", Color: "warning"},
								},
							},
						},
						RowActions: []ui.RowAction{
							// Edit — full add/edit form, OAuth-aware. Source
							// fetches the single record; secret stays blank so
							// leaving it untouched keeps the stored secret. The
							// Access ACL (which users may use this credential) is
							// edited by its own picker below the form, not as a
							// free-text tags field — so admins pick real users.
							ui.Expand("Edit", ui.FormPanel{
								Source:      "api/secure-api?name={name}",
								PostURL:     "api/secure-api",
								Invalidate:  costSources,
								TestURL:     "api/secure-api/test",
								TestLabel:   "Test token (oauth2)",
								SubmitLabel: "Save changes",
								Fields:      credentialFormFields(),
							}),
							// Tools — the tools connected to this credential (they declare
							// it). What a Secured cred is bound to (and a scoped one
							// dispatches through). Empty on a Secured cred = dead-credential.
							ui.Expand("Tools", ui.Table{
								Source: "api/secure-api?tools={name}",
								RowKey: "tool",
								Columns: []ui.Col{
									{Field: "tool", Label: "Tool", Flex: 1},
									{Field: "agent", Label: "On", Mute: true},
									// "secret" tools break once Secured (raw key
									// blocked) — rework them to fetch_via first.
									{Field: "via", Label: "Via", Type: "badge", Badges: []ui.BadgeMapping{
										{Value: "fetch_via", Label: "fetch_via", Color: "success"},
										{Value: "secret", Label: "secret ⚠", Color: "warning"},
										{Value: "api", Label: "api", Color: "mute"},
										{Value: "connector", Label: "connector", Color: "mute"},
									}},
								},
								EmptyText: "No tool uses this credential.",
							}),
							// Bindings — for a SECURED credential, the tools bound to
							// it (auto-resolved from their declaration) and any the
							// admin has REVOKED (a durable deny). A bound tool
							// dispatches through the cred (secret server-side) and
							// access follows the tool's own scope. Revoke → dispatch
							// refuses it and a same-name re-author is blocked;
							// Approve un-revokes. Only meaningful when Secured.
							ui.ExpandIf("Bindings", "secured", "", ui.Table{
								Source: "api/secure-api?bindings={name}",
								RowKey: "tool",
								Columns: []ui.Col{
									{Field: "tool", Label: "Tool", Flex: 1},
									{Field: "status", Label: "Status", Type: "badge", Badges: []ui.BadgeMapping{
										{Value: "bound", Label: "Bound", Color: "success"},
										{Value: "revoked", Label: "Revoked", Color: "danger"},
									}},
								},
								RowActions: []ui.RowAction{
									// Approve only shows on a revoked row (un-revoke).
									{Type: "button", Label: "Approve", Method: "POST",
										PostTo: "api/secure-api?action=approve_binding&name={cred}&tool={tool}",
										HideIf: "_approved"},
									{Type: "button", Label: "Revoke", Method: "POST", Variant: "danger",
										PostTo: "api/secure-api?action=revoke_binding&name={cred}&tool={tool}",
										HideIf: "_revoked"},
								},
								EmptyText: "No tools bound yet. A tool that declares this secured credential is bound automatically and appears here.",
							}),
							// Access — tier-1 user ACL: which USERS may use this
							// credential. (Tier 2 — which of a user's OWN agents may
							// use it — is on the agent editor, "Credentials this agent
							// may use", so each user scopes their own fleet instead of
							// the admin managing an unbounded per-agent list here.)
							// HIDDEN when Secured: a secured cred has no user ACL — its
							// access is deferred to the tools bound to it (see Bindings),
							// so a user list here would be moot and misleading.
							ui.ModalActionIf("Access", "", "secured", ui.ACLPicker(ui.ACLPickerConfig{
								OptionsSource: "api/user-candidates",
								RecordSource:  "api/secure-api?name={name}",
								Field:         "allowed_users",
								PostTo:        "api/secure-api",
								Method:        "POST",
								Noun:          "user",
								Intro:         "Which users may use this credential. Empty = all users. Each user then chooses which of THEIR agents use it, on the agent editor.",
								EmptyText:     "No other users to grant yet.",
							})),
							// Enable/Disable pair — only one renders depending on
							// current state. Left NEUTRAL (no variant): the button
							// color used to encode action-severity (green Enable /
							// amber Disable), which contradicted the adjacent state
							// badge — a green Enable sat next to a red "Disabled"
							// badge. Only the label changes now; state color lives
							// on the badge alone.
							{Type: "button", Label: "Enable",
								PostTo: "api/secure-api?action=enable&name={name}",
								Method: "POST", OnlyIf: "disabled"},
							{Type: "button", Label: "Disable",
								PostTo: "api/secure-api?action=disable&name={name}",
								Method: "POST",
								HideIf: "disabled"},
							// Lockdown pill — Open (generic call tool + auto-route +
							// per-agent scope) vs 🔒 Secured (reachable only through the
							// tools that already declare it: off the auto-route + catalog,
							// scope no longer applies, no NEW tool can declare it). The
							// current level is highlighted; picking the other POSTs it.
							// "Secured" confirms first — it's consequential.
							{Type: "segmented", Field: "access_level",
								PostTo: "api/secure-api?action=access&name={name}",
								Options: []ui.SelectOption{
									{Value: "open", Label: "Open"},
									{Value: "secured", Label: "🔒 Secured",
										Confirm: "Secure this credential to the tools that already use it? It leaves the fetch_url auto-route and the tool catalog, its per-agent scope no longer applies, and NO new or edited tool can declare it (set it back to Open to change which tools use it). Reversible."},
								}},
							{Type: "button", Label: "Export", Method: "client",
								PostTo: "credentials_export", Compact: true},
							// Delete stays red — irreversible destruction.
							{Type: "button", Label: "Delete",
								PostTo:  "api/secure-api?name={name}",
								Method:  "DELETE",
								Confirm: "Delete this credential? The encrypted secret goes with it.",
								Variant: "danger",
								// Every tool that names this credential is
								// broken the moment it goes: the "⚠ missing"
								// badge in Persistent Tools is computed from
								// whether the credential still exists.
								Invalidate: []string{"api/persistent-tools"}},
						},
						EmptyText: "No credentials registered. Add one with the button below.",
					},
					// Add lives in the same card; pops the create form in a
					// modal. Edit an existing credential from its row (leave
					// the secret blank to keep the stored value).
					ui.ModalButton{
						Label:    "Add credential",
						Title:    "Add API credential",
						Subtitle: "Pick a type. Bearer / header / query / basic attach a static secret; OAuth2 mints + refreshes a bearer token from a grant.",
						Variant:  "primary",
						Width:    "640px",
						Body: ui.FormPanel{
							PostURL:     "api/secure-api",
							TestURL:     "api/secure-api/test",
							TestLabel:   "Test token (oauth2)",
							SubmitLabel: "Create credential",
							Fields:      credentialFormFields(),
							// Supplying a credential a tool was waiting on
							// clears that tool's "⚠ missing" badge: the
							// usual reason to add one is the tool that has
							// been sitting there broken. The cost surfaces
							// go too, since a credential can arrive with a
							// per-call cost already set.
							Invalidate: append([]string{"api/secure-api", "api/persistent-tools"}, costSources...),
						},
					},
					// Export all credentials' CONFIG as one bundle. Secrets never
					// travel — imported credentials land inert (pending a secret)
					// until the admin supplies one here.
					ui.Toolbar{
						Actions: []ui.ToolbarAction{
							{Label: "Export all credentials", Method: "client", URL: "credentials_export_all"},
						},
					},
				},
			},
		},
	}
}

// credentialFormFields is the shared field list for the API Credentials
// add (modal) and edit (row Expand) forms. Type-specific inputs collapse
// via value-matched ShowWhen so a bearer token doesn't surface OAuth /
// JWT fields. The secret is a password that stays blank on edit — leaving
// it blank keeps the stored secret; the OAuth config of an existing draft
// is preserved server-side when only the secret is resent.
func credentialFormFields() []ui.FormField {
	return []ui.FormField{
		{Field: "ident", Type: "header", Label: "Identity"},
		{Field: "name", Label: "Name", Placeholder: "github_api", Help: "snake_case. Becomes call_<name> in the LLM catalog. Re-using a name updates that credential."},
		{Field: "type", Label: "Type", Type: "select", Options: []ui.SelectOption{
			{Value: "bearer", Label: "Bearer (Authorization: Bearer ...)"},
			{Value: "header", Label: "Custom header"},
			{Value: "query", Label: "Query param"},
			{Value: "basic_auth", Label: "HTTP Basic (user:pass)"},
			{Value: "oauth2", Label: "OAuth2 (token-minting)"},
		}},
		{Field: "param_name", Label: "Header / Param name", Placeholder: "X-Api-Key or api_key", ShowWhen: "type:header|query"},

		{Field: "oauth_hdr", Type: "header", Label: "OAuth2 config", ShowWhen: "type:oauth2"},
		{Field: "grant", Label: "Grant", Type: "select", ShowWhen: "type:oauth2", Options: []ui.SelectOption{
			{Value: "client_credentials", Label: "client_credentials"},
			{Value: "jwt_bearer", Label: "jwt_bearer"},
			{Value: "refresh_token", Label: "refresh_token"},
			{Value: "password", Label: "password (user login)"},
			{Value: "authorization_code", Label: "authorization_code (user connects their own account)"},
		}},
		{Field: "token_url", Label: "Token URL (https)", Placeholder: "https://api.ebay.com/identity/v1/oauth2/token", ShowWhen: "type:oauth2"},
		{Field: "authorize_url", Label: "Authorize URL (https)", Placeholder: "https://accounts.google.com/o/oauth2/v2/auth", ShowWhen: "type:oauth2;grant:authorization_code", Help: "The provider's consent page. Each user is sent here to approve, then redirected back to /account/oauth/callback (PKCE). Register that callback URL with the provider, and set Whose credentials = Per user. The Client Secret below is the app's client secret (blank for a public/PKCE-only client)."},
		{Field: "client_id", Label: "Client / App ID", Placeholder: "non-secret app/client ID", ShowWhen: "type:oauth2"},
		// Single shared secret field (one input avoids the duplicate-name
		// clobber the form's submit loop would otherwise cause). For OAuth it
		// IS the client secret; positioned right after Client/App ID so the
		// OAuth block reads Token URL → Client/App ID → Client Secret → Scope.
		{Field: "username", Label: "Username", Placeholder: "the user / API key to log in as", ShowWhen: "type:oauth2|basic_auth", Help: "HTTP Basic auth and the OAuth2 password grant. For OPNsense (basic_auth) this is the API key; the secret goes in the Secret/Password field below. Stored as plain config, so it shows when you re-edit (only the secret stays hidden)."},
		{Field: "secret", Label: "Client Secret / Secret / Password", Type: "password", Help: "The secret for this credential: OAuth = the CLIENT secret (jwt_bearer = the RSA private key; refresh_token = the refresh token); bearer = the token; header/query = the API key; basic_auth = the PASSWORD (OPNsense: the API secret), paired with the Username above. Stored encrypted. Leave blank when editing to keep it."},
		{Field: "scope", Label: "Scope (optional)", Placeholder: "https://api.ebay.com/oauth/api_scope", ShowWhen: "type:oauth2"},
		{Field: "jwt_issuer", Label: "JWT issuer (iss)", Placeholder: "service-account@project.iam.gserviceaccount.com", ShowWhen: "type:oauth2;grant:jwt_bearer"},
		{Field: "jwt_subject", Label: "JWT subject (sub, optional)", ShowWhen: "type:oauth2;grant:jwt_bearer"},
		{Field: "jwt_audience", Label: "JWT audience (aud, optional)", Placeholder: "defaults to token URL", ShowWhen: "type:oauth2;grant:jwt_bearer"},
		{Field: "jwt_key_id", Label: "JWT key id (kid, optional)", ShowWhen: "type:oauth2;grant:jwt_bearer"},
		{Field: "password", Label: "Password", Type: "password", ShowWhen: "type:oauth2;grant:password", Help: "The resource-owner password (the SECOND secret of the password grant; the Client Secret field above holds the CLIENT secret). Stored encrypted, separately. Leave blank when editing to keep it."},

		{Field: "safety", Type: "header", Label: "Safety + limits"},
		{Field: "base_url", Label: "Base URL", Placeholder: "https://192.168.0.1", Help: "The server this credential talks to. Requests are allowed only under this host (and the endpoints below). This is where you change which server it reaches."},
		{Field: "allowed_endpoints", Label: "Allowed Endpoints", Type: "tags", Help: "Paths under the Base URL this credential may call. e.g. /api/* allows everything under /api/ ; /api/core/* scopes to one module. Add/remove entries. Leave empty to allow ANY path under the Base URL."},
		// (The legacy "Allowed URL pattern" single-glob field is retired
		// from the form: two overlapping scoping fields kept misleading
		// admins and LLMs about which applied. Old records that still
		// carry a pattern keep working — simple prefix globs are
		// auto-migrated onto Base URL at startup, complex ones are
		// honored by the runtime fallback.)
		{Field: "insecure_skip_tls", Label: "Allow self-signed / skip TLS verification", Type: "toggle", Help: "Turn ON only for LAN appliances with self-signed certs or hosts addressed by IP (e.g. an OPNsense box at https://192.168.0.1, where no cert can validate). Disables certificate checking for THIS credential's requests only. Leave OFF for public internet APIs."},
		{Field: "denied_url_patterns", Label: "Denied URL patterns", Type: "tags", Help: "Optional explicit denies, checked before the allow pattern."},
		// A closed set, so it is ticked rather than typed. As free text it
		// invited "Get" and "POST " — values that match nothing, narrowing a
		// credential to less than the admin believed while reading as though
		// they had granted it.
		{Field: "allowed_methods", Label: "Allowed methods", Type: "checklist",
			Options: []ui.SelectOption{
				{Value: "GET", Label: "GET", Help: "read"},
				{Value: "HEAD", Label: "HEAD", Help: "read, headers only"},
				{Value: "OPTIONS", Label: "OPTIONS", Help: "read, capability probe"},
				{Value: "POST", Label: "POST", Help: "create or invoke"},
				{Value: "PUT", Label: "PUT", Help: "replace"},
				{Value: "PATCH", Label: "PATCH", Help: "modify"},
				{Value: "DELETE", Label: "DELETE", Help: "remove"},
			},
			Help: "Which HTTP methods this credential may use. NONE CHECKED = all of them, which is the default — tick some to narrow it, most usefully to the read-only three when a credential exists to fetch and nothing else."},
		{Field: "max_calls_per_day", Label: "Max calls / day", Type: "number", Min: 0, Help: "0 = unlimited."},
		{Field: "cost_per_call", Label: "Cost per call ($)", Type: "number", Decimals: 6, Min: 0, Help: "Optional. Dollar cost of one dispatched call through this credential, for the Costs tab chart + per-source breakdown. 0 = untracked (free endpoint)."},
		{Field: "requires_confirm", Label: "Require confirm before each call", Type: "toggle", Help: "The escalation tier. ON: every agent call through this credential renders an Allow once / Deny card in the chat and waits for the session owner; headless runs (channel wakes, schedules) are denied outright. Use for services that reach real people (messaging) or spend money. OFF: calls dispatch silently — right for an agent's own low-stakes accounts."},
		{Field: "cred_scope", Label: "Whose credentials", Type: "select",
			Options: []ui.SelectOption{
				{Value: "shared", Label: "Shared — one key for everyone (you set it here)"},
				{Value: "per_user", Label: "Per user — each user sets their own key (on their Account page)"},
			},
			Help: "Shared: this credential's secret (below) is used for every user's calls — a service account / shared key. Per user: leave the secret blank here; each user supplies their OWN key on their Account page, and calls run as that user. Use per-user when writes need real attribution + per-user permissions."},
		{Field: "description", Label: "Description", Type: "textarea", Rows: 2, Help: "Shown to the LLM as the call_<name> tool description."},
	}
}
