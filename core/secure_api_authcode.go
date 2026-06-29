// Authorization-code OAuth for per-user SecureAPI credentials — the interactive
// "Connect with your account" consent flow. The admin registers the OAuth app
// once (authorize_url, token_url, client_id, scope; client_secret in the
// encrypted __secret); each user runs the consent flow and gets their OWN
// access/refresh token, stored encrypted per (credential, user). The dispatch
// injects the calling user's access token (refreshing when expired).
//
// The HTTP start/callback handlers live in apps/account (user-facing); this file
// owns the token storage, the URL building, the code exchange/refresh, and the
// short-lived pending-state map that links a consent redirect back to its user.
package core

import (
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
	"sync"
	"time"
)

// CredOAuthToken is a per-user token set for an authorization_code credential.
type CredOAuthToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
}

// IsAuthCode reports whether a credential uses the interactive OAuth consent flow
// (vs a shared/per-user static key or a machine grant).
func (c SecureCredential) IsAuthCode() bool { return c.Grant == "authorization_code" }

func secureCredUserTokenKey(name, user string) string { return name + "__usertok__" + user }

// SaveUserToken stores (encrypted) a user's OAuth token for a credential.
func (s *SecureAPI) SaveUserToken(name, user string, tok CredOAuthToken) error {
	if !s.ready() || name == "" || user == "" {
		return fmt.Errorf("name and user required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.db.CryptSet(secureAPITable, secureCredUserTokenKey(name, user), tok)
	return nil
}

func (s *SecureAPI) loadUserToken(name, user string) (CredOAuthToken, bool) {
	if !s.ready() || user == "" {
		return CredOAuthToken{}, false
	}
	var tok CredOAuthToken
	ok := s.db.Get(secureAPITable, secureCredUserTokenKey(name, user), &tok)
	return tok, ok && tok.AccessToken != ""
}

// HasUserToken reports whether a user has connected (has a stored token) — for
// the Account page's connected/disconnected badge. Never returns the token.
func (s *SecureAPI) HasUserToken(name, user string) bool {
	_, ok := s.loadUserToken(name, user)
	return ok
}

// ClearUserToken disconnects a user from an OAuth credential.
func (s *SecureAPI) ClearUserToken(name, user string) {
	if s.ready() && name != "" && user != "" {
		s.mu.Lock()
		s.db.Unset(secureAPITable, secureCredUserTokenKey(name, user))
		s.mu.Unlock()
	}
}

// UserConnected reports whether a per_user credential is usable by the user —
// an OAuth token for authorization_code creds, else a stored key.
func (s *SecureAPI) UserConnected(c SecureCredential, user string) bool {
	if c.IsAuthCode() {
		return s.HasUserToken(c.Name, user)
	}
	return s.HasUserSecret(c.Name, user)
}

// --- the consent flow --------------------------------------------------------

// oauthPending links a consent redirect (by state) back to the user + the PKCE
// verifier that started it. Short-lived; cleaned on use or by TTL.
type oauthPending struct {
	cred     string
	user     string
	verifier string
	redirect string
	at       time.Time
}

var (
	oauthPendingMu sync.Mutex
	oauthPending_  = map[string]oauthPending{}
)

func randB64(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// OAuthStart begins a consent flow for (credential, user): mints state + a PKCE
// verifier, records the pending entry, and returns the provider authorize URL to
// redirect the user to. redirectURI must equal the callback the provider will
// hit (and that the admin registered with the provider).
func (s *SecureAPI) OAuthStart(c SecureCredential, user, redirectURI string) (string, error) {
	if !c.IsAuthCode() {
		return "", fmt.Errorf("credential %q is not an OAuth consent credential", c.Name)
	}
	if c.AuthorizeURL == "" || c.TokenURL == "" || c.ClientID == "" {
		return "", fmt.Errorf("credential %q is missing OAuth config (authorize_url / token_url / client_id)", c.Name)
	}
	state := randB64(24)
	verifier := randB64(48)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	oauthPendingMu.Lock()
	// Opportunistic TTL sweep (10 min) so abandoned flows don't accumulate.
	for k, p := range oauthPending_ {
		if time.Since(p.at) > 10*time.Minute {
			delete(oauthPending_, k)
		}
	}
	oauthPending_[state] = oauthPending{cred: c.Name, user: user, verifier: verifier, redirect: redirectURI, at: nowUTC()}
	oauthPendingMu.Unlock()

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", c.ClientID)
	q.Set("redirect_uri", redirectURI)
	if c.Scope != "" {
		q.Set("scope", c.Scope)
	}
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	sep := "?"
	if strings.Contains(c.AuthorizeURL, "?") {
		sep = "&"
	}
	return c.AuthorizeURL + sep + q.Encode(), nil
}

// OAuthCallback completes a consent flow: validates state, exchanges the code
// (with the PKCE verifier) for tokens, and stores them for the user. Returns the
// resolved (credential name, user) so the caller can redirect appropriately.
func (s *SecureAPI) OAuthCallback(ctx context.Context, state, code string) (credName, user string, err error) {
	oauthPendingMu.Lock()
	p, ok := oauthPending_[state]
	if ok {
		delete(oauthPending_, state)
	}
	oauthPendingMu.Unlock()
	if !ok {
		return "", "", fmt.Errorf("unknown or expired authorization state")
	}
	c, found := s.Load(p.cred)
	if !found {
		return "", "", fmt.Errorf("credential %q no longer exists", p.cred)
	}
	secret, _ := s.loadSecret(c.Name) // client_secret (deployment-level; may be empty for public clients)

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("code_verifier", p.verifier)
	form.Set("redirect_uri", p.redirect)
	form.Set("client_id", c.ClientID)
	if secret != "" {
		form.Set("client_secret", secret)
	}
	tok, err := credTokenRequest(ctx, c.TokenURL, form, CredOAuthToken{})
	if err != nil {
		return "", "", fmt.Errorf("token exchange failed: %w", err)
	}
	if err := s.SaveUserToken(c.Name, p.user, tok); err != nil {
		return "", "", err
	}
	return c.Name, p.user, nil
}

// userAccessToken returns a valid access token for (credential, user), refreshing
// it when expired (or near expiry). Used by the dispatch for authorization_code
// per_user credentials.
func (s *SecureAPI) userAccessToken(ctx context.Context, c SecureCredential, user string) (string, error) {
	tok, ok := s.loadUserToken(c.Name, user)
	if !ok {
		return "", fmt.Errorf("not connected")
	}
	// Fresh enough? (60s skew so a call doesn't race the expiry.)
	if tok.Expiry.IsZero() || time.Until(tok.Expiry) > 60*time.Second {
		return tok.AccessToken, nil
	}
	if tok.RefreshToken == "" {
		return tok.AccessToken, nil // no refresh available; use it and let the API 401 if stale
	}
	secret, _ := s.loadSecret(c.Name)
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", tok.RefreshToken)
	form.Set("client_id", c.ClientID)
	if secret != "" {
		form.Set("client_secret", secret)
	}
	refreshed, err := credTokenRequest(ctx, c.TokenURL, form, tok)
	if err != nil {
		return tok.AccessToken, nil // refresh failed; fall back to the (maybe-stale) token
	}
	_ = s.SaveUserToken(c.Name, user, refreshed)
	return refreshed.AccessToken, nil
}

// credTokenRequest POSTs an OAuth token request and parses the response. prev
// carries a refresh token forward when the provider omits it on refresh.
func credTokenRequest(ctx context.Context, endpoint string, form url.Values, prev CredOAuthToken) (CredOAuthToken, error) {
	var tok CredOAuthToken
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tok, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
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

func nowUTC() time.Time { return time.Now().UTC() }
