package clientcert

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/jwcrypt"
)

func made(t *testing.T) Cert {
	t.Helper()
	c, err := Generate("test bot", 0)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A certificate made here reads back as the same key and certificate, and
// only the certificate part goes to the provider.
func TestAMadeCertificateReadsBack(t *testing.T) {
	c := made(t)
	if life := time.Until(c.Expires()); life < 364*24*time.Hour || life > 366*24*time.Hour {
		t.Errorf("a new certificate is valid for %s, want about a year", life)
	}
	back, err := Parse(c.PEM())
	if err != nil {
		t.Fatal(err)
	}
	if back.Thumbprint() != c.Thumbprint() || back.Key.N.Cmp(c.Key.N) != 0 {
		t.Error("the stored PEM did not read back as the same certificate and key")
	}
	if pub := c.CertificatePEM(); strings.Contains(pub, "PRIVATE KEY") || !strings.Contains(pub, "BEGIN CERTIFICATE") {
		t.Errorf("the public certificate is not just the certificate:\n%s", pub)
	}
	// Certificate first, key second, as an issuing authority's files often go.
	key := strings.TrimSuffix(c.PEM(), c.CertificatePEM())
	if _, err := Parse(c.CertificatePEM() + key); err != nil {
		t.Errorf("certificate then key: %v", err)
	}
}

// What cannot sign in is refused when it is given, with what to do about it.
func TestParseRefusesWhatCannotSignIn(t *testing.T) {
	a, b := made(t), made(t)
	keyA := strings.TrimSuffix(a.PEM(), a.CertificatePEM())
	for _, c := range []struct{ text, want string }{
		{"", "no certificate or private key"},
		{a.CertificatePEM(), "no private key"},
		{keyA, "no certificate"},
		{keyA + b.CertificatePEM(), "not for this private key"},
		// Built, not written out: a key-shaped block is never committed
		// (scrub_test.go), even a fake one.
		{a.CertificatePEM() + string(pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: []byte{0}})), "passphrase"},
	} {
		if _, err := Parse(c.text); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("want an error about %q, got %v", c.want, err)
		}
	}
}

// The assertion is what Microsoft documents: PS256, the certificate's
// SHA-256 thumbprint in x5t#S256, iss and sub the client, aud the token
// endpoint, a jti, and a lifetime of minutes. It verifies with the
// certificate's public key, which is the provider's whole check.
func TestTheAssertionIsWhatTheProviderChecks(t *testing.T) {
	c := made(t)
	const client = "11111111-2222-3333-4444-555555555555"
	const aud = "https://login.microsoftonline.com/tenant-guid/oauth2/v2.0/token"
	now := time.Now()
	tok, err := c.Assertion(client, aud, now)
	if err != nil {
		t.Fatal(err)
	}
	hdr, err := jwcrypt.DecodeJWTHeader(tok)
	if err != nil || hdr.Algorithm != jwcrypt.PS256 {
		t.Fatalf("header %+v (%v), want PS256", hdr, err)
	}
	sum := sha256.Sum256(c.Leaf.Raw)
	head, _ := base64.RawURLEncoding.DecodeString(strings.Split(tok, ".")[0])
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); !strings.Contains(string(head), `"x5t#S256":"`+want+`"`) || !strings.Contains(string(head), `"typ":"JWT"`) {
		t.Errorf("header does not name the certificate by its SHA-256 thumbprint: %s", head)
	}
	claims, err := jwcrypt.VerifyJWT(tok, &c.Key.PublicKey, jwcrypt.VerifyOptions{Audience: aud, Algorithms: []jwcrypt.JWTAlgorithm{jwcrypt.PS256}})
	if err != nil {
		t.Fatalf("the assertion does not verify with the certificate's key: %v", err)
	}
	if claims.String("iss") != client || claims.String("sub") != client || claims.String("jti") == "" {
		t.Errorf("claims = %v", claims)
	}
	exp, _ := claims.Time("exp")
	if d := exp.Sub(now); d <= 0 || d > 10*time.Minute {
		t.Errorf("assertion lives %s, want a few minutes", d)
	}
	again, _ := c.Assertion(client, aud, now)
	if again == tok {
		t.Error("two assertions came out the same: the jti must differ")
	}
	if _, err := c.Assertion(client, aud, c.Expires().Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("an expired certificate signed: %v", err)
	}
}
