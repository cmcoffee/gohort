package jwks

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/jwcrypt"
)

// One key pair for the whole file: RSA generation otherwise dominates the run.
var (
	keyOnce sync.Once
	keyA    *rsa.PrivateKey
	keyB    *rsa.PrivateKey
)

func testKeys(t *testing.T) (*rsa.PrivateKey, *rsa.PrivateKey) {
	t.Helper()
	keyOnce.Do(func() {
		var err error
		if keyA, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
		if keyB, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
	})
	return keyA, keyB
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func publicJWK(k *rsa.PrivateKey, kid, extra string) string {
	return fmt.Sprintf(`{"kty":"RSA","use":"sig","alg":"RS256","kid":%q,"n":%q,"e":%q%s}`,
		kid, b64(k.N.Bytes()), b64(big.NewInt(int64(k.E)).Bytes()), extra)
}

func testClaims(aud string) map[string]interface{} {
	return map[string]interface{}{
		"iss": "https://issuer.test",
		"aud": aud,
		"exp": time.Now().Add(time.Hour).Unix(),
	}
}

func sign(t *testing.T, k *rsa.PrivateKey, kid, aud string) string {
	t.Helper()
	tok, err := jwcrypt.SignRS256(k, testClaims(aud), map[string]string{"kid": kid})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// keyServer serves an OIDC discovery document and a key set whose body the
// test can swap, counting fetches of each so caching behaviour is observable.
type keyServer struct {
	*httptest.Server
	mu       sync.Mutex
	keys     string
	fail     bool
	metaHits atomic.Int32
	keyHits  atomic.Int32
}

func newKeyServer(t *testing.T, keys string) *keyServer {
	t.Helper()
	s := &keyServer{keys: keys}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openidconfiguration", func(w http.ResponseWriter, r *http.Request) {
		s.metaHits.Add(1)
		fmt.Fprintf(w, `{"issuer":"https://issuer.test","jwks_uri":%q}`, s.URL+"/keys")
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		s.keyHits.Add(1)
		s.mu.Lock()
		body, fail := s.keys, s.fail
		s.mu.Unlock()
		if fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func (s *keyServer) setKeys(keys string) {
	s.mu.Lock()
	s.keys = keys
	s.mu.Unlock()
}

func (s *keyServer) setFail(fail bool) {
	s.mu.Lock()
	s.fail = fail
	s.mu.Unlock()
}

func (s *keyServer) verifier() *Verifier {
	return &Verifier{
		MetadataURL: s.URL + "/.well-known/openidconfiguration",
		Issuer:      "https://issuer.test",
		Name:        "test-issuer",
	}
}

// --- happy path --------------------------------------------------------------

func TestVerifierVerifyThroughDiscovery(t *testing.T) {
	a, _ := testKeys(t)
	srv := newKeyServer(t, fmt.Sprintf(`{"keys":[%s]}`, publicJWK(a, "k1", "")))
	v := srv.verifier()

	claims, err := v.Verify(context.Background(), sign(t, a, "k1", "app-1"), "app-1")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.String("iss") != "https://issuer.test" {
		t.Errorf("iss = %q", claims.String("iss"))
	}
	if srv.metaHits.Load() != 1 || srv.keyHits.Load() != 1 {
		t.Errorf("fetches: meta=%d keys=%d, want 1 and 1", srv.metaHits.Load(), srv.keyHits.Load())
	}
}

func TestVerifierVerifyDirectKeysURL(t *testing.T) {
	a, _ := testKeys(t)
	srv := newKeyServer(t, fmt.Sprintf(`{"keys":[%s]}`, publicJWK(a, "k1", "")))
	v := &Verifier{JWKSURL: srv.URL + "/keys", Issuer: "https://issuer.test"}

	if _, err := v.Verify(context.Background(), sign(t, a, "k1", "app-1"), "app-1"); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if srv.metaHits.Load() != 0 {
		t.Error("discovery was fetched despite an explicit JWKSURL")
	}
}

// The audience is what separates our traffic from every other tenant the same
// issuer signs for, so a mismatch has to fail.
func TestVerifierVerifyEnforcesAudienceAndIssuer(t *testing.T) {
	a, _ := testKeys(t)
	srv := newKeyServer(t, fmt.Sprintf(`{"keys":[%s]}`, publicJWK(a, "k1", "")))
	v := srv.verifier()
	ctx := context.Background()

	if _, err := v.Verify(ctx, sign(t, a, "k1", "someone-else"), "app-1"); err == nil {
		t.Error("a token for another audience verified")
	}
	tok, err := jwcrypt.SignRS256(a, map[string]interface{}{
		"iss": "https://evil.test", "aud": "app-1",
		"exp": time.Now().Add(time.Hour).Unix(),
	}, map[string]string{"kid": "k1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(ctx, tok, "app-1"); err == nil {
		t.Error("a token from another issuer verified")
	}
}

// --- caching -----------------------------------------------------------------

func TestVerifierCachesKeys(t *testing.T) {
	a, _ := testKeys(t)
	srv := newKeyServer(t, fmt.Sprintf(`{"keys":[%s]}`, publicJWK(a, "k1", "")))
	v := srv.verifier()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if _, err := v.Verify(ctx, sign(t, a, "k1", "app-1"), "app-1"); err != nil {
			t.Fatalf("verify %d: %v", i, err)
		}
	}
	if got := srv.keyHits.Load(); got != 1 {
		t.Errorf("fetched the key set %d times for 5 verifies, want 1", got)
	}
}

func TestVerifierRefetchesAfterTTL(t *testing.T) {
	a, _ := testKeys(t)
	srv := newKeyServer(t, fmt.Sprintf(`{"keys":[%s]}`, publicJWK(a, "k1", "")))
	v := srv.verifier()
	v.TTL = time.Millisecond
	ctx := context.Background()

	if _, err := v.Verify(ctx, sign(t, a, "k1", "app-1"), "app-1"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := v.Verify(ctx, sign(t, a, "k1", "app-1"), "app-1"); err != nil {
		t.Fatal(err)
	}
	if got := srv.keyHits.Load(); got != 2 {
		t.Errorf("key fetches = %d, want 2 (TTL expired between calls)", got)
	}
}

func TestVerifierSingleFlight(t *testing.T) {
	a, _ := testKeys(t)
	srv := newKeyServer(t, fmt.Sprintf(`{"keys":[%s]}`, publicJWK(a, "k1", "")))
	v := srv.verifier()
	token := sign(t, a, "k1", "app-1")

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := v.Verify(context.Background(), token, "app-1"); err != nil {
				t.Errorf("concurrent verify: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := srv.keyHits.Load(); got != 1 {
		t.Errorf("20 concurrent verifies caused %d fetches, want 1", got)
	}
}

// --- rotation ----------------------------------------------------------------

// A key the cache has never seen is what a rotation looks like from here, and
// it is the one failure worth re-fetching for.
func TestVerifierUnknownKidTriggersRefetch(t *testing.T) {
	a, b := testKeys(t)
	srv := newKeyServer(t, fmt.Sprintf(`{"keys":[%s]}`, publicJWK(a, "k1", "")))
	v := srv.verifier()
	ctx := context.Background()

	if _, err := v.Verify(ctx, sign(t, a, "k1", "app-1"), "app-1"); err != nil {
		t.Fatal(err)
	}

	// The issuer rotates: k2 appears, and a token arrives signed with it.
	srv.setKeys(fmt.Sprintf(`{"keys":[%s,%s]}`,
		publicJWK(a, "k1", ""), publicJWK(b, "k2", "")))

	if _, err := v.Verify(ctx, sign(t, b, "k2", "app-1"), "app-1"); err != nil {
		t.Fatalf("token signed with a rotated-in key failed: %v", err)
	}
	if got := srv.keyHits.Load(); got != 2 {
		t.Errorf("key fetches = %d, want 2 (one initial, one on the unknown kid)", got)
	}
}

// Without a cooldown, forged tokens naming random key ids would turn this into
// a request amplifier pointed at the issuer.
func TestVerifierUnknownKidRefetchIsRateLimited(t *testing.T) {
	a, _ := testKeys(t)
	srv := newKeyServer(t, fmt.Sprintf(`{"keys":[%s]}`, publicJWK(a, "k1", "")))
	v := srv.verifier()
	ctx := context.Background()

	if _, err := v.Verify(ctx, sign(t, a, "k1", "app-1"), "app-1"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		tok := sign(t, a, fmt.Sprintf("forged-%d", i), "app-1")
		if _, err := v.Verify(ctx, tok, "app-1"); err == nil {
			t.Fatal("a token naming an absent key verified")
		}
	}
	if got := srv.keyHits.Load(); got != 2 {
		t.Errorf("key fetches = %d, want 2 (10 unknown kids collapse to one refetch)", got)
	}
}

// A bad signature is not a rotation, so it must not pull the issuer at all.
func TestVerifierBadSignatureDoesNotRefetch(t *testing.T) {
	a, b := testKeys(t)
	srv := newKeyServer(t, fmt.Sprintf(`{"keys":[%s]}`, publicJWK(a, "k1", "")))
	v := srv.verifier()
	ctx := context.Background()

	if _, err := v.Verify(ctx, sign(t, a, "k1", "app-1"), "app-1"); err != nil {
		t.Fatal(err)
	}
	// Signed with the wrong key but naming a kid the set does hold.
	if _, err := v.Verify(ctx, sign(t, b, "k1", "app-1"), "app-1"); err == nil {
		t.Fatal("a token signed with an unrelated key verified")
	}
	if got := srv.keyHits.Load(); got != 1 {
		t.Errorf("key fetches = %d, want 1 (a bad signature is not a rotation)", got)
	}
}

// --- availability ------------------------------------------------------------

// Keys are public and a signature check is not weakened by their age. Dropping
// live traffic because the issuer's endpoint blipped would cost more than it
// protects, so a stale set keeps working inside the grace window.
func TestVerifierServesStaleKeysWhenRefreshFails(t *testing.T) {
	a, _ := testKeys(t)
	srv := newKeyServer(t, fmt.Sprintf(`{"keys":[%s]}`, publicJWK(a, "k1", "")))
	v := srv.verifier()
	v.TTL = time.Millisecond
	ctx := context.Background()

	if _, err := v.Verify(ctx, sign(t, a, "k1", "app-1"), "app-1"); err != nil {
		t.Fatal(err)
	}
	srv.setFail(true)
	time.Sleep(10 * time.Millisecond)

	if _, err := v.Verify(ctx, sign(t, a, "k1", "app-1"), "app-1"); err != nil {
		t.Fatalf("a stale-but-valid key set was refused: %v", err)
	}
}

func TestVerifierFailsWhenNeverFetched(t *testing.T) {
	a, _ := testKeys(t)
	srv := newKeyServer(t, `{"keys":[]}`)
	srv.setFail(true)
	v := srv.verifier()

	if _, err := v.Verify(context.Background(), sign(t, a, "k1", "app-1"), "app-1"); err == nil {
		t.Fatal("verification succeeded with no keys ever fetched")
	} else if !strings.Contains(err.Error(), "test-issuer") {
		t.Errorf("error should name the issuer, got: %v", err)
	}
}

func TestVerifierMetadataWithoutJWKSURI(t *testing.T) {
	a, _ := testKeys(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/meta", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"issuer":"https://issuer.test"}`)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)

	v := &Verifier{MetadataURL: s.URL + "/meta", Issuer: "https://issuer.test"}
	_, err := v.Verify(context.Background(), sign(t, a, "k1", "app-1"), "app-1")
	if err == nil || !strings.Contains(err.Error(), "jwks_uri") {
		t.Fatalf("error = %v, want it to mention jwks_uri", err)
	}
}

// --- key metadata ------------------------------------------------------------

func TestVerifierKeyByIDExposesExtraMembers(t *testing.T) {
	a, _ := testKeys(t)
	srv := newKeyServer(t, fmt.Sprintf(`{"keys":[%s]}`,
		publicJWK(a, "k1", `,"endorsements":["msteams"]`)))
	v := srv.verifier()

	key, ok := v.KeyByID(context.Background(), "k1")
	if !ok {
		t.Fatal("KeyByID missed a key that is in the set")
	}
	if string(key.Extra["endorsements"]) != `["msteams"]` {
		t.Errorf("endorsements = %s", key.Extra["endorsements"])
	}
	if _, ok := v.KeyByID(context.Background(), "absent"); ok {
		t.Error("KeyByID invented a key")
	}
}

// --- transport ---------------------------------------------------------------

// Key material fetched over plaintext can be swapped in transit, and swapped
// keys mean forged tokens this verifier would accept.
func TestCheckURL(t *testing.T) {
	ok := []string{
		"https://login.botframework.com/v1/.well-known/openidconfiguration",
		"http://127.0.0.1:9000/keys",
		"http://localhost:9000/keys",
		"http://[::1]:9000/keys",
	}
	bad := []string{
		"http://login.botframework.com/keys",
		"http://10.0.0.5/keys",
		"ftp://example.com/keys",
		"not a url",
		"",
		"/keys",
	}
	for _, u := range ok {
		if err := checkURL(u); err != nil {
			t.Errorf("%q rejected: %v", u, err)
		}
	}
	for _, u := range bad {
		if err := checkURL(u); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
}
