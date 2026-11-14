// Package oidc signs a person in with an OpenID Connect provider: the
// authorization code flow with PKCE, the code exchanged for an ID token, and
// the ID token checked (docs/design/oidc-signin.md).
//
// The first provider is a single Microsoft Entra tenant; the code is generic
// OIDC, and Entra is a configuration of it (EntraIssuer, and the tenant ID
// checked in the token's tid claim).
//
// It speaks no core type: core's sign-in handlers use it, and the admin page
// keeps its settings with it, and it costs core nothing against its export
// ceiling. Only the ID token is used: the access and refresh tokens are
// dropped, so nothing here can act as the person at the provider.
package oidc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/core/clientcert"
	"github.com/cmcoffee/gohort/core/jwks"
)

// Provider is one OIDC client registration: who the provider is, and how
// this program identifies itself to it.
type Provider struct {
	// Issuer is the provider's issuer URL; its discovery document is at
	// Issuer + "/.well-known/openid-configuration", and the ID token's iss
	// must equal it.
	Issuer string
	// ClientID is this program's client (application) ID there.
	ClientID string
	// RedirectURI is where the provider sends the browser back; it has to be
	// registered with the provider exactly.
	RedirectURI string
	// Tenant, when set, must equal the ID token's tid claim (Entra).
	Tenant string
	// The client credential, one of: a certificate (a private key JWT) or a
	// client secret.
	Certificate  *clientcert.Cert
	ClientSecret string
	// Scopes; empty asks for openid, profile and email.
	Scopes []string
	// Client makes the requests; nil uses one with a 20 second timeout.
	Client *http.Client
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// EntraIssuer is a single Entra tenant's issuer.
func EntraIssuer(tenant string) string {
	return "https://login.microsoftonline.com/" + strings.TrimSpace(tenant) + "/v2.0"
}

// leeway absorbs clock skew between the provider and this machine.
const leeway = 2 * time.Minute

// metadataTTL is how long a discovery document is used before it is read
// again.
const metadataTTL = 12 * time.Hour

// maxBody caps a response from the provider.
const maxBody = 1 << 20

// metadata is what sign-in needs from a discovery document.
type metadata struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

// issuerState is one issuer's discovery document and the verifier that
// caches its signing keys. Kept per issuer across sign-ins, so the keys are
// not fetched again for each person.
type issuerState struct {
	meta     metadata
	fetched  time.Time
	verifier *jwks.Verifier
}

var (
	issuersMu sync.Mutex
	issuers   = map[string]*issuerState{}
)

func (p Provider) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (p Provider) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// state reads the issuer's discovery document, or the copy already read.
func (p Provider) state(ctx context.Context) (*issuerState, error) {
	issuer := strings.TrimSuffix(strings.TrimSpace(p.Issuer), "/")
	if err := checkURL(issuer); err != nil {
		return nil, fmt.Errorf("the provider's issuer %v", err)
	}
	issuersMu.Lock()
	defer issuersMu.Unlock()
	if st, ok := issuers[issuer]; ok && time.Since(st.fetched) < metadataTTL {
		return st, nil
	}
	body, err := p.get(ctx, issuer+"/.well-known/openid-configuration")
	if err != nil {
		return nil, fmt.Errorf("the provider's discovery document: %w", err)
	}
	var m metadata
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("the provider's discovery document is not JSON: %v", err)
	}
	if strings.TrimSuffix(m.Issuer, "/") != issuer {
		return nil, fmt.Errorf("the provider's discovery document names issuer %q, not %q", m.Issuer, issuer)
	}
	for name, u := range map[string]string{"authorization_endpoint": m.AuthorizationEndpoint, "token_endpoint": m.TokenEndpoint, "jwks_uri": m.JWKSURI} {
		if err := checkURL(u); err != nil {
			return nil, fmt.Errorf("the provider's %s %v", name, err)
		}
	}
	st := &issuerState{meta: m, fetched: time.Now(), verifier: &jwks.Verifier{
		JWKSURL: m.JWKSURI, Issuer: m.Issuer, Leeway: leeway, Name: issuer,
	}}
	issuers[issuer] = st
	return st, nil
}

func (p Provider) get(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := p.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s answered HTTP %d", rawURL, resp.StatusCode)
	}
	return body, nil
}

// Request is a sign-in started: the URL to send the browser to, and what has
// to be kept until it comes back.
type Request struct {
	URL      string
	State    string
	Nonce    string
	Verifier string // the PKCE code verifier
}

// Start begins a sign-in: a fresh state, nonce and PKCE verifier, and the
// provider's authorize URL carrying them.
func (p Provider) Start(ctx context.Context) (Request, error) {
	if strings.TrimSpace(p.ClientID) == "" || strings.TrimSpace(p.RedirectURI) == "" {
		return Request{}, errors.New("sign-in is missing its client ID or redirect URI")
	}
	st, err := p.state(ctx)
	if err != nil {
		return Request{}, err
	}
	req := Request{State: random(24), Nonce: random(24), Verifier: random(48)}
	sum := sha256.Sum256([]byte(req.Verifier))
	scopes := p.Scopes
	if len(scopes) == 0 {
		scopes = []string{"openid", "profile", "email"}
	}
	q := url.Values{}
	q.Set("response_type", "code")
	// query, not form_post: the browser comes back with a top-level GET, which
	// carries the SameSite=Lax cookie that ties the sign-in to the browser
	// that started it. A cross-site POST would not carry it.
	q.Set("response_mode", "query")
	q.Set("client_id", p.ClientID)
	q.Set("redirect_uri", p.RedirectURI)
	q.Set("scope", strings.Join(scopes, " "))
	q.Set("state", req.State)
	q.Set("nonce", req.Nonce)
	q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(sum[:]))
	q.Set("code_challenge_method", "S256")
	sep := "?"
	if strings.Contains(st.meta.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	req.URL = st.meta.AuthorizationEndpoint + sep + q.Encode()
	return req, nil
}

// Identity is who the provider says signed in.
type Identity struct {
	// Issuer and Subject together are the person: Subject is the provider's
	// permanent ID for them (oid on Entra, sub elsewhere). Never the email,
	// which on Entra a person can edit.
	Issuer  string
	Subject string
	// For display, and for naming a new account; never for matching.
	PreferredUsername string
	Name              string
	Email             string
	Tenant            string
}

// Key is the identity as one string, the key its link is stored under.
func (id Identity) Key() string { return id.Issuer + "|" + id.Subject }

// Shown is how the identity reads to a person.
func (id Identity) Shown() string {
	for _, s := range []string{id.PreferredUsername, id.Email, id.Name} {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return id.Subject
}

// Finish completes a sign-in: it exchanges the code (with the PKCE verifier)
// at the token endpoint and checks the ID token: its signature, issuer,
// audience, expiry, the nonce Start made, and the tenant.
func (p Provider) Finish(ctx context.Context, code, verifier, nonce string) (Identity, error) {
	if strings.TrimSpace(code) == "" {
		return Identity{}, errors.New("the provider sent no code back")
	}
	st, err := p.state(ctx)
	if err != nil {
		return Identity{}, err
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", p.RedirectURI)
	form.Set("client_id", p.ClientID)
	form.Set("code_verifier", verifier)
	switch {
	case p.Certificate != nil:
		assertion, err := p.Certificate.Assertion(p.ClientID, st.meta.TokenEndpoint, p.now())
		if err != nil {
			return Identity{}, err
		}
		form.Set("client_assertion_type", clientcert.AssertionType)
		form.Set("client_assertion", assertion)
	case p.ClientSecret != "":
		form.Set("client_secret", p.ClientSecret)
	default:
		return Identity{}, errors.New("sign-in has no client credential: make a certificate or set a client secret")
	}
	idToken, err := p.exchange(ctx, st.meta.TokenEndpoint, form)
	if err != nil {
		return Identity{}, err
	}
	claims, err := st.verifier.Verify(ctx, idToken, p.ClientID)
	if err != nil {
		return Identity{}, fmt.Errorf("the provider's ID token did not check out: %v", err)
	}
	if got := claims.String("nonce"); got == "" || got != nonce {
		return Identity{}, errors.New("the ID token is not for this sign-in (its nonce does not match)")
	}
	id := Identity{
		Issuer:            claims.String("iss"),
		Subject:           claims.String("oid"),
		PreferredUsername: claims.String("preferred_username"),
		Name:              claims.String("name"),
		Email:             claims.String("email"),
		Tenant:            claims.String("tid"),
	}
	if p.Tenant != "" && !strings.EqualFold(id.Tenant, p.Tenant) {
		return Identity{}, fmt.Errorf("the account is from directory %q, not this deployment's", id.Tenant)
	}
	if id.Subject == "" {
		id.Subject = claims.String("sub")
	}
	if id.Subject == "" {
		return Identity{}, errors.New("the ID token names nobody (no oid or sub)")
	}
	return id, nil
}

// exchange posts the token request and returns the ID token.
func (p Provider) exchange(ctx context.Context, endpoint string, form url.Values) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := p.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("the provider's token endpoint did not answer: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return "", err
	}
	var out struct {
		IDToken     string `json:"id_token"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &out)
	if resp.StatusCode != http.StatusOK || out.Error != "" {
		msg := out.Error
		if out.Description != "" {
			msg += ": " + firstLine(out.Description)
		}
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return "", fmt.Errorf("the provider refused the sign-in code (%s)", msg)
	}
	if out.IDToken == "" {
		return "", errors.New("the provider sent no ID token (is the openid scope allowed?)")
	}
	return out.IDToken, nil
}

// firstLine is the first line of a provider's error description: Entra's run
// on with a trace ID and a timestamp.
func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func random(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// checkURL insists on https, with loopback exempt so a test can serve a
// provider. A provider's endpoints over plaintext could be swapped in
// transit, and swapped keys sign anyone in.
func checkURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return fmt.Errorf("is not a URL: %q", raw)
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" {
		h := u.Hostname()
		if ip := net.ParseIP(h); h == "localhost" || (ip != nil && ip.IsLoopback()) {
			return nil
		}
	}
	return fmt.Errorf("must be https: %q", raw)
}
