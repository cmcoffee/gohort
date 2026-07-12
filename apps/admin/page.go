// New admin page — framework-rendered with the simple sections migrated
// to core/ui's declarative components. Sections that need new framework
// primitives (DB Browser tree, Cost Rates split chart, Routing
// per-row select+number table, Watchers/Tools/Tasks edit dialogs) still
// live at /admin/legacy until each gets ported.

package admin

import (
	"net/http"
	"net/url"
	"regexp"
	"sort"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// boolOffSuffixRE matches a trailing "(0 = off)"-style parenthetical. On a bool
// tunable that renders as a toggle, the 0/1 convention is redundant noise, so the
// admin UI strips it from the label. Left intact on number knobs, where "0 = off"
// is a meaningful sentinel the operator needs to know.
var boolOffSuffixRE = regexp.MustCompile(`(?i)\s*\(\s*0\s*=\s*off\s*\)\s*$`)

// toggleLabel strips the redundant 0=off suffix from a bool tunable's label.
func toggleLabel(label string) string { return boolOffSuffixRE.ReplaceAllString(label, "") }

// themePickerOptions builds the Theme dropdown from the core/ui theme
// registry, so a newly-registered theme appears here with no edit.
func themePickerOptions() []ui.SelectOption {
	opts := make([]ui.SelectOption, 0)
	for _, t := range ui.Themes() {
		opts = append(opts, ui.SelectOption{Value: t.Name, Label: t.Label})
	}
	return opts
}

// buildTunableSections renders the operator tunable registry as one admin
// FormPanel section per category, each with a per-category "Revert to
// defaults". Field type, bounds, and help come straight from each TunableSpec,
// so the admin UI never grows by hand as knobs are registered.
func buildTunableSections() []ui.Section {
	var order []string
	byCat := map[string][]ui.FormField{}
	for _, s := range AllTunableSpecs() {
		if _, seen := byCat[s.Category]; !seen {
			order = append(order, s.Category)
		}
		// Bool knobs render as a real toggle, not a 0/1 number field.
		if s.Kind == KindBool {
			byCat[s.Category] = append(byCat[s.Category], ui.FormField{
				Field: s.Key,
				Label: toggleLabel(s.Label),
				Type:  "toggle",
				Help:  s.Help,
			})
			continue
		}
		unit := ""
		switch s.Kind {
		case KindSeconds:
			unit = " (seconds)"
		case KindMinutes:
			unit = " (minutes)"
		case KindHours:
			unit = " (hours)"
		case KindDays:
			unit = " (days)"
		}
		byCat[s.Category] = append(byCat[s.Category], ui.FormField{
			Field:    s.Key,
			Label:    s.Label + unit,
			Type:     "number",
			Help:     s.Help,
			Min:      int(s.Min),
			Max:      int(s.Max),
			Decimals: s.Decimals,
		})
	}
	sections := make([]ui.Section, 0, len(order))
	for _, cat := range order {
		sections = append(sections, ui.Section{
			Title:    cat,
			Group:    "Tuning",
			Subtitle: "Operator knobs, saved automatically as you edit. Use Revert to defaults to clear overrides.",
			Body: ui.FormPanel{
				Source:       "api/settings",
				ResetURL:     "api/settings/reset-tunables?category=" + url.QueryEscape(cat),
				ResetLabel:   "Revert to defaults",
				ResetConfirm: "Revert the " + cat + " settings to their built-in defaults?",
				Fields:       byCat[cat],
			},
		})
	}
	return sections
}

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
		{Field: "cost_per_call", Label: "Cost per call ($)", Type: "number", Decimals: 6, Min: 0, Help: "Optional. Dollar cost of one real (cache-miss) call to this hook, for the Costs tab chart + per-source breakdown. 0 = untracked (free endpoint)."},
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
		{Field: "allowed_url_pattern", Label: "Allowed URL pattern (legacy)", Placeholder: "https://api.github.com/**", Help: "Legacy single-glob alternative to Base URL + Allowed Endpoints. Leave blank when using those; one of the two is required. Requests not matching are rejected before the secret is attached."},
		{Field: "insecure_skip_tls", Label: "Allow self-signed / skip TLS verification", Type: "toggle", Help: "Turn ON only for LAN appliances with self-signed certs or hosts addressed by IP (e.g. an OPNsense box at https://192.168.0.1, where no cert can validate). Disables certificate checking for THIS credential's requests only. Leave OFF for public internet APIs."},
		{Field: "denied_url_patterns", Label: "Denied URL patterns", Type: "tags", Help: "Optional explicit denies, checked before the allow pattern."},
		{Field: "allowed_methods", Label: "Allowed methods", Type: "tags", Help: "e.g. GET, POST. Blank = all methods allowed."},
		{Field: "max_calls_per_day", Label: "Max calls / day", Type: "number", Min: 0, Help: "0 = unlimited."},
		{Field: "cost_per_call", Label: "Cost per call ($)", Type: "number", Decimals: 6, Min: 0, Help: "Optional. Dollar cost of one dispatched call through this credential, for the Costs tab chart + per-source breakdown. 0 = untracked (free endpoint)."},
		{Field: "requires_confirm", Label: "Require confirm before each call", Type: "toggle"},
		{Field: "cred_scope", Label: "Whose credentials", Type: "select",
			Options: []ui.SelectOption{
				{Value: "shared", Label: "Shared — one key for everyone (you set it here)"},
				{Value: "per_user", Label: "Per user — each user sets their own key (on their Account page)"},
			},
			Help: "Shared: this credential's secret (below) is used for every user's calls — a service account / shared key. Per user: leave the secret blank here; each user supplies their OWN key on their Account page, and calls run as that user. Use per-user when writes need real attribution + per-user permissions."},
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
		{Field: "oauth_client_id", Label: "Client ID (only if no auto-registration)", ShowWhen: "auth_mode:oauth", Help: "Leave BLANK for the normal flow: gohort auto-registers a client (Dynamic Client Registration). Fill this ONLY when the provider doesn't support auto-registration — pre-register an OAuth app at the provider, set its redirect URI to <this host>/admin/api/mcp-servers/oauth/callback, and paste the issued client_id here."},
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
		// Head registers the per-row "Reset password" modal client action.
		// Migrated off a hand-written <script> blob onto the typed ui.Head
		// builder — the framework assembles the <script>, the register call,
		// and the readiness guard.
		Head: ui.NewHead().
			CSS(adminUsersCSS).
			JS(adminUsersModalJS).
			JS(artifactDownloadHelper).
			ClientAction("admin_reset_password", adminResetPasswordAction).
			ClientAction("connectors_export", connectorsExportAction).
			ClientAction("connectors_export_all", connectorsExportAllAction).
			ClientAction("tools_export", toolsExportAction).
			ClientAction("tools_export_all", toolsExportAllAction).
			ClientAction("credentials_export", credentialsExportAction).
			ClientAction("credentials_export_all", credentialsExportAllAction).
			ClientAction("artifacts_export_all", artifactsExportAllAction),
		MaxWidth: "1200px", // desktop admin: wide enough for full-width tables in a single column
		Grid:     false,    // single column: sections stack vertically within each tab (Wide flags become no-ops)
		Tabbed:   true,     // category tab bar across the top (the multiple menus); sections grouped below
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
						{Field: "ui_theme", Label: "Theme", Type: "select",
							Options: themePickerOptions(),
							Help:    "Platform-wide UI theme. Reload the page after saving to see it."},
						{Field: "service_name", Label: "Service name", Type: "text",
							Placeholder: "gohort", Help: "Shown in the page title and email From: line."},
						{Field: "doc_brand", Label: "Document brand", Type: "text",
							Placeholder: "e.g. SnugLab Research",
							Help:        "Header label on exported documents (guide PDF/HTML) and the PDF branding line. Falls back to the site name."},
						{Field: "site_name", Label: "Site name", Type: "text",
							Placeholder: "e.g. SnugLab", Help: "Shown in exported-document footers."},
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
				Title:    "Channel Wake Rules",
				Subtitle: "Master gatekeeper applied to every channel before an inbound message wakes its agent. One rule per line; rules are OR'd (a message that matches ANY rule wakes the agent). These merge on top of each channel's own per-channel rules (set in the channel rail). Leave blank to apply no global rule.",
				Body: ui.FormPanel{
					Source: "api/settings",
					Fields: []ui.FormField{
						{Field: "channel_wake_rules", Label: "Master rules", Type: "textarea", Rows: 5,
							Placeholder: "Respond only when called by name\nAlways respond to a direct 1:1 message",
							Help:        "A cheap worker-LLM check runs these on each inbound. Follow-ups to the agent's own last message bypass the rules; owner messages are evaluated like anyone else's unless a rule says otherwise."},
					},
				},
			},
			{
				Title:    "Add account",
				Subtitle: "Invite a new user by email (they click a link and set their own password), or set a password directly. To reset an existing user's password, use the Reset password button on their row below.",
				Body:     ui.Card{HTML: userAdminHTML},
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
						// Reset password — per-row modal (set a new one, or send a
						// reset link). "client" so the modal can show the returned
						// link for manual copy when mail isn't configured.
						{Type: "button", Label: "Reset password", Method: "client",
							PostTo: "admin_reset_password", Compact: true, HideIf: "pending"},
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
						// App groups — assign whole bundles of apps at once.
						// Value stored is the group ID; access resolves the group
						// to its apps at check time. Sits alongside Apps: a user's
						// access is the union of both.
						func() ui.RowAction {
							a := ui.Expand("Groups", ui.ChipPicker{
								OptionsSource: "api/app-groups",
								RecordSource:  "api/users/{username}",
								Field:         "groups",
								PostTo:        "api/users/{username}/groups",
								Method:        "PUT",
								NameField:     "id",   // value stored in user.groups[]
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
				Title:    "App Groups",
				Subtitle: "Bundle apps under one name (e.g. \"Writers\", \"Ops\"), then assign a whole group to a user from the Groups picker above — access resolves the group to its apps, so editing a group instantly re-provisions everyone assigned to it.",
				Body: ui.Stack{
					Children: []ui.Component{
						// Create a new group (name + optional description). The
						// per-row editor below fills in its apps.
						ui.FormPanel{
							PostURL:     "api/app-groups",
							Method:      "POST",
							SubmitLabel: "Create group",
							Fields: []ui.FormField{
								{Field: "name", Type: "text", Label: "Name", Placeholder: "e.g. Writers"},
								{Field: "description", Type: "text", Label: "Description", Placeholder: "Optional note"},
							},
							Invalidate: []string{"api/app-groups"},
						},
						// Existing groups: per-row editor (name/description + the
						// apps chip picker) and delete.
						ui.Table{
							Source: "api/app-groups",
							RowKey: "id",
							Columns: []ui.Col{
								{Field: "name", Flex: 1},
								{Field: "description", Flex: 2, Mute: true},
							},
							RowActions: []ui.RowAction{
								ui.Expand("Edit", ui.Stack{
									Children: []ui.Component{
										ui.FormPanel{
											Source:  "api/app-groups/{id}",
											PostURL: "api/app-groups",
											Method:  "POST",
											Fields: []ui.FormField{
												{Field: "name", Type: "text", Label: "Name"},
												{Field: "description", Type: "text", Label: "Description"},
											},
										},
										// Which apps this group grants.
										ui.ChipPicker{
											OptionsSource: "api/apps",
											RecordSource:  "api/app-groups/{id}",
											Field:         "apps",
											PostTo:        "api/app-groups",
											Method:        "POST",
											NameField:     "path",
											LabelField:    "name",
										},
									},
								}),
								{Type: "button", Label: "Delete",
									PostTo:  "api/app-groups?id={id}",
									Method:  "DELETE",
									Confirm: "Delete this app group? Users assigned to it lose the apps it granted (unless they also have them via another grant).",
									Variant: "danger"},
							},
							EmptyText: "No app groups yet. Create one above, then add its apps and assign it to users.",
						},
					},
				},
			},
			{
				Title:    "Worker LLM",
				Subtitle: "The primary / local model most work runs on. Applies immediately on save (the live LLM is rebuilt — no restart). API key is stored encrypted; leave it blank to keep the current one.",
				Body: ui.FormPanel{
					Source: "api/worker-llm",
					Fields: []ui.FormField{
						{Field: "provider", Label: "Provider", Type: "select", Options: []ui.SelectOption{
							{Value: "ollama", Label: "Ollama"}, {Value: "llama.cpp", Label: "llama.cpp"},
							{Value: "anthropic", Label: "Anthropic"}, {Value: "openai", Label: "OpenAI"},
							{Value: "gemini", Label: "Gemini"}},
							Help: "Local providers (ollama / llama.cpp) are the usual worker."},
						{Field: "model", Label: "Model", Type: "text", Placeholder: "e.g. qwen3.6-27b", Help: "Blank = provider default."},
						{Field: "api_key", Label: "API key", Type: "password", Placeholder: "(leave blank to keep current)",
							Help: "Stored encrypted. Not needed for local ollama / llama.cpp."},
						{Field: "endpoint", Label: "Endpoint", Type: "text", Placeholder: "http://localhost:8080/v1",
							Help: "For local / self-hosted providers; blank = provider default.",
							Presets: []ui.FieldPreset{
								{Label: "Ollama", Value: "http://localhost:11434"},
								{Label: "llama.cpp", Value: "http://localhost:8080/v1"}}},
						{Field: "context_size", Label: "Context size (tokens)", Type: "number", Min: 0, Max: 262144,
							Help: "0 = default (65K for ollama / llama.cpp)."},
						{Field: "request_timeout_seconds", Label: "Request timeout (sec)", Type: "number", Min: 0, Max: 3600,
							Help: "0 = default 300s."},
						{Field: "native_tools", Label: "Native tool calling", Type: "toggle",
							Help: "Disable for models without tool-calling support (ollama)."},
						{Type: "header", Label: "Thinking", Collapsed: true},
						{Field: "disable_thinking", Label: "Disable thinking (force think=false)", Type: "toggle"},
						{Field: "thinking_budget", Label: "Thinking budget (tokens, 0 = unlimited)", Type: "number", Min: 0, Max: 131072,
							Help: "Also the hard ceiling for per-agent / per-route budgets. Default 4096."},
						{Field: "no_think_use_kwarg", Label: "No-think: send enable_thinking=false", Type: "toggle"},
						{Field: "no_think_send_budget", Label: "No-think: send thinking_budget cap", Type: "toggle"},
						{Field: "no_think_budget", Label: "No-think: budget value (tokens)", Type: "number", Min: 0, Max: 8192,
							Help: "0 = built-in default (512)."},
						{Field: "no_think_prepend_system", Label: "No-think: prepend /no_think to system prompt", Type: "toggle"},
						{Field: "no_think_prepend_user", Label: "No-think: prepend /no_think to last user message", Type: "toggle"},
					},
				},
			},
			{
				Title:    "Lead LLM",
				Subtitle: "The precision / remote model for high-stakes stages (routing sends \"lead\" stages here). Provider \"(use primary)\" reuses the worker. Applies immediately on save (no restart); key stored encrypted, blank keeps current.",
				Body: ui.FormPanel{
					Source: "api/lead-llm",
					Fields: []ui.FormField{
						{Field: "provider", Label: "Provider", Type: "select", Options: []ui.SelectOption{
							{Value: "", Label: "(use primary)"},
							{Value: "anthropic", Label: "Anthropic"}, {Value: "openai", Label: "OpenAI"},
							{Value: "gemini", Label: "Gemini"}, {Value: "ollama", Label: "Ollama"},
							{Value: "llama.cpp", Label: "llama.cpp"}},
							Help: "(use primary) routes lead stages to the worker model."},
						{Field: "model", Label: "Model", Type: "text", Placeholder: "e.g. claude-sonnet-5"},
						{Field: "api_key", Label: "API key", Type: "password", Placeholder: "(leave blank to keep current)",
							Help: "Stored encrypted. Blank reuses the primary provider's key where applicable."},
						{Field: "endpoint", Label: "Endpoint", Type: "text", Placeholder: "(provider default)",
							Help: "For local / self-hosted lead providers."},
						{Field: "native_tools", Label: "Native tool calling", Type: "toggle",
							Help: "Disable for models without tool-calling support (ollama)."},
						{Type: "header", Label: "Thinking", Collapsed: true},
						{Field: "disable_thinking", Label: "Disable thinking (force think=false)", Type: "toggle"},
						{Field: "thinking_budget", Label: "Thinking budget (tokens, 0 = unlimited)", Type: "number", Min: 0, Max: 131072},
						{Field: "no_think_use_kwarg", Label: "No-think: send enable_thinking=false", Type: "toggle"},
						{Field: "no_think_send_budget", Label: "No-think: send thinking_budget cap", Type: "toggle"},
						{Field: "no_think_budget", Label: "No-think: budget value (tokens)", Type: "number", Min: 0, Max: 8192},
						{Field: "no_think_prepend_system", Label: "No-think: prepend /no_think to system prompt", Type: "toggle"},
						{Field: "no_think_prepend_user", Label: "No-think: prepend /no_think to last user message", Type: "toggle"},
					},
				},
			},
			{
				Title:    "LLM Routing",
				Subtitle: "Pick which tier handles each pipeline stage. \"lead\" uses the precision (remote) LLM. \"worker\" uses the local model. \"worker (thinking)\" enables extended reasoning on the local model. Budget caps thinking tokens for that stage (0 = stage default). Private stages cannot route to lead.",
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
			// "Worker LLM Thinking" folded into the new "Worker LLM" section above
			// (its thinking + no-think controls now live in that section's
			// collapsible Thinking group). The api/worker-thinking endpoint is
			// retained for compatibility but no longer surfaced here.
			{
				Title:    "Cost History (Last 30 Days)",
				Subtitle: "Daily LLM + search spend across all pipelines. Hover any bar for the per-day breakdown of runs, tokens, searches, and images.",
				Body: ui.BarChart{
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
			},
			{
				Title:    "Cost by source",
				Subtitle: "Metered source-hook + credential spend over the last 30 days (a \"cost hook\" per source). Set a per-call cost on a source hook or API credential to track it here; it also folds into the chart total above.",
				Body: ui.Table{
					Source:    "api/cost-by-source?days=30",
					RowKey:    "source_id",
					EmptyText: "No metered external calls recorded yet. Set a \"Cost per call\" on a source hook or API credential to track its spend here.",
					Columns: []ui.Col{
						{Field: "label", Label: "Source", Flex: 2},
						{Field: "calls", Label: "Calls", Format: "thousands", Flex: 1},
						{Field: "cost", Label: "Cost ($)", Flex: 1},
					},
				},
			},
			{
				Title:    "Prices",
				Subtitle: "Per-token and per-call dollar rates that feed the dollar estimate above. Worker = local LLM, Lead = remote LLM. Set to 0 for free tiers. Saved automatically as you edit.",
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
				Title:    "System Dependencies",
				Subtitle: "External tools gohort shells out to for media + document handling. A missing one disables the feature it gates (e.g. no ffmpeg → inbound voice memos can't be transcribed). Date-versioned tools like yt-dlp are flagged when they go stale (its extractors rot fast, so a stale yt-dlp silently breaks video downloads). Install or update on the gohort host and restart; this list refreshes on reload.",
				Body: ui.Table{
					Source: "api/dependencies",
					RowKey: "name",
					Columns: []ui.Col{
						{Field: "name", Label: "Tool", Flex: 1},
						{Field: "present", Label: "Status", Type: "badge", Badges: []ui.BadgeMapping{
							{Value: true, Label: "Installed", Color: "success"},
							{Value: false, Label: "Missing", Color: "danger"},
						}},
						{Field: "version", Label: "Version", Flex: 1, Mute: true},
						{Field: "stale", Label: "Freshness", Type: "badge", Badges: []ui.BadgeMapping{
							{Value: true, Label: "Stale, update", Color: "warning"},
						}},
						{Field: "enables", Label: "Enables", Flex: 3, Mute: true},
						{Field: "install", Label: "Install", Flex: 2, Mute: true},
					},
					EmptyText: "No dependency information.",
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
						// What the task actually is (e.g. an event monitor's name +
						// the agent it wakes), from the task-describer registry.
						// Blank for kinds without a describer.
						{Field: "detail", Flex: 2},
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
										{Label: "Detail", Field: "detail"},
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
								// Open/Secure pair — Secure (formerly Restrict) reads
								// more naturally for "lock down to wrapped tools only".
								// Neutral for the same reason as Enable/Disable.
								{Type: "button", Label: "Open",
									PostTo: "api/secure-api?action=open&name={name}",
									Method: "POST", OnlyIf: "restricted"},
								{Type: "button", Label: "Secure",
									PostTo: "api/secure-api?action=restrict&name={name}",
									Method: "POST",
									HideIf: "restricted"},
								{Type: "button", Label: "Export", Method: "client",
									PostTo: "credentials_export", Compact: true},
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
						// Import — choose a gohort.bundle/v1 FILE. The unified
						// importer accepts ANY artifact bundle (connectors AND/OR
						// tools), a legacy connector pack, or a single artifact.
						// Everything reconstitutes as a DRAFT: connectors land
						// unapproved, tools land in the pending pool (see Tools).
						// A name that already exists is skipped, and no secret ever
						// travels — auth references a credential by name.
						ui.FormPanel{
							// No Source — this is a blank submit-only form.
							// A Source would trigger a GET prefill against the
							// import endpoint (POST-only) → 405, which renders
							// the whole panel as "Failed to load: method not
							// allowed".
							PostURL:     "api/artifacts/import",
							SubmitLabel: "Import artifacts",
							Fields: []ui.FormField{
								{Field: "pack", Label: "Artifact bundle file", Type: "file", Accept: ".json,application/json",
									Help: "Choose an exported bundle (.json) — connectors, tools, API credentials, and/or agents. Imported artifacts are drafted for review (connectors UNAPPROVED, tools PENDING, credentials inert until you add the secret); a name that already exists is skipped. No secrets travel."},
							},
						},
						// Export — per-row Export (in the table above) grabs one
						// connector; these buttons grab whole sets as one secret-free
						// gohort.bundle/v1. "Export everything" spans every artifact
						// type (connectors + tools + future types).
						ui.Toolbar{
							Actions: []ui.ToolbarAction{
								{Label: "Export all connectors", Method: "client", URL: "connectors_export_all"},
								{Label: "Export everything", Method: "client", URL: "artifacts_export_all"},
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
				Subtitle: "Approved tools the LLM gets in every session. Description shows what each one does. Share publishes a tool to ALL users (it loads for everyone's agents, on top of their own pool); Unshare pulls it back to its owner. Delete to revoke immediately. Export a tool (or all tools) as a portable bundle.",
				Body: ui.Stack{Children: []ui.Component{
					ui.Table{
						Source:       "api/persistent-tools",
						RecordsField: "active",
						RowKey:       "tool.name",
						Columns: []ui.Col{
							{Field: "tool.name", Flex: 1},
							{Field: "owner", Flex: 0, Mute: true},
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
					// Export all persistent tools (every owner) as one bundle.
					ui.Toolbar{
						Actions: []ui.ToolbarAction{
							{Label: "Export all tools", Method: "client", URL: "tools_export_all"},
						},
					},
				}},
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
			{
				Title:    "Local Model Scheduler",
				Subtitle: "Concurrent-request caps for local LLM backends. Default 1 (strict serial). Raise only when the backend supports parallel requests. Applies immediately on save (the live LLM is rebuilt).",
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
				Body: ui.Stack{Children: []ui.Component{
					ui.DisplayPanel{
						Source: "api/vector-stats",
						Pairs: []ui.DisplayPair{
							{Label: "Total chunks", Field: "total"},
							{Label: "Embedded", Field: "embedded"},
							{Label: "Empty (embed failed)", Field: "empty"},
						},
					},
					// Per-kind breakdown (documents + chunks) — the legible view,
					// vs the opaque per-source id dump it replaces.
					ui.Table{
						Source: "api/vector-stats/by-kind",
						RowKey: "kind",
						Columns: []ui.Col{
							{Field: "label", Label: "Source", Flex: 2},
							{Field: "documents", Label: "Documents"},
							{Field: "chunks", Label: "Chunks"},
						},
						EmptyText: "No chunks indexed yet.",
					},
				}},
			},
			{
				Title:     "Database Browser",
				Subtitle:  "Read-only view of the server database. Click a table to list its keys, click a key to inspect the record.",
				Collapsed: true,
				Body:      databaseBrowserCard(),
			},
		},
	}
	// Category for each section's top tab, and which sections span the
	// full grid width (tables, the cost chart, multi-pane Stacks, the DB
	// browser) vs the narrow config forms that pack two-up. Kept here in
	// one place so the section literals above stay uncluttered and the
	// layout reads at a glance. Tab order follows first appearance in the
	// Sections slice, so the order of these groups is set by section order.
	sectionGroup := map[string]string{
		"System Status": "System", "Site Settings": "System",
		"Users": "System", "Add account": "System", "Default Apps": "System",
		"App Groups": "System",

		"Cost History (Last 30 Days)": "Costs", "Cost by source": "Costs", "Prices": "Costs",

		"Worker LLM": "LLMs", "Lead LLM": "LLMs", "LLM Routing": "LLMs",
		"Ollama Proxy": "LLMs", "Agent Loop Tuning": "LLMs",
		"Local Model Scheduler": "LLMs",

		"Embeddings":                "Capabilities",
		"Audio Transcription (STT)": "Capabilities", "Image Generation": "Capabilities",
		"Web Search": "Capabilities", "Mail (SMTP)": "System",
		"Network Timeouts": "Tuning",

		"API Credentials": "Tools", "MCP Servers": "Tools", "Connectors": "Tools",
		"Source Hooks": "Tools", "Persistent Tools (Pending)": "Tools",
		"Persistent Tools (Active)": "Tools", "Tool Groups": "Tools",
		"Skills": "Tools", "Pipelines": "Tools",

		"Agent Capabilities — Outward & Spending": "Agents",

		"Scheduled Tasks": "Maintenance", "Maintenance": "Maintenance",
		"Migrations": "Maintenance", "Vector Index": "Maintenance",
		"Database Browser": "Maintenance",
	}
	wideSections := map[string]bool{
		"System Status": true, "Users": true, "LLM Routing": true,
		"Cost History (Last 30 Days)": true, "Cost by source": true, "Scheduled Tasks": true,
		"API Credentials": true, "MCP Servers": true, "Connectors": true, "Source Hooks": true,
		"Persistent Tools (Pending)": true, "Persistent Tools (Active)": true,
		"Tool Groups": true, "Skills": true, "Pipelines": true, "App Groups": true,
		"Migrations": true, "Database Browser": true,
		"Agent Capabilities — Outward & Spending": true,
	}
	// Generated tunable sections — one FormPanel per registered category, built
	// from core's tunable registry so a newly-registered knob appears here with
	// no admin edit. Pre-grouped under the "Tuning" tab; the loop below skips
	// them (their titles aren't in sectionGroup, so their Group is preserved).
	page.Sections = append(page.Sections, buildTunableSections()...)
	for i := range page.Sections {
		t := page.Sections[i].Title
		if g, ok := sectionGroup[t]; ok {
			page.Sections[i].Group = g
		}
		if wideSections[t] {
			page.Sections[i].Wide = true
		}
	}
	// Tab order (and clustering of each group's sections) — a stable sort
	// by this rank so the tabs read in a sensible order regardless of the
	// section authoring order above; sections keep their relative order
	// within each group.
	groupRank := map[string]int{"System": 0, "Costs": 1, "LLMs": 2, "Capabilities": 3, "Agents": 4, "Tools": 5, "Tuning": 6, "Maintenance": 7}
	sort.SliceStable(page.Sections, func(i, j int) bool {
		return groupRank[page.Sections[i].Group] < groupRank[page.Sections[j].Group]
	})
	page.ServeHTTP(w, r)
}

// userAdminHTML is the Add-account panel (top of the Users section). It must
// surface the invite LINK for manual copy when mail isn't configured — which
// the declarative FormPanel can't do — so it rides in a Card. Posts to
// api/users. Reset lives on each user's row (see admin_reset_password).
const userAdminHTML = `<div class="uadm">
  <input id="uadm-add-email" class="uadm-in" type="email" placeholder="email@example.com" autocomplete="off">
  <label class="uadm-chk"><input type="checkbox" id="uadm-add-admin"> Administrator</label>
  <div class="uadm-methods">
    <label><input type="radio" name="uadm-add-method" value="invite" checked> Send registration link</label>
    <label><input type="radio" name="uadm-add-method" value="password"> Set password now</label>
  </div>
  <input id="uadm-add-pw" class="uadm-in" type="password" placeholder="New password (6+ characters)" style="display:none" autocomplete="new-password">
  <div class="uadm-row"><button class="ui-row-btn primary" id="uadm-add-btn">Add account</button><span id="uadm-add-msg" class="uadm-msg"></span></div>
  <div id="uadm-add-link" class="uadm-link" style="display:none"></div>
</div>
<style>
.uadm { display:flex; flex-direction:column; gap:0.5rem; max-width:26rem; }
.uadm-in { background:var(--bg-0); color:var(--text); border:1px solid var(--border); border-radius:6px; padding:0.4rem 0.55rem; font:inherit; font-size:0.9rem; }
.uadm-chk { display:flex; align-items:center; gap:0.4rem; font-size:0.85rem; color:var(--text); }
.uadm-methods { display:flex; flex-direction:column; gap:0.25rem; font-size:0.85rem; color:var(--text-mute); }
.uadm-methods label { display:flex; align-items:center; gap:0.4rem; }
.uadm-row { display:flex; align-items:center; gap:0.6rem; margin-top:0.2rem; }
.uadm-msg { font-size:0.82rem; }
.uadm-msg.ok { color:var(--success); }
.uadm-msg.err { color:var(--danger); }
.uadm-link { border:1px solid var(--accent); border-radius:8px; padding:0.55rem 0.7rem; background:var(--bg-2); display:flex; flex-direction:column; gap:0.35rem; }
.uadm-link-lbl { font-size:0.78rem; color:var(--text-mute); }
.uadm-link code { font-family:ui-monospace,SFMono-Regular,Menlo,monospace; font-size:0.78rem; color:var(--text); word-break:break-all; }
.uadm-link button { align-self:flex-start; }
</style>
<script>
(function(){
  var root = document.querySelector('.uadm');
  if(!root) return;
  function $(id){ return document.getElementById(id); }
  function setMsg(el, t, ok){ el.textContent=t||''; el.className='uadm-msg '+(ok?'ok':'err'); }
  function showLink(box, link, emailed){
    box.innerHTML=''; box.style.display='';
    var lbl=document.createElement('div'); lbl.className='uadm-link-lbl';
    lbl.textContent = emailed ? 'Emailed to the user. Link (copy if needed):' : 'Mail is not configured — copy this link and send it to the user:';
    var code=document.createElement('code'); code.textContent=link;
    var copy=document.createElement('button'); copy.className='ui-row-btn'; copy.textContent='Copy';
    copy.addEventListener('click', function(){ if(navigator.clipboard) navigator.clipboard.writeText(link); copy.textContent='Copied'; setTimeout(function(){ copy.textContent='Copy'; }, 1200); });
    box.appendChild(lbl); box.appendChild(code); box.appendChild(copy);
  }
  var rbs=document.querySelectorAll('input[name="uadm-add-method"]');
  for(var i=0;i<rbs.length;i++){ rbs[i].addEventListener('change', function(){ var s=document.querySelector('input[name="uadm-add-method"]:checked'); $('uadm-add-pw').style.display=(s&&s.value==='password')?'':'none'; }); }
  $('uadm-add-btn').addEventListener('click', function(){
    var email=$('uadm-add-email').value.trim(), msg=$('uadm-add-msg'), linkbox=$('uadm-add-link');
    linkbox.style.display='none'; setMsg(msg,'',true);
    if(!email){ setMsg(msg,'Enter an email.',false); return; }
    var method=(document.querySelector('input[name="uadm-add-method"]:checked')||{}).value;
    var body={username:email, admin:$('uadm-add-admin').checked, invite: method==='invite'};
    if(method==='password'){ var pw=$('uadm-add-pw').value; if(pw.length<6){ setMsg(msg,'Password must be at least 6 characters.',false); return; } body.password=pw; }
    var btn=$('uadm-add-btn'); btn.disabled=true; var orig=btn.textContent; btn.textContent='Working…';
    fetch('api/users',{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)})
      .then(function(r){ return r.ok ? r.json() : r.text().then(function(t){ throw new Error(t||('HTTP '+r.status)); }); })
      .then(function(d){
        btn.disabled=false; btn.textContent=orig; $('uadm-add-email').value=''; $('uadm-add-pw').value='';
        if(window.uiInvalidate) window.uiInvalidate('api/users');
        if(d.status==='invited'){ setMsg(msg,'Invite created for '+email+'.',true); showLink(linkbox,d.link,d.emailed); }
        else { setMsg(msg,'Account created for '+email+'.',true); }
      })
      .catch(function(e){ btn.disabled=false; btn.textContent=orig; setMsg(msg,'Failed: '+(e&&e.message||e),false); });
  });
})();
</script>`

// The per-row "Reset password" modal, split for the typed ui.Head builder:
// adminUsersCSS (styles), adminUsersModalJS (el + openModal helpers), and
// adminResetPasswordAction (the client-action handler the Users table row
// button dispatches to by name). See serveNewAdminPage for the wire-up.
const adminUsersCSS = `
.admu-overlay { position:fixed; inset:0; background:rgba(0,0,0,0.45); display:flex; align-items:center; justify-content:center; z-index:1000; }
.admu-card { background:var(--bg-1); border:1px solid var(--border); border-radius:10px; padding:1rem 1.1rem; width:min(30rem,92vw); display:flex; flex-direction:column; gap:0.55rem; }
.admu-title { font-weight:600; color:var(--text-hi); }
.admu-opt { display:flex; align-items:center; gap:0.4rem; font-size:0.9rem; color:var(--text); }
.admu-in { background:var(--bg-0); color:var(--text); border:1px solid var(--border); border-radius:6px; padding:0.4rem 0.55rem; font:inherit; }
.admu-row { display:flex; gap:0.5rem; margin-top:0.3rem; }
.admu-msg { font-size:0.82rem; }
.admu-msg.ok { color:var(--success); }
.admu-msg.err { color:var(--danger); }
.admu-link { border:1px solid var(--accent); border-radius:8px; padding:0.5rem 0.65rem; background:var(--bg-2); display:flex; flex-direction:column; gap:0.35rem; }
.admu-link code { font-family:ui-monospace,Menlo,monospace; font-size:0.78rem; color:var(--text); word-break:break-all; }
.admu-link button { align-self:flex-start; }`

// adminUsersModalJS defines the shared helpers (el, openModal) for the reset-
// password action. Emitted inside the ui.Head init block; function declarations
// hoist, so adminResetPasswordAction can call openModal.
const adminUsersModalJS = `function el(tag, attrs, kids){ var n=document.createElement(tag); if(attrs) for(var k in attrs){ if(k==='text') n.textContent=attrs[k]; else if(k==='class') n.className=attrs[k]; else n.setAttribute(k,attrs[k]); } (kids||[]).forEach(function(c){ n.appendChild(typeof c==='string'?document.createTextNode(c):c); }); return n; }
  function openModal(user, ctx){
    var overlay = el('div', {class:'admu-overlay'});
    var pw = el('input', {type:'password', class:'admu-in', placeholder:'New password (6+ characters)', autocomplete:'new-password'});
    pw.style.display='none';
    var rLink = el('input', {type:'radio', name:'admu-mode', value:'link'}); rLink.checked=true;
    var rSet = el('input', {type:'radio', name:'admu-mode', value:'set'});
    function sync(){ pw.style.display = rSet.checked ? '' : 'none'; }
    rLink.addEventListener('change', sync); rSet.addEventListener('change', sync);
    var msg = el('div', {class:'admu-msg'});
    var linkBox = el('div', {class:'admu-link'}); linkBox.style.display='none';
    var doBtn = el('button', {class:'ui-row-btn primary'}, ['Reset']);
    var cancel = el('button', {class:'ui-row-btn'}, ['Close']);
    cancel.addEventListener('click', function(){ overlay.remove(); });
    doBtn.addEventListener('click', function(){
      var mode = rSet.checked ? 'set' : 'link';
      var body = {mode:mode};
      msg.textContent=''; msg.className='admu-msg';
      if(mode==='set'){ if(pw.value.length<6){ msg.textContent='Password must be at least 6 characters.'; msg.className='admu-msg err'; return; } body.password=pw.value; }
      doBtn.disabled=true; var o=doBtn.textContent; doBtn.textContent='Working…';
      fetch('api/users/'+encodeURIComponent(user)+'/reset-password', {method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'}, body:JSON.stringify(body)})
        .then(function(r){ return r.ok ? r.json() : r.text().then(function(t){ throw new Error(t||('HTTP '+r.status)); }); })
        .then(function(d){
          doBtn.disabled=false; doBtn.textContent=o;
          if(d.status==='password_set'){ overlay.remove(); if(ctx&&ctx.reload) ctx.reload(); (window.uiAlert||window.alert)('Password updated for '+user+'.'); return; }
          msg.textContent = d.emailed ? ('Reset link emailed to '+user+'.') : 'Mail not configured — copy this link:'; msg.className='admu-msg ok';
          linkBox.innerHTML=''; linkBox.style.display='';
          var code=el('code',{text:d.link}); var cp=el('button',{class:'ui-row-btn'},['Copy']);
          cp.addEventListener('click', function(){ if(navigator.clipboard) navigator.clipboard.writeText(d.link); cp.textContent='Copied'; setTimeout(function(){ cp.textContent='Copy'; },1200); });
          linkBox.appendChild(code); linkBox.appendChild(cp);
        })
        .catch(function(e){ doBtn.disabled=false; doBtn.textContent=o; msg.textContent='Failed: '+(e&&e.message||e); msg.className='admu-msg err'; });
    });
    var card = el('div', {class:'admu-card'}, [
      el('div', {class:'admu-title'}, ['Reset password — '+user]),
      el('label', {class:'admu-opt'}, [rLink, ' Send reset link']),
      el('label', {class:'admu-opt'}, [rSet, ' Set new password']),
      pw,
      el('div', {class:'admu-row'}, [doBtn, cancel]),
      msg, linkBox
    ]);
    overlay.appendChild(card);
    // No backdrop-click-to-close (a drag-select copy ending on the backdrop
    // would dismiss mid-copy); the Close button dismisses.
    document.body.appendChild(overlay);
  }`

// adminResetPasswordAction is the client-action handler registered under
// "admin_reset_password"; the Users table row button dispatches to it by name.
// It calls openModal (defined in adminUsersModalJS).
const adminResetPasswordAction = `function(ctx){
  var u = ctx && ctx.record && (ctx.record.username || ctx.record.id);
  if(u) openModal(u, ctx);
}`

// artifactDownload is the shared client-action body: it navigates a hidden
// anchor at the unified artifact-export endpoint so the browser downloads a
// secret-free gohort.bundle/v1. All the export buttons below are one-liners
// over it, differing only in the query (individual vs all-of-type vs all).
const artifactDownloadHelper = `function __artifactDownload(href, filename){
  var a = document.createElement('a');
  a.href = href; a.download = filename;
  document.body.appendChild(a); a.click(); a.remove();
}`

// connectorsExportAction downloads ONE connector as a 1-item bundle. Dispatched
// by the per-row "Export" button; reads the row's name.
const connectorsExportAction = `function(ctx){
  var n = ctx && ctx.record && ctx.record.name;
  if(!n){ window.uiAlert && window.uiAlert('No connector selected.'); return; }
  __artifactDownload('api/artifacts/export?type=connector&name=' + encodeURIComponent(n), n + '.gohort.json');
}`

// connectorsExportAllAction downloads every connector as one bundle.
const connectorsExportAllAction = `function(){
  __artifactDownload('api/artifacts/export?all=connector', 'connectors.gohort.json');
}`

// toolsExportAction downloads ONE persistent tool as a 1-item bundle. The
// persistent-tools row nests the definition under .tool and carries the owning
// user in .owner (tools are per-user), so export needs both.
const toolsExportAction = `function(ctx){
  var r = (ctx && ctx.record) || {};
  var n = (r.tool && r.tool.name) || r.name;
  var o = r.owner || '';
  if(!n){ window.uiAlert && window.uiAlert('No tool selected.'); return; }
  __artifactDownload('api/artifacts/export?type=tool&name=' + encodeURIComponent(n) + '&owner=' + encodeURIComponent(o), n + '.gohort.json');
}`

// toolsExportAllAction downloads every persistent tool (all owners) as one bundle.
const toolsExportAllAction = `function(){
  __artifactDownload('api/artifacts/export?all=tool', 'tools.gohort.json');
}`

// artifactsExportAllAction downloads EVERYTHING — connectors + tools +
// credentials + agents + any future registered type — as one gohort.bundle/v1.
const artifactsExportAllAction = `function(){
  __artifactDownload('api/artifacts/export', 'gohort-bundle.json');
}`

// credentialsExportAction downloads ONE API credential's CONFIG as a 1-item
// bundle. No secret travels — it lives in a separate encrypted key and is never
// part of the recipe; the importer supplies it on their side.
const credentialsExportAction = `function(ctx){
  var n = ctx && ctx.record && ctx.record.name;
  if(!n){ window.uiAlert && window.uiAlert('No credential selected.'); return; }
  __artifactDownload('api/artifacts/export?type=credential&name=' + encodeURIComponent(n), n + '.gohort.json');
}`

// credentialsExportAllAction downloads every API credential's config as one bundle.
const credentialsExportAllAction = `function(){
  __artifactDownload('api/artifacts/export?all=credential', 'credentials.gohort.json');
}`
