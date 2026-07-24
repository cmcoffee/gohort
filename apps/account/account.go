// Package account is the per-user settings surface — every logged-in user's own
// page (NOT admin-gated, distinct from the admin Site Settings). It gives the
// previously-scattered per-user preferences a home, and is where per-user
// integrations (individual OAuth connections) get connected/disconnected once
// the per-credential Scope work lands. Reached from the dashboard header
// (Account link next to Logout), not a tile (WebHidden).
package account

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

func init() { RegisterApp(new(Account)) }

type Account struct {
	AppCore
}

func (T Account) Name() string         { return "account" }
func (T Account) SystemPrompt() string { return "" }
func (T Account) Desc() string         { return "Apps: your personal account + preferences." }
func (T *Account) Init() error         { return T.Flags.Parse() }
func (T *Account) Main() error {
	Log("account is a dashboard-only app. Start with: gohort serve")
	return nil
}

func (T *Account) WebPath() string { return "/account" }
func (T *Account) WebName() string { return "Account" }
func (T *Account) WebDesc() string { return "Your personal preferences + connected accounts." }

// WebHidden keeps Account off the app grid — it's reached from the dashboard
// header (the Account link next to Logout), not as an app tile competing with
// the real apps.
func (T *Account) WebHidden() bool { return true }

func (T *Account) Routes() {
	T.HandleFunc("/api/prefs", T.handlePrefs)
	T.HandleFunc("/api/password", T.handlePassword)
	// Connected-accounts (per-user OAuth/MCP) endpoints stay registered here for
	// redirect-URI stability even though the management card now renders under
	// the Gateways app (which calls these by absolute /account path).
	T.HandleFunc("/api/connections", T.handleConnections)
	T.HandleFunc("/api/tokens", T.handleTokens)
	T.HandleFunc("/api/token-targets", T.handleTokenTargets)
	T.HandleFunc("/oauth/start", T.handleOAuthStart)
	T.HandleFunc("/oauth/callback", T.handleOAuthCallback)
	T.HandleFunc("/mcp/connect", T.handleMCPConnect)
	T.HandleFunc("/mcp/callback", T.handleMCPCallback)
	T.HandleFunc("/", T.servePage)
}

// accountBaseURL is the absolute site base for OAuth redirect URIs. Prefers the
// admin External URL (must match what the admin registered with the provider);
// falls back to the request's scheme+host.
func accountBaseURL(r *http.Request) string {
	base := ""
	if db := AuthDB(); db != nil {
		var ext string
		db.Get(WebTable, "external_url", &ext)
		base = strings.TrimRight(strings.TrimSpace(ext), "/")
	}
	if base == "" {
		scheme := "https"
		if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") != "https" {
			scheme = "http"
		}
		base = scheme + "://" + r.Host
	}
	return base
}

// oauthCallbackURI is the absolute redirect URI the SecureAPI OAuth provider
// sends the user back to.
func oauthCallbackURI(r *http.Request) string { return accountBaseURL(r) + "/account/oauth/callback" }

// mcpConnectCallbackURI is the absolute redirect URI for the per-user MCP OAuth
// consent flow. This is the CANONICAL per-user MCP callback — the account panel
// and the inline chat Connect prompt both route through /account/mcp/connect, so
// the registered redirect_uri stays consistent regardless of entry point. (A
// provider with a pre-registered client must allow this path in its OAuth app.)
func mcpConnectCallbackURI(r *http.Request) string {
	return accountBaseURL(r) + "/account/mcp/callback"
}

// handleOAuthStart begins the consent flow for ?cred=<name>: mints state + PKCE
// and redirects the user to the provider's authorize page.
func (T *Account) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("cred"))
	c, found := Secure().Load(name)
	if !found || !c.IsPerUser() || !c.IsAuthCode() {
		http.Error(w, "no such OAuth integration", http.StatusNotFound)
		return
	}
	authURL, err := Secure().OAuthStart(c, user, oauthCallbackURI(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, authURL, http.StatusFound)
}

// handleOAuthCallback completes the flow: exchanges the code for the user's token
// and returns them to the Account page.
func (T *Account) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		http.Redirect(w, r, "/gateways/?oauth=denied", http.StatusFound)
		return
	}
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if _, _, err := Secure().OAuthCallback(r.Context(), state, code); err != nil {
		Log("[account] oauth callback failed: %v", err)
		http.Redirect(w, r, "/gateways/?oauth=failed", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/gateways/?oauth=connected", http.StatusFound)
}

// handleMCPConnect begins the per-user OAuth consent for a hosted MCP server
// (?server=<name>): it mints PKCE + state (registering the server-level OAuth
// client via discovery + DCR on first use) and 302-redirects the user to the
// provider's consent screen. Any logged-in user connects their OWN account — the
// token is scoped to them — so this is NOT admin-gated. Reached from both the
// Account panel and the inline chat Connect prompt.
func (T *Account) handleMCPConnect(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	server := strings.TrimSpace(r.URL.Query().Get("server"))
	if server == "" {
		http.Error(w, "server required", http.StatusBadRequest)
		return
	}
	cfg, found := MCP().Load(server)
	if !found || cfg.AuthMode != MCPAuthOAuth {
		http.Error(w, "no such OAuth MCP server", http.StatusNotFound)
		return
	}
	authURL, err := MCP().StartOAuth(user, server, mcpConnectCallbackURI(r))
	if err != nil {
		mcpConnectResultPage(w, "Could not start authorization for "+server+":\n\n"+err.Error())
		return
	}
	http.Redirect(w, r, authURL, http.StatusFound)
}

// handleMCPCallback redeems the MCP OAuth callback (state + code), stores the
// user's token, and brings up their per-user connection. The state is the
// unguessable secret tying the callback to the authorizing user, so this only
// needs a logged-in session. Renders a self-contained result page (this tab is
// typically a consent popup opened from chat or the Account panel).
func (T *Account) handleMCPCallback(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		mcpConnectResultPage(w, "Authorization was declined or failed: "+e+" "+q.Get("error_description"))
		return
	}
	code, state := q.Get("code"), q.Get("state")
	if code == "" || state == "" {
		mcpConnectResultPage(w, "Missing authorization code or state.")
		return
	}
	if err := MCP().CompleteOAuth(state, code); err != nil {
		mcpConnectResultPage(w, "Could not complete the connection:\n\n"+err.Error())
		return
	}
	mcpConnectResultPage(w, "Connected. You can close this tab and return to your conversation.")
}

// mcpConnectResultPage renders a minimal self-contained result page for the MCP
// consent popup (no proxied assets — this tab may be a fresh OAuth popup). On a
// successful connect it signals the opener so an inline chat prompt can mark
// itself connected, then closes.
func mcpConnectResultPage(w http.ResponseWriter, msg string) {
	connected := strings.HasPrefix(msg, "Connected")
	notify := ""
	if connected {
		notify = `<script>try{if(window.opener)window.opener.postMessage('gohort-mcp-connected','*');}catch(e){}setTimeout(function(){try{window.close();}catch(e){}},1200);</script>`
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>Connect</title>` +
		`<style>html,body{height:100%;margin:0}body{background:#0d1117;color:#c9d1d9;` +
		`font-family:-apple-system,system-ui,sans-serif;display:flex;align-items:center;justify-content:center}` +
		`.card{max-width:480px;width:90%;background:#161b22;border:1px solid #30363d;border-radius:10px;padding:28px;white-space:pre-wrap;line-height:1.5}</style>` +
		`</head><body><div class="card">` + htmlEscape(msg) + `</div>` + notify + `</body></html>`))
}

// htmlEscape is a tiny escaper for the result-page message.
func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// handleConnections GET lists the per-user (per_user-scoped) credentials the user
// can connect, each flagged connected/not; POST sets or clears the user's secret
// for one. The secret value is never returned — only connected status.
func (T *Account) handleConnections(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		// SecureAPI per-user credentials + per-user OAuth MCP servers, rendered
		// in one panel. MCP entries carry Kind="mcp" + a ConnectURL so the panel
		// routes their Connect/Disconnect to the MCP endpoints.
		conns := Secure().PerUserConnectionsFor(user)
		conns = append(conns, MCP().PerUserOAuthConnectionsFor(user)...)
		if conns == nil {
			conns = []PerUserConnection{}
		}
		writeJSON(w, conns)
	case http.MethodPost:
		var body struct {
			Name       string `json:"name"`
			Secret     string `json:"secret"`
			Disconnect bool   `json:"disconnect"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		// MCP oauth server: only Disconnect flows through here (Connect goes via
		// the /mcp/connect consent redirect, not a POST). Handle before the
		// SecureAPI guard, whose Load() wouldn't find this name.
		if cfg, found := MCP().Load(body.Name); found && cfg.AuthMode == MCPAuthOAuth {
			if body.Disconnect {
				MCP().DisconnectUser(user, body.Name)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			http.Error(w, "this integration connects via OAuth — use Connect", http.StatusBadRequest)
			return
		}
		// Guard: only per_user credentials are touchable from the account page
		// (never the shared/admin secrets).
		c, found := Secure().Load(body.Name)
		if !found || !c.IsPerUser() {
			http.Error(w, "no such per-user integration", http.StatusNotFound)
			return
		}
		switch {
		case body.Disconnect && c.IsAuthCode():
			Secure().ClearUserToken(body.Name, user) // OAuth: drop the token
		case c.IsAuthCode():
			// OAuth creds connect via the consent flow (oauth/start), not by
			// posting a secret here.
			http.Error(w, "this integration connects via OAuth — use Connect", http.StatusBadRequest)
			return
		default:
			// Key creds: set or clear the user's key (empty secret = disconnect).
			if err := Secure().SaveUserSecret(body.Name, user, body.Secret); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- preferences endpoint ----------------------------------------------------

// handlePrefs GET returns the user's personal defaults; POST updates whichever
// fields are present (the FormPanel auto-saves per toggle).
func (T *Account) handlePrefs(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	db := AuthDB()
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{
			"notify":            AuthGetNotifyDefault(db, user),
			"private_mode":      AuthGetPrivateMode(db, user),
			"inferred_disabled": AuthGetInferredDisabled(db, user),
			"timezone":          AuthGetUserTimezone(db, user),
		})
	case http.MethodPost:
		var req struct {
			Notify           *bool   `json:"notify,omitempty"`
			PrivateMode      *bool   `json:"private_mode,omitempty"`
			InferredDisabled *bool   `json:"inferred_disabled,omitempty"`
			Timezone         *string `json:"timezone,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Notify != nil {
			AuthSetNotifyDefault(db, user, *req.Notify)
		}
		if req.PrivateMode != nil {
			AuthSetPrivateMode(db, user, *req.PrivateMode)
		}
		if req.InferredDisabled != nil {
			AuthSetInferredDisabled(db, user, *req.InferredDisabled)
		}
		if req.Timezone != nil {
			// Blank clears back to the deployment zone. Otherwise validate and
			// store the canonical IANA name so a typo can't corrupt the user's
			// schedules/stamp; a bad value is rejected so the form shows it.
			if tz := strings.TrimSpace(*req.Timezone); tz == "" {
				AuthSetUserTimezone(db, user, "")
			} else if _, iana, err := ResolveZone(tz); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			} else {
				AuthSetUserTimezone(db, user, iana)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handlePassword lets a logged-in user change their OWN password: POST
// {current, new}. The CURRENT password is verified (so a borrowed/hijacked
// session can't silently reset it), and only the hash is updated — admin flag,
// apps, and preferences are preserved. 204 on success; 400 on weak input; 403
// when the current password is wrong.
func (T *Account) handlePassword(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// Match the signup/reset policy (6–128 chars).
	if len(req.New) < 6 {
		http.Error(w, "New password must be at least 6 characters.", http.StatusBadRequest)
		return
	}
	if len(req.New) > 128 {
		http.Error(w, "New password is too long.", http.StatusBadRequest)
		return
	}
	if !AuthChangePassword(AuthDB(), user, req.Current, req.New) {
		http.Error(w, "Current password is incorrect.", http.StatusForbidden)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- page --------------------------------------------------------------------

func (T *Account) servePage(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	sections := []ui.Section{
		{
			Title:    "Preferences",
			Subtitle: "Personal defaults, applied across your agents. Saved as you toggle.",
			Body: ui.FormPanel{
				Source: "api/prefs",
				Fields: []ui.FormField{
					{Field: "notify", Label: "Email notifications", Type: "toggle",
						Help: "Receive email when an agent finishes work for you."},
					{Field: "private_mode", Label: "Private mode by default", Type: "toggle",
						Help: "Mask network-capable tools (web search, fetch, …) by default — keeps turns local. Per-agent overrides still apply."},
					{Field: "inferred_disabled", Label: "Clean mode by default", Type: "toggle",
						Help: "Suppress the Reference Memory layer by default — agents answer fresh from your question + knowledge, without prior derived findings. Per-agent overrides still apply."},
					{Field: "timezone", Label: "Timezone", Type: "select",
						Options: TimezoneSelectOptions("System default"),
						Help:    "Your personal timezone, used for the times agents see and the day boundaries of schedules you own. Blank uses the system default."},
				},
			},
		},
	}
	// App-contributed preference sections (RegisterAccountSection) — the
	// per-user sibling of the admin section registry. Each app's builder
	// decides per request whether this user gets its section (typically a
	// UserHasAppAccess gate), so preferences for apps the user can't reach
	// never render. Placed right after the core Preferences block.
	extraHead := ""
	for _, e := range AccountSectionEntries() {
		if e.Build == nil {
			continue
		}
		if s, show := e.Build(r, user); show {
			sections = append(sections, s)
			extraHead += e.Head
		}
	}
	// Change-password only makes sense when password auth is actually in use.
	if db := AuthDB(); db != nil && AuthHasUsers(db) {
		sections = append(sections, ui.Section{
			Title:    "Password",
			Subtitle: "Change the password you sign in with. You'll need your current one.",
			Body:     ui.Card{HTML: passwordHTML},
		})
	}
	// Connected accounts, My API credentials, and My tools moved to the Gateways
	// app (the per-user "outward reach" surface). Account keeps identity +
	// preferences, including inbound personal access tokens below (the keys an
	// external MCP client uses to reach THIS user's agents).
	sections = append(sections,
		ui.Section{
			Title:    "API keys (personal access)",
			Subtitle: "Tokens for connecting an external client — e.g. Claude Desktop over MCP, or a voice platform over the OpenAI /v1 endpoint — to your own gohort agents. Send it as the client's X-API-Key header, or as \"Authorization: Bearer <token>\". Shown once at creation; revoke any time. Each key is SCOPED: a new key reaches nothing until you grant it features and targets (Configure access). Keys created before scoping existed are marked Unrestricted — set a scope to lock them down.",
			Body:     ui.Card{HTML: tokensHTML},
		},
	)
	ui.Page{
		Title:         "Account",
		ShowTitle:     true,
		BackURL:       "/",
		MaxWidth:      "640px",
		Sections:      sections,
		ExtraHeadHTML: extraHead,
	}.ServeHTTP(w, r)
}

// handleTokens lists / mints / revokes the user's personal access tokens. The
// full secret is returned ONLY by the POST (mint) response; GET masks it.
func (T *Account) handleTokens(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, ListAccountTokens(user))
	case http.MethodPost:
		// action=scope rescopes an existing key; default POST mints a new one.
		if strings.TrimSpace(r.URL.Query().Get("action")) == "scope" {
			id := strings.TrimSpace(r.URL.Query().Get("id"))
			var body struct {
				Scope TokenScope `json:"scope"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if !SetAccountTokenScope(user, id, &body.Scope) {
				http.Error(w, "token not found", http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var req struct {
			Name  string      `json:"name"`
			Scope *TokenScope `json:"scope"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		// New keys are minted deny-by-default: an explicit (possibly empty)
		// scope, never the nil legacy-unrestricted case. A client that sends no
		// scope still gets an empty one, so it reaches nothing until scoped.
		scope := req.Scope
		if scope == nil {
			scope = &TokenScope{}
		}
		writeJSON(w, MintAccountTokenScoped(user, req.Name, scope)) // secret returned once
	case http.MethodDelete:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		RevokeAccountToken(user, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleTokenTargets feeds the per-key scope editor: which FEATURES this user
// may enable on a key (the registered shareable features the admin permits
// them), and which TARGETS a key may reach (their own + shared-to-them exposed
// agents, channels, and the raw tiers). A feature the admin has NOT granted
// this user is omitted, so the editor can't offer a key something the admin
// gate would reject anyway.
func (T *Account) handleTokenTargets(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	type feat struct {
		Key   string `json:"key"`
		Label string `json:"label"`
	}
	feats := []feat{}
	for _, f := range ShareableFeatures() {
		if FeatureAllowedForUser(T.DB, f.Key, user) {
			feats = append(feats, feat{Key: f.Key, Label: f.Label})
		}
	}
	writeJSON(w, map[string]any{
		"features": feats,
		"targets":  ListExternalTargets(T.DB, user),
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// passwordHTML is the change-password panel: current + new + confirm fields with
// client-side validation, posting to api/password. App-specific, so it rides in
// a Card (the Card renderer re-executes this <script>), same pattern as the
// connections + tokens panels.
const passwordHTML = `<div class="acct-pw">
  <input type="password" id="acct-pw-cur" class="acct-pw-input" placeholder="Current password" autocomplete="current-password">
  <input type="password" id="acct-pw-new" class="acct-pw-input" placeholder="New password (6+ characters)" autocomplete="new-password">
  <input type="password" id="acct-pw-cf" class="acct-pw-input" placeholder="Confirm new password" autocomplete="new-password">
  <div class="acct-pw-row">
    <button class="ui-row-btn primary" id="acct-pw-save">Change password</button>
    <span id="acct-pw-msg" class="acct-pw-msg"></span>
  </div>
</div>
<style>
.acct-pw { display:flex; flex-direction:column; gap:0.5rem; max-width:22rem; }
.acct-pw-input { background:var(--bg-0); color:var(--text); border:1px solid var(--border); border-radius:6px; padding:0.4rem 0.55rem; font:inherit; font-size:0.9rem; }
.acct-pw-row { display:flex; align-items:center; gap:0.6rem; }
.acct-pw-msg { font-size:0.82rem; }
.acct-pw-msg.ok { color:var(--success); }
.acct-pw-msg.err { color:var(--danger); }
</style>
<script>
(function(){
  var cur=document.getElementById('acct-pw-cur'), nw=document.getElementById('acct-pw-new'),
      cf=document.getElementById('acct-pw-cf'), btn=document.getElementById('acct-pw-save'),
      msg=document.getElementById('acct-pw-msg');
  if(!btn) return;
  function setMsg(t, ok){ msg.textContent=t||''; msg.className='acct-pw-msg '+(ok?'ok':'err'); }
  btn.addEventListener('click', function(){
    setMsg('', true);
    if(!cur.value){ setMsg('Enter your current password.', false); cur.focus(); return; }
    if(nw.value.length < 6){ setMsg('New password must be at least 6 characters.', false); nw.focus(); return; }
    if(nw.value !== cf.value){ setMsg('New passwords do not match.', false); cf.focus(); return; }
    btn.disabled=true; var orig=btn.textContent; btn.textContent='Changing…';
    fetch('api/password', {method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'},
        body: JSON.stringify({current: cur.value, new: nw.value})})
      .then(function(r){
        btn.disabled=false; btn.textContent=orig;
        if(r.status===204){ cur.value=nw.value=cf.value=''; setMsg('Password changed.', true); return; }
        return r.text().then(function(t){ setMsg(t || ('Error '+r.status), false); });
      })
      .catch(function(e){ btn.disabled=false; btn.textContent=orig; setMsg('Failed: '+(e&&e.message||e), false); });
  });
})();
</script>`

// tokensHTML is the personal-access-token panel: lists the user's tokens (name +
// masked value + created), an inline create row that reveals the full secret
// ONCE, and a themed-confirm revoke. App-specific, so it rides in a Card rather
// than a core/ui primitive — same approach as the connections panel above.
const tokensHTML = `<div id="acct-tokens" class="acct-tokens">Loading…</div>
<style>
.acct-tokens { display:flex; flex-direction:column; gap:0.55rem; }
.acct-tok { border:1px solid var(--border); border-radius:8px; padding:0.55rem 0.75rem; display:flex; align-items:center; gap:0.6rem; }
.acct-tok-meta { flex:1; min-width:0; }
.acct-tok-name { font-weight:600; color:var(--text); }
.acct-tok-sub { font-size:0.75rem; color:var(--text-mute); margin-top:0.1rem; }
.acct-tok-code { font-family:ui-monospace,SFMono-Regular,Menlo,monospace; }
.acct-tok-btn { cursor:pointer; background:var(--bg-2); color:var(--text-mute); border:1px solid var(--border); border-radius:6px; padding:0.3rem 0.7rem; font:inherit; font-size:0.8rem; }
.acct-tok-btn:hover { color:var(--danger); border-color:var(--danger); }
.acct-tok-newrow { display:flex; gap:0.4rem; margin-top:0.3rem; }
.acct-tok-input { flex:1; background:var(--bg-0); color:var(--text); border:1px solid var(--border); border-radius:6px; padding:0.4rem 0.55rem; font:inherit; font-size:0.85rem; }
.acct-tok-create { cursor:pointer; background:var(--accent); color:#fff; border:0; border-radius:6px; padding:0.4rem 0.9rem; font:inherit; font-weight:600; }
.acct-tok-create:disabled { opacity:0.6; cursor:default; }
.acct-tok-reveal { border:1px solid var(--accent); border-radius:8px; padding:0.7rem 0.8rem; background:var(--bg-2); }
.acct-tok-reveal code { font-family:ui-monospace,SFMono-Regular,Menlo,monospace; font-size:0.82rem; color:var(--text); word-break:break-all; display:block; margin-top:0.3rem; }
.acct-tok-empty { color:var(--text-mute); font-style:italic; padding:0.4rem 0; }
.acct-tok-badge { font-size:0.66rem; font-weight:600; padding:0.08rem 0.45rem; border-radius:999px; margin-left:0.4rem; }
.acct-tok-badge.warn { background:rgba(220,160,40,0.18); color:#c88a1e; }
.acct-tok-scope { border:1px solid var(--border); border-radius:8px; padding:0.6rem 0.75rem; margin-top:0.35rem; background:var(--bg-2); display:flex; flex-direction:column; gap:0.5rem; }
.acct-tok-scope h4 { margin:0; font-size:0.72rem; text-transform:uppercase; letter-spacing:0.03em; color:var(--text-mute); }
.acct-tok-scope-grp { display:flex; flex-direction:column; gap:0.2rem; }
.acct-tok-chk { display:flex; align-items:center; gap:0.45rem; font-size:0.83rem; color:var(--text); }
.acct-tok-chk input { margin:0; }
.acct-tok-scope-note { font-size:0.75rem; color:var(--text-mute); font-style:italic; }
.acct-tok-scope-actions { display:flex; gap:0.5rem; align-items:center; }
</style>
<script>
(function(){
  var root = document.getElementById('acct-tokens');
  if (!root) return;
  var CAT = null; // {features:[{key,label}], targets:[{value,label,group}]} — loaded once
  function el(tag, attrs, kids){ var n=document.createElement(tag); if(attrs) for(var k in attrs){ if(k==='text') n.textContent=attrs[k]; else n.setAttribute(k,attrs[k]); } (kids||[]).forEach(function(c){ n.appendChild(typeof c==='string'?document.createTextNode(c):c); }); return n; }
  function targets(){ return fetch('api/token-targets',{credentials:'same-origin'}).then(function(r){return r.json();}).then(function(d){ CAT=d||{features:[],targets:[]}; }).catch(function(){ CAT={features:[],targets:[]}; }); }
  function load(){ return fetch('api/tokens',{credentials:'same-origin'}).then(function(r){return r.json();}).then(render).catch(function(){ root.textContent='Failed to load.'; }); }

  // checkbox helper: returns {row, checked()} for one option.
  function chk(label, value, on){ var box=el('input',{type:'checkbox'}); if(on) box.checked=true; box.setAttribute('data-v',value); return { row: el('label',{class:'acct-tok-chk'},[box, document.createTextNode(label)]), box: box }; }

  // Build the feature + target picker. selected = {features:[], targets:[]} or null.
  // Returns {node, collect()} where collect() reads the ticked values.
  function scopeEditor(selected){
    selected = selected || {features:[], targets:[]};
    var selF = {}, selT = {};
    (selected.features||[]).forEach(function(f){ selF[f]=true; });
    (selected.targets||[]).forEach(function(t){ selT[t]=true; });
    var boxes = { features: [], targets: [] };
    var wrap = el('div',{class:'acct-tok-scope'});

    // Features
    wrap.appendChild(el('h4',{text:'Features this key may use'}));
    if(!CAT.features || !CAT.features.length){
      wrap.appendChild(el('div',{class:'acct-tok-scope-note',text:'No features available to you. An admin grants access under Feature Access.'}));
    } else {
      var fg = el('div',{class:'acct-tok-scope-grp'});
      CAT.features.forEach(function(f){ var c=chk(f.label||f.key, f.key, !!selF[f.key]); boxes.features.push(c); fg.appendChild(c.row); });
      wrap.appendChild(fg);
    }

    // Targets, grouped by their Group header (Tiers / Agents / Channels).
    wrap.appendChild(el('h4',{text:'Agents & tiers this key may reach'}));
    if(!CAT.targets || !CAT.targets.length){
      wrap.appendChild(el('div',{class:'acct-tok-scope-note',text:'No agents or channels are exposed. Turn on "Reachable over MCP" on an agent to offer it here.'}));
    } else {
      var groups = {}; var order = [];
      CAT.targets.forEach(function(t){ var g=t.group||'Other'; if(!groups[g]){ groups[g]=[]; order.push(g); } groups[g].push(t); });
      order.forEach(function(g){
        wrap.appendChild(el('div',{class:'acct-tok-scope-note',text:g}));
        var tg = el('div',{class:'acct-tok-scope-grp'});
        groups[g].forEach(function(t){ var c=chk(t.label||t.value, t.value, !!selT[t.value]); boxes.targets.push(c); tg.appendChild(c.row); });
        wrap.appendChild(tg);
      });
    }
    function collect(){
      var f=[], t=[];
      boxes.features.forEach(function(c){ if(c.box.checked) f.push(c.box.getAttribute('data-v')); });
      boxes.targets.forEach(function(c){ if(c.box.checked) t.push(c.box.getAttribute('data-v')); });
      return { features: f, targets: t };
    }
    return { node: wrap, collect: collect };
  }

  // One-line summary of a key's scope for the row.
  function scopeSummary(t){
    if(!t.scope){ return null; } // legacy: rendered as a badge instead
    var f=(t.scope.features||[]).length, tg=(t.scope.targets||[]).length;
    if(!f && !tg) return 'Reaches nothing yet — set a scope';
    return (f?f+' feature'+(f>1?'s':''):'no features')+' · '+(tg?tg+' target'+(tg>1?'s':''):'no targets');
  }

  function render(list){
    root.innerHTML='';
    list = list || [];
    if(!list.length){ root.appendChild(el('div',{class:'acct-tok-empty',text:'No API keys yet. Create one to connect an external client.'})); }
    list.forEach(function(t){
      var nameRow = el('div',{class:'acct-tok-name'},[ document.createTextNode(t.name || '(unnamed)') ]);
      if(!t.scope){ nameRow.appendChild(el('span',{class:'acct-tok-badge warn',text:'Unrestricted'})); }
      var subKids = [ el('span',{class:'acct-tok-code',text: t.token || ''}), document.createTextNode('  ·  created '+String(t.created||'').slice(0,10)) ];
      var sum = scopeSummary(t);
      if(sum){ subKids.push(document.createTextNode('  ·  '+sum)); }
      else if(!t.scope){ subKids.push(document.createTextNode('  ·  reaches everything (set a scope to restrict)')); }
      var meta = el('div',{class:'acct-tok-meta'},[ nameRow, el('div',{class:'acct-tok-sub'}, subKids) ]);

      var scopeBtn = el('button',{class:'acct-tok-btn',style:'color:var(--text-mute)',text:'Scope'});
      var del = el('button',{class:'acct-tok-btn',text:'Revoke'});
      var rowEl = el('div',{class:'acct-tok'},[meta,scopeBtn,del]);
      var holder = el('div',{},[rowEl]); // row + (optional) inline editor
      del.addEventListener('click',function(){
        var go = window.uiConfirm ? window.uiConfirm('Revoke this API key? Any client using it stops working.') : Promise.resolve(true);
        go.then(function(ok){ if(!ok) return; fetch('api/tokens?id='+encodeURIComponent(t.id),{method:'DELETE',credentials:'same-origin'}).then(load); });
      });
      scopeBtn.addEventListener('click',function(){
        var existing = holder.querySelector('.acct-tok-scope');
        if(existing){ existing.parentNode.removeChild(existing); return; }
        var ed = scopeEditor(t.scope);
        var save = el('button',{class:'acct-tok-create',text:'Save scope'});
        save.addEventListener('click',function(){
          save.disabled=true; save.textContent='Saving…';
          fetch('api/tokens?action=scope&id='+encodeURIComponent(t.id),{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json'},body:JSON.stringify({scope: ed.collect()})})
            .then(function(r){ if(r.status===204){ load(); } else { save.disabled=false; save.textContent='Save scope'; } })
            .catch(function(){ save.disabled=false; save.textContent='Save scope'; });
        });
        ed.node.appendChild(el('div',{class:'acct-tok-scope-actions'},[save]));
        holder.appendChild(ed.node);
      });
      root.appendChild(holder);
    });

    // Create row: name + a "Configure access" expander (deny-by-default), then Create.
    var nameInput = el('input',{type:'text',class:'acct-tok-input',placeholder:'Name (e.g. Claude Desktop)'});
    var create = el('button',{class:'acct-tok-create',text:'Create key'});
    var newHolder = el('div',{});
    var pendingScope = null;
    var cfgBtn = el('button',{class:'acct-tok-btn',style:'color:var(--text-mute)',text:'Configure access'});
    cfgBtn.addEventListener('click',function(){
      var existing = newHolder.querySelector('.acct-tok-scope');
      if(existing){ existing.parentNode.removeChild(existing); pendingScope=null; return; }
      pendingScope = scopeEditor(null);
      newHolder.appendChild(pendingScope.node);
    });
    create.addEventListener('click',function(){
      // Deny-by-default: an empty scope reaches nothing until the user ticks options.
      var scope = pendingScope ? pendingScope.collect() : {features:[], targets:[]};
      create.disabled=true; create.textContent='Creating…';
      fetch('api/tokens',{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json'},body:JSON.stringify({name:nameInput.value.trim(), scope: scope})})
        .then(function(r){return r.json();}).then(function(t){ load().then(function(){ reveal(t); }); })
        .catch(function(){ create.disabled=false; create.textContent='Create key'; });
    });
    root.appendChild(el('div',{class:'acct-tok-newrow'},[nameInput,cfgBtn,create]));
    root.appendChild(newHolder);
  }
  function reveal(t){
    if(!t || !t.token) return;
    root.insertBefore(el('div',{class:'acct-tok-reveal'},[ el('div',{class:'acct-tok-sub',text:'Copy this now — it will not be shown again:'}), el('code',{text:t.token}) ]), root.firstChild);
  }
  targets().then(load);
})();
</script>`

// credentialsHTML is the "My API credentials" panel: lists the user's OWN
// credentials (name + type badge + base URL) with Edit/Delete, and an inline
// add/edit form. App-specific, so it rides in a Card (the Card renderer
// re-executes this <script>), same pattern as the connections + tokens panels.
// All CRUD hits api/credentials, which scopes every op to the calling user.
const credentialsHTML = `<div id="acct-creds" class="acct-creds">Loading…</div>
<style>
.acct-creds { display:flex; flex-direction:column; gap:0.55rem; }
.acct-cred { border:1px solid var(--border); border-radius:8px; padding:0.55rem 0.75rem; display:flex; align-items:center; gap:0.6rem; }
.acct-cred-meta { flex:1; min-width:0; }
.acct-cred-name { font-weight:600; color:var(--text); }
.acct-cred-sub { font-size:0.75rem; color:var(--text-mute); margin-top:0.1rem; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.acct-cred-badge { font-size:0.68rem; font-weight:600; padding:0.08rem 0.45rem; border-radius:999px; background:var(--bg-2); color:var(--text-mute); margin-left:0.4rem; }
.acct-cred-btn { cursor:pointer; background:var(--bg-2); color:var(--text-mute); border:1px solid var(--border); border-radius:6px; padding:0.3rem 0.7rem; font:inherit; font-size:0.8rem; }
.acct-cred-btn:hover { color:var(--text); }
.acct-cred-btn.danger:hover { color:var(--danger); border-color:var(--danger); }
.acct-cred-empty { color:var(--text-mute); font-style:italic; padding:0.4rem 0; }
.acct-cred-form { border:1px solid var(--accent); border-radius:8px; padding:0.75rem 0.85rem; display:flex; flex-direction:column; gap:0.5rem; background:var(--bg-2); }
.acct-cred-form label { font-size:0.78rem; color:var(--text-mute); display:flex; flex-direction:column; gap:0.2rem; }
.acct-cred-form input, .acct-cred-form select, .acct-cred-form textarea { background:var(--bg-0); color:var(--text); border:1px solid var(--border); border-radius:6px; padding:0.4rem 0.55rem; font:inherit; font-size:0.85rem; }
.acct-cred-form .row { display:flex; gap:0.6rem; }
.acct-cred-form .row > label { flex:1; }
.acct-cred-form .actions { display:flex; gap:0.5rem; align-items:center; margin-top:0.2rem; }
.acct-cred-form .chk { flex-direction:row; align-items:center; gap:0.4rem; }
.acct-cred-form .chk input { width:auto; }
.acct-cred-add { align-self:flex-start; cursor:pointer; background:var(--accent); color:#fff; border:0; border-radius:6px; padding:0.4rem 0.9rem; font:inherit; font-weight:600; }
.acct-cred-msg { font-size:0.8rem; }
.acct-cred-msg.err { color:var(--danger); }
</style>
<script>
(function(){
  var root = document.getElementById('acct-creds');
  if (!root) return;
  function el(tag, attrs, kids){ var n=document.createElement(tag); if(attrs) for(var k in attrs){ if(k==='text') n.textContent=attrs[k]; else if(k==='class') n.className=attrs[k]; else n.setAttribute(k,attrs[k]); } (kids||[]).forEach(function(c){ n.appendChild(typeof c==='string'?document.createTextNode(c):c); }); return n; }
  var TYPES = [['bearer','Bearer token'],['header','Custom header'],['query','Query param'],['basic_auth','Basic auth'],['none','No auth (public)']];
  function load(){ return fetch('api/credentials',{credentials:'same-origin'}).then(function(r){return r.json();}).then(render).catch(function(){ root.textContent='Failed to load.'; }); }
  function render(list){
    root.innerHTML='';
    list = list || [];
    if(!list.length){ root.appendChild(el('div',{class:'acct-cred-empty',text:'No credentials yet. Add one to let your agents call an API as you.'})); }
    list.forEach(function(c){
      var sub = (c.base_url||'') + (c.description ? '  ·  '+c.description : '');
      var meta = el('div',{class:'acct-cred-meta'},[
        el('div',{class:'acct-cred-name'},[ document.createTextNode(c.name), el('span',{class:'acct-cred-badge',text:c.type}) ]),
        el('div',{class:'acct-cred-sub',text: sub})
      ]);
      var edit = el('button',{class:'acct-cred-btn',text:'Edit'});
      edit.addEventListener('click',function(){ showForm(c); });
      var del = el('button',{class:'acct-cred-btn danger',text:'Delete'});
      del.addEventListener('click',function(){
        var go = window.uiConfirm ? window.uiConfirm('Delete credential "'+c.name+'"? Agents and tools using it stop working.') : Promise.resolve(confirm('Delete "'+c.name+'"?'));
        go.then(function(ok){ if(!ok) return; fetch('api/credentials?name='+encodeURIComponent(c.name),{method:'DELETE',credentials:'same-origin'}).then(load); });
      });
      root.appendChild(el('div',{class:'acct-cred'},[meta,edit,del]));
    });
    var add = el('button',{class:'acct-cred-add',text:'+ Add credential'});
    add.addEventListener('click',function(){ showForm(null); });
    root.appendChild(add);
  }
  function field(labelText, node){ return el('label',{},[document.createTextNode(labelText), node]); }
  function showForm(existing){
    var editing = !!existing;
    var nameI = el('input',{type:'text',placeholder:'lower_snake_case',value: editing?existing.name:''});
    if(editing) nameI.setAttribute('readonly','readonly');
    var typeSel = el('select',{});
    TYPES.forEach(function(t){ var o=el('option',{value:t[0],text:t[1]}); if(editing&&existing.type===t[0]) o.setAttribute('selected','selected'); typeSel.appendChild(o); });
    var baseI = el('input',{type:'text',placeholder:'https://api.example.com',value: editing?(existing.base_url||''):''});
    var paramWrap = field('Header / query name', el('input',{type:'text',placeholder:'X-Api-Key',value: editing?(existing.param_name||''):''}));
    var paramI = paramWrap.querySelector('input');
    var descI = el('input',{type:'text',placeholder:'What this is for (optional)',value: editing?(existing.description||''):''});
    var secretI = el('input',{type:'password',placeholder: editing?'Leave blank to keep current secret':'Paste your key / token'});
    var confirmC = el('input',{type:'checkbox'}); if(editing&&existing.requires_confirm) confirmC.setAttribute('checked','checked');
    var msg = el('span',{class:'acct-cred-msg'});
    function syncType(){
      var t = typeSel.value;
      paramWrap.style.display = (t==='header'||t==='query') ? '' : 'none';
      secretI.parentNode.style.display = (t==='none') ? 'none' : '';
    }
    typeSel.addEventListener('change', syncType);
    var save = el('button',{class:'acct-cred-add',text: editing?'Save':'Create'});
    var cancel = el('button',{class:'acct-cred-btn',text:'Cancel'});
    cancel.addEventListener('click', load);
    save.addEventListener('click',function(){
      msg.textContent=''; msg.className='acct-cred-msg';
      var body = { name:nameI.value.trim(), type:typeSel.value, base_url:baseI.value.trim(),
        param_name:paramI.value.trim(), description:descI.value.trim(), secret:secretI.value,
        requires_confirm:confirmC.checked };
      save.disabled=true; var orig=save.textContent; save.textContent='Saving…';
      fetch('api/credentials',{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)})
        .then(function(r){ if(r.status===204){ load(); return; } return r.text().then(function(t){ throw new Error(t||('HTTP '+r.status)); }); })
        .catch(function(e){ save.disabled=false; save.textContent=orig; msg.className='acct-cred-msg err'; msg.textContent=(e&&e.message||e); });
    });
    var form = el('div',{class:'acct-cred-form'},[
      field('Name', nameI),
      el('div',{class:'row'},[ field('Type', typeSel), field('Base URL', baseI) ]),
      paramWrap,
      field('Description', descI),
      field('Secret', secretI),
      el('label',{class:'chk'},[confirmC, document.createTextNode('Require confirmation before each call')]),
      el('div',{class:'actions'},[save, cancel, msg])
    ]);
    root.innerHTML=''; root.appendChild(form); syncType(); nameI.focus();
  }
  load();
})();
</script>`

// userToolsHTML is the "My tools" panel: a read-mostly list of the user's OWN
// persistent tools (name + mode badge + description, a missing-dependency badge,
// a shared badge) with a Delete control. Authoring happens in chat, so there's
// no create form here. App-specific, so it rides in a Card (the Card renderer
// re-executes this <script>), same pattern as the other Account panels.
const userToolsHTML = `<div id="acct-tools" class="acct-tools">Loading…</div>
<style>
.acct-tools { display:flex; flex-direction:column; gap:0.55rem; }
.acct-tool { border:1px solid var(--border); border-radius:8px; padding:0.55rem 0.75rem; display:flex; align-items:center; gap:0.6rem; }
.acct-tool-meta { flex:1; min-width:0; }
.acct-tool-name { font-weight:600; color:var(--text); }
.acct-tool-badge { font-size:0.68rem; font-weight:600; padding:0.08rem 0.45rem; border-radius:999px; background:var(--bg-2); color:var(--text-mute); margin-left:0.4rem; }
.acct-tool-badge.shared { background:color-mix(in srgb, var(--accent) 22%, transparent); color:var(--accent); }
.acct-tool-badge.missing { background:color-mix(in srgb, var(--danger) 20%, transparent); color:var(--danger); }
.acct-tool-sub { font-size:0.75rem; color:var(--text-mute); margin-top:0.1rem; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.acct-tool-btn { cursor:pointer; background:var(--bg-2); color:var(--text-mute); border:1px solid var(--border); border-radius:6px; padding:0.3rem 0.7rem; font:inherit; font-size:0.8rem; }
.acct-tool-btn:hover { color:var(--danger); border-color:var(--danger); }
.acct-tool-empty { color:var(--text-mute); font-style:italic; padding:0.4rem 0; }
</style>
<script>
(function(){
  var root = document.getElementById('acct-tools');
  if (!root) return;
  function el(tag, attrs, kids){ var n=document.createElement(tag); if(attrs) for(var k in attrs){ if(k==='text') n.textContent=attrs[k]; else if(k==='class') n.className=attrs[k]; else n.setAttribute(k,attrs[k]); } (kids||[]).forEach(function(c){ n.appendChild(typeof c==='string'?document.createTextNode(c):c); }); return n; }
  function load(){ return fetch('api/tools',{credentials:'same-origin'}).then(function(r){return r.json();}).then(render).catch(function(){ root.textContent='Failed to load.'; }); }
  function render(list){
    root.innerHTML='';
    list = list || [];
    if(!list.length){ root.appendChild(el('div',{class:'acct-tool-empty',text:'No tools yet. Ask the assistant in chat to build one for you.'})); return; }
    list.forEach(function(t){
      var name = el('div',{class:'acct-tool-name'},[document.createTextNode(t.name)]);
      if(t.mode) name.appendChild(el('span',{class:'acct-tool-badge',text:t.mode}));
      if(t.shared) name.appendChild(el('span',{class:'acct-tool-badge shared',text:'shared'}));
      if(t.missing) name.appendChild(el('span',{class:'acct-tool-badge missing',text:'missing '+(t.credential||'credential')}));
      var subText = t.description || '';
      if(t.last_used) subText = subText ? (subText+'  ·  last used '+t.last_used) : ('last used '+t.last_used);
      var meta = el('div',{class:'acct-tool-meta'},[ name, el('div',{class:'acct-tool-sub',text: subText}) ]);
      var del = el('button',{class:'acct-tool-btn',text:'Delete'});
      del.addEventListener('click',function(){
        var go = window.uiConfirm ? window.uiConfirm('Delete tool "'+t.name+'"? Agents using it lose it.') : Promise.resolve(confirm('Delete "'+t.name+'"?'));
        go.then(function(ok){ if(!ok) return; fetch('api/tools?name='+encodeURIComponent(t.name),{method:'DELETE',credentials:'same-origin'}).then(load); });
      });
      root.appendChild(el('div',{class:'acct-tool'},[meta,del]));
    });
  }
  load();
})();
</script>`
