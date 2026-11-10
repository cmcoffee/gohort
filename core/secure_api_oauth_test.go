package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/cmcoffee/gohort/core/clientcert"
	"github.com/cmcoffee/snugforge/jwcrypt"
)

// A client_credentials credential that signs in with a certificate sends a
// signed client assertion in the secret's place: no Basic header, the client
// named in the body, the assertion verifying with the certificate's key for
// the token URL (or jwt_audience) with the kid it was given. Its form reads
// the certificate back; one that signs in with a secret has none.
func TestACertificateCredentialSignsInWithAnAssertion(t *testing.T) {
	secureAPITestStore(t)
	var mu sync.Mutex
	var asked url.Values
	var basic string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		mu.Lock()
		asked, basic = r.PostForm, r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
	}))
	defer srv.Close()
	prev := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = prev })

	cert, err := clientcert.Generate("sso test", 0)
	if err != nil {
		t.Fatal(err)
	}
	cred := SecureCredential{Name: "sso", Type: SecureCredOAuth2, Grant: OAuthGrantClientCredentials, ClientID: "client-1",
		TokenURL: srv.URL + "/token", BaseURL: "https://api.example", ClientAuth: oauthClientAuthCertificate, JWTKeyID: "key-7"}
	if err := Secure().Save(cred, cert.PEM()); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := Secure().mintOAuthGrantCtx(context.Background(), cred, cert.PEM()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got, auth := asked, basic
	mu.Unlock()
	if auth != "" || got.Get("client_secret") != "" || got.Get("client_id") != "client-1" ||
		got.Get("client_assertion_type") != clientcert.AssertionType || got.Get("grant_type") != "client_credentials" {
		t.Fatalf("token request: auth %q, form %v", auth, got)
	}
	tok := got.Get("client_assertion")
	claims, err := jwcrypt.VerifyJWT(tok, &cert.Key.PublicKey, jwcrypt.VerifyOptions{Audience: cred.TokenURL, Algorithms: []jwcrypt.JWTAlgorithm{jwcrypt.PS256}})
	if err != nil || claims.String("iss") != "client-1" || claims.String("sub") != "client-1" {
		t.Fatalf("the assertion: %v %v", claims, err)
	}
	if hdr, _ := jwcrypt.DecodeJWTHeader(tok); hdr.KeyID != "key-7" {
		t.Errorf("kid = %q, want the credential's jwt_key_id", hdr.KeyID)
	}
	// A provider that names another audience gets it.
	cred.JWTAudience = "https://login.example/realm"
	if a, err := clientAssertion(cred, cert.PEM()); err != nil {
		t.Fatal(err)
	} else if _, err := jwcrypt.VerifyJWT(a, &cert.Key.PublicKey, jwcrypt.VerifyOptions{Audience: cred.JWTAudience, Algorithms: []jwcrypt.JWTAlgorithm{jwcrypt.PS256}}); err != nil {
		t.Errorf("jwt_audience is not the assertion's aud: %v", err)
	}
	if back, ok := Secure().ClientCertificate("sso"); !ok || back.Thumbprint() != cert.Thumbprint() {
		t.Error("the credential's certificate does not read back")
	}
	// A secret that is not a certificate is refused with why, not sent.
	if _, err := clientAssertion(cred, "plain-secret"); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("a plain secret as a certificate: %v", err)
	}
	plain := SecureCredential{Name: "sso_plain", Type: SecureCredOAuth2, Grant: OAuthGrantClientCredentials, ClientID: "c",
		TokenURL: srv.URL + "/token", BaseURL: "https://api.example"}
	Secure().Save(plain, "client-secret")
	if _, ok := Secure().ClientCertificate("sso_plain"); ok {
		t.Error("a secret credential reads as having a certificate")
	}
	if _, _, _, err := Secure().mintOAuthGrantCtx(context.Background(), plain, "client-secret"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.HasPrefix(basic, "Basic ") || asked.Get("client_assertion") != "" {
		t.Errorf("a secret credential still signs in with Basic: auth %q, form %v", basic, asked)
	}
}
