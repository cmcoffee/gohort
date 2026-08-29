// JWKS verification: resolving an OIDC issuer's signing keys so an inbound
// bearer token can be checked.
//
// The cryptography lives in snugforge/jwcrypt, which is deliberately offline.
// What lives here is the policy a crypto library has no business deciding:
// which HTTP client, how long a fetched key set stays trusted, how hard an
// unrecognized key id may push us to re-fetch, and what to do when the issuer
// is briefly unreachable.
//
// The shape this exists for is a PUSH endpoint. A service POSTs to a public
// route carrying a JWT signed by a key we have never seen, and we have to
// decide in-line whether to trust it -- there is no credential to look up and
// no prior handshake, only the token and the issuer's published keys.
//
// A subpackage rather than another file in the core hub: it speaks no core type
// and calls no core function, so it costs the hub nothing to keep it out --
// which is the first answer TestCoreStaysUnderItsCeiling asks for.
package jwks

import (
	"context"
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

	"github.com/cmcoffee/snugforge/jwcrypt"
	"github.com/cmcoffee/snugforge/nfo"
)

// Aliases so a caller verifying a token does not have to import jwcrypt too,
// matching how core exposes the rest of the snugforge surface.
type (
	Claims = jwcrypt.Claims
	Key    = jwcrypt.JWK
)

const (
	// defaultTTL is how long a fetched key set is used before a refresh.
	defaultTTL = 12 * time.Hour

	// missCooldown bounds how often an unrecognized key id may pull a
	// re-fetch. A rotation is the honest reason for an unknown kid; a flood of
	// forged tokens is the dishonest one, and without a cooldown the second
	// turns this verifier into a request amplifier aimed at the issuer.
	missCooldown = 5 * time.Minute

	// staleGrace is how long an already-fetched key set stays usable once
	// refreshes start failing. Signing keys are public and a signature check is
	// not weakened by their age, so dropping every inbound message because the
	// issuer's endpoint blipped costs more than it protects. Past this window
	// the set is treated as gone and verification fails.
	staleGrace = 7 * 24 * time.Hour

	// maxBody caps a metadata or key-set response.
	maxBody = 1 << 20
)

// httpClient is the client for discovery and key-set fetches. Each call is
// additionally bounded by its context.
var httpClient = &http.Client{Timeout: 20 * time.Second}

// Verifier verifies tokens from one issuer, caching its published keys.
//
// Safe for concurrent use, and deliberately single-flight: a burst of inbound
// requests arriving on a cold or expired cache coalesces behind one fetch
// rather than each opening its own.
type Verifier struct {
	// MetadataURL is the issuer's OIDC discovery document, from which jwks_uri
	// is read. Either this or JWKSURL is required.
	MetadataURL string
	// JWKSURL is the key set itself, for an issuer that publishes no discovery
	// document. Takes precedence over MetadataURL.
	JWKSURL string
	// Issuer, when set, must equal each token's iss claim.
	Issuer string
	// Algorithms is the accepted algorithm allow-list. Empty means jwcrypt's
	// default of RS256 and RS512.
	Algorithms []jwcrypt.JWTAlgorithm
	// Leeway absorbs clock skew when checking exp and nbf.
	Leeway time.Duration
	// TTL overrides how long a fetched key set is trusted.
	TTL time.Duration
	// Name labels this issuer in log lines. Falls back to Issuer.
	Name string

	mu       sync.Mutex
	set      *jwcrypt.JWKSet
	keysURL  string // resolved once; an issuer's jwks_uri does not move
	fetched  time.Time
	lastMiss time.Time
}

// Verify checks a token's signature against the issuer's current keys and its
// claims against this verifier's policy, returning the decoded claims.
//
// audience is per-call rather than per-verifier because one issuer signs for
// many audiences: Bot Framework mints tokens for every bot in the world, and
// what makes one OURS is that aud equals our app id. A caller that let this be
// empty would accept another tenant's traffic.
func (v *Verifier) Verify(ctx context.Context, token, audience string) (Claims, error) {
	set, err := v.keySet(ctx, false)
	if err != nil {
		return nil, err
	}
	opts := jwcrypt.VerifyOptions{
		Issuer:     v.Issuer,
		Audience:   audience,
		Algorithms: v.Algorithms,
		Leeway:     v.Leeway,
	}

	claims, err := set.VerifyJWT(token, opts)
	if err == nil {
		return claims, nil
	}

	// An unknown key id is the ONE failure a re-fetch can fix, because it is
	// what a key rotation looks like from this side. Every other error means the
	// token itself is wrong, and fetching again would not change that -- which
	// is exactly why jwcrypt reports this case as its own type.
	var unknown *jwcrypt.UnknownKeyIDError
	if !errors.As(err, &unknown) {
		return nil, err
	}
	refreshed, ferr := v.keySet(ctx, true)
	if ferr != nil {
		return nil, fmt.Errorf("%w (refresh after unknown key id failed: %v)", err, ferr)
	}
	if refreshed == set {
		return nil, err // the cooldown declined the re-fetch; report the original
	}
	return refreshed.VerifyJWT(token, opts)
}

// KeyByID returns a key from the issuer's current set, for a caller that needs
// key METADATA rather than a verdict -- Bot Framework publishes a per-key
// "endorsements" list naming the channels a key may sign for, which a caller
// reads through JWK.Extra.
func (v *Verifier) KeyByID(ctx context.Context, kid string) (*Key, bool) {
	set, err := v.keySet(ctx, false)
	if err != nil {
		return nil, false
	}
	return set.KeyByID(kid)
}

// keySet returns the cached key set, fetching when it is missing or stale.
// force asks for a refresh ahead of the TTL, subject to the miss cooldown; a
// declined refresh returns the existing set unchanged, which the caller detects
// by identity.
//
// The lock is held across the fetch on purpose: that is what makes concurrent
// callers wait for one request instead of starting several.
func (v *Verifier) keySet(ctx context.Context, force bool) (*jwcrypt.JWKSet, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	ttl := v.TTL
	if ttl <= 0 {
		ttl = defaultTTL
	}
	now := time.Now()

	if v.set != nil && !force && now.Sub(v.fetched) < ttl {
		return v.set, nil
	}
	if force && v.set != nil {
		if now.Sub(v.lastMiss) < missCooldown {
			return v.set, nil
		}
		v.lastMiss = now
	}

	set, err := v.fetch(ctx)
	if err != nil {
		if v.set != nil && now.Sub(v.fetched) < staleGrace {
			nfo.Warn("[jwks] %s refresh failed, continuing with keys fetched %s ago: %v",
				v.label(), now.Sub(v.fetched).Round(time.Minute), err)
			return v.set, nil
		}
		return nil, fmt.Errorf("%s key set unavailable: %w", v.label(), err)
	}
	v.set, v.fetched = set, now
	nfo.Debug("[jwks] %s key set refreshed (%d key(s))", v.label(), len(set.Keys))
	return set, nil
}

// fetch retrieves and parses the issuer's key set.
func (v *Verifier) fetch(ctx context.Context) (*jwcrypt.JWKSet, error) {
	keysURL, err := v.resolveKeysURL(ctx)
	if err != nil {
		return nil, err
	}
	body, err := getJSON(ctx, keysURL)
	if err != nil {
		return nil, err
	}
	return jwcrypt.ParseJWKSet(body)
}

// resolveKeysURL returns the key-set URL, reading it out of the OIDC discovery
// document the first time. The result is remembered because an issuer's
// jwks_uri effectively never moves, and re-reading discovery on every refresh
// doubles the request count for nothing.
//
// Caller holds v.mu.
func (v *Verifier) resolveKeysURL(ctx context.Context) (string, error) {
	if v.keysURL != "" {
		return v.keysURL, nil
	}
	if strings.TrimSpace(v.JWKSURL) != "" {
		if err := checkURL(v.JWKSURL); err != nil {
			return "", fmt.Errorf("jwks url: %w", err)
		}
		v.keysURL = strings.TrimSpace(v.JWKSURL)
		return v.keysURL, nil
	}
	if err := checkURL(v.MetadataURL); err != nil {
		return "", fmt.Errorf("metadata url: %w", err)
	}
	body, err := getJSON(ctx, strings.TrimSpace(v.MetadataURL))
	if err != nil {
		return "", err
	}
	var doc struct {
		JWKSURI string `json:"jwks_uri"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", fmt.Errorf("metadata document is not JSON: %w", err)
	}
	if strings.TrimSpace(doc.JWKSURI) == "" {
		return "", fmt.Errorf("metadata document names no jwks_uri")
	}
	if err := checkURL(doc.JWKSURI); err != nil {
		return "", fmt.Errorf("jwks_uri from metadata: %w", err)
	}
	v.keysURL = strings.TrimSpace(doc.JWKSURI)
	return v.keysURL, nil
}

func (v *Verifier) label() string {
	switch {
	case v.Name != "":
		return v.Name
	case v.Issuer != "":
		return v.Issuer
	}
	return "jwks"
}

// getJSON fetches a JSON document, bounded in size and status-checked.
func getJSON(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s: HTTP %d", rawURL, resp.StatusCode)
	}
	return body, nil
}

// checkURL insists on https. Key material fetched over plaintext can be
// swapped in transit, and swapped keys mean an attacker mints tokens this
// verifier then accepts -- so the transport is part of the trust, not a
// deployment detail. Loopback is exempt so a test can serve a fixture.
func checkURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return fmt.Errorf("not a valid url: %q", raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if loopbackHost(u.Hostname()) {
			return nil
		}
	}
	return fmt.Errorf("must be https (got %q)", raw)
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
