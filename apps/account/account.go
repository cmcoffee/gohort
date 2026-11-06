// Package account is the per-user settings surface — every logged-in user's own
// page (NOT admin-gated, distinct from the admin Site Settings). It gives the
// previously-scattered per-user preferences a home, and is where per-user
// integrations (individual OAuth connections) get connected/disconnected once
// the per-credential Scope work lands. Reached from the dashboard header
// (Account link next to Logout), not a tile (WebHidden).
package account

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/mcppersonal"
	"github.com/cmcoffee/gohort/core/ui"
	"strconv"
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

// WebEveryUser: a person's own account is theirs to open. Hidden from the
// grant pickers, it could not be granted, so it refused every non-admin.
func (T *Account) WebEveryUser() bool { return true }

func (T *Account) Routes() {
	T.HandleFunc("/api/prefs", jsonapi.Methods{
		"GET":  jsonapi.Get(T.DB, T.handlePrefs),
		"POST": jsonapi.Post(T.DB, T.handlePrefsSave),
	}.Serve)
	T.HandleFunc("/api/password", jsonapi.Methods{
		"POST": jsonapi.Post(T.DB, T.handlePassword),
	}.Serve)
	// Connected-accounts (per-user OAuth/MCP) endpoints stay registered here for
	// redirect-URI stability even though the management card now renders under
	// the Gateways app (which calls these by absolute /account path).
	T.HandleFunc("/api/connections", jsonapi.Methods{
		"GET":  jsonapi.Get(T.DB, T.handleConnections),
		"POST": jsonapi.Post(T.DB, T.handleConnectionSave),
	}.Serve)
	T.HandleFunc("/api/tokens", jsonapi.Methods{
		"GET":    jsonapi.Get(T.DB, T.handleTokens),
		"POST":   T.tokenPost(),
		"DELETE": jsonapi.Delete(T.DB, T.handleTokenRevoke),
	}.Serve)
	T.HandleFunc("/api/token-targets", jsonapi.Get(T.DB, T.handleTokenTargets))
	T.HandleFunc("/api/artifacts/export", T.handleArtifactExport)
	T.HandleFunc("/api/artifacts/preview", jsonapi.Methods{
		"POST": jsonapi.Post(T.DB, T.handleArtifactPreview, jsonapi.MaxBody(maxAccountImportBytes)),
	}.Serve)
	T.HandleFunc("/api/artifacts/import", jsonapi.Methods{
		"POST": jsonapi.Post(T.DB, T.handleArtifactImport, jsonapi.MaxBody(maxAccountImportBytes)),
	}.Serve)
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
	// Not a credential somebody may not use, or one switched off (an imported
	// draft lands disabled): either would send the person to its authorize
	// page for a connection nothing can use.
	if c.Disabled || !Secure().UserMayUse(c, user) {
		http.Error(w, "this integration is not available to you", http.StatusForbidden)
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
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		http.Redirect(w, r, "/extensions/?oauth=denied", http.StatusFound)
		return
	}
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if _, _, err := Secure().OAuthCallback(r.Context(), state, code, user); err != nil {
		Log("[account] oauth callback failed: %v", err)
		http.Redirect(w, r, "/extensions/?oauth=failed", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/extensions/?oauth=connected", http.StatusFound)
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
	// Every exit from here renders the result PAGE, including the two
	// that used to be a bare http.Error. This runs in a 600x760 popup
	// with no chrome: a raw 404 in there is a blank white box, and the
	// only thing it tells somebody who clicked Reconnect is that
	// Reconnect does not work. The failures worth explaining are the
	// ones where the integration was renamed or its auth mode changed
	// under a card that still lists it.
	server := strings.TrimSpace(r.URL.Query().Get("server"))
	if server == "" {
		mcpConnectResultPage(w, "This link did not say which integration to connect.\n\n"+
			"Close this window and try Connect again from Extensions.")
		return
	}
	// One of the person's own MCP servers (Extensions > Integrations).
	if r.URL.Query().Get("own") == "1" {
		authURL, err := mcppersonal.StartConnect(user, server, mcpConnectCallbackURI(r))
		if err != nil {
			mcpConnectResultPage(w, "Could not start signing in to "+server+":\n\n"+err.Error())
			return
		}
		http.Redirect(w, r, authURL, http.StatusFound)
		return
	}
	cfg, found := MCP().Load(server)
	switch {
	case !found:
		mcpConnectResultPage(w, "There is no integration called "+strconv.Quote(server)+" any more.\n\n"+
			"It was probably renamed or removed by an administrator. Close this window and reload "+
			"Extensions to see what is available now.")
		return
	case cfg.AuthMode != MCPAuthOAuth:
		mcpConnectResultPage(w, strconv.Quote(server)+" does not connect with your own account.\n\n"+
			"It authenticates a different way, so there is nothing here for you to authorize. "+
			"Close this window; if an agent cannot reach it, that is for an administrator to fix.")
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
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
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
	if err := MCP().CompleteOAuth(state, code, user); err != nil {
		mcpConnectResultPage(w, "Could not complete the connection:\n\n"+err.Error())
		return
	}
	// A sign-in to one of their own servers: ask it what it offers now, so
	// its tools reach their agents without a Check by hand.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 45*time.Second)
	mcppersonal.AfterConnect(ctx, user)
	cancel()
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
	w.Write([]byte(`<!DOCTYPE html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Connect</title>` +
		`<style>html,body{height:100%;margin:0}body{background:#0d1117;color:#c9d1d9;` +
		`font-family:-apple-system,system-ui,sans-serif;display:flex;align-items:center;justify-content:center}` +
		`.card{max-width:480px;width:90%;background:#161b22;border:1px solid #30363d;border-radius:10px;padding:28px;white-space:pre-wrap;line-height:1.5}</style>` +
		`</head><body><div class="card">` + htmlEscape(msg) + `</div>` + notify + `</body></html>`))
}

// htmlEscape is a tiny escaper for the result-page message.
func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// handleConnections lists the per-user (per_user-scoped) credentials the user
// can connect, each flagged connected/not. The secret value is never returned,
// only connected status.
func (T *Account) handleConnections(c *jsonapi.Call) (any, error) {
	user := c.User
	// SecureAPI per-user credentials + per-user OAuth MCP servers, rendered
	// in one panel. MCP entries carry Kind="mcp" + a ConnectURL so the panel
	// routes their Connect/Disconnect to the MCP endpoints.
	// The deployment's per-user integrations, then the user's own API
	// credentials (their configuration lives under Integrations; their
	// key lives here, with every other key and sign-in), then MCP servers.
	conns := Secure().PerUserConnectionsFor(user)
	conns = append(conns, Secure().OwnConnectionsFor(user)...)
	conns = append(conns, MCP().PerUserOAuthConnectionsFor(user)...)
	conns = append(conns, mcppersonal.Connections(user)...)
	if conns == nil {
		conns = []PerUserConnection{}
	}
	return conns, nil
}

// connectionSave is what the Connected accounts panel posts for one
// integration.
type connectionSave struct {
	Name       string `json:"name"`
	Secret     string `json:"secret"`
	Disconnect bool   `json:"disconnect"`
	// Kind "own" is one of the user's own credentials, which can share
	// a name with a deployment one, so the panel says which it means.
	Kind string `json:"kind"`
	// A POINTER so "absent" and "off" are different things: a save
	// that says nothing about the lock must not read as a request to
	// unlock, which is the shape that loses a setting nobody touched.
	Secured *bool `json:"secured,omitempty"`
}

// handleConnectionSave sets or clears the user's secret for one integration,
// or moves its lock.
func (T *Account) handleConnectionSave(c *jsonapi.Call, body connectionSave) (any, error) {
	user := c.User
	// The user's own credential: only its key is set here. Its
	// configuration, reach and lending stay under Integrations.
	if body.Kind == ConnKindOwn {
		if body.Secured != nil {
			return nil, jsonapi.BadRequest("this credential's reach is set under Integrations")
		}
		// Only an explicit Disconnect removes the key. A blank save is a
		// mistake to refuse, not a request to wipe what is stored.
		secret := body.Secret
		if body.Disconnect {
			secret = ""
		} else if strings.TrimSpace(secret) == "" {
			return nil, jsonapi.BadRequest("paste a key to save")
		}
		if err := Secure().SetOwnedSecret(user, body.Name, secret); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
		return jsonapi.Empty(http.StatusNoContent), nil
	}
	// One of their own MCP servers: Disconnect only, as below.
	if body.Kind == mcppersonal.ConnKind {
		if !body.Disconnect {
			return nil, jsonapi.BadRequest("this server connects via OAuth: use Connect")
		}
		mcppersonal.Disconnect(user, body.Name)
		return jsonapi.Empty(http.StatusNoContent), nil
	}
	// MCP oauth server: only Disconnect flows through here (Connect goes via
	// the /mcp/connect consent redirect, not a POST). Handle before the
	// SecureAPI guard, whose Load() wouldn't find this name.
	if cfg, found := MCP().Load(body.Name); found && cfg.AuthMode == MCPAuthOAuth {
		if body.Disconnect {
			MCP().DisconnectUser(user, body.Name)
			return jsonapi.Empty(http.StatusNoContent), nil
		}
		return nil, jsonapi.BadRequest("this integration connects via OAuth: use Connect")
	}
	// Guard: only per_user credentials are touchable from the account page
	// (never the shared/admin secrets).
	cred, found := Secure().Load(body.Name)
	if !found || !cred.IsPerUser() {
		return nil, jsonapi.NotFound("no such per-user integration")
	}
	// The lock on this user's OWN key: whether every agent they have gets a
	// generic call_<name> route to spend it. Sent on its own, never
	// alongside a secret, so a save that only moves the switch cannot
	// disturb the key and vice versa.
	if body.Secured != nil {
		if cred.Secured && !*body.Secured {
			// The admin's lock is about the deployment's exposure and is
			// not a user's to lift. Said plainly rather than silently
			// ignored, so a switch that will not move explains itself.
			return nil, jsonapi.Forbidden("an admin has secured this integration for everyone: you can keep it locked, but not open it")
		}
		if err := Secure().SetUserSecured(body.Name, user, *body.Secured); err != nil {
			return nil, jsonapi.Status(http.StatusInternalServerError, err.Error())
		}
		return jsonapi.Empty(http.StatusNoContent), nil
	}
	switch {
	case body.Disconnect && cred.IsAuthCode():
		Secure().ClearUserToken(body.Name, user) // OAuth: drop the token
	case cred.IsAuthCode():
		// OAuth creds connect via the consent flow (oauth/start), not by
		// posting a secret here.
		return nil, jsonapi.BadRequest("this integration connects via OAuth: use Connect")
	default:
		// Key creds: set or clear the user's key (empty secret = disconnect).
		if err := Secure().SaveUserSecret(body.Name, user, body.Secret); err != nil {
			return nil, jsonapi.Status(http.StatusInternalServerError, err.Error())
		}
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// --- preferences endpoint ----------------------------------------------------

// handlePrefs returns the user's personal defaults.
func (T *Account) handlePrefs(c *jsonapi.Call) (any, error) {
	db, user := AuthDB(), c.User
	return map[string]any{
		"notify":            AuthGetNotifyDefault(db, user),
		"private_mode":      AuthGetPrivateMode(db, user),
		"inferred_disabled": AuthGetInferredDisabled(db, user),
		"timezone":          AuthGetUserTimezone(db, user),
		// RESOLVED, so the form shows what will actually happen rather than
		// a blank that means "nobody has asked you yet". Saving from here
		// then stores it explicitly, which is how never-chosen becomes a
		// decision without anybody being surprised by the default.
		"notify_forward": ResolveNotifyForward(db, user),
	}, nil
}

// handlePrefsSave updates whichever fields are present (the FormPanel
// auto-saves per toggle).
func (T *Account) handlePrefsSave(c *jsonapi.Call, req struct {
	Notify           *bool   `json:"notify,omitempty"`
	PrivateMode      *bool   `json:"private_mode,omitempty"`
	InferredDisabled *bool   `json:"inferred_disabled,omitempty"`
	Timezone         *string `json:"timezone,omitempty"`
	NotifyForward    *string `json:"notify_forward,omitempty"`
}) (any, error) {
	db, user := AuthDB(), c.User
	if req.NotifyForward != nil {
		AuthSetNotifyForward(db, user, *req.NotifyForward)
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
			return nil, jsonapi.BadRequest(err.Error())
		} else {
			AuthSetUserTimezone(db, user, iana)
		}
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handlePassword lets a logged-in user change their OWN password: POST
// {current, new}. The CURRENT password is verified (so a borrowed/hijacked
// session can't silently reset it), and only the hash is updated — admin flag,
// apps, and preferences are preserved. 204 on success; 400 on weak input; 403
// when the current password is wrong.
func (T *Account) handlePassword(c *jsonapi.Call, req struct {
	Current string `json:"current"`
	New     string `json:"new"`
}) (any, error) {
	// Match the signup/reset policy (6–128 chars).
	if len(req.New) < 6 {
		return nil, jsonapi.BadRequest("New password must be at least 6 characters.")
	}
	if len(req.New) > 128 {
		return nil, jsonapi.BadRequest("New password is too long.")
	}
	if !AuthChangePassword(AuthDB(), c.User, req.Current, req.New, c.R) {
		return nil, jsonapi.Forbidden("Current password is incorrect.")
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// --- page --------------------------------------------------------------------

func (T *Account) servePage(w http.ResponseWriter, r *http.Request) {
	// The page is the bare path only. Everything else under it that no
	// route claimed is a wrong address: answering it with the page and a 200
	// hid a mistyped API path behind a page of HTML.
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	// Each forwarding option says whether it can actually deliver, and why not.
	// An option that silently does nothing is worse than one that is not
	// offered: you turn it on, believe you will be told, and are not. This page
	// is built per request, so it can answer for THIS account rather than for
	// the deployment in general.
	forwardOptions := notifyForwardOptions(user)

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
						Help:   "Mask network-capable tools by default, which keeps turns local.",
						Detail: "Web search, fetch and the like. Per-agent overrides still apply."},
					{Field: "inferred_disabled", Label: "Fresh by default", Type: "toggle",
						Help:   "Agents answer without the notes they gathered from past conversations, unless you switch Fresh off.",
						Detail: "Agents then answer fresh from your question and knowledge, without prior derived findings. Per-agent overrides still apply."},
					{Field: "notify_forward", Label: "Forward notifications", Type: "select",
						Options: forwardOptions,
						Help:    "Notifications are always kept. This is whether they also reach you somewhere else.",
						Detail:  "Everything an agent tells you out of band is a notification, including anything it sends with notify_me, and this is the one setting that decides where those go. Forwarded copies are prefixed [agent@service] so a text on your phone says what it came from. Only the FIRST time something happens is forwarded; a repeat raises the count on the notice instead, because a surface that texts you twenty-four times a day is one you switch off before it is ever useful. Email needs mail configured and an account that is an email address; phone needs the messaging bridge."},
					{Field: "timezone", Label: "Timezone", Type: "select",
						Options: TimezoneSelectOptions("System default"),
						Help:    "Your personal timezone. Blank uses the system default.",
						Detail:  "It sets the times agents see, and the day boundaries of schedules you own."},
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
	// Connected accounts, API credentials, and Extensions › Tools moved to the Gateways
	// app (the per-user "outward reach" surface). Account keeps identity +
	// preferences, including inbound personal access tokens below (the keys an
	// external MCP client uses to reach THIS user's agents).
	sections = append(sections,
		ui.Section{
			Title:    "API keys (personal access)",
			Subtitle: "Tokens for connecting an external client to your own gohort agents.",
			Detail:   "For example Claude Desktop over MCP, a voice platform over the OpenAI-compatible /v1 endpoint, or an Ollama-protocol app over the Ollama proxy. Send it as the client's X-API-Key header, or as \"Authorization: Bearer <token>\". It is shown once at creation and can be revoked any time. Keys expire after the period your administrator sets; when one does, create a new one.\n\nEach key is SCOPED under Configure access: what it connects with (the Model API, MCP), and what it can reach (your agents, an app's agents, models, channels). It reaches only what is ticked, whichever way it connects. A new key reaches nothing until you tick something. Keys created before scoping existed are marked Unrestricted; set a scope to lock them down.",
			Body:     ui.Card{HTML: tokensHTML},
		},
		artifactsSection(),
	)
	extraHead += artifactsHead()
	ui.Page{
		Title:         "Account",
		ShowTitle:     true,
		BackURL:       "/",
		MaxWidth:      "640px",
		Sections:      sections,
		ExtraHeadHTML: extraHead,
	}.ServeHTTP(w, r)
}

// handleTokens lists the user's personal access tokens, secrets masked.
func (T *Account) handleTokens(c *jsonapi.Call) (any, error) {
	// Swept here rather than on a timer: the list is the only place the
	// rows are read, so it is the moment the cleanup is owed, and it keeps
	// this file free of a goroutine whose failure mode is silent.
	SweepExpiredAccountTokens()
	// Each with the kind it is for, read off its scope, so the list can say
	// it and the editor knows which kind it is editing.
	type keyRow struct {
		AccountToken
		Kind string `json:"kind"`
	}
	rows := []keyRow{}
	for _, t := range ListAccountTokens(c.User) {
		rows = append(rows, keyRow{AccountToken: t, Kind: t.Scope.Kind()})
	}
	return rows, nil
}

// tokenPost tells /api/tokens' two POST shapes apart: ?action=scope rescopes
// an existing key, anything else mints a new one. The full secret is returned
// ONLY by the mint.
func (T *Account) tokenPost() http.HandlerFunc {
	scope := jsonapi.Post(T.DB, T.handleTokenScope)
	mint := jsonapi.Post(T.DB, T.handleTokenMint)
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(r.URL.Query().Get("action")) == "scope" {
			scope(w, r)
			return
		}
		mint(w, r)
	}
}

// handleTokenScope rescopes the ?id= key to what the editor offers, as the
// one kind it is for.
func (T *Account) handleTokenScope(c *jsonapi.Call, body struct {
	Kind  string     `json:"kind"`
	Scope TokenScope `json:"scope"`
}) (any, error) {
	id := strings.TrimSpace(c.R.URL.Query().Get("id"))
	scope, err := T.kindScope(c.User, body.Kind, &body.Scope)
	if err != nil {
		return nil, err
	}
	// A kind no longer made (the desktop app's) stays editable on a key that
	// already is one, and no other key can be turned into one: rescoping was
	// the way round the refusal minting makes.
	if k := scope.Kind(); k != "" && !slices.Contains(keyKinds(c.User), k) && storedKeyKind(c.User, id) != k {
		return nil, jsonapi.BadRequest("a key cannot be changed into that kind: the desktop app makes its own key when you sign in to it")
	}
	if !SetAccountTokenScope(c.User, id, scope) {
		return nil, jsonapi.NotFound("token not found")
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// storedKeyKind is the kind owner's key id is now, or "" when there is no such
// key.
func storedKeyKind(owner, id string) string {
	for _, t := range ListAccountTokens(owner) {
		if t.ID == id {
			return t.Scope.Kind()
		}
	}
	return ""
}

// tokenMintBody is a key to mint: its name, the one kind it is for, and its
// scope within that kind.
type tokenMintBody struct {
	Name  string      `json:"name"`
	Kind  string      `json:"kind"`
	Scope *TokenScope `json:"scope"`
}

// keyKinds are the kinds of key this user may make: a kind is offered when
// the administrator has granted them the door it opens. Not the desktop app:
// signing in to it provisions the key it needs (core desktop_key.go), so a
// key made by hand for it was a second way to do what sign-in does. A
// desktop key made before keeps working and can still be edited.
func keyKinds(user string) []string {
	var out []string
	for _, k := range []string{KeyKindAgents, KeyKindMCP, KeyKindModels, KeyKindOutside} {
		if kindOpen(user, k) {
			out = append(out, k)
		}
	}
	return out
}

// kindOpen reports whether the administrator has granted this user the door a
// kind of key opens.
func kindOpen(user, kind string) bool {
	doors := KeyKindDoors(kind)
	return len(doors) > 0 && FeatureAllowedForUser(RootDB, doors[0], user)
}

// kindScope is the scope a key of one kind is saved with: that kind's door,
// and of what was posted only what the kind reaches and the user may grant.
// Anything of another kind is dropped, so a key does one thing. No kind named
// means the one the posted scope reads as; a scope that reads as several, or a
// kind this user cannot make, is refused. A scope that opens nothing stays
// empty, the deny-by-default key.
func (T *Account) kindScope(user, kind string, posted *TokenScope) (*TokenScope, error) {
	if posted == nil {
		posted = &TokenScope{}
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = posted.Kind()
	}
	switch kind {
	case "":
		return &TokenScope{}, nil
	case KeyKindSeveral:
		return nil, jsonapi.BadRequest("a key does one thing: choose its kind (the Model API to your agents, MCP, raw models, the desktop app, or people outside gohort) and make another key for the rest")
	}
	if !kindOpen(user, kind) {
		return nil, jsonapi.BadRequest("that kind of key is not open to you: an administrator grants it under Feature Access")
	}
	want := &TokenScope{Features: KeyKindDoors(kind)}
	reaches := func(t string) bool {
		t = strings.TrimSpace(t)
		switch kind {
		case KeyKindAgents:
			return strings.HasPrefix(t, "agent:") || strings.HasPrefix(t, "channel:")
		case KeyKindMCP, KeyKindOutside:
			return strings.HasPrefix(t, "agent:")
		case KeyKindModels:
			return IsRawTier(t)
		}
		return false
	}
	for _, t := range posted.Targets {
		if reaches(t) {
			want.Targets = append(want.Targets, t)
		}
	}
	if kind == KeyKindAgents || kind == KeyKindMCP {
		for _, f := range posted.Features {
			if strings.HasPrefix(strings.TrimSpace(f), "app:") {
				want.Features = append(want.Features, f)
			}
		}
	}
	if kind == KeyKindMCP {
		want.Tools = posted.Tools
	}
	want.Outside = kind == KeyKindOutside
	return T.grantableScope(user, want), nil
}

// handleTokenMint mints a key and returns its secret, once. How long a key
// lives is not the owner's to choose: it is the admin's API key lifetime
// (core AccountKeyLifetime).
func (T *Account) handleTokenMint(c *jsonapi.Call, req *tokenMintBody) (any, error) {
	if req == nil {
		req = &tokenMintBody{}
	}
	// New keys are minted deny-by-default: an explicit (possibly empty)
	// scope, never the nil legacy-unrestricted case. A client that sends no
	// scope still gets an empty one, so it reaches nothing until scoped.
	scope, err := T.kindScope(c.User, req.Kind, req.Scope)
	if err != nil {
		return nil, err
	}
	if k := scope.Kind(); k != "" && !slices.Contains(keyKinds(c.User), k) {
		return nil, jsonapi.BadRequest("a new key cannot be of that kind: the desktop app makes its own key when you sign in to it")
	}
	return MintAccountTokenScoped(c.User, req.Name, scope), nil // secret returned once
}

// handleTokenRevoke revokes the ?id= key.
func (T *Account) handleTokenRevoke(c *jsonapi.Call, _ struct{}) (any, error) {
	id := strings.TrimSpace(c.R.URL.Query().Get("id"))
	if id == "" {
		return nil, jsonapi.BadRequest("missing id")
	}
	RevokeAccountToken(c.User, id)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// grantableScope keeps only what the scope editor offers this user: features
// the admin permits them, and targets from their own grantable list. The
// editor never shows anything else, but the save took whatever was posted, so
// a key could name another user's agent or a feature the admin had withheld.
func (T *Account) grantableScope(user string, scope *TokenScope) *TokenScope {
	if scope == nil {
		return nil
	}
	out := *scope
	feats := map[string]bool{}
	for _, f := range ShareableFeatures() {
		if FeatureAllowedForUser(RootDB, f.Key, user) {
			feats[f.Key] = true
		}
	}
	targets := map[string]bool{}
	for _, t := range ListExternalTargets(T.DB, user) {
		targets[t.Value] = true
	}
	out.Features, out.Targets = nil, nil
	for _, f := range scope.Features {
		if feats[strings.TrimSpace(f)] {
			out.Features = append(out.Features, strings.TrimSpace(f))
		}
	}
	for _, t := range scope.Targets {
		if targets[strings.TrimSpace(t)] {
			out.Targets = append(out.Targets, strings.TrimSpace(t))
		}
	}
	return &out
}

// handleTokenTargets feeds the per-key scope editor: which FEATURES this user
// may enable on a key (the registered shareable features the admin permits
// them), and which TARGETS a key may reach (their own + shared-to-them exposed
// agents, channels, and the raw tiers). A feature the admin has NOT granted
// this user is omitted, so the editor can't offer a key something the admin
// gate would reject anyway.
func (T *Account) handleTokenTargets(c *jsonapi.Call) (any, error) {
	user := c.User
	// Kind is where the editor puts a feature: "door" (a way a client
	// connects: the Model API, MCP), "app" (an app's agents, which are
	// something a key REACHES, listed with the agents), or "lends" (the
	// Desktop bridge, which lends this computer's tools and reaches nothing).
	type feat struct {
		Key   string `json:"key"`
		Label string `json:"label"`
		Kind  string `json:"kind"`
	}
	feats := []feat{}
	for _, f := range ShareableFeatures() {
		if FeatureAllowedForUser(RootDB, f.Key, user) {
			feats = append(feats, feat{Key: f.Key, Label: f.Label, Kind: featureKind(f.Key)})
		}
	}
	// The MCP tools a key may be narrowed to. Built-ins are always present;
	// app-contributed ones appear only while the admin has them exposed, so the
	// picker can't offer a key something the server-side gate would refuse.
	type mcpTool struct {
		Name  string `json:"name"`
		Label string `json:"label"`
	}
	tools := []mcpTool{}
	for _, t := range MCPBuiltinTools() {
		tools = append(tools, mcpTool{Name: t.Name, Label: t.Label})
	}
	for _, st := range MCPAppToolStatuses() {
		if st.Exposed {
			tools = append(tools, mcpTool{Name: st.Name, Label: st.Name})
		}
	}
	return map[string]any{
		"kinds":    keyKinds(user),
		"features": feats,
		"targets":  ListExternalTargets(T.DB, user),
		"tools":    tools,
	}, nil
}

// featureKind sorts a shareable feature for the key editor (handleTokenTargets).
func featureKind(key string) string {
	switch {
	case strings.HasPrefix(key, "app:"):
		return "app"
	case key == "desktop": // core's Desktop bridge feature
		return "lends"
	}
	return "door"
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
.acct-pw-input { background:var(--bg-0); color:var(--text); border:1px solid var(--border); border-radius:var(--btn-radius, 6px); padding:0.4rem 0.55rem; font:inherit; font-size:var(--fs-md, 0.9rem); }
.acct-pw-row { display:flex; align-items:center; gap:0.6rem; }
.acct-pw-msg { font-size:var(--fs-sm, 0.82rem); }
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
.acct-tok-sub { font-size:var(--fs-xs, 0.75rem); color:var(--text-mute); margin-top:0.1rem; }
.acct-tok-code { font-family:ui-monospace,SFMono-Regular,Menlo,monospace; }
.acct-tok-btn { cursor:pointer; background:var(--bg-2); color:var(--text-mute); border:1px solid var(--border); border-radius:var(--btn-radius, 6px); padding:0.3rem 0.7rem; font:inherit; font-size:var(--fs-sm, 0.8rem); }
@media (pointer: coarse) { .acct-tok-btn { min-height:var(--btn-h-sm, 2.25rem); } }
.acct-tok-btn:hover { color:var(--danger); border-color:var(--danger); }
.acct-tok-input { flex:1; background:var(--bg-0); color:var(--text); border:1px solid var(--border); border-radius:var(--btn-radius, 6px); padding:0.4rem 0.55rem; font:inherit; font-size:var(--fs-sm, 0.85rem); }
.acct-tok-reveal { border:1px solid var(--accent); border-radius:8px; padding:0.7rem 0.8rem; background:var(--bg-2); }
.acct-tok-reveal code { font-family:ui-monospace,SFMono-Regular,Menlo,monospace; font-size:var(--fs-sm, 0.82rem); color:var(--text); word-break:break-all; display:block; margin-top:0.3rem; }
.acct-tok-empty { color:var(--text-mute); font-style:italic; padding:0.4rem 0; }
.acct-tok-badge { font-size:var(--fs-2xs, 0.72rem); font-weight:600; padding:0.08rem 0.45rem; border-radius:999px; margin-left:0.4rem; }
.acct-tok-badge.warn { background:rgba(220,160,40,0.18); color:#c88a1e; }
.acct-tok-scope { border:1px solid var(--border); border-radius:8px; padding:0.6rem 0.75rem; margin-top:0.35rem; background:var(--bg-2); display:flex; flex-direction:column; gap:0.5rem; }
.acct-tok-scope h4 { margin:0; font-size:var(--fs-2xs, 0.72rem); text-transform:uppercase; letter-spacing:0.03em; color:var(--text-mute); }
.acct-tok-scope-grp { display:flex; flex-direction:column; gap:0.2rem; }
.acct-tok-chk { display:flex; align-items:center; gap:0.45rem; font-size:var(--fs-sm, 0.83rem); color:var(--text); }
.acct-tok-chk input { margin:0; }
.acct-tok-scope-note { font-size:var(--fs-xs, 0.75rem); color:var(--text-mute); font-style:italic; }
.acct-tok-scope-sub { font-size:var(--fs-xs, 0.75rem); color:var(--text); font-weight:600; margin-top:0.2rem; }
.acct-tok-scope-block { display:flex; flex-direction:column; gap:0.5rem; }
.acct-tok-scope-actions { display:flex;flex-wrap:wrap; gap:0.5rem; align-items:center; }
/* A key's kind, in its colour: the pill on its row, the edge of the row and
   of its editor, the card that chose it. */
.acct-tok { border-left:3px solid var(--tone, var(--border)); }
.acct-tok-kind { display:inline-block; white-space:nowrap; font-size:var(--fs-2xs, 0.72rem); font-weight:600; padding:0.08rem 0.5rem; border-radius:999px; margin-left:0.45rem;
  color:var(--tone); border:1px solid color-mix(in srgb, var(--tone) 45%, transparent); background:color-mix(in srgb, var(--tone) 12%, transparent); }
.acct-tok-scope { border-left:3px solid var(--tone, var(--border)); background:linear-gradient(155deg, color-mix(in srgb, var(--tone, var(--border)) 8%, var(--bg-2)) 0%, var(--bg-2) 55%); }
.acct-tok-edit { display:flex; flex-direction:column; gap:0.55rem; margin-top:0.35rem; padding:0.6rem 0.75rem; border:1px dashed var(--border); border-radius:8px; }
.acct-tok-newbtn { align-self:flex-start; margin-top:0.3rem; }
.acct-tok-newform { display:flex; flex-direction:column; gap:0.6rem; }
.acct-tok-kinds { display:grid; grid-template-columns:repeat(auto-fill, minmax(min(190px, 100%), 1fr)); gap:0.5rem; }
.acct-tok-kindcard { --tone: var(--accent); display:flex; flex-direction:column; gap:0.25rem; text-align:left; cursor:pointer; font:inherit;
  padding:0.6rem 0.7rem; border-radius:8px; color:var(--text); background:var(--bg-2);
  border:1px solid var(--border); border-left:3px solid var(--tone); }
.acct-tok-kindcard:hover { border-color:color-mix(in srgb, var(--tone) 55%, var(--border)); border-left-color:var(--tone); }
.acct-tok-kindcard[aria-checked="true"] { border-color:var(--tone); background:color-mix(in srgb, var(--tone) 12%, var(--bg-2)); box-shadow:0 0 0 1px var(--tone) inset; }
.acct-tok-kindcard:focus-visible { outline:2px solid var(--accent); outline-offset:2px; }
.acct-tok-kindcard-name { font-weight:600; font-size:var(--fs-sm, 0.85rem); color:var(--text-hi, var(--text)); }
.acct-tok-kindcard-desc { font-size:var(--fs-xs, 0.75rem); color:var(--text-mute); line-height:1.4; }
</style>
<script>
(function(){
  var root = document.getElementById('acct-tokens');
  if (root && window.uiLoading) { root.textContent = ''; root.appendChild(window.uiLoading()); }
  if (!root) return;
  var CAT = null; // {features:[{key,label}], targets:[{value,label,group}]}, loaded once
  function el(tag, attrs, kids){ var n=document.createElement(tag); if(attrs) for(var k in attrs){ if(k==='text') n.textContent=attrs[k]; else n.setAttribute(k,attrs[k]); } (kids||[]).forEach(function(c){ n.appendChild(typeof c==='string'?document.createTextNode(c):c); }); return n; }
  function targets(){ return fetch('api/token-targets',{credentials:'same-origin'}).then(function(r){return r.json();}).then(function(d){ CAT=d||{features:[],targets:[],tools:[]}; }).catch(function(){ CAT={features:[],targets:[],tools:[]}; }); }
  function load(){ return fetch('api/tokens',{credentials:'same-origin'}).then(function(r){return r.json();}).then(render).catch(function(){ root.textContent='Failed to load.'; }); }

  // checkbox helper: returns {row, checked()} for one option.
  function chk(label, value, on){ var box=el('input',{type:'checkbox'}); if(on) box.checked=true; box.setAttribute('data-v',value); return { row: el('label',{class:'acct-tok-chk'},[box, document.createTextNode(label)]), box: box }; }

  // A key does one thing: it is of one kind, chosen when it is made. Each
  // kind has its own colour, a line saying what it is for, and only its own
  // choices below. The kinds offered are the ones an administrator has opened
  // to this account (CAT.kinds).
  var KINDS = {
    agents:  {label:'Model API', tone:'var(--accent)', desc:'Your agents, app agents and channels, from any client that speaks the OpenAI API.'},
    mcp:     {label:'MCP', tone:'var(--accent-2, var(--accent))', desc:'Your agents and their tools, from an MCP client such as Claude Desktop.'},
    models:  {label:'Raw models', tone:'var(--warning)', desc:'The worker and lead models with no agent in front, over the Model API or the model proxy. No persona, no guardrails: raw compute.'},
    desktop: {label:'Desktop app', tone:'var(--accent-3, var(--accent))', desc:'The gohort desktop app signs in with it and lends the computer it runs on to your agents, as tools. It reaches no agent.'},
    outside: {label:'People outside gohort', tone:'var(--success)', desc:'A platform such as a chat widget, calling the Model API from its own server for people who are not gohort users.'}
  };
  function kindPill(kind){
    if(kind === 'several') return el('span',{class:'acct-tok-badge warn',text:'Several kinds'});
    var k = KINDS[kind]; if(!k) return null;
    var p = el('span',{class:'acct-tok-kind',text:k.label}); p.style.setProperty('--tone', k.tone); return p;
  }
  // The kind choice: one card per kind this account may make. onPick(kind)
  // runs on a choice. Returns the node.
  function kindPicker(current, onPick){
    var wrap = el('div',{class:'acct-tok-kinds',role:'radiogroup'});
    var offered = (CAT.kinds||[]).filter(function(k){ return KINDS[k]; });
    if(!offered.length){
      wrap.appendChild(el('div',{class:'acct-tok-scope-note',text:'No kind of key is open to you yet. An administrator grants them under Feature Access.'}));
      return wrap;
    }
    offered.forEach(function(k){
      var card = el('button',{type:'button',class:'acct-tok-kindcard',role:'radio','aria-checked': String(k===current)});
      card.style.setProperty('--tone', KINDS[k].tone);
      card.appendChild(el('span',{class:'acct-tok-kindcard-name',text:KINDS[k].label}));
      card.appendChild(el('span',{class:'acct-tok-kindcard-desc',text:KINDS[k].desc}));
      card.addEventListener('click', function(){
        wrap.querySelectorAll('.acct-tok-kindcard').forEach(function(c){ c.setAttribute('aria-checked','false'); });
        card.setAttribute('aria-checked','true');
        onPick(k);
      });
      wrap.appendChild(card);
    });
    return wrap;
  }

  // The choices for one kind of key, coloured with it. selected = the key's
  // scope, or null for a new key. Returns {node, collect()} where collect()
  // reads what is ticked as {kind, scope}; the server adds the kind's door.
  function scopeEditor(selected, kind){
    selected = selected || {features:[], targets:[]};
    var selF = {}, selT = {};
    (selected.features||[]).forEach(function(f){ selF[f]=true; });
    (selected.targets||[]).forEach(function(t){ selT[t]=true; });
    var boxes = { features: [], targets: [], tools: [] };
    var wrap = el('div',{class:'acct-tok-scope'});
    wrap.style.setProperty('--tone', (KINDS[kind]||{}).tone || 'var(--border)');
    var feats = CAT.features || [];
    function addFeature(grp, f, label){ var c=chk(label||f.label||f.key, f.key, !!selF[f.key]); boxes.features.push(c); grp.appendChild(c.row); return c; }
    function addTarget(grp, t){ var c=chk(t.label||t.value, t.value, !!selT[t.value]); boxes.targets.push(c); grp.appendChild(c.row); return c; }
    function group(title, note){
      wrap.appendChild(el('h4',{text:title}));
      if(note) wrap.appendChild(el('div',{class:'acct-tok-scope-note',text:note}));
      var g = el('div',{class:'acct-tok-scope-grp'}); wrap.appendChild(g); return g;
    }
    var byGroup = {};
    (CAT.targets||[]).forEach(function(t){ var g=t.group||'Other'; (byGroup[g]=byGroup[g]||[]).push(t); });
    function agentsGroup(note){
      var agents = byGroup['Agents'] || [];
      var ag = group('Your agents', agents.length ? note : 'You have no agents to offer yet.');
      agents.forEach(function(t){ addTarget(ag, t); });
    }
    function appsGroup(){
      var apps = feats.filter(function(f){ return f.kind === 'app'; });
      if(!apps.length) return;
      var ap = group('App agents');
      apps.forEach(function(f){ addFeature(ap, f, (f.label||f.key).replace(/\s*\(app agents\)\s*$/,'')); });
    }

    var toolsTouched = false;
    if(kind === 'agents'){
      agentsGroup('Only the ticked ones.');
      appsGroup();
      if((byGroup['Channels']||[]).length){
        var cg = group('Channels', 'Join a conversation one of your agents serves.');
        byGroup['Channels'].forEach(function(t){ addTarget(cg, t); });
      }
    } else if(kind === 'mcp'){
      agentsGroup('Only the ticked ones.');
      appsGroup();
      // A key with no explicit tool list is NOT narrowed (every exposed tool),
      // which is why an untouched editor sends no tools rather than an empty
      // list: an empty list means "none", and the two must not be the same
      // value on the wire.
      if(CAT.tools && CAT.tools.length){
        var selTools = selected.tools; // null/undefined = not narrowed
        var narrowed = !!selTools;
        var sel = {}; (selTools||[]).forEach(function(n){ sel[n]=true; });
        var mg = group('Tools it may call', null);
        var note = el('div',{class:'acct-tok-scope-note',text: narrowed ? 'Only the ticked tools.' : 'Not narrowed: every exposed tool. Untick any to restrict it.'});
        mg.parentNode.insertBefore(note, mg);
        CAT.tools.forEach(function(t){
          var c = chk(t.label||t.name, t.name, narrowed ? !!sel[t.name] : true);
          c.box.addEventListener('change', function(){ toolsTouched = true; note.textContent='Only the ticked tools.'; });
          boxes.tools.push(c); mg.appendChild(c.row);
        });
      }
    } else if(kind === 'models'){
      var tiers = byGroup['Tiers'] || [];
      var tg = group('Models', tiers.length ? 'Over the Model API, only the ticked ones. The model proxy takes the key either way.' : 'No model tier is open to you.');
      tiers.forEach(function(t){ addTarget(tg, t); });
    } else if(kind === 'desktop'){
      wrap.appendChild(el('div',{class:'acct-tok-scope-note',text:'Nothing to choose. The desktop app now makes its own key when you sign in to it, so this one is only needed by an app that still uses it.'}));
    } else if(kind === 'outside'){
      agentsGroup('Only agents set to serve people outside gohort answer, and only the ticked ones.');
      wrap.appendChild(el('div',{class:'acct-tok-scope-note',text:'Every call names the person it is for, in the X-End-User header or the request\'s user field, and each person gets their own memory and conversations, apart from yours and from each other\'s. Never put this key in a browser or an app people can open: whoever holds it can speak as anyone.'}));
    }

    function collect(){
      var f=[], t=[], m=[];
      boxes.features.forEach(function(c){ if(c.box.checked) f.push(c.box.getAttribute('data-v')); });
      boxes.targets.forEach(function(c){ if(c.box.checked) t.push(c.box.getAttribute('data-v')); });
      boxes.tools.forEach(function(c){ if(c.box.checked) m.push(c.box.getAttribute('data-v')); });
      var scope = { features: f, targets: t };
      // Only send a tools list once the user has touched it, or when the key
      // already had one, so a key never narrowed stays un-narrowed instead of
      // being pinned to today's tool set.
      if(kind === 'mcp' && (toolsTouched || selected.tools)){ scope.tools = m; }
      return { kind: kind, scope: scope };
    }
    return { node: wrap, collect: collect };
  }

  // Age of a key's last use, in words. A key nobody has used is the one worth
  // revoking, and it was impossible to tell before because nothing recorded it.
  function lastUsed(t){
    if(!t.last_seen) return 'never used';
    var days = Math.floor((Date.now() - Date.parse(t.last_seen)) / 86400000);
    if(isNaN(days)) return 'never used';
    if(days <= 0) return 'used today';
    if(days === 1) return 'used yesterday';
    if(days < 60) return 'used '+days+' days ago';
    return 'unused for '+Math.floor(days/30)+' months';
  }

  // Deadline text, and whether it is close enough to say so loudly.
  function expiryText(t){
    if(!t.expires) return 'no expiry';
    var days = Math.ceil((Date.parse(t.expires) - Date.now()) / 86400000);
    if(isNaN(days)) return 'no expiry';
    if(days <= 0) return 'expired';
    if(days === 1) return 'expires tomorrow';
    return 'expires in '+days+' days';
  }

  // What the key reaches, in one line: its kind says the rest.
  function scopeSummary(t){
    if(!t.scope) return null; // legacy: rendered as a badge instead
    var kind = t.kind;
    if(!kind) return 'Opens nothing yet: open Access';
    if(kind === 'several') return 'holds several kinds: open Access to choose the one it is for';
    if(kind === 'desktop') return 'for the desktop app';
    var n = (t.scope.targets||[]).length;
    (t.scope.features||[]).forEach(function(f){ if(f.indexOf('app:') === 0) n++; });
    if(kind === 'models') return n ? 'reaches ' + n + ' model' + (n>1?'s':'') : 'the model proxy only';
    return 'reaches ' + (n ? n + ' thing' + (n>1?'s':'') : 'nothing yet');
  }

  function render(list){
    root.innerHTML='';
    list = list || [];
    if(!list.length){ root.appendChild(el('div',{class:'acct-tok-empty',text:'No API keys yet. Create one to connect an external client.'})); }
    list.forEach(function(t){
      var nameRow = el('div',{class:'acct-tok-name'},[ document.createTextNode(t.name || '(unnamed)') ]);
      if(!t.scope){ nameRow.appendChild(el('span',{class:'acct-tok-badge warn',text:'Unrestricted'})); }
      else { var pill = kindPill(t.kind); if(pill) nameRow.appendChild(pill); }
      var subKids = [ el('span',{class:'acct-tok-code',text: t.token || ''}), document.createTextNode('  -  created '+String(t.created||'').slice(0,10)) ];
      subKids.push(document.createTextNode('  -  '+lastUsed(t)));
      var exp = expiryText(t);
      if(t.expires){
        var expDays = Math.ceil((Date.parse(t.expires) - Date.now()) / 86400000);
        subKids.push(el('span',{class:'acct-tok-badge'+(expDays<=7?' warn':''),text:exp}));
      }
      var sum = scopeSummary(t);
      if(sum){ subKids.push(document.createTextNode('  -  '+sum)); }
      else if(!t.scope){ subKids.push(document.createTextNode('  -  reaches everything (open Access to give it one kind)')); }
      var meta = el('div',{class:'acct-tok-meta'},[ nameRow, el('div',{class:'acct-tok-sub'}, subKids) ]);

      var scopeBtn = el('button',{class:'acct-tok-btn',style:'color:var(--text-mute)',text:'Access'});
      var del = el('button',{class:'acct-tok-btn',text:'Revoke'});
      var rowEl = el('div',{class:'acct-tok'},[meta,scopeBtn,del]);
      if(t.scope && KINDS[t.kind]) rowEl.style.setProperty('--tone', KINDS[t.kind].tone);
      var holder = el('div',{},[rowEl]); // row + (optional) inline editor
      del.addEventListener('click',function(){
        var go = window.uiConfirm ? window.uiConfirm('Revoke this API key? Any client using it stops working.') : Promise.resolve(true);
        go.then(function(ok){ if(!ok) return; fetch('api/tokens?id='+encodeURIComponent(t.id),{method:'DELETE',credentials:'same-origin'}).then(load); });
      });
      scopeBtn.addEventListener('click',function(){
        var existing = holder.querySelector('.acct-tok-edit');
        if(existing){ existing.parentNode.removeChild(existing); return; }
        var box = el('div',{class:'acct-tok-edit'});
        var slot = el('div',{});
        var msg = el('span',{class:'acct-tok-scope-note'});
        var save = el('button',{class:'ui-row-btn primary',text:'Save'});
        var ed = null;
        function show(kind){
          slot.innerHTML = '';
          ed = scopeEditor(t.scope, kind);
          slot.appendChild(ed.node);
          save.disabled = false;
        }
        if(KINDS[t.kind]){
          // A key's kind is fixed: another kind is another key.
          box.appendChild(el('div',{class:'acct-tok-scope-note',text:KINDS[t.kind].desc + ' For anything else, make another key.'}));
          show(t.kind);
        } else {
          box.appendChild(el('div',{class:'acct-tok-scope-note',text: t.scope ? 'This key holds more than one kind, or none yet. Choose the one it is for: what belongs to the others is removed when you save.' : 'This key predates scoping and reaches everything. Choose the one kind it is for.'}));
          box.appendChild(kindPicker(null, show));
          save.disabled = true;
        }
        box.appendChild(slot);
        save.addEventListener('click',function(){
          if(!ed) return;
          save.disabled=true; save.textContent='Saving…'; msg.textContent='';
          fetch('api/tokens?action=scope&id='+encodeURIComponent(t.id),{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json'},body:JSON.stringify(ed.collect())})
            .then(function(r){ if(r.status===204){ load(); return; } return r.text().then(function(x){ throw new Error(x || ('HTTP '+r.status)); }); })
            .catch(function(e){ save.disabled=false; save.textContent='Save'; msg.textContent=e.message; });
        });
        box.appendChild(el('div',{class:'acct-tok-scope-actions'},[save, msg]));
        holder.appendChild(box);
      });
      root.appendChild(holder);
    });

    // A new key: one button, and a dialog asking its name and what it is
    // for. With one kind open (MCP alone, for most people) there is nothing
    // to choose, so the dialog goes straight to that kind's options.
    var newBtn = el('button',{class:'ui-row-btn primary acct-tok-newbtn',text:'+ New API key'});
    newBtn.addEventListener('click', openNew);
    root.appendChild(newBtn);
  }

  function openNew(){
    var offered = (CAT.kinds||[]).filter(function(k){ return KINDS[k]; });
    window.uiOpenSimpleModal({
      title: 'New API key',
      subtitle: offered.length === 1 ? KINDS[offered[0]].label + ' key' : 'Each key does one thing. Make another for anything else.',
      width: '620px',
      mount: function(body, dlg){
        var form = el('div',{class:'acct-tok-newform'});
        body.appendChild(form);
        var nameInput = el('input',{type:'text',class:'acct-tok-input',placeholder:'Name (e.g. Claude Desktop)'});
        form.appendChild(nameInput);
        var slot = el('div',{});
        var msg = el('div',{class:'acct-tok-scope-note'});
        var pending = null;
        var create = el('button',{class:'ui-row-btn primary',text:'Create key'});
        create.disabled = true;
        function show(kind){
          slot.innerHTML = '';
          pending = scopeEditor(null, kind);
          slot.appendChild(pending.node);
          create.disabled = false;
        }
        if(!offered.length){
          form.appendChild(el('div',{class:'acct-tok-scope-note',text:'No kind of key is open to you yet. An administrator grants them under Feature Access.'}));
        } else if(offered.length === 1){
          form.appendChild(el('div',{class:'acct-tok-scope-note',text:KINDS[offered[0]].desc}));
          show(offered[0]);
        } else {
          form.appendChild(kindPicker(null, show));
        }
        form.appendChild(slot);
        form.appendChild(msg);
        create.addEventListener('click', function(){
          if(!pending) return;
          var req = pending.collect();
          req.name = nameInput.value.trim();
          create.disabled = true; create.textContent = 'Creating…'; msg.textContent = '';
          fetch('api/tokens',{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json'},body:JSON.stringify(req)})
            .then(function(r){ if(!r.ok) return r.text().then(function(x){ throw new Error(x || ('HTTP '+r.status)); }); return r.json(); })
            .then(function(t){ dlg.close(); load().then(function(){ reveal(t); }); })
            .catch(function(e){ create.disabled = false; create.textContent = 'Create key'; msg.textContent = e.message; });
        });
        dlg.addAction(create);
        setTimeout(function(){ nameInput.focus(); }, 0);
      }
    });
  }
  function reveal(t){
    if(!t || !t.token) return;
    root.insertBefore(el('div',{class:'acct-tok-reveal'},[ el('div',{class:'acct-tok-sub',text:'Copy this now, it will not be shown again:'}), el('code',{text:t.token}) ]), root.firstChild);
  }
  targets().then(load);
})();
</script>`

// notifyForwardOptions labels each destination with whether it can reach THIS
// account, and says what is missing when it cannot.
//
// Email is the one that surprises people. There is no separate address on an
// account: NotifyUser sends only when the USERNAME is itself a valid email, so
// somebody signed in as "craig" can never receive one however the deployment's
// mail is configured. That is the existing contract everywhere in the app and
// it is invisible from here, which is exactly the kind of thing a settings page
// exists to say out loud.
//
// Unavailable options stay in the list rather than being dropped. A stored
// choice must still render as itself, and a setting that silently disappears
// when its transport goes down is one nobody can reason about; saying why is
// more useful than pretending the option never existed.
func notifyForwardOptions(user string) []ui.SelectOption {
	emailWhy := ""
	switch {
	case !strings.Contains(user, "@"):
		emailWhy = "unavailable: your username is not an email address"
	case !EmailConfigured():
		emailWhy = "unavailable: mail is not configured for this deployment"
	}
	phoneWhy := ""
	if NoticePhoneReady == nil || !NoticePhoneReady(user) {
		phoneWhy = "unavailable: no messaging bridge with your handle"
	}
	label := func(base, why string) string {
		if why == "" {
			return base
		}
		return base + " (" + why + ")"
	}
	both := ""
	switch {
	case emailWhy != "" && phoneWhy != "":
		both = "unavailable: neither is set up"
	case emailWhy != "":
		both = "email half unavailable"
	case phoneWhy != "":
		both = "phone half unavailable"
	}
	return []ui.SelectOption{
		{Value: "off", Label: "Nowhere (keep them in the bell)"},
		{Value: "email", Label: label("Email", emailWhy)},
		{Value: "phone", Label: label("Phone", phoneWhy)},
		{Value: "both", Label: label("Email and phone", both)},
	}
}
