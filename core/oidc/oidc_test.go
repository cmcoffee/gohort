package oidc_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cmcoffee/gohort/core/clientcert"
	"github.com/cmcoffee/gohort/core/oidc"
	"github.com/cmcoffee/gohort/core/oidc/oidctest"
	"github.com/cmcoffee/snugforge/kvlite"
)

const (
	tenant   = "11111111-2222-3333-4444-555555555555"
	clientID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	redirect = "https://gohort.example.test/login/sso/callback"
)

var person = oidctest.User{OID: "oid-1", PreferredUsername: "pat@example.test", Name: "Pat"}

func provider(t *testing.T) (*oidctest.Provider, oidc.Provider) {
	t.Helper()
	fake := oidctest.New(t, clientID, tenant)
	fake.ClientSecret = "s3cret"
	return fake, oidc.Provider{Issuer: fake.Issuer, ClientID: clientID, RedirectURI: redirect, Tenant: tenant, ClientSecret: "s3cret"}
}

// signIn runs Start, the person at the provider, and Finish.
func signIn(t *testing.T, fake *oidctest.Provider, p oidc.Provider, u oidctest.User) (oidc.Identity, error) {
	t.Helper()
	req, err := p.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	back, _ := url.Parse(fake.Authorize(t, req.URL, u))
	if back.Query().Get("state") != req.State {
		t.Fatal("the provider did not send the state back")
	}
	return p.Finish(context.Background(), back.Query().Get("code"), req.Verifier, req.Nonce)
}

func TestASignInIsThePersonByTheirPermanentID(t *testing.T) {
	fake, p := provider(t)
	id, err := signIn(t, fake, p, person)
	if err != nil {
		t.Fatal(err)
	}
	if id.Subject != "oid-1" || id.Issuer != fake.Issuer || id.Key() != fake.Issuer+"|oid-1" || id.Shown() != "pat@example.test" {
		t.Errorf("identity %+v", id)
	}
	form := fake.Forms[len(fake.Forms)-1]
	if form.Get("code_verifier") == "" || form.Get("redirect_uri") != redirect {
		t.Errorf("token request %v", form)
	}
}

func TestATokenThatIsNotForThisSignInIsRefused(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"another directory": func(c map[string]any) { c["tid"] = "99999999-2222-3333-4444-555555555555" },
		"another issuer":    func(c map[string]any) { c["iss"] = "https://login.example.test/other/v2.0" },
		"another app":       func(c map[string]any) { c["aud"] = "ffffffff-bbbb-cccc-dddd-eeeeeeeeeeee" },
		"another sign-in":   func(c map[string]any) { c["nonce"] = "someone-elses" },
		"no nonce":          func(c map[string]any) { delete(c, "nonce") },
		"expired":           func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() },
		"not yet":           func(c map[string]any) { c["nbf"] = time.Now().Add(time.Hour).Unix() },
		"nobody":            func(c map[string]any) { delete(c, "oid"); delete(c, "sub") },
	} {
		t.Run(name, func(t *testing.T) {
			fake, p := provider(t)
			fake.Mutate = mutate
			if id, err := signIn(t, fake, p, person); err == nil {
				t.Errorf("signed in as %+v", id)
			}
		})
	}
	t.Run("signed with a key not in the set", func(t *testing.T) {
		fake, p := provider(t)
		fake.SignWith = oidctest.OtherKey(t)
		if _, err := signIn(t, fake, p, person); err == nil {
			t.Error("signed in")
		}
	})
	t.Run("a person from another directory", func(t *testing.T) {
		fake, p := provider(t)
		u := person
		u.Tenant = "99999999-2222-3333-4444-555555555555"
		if _, err := signIn(t, fake, p, u); err == nil || !strings.Contains(err.Error(), "directory") {
			t.Errorf("signed in: %v", err)
		}
	})
}

func TestTheCodeIsUsedOnceAndOnlyWithItsVerifier(t *testing.T) {
	fake, p := provider(t)
	req, _ := p.Start(context.Background())
	back, _ := url.Parse(fake.Authorize(t, req.URL, person))
	code := back.Query().Get("code")
	if _, err := p.Finish(context.Background(), code, "a-different-verifier", req.Nonce); err == nil {
		t.Error("a code was redeemed without its verifier")
	}
	req, _ = p.Start(context.Background())
	back, _ = url.Parse(fake.Authorize(t, req.URL, person))
	code = back.Query().Get("code")
	if _, err := p.Finish(context.Background(), code, req.Verifier, req.Nonce); err != nil {
		t.Fatal(err)
	}
	_, err := p.Finish(context.Background(), code, req.Verifier, req.Nonce)
	if err == nil {
		t.Fatal("a code was redeemed twice")
	}
	if strings.Contains(err.Error(), "Trace ID") {
		t.Errorf("the provider's trace lines reach the person: %v", err)
	}
	if _, err := p.Finish(context.Background(), "", req.Verifier, req.Nonce); err == nil {
		t.Error("no code was accepted")
	}
}

func TestTheClientProvesItselfWithACertificate(t *testing.T) {
	fake, p := provider(t)
	fake.ClientSecret = ""
	cert, err := clientcert.Generate("gohort sign-in", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	p.ClientSecret, p.Certificate = "", &cert
	if _, err := signIn(t, fake, p, person); err != nil {
		t.Fatal(err)
	}
	form := fake.Forms[len(fake.Forms)-1]
	if form.Get("client_assertion_type") != clientcert.AssertionType || form.Get("client_assertion") == "" || form.Get("client_secret") != "" {
		t.Errorf("token request %v", form)
	}
	p.Certificate = nil
	if _, err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := signIn(t, fake, p, person); err == nil || !strings.Contains(err.Error(), "no client credential") {
		t.Errorf("a sign-in with no credential: %v", err)
	}
}

// A discovery document that names another issuer is not believed: its keys
// and tokens would be that issuer's.
func TestADiscoveryDocumentForAnotherIssuerIsRefused(t *testing.T) {
	fake, p := provider(t)
	fake.DiscoveryIssuer = "https://login.example.test/someone-else/v2.0"
	if _, err := p.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "names issuer") {
		t.Errorf("started against a mismatched discovery document: %v", err)
	}
}

func TestAPlaintextProviderIsRefused(t *testing.T) {
	p := oidc.Provider{Issuer: "http://login.example.test/x/v2.0", ClientID: clientID, RedirectURI: redirect}
	if _, err := p.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "https") {
		t.Errorf("plaintext issuer: %v", err)
	}
	if oidc.EntraIssuer(" "+tenant+" ") != "https://login.microsoftonline.com/"+tenant+"/v2.0" {
		t.Error(oidc.EntraIssuer(tenant))
	}
}

func db() *store { return &store{kv: kvlite.MemStore()} }

// store adapts kvlite to oidc.Store the way core.Database does.
type store struct{ kv kvlite.Store }

func (s *store) Get(t, k string, out interface{}) bool { ok, _ := s.kv.Get(t, k, out); return ok }
func (s *store) Set(t, k string, v interface{})        { s.kv.Set(t, k, v) }
func (s *store) CryptSet(t, k string, v interface{})   { s.kv.CryptSet(t, k, v) }
func (s *store) Unset(t, k string)                     { s.kv.Unset(t, k) }
func (s *store) Keys(t string) []string                { k, _ := s.kv.Keys(t); return k }

func TestSettingsRefuseWhatCannotSignAnyoneIn(t *testing.T) {
	d := db()
	s := oidc.LoadSettings(d)
	if s.Enabled || s.NewPeople != oidc.NewPeopleApproval || s.Credential != oidc.CredentialCertificate || s.Label != oidc.DefaultLabel {
		t.Fatalf("defaults %+v", s)
	}
	for _, bad := range []string{"common", "organizations", "consumers"} {
		if err := oidc.SaveSettings(d, oidc.Settings{Tenant: bad}); err == nil || !strings.Contains(err.Error(), "anyone's Microsoft account") {
			t.Errorf("tenant %q: %v", bad, err)
		}
	}
	if err := oidc.SaveSettings(d, oidc.Settings{Tenant: "contoso.example.test"}); err == nil {
		t.Error("a domain name saved as the tenant")
	}
	if err := oidc.SaveSettings(d, oidc.Settings{Tenant: tenant, ClientID: "my-app"}); err == nil {
		t.Error("a client ID that is not a GUID saved")
	}
	if err := oidc.SaveSettings(d, oidc.Settings{Enabled: true, Tenant: tenant, ClientID: clientID}); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("turned on with no certificate: %v", err)
	}
	cert, _ := clientcert.Generate("gohort sign-in", time.Hour)
	oidc.SaveCertificate(d, cert)
	if err := oidc.SaveSettings(d, oidc.Settings{Enabled: true, Tenant: tenant, ClientID: clientID}); err != nil {
		t.Fatal(err)
	}
	if got, ok := oidc.LoadCertificate(d); !ok || got.Thumbprint() != cert.Thumbprint() {
		t.Error("the certificate did not come back")
	}
	if err := oidc.SaveSettings(d, oidc.Settings{Enabled: true, Tenant: tenant, ClientID: clientID, Credential: oidc.CredentialSecret}); err == nil {
		t.Error("turned on with a secret that was never set")
	}
	oidc.SaveSecret(d, " shh ")
	if err := oidc.SaveSettings(d, oidc.Settings{Enabled: true, Tenant: tenant, ClientID: clientID, Credential: oidc.CredentialSecret}); err != nil || oidc.LoadSecret(d) != "shh" {
		t.Errorf("a secret: %v %q", err, oidc.LoadSecret(d))
	}
	p, err := oidc.LoadSettings(d).Provider(d, "https://issuer.example.test", redirect)
	if err != nil || p.ClientSecret != "shh" || p.Certificate != nil || p.Tenant != tenant {
		t.Errorf("provider %+v %v", p, err)
	}
}

func TestLinksBelongToOneAccount(t *testing.T) {
	d := db()
	id := oidc.Identity{Issuer: "https://issuer.example.test", Subject: "oid-1", PreferredUsername: "pat@example.test"}
	if _, ok := oidc.LinkedAccount(d, id); ok {
		t.Fatal("linked before linking")
	}
	oidc.SetLink(d, id, "pat", time.Now())
	if l, ok := oidc.LinkedAccount(d, id); !ok || l.Username != "pat" || l.ShownAs != "pat@example.test" {
		t.Fatalf("link %+v", l)
	}
	links := oidc.LinksFor(d, "pat")
	if len(links) != 1 || oidc.LinksFor(d, "sam") != nil {
		t.Fatalf("links %+v", links)
	}
	if oidc.Unlink(d, "sam", links[0].Key) {
		t.Error("another account removed pat's link")
	}
	if !oidc.Unlink(d, "pat", links[0].Key) {
		t.Error("pat could not remove their own link")
	}
	oidc.SetLink(d, id, "pat", time.Now())
	if oidc.UnlinkAll(d, "pat") != 1 || len(oidc.LinksFor(d, "pat")) != 0 {
		t.Error("deleting the account left its link")
	}
}
