// Package mcppersonal is a person's own MCP servers: ones they add for
// themselves, whose tools reach only their own agents.
//
// The servers an administrator adds (core MCPManager) are the deployment's:
// one record, tools registered for every agent, each person signing in where
// the server asks. A personal one is not: it is stored against its owner, its
// tools are offered only in its owner's runs (an agent tool provider, never
// the global registry), and its token, when it has one, is the owner's.
//
// Whether people may add one at all is the administrator's policy
// (core/governance): under Needs approval a new one is held off until an
// administrator approves it.
//
// A personal server may not point at this network's private addresses unless
// its owner is an administrator: the server makes the request, so a URL aimed
// inward would be a way past every other network rule. That is checked when
// it is saved, and again on every connection, after the name resolves.
package mcppersonal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/internal/mcpclient"
	"github.com/cmcoffee/gohort/core/sourcehooks"
)

const table = "mcp_personal_servers"

// Auth modes a personal server may use: none, a bearer token, one of the
// owner's API credentials (any type, held to its own rules), or OAuth, where
// the owner signs in from Connected accounts. OAuth takes its endpoints from
// discovery, and its client from the provider's own registration or, for a
// provider without it, one the owner registered there (OAuthClientID).
const (
	AuthNone       = ""
	AuthBearer     = "bearer"
	AuthCredential = "secure_api"
	AuthOAuth      = "oauth"
)

var nameRE = regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)

// ServerTool is one tool a server offers, as it last said.
type ServerTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Schema      map[string]any `json:"schema,omitempty"`
}

// Server is one person's MCP server.
type Server struct {
	Owner string `json:"owner"`
	Name  string `json:"name"`
	URL   string `json:"url"`
	Auth  string `json:"auth,omitempty"`
	// Credential names the API credential it signs in with (AuthCredential),
	// resolved as the owner: their own, one lent to them, or one of the
	// deployment's they may use.
	Credential string `json:"credential,omitempty"`
	// OAuthClientID is an OAuth app its owner registered at the provider, for
	// one that does not register clients itself; its secret, when it has one,
	// is kept with the bearer token's. OAuthScopes, when set, replace the
	// scopes discovery advertises.
	OAuthClientID string `json:"oauth_client_id,omitempty"`
	OAuthScopes   string `json:"oauth_scopes,omitempty"`
	// Disabled is the owner's switch. AdminDisabled is an administrator's
	// hold, which the owner's switch cannot lift; AwaitingApproval is that
	// hold on one added under Needs approval, said apart so both read
	// "waiting for approval" rather than "switched off".
	Disabled         bool `json:"disabled,omitempty"`
	AdminDisabled    bool `json:"admin_disabled,omitempty"`
	AwaitingApproval bool `json:"awaiting_approval,omitempty"`
	// OneTool offers its tools as one tool named for the server, with an
	// action per tool, instead of each on its own.
	OneTool bool `json:"one_tool,omitempty"`
	// What the server offered when it last answered, and when; or why it
	// did not.
	Tools     []ServerTool `json:"tools,omitempty"`
	CheckedAt time.Time    `json:"checked_at,omitempty"`
	CheckErr  string       `json:"check_err,omitempty"`
	Created   time.Time    `json:"created"`
}

// SignIn says how s signs in, for a list.
func (s Server) SignIn() string {
	switch s.Auth {
	case AuthBearer:
		return "bearer token"
	case AuthCredential:
		return "API credential " + s.Credential
	case AuthOAuth:
		if Connected(s.Owner, s.Name) {
			return "OAuth, signed in"
		}
		return "OAuth, not signed in"
	}
	return "none"
}

// Usable reports whether its tools are offered.
func (s Server) Usable() bool { return !s.Disabled && !s.AdminDisabled && !s.AwaitingApproval }

func key(owner, name string) string      { return owner + "/" + name }
func tokenKey(owner, name string) string { return key(owner, name) + "__token" }

// oauthKey is what the MCP manager keeps a server's OAuth client and the
// owner's tokens under: no administrator's server name can be one.
func oauthKey(owner, name string) string { return "personal:" + key(owner, name) }

func store() Database {
	if AuthDB == nil {
		return nil
	}
	return AuthDB()
}

// List is owner's servers, by name.
func List(owner string) []Server {
	var out []Server
	for _, s := range All() {
		if s.Owner == owner {
			out = append(out, s)
		}
	}
	return out
}

// All is every person's servers, by owner then name: the administrator's view.
func All() []Server {
	db := store()
	if db == nil {
		return nil
	}
	var out []Server
	for _, k := range db.Keys(table) {
		if strings.HasSuffix(k, "__token") {
			continue
		}
		var s Server
		if db.Get(table, k, &s) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Owner != out[j].Owner {
			return out[i].Owner < out[j].Owner
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Load is one of owner's servers.
func Load(owner, name string) (Server, bool) {
	var s Server
	db := store()
	if db == nil || owner == "" || name == "" {
		return s, false
	}
	return s, db.Get(table, key(owner, name), &s)
}

// Save adds or updates one of owner's servers. token, when given, is its
// secret: the bearer token, or the client secret of an OAuth app registered
// by hand; "" keeps the one it has. hold holds a NEW one for approval
// (the administrator's policy says so); an update keeps the state it has.
// A server moved to another address loses its token and its tool list:
// they were for the server it was.
func Save(s Server, token string, hold bool) error {
	db := store()
	if db == nil {
		return fmt.Errorf("the store is not ready")
	}
	s.Name = strings.TrimSpace(s.Name)
	s.URL = strings.TrimSpace(s.URL)
	if strings.TrimSpace(s.Owner) == "" {
		return fmt.Errorf("a personal server needs its owner")
	}
	if !nameRE.MatchString(s.Name) {
		return fmt.Errorf("name must be 1 to 40 lowercase letters, digits, dashes or underscores")
	}
	if err := checkURL(s.Owner, s.URL); err != nil {
		return err
	}
	s.Credential = strings.TrimSpace(s.Credential)
	s.OAuthClientID, s.OAuthScopes = strings.TrimSpace(s.OAuthClientID), strings.Join(strings.Fields(s.OAuthScopes), " ")
	if s.Auth != AuthOAuth {
		s.OAuthClientID, s.OAuthScopes = "", ""
	}
	switch s.Auth {
	case AuthNone, AuthBearer, AuthOAuth:
		s.Credential = ""
	case AuthCredential:
		if s.Credential == "" {
			return fmt.Errorf("name the API credential it signs in with")
		}
		if _, ok := Secure().Resolve(s.Credential, s.Owner); !ok {
			return fmt.Errorf("no API credential named %q that you can use: add it under APIs first", s.Credential)
		}
	default:
		return fmt.Errorf("auth must be none, bearer, secure_api or oauth")
	}
	if prev, ok := Load(s.Owner, s.Name); ok {
		s.Disabled, s.AdminDisabled, s.AwaitingApproval, s.Created = prev.Disabled, prev.AdminDisabled, prev.AwaitingApproval, prev.Created
		moved := origin(prev.URL) != origin(s.URL)
		if !moved {
			s.Tools, s.CheckedAt, s.CheckErr = prev.Tools, prev.CheckedAt, prev.CheckErr
		} else {
			db.Unset(table, tokenKey(s.Owner, s.Name))
		}
		// Its OAuth client and the owner's sign-in were for the server it
		// was, for signing in that way, and for the client it named.
		reclient := prev.OAuthClientID != s.OAuthClientID || prev.OAuthScopes != s.OAuthScopes ||
			(s.OAuthClientID != "" && strings.TrimSpace(token) != "")
		// A secret is for the app it was given with.
		if prev.OAuthClientID != s.OAuthClientID && strings.TrimSpace(token) == "" {
			db.Unset(table, tokenKey(s.Owner, s.Name))
		}
		if moved || s.Auth != AuthOAuth || reclient {
			MCP().ForgetOAuth(oauthKey(s.Owner, s.Name))
		}
	} else {
		s.Created = time.Now()
		if hold {
			s.AdminDisabled, s.AwaitingApproval = true, true
		}
	}
	db.Set(table, key(s.Owner, s.Name), s)
	switch {
	case s.Auth != AuthBearer && (s.Auth != AuthOAuth || s.OAuthClientID == ""):
		db.Unset(table, tokenKey(s.Owner, s.Name))
	case strings.TrimSpace(token) != "":
		db.CryptSet(table, tokenKey(s.Owner, s.Name), strings.TrimSpace(token))
	}
	dropClient(s.Owner, s.Name)
	return nil
}

// checkURL refuses an address a personal server may not use.
func checkURL(owner, raw string) error {
	if !strings.HasPrefix(raw, "https://") && !strings.HasPrefix(raw, "http://") {
		return fmt.Errorf("url must be http(s)")
	}
	if UserIsAdmin(owner) {
		return nil
	}
	if !strings.HasPrefix(raw, "https://") {
		return fmt.Errorf("url must be https: a personal server's traffic carries its token")
	}
	if err := RefuseNonPublicHost(raw); err != nil {
		return fmt.Errorf("a personal MCP server can reach public addresses only (%v): an administrator adds servers on this network", err)
	}
	return nil
}

func origin(raw string) string {
	raw = strings.TrimSpace(raw)
	if i := strings.Index(raw, "://"); i >= 0 {
		rest := raw[i+3:]
		if j := strings.IndexAny(rest, "/?#"); j >= 0 {
			rest = rest[:j]
		}
		return strings.ToLower(raw[:i+3] + rest)
	}
	return strings.ToLower(raw)
}

// Remove removes one of owner's servers and its token.
func Remove(owner, name string) error {
	db := store()
	if db == nil {
		return fmt.Errorf("the store is not ready")
	}
	if _, ok := Load(owner, name); !ok {
		return fmt.Errorf("no server %q", name)
	}
	db.Unset(table, key(owner, name))
	db.Unset(table, tokenKey(owner, name))
	MCP().ForgetOAuth(oauthKey(owner, name))
	dropClient(owner, name)
	return nil
}

func mutate(owner, name string, fn func(*Server) error) error {
	db := store()
	if db == nil {
		return fmt.Errorf("the store is not ready")
	}
	s, ok := Load(owner, name)
	if !ok {
		return fmt.Errorf("no server %q", name)
	}
	if err := fn(&s); err != nil {
		return err
	}
	db.Set(table, key(owner, name), s)
	return nil
}

// SetDisabled is the owner's own switch. Switching on one an administrator
// holds is refused, with the reason.
func SetDisabled(owner, name string, disabled bool) error {
	return mutate(owner, name, func(s *Server) error {
		if !disabled && s.AwaitingApproval {
			return fmt.Errorf("server %q is waiting for an administrator to approve it", name)
		}
		if !disabled && s.AdminDisabled {
			return fmt.Errorf("an administrator switched server %q off: ask them to enable it", name)
		}
		s.Disabled = disabled
		return nil
	})
}

// AdminSetDisabled is an administrator switching one off or on. Off holds
// until an administrator turns it on again; on approves one that waited.
func AdminSetDisabled(owner, name string, disabled bool) error {
	return mutate(owner, name, func(s *Server) error {
		s.AdminDisabled = disabled
		if !disabled {
			s.AwaitingApproval, s.Disabled = false, false
		}
		return nil
	})
}

// SetOneTool switches how a server's tools are offered: as one tool, or each
// on its own. An agent listing them in either shape keeps them (Aliases).
func SetOneTool(owner, name string, one bool) error {
	return mutate(owner, name, func(s *Server) error {
		s.OneTool = one
		return nil
	})
}

// --- connections ---

var clients = struct {
	sync.Mutex
	m map[string]*mcpclient.Client // key -> a connection that initialized
}{m: map[string]*mcpclient.Client{}}

func dropClient(owner, name string) {
	clients.Lock()
	c := clients.m[key(owner, name)]
	delete(clients.m, key(owner, name))
	clients.Unlock()
	if c != nil {
		c.Close()
	}
}

// httpClient is the client a server's owner may use: an administrator's can
// reach any address, anyone else's only public ones, checked on the address
// actually dialled. No header deadline: a slow tool's answer comes when it
// comes, and the call's context bounds it.
func httpClient(owner string) *http.Client {
	if UserIsAdmin(owner) {
		return &http.Client{}
	}
	c := sourcehooks.NewPublicHTTPClient()
	if tr, ok := c.Transport.(*http.Transport); ok {
		tr.ResponseHeaderTimeout = 0
	}
	return c
}

// connect opens and initializes a connection to s.
func connect(ctx context.Context, s Server) (*mcpclient.Client, error) {
	if err := checkURL(s.Owner, s.URL); err != nil {
		return nil, err
	}
	var auth mcpclient.Authorizer
	switch s.Auth {
	case AuthCredential:
		owner, cred := s.Owner, s.Credential
		auth = func(req *http.Request) error { return Secure().AuthorizeRequestAs(owner, cred, req) }
	case AuthOAuth:
		if !MCP().OAuthConnected(s.Owner, oauthKey(s.Owner, s.Name)) {
			return nil, notConnected(s.Name)
		}
		auth = MCP().OAuthAuthorizer(s.Owner, oauthKey(s.Owner, s.Name))
	case AuthBearer:
		var tok string
		if db := store(); db != nil {
			db.Get(table, tokenKey(s.Owner, s.Name), &tok)
		}
		if strings.TrimSpace(tok) == "" {
			return nil, fmt.Errorf("server %q needs its token: set it on your Extensions page", s.Name)
		}
		auth = func(req *http.Request) error {
			req.Header.Set("Authorization", "Bearer "+tok)
			return nil
		}
	}
	c := mcpclient.New(mcpclient.NewHTTPTransport(s.URL, mcpclient.HTTPOptions{Auth: auth, Client: httpClient(s.Owner)}))
	hctx, cancel := context.WithTimeout(ctx, TuneDuration("tune_mcp_handshake_timeout"))
	defer cancel()
	if err := c.Initialize(hctx); err != nil {
		c.Close()
		return nil, signInErr(s, err)
	}
	return c, nil
}

func notConnected(name string) error {
	return fmt.Errorf("you have not signed in to %s yet: press Connect beside it under Connected accounts on your Extensions page", name)
}

// signInErr says what a failure means for the way s signs in: for OAuth, a
// refused token is renewed for the next call, and a sign-in that cannot be
// renewed is one to make again.
func signInErr(s Server, err error) error {
	if s.Auth != AuthOAuth {
		return err
	}
	var nc MCPNotConnectedError
	if errors.As(err, &nc) {
		return notConnected(s.Name)
	}
	if errors.Is(err, mcpclient.ErrUnauthorized) {
		MCP().InvalidateOAuth(oauthKey(s.Owner, s.Name), s.Owner)
		return fmt.Errorf("%s refused your sign-in; it has been renewed: try again", s.Name)
	}
	return err
}

// clientFor is a live connection to s, opened on first use.
func clientFor(ctx context.Context, s Server) (*mcpclient.Client, error) {
	k := key(s.Owner, s.Name)
	clients.Lock()
	c := clients.m[k]
	clients.Unlock()
	if c != nil {
		return c, nil
	}
	c, err := connect(ctx, s)
	if err != nil {
		return nil, err
	}
	clients.Lock()
	if prev := clients.m[k]; prev != nil {
		clients.Unlock()
		c.Close()
		return prev, nil
	}
	clients.m[k] = c
	clients.Unlock()
	return c, nil
}

// Check connects to one of owner's servers and records what it offers, or
// why it did not answer. It works on a server waiting for approval too, so
// the administrator sees what they are approving.
func Check(ctx context.Context, owner, name string) (Server, error) {
	s, ok := Load(owner, name)
	if !ok {
		return s, fmt.Errorf("no server %q", name)
	}
	dropClient(owner, name)
	var tools []ServerTool
	c, err := clientFor(ctx, s)
	if err == nil {
		lctx, cancel := context.WithTimeout(ctx, TuneDuration("tune_mcp_handshake_timeout"))
		var defs []mcpclient.ToolDef
		defs, err = c.ListTools(lctx)
		cancel()
		for _, d := range defs {
			tools = append(tools, ServerTool{Name: d.Name, Description: d.Description, Schema: d.InputSchema})
		}
	}
	if err != nil {
		dropClient(owner, name)
	}
	merr := mutate(owner, name, func(cur *Server) error {
		cur.CheckedAt = time.Now()
		if err != nil {
			cur.CheckErr = err.Error()
			return nil
		}
		cur.Tools, cur.CheckErr = tools, ""
		return nil
	})
	if merr != nil {
		return s, merr
	}
	s, _ = Load(owner, name)
	return s, err
}

// call runs one tool on s.
func call(ctx context.Context, s Server, tool string, args map[string]any) (string, error) {
	c, err := clientFor(ctx, s)
	if err != nil {
		return "", err
	}
	cctx, cancel := context.WithTimeout(ctx, TuneDuration("tune_mcp_call_timeout"))
	defer cancel()
	out, err := c.CallTool(cctx, tool, args)
	if err != nil {
		if _, toolSaid := err.(*mcpclient.ToolError); !toolSaid {
			dropClient(s.Owner, s.Name) // the connection, not the tool: open a fresh one next time
		}
		err = signInErr(s, err)
	}
	return out, err
}

// Connected reports whether owner has signed in to one of their OAuth
// servers.
func Connected(owner, name string) bool {
	return MCP().OAuthConnected(owner, oauthKey(owner, name))
}

// StartConnect begins owner's sign-in to one of their OAuth servers and
// returns the provider's address to send them to. The callback is the
// account's MCP one, which CompleteOAuth redeems for any server; AfterConnect
// then asks the server what it offers.
func StartConnect(owner, name, redirectURI string) (string, error) {
	s, ok := Load(owner, name)
	if !ok {
		return "", fmt.Errorf("no server %q", name)
	}
	if s.Auth != AuthOAuth {
		return "", fmt.Errorf("%s does not sign in with OAuth", name)
	}
	if err := checkURL(owner, s.URL); err != nil {
		return "", err
	}
	var secret string
	if db := store(); db != nil && s.OAuthClientID != "" {
		db.Get(table, tokenKey(owner, name), &secret)
	}
	return MCP().StartOAuthFor(MCPOAuthStart{
		User: owner, Server: oauthKey(owner, name), URL: s.URL, RedirectURI: redirectURI,
		ClientID: s.OAuthClientID, ClientSecret: secret, Scopes: s.OAuthScopes,
		PublicOnly: !UserIsAdmin(owner),
	})
}

// Disconnect drops owner's sign-in to one of their OAuth servers.
func Disconnect(owner, name string) {
	MCP().DisconnectUser(owner, oauthKey(owner, name))
	dropClient(owner, name)
}

// AfterConnect checks each of owner's OAuth servers that is signed in and
// has not answered since: a sign-in just finished, and the callback does not
// say for which.
func AfterConnect(ctx context.Context, owner string) {
	for _, s := range List(owner) {
		if s.Auth == AuthOAuth && Connected(owner, s.Name) && (s.CheckedAt.IsZero() || s.CheckErr != "") {
			Check(ctx, owner, s.Name)
		}
	}
}

// Connections are owner's OAuth servers, for the Connected accounts list.
func Connections(owner string) []PerUserConnection {
	var out []PerUserConnection
	for _, s := range List(owner) {
		if s.Auth != AuthOAuth {
			continue
		}
		out = append(out, PerUserConnection{
			Name:        s.Name,
			Description: "Your own MCP server at " + strings.TrimPrefix(strings.TrimPrefix(origin(s.URL), "https://"), "http://") + ".",
			Connected:   Connected(owner, s.Name),
			OAuth:       true,
			Kind:        ConnKind,
			ConnectURL:  "/account/mcp/connect?own=1&server=" + url.QueryEscape(s.Name),
		})
	}
	return out
}

// ConnKind marks one of these in the Connected accounts list, so a Disconnect
// reaches this server and not an administrator's of the same name.
const ConnKind = "mcp_own"

// --- the owner's tools ---

// named is one tool of one of owner's servers, with the name an agent knows
// it by.
type named struct {
	server    Server
	raw, name string
	desc      string
	params    map[string]ToolParam
	required  []string
	confirm   bool
}

// namedTools are every tool of every one of owner's servers, on or off, with
// their names. Named across all of them, so switching one off never renames
// another's tools (a name lists on an agent; it must stay that name). A
// server offered as one tool takes its own name, sanitized, or name_mcp when
// that is taken; its tools keep theirs for the allowlist to follow.
func namedTools(owner string, taken map[string]bool) (tools []named, whole map[string]string) {
	if taken == nil {
		taken = map[string]bool{}
	}
	whole = map[string]string{}
	for _, s := range List(owner) {
		if s.OneTool && len(s.Tools) > 0 {
			for _, cand := range []string{s.Name, s.Name + "_mcp"} {
				if n, _, _, _ := MCPToolShape(cand, "", nil, nil); n != "" {
					n = strings.TrimSuffix(n, "_")
					if !taken[n] {
						whole[s.Name] = n
						taken[n] = true
						break
					}
				}
			}
		}
		for _, t := range s.Tools {
			name, params, required, confirm := MCPToolShape(s.Name, t.Name, t.Schema, taken)
			if name == "" {
				continue
			}
			taken[name] = true
			desc := strings.TrimSpace(t.Description)
			if desc == "" {
				desc = t.Name
			}
			tools = append(tools, named{server: s, raw: t.Name, name: name, desc: desc, params: params, required: required, confirm: confirm})
		}
	}
	return tools, whole
}

// Offers reports whether one of owner's servers, on or off, offers a tool an
// agent knows by name, in either shape. An agent's tool list keeps such a
// name while its server is switched off, as it keeps any tool that is theirs.
func Offers(owner, name string, taken map[string]bool) bool {
	tools, whole := namedTools(owner, taken)
	for _, w := range whole {
		if w == name {
			return true
		}
	}
	for _, n := range tools {
		if n.name == name {
			return true
		}
	}
	return false
}

// Aliases are, for each tool owner's usable servers offer an agent, the
// names an agent's tool list may know it by in the other shape: a server's
// one tool answers to each of its tools' names, and each tool to the
// server's one name. So switching a server between the shapes keeps every
// agent that lists its tools.
func Aliases(owner string, taken map[string]bool) map[string][]string {
	tools, whole := namedTools(owner, taken)
	out := map[string][]string{}
	for _, n := range tools {
		if w, one := whole[n.server.Name]; one {
			out[w] = append(out[w], n.name)
		} else if alt := sanitizedServerName(n.server.Name); alt != "" {
			out[n.name] = append(out[n.name], alt)
		}
	}
	return out
}

// OneToolActions maps each of owner's servers set to one tool to its actions,
// keyed by the name each would have as a separate tool: one tool, then that
// name, then the action. A grant of a separate tool's name reaches the one
// tool, and with this only the action it names.
func OneToolActions(owner string, taken map[string]bool) map[string]map[string]string {
	tools, whole := namedTools(owner, taken)
	out := map[string]map[string]string{}
	for _, n := range tools {
		if w, one := whole[n.server.Name]; one {
			if out[w] == nil {
				out[w] = map[string]string{}
			}
			out[w][n.name] = n.raw
		}
	}
	return out
}

func sanitizedServerName(server string) string {
	n, _, _, _ := MCPToolShape(server, "", nil, nil)
	return strings.TrimSuffix(n, "_")
}

// Tools are the tools owner's usable servers offer, for a run as sess: the
// same names and shapes an administrator's server's tools get (MCPToolShape),
// free of every name in taken, or one tool per server set to OneTool. A call
// made as anybody but the owner is refused: a person's server answers for
// them alone. One whose name says it changes something asks in the chat
// first, and is refused where nobody is watching to say yes.
func Tools(sess *ToolSession, owner string, taken map[string]bool) []AgentToolDef {
	if owner == "" {
		return nil
	}
	tools, whole := namedTools(owner, taken)
	var out []AgentToolDef
	groups := map[string]*GroupedTool{}
	var order []string
	for _, n := range tools {
		if !n.server.Usable() {
			continue
		}
		run := toolRun(sess, owner, n)
		if w, one := whole[n.server.Name]; one {
			g := groups[w]
			if g == nil {
				g = NewGroupedTool(w, "The tools of "+n.server.Name+", your own MCP server, one action each.")
				groups[w] = g
				order = append(order, w)
			}
			g.AddAction(n.raw, &GroupedToolAction{
				Description: n.desc, Params: n.params, Required: n.required,
				Caps: []Capability{CapNetwork, CapRead}, NeedsConfirm: n.confirm,
				Handler: func(args map[string]any, _ *ToolSession) (string, error) { return run(context.Background(), args) },
			})
			continue
		}
		out = append(out, AgentToolDef{
			Tool: Tool{
				Name:        n.name,
				Description: n.desc + " (from " + n.server.Name + ", your own MCP server)",
				Parameters:  n.params,
				Required:    n.required,
				Caps:        []Capability{CapNetwork, CapRead},
			},
			NeedsConfirm: n.confirm,
			Handler:      run,
		})
	}
	for _, w := range order {
		out = append(out, ChatToolToAgentToolDefWithSession(groups[w], sess))
	}
	return out
}

// toolRun is one of the owner's tools, called as sess.
func toolRun(sess *ToolSession, owner string, n named) func(ctx context.Context, args map[string]any) (string, error) {
	srvName, raw, label := n.server.Name, n.raw, n.name
	return func(ctx context.Context, args map[string]any) (string, error) {
		if sess != nil && sess.Username != "" && sess.Username != owner {
			return "", fmt.Errorf("%s is %s's own MCP server and answers for them alone", srvName, owner)
		}
		cur, ok := Load(owner, srvName)
		if !ok || !cur.Usable() {
			return "", fmt.Errorf("%s is switched off or gone: its owner manages it on their Extensions page", srvName)
		}
		if n.confirm {
			if sess == nil || sess.AskInChat == nil {
				return "", fmt.Errorf("not run: %s changes something on %s, and nobody is watching this run to approve it", label, srvName)
			}
			brief, _ := json.Marshal(args)
			if sess.AskInChat(fmt.Sprintf("Run %s on %s, your own MCP server? Its name says it changes something.", raw, srvName), clip(string(brief), 600), []string{"Run it"}) == "" {
				return fmt.Sprintf("Not run: %s was not approved.", label), nil
			}
		}
		if sess != nil && ctx == context.Background() {
			ctx = sess.Context()
		}
		return call(ctx, cur, raw, args)
	}
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
