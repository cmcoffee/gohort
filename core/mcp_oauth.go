// MCP OAuth 2.1 client flow (the authorization spec, 2025-06-18) for
// hosted MCP servers like Atlassian Cloud. These are the PURE protocol
// functions — discovery, dynamic client registration, PKCE, and token
// exchange/refresh — with no manager or DB dependency, so they unit-test
// against an httptest authorization/resource server. The manager
// integration (per-user token storage, pending-state, the refresh-aware
// Authorizer, StartOAuth/CompleteOAuth) lives in mcp_manager.go.
//
// Flow: 401+WWW-Authenticate -> Protected Resource Metadata (RFC 9728)
// -> Authorization Server Metadata (RFC 8414) -> Dynamic Client
// Registration (RFC 7591) -> authorize with PKCE S256 + resource
// indicator (RFC 8707) -> code exchange -> Bearer on the transport.
package core

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// mcpOAuthHTTP is the client for all OAuth-side calls (discovery, DCR,
// token). Per-call context still bounds each request.
var mcpOAuthHTTP = &http.Client{Timeout: 30 * time.Second}

// mcpOAuthConfig is the server-level OAuth config, shared across users:
// the (dynamically registered) client + discovered endpoints + the
// canonical resource URI. Persisted encrypted under <server>__oauthcfg.
type mcpOAuthConfig struct {
	ClientID             string `json:"client_id"`
	ClientSecret         string `json:"client_secret,omitempty"`
	AuthEndpoint         string `json:"authorization_endpoint"`
	TokenEndpoint        string `json:"token_endpoint"`
	RegistrationEndpoint string `json:"registration_endpoint,omitempty"`
	Resource             string `json:"resource"` // canonical MCP server URI (RFC 8707)
	Scope                string `json:"scope,omitempty"`
	// Audience is the OAuth audience for Auth0/Okta-style authorization servers
	// (e.g. Atlassian wants audience=api.atlassian.com). When set it is sent in
	// place of the RFC 8707 `resource` param, which those providers ignore — and
	// which some reject when it isn't a registered API identifier.
	Audience string `json:"audience,omitempty"`
	// RegisteredRedirects is the redirect_uris the DCR client was
	// registered with, remembered so a later flow can tell whether the
	// cached client will accept the URI it is about to send.
	//
	// This is the fix for a failure with no local symptom at all: the
	// client is registered once, with whichever callback the first flow
	// used, and cached. A second entry point — the per-user connect,
	// whose callback lives under /account rather than /admin — then sent
	// a URI that client had never registered, and the AUTHORIZATION
	// SERVER refused it. Nothing here saw an error; the person saw
	// "the app's callback URL is invalid" on the provider's own page,
	// with no way to register anything by hand because the client was
	// created programmatically.
	RegisteredRedirects []string `json:"registered_redirects,omitempty"`
}

// knowsRedirect reports whether the registered client will accept this
// redirect URI. An empty list means the client predates the field, so
// nothing is known — treated as "does not know it" only when the URI is
// not the one thing we can infer, to avoid re-registering every old
// client on sight.
func (c mcpOAuthConfig) knowsRedirect(uri string) bool {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return true
	}
	for _, r := range c.RegisteredRedirects {
		if strings.EqualFold(strings.TrimSpace(r), uri) {
			return true
		}
	}
	return false
}

// mcpApplyResourceParam sets the resource-targeting parameter on an authorize or
// token request: `audience` for Auth0-style servers when configured, otherwise
// the RFC 8707 `resource` indicator (the MCP-spec default).
func mcpApplyResourceParam(v url.Values, cfg mcpOAuthConfig) {
	if cfg.Audience != "" {
		v.Set("audience", cfg.Audience)
		return
	}
	if cfg.Resource != "" {
		v.Set("resource", cfg.Resource)
	}
}

// mcpOAuthToken is a per-user token set. Persisted encrypted under
// <server>__oauthtok__<user>.
type mcpOAuthToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Expiry       time.Time `json:"expiry"`
}

func mcpOAuthCfgKey(server string) string       { return server + "__oauthcfg" }
func mcpOAuthTokKey(server, user string) string { return server + "__oauthtok__" + user }

// --- discovery (RFC 9728 + RFC 8414) ---

// mcpDiscover resolves a hosted MCP server URL to its OAuth endpoints +
// canonical resource. Probes the server for a 401 WWW-Authenticate
// (resource_metadata pointer), falls back to the well-known path, then
// reads the protected-resource metadata and the authorization-server
// metadata.
func mcpDiscover(ctx context.Context, serverURL string) (mcpOAuthConfig, error) {
	var cfg mcpOAuthConfig

	var prm struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
		ScopesSupported      []string `json:"scopes_supported"`
	}
	// Try each candidate protected-resource-metadata URL in priority order
	// (WWW-Authenticate pointer, then path-aware well-known, then origin
	// well-known) until one yields metadata. A single guessed URL missed
	// servers mounted at a subpath (RFC 9728 inserts the well-known segment
	// BEFORE the resource path) — e.g. an MCP endpoint at /v1/mcp.
	var prmErr error
	got := false
	for _, prmURL := range mcpResourceMetadataCandidates(ctx, serverURL) {
		if err := mcpGetJSON(ctx, prmURL, &prm); err != nil {
			prmErr = err
			continue
		}
		got = true
		break
	}
	if !got {
		if prmErr == nil {
			prmErr = fmt.Errorf("no protected-resource metadata at any well-known location")
		}
		return cfg, fmt.Errorf("protected-resource metadata: %w", prmErr)
	}
	if len(prm.AuthorizationServers) == 0 {
		return cfg, fmt.Errorf("resource metadata lists no authorization_servers")
	}
	cfg.Resource = strings.TrimRight(prm.Resource, "/")
	if cfg.Resource == "" {
		cfg.Resource = mcpCanonicalResource(serverURL)
	}
	cfg.Scope = strings.Join(prm.ScopesSupported, " ")

	as := strings.TrimRight(prm.AuthorizationServers[0], "/")
	var asm struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
		RegistrationEndpoint  string `json:"registration_endpoint"`
	}
	// RFC 8414 well-known, with the OIDC discovery doc as a fallback.
	if err := mcpGetJSON(ctx, as+"/.well-known/oauth-authorization-server", &asm); err != nil || asm.AuthorizationEndpoint == "" {
		_ = mcpGetJSON(ctx, as+"/.well-known/openid-configuration", &asm)
	}
	if asm.AuthorizationEndpoint == "" || asm.TokenEndpoint == "" {
		return cfg, fmt.Errorf("authorization-server metadata missing endpoints (from %s)", as)
	}
	cfg.AuthEndpoint = asm.AuthorizationEndpoint
	cfg.TokenEndpoint = asm.TokenEndpoint
	cfg.RegistrationEndpoint = asm.RegistrationEndpoint
	return cfg, nil
}

// mcpResourceMetadataCandidates lists protected-resource-metadata URLs to try,
// in priority order: the pointer the server advertises via WWW-Authenticate on a
// 401 (RFC 9728), then the path-aware well-known (well-known segment inserted
// before the resource path), then the bare-origin well-known (servers that host
// it at the root). Deduplicated, empties dropped.
func mcpResourceMetadataCandidates(ctx context.Context, serverURL string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(u string) {
		if u != "" && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	add(mcpProbeWWWAuthPRM(ctx, serverURL))
	add(mcpWellKnownPRM(serverURL))     // path-aware (RFC 9728)
	add(mcpWellKnownPRMRoot(serverURL)) // origin
	return out
}

// mcpProbeWWWAuthPRM makes an unauthenticated request to the server and returns
// the resource_metadata URL it advertises via WWW-Authenticate on a 401 (RFC
// 9728). "" when the server doesn't 401 or doesn't advertise a pointer.
func mcpProbeWWWAuthPRM(ctx context.Context, serverURL string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL, nil)
	if err != nil {
		return ""
	}
	resp, err := mcpOAuthHTTP.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusUnauthorized {
		return ""
	}
	return parseResourceMetadata(resp.Header.Get("WWW-Authenticate"))
}

// mcpWellKnownPRM builds the RFC 9728 path-aware protected-resource-metadata URL:
// the well-known segment is inserted BETWEEN the host and the resource's path
// (https://h/v1/mcp -> https://h/.well-known/oauth-protected-resource/v1/mcp).
func mcpWellKnownPRM(serverURL string) string {
	u, err := url.Parse(serverURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return strings.TrimRight(serverURL, "/") + "/.well-known/oauth-protected-resource"
	}
	path := strings.TrimRight(u.Path, "/")
	return u.Scheme + "://" + u.Host + "/.well-known/oauth-protected-resource" + path
}

// mcpWellKnownPRMRoot builds <origin>/.well-known/oauth-protected-resource — the
// fallback for servers that host the metadata at the root regardless of path.
func mcpWellKnownPRMRoot(serverURL string) string {
	u, err := url.Parse(serverURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return strings.TrimRight(serverURL, "/") + "/.well-known/oauth-protected-resource"
	}
	return u.Scheme + "://" + u.Host + "/.well-known/oauth-protected-resource"
}

// mcpCanonicalResource returns the canonical MCP server URI (RFC 8707):
// scheme+host+path, no query/fragment, no trailing slash.
func mcpCanonicalResource(serverURL string) string {
	u, err := url.Parse(serverURL)
	if err != nil {
		return strings.TrimRight(serverURL, "/")
	}
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}

// parseResourceMetadata extracts the resource_metadata URL from a
// WWW-Authenticate header value (quoted or bare). "" if absent.
func parseResourceMetadata(header string) string {
	idx := strings.Index(header, "resource_metadata")
	if idx < 0 {
		return ""
	}
	rest := strings.TrimLeft(header[idx+len("resource_metadata"):], " =")
	if rest == "" {
		return ""
	}
	if rest[0] == '"' {
		rest = rest[1:]
		if j := strings.IndexByte(rest, '"'); j >= 0 {
			return rest[:j]
		}
		return ""
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] == ',' || rest[i] == ' ' {
			return rest[:i]
		}
	}
	return rest
}

// --- dynamic client registration (RFC 7591) ---

// mcpRegisterClient registers a public PKCE client at the AS and returns
// the issued client_id (and client_secret if the AS makes it
// confidential).
func mcpRegisterClient(ctx context.Context, registrationEndpoint string, redirectURIs ...string) (clientID, clientSecret string, err error) {
	if registrationEndpoint == "" {
		return "", "", fmt.Errorf("authorization server has no registration_endpoint (manual client_id needed)")
	}
	// EVERY callback this deployment can send, not just the one that
	// happens to be starting the flow. RFC 7591 takes an array, the
	// client is registered once and cached, and a client registered with
	// one URI rejects the other for good — which is invisible here,
	// because the refusal happens on the provider's page.
	uris := []string{}
	seen := map[string]bool{}
	for _, u := range redirectURIs {
		if u = strings.TrimSpace(u); u != "" && !seen[u] {
			seen[u] = true
			uris = append(uris, u)
		}
	}
	if len(uris) == 0 {
		return "", "", fmt.Errorf("no redirect URI to register")
	}
	reqBody, _ := json.Marshal(map[string]any{
		"client_name":                "gohort",
		"redirect_uris":              uris,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, registrationEndpoint, bytes.NewReader(reqBody))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := mcpOAuthHTTP.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("client registration %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var reg struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.Unmarshal(body, &reg); err != nil {
		return "", "", err
	}
	if reg.ClientID == "" {
		return "", "", fmt.Errorf("registration returned no client_id")
	}
	return reg.ClientID, reg.ClientSecret, nil
}

// --- PKCE + state ---

// mcpPKCE returns a fresh (verifier, S256 challenge) pair.
func mcpPKCE() (verifier, challenge string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge
}

// mcpRandomState returns an unguessable state/nonce value.
func mcpRandomState() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// --- authorize URL + token exchange ---

// mcpAuthorizeURL builds the browser authorization URL (PKCE S256 +
// resource indicator).
func mcpAuthorizeURL(cfg mcpOAuthConfig, redirectURI, state, challenge string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", redirectURI)
	if cfg.Scope != "" {
		q.Set("scope", cfg.Scope)
	}
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	mcpApplyResourceParam(q, cfg)
	sep := "?"
	if strings.Contains(cfg.AuthEndpoint, "?") {
		sep = "&"
	}
	return cfg.AuthEndpoint + sep + q.Encode()
}

// mcpExchangeCode swaps an authorization code (+ PKCE verifier) for tokens.
func mcpExchangeCode(ctx context.Context, cfg mcpOAuthConfig, code, verifier, redirectURI string) (mcpOAuthToken, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("code_verifier", verifier)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", cfg.ClientID)
	if cfg.ClientSecret != "" {
		form.Set("client_secret", cfg.ClientSecret)
	}
	mcpApplyResourceParam(form, cfg)
	return mcpTokenRequest(ctx, cfg.TokenEndpoint, form, mcpOAuthToken{})
}

// mcpRefreshAccess renews an access token using the refresh token. The
// old refresh token is retained if the AS doesn't rotate one.
func mcpRefreshAccess(ctx context.Context, cfg mcpOAuthConfig, prev mcpOAuthToken) (mcpOAuthToken, error) {
	if prev.RefreshToken == "" {
		return mcpOAuthToken{}, fmt.Errorf("no refresh token")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", prev.RefreshToken)
	form.Set("client_id", cfg.ClientID)
	if cfg.ClientSecret != "" {
		form.Set("client_secret", cfg.ClientSecret)
	}
	mcpApplyResourceParam(form, cfg)
	return mcpTokenRequest(ctx, cfg.TokenEndpoint, form, prev)
}

// mcpTokenRequest POSTs a token request and parses the response. prev
// carries forward a refresh token the AS omits on rotation-less refresh.
func mcpTokenRequest(ctx context.Context, endpoint string, form url.Values, prev mcpOAuthToken) (mcpOAuthToken, error) {
	var tok mcpOAuthToken
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tok, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := mcpOAuthHTTP.Do(req)
	if err != nil {
		return tok, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return tok, fmt.Errorf("token endpoint %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var r struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return tok, err
	}
	if r.AccessToken == "" {
		return tok, fmt.Errorf("token response had no access_token")
	}
	tok.AccessToken = r.AccessToken
	tok.RefreshToken = r.RefreshToken
	if tok.RefreshToken == "" {
		tok.RefreshToken = prev.RefreshToken
	}
	if r.ExpiresIn > 0 {
		tok.Expiry = time.Now().Add(time.Duration(r.ExpiresIn) * time.Second)
	}
	return tok, nil
}

// mcpGetJSON GETs url and decodes a JSON body into out.
func mcpGetJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := mcpOAuthHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GET %s: %d", url, resp.StatusCode)
	}
	return json.Unmarshal(body, out)
}

// MCPRedirectURIs is every callback path this deployment can send a
// per-user MCP consent to, derived from any one of them.
//
// There are two, and they cannot be merged: the admin Connect button's
// callback lives under /admin, whose whole sub-mux is gated behind an
// admin check, so a non-admin cannot complete a consent that lands
// there — the per-user flow needs its own under /account. Both are
// registered on the client so either entry point works with one
// registration, which is the thing that was missing.
//
// Derived from a given URI rather than from config so the caller's own
// base (external_url, or scheme+host) is preserved exactly; guessing a
// base here is how a redirect URI stops matching by one character.
func MCPRedirectURIs(primary string, extra ...string) []string {
	out := []string{}
	seen := map[string]bool{}
	add := func(u string) {
		if u = strings.TrimSpace(u); u != "" && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	add(primary)
	for _, e := range extra {
		add(e)
	}
	// The sibling path, on the same base.
	for _, known := range mcpCallbackPaths {
		if base, ok := strings.CutSuffix(primary, known); ok {
			for _, p := range mcpCallbackPaths {
				add(base + p)
			}
			break
		}
	}
	return out
}

// mcpCallbackPaths are the two per-user MCP consent callbacks this
// deployment serves. Listed once, here, so a flow that adds a third has
// one place to say so.
var mcpCallbackPaths = []string{
	"/account/mcp/callback",                 // any user, from Extensions or a chat prompt
	"/admin/api/mcp-servers/oauth/callback", // the Connect button on the admin MCP row
}
