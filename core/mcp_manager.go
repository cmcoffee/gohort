// MCP manager: the SERVER-SIDE Model Context Protocol client. It holds
// the admin-configured list of remote MCP servers, connects to the
// enabled ones over HTTP, and surfaces each server's tools to the agent
// loop as native ChatTools (and, in a later stage, as a ReferenceSource
// for the writer apps' source picker).
//
// This is the server-side counterpart to the per-user stdio host in
// gohort-desktop/mcp. Here the connections are shared and always-on, so
// an org knowledge source (Confluence, etc.) is available to every
// agent and pipeline without a user's laptop in the loop.
//
// Auth reuses the SecureAPI/apiclient OAuth machinery (see
// AuthorizeRequest); static bearer tokens are stored encrypted in the
// same kvlite table as the server config.
package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cmcoffee/gohort/core/mcpclient"
)

const (
	// mcpServersTable holds both the per-server config records (keyed by
	// server name) and the encrypted bearer tokens (keyed name+"__token").
	mcpServersTable = "mcp_servers"

	// mcpHandshakeTimeout bounds initialize + tools/list at connect time.
	mcpHandshakeTimeout = 20 * time.Second
	// mcpCallTimeout bounds a single tools/call. Generous: remote search
	// over a wiki can be slow, but a hung call must not pin a round.
	mcpCallTimeout = 55 * time.Second
)

// MCPAuthMode selects how outbound requests to a server are authorized.
type MCPAuthMode string

const (
	MCPAuthNone   MCPAuthMode = ""           // no credentials
	MCPAuthBearer MCPAuthMode = "bearer"     // static token, stored encrypted
	MCPAuthSecure MCPAuthMode = "secure_api" // a SecureAPI OAuth2 credential
	MCPAuthOAuth  MCPAuthMode = "oauth"      // MCP-spec OAuth 2.1 (hosted SaaS), PER-USER tokens
)

// mcpNameRE constrains server names so they namespace tools cleanly
// (<server>.<tool>) and never collide with the token-key suffix.
var mcpNameRE = regexp.MustCompile(`^[a-z0-9_-]+$`)

// MCPServerConfig is one remote MCP server. Persisted as-is (JSON) under
// its Name; the bearer token, when any, lives encrypted under a sibling
// key and is never part of this struct.
type MCPServerConfig struct {
	Name            string      `json:"name"`
	URL             string      `json:"url"`              // Streamable-HTTP endpoint
	AuthMode        MCPAuthMode `json:"auth_mode"`        // none | bearer | secure_api
	SecureCred      string      `json:"secure_cred"`      // SecureAPI credential name (secure_api mode)
	ExposeTools     bool        `json:"expose_tools"`     // register the server's tools for agents
	ExposeReference bool        `json:"expose_reference"` // expose as a ReferenceSource (Stage 5)
	SearchTool      string      `json:"search_tool"`      // MCP tool used for reference Fetch (default "search")
	Enabled         bool        `json:"enabled"`
}

// mcpConn is a live connection to one server plus the tools it reported.
type mcpConn struct {
	cfg    MCPServerConfig
	client *mcpclient.Client
	tools  []mcpclient.ToolDef
	alive  atomic.Bool
}

// mcpPendingAuth is one in-flight OAuth authorization, keyed by state
// until the callback redeems it.
type mcpPendingAuth struct {
	user        string
	server      string
	verifier    string
	redirectURI string
	created     time.Time
}

// MCPManager is the singleton accessor for the remote-MCP subsystem.
type MCPManager struct {
	db Database

	mu        sync.Mutex
	conns     map[string]*mcpConn      // SHARED connections (none/bearer/secure_api): server name -> conn
	userConns map[string]*mcpConn      // PER-USER oauth connections: user+"\x00"+server -> conn
	proxies   map[string]*mcpProxyTool // "<server>.<tool>" -> registered proxy (register-once)

	oauthMu        sync.Mutex                // serializes token refresh
	oauthPendingMu sync.Mutex                // guards oauthPending
	oauthPending   map[string]mcpPendingAuth // state -> in-flight authorization
}

var (
	mcpInstance   *MCPManager
	mcpInstanceMu sync.Mutex
)

// MCP returns the singleton MCPManager, binding the global AuthDB the
// first time it's available (mirrors Secure()).
func MCP() *MCPManager {
	mcpInstanceMu.Lock()
	defer mcpInstanceMu.Unlock()
	if mcpInstance == nil {
		mcpInstance = &MCPManager{
			conns:        map[string]*mcpConn{},
			userConns:    map[string]*mcpConn{},
			proxies:      map[string]*mcpProxyTool{},
			oauthPending: map[string]mcpPendingAuth{},
		}
	}
	if mcpInstance.db == nil && AuthDB != nil {
		mcpInstance.db = AuthDB()
	}
	return mcpInstance
}

func (m *MCPManager) ready() bool { return m != nil && m.db != nil }

func mcpTokenKey(name string) string { return name + "__token" }

// --- config CRUD ---

// List returns every configured server (tokens excluded).
func (m *MCPManager) List() []MCPServerConfig {
	if !m.ready() {
		return nil
	}
	var out []MCPServerConfig
	for _, k := range m.db.Keys(mcpServersTable) {
		// Skip sidecar records (bearer token + per-server/per-user oauth
		// state) — only the config records are keyed by bare server name.
		if strings.HasSuffix(k, "__token") || strings.Contains(k, "__oauthcfg") || strings.Contains(k, "__oauthtok__") {
			continue
		}
		var c MCPServerConfig
		if m.db.Get(mcpServersTable, k, &c) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Load returns one server config.
func (m *MCPManager) Load(name string) (MCPServerConfig, bool) {
	var c MCPServerConfig
	if !m.ready() || name == "" {
		return c, false
	}
	ok := m.db.Get(mcpServersTable, name, &c)
	return c, ok
}

// Save validates and persists a server config. A non-empty token is
// stored encrypted only in bearer mode; passing "" leaves any existing
// token untouched.
func (m *MCPManager) Save(c MCPServerConfig, token string) error {
	if !m.ready() {
		return fmt.Errorf("mcp store not ready")
	}
	c.Name = strings.TrimSpace(c.Name)
	c.URL = strings.TrimSpace(c.URL)
	if !mcpNameRE.MatchString(c.Name) {
		return fmt.Errorf("name must match %s", mcpNameRE.String())
	}
	if !strings.HasPrefix(c.URL, "https://") && !strings.HasPrefix(c.URL, "http://") {
		return fmt.Errorf("url must be http(s)")
	}
	if c.AuthMode == MCPAuthSecure && strings.TrimSpace(c.SecureCred) == "" {
		return fmt.Errorf("secure_api mode requires a credential name")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.db.Set(mcpServersTable, c.Name, c)
	if c.AuthMode == MCPAuthBearer && strings.TrimSpace(token) != "" {
		m.db.CryptSet(mcpServersTable, mcpTokenKey(c.Name), strings.TrimSpace(token))
	}
	return nil
}

// Delete removes a server config + its token and drops the connection.
// Already-registered proxy tools can't be unregistered (the ChatTool
// registry is append-only); they go inert and error if called.
func (m *MCPManager) Delete(name string) error {
	if !m.ready() {
		return fmt.Errorf("mcp store not ready")
	}
	m.disconnect(name)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.db.Unset(mcpServersTable, name)
	m.db.Unset(mcpServersTable, mcpTokenKey(name))
	return nil
}

// token reads the stored (decrypted) bearer token for a server.
func (m *MCPManager) token(name string) string {
	var tok string
	if m.ready() {
		m.db.Get(mcpServersTable, mcpTokenKey(name), &tok)
	}
	return tok
}

// --- connection lifecycle ---

// Reload (re)connects every enabled server in the background and drops
// connections for disabled/removed ones. Non-blocking: a slow or dead
// server never stalls startup or the caller.
func (m *MCPManager) Reload() {
	if !m.ready() {
		return
	}
	configured := map[string]bool{}
	for _, cfg := range m.List() {
		configured[cfg.Name] = true
		if !cfg.Enabled {
			m.disconnect(cfg.Name)
			continue
		}
		if cfg.AuthMode == MCPAuthOAuth {
			// Per-user + lazy: no shared connection. Restore connections
			// for users already authorized (re-registers the global tool
			// schema after a restart).
			go m.reconnectOAuthUsers(cfg)
			continue
		}
		go m.bringUp(cfg)
	}
	// Drop shared connections whose config vanished.
	m.mu.Lock()
	stale := []string{}
	for name := range m.conns {
		if !configured[name] {
			stale = append(stale, name)
		}
	}
	// Drop per-user connections for vanished servers.
	for key := range m.userConns {
		if i := strings.IndexByte(key, 0); i >= 0 && !configured[key[i+1:]] {
			conn := m.userConns[key]
			delete(m.userConns, key)
			if conn != nil {
				conn.alive.Store(false)
				conn.client.Close()
			}
		}
	}
	m.mu.Unlock()
	for _, name := range stale {
		m.disconnect(name)
	}
}

// bringUp connects one server and registers its tools.
func (m *MCPManager) bringUp(cfg MCPServerConfig) {
	conn, err := m.connect(cfg)
	if err != nil {
		Warn("[mcp] %q connect failed: %v", cfg.Name, err)
		return
	}
	m.mu.Lock()
	if old := m.conns[cfg.Name]; old != nil {
		old.client.Close()
	}
	m.conns[cfg.Name] = conn
	m.mu.Unlock()

	if cfg.ExposeTools {
		m.registerTools(cfg, conn.tools)
	}
	if cfg.ExposeReference {
		// Idempotent: the reference registry replaces by Kind, so a
		// reconnect/Reload simply refreshes the same source.
		RegisterReferenceSource(mcpReferenceSource{mgr: m, server: cfg.Name})
	}
	Log("[mcp] %q connected: %d tool(s)", cfg.Name, len(conn.tools))
}

// connect builds a SHARED client (none/bearer/secure_api auth) and
// enumerates tools.
func (m *MCPManager) connect(cfg MCPServerConfig) (*mcpConn, error) {
	return m.dialMCP(cfg, m.authorizer(cfg))
}

// dialMCP builds a client with the given authorizer, performs the
// handshake, and enumerates tools. Shared by the shared-connection path
// and the per-user oauth path (which passes a per-user authorizer).
func (m *MCPManager) dialMCP(cfg MCPServerConfig, auth mcpclient.Authorizer) (*mcpConn, error) {
	tr := mcpclient.NewHTTPTransport(cfg.URL, mcpclient.HTTPOptions{Auth: auth})
	cl := mcpclient.New(tr)
	ctx, cancel := context.WithTimeout(context.Background(), mcpHandshakeTimeout)
	defer cancel()
	if err := cl.Initialize(ctx); err != nil {
		cl.Close()
		return nil, err
	}
	tools, err := cl.ListTools(ctx)
	if err != nil {
		cl.Close()
		return nil, err
	}
	conn := &mcpConn{cfg: cfg, client: cl, tools: tools}
	conn.alive.Store(true)
	return conn, nil
}

// bringUpUser connects one user's per-user oauth connection and registers
// the global tool schema + reference source (idempotent — the schema is
// identical for every user; only the call HANDLER dispatches per-user).
func (m *MCPManager) bringUpUser(user string, cfg MCPServerConfig) {
	conn, err := m.dialMCP(cfg, m.oauthAuthorizer(user, cfg.Name))
	if err != nil {
		Warn("[mcp] %q oauth connect for user %q failed: %v", cfg.Name, user, err)
		return
	}
	key := user + "\x00" + cfg.Name
	m.mu.Lock()
	if old := m.userConns[key]; old != nil {
		old.client.Close()
	}
	m.userConns[key] = conn
	m.mu.Unlock()
	if cfg.ExposeTools {
		m.registerTools(cfg, conn.tools)
	}
	if cfg.ExposeReference {
		RegisterReferenceSource(mcpReferenceSource{mgr: m, server: cfg.Name})
	}
	Log("[mcp] %q connected for user %q: %d tool(s)", cfg.Name, user, len(conn.tools))
}

// reconnectOAuthUsers restores per-user connections for every user with a
// stored token for this server (used on Reload after a restart).
func (m *MCPManager) reconnectOAuthUsers(cfg MCPServerConfig) {
	if !m.ready() {
		return
	}
	prefix := mcpOAuthTokKey(cfg.Name, "")
	for _, k := range m.db.Keys(mcpServersTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		user := strings.TrimPrefix(k, prefix)
		if user == "" {
			continue
		}
		m.bringUpUser(user, cfg)
	}
}

// disconnect tears down a live connection (if any) and marks it dead.
func (m *MCPManager) disconnect(name string) {
	m.mu.Lock()
	conn := m.conns[name]
	delete(m.conns, name)
	m.mu.Unlock()
	if conn != nil {
		conn.alive.Store(false)
		conn.client.Close()
	}
}

// authorizer returns the per-request credential hook for a server.
func (m *MCPManager) authorizer(cfg MCPServerConfig) mcpclient.Authorizer {
	switch cfg.AuthMode {
	case MCPAuthBearer:
		tok := m.token(cfg.Name)
		if tok == "" {
			return nil
		}
		return func(req *http.Request) error {
			req.Header.Set("Authorization", "Bearer "+tok)
			return nil
		}
	case MCPAuthSecure:
		cred := strings.TrimSpace(cfg.SecureCred)
		return func(req *http.Request) error {
			return Secure().AuthorizeRequest(cred, req)
		}
	default:
		return nil
	}
}

// callTool dispatches a tools/call for the given user under a fresh
// per-call timeout. user is "" for shared servers (the ChatTool Run path
// carries no context/user); oauth servers require a non-empty user.
func (m *MCPManager) callTool(user, server, rawName string, args map[string]any) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), mcpCallTimeout)
	defer cancel()
	return m.callToolForUser(ctx, user, server, rawName, args)
}

// callToolForUser dispatches honoring the caller's context (the
// reference-source Fetch path passes a request ctx + the real user).
func (m *MCPManager) callToolForUser(ctx context.Context, user, server, rawName string, args map[string]any) (string, error) {
	conn, err := m.connFor(user, server)
	if err != nil {
		return "", err
	}
	return conn.client.CallTool(ctx, rawName, args)
}

// connFor resolves the connection to use for (user, server). Shared
// servers use the single shared connection; oauth servers use the
// caller's per-user connection, lazily dialed when the user holds a
// token. Returns an actionable error when the user hasn't authorized.
func (m *MCPManager) connFor(user, server string) (*mcpConn, error) {
	cfg, ok := m.Load(server)
	if !ok {
		return nil, fmt.Errorf("mcp server %q not configured", server)
	}
	if cfg.AuthMode == MCPAuthOAuth {
		if user == "" {
			return nil, fmt.Errorf("mcp server %q needs per-user authorization (no user in this context)", server)
		}
		key := user + "\x00" + server
		m.mu.Lock()
		conn := m.userConns[key]
		m.mu.Unlock()
		if conn != nil && conn.alive.Load() {
			return conn, nil
		}
		if _, ok := m.loadOAuthToken(server, user); !ok {
			return nil, fmt.Errorf("not connected to %q — authorize it in Admin -> MCP Servers -> Connect", server)
		}
		conn, err := m.dialMCP(cfg, m.oauthAuthorizer(user, server))
		if err != nil {
			return nil, err
		}
		m.mu.Lock()
		m.userConns[key] = conn
		m.mu.Unlock()
		return conn, nil
	}
	m.mu.Lock()
	conn := m.conns[server]
	m.mu.Unlock()
	if conn == nil || !conn.alive.Load() {
		return nil, fmt.Errorf("mcp server %q is not connected", server)
	}
	return conn, nil
}

// connected reports whether (user, server) currently has a usable
// connection-or-token. For shared servers user is ignored.
func (m *MCPManager) connected(user, server string) bool {
	cfg, ok := m.Load(server)
	if !ok {
		return false
	}
	if cfg.AuthMode == MCPAuthOAuth {
		if user == "" {
			return false
		}
		m.mu.Lock()
		conn := m.userConns[user+"\x00"+server]
		m.mu.Unlock()
		if conn != nil && conn.alive.Load() {
			return true
		}
		_, ok := m.loadOAuthToken(server, user)
		return ok
	}
	m.mu.Lock()
	conn := m.conns[server]
	m.mu.Unlock()
	return conn != nil && conn.alive.Load()
}

// Connected reports whether (user, server) can serve calls now (shared
// connection live, or the user holds an oauth token). Exported for the
// admin UI's per-row status.
func (m *MCPManager) Connected(user, server string) bool { return m.connected(user, server) }

// oauthAuthorizer returns a refresh-aware per-user Authorizer for an
// oauth server: it sets a valid Bearer token, refreshing when near
// expiry.
func (m *MCPManager) oauthAuthorizer(user, server string) mcpclient.Authorizer {
	return func(req *http.Request) error {
		tok, err := m.validOAuthToken(user, server)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		return nil
	}
}

// validOAuthToken returns a current access token for (user, server),
// refreshing if it is missing/near expiry. Serialized to avoid refresh
// storms.
func (m *MCPManager) validOAuthToken(user, server string) (string, error) {
	m.oauthMu.Lock()
	defer m.oauthMu.Unlock()
	tok, ok := m.loadOAuthToken(server, user)
	if !ok {
		return "", fmt.Errorf("no token for user %q on %q", user, server)
	}
	if tok.AccessToken != "" && (tok.Expiry.IsZero() || time.Until(tok.Expiry) > 60*time.Second) {
		return tok.AccessToken, nil
	}
	cfg, ok := m.loadOAuthCfg(server)
	if !ok {
		if tok.AccessToken != "" {
			return tok.AccessToken, nil // best effort
		}
		return "", fmt.Errorf("no oauth config for %q", server)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	nt, err := mcpRefreshAccess(ctx, cfg, tok)
	if err != nil {
		if tok.AccessToken != "" && (tok.Expiry.IsZero() || time.Now().Before(tok.Expiry)) {
			return tok.AccessToken, nil // not yet expired; ride it out
		}
		return "", fmt.Errorf("token refresh failed for %q: %w", server, err)
	}
	m.saveOAuthToken(server, user, nt)
	return nt.AccessToken, nil
}

// Test connects to a candidate config (without persisting) and reports
// the tool count. Used by the admin "Test" button. A non-empty token
// overrides the stored one for the probe; never echoed back.
func (m *MCPManager) Test(cfg MCPServerConfig, token string) (string, error) {
	auth := m.authorizer(cfg)
	if cfg.AuthMode == MCPAuthBearer && strings.TrimSpace(token) != "" {
		t := strings.TrimSpace(token)
		auth = func(req *http.Request) error {
			req.Header.Set("Authorization", "Bearer "+t)
			return nil
		}
	}
	cl := mcpclient.New(mcpclient.NewHTTPTransport(strings.TrimSpace(cfg.URL), mcpclient.HTTPOptions{Auth: auth}))
	defer cl.Close()
	ctx, cancel := context.WithTimeout(context.Background(), mcpHandshakeTimeout)
	defer cancel()
	if err := cl.Initialize(ctx); err != nil {
		return "", err
	}
	tools, err := cl.ListTools(ctx)
	if err != nil {
		return "", err
	}
	info := cl.ServerInfo()
	name := info.ServerName
	if name == "" {
		name = "(unnamed server)"
	}
	return fmt.Sprintf("Connected to %s: %d tool(s) available.", name, len(tools)), nil
}

// --- oauth storage + flow (manager side; pure protocol is in mcp_oauth.go) ---

func (m *MCPManager) loadOAuthCfg(server string) (mcpOAuthConfig, bool) {
	var c mcpOAuthConfig
	if !m.ready() {
		return c, false
	}
	ok := m.db.Get(mcpServersTable, mcpOAuthCfgKey(server), &c)
	return c, ok && c.ClientID != ""
}

func (m *MCPManager) saveOAuthCfg(server string, c mcpOAuthConfig) {
	if m.ready() {
		m.db.CryptSet(mcpServersTable, mcpOAuthCfgKey(server), c)
	}
}

func (m *MCPManager) loadOAuthToken(server, user string) (mcpOAuthToken, bool) {
	var t mcpOAuthToken
	if !m.ready() || user == "" {
		return t, false
	}
	ok := m.db.Get(mcpServersTable, mcpOAuthTokKey(server, user), &t)
	return t, ok && t.AccessToken != ""
}

func (m *MCPManager) saveOAuthToken(server, user string, t mcpOAuthToken) {
	if m.ready() && user != "" {
		m.db.CryptSet(mcpServersTable, mcpOAuthTokKey(server, user), t)
	}
}

// StartOAuth begins an authorization for (user, server): it ensures a
// server-level OAuth client exists (discover + dynamic registration on
// first use), mints PKCE + state, records the pending authorization, and
// returns the browser authorization URL. redirectURI must match the
// admin callback route and be https-or-localhost.
func (m *MCPManager) StartOAuth(user, server, redirectURI string) (string, error) {
	if !m.ready() {
		return "", fmt.Errorf("mcp store not ready")
	}
	cfg, ok := m.Load(server)
	if !ok || cfg.AuthMode != MCPAuthOAuth {
		return "", fmt.Errorf("server %q is not an oauth server", server)
	}
	oc, ok := m.loadOAuthCfg(server)
	if !ok || oc.AuthEndpoint == "" {
		ctx, cancel := context.WithTimeout(context.Background(), mcpHandshakeTimeout)
		defer cancel()
		disc, err := mcpDiscover(ctx, cfg.URL)
		if err != nil {
			return "", fmt.Errorf("discovery: %w", err)
		}
		clientID, clientSecret, err := mcpRegisterClient(ctx, disc.RegistrationEndpoint, redirectURI)
		if err != nil {
			return "", fmt.Errorf("client registration: %w", err)
		}
		disc.ClientID = clientID
		disc.ClientSecret = clientSecret
		oc = disc
		m.saveOAuthCfg(server, oc)
	}
	verifier, challenge := mcpPKCE()
	state := mcpRandomState()
	m.oauthPendingMu.Lock()
	m.oauthPending[state] = mcpPendingAuth{user: user, server: server, verifier: verifier, redirectURI: redirectURI, created: time.Now()}
	m.oauthPendingMu.Unlock()
	return mcpAuthorizeURL(oc, redirectURI, state, challenge), nil
}

// CompleteOAuth redeems the callback (state + code): exchanges the code
// for tokens, stores them for the authorizing user, and brings that
// user's connection up (registering the global tool schema on first
// success).
func (m *MCPManager) CompleteOAuth(state, code string) error {
	m.oauthPendingMu.Lock()
	p, ok := m.oauthPending[state]
	delete(m.oauthPending, state)
	m.oauthPendingMu.Unlock()
	if !ok {
		return fmt.Errorf("unknown or expired authorization state")
	}
	oc, ok := m.loadOAuthCfg(p.server)
	if !ok {
		return fmt.Errorf("no oauth config for %q", p.server)
	}
	ctx, cancel := context.WithTimeout(context.Background(), mcpHandshakeTimeout)
	defer cancel()
	tok, err := mcpExchangeCode(ctx, oc, code, p.verifier, p.redirectURI)
	if err != nil {
		return fmt.Errorf("token exchange: %w", err)
	}
	m.saveOAuthToken(p.server, p.user, tok)
	if cfg, ok := m.Load(p.server); ok {
		go m.bringUpUser(p.user, cfg)
	}
	return nil
}

// --- tool exposure ---

// registerTools registers (once) a proxy ChatTool per remote tool, in
// sorted order so the catalog stays deterministic. Already-registered
// proxies are refreshed in place (the registry is append-only, so we
// never re-register).
func (m *MCPManager) registerTools(cfg MCPServerConfig, tools []mcpclient.ToolDef) {
	byName := map[string]mcpclient.ToolDef{}
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		byName[t.Name] = t
		names = append(names, t.Name)
	}
	sort.Strings(names)
	for _, raw := range names {
		def := byName[raw]
		full := cfg.Name + "." + raw
		params, required := mcpMapSchema(def.InputSchema)
		desc := def.Description
		if desc == "" {
			desc = raw
		}

		m.mu.Lock()
		proxy, exists := m.proxies[full]
		if !exists {
			proxy = &mcpProxyTool{mgr: m, server: cfg.Name, rawName: raw, fullName: full}
			m.proxies[full] = proxy
		}
		proxy.desc = desc
		proxy.params = params
		proxy.required = required
		proxy.confirm = mcpLooksMutating(raw)
		m.mu.Unlock()

		if !exists {
			RegisterChatTool(proxy)
		}
	}
}

// mcpProxyTool adapts one remote MCP tool to the ChatTool interface. It
// resolves the live connection at call time, so a Reload that reconnects
// a server updates behavior without re-registering (the registry can't
// unregister). Registered once per "<server>.<tool>".
type mcpProxyTool struct {
	mgr      *MCPManager
	server   string
	rawName  string
	fullName string

	desc     string
	params   map[string]ToolParam
	required []string
	confirm  bool
}

func (t *mcpProxyTool) Name() string                 { return t.fullName }
func (t *mcpProxyTool) Desc() string                 { return t.desc }
func (t *mcpProxyTool) Params() map[string]ToolParam { return t.params }

// Run is the user-less fallback (shared servers). For oauth servers it
// yields an actionable "authorize" error since no user is in scope.
func (t *mcpProxyTool) Run(args map[string]any) (string, error) {
	return t.mgr.callTool("", t.server, t.rawName, args)
}

// RunWithSession dispatches on the calling user so oauth servers use that
// user's token (results respect their ACLs). The schema is identical for
// every user; only this handler is per-user.
func (t *mcpProxyTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	user := ""
	if sess != nil {
		user = sess.Username
	}
	return t.mgr.callTool(user, t.server, t.rawName, args)
}

// Required reports the required params (consumed by the schema builder).
func (t *mcpProxyTool) Required() []string { return t.required }

// Caps: remote tools are network calls; treat as read+network. Mutating
// remote actions additionally gate behind confirmation (see NeedsConfirm).
func (t *mcpProxyTool) Caps() []Capability { return []Capability{CapNetwork, CapRead} }

// NeedsConfirm gates remote tools whose name suggests a side effect.
func (t *mcpProxyTool) NeedsConfirm() bool { return t.confirm }

// IsInternetTool drops these from private-mode sessions.
func (t *mcpProxyTool) IsInternetTool() bool { return true }

// --- reference source exposure ---

// mcpReferenceSource exposes one MCP server as a ReferenceSource so the
// writer/research source pickers can pull it as RAG knowledge. Fetch
// calls the server's configured search tool (default "search") with a
// {query} argument and returns its text. It resolves the live connection
// and current config dynamically, so toggling the server off or losing
// the connection cleanly removes it from pickers (ReferenceGroups omits
// empty List() results).
type mcpReferenceSource struct {
	mgr    *MCPManager
	server string
}

func (s mcpReferenceSource) Kind() string  { return "mcp:" + s.server }
func (s mcpReferenceSource) Label() string { return s.server }

func (s mcpReferenceSource) List(user string) []ReferenceItem {
	cfg, ok := s.mgr.Load(s.server)
	// connected() is user-aware: an oauth server only lists for users who
	// have authorized it, so the picker shows it per-identity.
	if !ok || !cfg.ExposeReference || !s.mgr.connected(user, s.server) {
		return nil
	}
	return []ReferenceItem{{ID: s.server, Name: s.server, Desc: "Remote MCP knowledge source"}}
}

func (s mcpReferenceSource) Fetch(ctx context.Context, user, itemID, query string) string {
	cfg, ok := s.mgr.Load(s.server)
	if !ok || !cfg.ExposeReference {
		return ""
	}
	tool := strings.TrimSpace(cfg.SearchTool)
	if tool == "" {
		tool = "search"
	}
	out, err := s.mgr.callToolForUser(ctx, user, s.server, tool, map[string]any{"query": query})
	if err != nil {
		Warn("[mcp] %q reference fetch failed: %v", s.server, err)
		return ""
	}
	return out
}

// mcpLooksMutating is a conservative name heuristic: gate confirmation on
// remote tools that look like they change state. Read-style tools run
// without a prompt.
func mcpLooksMutating(name string) bool {
	n := strings.ToLower(name)
	for _, verb := range []string{"create", "update", "delete", "remove", "write", "add", "set", "put", "post", "edit", "move", "send", "upload", "publish", "archive"} {
		if strings.Contains(n, verb) {
			return true
		}
	}
	return false
}

// mcpMapSchema flattens an MCP JSON-Schema inputSchema into the flat
// per-property shape ToolParam uses. Nested object/array properties keep
// their top-level type and fold a compact JSON of their sub-schema into
// the description (ToolParam can't nest), so the model still sees the
// expected shape. Ported from gohort-desktop/mcp/tool.go.
func mcpMapSchema(schema map[string]any) (map[string]ToolParam, []string) {
	out := map[string]ToolParam{}
	if schema == nil {
		return out, nil
	}
	props, _ := schema["properties"].(map[string]any)
	for name, raw := range props {
		p, _ := raw.(map[string]any)
		typ, _ := p["type"].(string)
		if typ == "" {
			typ = "string"
		}
		desc, _ := p["description"].(string)
		if typ == "object" || typ == "array" {
			if shape := mcpCompactSchema(p); shape != "" {
				if desc != "" {
					desc += " "
				}
				desc += "(shape: " + shape + ")"
			}
		}
		out[name] = ToolParam{Type: typ, Description: desc}
	}
	var required []string
	// inputSchema["required"] may decode as []any (JSON) or []string.
	switch reqs := schema["required"].(type) {
	case []any:
		for _, r := range reqs {
			if s, ok := r.(string); ok {
				required = append(required, s)
			}
		}
	case []string:
		required = append(required, reqs...)
	}
	return out, required
}

// mcpCompactSchema renders a sub-schema as compact JSON for the param
// description. Best-effort; empty on failure or if oversized.
func mcpCompactSchema(p map[string]any) string {
	b, err := json.Marshal(p)
	if err != nil || len(b) > 400 {
		return ""
	}
	return string(b)
}
