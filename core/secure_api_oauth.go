// OAuth2 support for the secure-API credential system, built on snugforge
// apiclient (the same OAuth2 client + pluggable encrypted TokenStore +
// refresh machinery gohort already uses for the LLM API). One `oauth2`
// credential TYPE, grant-driven:
//
//   client_credentials  secret = client_secret      (eBay, most vendor APIs)
//   jwt_bearer          secret = RSA private key     (Google service accts)
//   refresh_token       secret = the refresh token   (public-client refresh)
//
// apiclient owns the token lifecycle: persistent encrypted storage, expiry
// tracking, the built-in refresh_token grant, and re-acquisition on expiry.
// We only supply the NewToken minter for the grants apiclient does not mint
// natively (client_credentials, jwt_bearer) and a TokenStore over the
// secure-API DB so OAuth tokens sit encrypted next to the credentials.

package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/snugforge/apiclient"
	"github.com/cmcoffee/snugforge/jwcrypt"
)

const (
	// SecureCredOAuth2 mints a Bearer token via c.Grant rather than
	// injecting a static secret.
	SecureCredOAuth2 = "oauth2"

	OAuthGrantClientCredentials = "client_credentials"
	OAuthGrantJWTBearer         = "jwt_bearer"
	OAuthGrantRefreshToken      = "refresh_token"
)

func oauthTokenKey(name string) string { return name + "__oauthtoken" }

// secureOAuthTokenStore implements apiclient.TokenStore over the secure-API
// DB (encrypted via CryptSet), so OAuth access/refresh tokens persist and
// stay encrypted alongside the credential secrets in the same store.
type secureOAuthTokenStore struct{ s *SecureAPI }

func (t secureOAuthTokenStore) Save(name string, a *apiclient.Auth) error {
	t.s.db.CryptSet(secureAPITable, oauthTokenKey(name), a)
	return nil
}
func (t secureOAuthTokenStore) Load(name string) (*apiclient.Auth, error) {
	var a apiclient.Auth
	if !t.s.db.Get(secureAPITable, oauthTokenKey(name), &a) || a.AccessToken == "" && a.RefreshToken == "" {
		return nil, nil // none stored — apiclient acquires via NewToken
	}
	return &a, nil
}
func (t secureOAuthTokenStore) Delete(name string) error {
	t.s.db.Unset(secureAPITable, oauthTokenKey(name))
	return nil
}

// Per-credential apiclient cache. Each oauth2 credential gets one client,
// configured for its grant; invalidated (with its stored token) when the
// credential is saved so config changes take effect.
var (
	oauthClientsMu sync.Mutex
	oauthClients   = map[string]*apiclient.APIClient{}
)

func invalidateOAuthClient(name string) {
	oauthClientsMu.Lock()
	delete(oauthClients, name)
	oauthClientsMu.Unlock()
	if s := Secure(); s != nil && s.ready() {
		s.db.Unset(secureAPITable, oauthTokenKey(name))
	}
}

// oauthClient lazily builds (and caches) the apiclient configured for this
// credential's grant. `secret` is the grant's sensitive material.
func (s *SecureAPI) oauthClient(c SecureCredential, secret string) *apiclient.APIClient {
	oauthClientsMu.Lock()
	defer oauthClientsMu.Unlock()
	if ac, ok := oauthClients[c.Name]; ok {
		return ac
	}
	store := secureOAuthTokenStore{s: s}
	ac := &apiclient.APIClient{
		ApplicationID:  c.ClientID,
		RefreshPath:    c.TokenURL, // built-in refresh_token grant posts here
		ReacquireToken: true,       // re-mint via NewToken on expiry (cc/jwt carry no refresh token)
	}
	ac.TokenStore = store
	// NewToken acquires the INITIAL token for grants apiclient doesn't mint
	// natively, and is the re-acquire path when a refresh fails.
	ac.NewToken = func(string) (*apiclient.Auth, error) {
		access, refresh, ttl, err := s.mintOAuthGrant(c, secret)
		if err != nil {
			return nil, err
		}
		return &apiclient.Auth{
			AccessToken:  access,
			RefreshToken: refresh,
			Scope:        c.Scope,
			Expires:      time.Now().Add(ttl).Unix(),
		}, nil
	}
	oauthClients[c.Name] = ac
	return ac
}

// mintOAuthGrant performs the token request for the credential's grant and
// returns (access, refresh, ttl). The grant builds the body/auth; response
// parsing is shared. Token endpoint must be https.
func (s *SecureAPI) mintOAuthGrant(c SecureCredential, secret string) (string, string, time.Duration, error) {
	if strings.TrimSpace(c.TokenURL) == "" {
		return "", "", 0, fmt.Errorf("oauth credential %q has no token_url", c.Name)
	}
	if !strings.HasPrefix(strings.ToLower(c.TokenURL), "https://") {
		return "", "", 0, fmt.Errorf("oauth token_url for %q must be https", c.Name)
	}
	form := url.Values{}
	basicAuth := false
	switch c.Grant {
	case OAuthGrantClientCredentials:
		form.Set("grant_type", "client_credentials")
		basicAuth = true // client_id:client_secret via HTTP Basic (eBay + most)
	case OAuthGrantJWTBearer:
		assertion, err := buildJWTAssertion(c, secret)
		if err != nil {
			return "", "", 0, err
		}
		form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
		form.Set("assertion", assertion)
	case OAuthGrantRefreshToken:
		form.Set("grant_type", "refresh_token")
		form.Set("refresh_token", secret) // secret = the refresh token
		if c.ClientID != "" {
			form.Set("client_id", c.ClientID)
		}
	default:
		return "", "", 0, fmt.Errorf("unsupported oauth grant %q for %q (use client_credentials | jwt_bearer | refresh_token)", c.Grant, c.Name)
	}
	if c.Scope != "" {
		form.Set("scope", c.Scope)
	}

	ctx, cancel := context.WithTimeout(context.Background(), secureAPIRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", c.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if basicAuth && c.ClientID != "" {
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(c.ClientID+":"+secret)))
	}
	resp, err := NewBoundedHTTPClient().Do(req)
	if err != nil {
		return "", "", 0, fmt.Errorf("oauth token request to %s failed: %w", c.TokenURL, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != 200 {
		// Surface the provider's error verbatim so LLM-assisted setup can
		// read it and fix the config (wrong scope, bad client, etc.).
		return "", "", 0, fmt.Errorf("oauth token endpoint %s returned %d: %s", c.TokenURL, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", "", 0, fmt.Errorf("oauth token response from %s was not JSON: %w", c.TokenURL, err)
	}
	if parsed.AccessToken == "" {
		return "", "", 0, fmt.Errorf("oauth token response from %s had no access_token: %s", c.TokenURL, strings.TrimSpace(string(raw)))
	}
	ttl := time.Duration(parsed.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = time.Hour // sane default when the provider omits expires_in
	}
	return parsed.AccessToken, parsed.RefreshToken, ttl, nil
}

// buildJWTAssertion signs the JWT assertion for the jwt_bearer grant
// (RFC 7523) with jwcrypt. The credential secret is the RSA private key
// (PEM / PKCS8 / JWK, auto-detected). aud defaults to token_url.
func buildJWTAssertion(c SecureCredential, privateKey string) (string, error) {
	key, err := jwcrypt.ParseRSAPrivateKey([]byte(privateKey))
	if err != nil {
		return "", fmt.Errorf("parse jwt signing key for %q: %w", c.Name, err)
	}
	if strings.TrimSpace(c.JWTIssuer) == "" {
		return "", fmt.Errorf("jwt_bearer credential %q needs jwt_issuer (the iss claim)", c.Name)
	}
	aud := strings.TrimSpace(c.JWTAudience)
	if aud == "" {
		aud = c.TokenURL
	}
	now := time.Now()
	claims := map[string]interface{}{
		"iss": c.JWTIssuer,
		"aud": aud,
		"iat": now.Unix(),
		"exp": now.Add(time.Hour).Unix(),
	}
	if sub := strings.TrimSpace(c.JWTSubject); sub != "" {
		claims["sub"] = sub
	}
	if c.Scope != "" {
		claims["scope"] = c.Scope
	}
	header := map[string]string{}
	if c.JWTKeyID != "" {
		header["kid"] = c.JWTKeyID
	}
	return jwcrypt.SignRS256(key, claims, header)
}

// oauthSetToken sets the Authorization header on req for an oauth2
// credential via apiclient (cached/refreshed/re-minted as needed) and
// returns the bearer token string so the dispatcher can redact it.
func (s *SecureAPI) oauthSetToken(c SecureCredential, secret string, req *http.Request) (string, error) {
	if err := s.oauthClient(c, secret).SetToken(c.Name, req); err != nil {
		return "", err
	}
	return strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer "), nil
}

// SaveOAuthDraft persists an oauth2 credential's CONFIG without a secret:
// the "Builder scaffolds, admin completes" path. Stored DISABLED (inert,
// not dispatchable) with a "(pending)" secret placeholder so the admin UI
// flags it as needing the secret. The admin pastes the real secret (the
// handler preserves this config) and enables it to go live. Validates the
// config so a malformed draft is rejected up front.
func (s *SecureAPI) SaveOAuthDraft(c SecureCredential) error {
	if !s.ready() {
		return fmt.Errorf("secure-api store not initialized")
	}
	c.Type = SecureCredOAuth2
	c.Disabled = true // inert until the admin adds the secret + enables
	switch c.Grant {
	case OAuthGrantClientCredentials, OAuthGrantJWTBearer, OAuthGrantRefreshToken:
	default:
		return fmt.Errorf("draft needs a grant: client_credentials, jwt_bearer, or refresh_token")
	}
	if strings.TrimSpace(c.TokenURL) == "" || !strings.HasPrefix(strings.ToLower(c.TokenURL), "https://") {
		return fmt.Errorf("draft needs an https token_url")
	}
	if strings.TrimSpace(c.AllowedURLPattern) == "" {
		return fmt.Errorf("draft needs an allowed_url_pattern (e.g. https://api.ebay.com/buy/browse/**)")
	}
	if c.Grant == OAuthGrantJWTBearer && strings.TrimSpace(c.JWTIssuer) == "" {
		return fmt.Errorf("jwt_bearer draft needs jwt_issuer")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var existing SecureCredential
	if s.db.Get(secureAPITable, c.Name, &existing) {
		c.CreatedAt = existing.CreatedAt
		c.LastUsedAt = existing.LastUsedAt
	} else {
		c.CreatedAt = time.Now()
	}
	s.db.Set(secureAPITable, c.Name, c)
	// Placeholder secret so loadSecret finds something; the admin replaces
	// it with the real client_secret / private key / refresh token.
	var hasSecret string
	if !s.db.Get(secureAPITable, secureCredSecretKey(c.Name), &hasSecret) || hasSecret == "" || hasSecret == "(pending)" {
		s.db.CryptSet(secureAPITable, secureCredSecretKey(c.Name), "(pending)")
	}
	invalidateOAuthClient(c.Name)
	return nil
}

// OAuthDraftPending reports whether a credential is an unfinished oauth2
// draft (config present, real secret not yet supplied). The admin UI uses
// this to badge it "needs secret".
func (s *SecureAPI) OAuthDraftPending(name string) bool {
	if c, ok := s.Load(name); !ok || c.Type != SecureCredOAuth2 {
		return false
	}
	secret, ok := s.loadSecret(name)
	return !ok || secret == "" || secret == "(pending)"
}

// TestMintFromPosted mints (and discards) an access token from a credential
// config supplied directly (typically the admin form's current working
// state), without requiring a save first. The posted secret is used when
// present; when blank (the admin is editing an existing credential and left
// the secret field untouched) it falls back to the stored secret. This is
// what the admin UI's inline "Test token" button calls so the operator can
// verify the config + secret before or after committing it.
func (s *SecureAPI) TestMintFromPosted(c SecureCredential, postedSecret string) (string, error) {
	if !s.ready() {
		return "", fmt.Errorf("secure-api store not initialized")
	}
	if c.Type != SecureCredOAuth2 {
		return "", fmt.Errorf("only oauth2 credentials mint tokens")
	}
	if strings.TrimSpace(c.Grant) == "" || strings.TrimSpace(c.TokenURL) == "" {
		return "", fmt.Errorf("grant and token_url are required to test")
	}
	secret := strings.TrimSpace(postedSecret)
	if secret == "" || secret == "(pending)" {
		if stored, ok := s.loadSecret(c.Name); ok {
			secret = strings.TrimSpace(stored)
		}
	}
	if secret == "" || secret == "(pending)" {
		return "", fmt.Errorf("enter the client secret / private key / refresh token to test")
	}
	// Don't let a test built from possibly-unsaved config poison the cached
	// client for the saved credential — invalidate on both sides of the mint.
	invalidateOAuthClient(c.Name)
	_, _, ttl, err := s.mintOAuthGrant(c, secret)
	invalidateOAuthClient(c.Name)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("OK — minted an access token via %s (valid ~%s).", c.Grant, ttl.Round(time.Second)), nil
}

// TestMintToken mints (and discards) an access token for an oauth2
// credential and reports the outcome — the enabler for LLM-assisted setup:
// the model fills the config in from the API's OAuth docs, calls this to
// verify, and reads any error to iterate. Never returns the token itself.
func (s *SecureAPI) TestMintToken(name string) (string, error) {
	c, ok := s.Load(name)
	if !ok {
		return "", fmt.Errorf("credential %q not found", name)
	}
	if c.Type != SecureCredOAuth2 {
		return "", fmt.Errorf("credential %q is not an oauth2 credential", name)
	}
	secret, ok := s.loadSecret(name)
	if !ok || secret == "" {
		return "", fmt.Errorf("credential %q has no stored secret (the client_secret / private key / refresh token)", name)
	}
	invalidateOAuthClient(name)
	_, _, ttl, err := s.mintOAuthGrant(c, secret)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("OK — minted an access token via %s (valid ~%s). Ready to use as fetch_url_%s against %s.", c.Grant, ttl.Round(time.Second), c.Name, c.AllowedURLPattern), nil
}
