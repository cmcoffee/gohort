// Package clientcert is a certificate an app signs in with instead of a
// client secret: OAuth's private key JWT client authentication (RFC 7523,
// 2.2), which Microsoft Entra calls a certificate credential.
//
// The app keeps the private key and the identity provider keeps the public
// certificate. To sign in, the app sends a short-lived JWT signed with the
// key in place of a secret, and the provider checks the signature against
// the certificate it has on file. The key never leaves the app, which is why
// providers recommend it over a secret that travels with every request.
//
// A certificate is made here (Generate), so the key is never anywhere else,
// or brought from an issuing authority (Parse). Either way it is stored as
// one PEM text, key and certificate together (PEM), encrypted by the caller.
package clientcert

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/cmcoffee/snugforge/jwcrypt"
)

// AssertionType is the client_assertion_type a token request names when it
// signs in with a certificate.
const AssertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"

const (
	// keyBits is the size of a key made here: what identity providers
	// accept for a certificate credential, and what they ask for at least.
	keyBits = 2048
	// DefaultLifetime is how long a certificate made here is valid.
	DefaultLifetime = 365 * 24 * time.Hour
	// assertionLife is how long one signed assertion is good for. Microsoft
	// asks for "5-10 minutes after nbf at most"; one is sent the moment it is
	// made, so a short one costs nothing.
	assertionLife = 5 * time.Minute
)

// Cert is a private key and the certificate for it.
type Cert struct {
	Key  *rsa.PrivateKey
	Leaf *x509.Certificate
	// KeyID, when set, is sent as the assertion's kid, for a provider that
	// finds the key by an id it was registered under (a JWK set) rather than
	// by the certificate's thumbprint. Not stored.
	KeyID string
}

// Generate makes a new key and a self-signed certificate for it, named name
// and valid for life (DefaultLifetime when zero). Self-signed is what an
// identity provider expects here: it trusts the certificate because it was
// uploaded to the app's registration, not because anyone issued it.
func Generate(name string, life time.Duration) (Cert, error) {
	if life <= 0 {
		life = DefaultLifetime
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "gohort client"
	}
	key, err := rsa.GenerateKey(rand.Reader, keyBits)
	if err != nil {
		return Cert{}, fmt.Errorf("making a key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return Cert{}, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(life),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return Cert{}, fmt.Errorf("making the certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return Cert{}, err
	}
	return Cert{Key: key, Leaf: leaf}, nil
}

// Parse reads a key and its certificate from PEM text, in either order, as
// one file or two pasted together. It refuses a certificate that is not for
// the key: one that does not match would only fail later, at sign-in, with a
// provider's error that does not say why.
func Parse(text string) (Cert, error) {
	var c Cert
	var keyBlock *pem.Block
	rest := []byte(text)
	for {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		switch {
		case b.Type == "CERTIFICATE":
			if c.Leaf != nil {
				continue // the first certificate is the app's; the rest are its chain
			}
			leaf, err := x509.ParseCertificate(b.Bytes)
			if err != nil {
				return Cert{}, fmt.Errorf("reading the certificate: %w", err)
			}
			c.Leaf = leaf
		case strings.HasSuffix(b.Type, "PRIVATE KEY"):
			if b.Type == "ENCRYPTED PRIVATE KEY" || b.Headers["Proc-Type"] != "" {
				return Cert{}, fmt.Errorf("the private key is protected with a passphrase: export it without one")
			}
			keyBlock = b
		}
	}
	switch {
	case c.Leaf == nil && keyBlock == nil:
		return Cert{}, fmt.Errorf("no certificate or private key in it: paste both, in PEM (the text starting -----BEGIN)")
	case c.Leaf == nil:
		return Cert{}, fmt.Errorf("there is a private key but no certificate: paste the certificate too")
	case keyBlock == nil:
		return Cert{}, fmt.Errorf("there is a certificate but no private key: paste the key too")
	}
	key, err := rsaKey(keyBlock.Bytes)
	if err != nil {
		return Cert{}, err
	}
	pub, ok := c.Leaf.PublicKey.(*rsa.PublicKey)
	if !ok || pub.N.Cmp(key.PublicKey.N) != 0 || pub.E != key.PublicKey.E {
		return Cert{}, fmt.Errorf("the certificate is not for this private key")
	}
	c.Key = key
	return c, nil
}

// rsaKey reads an RSA private key in either form a key is exported in:
// PKCS #1 ("RSA PRIVATE KEY") or PKCS #8 ("PRIVATE KEY").
func rsaKey(der []byte) (*rsa.PrivateKey, error) {
	if k, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("reading the private key: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("the private key is not an RSA key, which is what is needed here")
	}
	return rk, nil
}

// PEM is the key and the certificate as one PEM text, the way Parse reads
// it back: what is stored.
func (c Cert) PEM() string {
	key := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(c.Key)})
	return string(key) + c.CertificatePEM()
}

// CertificatePEM is the public certificate alone: what is uploaded to the
// app's registration. It holds no secret.
func (c Cert) CertificatePEM() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Leaf.Raw}))
}

// Thumbprint is the certificate's SHA-256 thumbprint, base64url without
// padding: the x5t#S256 header that tells the provider which of the app's
// certificates signed.
func (c Cert) Thumbprint() string {
	sum := sha256.Sum256(c.Leaf.Raw)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ThumbprintSHA1 is the SHA-1 thumbprint in hex, the way Microsoft's portal
// lists an uploaded certificate, so a person can tell which one is which.
func (c Cert) ThumbprintSHA1() string {
	sum := sha1.Sum(c.Leaf.Raw)
	return strings.ToUpper(fmt.Sprintf("%x", sum[:]))
}

// Expires is when the certificate stops being valid.
func (c Cert) Expires() time.Time { return c.Leaf.NotAfter }

// Assertion is a client assertion for clientID at the token endpoint
// audience, signed with the key: PS256, the certificate named by its
// SHA-256 thumbprint, and the claims RFC 7523 and Microsoft ask for (iss
// and sub the client, aud the token endpoint, a unique jti, valid for five
// minutes from now).
func (c Cert) Assertion(clientID, audience string, now time.Time) (string, error) {
	if c.Key == nil || c.Leaf == nil {
		return "", fmt.Errorf("no certificate to sign with")
	}
	if now.After(c.Leaf.NotAfter) {
		return "", fmt.Errorf("the certificate expired on %s: make or upload a new one", c.Leaf.NotAfter.Format("Jan 2, 2006"))
	}
	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return "", err
	}
	claims := map[string]any{
		"iss": clientID,
		"sub": clientID,
		"aud": audience,
		"jti": fmt.Sprintf("%x", jti),
		"nbf": now.Unix(),
		"iat": now.Unix(),
		"exp": now.Add(assertionLife).Unix(),
	}
	header := map[string]string{"typ": "JWT", "x5t#S256": c.Thumbprint()}
	if c.KeyID != "" {
		header["kid"] = c.KeyID
	}
	return jwcrypt.SignPS256(c.Key, claims, header)
}
