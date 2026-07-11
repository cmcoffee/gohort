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
	T.HandleFunc("/api/connections", T.handleConnections)
	T.HandleFunc("/api/tokens", T.handleTokens)
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
		http.Redirect(w, r, "/account/?oauth=denied", http.StatusFound)
		return
	}
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if _, _, err := Secure().OAuthCallback(r.Context(), state, code); err != nil {
		Log("[account] oauth callback failed: %v", err)
		http.Redirect(w, r, "/account/?oauth=failed", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/account/?oauth=connected", http.StatusFound)
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
		writeJSON(w, map[string]bool{
			"notify":            AuthGetNotifyDefault(db, user),
			"private_mode":      AuthGetPrivateMode(db, user),
			"inferred_disabled": AuthGetInferredDisabled(db, user),
		})
	case http.MethodPost:
		var req struct {
			Notify           *bool `json:"notify,omitempty"`
			PrivateMode      *bool `json:"private_mode,omitempty"`
			InferredDisabled *bool `json:"inferred_disabled,omitempty"`
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
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
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
				},
			},
		},
	}
	// Change-password only makes sense when password auth is actually in use.
	if db := AuthDB(); db != nil && AuthHasUsers(db) {
		sections = append(sections, ui.Section{
			Title:    "Password",
			Subtitle: "Change the password you sign in with. You'll need your current one.",
			Body:     ui.Card{HTML: passwordHTML},
		})
	}
	sections = append(sections,
		ui.Section{
			Title:    "Connected accounts",
			Subtitle: "Integrations you authorize with your own account (read or write as you). Your key is stored encrypted and never shown to the assistant.",
			Body:     ui.Card{HTML: connectionsHTML},
		},
		ui.Section{
			Title:    "API keys (personal access)",
			Subtitle: "Tokens for connecting an external client — e.g. Claude Desktop over MCP — to your own gohort agents and tools. Put the token in the client's X-API-Key header. Shown once at creation; revoke any time.",
			Body:     ui.Card{HTML: tokensHTML},
		},
	)
	ui.Page{
		Title:     "Account",
		ShowTitle: true,
		BackURL:   "/",
		MaxWidth:  "640px",
		Sections:  sections,
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
		var req struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		writeJSON(w, MintAccountToken(user, req.Name)) // secret returned once
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

// connectionsHTML is the Connected-accounts panel: a container the inline script
// fills by fetching api/connections, rendering each per_user integration with a
// connected/not badge + a key field (Save / Disconnect). App-specific, so it
// rides in a Card rather than a core/ui primitive. The Card renderer re-executes
// this <script>.
const connectionsHTML = `<div id="acct-conns" class="acct-conns">Loading…</div>
<style>
.acct-conns { display: flex; flex-direction: column; gap: 0.6rem; }
.acct-conn { border: 1px solid var(--border); border-radius: 8px; padding: 0.7rem 0.8rem; }
.acct-conn-head { display: flex; align-items: center; gap: 0.5rem; margin-bottom: 0.5rem; }
.acct-conn-name { font-weight: 600; color: var(--text); flex: 1; }
.acct-conn-badge { font-size: 0.7rem; font-weight: 600; padding: 0.1rem 0.5rem; border-radius: 999px; }
.acct-conn-badge.on { background: color-mix(in srgb, var(--success) 22%, transparent); color: var(--success); }
.acct-conn-badge.off { background: var(--bg-2); color: var(--text-mute); }
.acct-conn-desc { font-size: 0.82rem; color: var(--text-mute); margin-bottom: 0.5rem; }
.acct-conn-row { display: flex; gap: 0.4rem; align-items: center; }
.acct-conn-row input { flex: 1; background: var(--bg-0); color: var(--text); border: 1px solid var(--border); border-radius: 6px; padding: 0.35rem 0.5rem; font: inherit; font-size: 0.85rem; }
.acct-conns-empty { color: var(--text-mute); font-style: italic; padding: 0.5rem 0; }
</style>
<script>
(function(){
  var box = document.getElementById('acct-conns');
  if (!box) return;
  // A consent popup reports success via postMessage; refresh so the badge flips.
  window.addEventListener('message', function(e){ if (e && e.data === 'gohort-mcp-connected') load(); });
  function el(t, a, k){ var n=document.createElement(t); if(a) for(var x in a){ if(x==='text') n.textContent=a[x]; else if(x==='class') n.className=a[x]; else n.setAttribute(x,a[x]); } (k||[]).forEach(function(c){ n.appendChild(typeof c==='string'?document.createTextNode(c):c); }); return n; }
  function post(body, btn){
    btn.disabled = true; var orig = btn.textContent; btn.textContent = '…';
    return fetch('api/connections', {method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'}, body: JSON.stringify(body)})
      .then(function(r){ if(!r.ok && r.status!==204) return r.text().then(function(t){ throw new Error(t||('HTTP '+r.status)); }); load(); })
      .catch(function(e){ btn.disabled=false; btn.textContent=orig; alert('Failed: '+(e&&e.message||e)); });
  }
  function save(name, secret, btn){ return post({name:name, secret:secret}, btn); }
  function disconnect(name, btn){ return post({name:name, disconnect:true}, btn); }
  function load(){
    fetch('api/connections', {credentials:'same-origin'}).then(function(r){ return r.json(); }).then(function(list){
      box.innerHTML = '';
      if (!list || !list.length){ box.appendChild(el('div',{class:'acct-conns-empty',text:'No per-user integrations available yet. When your admin enables one, it appears here to connect with your own account.'})); return; }
      list.forEach(function(c){
        var badge = el('span', {class:'acct-conn-badge '+(c.connected?'on':'off'), text: c.connected?'Connected':'Not connected'});
        var head = el('div', {class:'acct-conn-head'}, [el('span',{class:'acct-conn-name',text:c.name}), badge]);
        var card = el('div', {class:'acct-conn'}, [head]);
        if (c.description) card.appendChild(el('div',{class:'acct-conn-desc',text:c.description}));
        var row = el('div', {class:'acct-conn-row'});
        if (c.oauth){
          if (c.connect_url){
            // MCP-style OAuth: open consent in a popup and refresh the list when
            // it reports back (postMessage), so the badge flips without a full
            // page nav. Falls back to a new tab if the popup is blocked.
            var conn = el('button', {class:'ui-row-btn primary', text: c.connected?'Reconnect':'Connect'});
            conn.addEventListener('click', function(){
              var w = window.open(c.connect_url, 'gohort-connect', 'width=600,height=760');
              if (!w) { window.open(c.connect_url, '_blank'); }
            });
            row.appendChild(conn);
          } else {
            // SecureAPI OAuth: a Connect link that redirects to the provider.
            var connA = el('a', {class:'ui-row-btn primary', href:'oauth/start?cred='+encodeURIComponent(c.name), text: c.connected?'Reconnect':'Connect'});
            row.appendChild(connA);
          }
          if (c.connected){
            var d2 = el('button', {class:'ui-row-btn', text:'Disconnect'});
            d2.addEventListener('click', function(){ if(!confirm('Disconnect '+c.name+'? Your authorization is removed.')) return; disconnect(c.name, d2); });
            row.appendChild(d2);
          }
        } else {
          // Per-user key: paste a key.
          var inp = el('input', {type:'password', placeholder: c.connected?'Replace your key…':'Paste your key / token'});
          var saveBtn = el('button', {class:'ui-row-btn primary', text: c.connected?'Update':'Connect'});
          saveBtn.addEventListener('click', function(){ var v=inp.value.trim(); if(!v){ inp.focus(); return; } save(c.name, v, saveBtn); });
          row.appendChild(inp); row.appendChild(saveBtn);
          if (c.connected){
            var dis = el('button', {class:'ui-row-btn', text:'Disconnect'});
            dis.addEventListener('click', function(){ if(!confirm('Disconnect '+c.name+'? Your stored key is removed.')) return; disconnect(c.name, dis); });
            row.appendChild(dis);
          }
        }
        card.appendChild(row);
        box.appendChild(card);
      });
    }).catch(function(){ box.textContent = 'Could not load connections.'; });
  }
  load();
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
</style>
<script>
(function(){
  var root = document.getElementById('acct-tokens');
  if (!root) return;
  function el(tag, attrs, kids){ var n=document.createElement(tag); if(attrs) for(var k in attrs){ if(k==='text') n.textContent=attrs[k]; else n.setAttribute(k,attrs[k]); } (kids||[]).forEach(function(c){ n.appendChild(typeof c==='string'?document.createTextNode(c):c); }); return n; }
  function load(){ return fetch('api/tokens',{credentials:'same-origin'}).then(function(r){return r.json();}).then(render).catch(function(){ root.textContent='Failed to load.'; }); }
  function render(list){
    root.innerHTML='';
    list = list || [];
    if(!list.length){ root.appendChild(el('div',{class:'acct-tok-empty',text:'No API keys yet. Create one to connect an external client.'})); }
    list.forEach(function(t){
      var meta = el('div',{class:'acct-tok-meta'},[
        el('div',{class:'acct-tok-name',text: t.name || '(unnamed)'}),
        el('div',{class:'acct-tok-sub'},[ el('span',{class:'acct-tok-code',text: t.token || ''}), document.createTextNode('  ·  created '+String(t.created||'').slice(0,10)) ])
      ]);
      var del = el('button',{class:'acct-tok-btn',text:'Revoke'});
      del.addEventListener('click',function(){
        var go = window.uiConfirm ? window.uiConfirm('Revoke this API key? Any client using it stops working.') : Promise.resolve(true);
        go.then(function(ok){ if(!ok) return; fetch('api/tokens?id='+encodeURIComponent(t.id),{method:'DELETE',credentials:'same-origin'}).then(load); });
      });
      root.appendChild(el('div',{class:'acct-tok'},[meta,del]));
    });
    var nameInput = el('input',{type:'text',class:'acct-tok-input',placeholder:'Name (e.g. Claude Desktop)'});
    var create = el('button',{class:'acct-tok-create',text:'Create key'});
    create.addEventListener('click',function(){
      create.disabled=true; create.textContent='Creating…';
      fetch('api/tokens',{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json'},body:JSON.stringify({name:nameInput.value.trim()})})
        .then(function(r){return r.json();}).then(function(t){ load().then(function(){ reveal(t); }); })
        .catch(function(){ create.disabled=false; create.textContent='Create key'; });
    });
    root.appendChild(el('div',{class:'acct-tok-newrow'},[nameInput,create]));
  }
  function reveal(t){
    if(!t || !t.token) return;
    root.insertBefore(el('div',{class:'acct-tok-reveal'},[ el('div',{class:'acct-tok-sub',text:'Copy this now — it will not be shown again:'}), el('code',{text:t.token}) ]), root.firstChild);
  }
  load();
})();
</script>`
