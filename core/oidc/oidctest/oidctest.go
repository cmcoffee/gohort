// Package oidctest is an OpenID Connect provider in a test: discovery, a key
// set, an authorize step that stands in for the person at the provider, and
// a token endpoint that checks what a real one checks (the code once, the
// PKCE verifier, the redirect URI, a client credential). Nothing reaches a
// real provider.
package oidctest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/jwcrypt"
)

// User is the person who signs in at the provider.
type User struct {
	OID               string
	PreferredUsername string
	Name              string
	Email             string
	// Tenant is their directory; empty is the provider's own.
	Tenant string
}

// Provider is the fake. Change its fields between steps to make it behave
// as a wrong or hostile provider would.
type Provider struct {
	Server   *httptest.Server
	Issuer   string
	ClientID string
	Tenant   string
	// ClientSecret, when set, is the only secret the token endpoint takes; a
	// client assertion is taken when it is empty.
	ClientSecret string
	// Mutate changes an ID token's claims before it is signed.
	Mutate func(claims map[string]any)
	// SignWith, when set, signs ID tokens with a key the key set lacks.
	SignWith *rsa.PrivateKey
	// DiscoveryIssuer, when set, is the issuer the discovery document names
	// in place of its own.
	DiscoveryIssuer string

	key *rsa.PrivateKey

	mu    sync.Mutex
	codes map[string]grant
	// Forms are the token requests received.
	Forms []url.Values
}

type grant struct {
	user        User
	nonce       string
	challenge   string
	redirectURI string
}

const kid = "test-key"

var (
	keyOnce sync.Once
	sharedK *rsa.PrivateKey
	otherK  *rsa.PrivateKey
)

// keys are made once per test binary: RSA key generation is slow.
func keys(t testing.TB) (*rsa.PrivateKey, *rsa.PrivateKey) {
	keyOnce.Do(func() {
		var err error
		if sharedK, err = rsa.GenerateKey(rand.Reader, 2048); err == nil {
			otherK, err = rsa.GenerateKey(rand.Reader, 2048)
		}
		if err != nil {
			t.Fatal(err)
		}
	})
	return sharedK, otherK
}

// OtherKey is a key the provider's key set does not have.
func OtherKey(t testing.TB) *rsa.PrivateKey {
	_, o := keys(t)
	return o
}

// New starts a provider for one client registration in one directory. Its
// issuer is its own server's URL, so a test can point sign-in at it.
func New(t testing.TB, clientID, tenant string) *Provider {
	t.Helper()
	p := &Provider{ClientID: clientID, Tenant: tenant, codes: map[string]grant{}}
	p.key, _ = keys(t)
	mux := http.NewServeMux()
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Server.Close)
	p.Issuer = p.Server.URL + "/" + tenant + "/v2.0"
	mux.HandleFunc("/"+tenant+"/v2.0/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		issuer := p.Issuer
		if p.DiscoveryIssuer != "" {
			issuer = p.DiscoveryIssuer
		}
		json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 issuer,
			"authorization_endpoint": p.Server.URL + "/authorize",
			"token_endpoint":         p.Server.URL + "/token",
			"jwks_uri":               p.Server.URL + "/keys",
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"keys":[{"kty":"RSA","use":"sig","alg":"RS256","kid":%q,"n":%q,"e":%q}]}`,
			kid, b64(p.key.N.Bytes()), b64(big.NewInt(int64(p.key.E)).Bytes()))
	})
	mux.HandleFunc("/token", p.token)
	return p
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// Authorize stands in for the person signing in at the provider: it checks
// the authorize URL the way the provider would, and returns where the
// provider sends the browser back (the redirect URI with a code and the
// state).
func (p *Provider) Authorize(t testing.TB, authorizeURL string, u User) string {
	t.Helper()
	parsed, err := url.Parse(authorizeURL)
	if err != nil || !strings.HasPrefix(authorizeURL, p.Server.URL+"/authorize?") {
		t.Fatalf("not this provider's authorize URL: %s", authorizeURL)
	}
	q := parsed.Query()
	for k, want := range map[string]string{"response_type": "code", "client_id": p.ClientID, "code_challenge_method": "S256", "response_mode": "query"} {
		if q.Get(k) != want {
			t.Fatalf("authorize %s = %q, want %q", k, q.Get(k), want)
		}
	}
	for _, k := range []string{"redirect_uri", "state", "nonce", "code_challenge", "scope"} {
		if q.Get(k) == "" {
			t.Fatalf("authorize has no %s", k)
		}
	}
	if !strings.Contains(" "+q.Get("scope")+" ", " openid ") {
		t.Fatalf("authorize scope %q lacks openid", q.Get("scope"))
	}
	code := b64([]byte(fmt.Sprintf("code-%d-%s", time.Now().UnixNano(), u.OID)))
	p.mu.Lock()
	p.codes[code] = grant{user: u, nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirectURI: q.Get("redirect_uri")}
	p.mu.Unlock()
	back := url.Values{"code": {code}, "state": {q.Get("state")}}
	return q.Get("redirect_uri") + "?" + back.Encode()
}

// token is the token endpoint.
func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	p.mu.Lock()
	p.Forms = append(p.Forms, r.PostForm)
	g, ok := p.codes[r.PostForm.Get("code")]
	delete(p.codes, r.PostForm.Get("code"))
	p.mu.Unlock()
	refuse := func(code, desc string) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": desc + "\r\nTrace ID: 0000"})
	}
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	switch {
	case r.PostForm.Get("grant_type") != "authorization_code":
		refuse("unsupported_grant_type", "not a code grant")
		return
	case !ok:
		refuse("invalid_grant", "AADSTS54005: the code was already redeemed or never issued")
		return
	case b64(sum[:]) != g.challenge:
		refuse("invalid_grant", "AADSTS501481: the code verifier does not match the challenge")
		return
	case r.PostForm.Get("redirect_uri") != g.redirectURI:
		refuse("invalid_grant", "the redirect URI does not match")
		return
	case r.PostForm.Get("client_id") != p.ClientID:
		refuse("invalid_client", "unknown client")
		return
	}
	if p.ClientSecret != "" {
		if r.PostForm.Get("client_secret") != p.ClientSecret {
			refuse("invalid_client", "AADSTS7000215: invalid client secret")
			return
		}
	} else if r.PostForm.Get("client_assertion") == "" || r.PostForm.Get("client_assertion_type") == "" {
		refuse("invalid_client", "AADSTS7000218: a client assertion or secret is required")
		return
	}
	tenant := g.user.Tenant
	if tenant == "" {
		tenant = p.Tenant
	}
	now := time.Now()
	claims := map[string]any{
		"iss": p.Issuer, "aud": p.ClientID, "exp": now.Add(time.Hour).Unix(), "iat": now.Unix(), "nbf": now.Unix(),
		"nonce": g.nonce, "tid": tenant, "oid": g.user.OID, "sub": "pairwise-" + g.user.OID,
		"preferred_username": g.user.PreferredUsername, "name": g.user.Name,
	}
	if g.user.Email != "" {
		claims["email"] = g.user.Email
	}
	if p.Mutate != nil {
		p.Mutate(claims)
	}
	key := p.key
	if p.SignWith != nil {
		key = p.SignWith
	}
	tok, err := jwcrypt.SignRS256(key, claims, map[string]string{"kid": kid})
	if err != nil {
		refuse("server_error", err.Error())
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"id_token": tok, "access_token": "not-for-you", "token_type": "Bearer"})
}
