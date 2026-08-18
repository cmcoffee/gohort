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
	tok, ok := s.loadUserTokenRaw(name, user)
	return tok, ok && tok.AccessToken != ""
}

// loadUserTokenRaw is loadUserToken without the "has an access token" gate.
//
// Needed because a token can legitimately hold a refresh token and NO access
// token: that is what a 401 leaves behind (see InvalidateUserAccessToken), and
// it means "we can mint a new one", not "disconnected". The gated form is kept
// for callers that specifically want a usable access token right now.
func (s *SecureAPI) loadUserTokenRaw(name, user string) (CredOAuthToken, bool) {
	if !s.ready() || user == "" {
		return CredOAuthToken{}, false
	}
	var tok CredOAuthToken
	ok := s.db.Get(secureAPITable, secureCredUserTokenKey(name, user), &tok)
	return tok, ok
}

// HasUserToken reports whether a user has connected (has a stored token) — for
// the Account page's connected/disconnected badge. Never returns the token.
//
// A refresh token alone counts as connected. Between a 401 and the next call
// there is no access token stored, and reporting that as disconnected would put
// a "Connect" button in front of someone whose account is fine and about to
// heal itself.
func (s *SecureAPI) HasUserToken(name, user string) bool {
	tok, ok := s.loadUserTokenRaw(name, user)
	return ok && (tok.AccessToken != "" || tok.RefreshToken != "")
}

// InvalidateUserAccessToken drops a user's ACCESS token for a credential while
// keeping the refresh token, so the next call mints a fresh one.
//
// Called when the provider answers 401: the server has told us the token is no
// longer good, which is more authoritative than any expiry we stored — and is
// the only signal available at all when the provider never sent one.
//
// With no refresh token there is nothing to recover with, so the record goes
// entirely and the user is asked to reconnect, which is true in that case.
func (s *SecureAPI) InvalidateUserAccessToken(name, user string) {
	tok, ok := s.loadUserTokenRaw(name, user)
	if !ok {
		return
	}
	if tok.RefreshToken == "" {
		s.ClearUserToken(name, user)
		Log("[secureapi] %q rejected %q's token and no refresh token is stored — reconnect required", name, user)
		return
	}
	tok.AccessToken = ""
	tok.Expiry = time.Time{}
	_ = s.SaveUserToken(name, user, tok)
	Log("[secureapi] %q rejected %q's access token — cleared; the next call will refresh it", name, user)
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
	// RAW: a token holding only a refresh token is the state a 401 leaves, and
	// it is exactly the case this function exists to resolve. The gated load
	// would call it "not connected" and send the user off to reconnect an
	// account that is one refresh away from working.
	tok, ok := s.loadUserTokenRaw(c.Name, user)
	if !ok || (tok.AccessToken == "" && tok.RefreshToken == "") {
		return "", fmt.Errorf("not connected")
	}
	// Fresh enough? (60s skew so a call doesn't race the expiry.)
	//
	// A zero Expiry means the provider told us nothing, NOT that the token is
	// good forever — so it only passes here while we still hold one. Once a 401
	// has cleared it, this falls through to the refresh below instead of
	// handing back an empty bearer.
	if tok.AccessToken != "" && (tok.Expiry.IsZero() || time.Until(tok.Expiry) > 60*time.Second) {
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
		// The refresh token itself is finished — revoked, expired, or already
		// rotated away. Retrying gets the same answer forever, so clear the
		// record and say so plainly rather than handing back a token that
		// cannot work and letting it fail as something else.
		if oauthGrantRejected(err) {
			s.ClearUserToken(c.Name, user)
			Log("[secureapi] %q refused %q's refresh token (%v) — cleared; reconnect required", c.Name, user, err)
			return "", fmt.Errorf("your %q connection has expired and can't be renewed automatically — reconnect it on your Account page (Connected accounts)", c.Name)
		}
		// Anything else (a blip, a 500, a timeout) leaves a good refresh token.
		// Ride the current access token out and try again on the next call.
		if tok.AccessToken != "" {
			return tok.AccessToken, nil
		}
		return "", fmt.Errorf("could not renew your %q access token: %w", c.Name, err)
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
