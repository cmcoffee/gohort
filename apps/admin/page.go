// New admin page — framework-rendered with the simple sections migrated
// to core/ui's declarative components. Sections that need new framework
// primitives (DB Browser tree, Cost Rates split chart, Routing
// per-row select+number table, Watchers/Tools/Tasks edit dialogs) still
// live at /admin/legacy until each gets ported.

package admin

import (
	"net/http"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// sourceHookFormTemplates turns the built-in SourceHookTemplates into
// FormPanel presets for the "Start from template" dropdown — so an
// operator can pick PubMed / OpenAlex / Westlaw / etc. and have the
// endpoint + field mappings filled in, then just add the API key.
// sourceHookFormFields is the editable field set for a source hook, shared
// by the "Add source" modal (empty create form) and the per-row "Edit"
// expand (pre-filled from api/source-hooks?name={name}). Field names match
// the SourceHook json tags so the GET-one response pre-fills directly.
func sourceHookFormFields() []ui.FormField {
	return []ui.FormField{
		{Field: "ident", Type: "header", Label: "Identity"},
		{Field: "name", Label: "Name", Placeholder: "e.g. PubMed", Help: "Display name and unique key — re-using a name updates that hook."},
		{Field: "type", Label: "Type", Type: "select", Options: []ui.SelectOption{
			{Value: "api", Label: "API — search endpoint returning results"},
			{Value: "rag", Label: "RAG — endpoint returning document chunks"},
			{Value: "paywall", Label: "Paywall — adds auth headers to fetch_url (not a search tool)"},
		}},
		{Field: "endpoint", Label: "Endpoint URL", Placeholder: "https://api.example.com/search", Help: "Base URL for API/RAG. Leave blank for paywall hooks."},
		{Field: "auth", Type: "header", Label: "Authentication"},
		{Field: "auth_type", Label: "Auth type", Type: "select", Options: []ui.SelectOption{
			{Value: "none", Label: "None"},
			{Value: "api_key", Label: "API key"},
			{Value: "bearer", Label: "Bearer token"},
		}},
		{Field: "auth_key", Label: "Auth key / token", Type: "password", Help: "Stored encrypted. Leave blank to keep an existing secret unchanged."},
		{Field: "mapping", Type: "header", Label: "Result mapping (API / RAG)", Help: "How to read the endpoint's JSON response."},
		{Field: "query_param", Label: "Query param", Placeholder: "q / query / term", Help: "Name of the query parameter the endpoint expects."},
		{Field: "results_path", Label: "Results path", Placeholder: "results / data.items", Help: "Dotted JSON path to the results array."},
		{Field: "title_field", Label: "Title field", Placeholder: "title"},
		{Field: "url_field", Label: "URL field", Placeholder: "url"},
		{Field: "snippet_field", Label: "Snippet field", Placeholder: "snippet"},
		{Field: "content_field", Label: "Content field (RAG)", Placeholder: "content"},
		{Field: "activation", Type: "header", Label: "Activation"},
		{Field: "trigger_domains", Label: "Trigger domains", Type: "tags", Help: "Topic domains that activate this hook in research/debate (e.g. legal, medical)."},
		{Field: "always_active", Label: "Always active", Type: "toggle", Help: "Query this hook for every topic, regardless of trigger domains."},
		{Field: "domains", Label: "Paywall domains", Type: "tags", Help: "(paywall only) domains this hook attaches auth to, e.g. wsj.com."},
		{Field: "max_rps", Label: "Max requests/sec", Type: "number", Min: 0, Max: 100, Help: "0 = unlimited."},
		{Field: "llm", Type: "header", Label: "LLM exposure"},
		{Field: "expose_to_llm", Label: "Expose as an agent tool", Type: "toggle", Help: "When on, agents can call this hook directly as a named tool. Paywall hooks are never exposed."},
		{Field: "tool_name", Label: "Tool name", Placeholder: "pubmed_search", Help: "Lowercase, underscores. Defaults to \"<name>_search\" when blank."},
		{Field: "tool_description", Label: "Tool description", Type: "textarea", Rows: 3, Help: "Tells the model when to choose this over web_search."},
	}
}

func sourceHookFormTemplates() []ui.FormTemplate {
	tpls := SourceHookTemplates()
	out := make([]ui.FormTemplate, 0, len(tpls))
	for _, t := range tpls {
		h := t.Hook
		label := h.Name
		if t.Description != "" {
			label += " — " + t.Description
		}
		vals := map[string]any{
			"name":          h.Name,
			"type":          string(h.Type),
			"endpoint":      h.Endpoint,
			"auth_type":     string(h.AuthType),
			"query_param":   h.QueryParam,
			"results_path":  h.ResultsPath,
			"title_field":   h.TitleField,
			"url_field":     h.URLField,
			"snippet_field": h.SnippetField,
		}
		if h.ContentField != "" {
			vals["content_field"] = h.ContentField
		}
		if len(h.TriggerDomains) > 0 {
			vals["trigger_domains"] = h.TriggerDomains
		}
		if len(h.Domains) > 0 {
			vals["domains"] = h.Domains
		}
		out = append(out, ui.FormTemplate{Label: label, Values: vals})
	}
	return out
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
		}},
		{Field: "client_id", Label: "Client / App ID", Placeholder: "non-secret app/client ID", ShowWhen: "type:oauth2"},
		{Field: "token_url", Label: "Token URL (https)", Placeholder: "https://api.ebay.com/identity/v1/oauth2/token", ShowWhen: "type:oauth2"},
		{Field: "scope", Label: "Scope (optional)", Placeholder: "https://api.ebay.com/oauth/api_scope", ShowWhen: "type:oauth2"},
		{Field: "jwt_issuer", Label: "JWT issuer (iss)", Placeholder: "service-account@project.iam.gserviceaccount.com", ShowWhen: "type:oauth2;grant:jwt_bearer"},
		{Field: "jwt_subject", Label: "JWT subject (sub, optional)", ShowWhen: "type:oauth2;grant:jwt_bearer"},
		{Field: "jwt_audience", Label: "JWT audience (aud, optional)", Placeholder: "defaults to token URL", ShowWhen: "type:oauth2;grant:jwt_bearer"},
		{Field: "jwt_key_id", Label: "JWT key id (kid, optional)", ShowWhen: "type:oauth2;grant:jwt_bearer"},

		{Field: "secret", Label: "Secret", Type: "password", Help: "Token / API key / client secret / RSA private key / refresh token, depending on type. Stored encrypted. Leave blank when editing to keep the existing secret."},

		{Field: "safety", Type: "header", Label: "Safety + limits"},
		{Field: "allowed_url_pattern", Label: "Allowed URL pattern", Placeholder: "https://api.github.com/**", Help: "The linchpin safety property: requests to URLs that don't match are rejected before the secret is attached. * matches up to next slash; ** matches arbitrary chars."},
		{Field: "denied_url_patterns", Label: "Denied URL patterns", Type: "tags", Help: "Optional explicit denies, checked before the allow pattern."},
		{Field: "allowed_methods", Label: "Allowed methods", Type: "tags", Help: "e.g. GET, POST. Blank = all methods allowed."},
		{Field: "max_calls_per_day", Label: "Max calls / day", Type: "number", Min: 0, Help: "0 = unlimited."},
		{Field: "requires_confirm", Label: "Require confirm before each call", Type: "toggle"},
		{Field: "description", Label: "Description", Type: "textarea", Rows: 2, Help: "Shown to the LLM as the call_<name> tool description."},
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
		{Field: "oauth_note", Type: "header", Label: "Hosted login: Save first, then click Connect on the server's row to authorize. Each user connects their own account. The callback host must be https or localhost.", ShowWhen: "auth_mode:oauth"},

		{Field: "expose_hdr", Type: "header", Label: "Exposure"},
		{Field: "expose_tools", Label: "Expose tools to agents", Type: "toggle", Help: "Register the server's tools as <name>.<tool> in the agent catalog."},
		{Field: "expose_reference", Label: "Expose as a reference source", Type: "toggle", Help: "Make the server selectable in writer/research source pickers (uses the Search tool below)."},
		{Field: "search_tool", Label: "Search tool name", Placeholder: "search", Help: "MCP tool called for reference lookups. Only used when 'Expose as a reference source' is on. Defaults to 'search'."},

		{Field: "enabled", Label: "Enabled", Type: "toggle", Help: "Connect on startup and on save. Disable to suspend without deleting."},
	}
}

// databaseBrowserCard is the read-only kvlite table/key/record browser.
// A 3-pane drill-down is app-specific developer tooling, not a generic
// primitive, so it rides the sanctioned Card escape hatch (raw HTML +
// inline script) and talks to the existing /api/db/{tables,keys,record}
// endpoints. Self-contained: scoped CSS + DOM-built rows (textContent, no
// innerHTML injection). No backticks inside this raw string per repo rule.
func databaseBrowserCard() ui.Card {
	return ui.Card{HTML: `
<style>
.dbb { display:flex; gap:0.75rem; margin-top:0.25rem; min-height:200px; }
.dbb-pane { display:flex; flex-direction:column; min-width:0; }
.dbb-label { font-size:0.72rem; color:var(--text-mute,#8b949e); text-transform:uppercase; letter-spacing:0.05em; margin-bottom:0.35rem; white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }
.dbb-list { background:var(--bg-0,#0d1117); border:1px solid var(--border,#30363d); border-radius:6px; overflow-y:auto; max-height:380px; flex:1; }
.dbb-item { padding:0.35rem 0.6rem; font-size:0.8rem; color:var(--text,#c9d1d9); border-bottom:1px solid #161b22; cursor:pointer; word-break:break-all; line-height:1.4; }
.dbb-item:last-child { border-bottom:none; }
.dbb-item:hover { background:#21262d; }
.dbb-item.active { background:#1f3047; color:#79c0ff; }
.dbb-empty { padding:0.5rem 0.6rem; font-size:0.8rem; color:#8b949e; font-style:italic; }
.dbb-record { background:var(--bg-0,#0d1117); border:1px solid var(--border,#30363d); border-radius:6px; padding:0.6rem 0.75rem; overflow:auto; max-height:380px; font-size:0.78rem; color:var(--text,#c9d1d9); margin:0; white-space:pre; font-family:monospace; line-height:1.5; }
</style>
<div class="dbb">
  <div class="dbb-pane" style="width:180px;flex-shrink:0">
    <div class="dbb-label">Tables</div>
    <div class="dbb-list" id="dbb-tables"><div class="dbb-empty">Loading...</div></div>
  </div>
  <div class="dbb-pane" id="dbb-keys-pane" style="width:200px;flex-shrink:0;display:none">
    <div class="dbb-label" id="dbb-keys-label">Keys</div>
    <div class="dbb-list" id="dbb-keys"></div>
  </div>
  <div class="dbb-pane" id="dbb-rec-pane" style="flex:1;display:none">
    <div class="dbb-label" id="dbb-rec-label">Record</div>
    <pre class="dbb-record" id="dbb-rec"></pre>
  </div>
</div>
<script>
(function(){
  var activeTable = '';
  function mkItem(text, onclick){
    var d = document.createElement('div');
    d.className = 'dbb-item';
    d.textContent = text;
    d.addEventListener('click', function(){ onclick(d); });
    return d;
  }
  function clearActive(sel){ document.querySelectorAll(sel).forEach(function(e){ e.classList.remove('active'); }); }
  function loadTables(){
    fetch('api/db/tables').then(function(r){ return r.json(); }).then(function(tables){
      var list = document.getElementById('dbb-tables');
      list.innerHTML = '';
      if(!tables || !tables.length){ list.innerHTML = '<div class="dbb-empty">No tables found.</div>'; return; }
      tables.forEach(function(t){ list.appendChild(mkItem(t, function(el){ selectTable(t, el); })); });
    }).catch(function(){ var l=document.getElementById('dbb-tables'); if(l) l.innerHTML='<div class="dbb-empty">Failed to load tables.</div>'; });
  }
  function selectTable(table, el){
    activeTable = table;
    clearActive('#dbb-tables .dbb-item');
    if(el) el.classList.add('active');
    document.getElementById('dbb-keys-pane').style.display = '';
    document.getElementById('dbb-rec-pane').style.display = 'none';
    document.getElementById('dbb-keys-label').textContent = table;
    var keyList = document.getElementById('dbb-keys');
    keyList.innerHTML = '<div class="dbb-empty">Loading...</div>';
    fetch('api/db/keys?table=' + encodeURIComponent(table)).then(function(r){ return r.json(); }).then(function(keys){
      keyList.innerHTML = '';
      if(!keys || !keys.length){ keyList.innerHTML = '<div class="dbb-empty">No keys.</div>'; return; }
      keys.forEach(function(k){ keyList.appendChild(mkItem(k, function(el){ loadRecord(k, el); })); });
    }).catch(function(){ keyList.innerHTML = '<div class="dbb-empty">Failed to load keys.</div>'; });
  }
  function loadRecord(key, el){
    clearActive('#dbb-keys .dbb-item');
    if(el) el.classList.add('active');
    document.getElementById('dbb-rec-pane').style.display = '';
    document.getElementById('dbb-rec-label').textContent = key;
    var view = document.getElementById('dbb-rec');
    view.textContent = 'Loading...';
    fetch('api/db/record?table=' + encodeURIComponent(activeTable) + '&key=' + encodeURIComponent(key)).then(function(r){
      if(!r.ok) return r.text().then(function(t){ throw new Error(t); });
      return r.json();
    }).then(function(v){ view.textContent = JSON.stringify(v, null, 2); }).catch(function(err){ view.textContent = 'Error: ' + err.message; });
  }
  loadTables();
})();
</script>
`}
}

func (a *AdminApp) serveNewAdminPage(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	page := ui.Page{
		Title:     "Administrator",
		ShowTitle: true,
		BackURL:   "/",
		MaxWidth:  "900px", // wider than mobile-default; admin lives on a desktop browser
		Sections: []ui.Section{
			{
				Title:    "System Status",
				Subtitle: "Live readiness summary. Refreshes every 10 seconds.",
				Body: ui.DisplayPanel{
					Source:        "api/status",
					AutoRefreshMS: 10000,
					Pairs: []ui.DisplayPair{
						{Label: "TLS enabled", Field: "tls_enabled"},
						{Label: "TLS self-signed", Field: "tls_self_signed"},
						{Label: "Auth enabled", Field: "auth_enabled"},
						{Label: "User count", Field: "user_count"},
						{Label: "Active sessions", Field: "active_sessions"},
						{Label: "Public signup", Field: "allow_signup"},
					},
				},
			},
			{
				Title:    "Site Settings",
				Subtitle: "Authentication, naming, and operational quotas. Saved automatically as you edit.",
				Body: ui.FormPanel{
					Source: "api/settings",
					Fields: []ui.FormField{
						{Field: "allow_signup", Label: "Allow public signup", Type: "toggle",
							Help: "When off, only existing accounts can sign in. Approvals can still happen via the user list."},
						{Field: "service_name", Label: "Service name", Type: "text",
							Placeholder: "gohort", Help: "Shown in the page title and email From: line."},
						{Field: "external_url", Label: "External URL", Type: "text",
							Placeholder: "https://gohort.example.com",
							Help:        "Used to build links in notification emails. Include scheme."},
						{Field: "notify_from", Label: "Notification From", Type: "text",
							Placeholder: "noreply@example.com"},
						{Field: "session_days", Label: "Session lifetime (days)", Type: "number",
							Min: 1, Max: 90, Help: "Default 7."},
						{Field: "max_login_attempts", Label: "Max login attempts", Type: "number",
							Min: 1, Max: 100, Help: "Default 5. Failed attempts above this trigger a temporary lockout."},
						{Field: "lockout_minutes", Label: "Lockout duration (minutes)", Type: "number",
							Min: 1, Max: 1440, Help: "Default 15."},
						{Field: "fetch_cache_quota_mb", Label: "Fetch cache quota (MB)", Type: "number",
							Min: 0, Max: 10240, Help: "Disk budget for the URL fetch cache. 0 disables caching."},
					},
				},
			},
			{
				Title:    "Users",
				Subtitle: "Approve pending signups, grant or revoke admin, manage app access, or delete accounts. Pending users see a placeholder page until approved.",
				Body: ui.Table{
					Source: "api/users",
					RowKey: "username",
					Columns: []ui.Col{
						{Field: "username", Flex: 1},
					},
					RowActions: []ui.RowAction{
						// Admin toggle — partial PUT with {admin: bool}.
						// Label clarifies what the switch controls
						// (without it, the lone switch in the row was
						// ambiguous — users couldn't tell if it was
						// "user enabled" or "admin role").
						{
							Type:   "toggle",
							Field:  "admin",
							Label:  "Admin",
							PostTo: "api/users/{username}",
							Method: "PUT",
						},
						// Approve / reject — visible only while the user is pending.
						{Type: "button", Label: "Approve", PostTo: "api/users/{username}/approve",
							Method: "POST", OnlyIf: "pending"},
						{Type: "button", Label: "Reject", PostTo: "api/users/{username}/reject",
							Method: "POST", OnlyIf: "pending", Variant: "danger"},
						// Apps access via chip picker. RecordSource hits the
						// per-user GET we just added so the picker shows the
						// user's actual current apps (not the global list).
						func() ui.RowAction {
							a := ui.Expand("Apps", ui.ChipPicker{
								OptionsSource: "api/apps",
								RecordSource:  "api/users/{username}",
								Field:         "apps",
								PostTo:        "api/users/{username}/apps",
								Method:        "PUT",
								NameField:     "path", // value stored in user.apps[]
								LabelField:    "name", // friendly label rendered on the chip
							})
							a.Compact = true
							return a
						}(),
						// Delete with confirm — danger variant.
						{Type: "button", Label: "Delete", PostTo: "api/users/{username}",
							Method:  "DELETE",
							Confirm: "Delete this user permanently? Their sessions and apps assignment go with them.",
							Variant: "danger"},
					},
					EmptyText: "No users yet.",
				},
			},
			{
				Title:    "Default Apps",
				Subtitle: "Apps every newly-approved user gets access to by default. Per-user overrides above take precedence.",
				Body: ui.ChipPicker{
					OptionsSource: "api/apps",
					RecordSource:  "api/settings",
					Field:         "default_apps",
					PostTo:        "api/settings",
					Method:        "PUT",
					NameField:     "path",
					LabelField:    "name",
				},
			},
			{
				Title:     "LLM Routing",
				Subtitle:  "Pick which tier handles each pipeline stage. \"lead\" uses the precision (remote) LLM. \"worker\" uses the local model. \"worker (thinking)\" enables extended reasoning on the local model. Budget caps thinking tokens for that stage (0 = stage default). Private stages cannot route to lead.",
				Collapsed: true,
				Body: ui.Table{
					Source: "api/routing",
					RowKey: "key",
					Columns: []ui.Col{
						{Field: "label", Flex: 1},
						{Field: "group", Mute: true},
					},
					RowActions: []ui.RowAction{
						{
							Type:   "select",
							Field:  "value",
							PostTo: "api/routing",
							Method: "POST",
							Width:  "10rem",
							Options: []ui.SelectOption{
								{Value: "lead", Label: "Lead"},
								{Value: "worker", Label: "Worker"},
								{Value: "worker (thinking)", Label: "Worker (Thinking)"},
							},
							// Hide the "lead" option when the stage is
							// private — private stages can't escalate.
							FilterOptionsIf: "private",
							FilterOptions:   "lead",
							// Mark the option matching the stage's
							// out-of-the-box Default with an asterisk
							// so operators can tell at a glance what
							// the registered default is even when
							// they've overridden it.
							DefaultField: "default",
						},
						{
							Type:   "number",
							Field:  "think_budget",
							Label:  "budget",
							PostTo: "api/routing",
							Method: "POST",
							Min:    0,
							Max:    65536,
							Width:  "7rem",
						},
					},
					EmptyText: "No routing stages registered.",
				},
			},
			{
				Title:    "Worker LLM Thinking",
				Subtitle: "Default thinking settings for the worker (local) LLM. Per-route overrides in the routing table take precedence. Budget 0 = unlimited.",
				Body: ui.FormPanel{
					Source: "api/worker-thinking",
					Fields: []ui.FormField{
						{Field: "enabled", Label: "Thinking enabled by default", Type: "toggle"},
						{Field: "budget", Label: "Default thinking budget (tokens)", Type: "number",
							Min: 0, Max: 65536, Help: "0 = unlimited (model decides).",
							ShowWhen: "enabled"},
					},
				},
			},
			{
				Title:    "Cost History (Last 30 Days)",
				Subtitle: "Daily LLM + search spend across all pipelines. Hover any bar for the per-day breakdown. Tap **Adjust prices** to edit the per-token / per-call rates that feed the dollar estimate.",
				Body: ui.Stack{
					Children: []ui.Component{
						ui.BarChart{
							Source:    "api/cost-history?days=30",
							XField:    "date",
							YField:    "cost",
							XFormat:   "date",
							YPrefix:   "$",
							YDecimals: 4,
							HeightPx:  220,
							EmptyText: "No usage recorded in the last 30 days.",
							Breakdown: []ui.DisplayPair{
								{Label: "Runs", Field: "run_count", Format: "thousands"},
								{Label: "Worker in", Field: "worker_input", Format: "thousands", Mono: true},
								{Label: "Worker out", Field: "worker_output", Format: "thousands", Mono: true},
								{Label: "Lead in", Field: "lead_input", Format: "thousands", Mono: true},
								{Label: "Lead out", Field: "lead_output", Format: "thousands", Mono: true},
								{Label: "Searches", Field: "search_calls", Format: "thousands", Mono: true},
								{Label: "Images", Field: "image_calls", Format: "thousands", Mono: true},
							},
						},
						ui.ModalButton{
							Label:    "Adjust prices",
							Title:    "Adjust prices",
							Subtitle: "Per-token and per-call dollar rates used to estimate run costs. Worker = local LLM, Lead = remote LLM. Set to 0 for free tiers.",
							Body: ui.FormPanel{
								Source: "api/cost-rates",
								Method: "PUT",
								Fields: []ui.FormField{
									{Field: "worker_input_per_1k", Label: "Worker input ($/1K tokens)",
										Type: "number", Decimals: 6, Min: 0,
										Help: "Cost of one thousand input tokens to the worker LLM."},
									{Field: "worker_output_per_1k", Label: "Worker output ($/1K tokens)",
										Type: "number", Decimals: 6, Min: 0},
									{Field: "lead_input_per_1k", Label: "Lead input ($/1K tokens)",
										Type: "number", Decimals: 6, Min: 0,
										Help: "Cost of one thousand input tokens to the lead (remote) LLM."},
									{Field: "lead_output_per_1k", Label: "Lead output ($/1K tokens)",
										Type: "number", Decimals: 6, Min: 0},
									{Field: "search_per_call", Label: "Search ($/call)",
										Type: "number", Decimals: 6, Min: 0,
										Help: "Cost per web-search API call (Brave, Tavily, etc.)."},
									{Field: "image_per_call", Label: "Image generation ($/call)",
										Type: "number", Decimals: 6, Min: 0},
								},
							},
						},
					},
				},
			},
			{
				Title:    "Ollama Proxy",
				Subtitle: "Expose gohort as a fair-queued Ollama endpoint. Point Ollama clients at gohort's port instead of Ollama's; they share the local model scheduler. Requires restart when port changes.",
				Body: ui.FormPanel{
					Source: "api/settings",
					Fields: []ui.FormField{
						{Field: "ollama_proxy_enabled", Label: "Enable Ollama Proxy", Type: "toggle"},
						{Field: "ollama_proxy_port", Label: "Proxy port", Type: "number",
							Min: 1024, Max: 65535,
							Help:        "TCP port the proxy listens on. Default suggestion: 11435.",
							ShowWhen:    "ollama_proxy_enabled",
							Placeholder: "11435"},
					},
				},
			},
			{
				Title:    "Embeddings",
				Subtitle: "Vector store ingestion + semantic search. Endpoint is an Ollama-compatible /api/embed server — typically the same host as the worker LLM. Disabling makes ingestion and search no-ops.",
				Body: ui.FormPanel{
					Source:    "api/embeddings",
					TestURL:   "api/embeddings/test",
					TestLabel: "Test embed call",
					Fields: []ui.FormField{
						{Field: "enabled", Label: "Enable embeddings", Type: "toggle"},
						{Field: "endpoint", Label: "Endpoint", Type: "text",
							Placeholder: "http://localhost:11434/api",
							Help:        "Base URL including the API version prefix — gohort appends /embeddings. Pick a preset below for the canonical path on common platforms.",
							ShowWhen:    "enabled",
							Presets: []ui.FieldPreset{
								{Label: "Ollama", Value: "http://localhost:11434/api", Hint: "Ollama native API (→ /api/embeddings)"},
								{Label: "llama.cpp", Value: "http://localhost:8080/v1", Hint: "llama.cpp OpenAI-compatible (→ /v1/embeddings)"},
								{Label: "vLLM", Value: "http://localhost:8000/v1", Hint: "vLLM OpenAI-compatible (→ /v1/embeddings)"},
								{Label: "OpenAI", Value: "https://api.openai.com/v1", Hint: "OpenAI hosted (→ /v1/embeddings, requires API key support)"},
							}},
						{Field: "model", Label: "Model", Type: "text",
							Placeholder: "nomic-embed-text",
							Help:        "Leave blank for single-model backends (llama.cpp, vLLM, hf-tei — they ignore this field). Required for Ollama. Click a chip below to fill from the endpoint's model list.",
							ShowWhen:    "enabled",
							ChipsSource: "api/embeddings/models"},
						{Field: "api_key", Label: "API Key", Type: "password",
							Help:     "Optional bearer token. Set for OpenAI hosted / authenticated proxies; leave blank for local Ollama, llama.cpp, or vLLM.",
							ShowWhen: "enabled"},
					},
				},
			},
			{
				Title:    "Audio Transcription (STT)",
				Subtitle: "OpenAI-compatible /audio/transcriptions endpoint used for video / audio attachment transcription. Endpoint includes the API version prefix; gohort appends /audio/transcriptions.",
				Body: ui.FormPanel{
					Source:    "api/transcribe",
					TestURL:   "api/transcribe/test",
					TestLabel: "Test endpoint",
					Fields: []ui.FormField{
						{Field: "enabled", Label: "Enable transcription", Type: "toggle"},
						{Field: "endpoint", Label: "Endpoint", Type: "text",
							Placeholder: "http://localhost:8089/v1",
							Help:        "Base URL with the version prefix — gohort appends /audio/transcriptions.",
							ShowWhen:    "enabled",
							Presets: []ui.FieldPreset{
								{Label: "whisper.cpp", Value: "http://localhost:8089/v1", Hint: "Default whisper.cpp HTTP server port"},
								{Label: "OpenAI", Value: "https://api.openai.com/v1", Hint: "OpenAI hosted Whisper"},
							}},
						{Field: "model", Label: "Model", Type: "text",
							Placeholder: "whisper-1",
							Help:        "Optional. whisper.cpp ignores this; OpenAI expects 'whisper-1'.",
							ShowWhen:    "enabled"},
						{Field: "api_key", Label: "API Key", Type: "password",
							Help:     "Optional bearer token. Set for real OpenAI / authenticated proxies; leave blank for local whisper.cpp.",
							ShowWhen: "enabled"},
					},
				},
			},
			{
				Title:    "Image Generation",
				Subtitle: "LLM-driven image generation provider used by tools that produce illustrations or thumbnails. Leave API key blank to reuse the matching LLM provider's key (e.g. Gemini for the gemini image model).",
				Body: ui.FormPanel{
					Source:    "api/image-gen",
					TestURL:   "api/image-gen/test",
					TestLabel: "Test API key",
					Fields: []ui.FormField{
						{Field: "provider", Label: "Provider", Type: "select",
							Options: []ui.SelectOption{
								{Value: "gemini", Label: "Gemini (Imagen)"},
								{Value: "openai", Label: "OpenAI (DALL-E)"},
								{Value: "none", Label: "Disabled"},
							}},
						{Field: "api_key", Label: "API Key", Type: "password",
							Help:     "Provider API key. Leave blank to reuse the matching LLM provider's key.",
							ShowWhen: "provider"},
					},
				},
			},
			{
				Title:    "Web Search",
				Subtitle: "Provider for the web_search tool. DuckDuckGo and a SearXNG instance require no key; Brave / Google / Serper need one.",
				Body: ui.FormPanel{
					Source:    "api/web-search",
					TestURL:   "api/web-search/test",
					TestLabel: "Test search call",
					Fields: []ui.FormField{
						{Field: "provider", Label: "Provider", Type: "select",
							Options: []ui.SelectOption{
								{Value: "duckduckgo", Label: "DuckDuckGo (no key)"},
								{Value: "brave", Label: "Brave"},
								{Value: "google", Label: "Google"},
								{Value: "serper", Label: "Serper"},
								{Value: "searxng", Label: "SearXNG (self-hosted)"},
							}},
						{Field: "api_key", Label: "API Key", Type: "password",
							Help: "Required for Brave / Google / Serper."},
						{Field: "endpoint", Label: "Endpoint", Type: "text",
							Placeholder: "https://searx.example.com",
							Help:        "Required for SearXNG. The base URL of your instance."},
					},
				},
			},
			{
				Title:    "Mail (SMTP)",
				Subtitle: "Outbound SMTP for notification emails — signup approvals, scheduled deliveries, watcher alerts. Leave Server blank for localhost:25.",
				Body: ui.FormPanel{
					Source:    "api/mail",
					TestURL:   "api/mail/test",
					TestLabel: "Send test email",
					Fields: []ui.FormField{
						{Field: "server", Label: "SMTP Server", Type: "text",
							Placeholder: "smtp.gmail.com:587",
							Presets: []ui.FieldPreset{
								{Label: "Gmail", Value: "smtp.gmail.com:587"},
								{Label: "Outlook", Value: "smtp-mail.outlook.com:587"},
								{Label: "iCloud", Value: "smtp.mail.me.com:587"},
								{Label: "Local", Value: "localhost:25"},
							}},
						{Field: "from", Label: "From Address", Type: "text",
							Placeholder: "noreply@example.com"},
						{Field: "recipient", Label: "Default Recipient", Type: "text",
							Help: "Test emails and pipeline reports go here when no per-call recipient is given."},
						{Field: "username", Label: "SMTP Username", Type: "text"},
						{Field: "password", Label: "SMTP Password", Type: "password"},
					},
				},
			},
			{
				Title:    "Network Timeouts",
				Subtitle: "Outbound HTTP timeouts for source hooks and search APIs. Raise when working against slow upstreams; lower to fail fast in a tight loop.",
				Body: ui.FormPanel{
					Source: "api/network",
					Fields: []ui.FormField{
						{Field: "connect_timeout_seconds", Label: "Connect timeout (seconds)",
							Type: "number", Min: 1, Max: 120, Placeholder: "10",
							Help: "TCP + TLS connection timeout. Default 10."},
						{Field: "request_timeout_seconds", Label: "Request timeout (seconds)",
							Type: "number", Min: 1, Max: 300, Placeholder: "15",
							Help: "Per-read I/O timeout for HTTP response bodies. Default 15."},
					},
				},
			},
			{
				Title:    "Agent Loop Tuning",
				Subtitle: "Per-round behavior of the agent loop. Lower the history budget when long sessions push prefill latency or thrash the LLM's prompt cache; raise it when you need the model to remember more context across rounds.",
				Body: ui.FormPanel{
					Source: "api/agent-loop-tuning",
					Fields: []ui.FormField{
						{Field: "history_budget_percent", Label: "History budget (% of context window)",
							Type: "number", Min: 25, Max: 90, Placeholder: "50",
							Help: "Steady-state cap on per-round history as a percent of the LLM's context window. Default 50 (a 200K-window worker targets ~100K of history). Lower = faster prefill, more aggressive elision of old tool results; higher = more retained context, slower prefill. Clamped to 25-90."},
					},
				},
			},
			{
				Title:    "Scheduled Tasks",
				Subtitle: "Pending background work — proactive messages, scheduled updates. Expand a row for the full record + payload.",
				Body: ui.Table{
					Source: "api/scheduled-tasks",
					RowKey: "id",
					Columns: []ui.Col{
						{Field: "kind", Flex: 1},
						// Future-aware relative time: "in 5m" while pending,
						// flips to "5m ago" if the worker missed firing it.
						{Field: "run_at", Format: "fromnow", Mute: true},
					},
					RowActions: []ui.RowAction{
						// One combined "Details" expand showing the full
						// record AND the payload JSON, instead of two
						// adjacent buttons. Stack composes them in one panel.
						ui.Expand("Details", ui.Stack{
							Children: []ui.Component{
								ui.RecordView{
									Pairs: []ui.DisplayPair{
										{Label: "ID", Field: "id", Mono: true},
										{Label: "Kind", Field: "kind"},
										{Label: "Fires", Field: "run_at", Format: "fromnow"},
										{Label: "Run at (UTC)", Field: "run_at", Mono: true},
										{Label: "Created", Field: "created", Format: "reltime"},
									},
								},
								ui.JSONView{Field: "payload", Title: "Task payload"},
							},
						}),
						{
							Type: "button", Label: "Cancel",
							PostTo:  "api/scheduled-tasks?id={id}",
							Method:  "DELETE",
							Variant: "warning",
							Confirm: "Cancel this scheduled task?",
						},
					},
					AutoRefreshMS: 30000,
					EmptyText:     "No tasks scheduled.",
				},
			},
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
								// Status badges — at-a-glance current state.
								{
									Field: "disabled", Type: "badge",
									Badges: []ui.BadgeMapping{
										{Value: true, Label: "Disabled", Color: "danger"},
										{Value: false, Label: "Enabled", Color: "success"},
									},
								},
								{
									Field: "restricted", Type: "badge",
									Badges: []ui.BadgeMapping{
										{Value: true, Label: "Secured", Color: "warning"},
										{Value: false, Label: "Open", Color: "mute"},
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
								// leaving it untouched keeps the stored secret.
								ui.Expand("Edit", ui.FormPanel{
									Source:      "api/secure-api?name={name}",
									PostURL:     "api/secure-api",
									TestURL:     "api/secure-api/test",
									TestLabel:   "Test token (oauth2)",
									SubmitLabel: "Save changes",
									Fields:      credentialFormFields(),
								}),
								// Enable/Disable pair — only one renders depending on
								// current state. Disable uses warning (amber) so it
								// reads as suspend-not-destroy, distinct from Delete.
								{Type: "button", Label: "Enable",
									PostTo: "api/secure-api?action=enable&name={name}",
									Method: "POST", OnlyIf: "disabled", Variant: "success"},
								{Type: "button", Label: "Disable",
									PostTo:  "api/secure-api?action=disable&name={name}",
									Method:  "POST",
									HideIf:  "disabled",
									Variant: "warning"},
								// Open/Secure pair — Secure (formerly Restrict) reads
								// more naturally for "lock down to wrapped tools only".
								{Type: "button", Label: "Open",
									PostTo: "api/secure-api?action=open&name={name}",
									Method: "POST", OnlyIf: "restricted", Variant: "success"},
								{Type: "button", Label: "Secure",
									PostTo:  "api/secure-api?action=restrict&name={name}",
									Method:  "POST",
									HideIf:  "restricted",
									Variant: "warning"},
								// Delete stays red — irreversible destruction.
								{Type: "button", Label: "Delete",
									PostTo:  "api/secure-api?name={name}",
									Method:  "DELETE",
									Confirm: "Delete this credential? The encrypted secret goes with it.",
									Variant: "danger"},
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
							},
						},
					},
				},
			},
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
								{Type: "button", Label: "Enable",
									PostTo: "api/mcp-servers?action=enable&name={name}",
									Method: "POST", HideIf: "enabled", Variant: "success"},
								{Type: "button", Label: "Disable",
									PostTo:  "api/mcp-servers?action=disable&name={name}",
									Method:  "POST",
									OnlyIf:  "enabled",
									Variant: "warning"},
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
				Title:    "Source Hooks",
				Subtitle: "Curated external sources (PubMed, OpenAlex, EDGAR, custom API/RAG endpoints). Flip \"Expose to LLM\" and the hook becomes a per-hook agent tool (e.g. pubmed_search) any orchestrate agent can call directly; otherwise it's reachable only by the research/debate pipelines via topic routing.",
				Body: ui.Stack{
					Children: []ui.Component{
						ui.Table{
							Source: "api/source-hooks",
							RowKey: "name",
							Columns: []ui.Col{
								{Field: "name", Flex: 1},
								{Field: "type", Mute: true},
								{Field: "effective_tool", Label: "Tool", Mute: true, Flex: 1},
								{
									Field: "expose_to_llm", Type: "badge", Label: "LLM",
									Badges: []ui.BadgeMapping{
										{Value: true, Label: "Exposed", Color: "success"},
										{Value: false, Label: "Hidden", Color: "mute"},
									},
								},
								{
									Field: "has_auth", Type: "badge", Label: "Auth",
									Badges: []ui.BadgeMapping{
										{Value: true, Label: "Key set", Color: "success"},
										{Value: false, Label: "None", Color: "mute"},
									},
								},
							},
							RowActions: []ui.RowAction{
								ui.Expand("Edit", ui.FormPanel{
									Source:      "api/source-hooks?name={name}",
									PostURL:     "api/source-hooks",
									SubmitLabel: "Save changes",
									Templates:   sourceHookFormTemplates(),
									Fields:      sourceHookFormFields(),
								}),
								{Type: "button", Label: "Expose to LLM",
									PostTo: "api/source-hooks?action=expose&name={name}",
									Method: "POST", HideIf: "expose_to_llm", Variant: "success"},
								{Type: "button", Label: "Hide from LLM",
									PostTo:  "api/source-hooks?action=hide&name={name}",
									Method:  "POST",
									OnlyIf:  "expose_to_llm",
									Variant: "warning"},
								{Type: "button", Label: "Delete",
									PostTo:  "api/source-hooks?name={name}",
									Method:  "DELETE",
									Confirm: "Delete this source hook? Its encrypted auth key goes with it.",
									Variant: "danger"},
							},
							EmptyText: "No source hooks configured. Add one with the button below.",
						},
						// Add lives in the same card, below the listed sources;
						// pops the create form in a modal. Edit an existing hook
						// from its row (leave the auth key blank to keep the secret).
						ui.ModalButton{
							Label:    "Add source",
							Title:    "Add source hook",
							Subtitle: "For API/RAG hooks the field mappings tell the adapter how to read the endpoint's JSON.",
							Variant:  "primary",
							Width:    "640px",
							Body: ui.FormPanel{
								PostURL:     "api/source-hooks",
								SubmitLabel: "Create source hook",
								Templates:   sourceHookFormTemplates(),
								Fields:      sourceHookFormFields(),
							},
						},
					},
				},
			},
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
								{Label: "Credential", Field: "tool.credential", Mono: true},
								{Label: "Hook capabilities", Field: "tool.hook_capabilities", Mono: true},
								{Label: "Raw network", Field: "tool.raw_network"},
								{Label: "State path", Field: "tool.state_path", Mono: true},
								{Label: "Response pipe", Field: "tool.response_pipe", Mono: true, Block: true},
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
				Title:    "Persistent Tools (Active)",
				Subtitle: "Approved tools the LLM gets in every session. Description shows what each one does. Delete to revoke immediately.",
				Body: ui.Table{
					Source:       "api/persistent-tools",
					RecordsField: "active",
					RowKey:       "tool.name",
					Columns: []ui.Col{
						{Field: "tool.name", Flex: 1},
						{Field: "owner", Flex: 0, Mute: true},
						{Field: "tool.description", Flex: 2, Mute: true},
						{Field: "last_used_at", Format: "reltime", Mute: true},
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
								{Label: "Credential", Field: "tool.credential", Mono: true},
								{Label: "Hook capabilities", Field: "tool.hook_capabilities", Mono: true},
								{Label: "Raw network", Field: "tool.raw_network"},
								{Label: "State path", Field: "tool.state_path", Mono: true},
								{Label: "Response pipe", Field: "tool.response_pipe", Mono: true, Block: true},
								{Label: "Approved at", Field: "approved_at", Format: "reltime"},
								{Label: "Last used", Field: "last_used_at", Format: "reltime"},
							},
						}),
						{Type: "button", Label: "Delete",
							PostTo:     "api/persistent-tools?name={tool.name}&owner={owner}",
							Method:     "DELETE",
							Variant:    "danger",
							Confirm:    "Delete this active tool? The LLM will lose access immediately.",
							Optimistic: true},
					},
					EmptyText: "No active persistent tools.",
				},
			},
			{
				Title:    "Tool Groups",
				Subtitle: "Bundle related chat tools (a Calendar API, a communications suite, an Acme integration) under one logical heading. The runtime catalog collapses members into a single expandable entry — context savings when a deployment has many tools that share a purpose. Pick which tools to group; the LLM proposes the name and description based on what you selected.",
				Body: ui.Stack{
					Children: []ui.Component{
						// Auto-create: pick tools → LLM names + describes.
						// Skips the awkward "what should I call this?"
						// step since the LLM is the one that'll have to
						// call the group later anyway.
						ui.Card{
							HTML: `<div id="tg-create-wrap">
  <div id="tg-create-tools" style="display:flex;flex-wrap:wrap;gap:0.3rem;margin-bottom:0.6rem">Loading tools…</div>
  <div style="display:flex;gap:0.6rem;align-items:center">
    <button id="tg-create-btn" type="button" class="ui-row-btn primary" disabled>Create group (0 selected)</button>
    <span id="tg-create-status" style="color:var(--text-mute);font-size:0.85rem"></span>
  </div>
</div>
<style>
#tg-create-tools .tg-chip {
  background: transparent; border: 1px solid var(--border);
  color: var(--text-mute); padding: 0.2rem 0.55rem; border-radius: 999px;
  cursor: pointer; font-size: 0.78rem; font-family: inherit;
}
#tg-create-tools .tg-chip:hover { color: var(--text); border-color: var(--text-mute); }
#tg-create-tools .tg-chip.on { color: var(--accent); border-color: var(--accent); }
</style>
<script>
(function(){
  var selected = {};
  var wrap = document.getElementById('tg-create-tools');
  var btn = document.getElementById('tg-create-btn');
  var status = document.getElementById('tg-create-status');
  function updateBtn() {
    var n = Object.keys(selected).length;
    btn.textContent = 'Create group (' + n + ' selected)';
    btn.disabled = n === 0;
  }
  fetch('api/tool-groups/registry?exclude_grouped=true').then(function(r){return r.json()}).then(function(tools){
    wrap.innerHTML = '';
    if (!tools || !tools.length) { wrap.textContent = '(no tools available)'; return; }
    tools.forEach(function(t){
      var chip = document.createElement('button');
      chip.type = 'button';
      chip.className = 'tg-chip';
      chip.textContent = t.name;
      if (t.description) chip.title = t.description;
      chip.addEventListener('click', function(){
        if (selected[t.name]) { delete selected[t.name]; chip.classList.remove('on'); }
        else { selected[t.name] = true; chip.classList.add('on'); }
        updateBtn();
      });
      wrap.appendChild(chip);
    });
  }).catch(function(err){ wrap.textContent = 'Failed to load tools: ' + err.message; });
  btn.addEventListener('click', function(){
    var members = Object.keys(selected);
    if (!members.length) return;
    btn.disabled = true;
    status.textContent = 'Asking the LLM to name + describe…';
    fetch('api/tool-groups/auto-create', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({members: members})
    }).then(function(r){
      if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP '+r.status)); });
      return r.json();
    }).then(function(g){
      status.textContent = 'Created "' + g.name + '". Reloading…';
      setTimeout(function(){ window.location.reload(); }, 600);
    }).catch(function(err){
      status.textContent = 'Failed: ' + (err && err.message || err);
      btn.disabled = false;
    });
  });
})();
</script>`,
						},
						// Table of existing groups with per-row editor + delete.
						ui.Table{
							Source: "api/tool-groups",
							RowKey: "id",
							Columns: []ui.Col{
								{Field: "name", Flex: 1},
								{Field: "description", Flex: 2, Mute: true},
							},
							RowActions: []ui.RowAction{
								ui.Expand("Edit", ui.Stack{
									Children: []ui.Component{
										// Name + description edit. Loads the
										// group, POSTs the full record back.
										ui.FormPanel{
											Source:  "api/tool-groups/{id}",
											PostURL: "api/tool-groups",
											Method:  "POST",
											Fields: []ui.FormField{
												{Field: "name", Type: "text", Label: "Name",
													SuggestURL: "api/tool-groups/suggest"},
												{Field: "description", Type: "textarea", Label: "Description", Rows: 2,
													SuggestURL: "api/tool-groups/suggest"},
											},
										},
										// Members chip picker — toggleable
										// across the global tool registry.
										ui.ChipPicker{
											OptionsSource: "api/tool-groups/registry?exclude_grouped=true&except_group={id}",
											RecordSource:  "api/tool-groups/{id}",
											Field:         "members",
											PostTo:        "api/tool-groups",
											Method:        "POST",
											NameField:     "name",
											LabelField:    "name",
											DescField:     "description",
										},
									},
								}),
								// Admin-curated groups: Delete drops the row.
								{Type: "button", Label: "Delete",
									PostTo:  "api/tool-groups?id={id}",
									Method:  "DELETE",
									Confirm: "Delete this tool group? The member tools themselves are unaffected; only the grouping definition disappears.",
									Variant: "danger",
									HideIf:  "is_builtin"},
								// Framework-default groups: Revert drops the
								// admin shadow (if any) so the in-code default
								// surfaces again. No-op if no shadow has been
								// saved — the backend returns an error which
								// surfaces as a toast.
								{Type: "button", Label: "Revert",
									PostTo:  "api/tool-groups?id={id}",
									Method:  "DELETE",
									Confirm: "Revert this framework-default tool group to its in-code defaults? Any admin edits you made are discarded.",
									OnlyIf:  "is_builtin"},
							},
							EmptyText: "No tool groups defined. Create one above to start collapsing related tools into a single catalog entry.",
						},
					},
				},
			},
			{
				Title:    "Skills",
				Subtitle: "Domain packs the assistant draws on in its own context — instructions plus optional knowledge sources (attached collections and/or source-hooks). The LLM reaches a skill via read_skill (pull its approach), skill_knowledge_search (search its sources — collections + source-hooks merged) and skill_knowledge_fetch_doc. A skill with Triggers also auto-injects its instructions when they match the turn (e.g. *.pdf). No activation, no sub-agents — stateless calls. Builder is the canonical authoring path; this surface manages what's authored. Disabled skills are hidden from the LLM.",
				Body: ui.Table{
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
						{Type: "button", Label: "Delete",
							PostTo:  "api/skills?id={id}",
							Method:  "DELETE",
							Confirm: "Delete this skill? The definition is gone for good; Builder will need to re-author if you want it back.",
							Variant: "danger"},
					},
					EmptyText: "No skills defined. Talk to Builder in Agency to author one — \"create a skill called X that fires when…\".",
				},
			},
			{
				Title:    "Pipelines",
				Subtitle: "Declarative multi-stage workflows authored in Agency (the pipeline tool, or via Builder). This surface lists every user's pipelines and lets you inspect the stages or delete a definition. Deleting one also drops it from any agent it was attached to.",
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
						{Type: "button", Label: "Delete",
							PostTo:     "api/pipelines?id={id}",
							Method:     "DELETE",
							Confirm:    "Delete this pipeline definition? It's removed for the owning user and detached from any agent that used it. Authoring it again means re-creating the stages.",
							Variant:    "danger",
							Optimistic: true},
					},
					EmptyText: "No pipelines defined. They're authored in Agency via the pipeline tool or Builder.",
				},
			},
			{
				Title:    "Local Model Scheduler",
				Subtitle: "Concurrent-request caps for local LLM backends. Default 1 (strict serial). Raise only when the backend supports parallel requests. Requires restart to apply.",
				Body: ui.FormPanel{
					Source: "api/local-scheduler",
					Fields: []ui.FormField{
						{Field: "ollama_max_parallel", Label: "Ollama max parallel", Type: "number",
							Min: 1, Max: 16},
						{Field: "llamacpp_max_parallel", Label: "llama.cpp max parallel", Type: "number",
							Min: 1, Max: 16},
					},
				},
			},
			{
				Title:    "Maintenance",
				Subtitle: "One-shot operations that fix stale state or rebuild derived data. Each runs in the background and reports the number of records touched.",
				Body: ui.ActionList{
					Source:     "api/maintenance",
					LabelField: "Label",
					DescField:  "Desc",
					PostTo:     "api/maintenance?key={Label}",
					Method:     "POST",
					ButtonText: "Run",
					EmptyText:  "No maintenance functions registered.",
				},
			},
			{
				Title:     "Migrations",
				Subtitle:  "Schema / data migrations the apps have run on this deployment. Auto-fire on app init when triggered (no manual button) and never run twice for the same (app, name, owner). An error column indicates a panic during the run — clear the marker in the DB to retry after a fix.",
				Collapsed: true,
				Body: ui.Table{
					Source: "api/migrations",
					RowKey: "key",
					Columns: []ui.Col{
						{Field: "app", Label: "App", Flex: 1},
						{Field: "name", Label: "Migration", Flex: 2},
						{Field: "owner", Label: "Owner", Mute: true},
						{Field: "ran_at", Label: "Ran", Format: "reltime", Mute: true},
						{Field: "changed", Label: "Changed", Mute: true},
						{Field: "error", Label: "Error", Mute: true},
					},
					EmptyText: "No migrations have run on this deployment yet.",
				},
			},
			{
				Title:    "Vector Index",
				Subtitle: "Snapshot of the semantic-search index. Chunks are written automatically as records (research / debate / answer) are produced.",
				Body: ui.DisplayPanel{
					Source: "api/vector-stats",
					Pairs: []ui.DisplayPair{
						{Label: "Total chunks", Field: "total"},
						{Label: "Embedded", Field: "embedded"},
						{Label: "Empty (embed failed)", Field: "empty"},
						{Label: "By source", Field: "by_source_text", Mono: true},
					},
				},
			},
			{
				Title:     "Database Browser",
				Subtitle:  "Read-only view of the server database. Click a table to list its keys, click a key to inspect the record.",
				Collapsed: true,
				Body:      databaseBrowserCard(),
			},
		},
	}
	page.ServeHTTP(w, r)
}
