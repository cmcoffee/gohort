package core

// Rotating peer credentials.
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

// grantFor mints a key in a scratch store, optionally on the token flow.
func grantFor(t *testing.T, rotating bool) PeerKey {
	t.Helper()
	k, err := MintPeerKey("test-peer", []string{PeerCapEmbeddings}, 0)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if rotating {
		if k, err = SetPeerKeyRotating(k.ID, true); err != nil {
			t.Fatalf("set rotating: %v", err)
		}
	}
	return k
}

// --- the happy path ----------------------------------------------------------

func TestPairingCodeBuysATokenPairThatAuthenticates(t *testing.T) {
	defer scratchPeerStore(t)()
	k := grantFor(t, true)

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
	k := grantFor(t, true)
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
	k := grantFor(t, true)
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
	k := grantFor(t, true)
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

func TestARotatingPairingCodeIsSingleUse(t *testing.T) {
	defer scratchPeerStore(t)()
	k := grantFor(t, true)

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

func TestARotatingKeyIsRefusedAsABearer(t *testing.T) {
	defer scratchPeerStore(t)()
	k := grantFor(t, true)

	if authenticates(k.Key) {
		t.Fatal("a rotating grant's pairing code authenticated a capability call — " +
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

// --- backward compatibility --------------------------------------------------

// TestAnOrdinaryKeyKeepsWorkingUntouched — the property that makes this release
// safe to ship. Every key minted before the token flow existed has Rotating
// false, and a peer using one must not notice anything changed.
func TestAnOrdinaryKeyKeepsWorkingUntouched(t *testing.T) {
	defer scratchPeerStore(t)()
	k := grantFor(t, false)

	if !authenticates(k.Key) {
		t.Fatal("an ordinary peer key stopped authenticating — this breaks every live pairing")
	}
	// Exchange is still OFFERED, so a consuming instance can adopt the flow
	// before the operator commits the serving side to it.
	pair, code, msg := exchange(t, pairingBody(k.Key))
	if code != http.StatusOK {
		t.Fatalf("exchange on an ordinary key → %d: %s", code, msg)
	}
	if !authenticates(pair.AccessToken) {
		t.Error("a token issued against an ordinary key does not authenticate")
	}
	// And the code is NOT spent, because on an ordinary grant it is a standing
	// credential rather than a pairing code.
	if _, code, _ := exchange(t, pairingBody(k.Key)); code != http.StatusOK {
		t.Errorf("exchanging an ordinary key twice → %d, want 200", code)
	}
	if !authenticates(k.Key) {
		t.Error("exchanging an ordinary key revoked it")
	}
}

// --- revocation reaches issued credentials -----------------------------------

func TestDisablingAGrantRevokesItsTokens(t *testing.T) {
	defer scratchPeerStore(t)()
	k := grantFor(t, true)
	pair, _, _ := exchange(t, pairingBody(k.Key))

	SetPeerKeyDisabled(k.ID, true)
	if authenticates(pair.AccessToken) {
		t.Error("a disabled grant's access token still authenticates — revoked on screen, not in fact")
	}
	if _, code, _ := exchange(t, refreshBody(pair.RefreshToken)); code == http.StatusOK {
		t.Error("a disabled grant still rotates its tokens")
	}
}

func TestDeletingAGrantRevokesItsTokens(t *testing.T) {
	defer scratchPeerStore(t)()
	k := grantFor(t, true)
	pair, _, _ := exchange(t, pairingBody(k.Key))

	DeletePeerKey(k.ID)
	if authenticates(pair.AccessToken) {
		t.Error("a deleted grant's access token still authenticates")
	}
	if _, code, _ := exchange(t, refreshBody(pair.RefreshToken)); code == http.StatusOK {
		t.Error("a deleted grant still rotates its tokens")
	}
}

// --- expiry ------------------------------------------------------------------

// TestAnExpiredAccessTokenIsRefusedWithoutWaitingForASweep — expiry is enforced
// at the lookup, so a sweep that has not run yet is never the difference
// between valid and not.
func TestAnExpiredAccessTokenIsRefusedWithoutWaitingForASweep(t *testing.T) {
	defer scratchPeerStore(t)()
	k := grantFor(t, true)
	pair, _, _ := exchange(t, pairingBody(k.Key))

	stored, ok := getPeerAccessToken(pair.AccessToken)
	if !ok {
		t.Fatal("the access token was not stored")
	}
	stored.Expires = time.Now().Add(-time.Second)
	RootDB.Set(peerAccessTable, pair.AccessToken, stored)

	if authenticates(pair.AccessToken) {
		t.Error("an expired access token still authenticates")
	}
	// The refresh half is untouched by the access token's expiry — that is the
	// entire point of the pair.
	if _, code, msg := exchange(t, refreshBody(pair.RefreshToken)); code != http.StatusOK {
		t.Errorf("refresh after access expiry → %d: %s", code, msg)
	}
}

func TestAnExpiredRefreshTokenIsRefused(t *testing.T) {
	defer scratchPeerStore(t)()
	k := grantFor(t, true)
	pair, _, _ := exchange(t, pairingBody(k.Key))

	stored, _ := getPeerRefreshToken(pair.RefreshToken)
	stored.Expires = time.Now().Add(-time.Second)
	RootDB.Set(peerRefreshTable, pair.RefreshToken, stored)

	_, code, msg := exchange(t, refreshBody(pair.RefreshToken))
	if code != http.StatusUnauthorized {
		t.Fatalf("expired refresh → %d, want 401", code)
	}
	if !strings.Contains(msg, "expired") {
		t.Errorf("the refusal does not say it expired: %q", msg)
	}
}

// --- re-pairing --------------------------------------------------------------

// TestRepairIssuesANewCodeAndDropsTheOldChain — the replacement for the admin
// page's re-display affordance, which a spent code cannot offer.
func TestRepairIssuesANewCodeAndDropsTheOldChain(t *testing.T) {
	defer scratchPeerStore(t)()
	k := grantFor(t, true)
	old, _, _ := exchange(t, pairingBody(k.Key))

	repaired, err := RepairPeerKey(k.ID)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if repaired.Key == k.Key {
		t.Error("re-pairing reissued the same code")
	}
	if repaired.Paired != "" {
		t.Error("re-pairing left the code marked as already exchanged")
	}
	if authenticates(old.AccessToken) {
		t.Error("the previous chain survived a re-pair — two credentials for one grant")
	}
	// Capabilities and scope survive: the grant is the durable thing, the
	// credential is the lease.
	if len(repaired.Caps) != len(k.Caps) {
		t.Errorf("re-pairing changed the capability grant: %v → %v", k.Caps, repaired.Caps)
	}
	if _, code, msg := exchange(t, pairingBody(repaired.Key)); code != http.StatusOK {
		t.Errorf("the new pairing code does not work → %d: %s", code, msg)
	}
}

// TestTurningRotationOffRestoresTheStaticKey — a switch the operator cannot
// reverse is one they cannot recover from without re-pairing both ends.
func TestTurningRotationOffRestoresTheStaticKey(t *testing.T) {
	defer scratchPeerStore(t)()
	k := grantFor(t, true)
	if _, code, _ := exchange(t, pairingBody(k.Key)); code != http.StatusOK {
		t.Fatal("pairing failed")
	}
	if authenticates(k.Key) {
		t.Fatal("precondition: a rotating key should not authenticate")
	}

	if _, err := SetPeerKeyRotating(k.ID, false); err != nil {
		t.Fatalf("unset rotating: %v", err)
	}
	if !authenticates(k.Key) {
		t.Error("turning rotation off did not restore the static key")
	}
}

// --- malformed input ---------------------------------------------------------

func TestTheTokenEndpointRefusesNonsense(t *testing.T) {
	defer scratchPeerStore(t)()
	grantFor(t, true)

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
	ordinary := grantFor(t, false)
	rotating := grantFor(t, true)

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
	if m.Token.Required {
		t.Error("exchange is reported as required for an ordinary key")
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
