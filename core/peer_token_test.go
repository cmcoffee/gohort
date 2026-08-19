package core

// Rotating peer credentials, serving half.
//
// The reason this file is long for the amount of code it covers: every branch
// here is a security decision, and the ones that fail OPEN (a spent pairing code
// still working, a static key still accepted on a rotating grant, a revoked
// grant's access token outliving it) all look exactly like success from the
// outside. They have to be asserted, because nothing else would notice.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// exchange posts a token request and returns the decoded response plus status.
func exchange(t *testing.T, body string) (peerTokenPair, int, string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/peer/v1/token", strings.NewReader(body))
	w := httptest.NewRecorder()
	HandlePeerToken(w, r)
	var pair peerTokenPair
	var errBody struct {
		Error string `json:"error"`
	}
	raw := w.Body.Bytes()
	_ = json.Unmarshal(raw, &pair)
	_ = json.Unmarshal(raw, &errBody)
	return pair, w.Code, errBody.Error
}

func pairingBody(code string) string {
	return `{"grant_type":"pairing_code","pairing_code":"` + code + `"}`
}

func refreshBody(tok string) string {
	return `{"grant_type":"refresh_token","refresh_token":"` + tok + `"}`
}

// authenticates reports whether a secret gets through peerFromRequest — the
// single gate every capability handler sits behind.
func authenticates(secret string) bool {
	r := httptest.NewRequest(http.MethodGet, "/api/peer/v1/manifest", nil)
	r.Header.Set(peerKeyHeader, secret)
	_, ok := peerFromRequest(r)
	return ok
}

// grantFor mints a key in a scratch store. There is no flag: exchange is
// mandatory, so every key is a pairing code.
func grantFor(t *testing.T) PeerKey {
	t.Helper()
	k, err := MintPeerKey("test-peer", []string{PeerCapEmbeddings}, 0)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	return k
}

// --- the happy path ----------------------------------------------------------

func TestPairingCodeBuysATokenPairThatAuthenticates(t *testing.T) {
	defer scratchPeerStore(t)()
	k := grantFor(t)

	pair, code, msg := exchange(t, pairingBody(k.Key))
	if code != http.StatusOK {
		t.Fatalf("exchange → %d: %s", code, msg)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatalf("exchange returned an incomplete pair: %+v", pair)
	}
	if pair.TokenType != "Bearer" {
		t.Errorf("token_type = %q", pair.TokenType)
	}
	if pair.ExpiresIn != int(peerAccessTTL/time.Second) {
		t.Errorf("expires_in = %d, want %d", pair.ExpiresIn, int(peerAccessTTL/time.Second))
	}
	if !authenticates(pair.AccessToken) {
		t.Error("the access token does not authenticate a request")
	}
	if authenticates(pair.RefreshToken) {
		t.Error("the REFRESH token authenticates a capability call — it must only work at the token endpoint")
	}
}

func TestRefreshRotatesBothHalves(t *testing.T) {
	defer scratchPeerStore(t)()
	k := grantFor(t)
	first, _, _ := exchange(t, pairingBody(k.Key))

	second, code, msg := exchange(t, refreshBody(first.RefreshToken))
	if code != http.StatusOK {
		t.Fatalf("refresh → %d: %s", code, msg)
	}
	if second.RefreshToken == first.RefreshToken {
		t.Error("the refresh token did not rotate — a static refresh token is the long-lived secret by another name")
	}
	if second.AccessToken == first.AccessToken {
		t.Error("the access token did not rotate")
	}
	if !authenticates(second.AccessToken) {
		t.Error("the rotated access token does not authenticate")
	}
}

// --- reuse detection ---------------------------------------------------------

// TestReplayInsideTheGraceWindowReturnsTheSameSuccessor — the concurrency case.
// A peer runs several capabilities at once, so a dropped response or two
// goroutines racing an expiry both present a just-used token. Treating that as
// theft would disable the grant and take the link down: the alarm firing on its
// own tail.
func TestReplayInsideTheGraceWindowReturnsTheSameSuccessor(t *testing.T) {
	defer scratchPeerStore(t)()
	k := grantFor(t)
	first, _, _ := exchange(t, pairingBody(k.Key))

	second, _, _ := exchange(t, refreshBody(first.RefreshToken))
	replay, code, msg := exchange(t, refreshBody(first.RefreshToken))
	if code != http.StatusOK {
		t.Fatalf("replay inside the grace window → %d: %s", code, msg)
	}
	if replay.RefreshToken != second.RefreshToken || replay.AccessToken != second.AccessToken {
		t.Error("the replay minted a SECOND family — the caller and the server now track different chains")
	}
	if replay.ExpiresIn > second.ExpiresIn {
		t.Errorf("the replay reported a longer life (%d) than the token really has (%d), so the caller renews late",
			replay.ExpiresIn, second.ExpiresIn)
	}
	if got, _ := peerGrantByID(k.ID); got.Disabled {
		t.Error("a retry disabled the grant")
	}
}

// TestReuseOutsideTheGraceWindowKillsTheFamily — the alarm. A legitimate holder
// has long since moved to the successor, so this is someone else's copy.
func TestReuseOutsideTheGraceWindowKillsTheFamily(t *testing.T) {
	defer scratchPeerStore(t)()
	k := grantFor(t)
	first, _, _ := exchange(t, pairingBody(k.Key))
	second, _, _ := exchange(t, refreshBody(first.RefreshToken))

	// Age the consumption past the grace window.
	stored, ok := getPeerRefreshToken(first.RefreshToken)
	if !ok {
		t.Fatal("the consumed refresh token was deleted — reuse would read as an unknown token")
	}
	stored.ConsumedAt = time.Now().Add(-peerRefreshGrace - time.Minute)
	RootDB.Set(peerRefreshTable, first.RefreshToken, stored)

	_, code, msg := exchange(t, refreshBody(first.RefreshToken))
	if code != http.StatusUnauthorized {
		t.Fatalf("reuse → %d, want 401", code)
	}
	if !strings.Contains(msg, "already used") {
		t.Errorf("the refusal does not say why: %q", msg)
	}
	if got, _ := peerGrantByID(k.ID); !got.Disabled {
		t.Error("reuse did not disable the grant")
	}
	if authenticates(second.AccessToken) {
		t.Error("the legitimate holder's access token still works after a theft was detected")
	}
	if _, code, _ := exchange(t, refreshBody(second.RefreshToken)); code == http.StatusOK {
		t.Error("the legitimate holder's refresh token still works after a theft was detected")
	}
}

// --- single use --------------------------------------------------------------

func TestAPairingCodeIsSingleUse(t *testing.T) {
	defer scratchPeerStore(t)()
	k := grantFor(t)

	if _, code, msg := exchange(t, pairingBody(k.Key)); code != http.StatusOK {
		t.Fatalf("first exchange → %d: %s", code, msg)
	}
	_, code, msg := exchange(t, pairingBody(k.Key))
	if code != http.StatusUnauthorized {
		t.Fatalf("second exchange → %d, want 401 — a reusable code is the long-lived secret this replaces", code)
	}
	if !strings.Contains(msg, "single use") {
		t.Errorf("the refusal does not explain itself: %q", msg)
	}
}

func TestAPeerKeyIsRefusedAsABearer(t *testing.T) {
	defer scratchPeerStore(t)()
	k := grantFor(t)

	if authenticates(k.Key) {
		t.Fatal("a pairing code authenticated a capability call — " +
			"the long-lived bearer secret is still in play and nothing is rotating")
	}
	// And the refusal must point somewhere, or it reads as a broken key.
	r := httptest.NewRequest(http.MethodGet, "/api/peer/v1/manifest", nil)
	r.Header.Set(peerKeyHeader, k.Key)
	w := httptest.NewRecorder()
	if _, ok := peerAuthorize(w, r, PeerCapEmbeddings); ok {
		t.Fatal("peerAuthorize admitted a pairing code")
	}
	if !strings.Contains(w.Body.String(), "/api/peer/v1/token") {
		t.Errorf("the refusal does not name the token endpoint: %s", w.Body.String())
	}
}

// --- the compatibility break -------------------------------------------------

// The property that USED to hold here was the opposite: an ordinary key kept
// working untouched, because rotation was opt-in per grant. It is not any more.
// Exchange is mandatory, so a key authenticates nothing and this test exists to
// make that break loud if anybody softens it back.
func TestAKeyIsNeverACredential(t *testing.T) {
	defer scratchPeerStore(t)()
	k := grantFor(t)

	if authenticates(k.Key) {
		t.Fatal("a peer key authenticated a request — it is a pairing code, and accepting it leaves exactly the long-lived bearer secret the token flow exists to retire")
	}
	pair, code, msg := exchange(t, pairingBody(k.Key))
	if code != http.StatusOK {
		t.Fatalf("exchange → %d: %s", code, msg)
	}
	if !authenticates(pair.AccessToken) {
		t.Error("the access token from an exchange does not authenticate")
	}
	// And the code is spent, so a copy of it taken from a chat log is worth
	// nothing once the peer it was meant for has paired.
	if _, code, _ := exchange(t, pairingBody(k.Key)); code == http.StatusOK {
		t.Error("a spent pairing code was honored a second time")
	}
}

// First contact has to be possible. A peer arriving with nothing but its code
// learns that it must exchange by READING THE MANIFEST, so the manifest is the
// one door an unspent code opens — otherwise the instruction sits behind the
// lock it is the instruction for.
func TestAnUnspentCodeCanReadTheManifestAndNothingElse(t *testing.T) {
	defer scratchPeerStore(t)()
	k := grantFor(t)

	r := httptest.NewRequest(http.MethodGet, "/api/peer/manifest", nil)
	r.Header.Set(peerKeyHeader, k.Key)
	w := httptest.NewRecorder()
	HandlePeerManifest(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("first contact could not read the manifest → %d: %s", w.Code, w.Body.String())
	}
	var m PeerManifest
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("manifest decode: %v", err)
	}
	// And it must SAY exchange is required, or the peer has no reason to.
	if m.Token == nil || !m.Token.Required {
		t.Fatalf("the manifest does not require exchange: %+v", m.Token)
	}

	// Once spent, the code opens nothing at all — the peer holds tokens by then
	// and has no reason to come back with it.
	if _, code, msg := exchange(t, pairingBody(k.Key)); code != http.StatusOK {
		t.Fatalf("exchange → %d: %s", code, msg)
	}
	r2 := httptest.NewRequest(http.MethodGet, "/api/peer/manifest", nil)
	r2.Header.Set(peerKeyHeader, k.Key)
	w2 := httptest.NewRecorder()
	HandlePeerManifest(w2, r2)
	if w2.Code == http.StatusOK {
		t.Error("a spent pairing code still read the manifest")
	}
}

// --- malformed input ---------------------------------------------------------

func TestTheTokenEndpointRefusesNonsense(t *testing.T) {
	defer scratchPeerStore(t)()
	grantFor(t)

	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"unknown grant type", `{"grant_type":"authorization_code"}`, http.StatusBadRequest},
		{"missing grant type", `{}`, http.StatusBadRequest},
		{"pairing with no code", `{"grant_type":"pairing_code"}`, http.StatusBadRequest},
		{"refresh with no token", `{"grant_type":"refresh_token"}`, http.StatusBadRequest},
		{"unknown pairing code", pairingBody("nope"), http.StatusUnauthorized},
		{"unknown refresh token", refreshBody("nope"), http.StatusUnauthorized},
		{"not json", `not json`, http.StatusBadRequest},
	} {
		if _, code, _ := exchange(t, tc.body); code != tc.want {
			t.Errorf("%s → %d, want %d", tc.name, code, tc.want)
		}
	}

	r := httptest.NewRequest(http.MethodGet, "/api/peer/v1/token", nil)
	w := httptest.NewRecorder()
	HandlePeerToken(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET → %d, want 405", w.Code)
	}
}

// --- discovery ---------------------------------------------------------------

// TestTheManifestAdvertisesExchangePerKey — a peer that discovers the token flow
// only when its static key starts being refused has already broken.
func TestTheManifestAdvertisesExchangePerKey(t *testing.T) {
	defer scratchPeerStore(t)()
	ordinary := grantFor(t)
	rotating := grantFor(t)

	manifestFor := func(secret string) PeerManifest {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/peer/v1/manifest", nil)
		r.Header.Set(peerKeyHeader, secret)
		w := httptest.NewRecorder()
		HandlePeerManifest(w, r)
		var m PeerManifest
		if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
			t.Fatalf("manifest (%d): %v", w.Code, err)
		}
		return m
	}

	m := manifestFor(ordinary.Key)
	if m.Token == nil {
		t.Fatal("the manifest does not mention exchange at all")
	}
	if m.Token.Path != "/api/peer/v1/token" {
		t.Errorf("token path = %q", m.Token.Path)
	}
	// Required for EVERY key now. It used to be per-grant, which is what let a
	// consuming instance adopt the flow ahead of the operator; that ordering
	// existed for the changeover and the changeover is done.
	if !m.Token.Required {
		t.Error("the manifest does not require exchange — a peer reading this has no reason to pair")
	}
	if m.Token.ExpiresIn != int(peerAccessTTL/time.Second) {
		t.Errorf("expires_in = %d", m.Token.ExpiresIn)
	}

	// The rotating grant reaches the manifest through its access token.
	pair, _, _ := exchange(t, pairingBody(rotating.Key))
	if got := manifestFor(pair.AccessToken); got.Token == nil || !got.Token.Required {
		t.Error("exchange is not reported as required for a rotating key")
	}
}
