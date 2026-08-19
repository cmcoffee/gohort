// Peer credentials that rotate.
//
// A peer key is a bearer secret with no expiry: whoever holds it is the peer,
// forever, and the only lever against a copy of it is an operator noticing and
// revoking by hand. That is defensible between two machines one person owns and
// stops being defensible the moment a grant outlives the reason it was made.
//
// This file adds the other shape. The KEY becomes a pairing code exchanged once
// for a short-lived access token and a refresh token, and every refresh rotates
// both. The security property is not the expiry — it is that a stolen refresh
// token must be USED to be worth anything, and using it makes the theft visible:
// the legitimate holder's next refresh presents a token the server has already
// consumed, and that collision is the alarm. Nothing else about a copied bearer
// secret is detectable at all.
//
// MANDATORY, AND IT BREAKS COMPATIBILITY. It shipped opt-in per grant so a
// consuming instance could adopt the flow before the operator committed to it;
// that ordering existed to avoid stranding a peer during the changeover, and it
// has served its purpose. Every grant now works one way: the key is a pairing
// code, exchanged here exactly once, and refused at every capability endpoint.
//
// The opt-in could not stay. A switch that leaves a long-lived bearer secret
// working when it is off is a switch most deployments never flip, and a
// rotation scheme nobody turns on is a rotation scheme that does not exist. It
// also made the admin page dishonest in a way that mattered: an operator
// looking at a key could not tell, without reading a toggle two columns away,
// whether the string in front of them was a credential or a spent code.
//
// A peer running a build older than the consuming half (v0.6.288) will 401
// after this and must be upgraded. Both ends are the same operator by
// construction — a peer key is not an account — so that is a rollout ordering
// problem, not a compatibility surface owed to strangers.
package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// peerAccessTable and peerRefreshTable hold issued tokens keyed by the
	// SECRET, so authenticating a request is one Get rather than a scan of
	// every token ever issued. Same storage decision as AccountToken, and it
	// carries no timing leak: a keyed lookup does not compare the presented
	// secret character by character the way LookupPeerKey's constant-time
	// sweep exists to defend against.
	peerAccessTable  = "peer_access_tokens"
	peerRefreshTable = "peer_refresh_tokens"
)

const (
	// peerAccessTTL bounds one access token. Short enough that a copy taken
	// off the wire is worth little, long enough that a peer holding several
	// capabilities open is not spending its time re-authenticating.
	peerAccessTTL = 15 * time.Minute

	// peerRefreshTTL bounds a whole token family. A peer that has said nothing
	// for a month is not a peer any more, and this is the expiry the static
	// key never had.
	peerRefreshTTL = 30 * 24 * time.Hour

	// peerRefreshGrace is how long a just-consumed refresh token still answers,
	// by REPLAYING the successor it already produced rather than rotating
	// again.
	//
	// This window is what separates a retry from a theft, and without it the
	// two are indistinguishable. A peer runs several capabilities at once; a
	// dropped response, a restart mid-exchange, or two goroutines racing the
	// same expiry all produce a second presentation of a token that was just
	// used. Treating those as stolen would disable the grant and take the link
	// down — the alarm firing on its own tail. Outside the window the same
	// event is the real thing, because a legitimate holder has long since moved
	// on to the successor.
	peerRefreshGrace = 30 * time.Second
)

// peerAccessToken is one short-lived credential. Family ties it to the refresh
// chain that produced it, which is the unit reuse detection revokes.
type peerAccessToken struct {
	Token   string    `json:"token"`
	GrantID string    `json:"grant_id"`
	Family  string    `json:"family"`
	Issued  time.Time `json:"issued"`
	Expires time.Time `json:"expires"`
}

// peerRefreshToken is one link in a rotation chain.
//
// Consumed tokens are KEPT until their original expiry rather than deleted.
// Deleting them would turn the reuse of a stolen token into an ordinary
// "unrecognized token" 401 — the same answer a typo gets — and the whole
// detection property depends on the server still recognizing what it retired.
type peerRefreshToken struct {
	Token      string    `json:"token"`
	GrantID    string    `json:"grant_id"`
	Family     string    `json:"family"`
	Issued     time.Time `json:"issued"`
	Expires    time.Time `json:"expires"`
	Consumed   bool      `json:"consumed,omitempty"`
	ConsumedAt time.Time `json:"consumed_at,omitempty"`
	// NextAccess and NextRefresh are what this token rotated into, retained so
	// a presentation inside the grace window can be answered with the SAME pair
	// instead of minting another. A retry that produced a second family would
	// leave the caller holding one chain while the server tracked two.
	NextAccess  string `json:"next_access,omitempty"`
	NextRefresh string `json:"next_refresh,omitempty"`
}

// peerTokenMu serializes the exchange. The read-modify-write on a refresh
// token — check consumed, mint the successor, mark it — is the whole of reuse
// detection, and two concurrent refreshes interleaving through it would let
// both mint a family and neither observe a collision.
var peerTokenMu sync.Mutex

// peerTokenSecret mints a token secret with the same entropy as a peer key.
func peerTokenSecret() string { return strings.ReplaceAll(UUIDv4()+UUIDv4(), "-", "") }

// --- storage -----------------------------------------------------------------

func getPeerAccessToken(secret string) (peerAccessToken, bool) {
	var t peerAccessToken
	if RootDB == nil || strings.TrimSpace(secret) == "" {
		return t, false
	}
	if !RootDB.Get(peerAccessTable, secret, &t) {
		return t, false
	}
	return t, true
}

func getPeerRefreshToken(secret string) (peerRefreshToken, bool) {
	var t peerRefreshToken
	if RootDB == nil || strings.TrimSpace(secret) == "" {
		return t, false
	}
	if !RootDB.Get(peerRefreshTable, secret, &t) {
		return t, false
	}
	return t, true
}

// revokePeerTokenFamily removes every token in one chain.
//
// Used both for reuse detection and for forgetting a grant, because a token
// outliving the grant it was issued against is a credential nobody can see in
// the admin table.
func revokePeerTokenFamily(family string) int {
	if RootDB == nil || strings.TrimSpace(family) == "" {
		return 0
	}
	n := 0
	for _, secret := range RootDB.Keys(peerAccessTable) {
		var t peerAccessToken
		if RootDB.Get(peerAccessTable, secret, &t) && t.Family == family {
			RootDB.Unset(peerAccessTable, secret)
			n++
		}
	}
	for _, secret := range RootDB.Keys(peerRefreshTable) {
		var t peerRefreshToken
		if RootDB.Get(peerRefreshTable, secret, &t) && t.Family == family {
			RootDB.Unset(peerRefreshTable, secret)
			n++
		}
	}
	return n
}

// RevokePeerGrantTokens drops every token issued against a grant. Called when a
// key is deleted or disabled so revocation reaches credentials already handed
// out, not just the pairing code.
func RevokePeerGrantTokens(grantID string) int {
	if RootDB == nil || strings.TrimSpace(grantID) == "" {
		return 0
	}
	n := 0
	for _, secret := range RootDB.Keys(peerAccessTable) {
		var t peerAccessToken
		if RootDB.Get(peerAccessTable, secret, &t) && t.GrantID == grantID {
			RootDB.Unset(peerAccessTable, secret)
			n++
		}
	}
	for _, secret := range RootDB.Keys(peerRefreshTable) {
		var t peerRefreshToken
		if RootDB.Get(peerRefreshTable, secret, &t) && t.GrantID == grantID {
			RootDB.Unset(peerRefreshTable, secret)
			n++
		}
	}
	return n
}

// sweepPeerTokens drops tokens past their expiry.
//
// Run on mint rather than on a timer: the table only grows when something is
// issued, so that is the moment the cleanup is owed, and it keeps this file
// free of a goroutine whose failure mode is silent. Consumed refresh tokens are
// swept only once genuinely expired — see peerRefreshToken.
func sweepPeerTokens() {
	if RootDB == nil {
		return
	}
	now := time.Now()
	for _, secret := range RootDB.Keys(peerAccessTable) {
		var t peerAccessToken
		if RootDB.Get(peerAccessTable, secret, &t) && now.After(t.Expires) {
			RootDB.Unset(peerAccessTable, secret)
		}
	}
	for _, secret := range RootDB.Keys(peerRefreshTable) {
		var t peerRefreshToken
		if RootDB.Get(peerRefreshTable, secret, &t) && now.After(t.Expires) {
			RootDB.Unset(peerRefreshTable, secret)
		}
	}
}

// --- minting -----------------------------------------------------------------

// PeerTokenInfo is the manifest's description of credential exchange.
type PeerTokenInfo struct {
	Path string `json:"path"`
	// Required distinguishes "you MUST exchange" from "you may". False means
	// the calling key still works as a bearer and the endpoint is available
	// anyway, which is what lets a consuming instance adopt the flow first and
	// the operator flip the switch second — rather than both at once, which is
	// the ordering that strands a peer.
	Required bool `json:"required"`
	// ExpiresIn is the access-token lifetime in seconds, so the caller can
	// schedule renewal without waiting for a 401 to tell it.
	ExpiresIn int `json:"expires_in"`
}

// peerTokenPair is what an exchange returns.
type peerTokenPair struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

// mintPeerTokenPair issues an access + refresh pair in one family. Caller holds
// peerTokenMu.
func mintPeerTokenPair(grantID, family string) (peerTokenPair, error) {
	if RootDB == nil {
		return peerTokenPair{}, fmt.Errorf("no database available")
	}
	sweepPeerTokens()
	now := time.Now()
	access := peerAccessToken{
		Token: peerTokenSecret(), GrantID: grantID, Family: family,
		Issued: now, Expires: now.Add(peerAccessTTL),
	}
	refresh := peerRefreshToken{
		Token: peerTokenSecret(), GrantID: grantID, Family: family,
		Issued: now, Expires: now.Add(peerRefreshTTL),
	}
	RootDB.Set(peerAccessTable, access.Token, access)
	RootDB.Set(peerRefreshTable, refresh.Token, refresh)
	return peerTokenPair{
		AccessToken:  access.Token,
		TokenType:    "Bearer",
		ExpiresIn:    int(peerAccessTTL / time.Second),
		RefreshToken: refresh.Token,
	}, nil
}

// --- the endpoint ------------------------------------------------------------

// peerTokenRequest is the exchange body. Shaped like an OAuth token request
// without being one: there is no user to consent, no authorization code and no
// client registry, so the two grant types below are the entire protocol.
type peerTokenRequest struct {
	GrantType    string `json:"grant_type"`
	PairingCode  string `json:"pairing_code,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

// HandlePeerToken serves POST /api/peer/v1/token.
//
// Deliberately NOT behind peerAuthorize: the credential is in the BODY, and the
// capability check every other handler runs has nothing to say about an
// exchange. It still charges the per-source failure throttle by hand, because
// this endpoint reads the whole key table on a pairing attempt and is the one
// place an attacker can guess at without holding anything.
func HandlePeerToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		peerDeny(w, http.StatusMethodNotAllowed, "POST a {grant_type} body")
		return
	}
	if peerSourceThrottled(r) {
		w.Header().Set("Retry-After", "60")
		peerDeny(w, http.StatusTooManyRequests, "too many failed authentication attempts from this address")
		return
	}
	var req peerTokenRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		peerDeny(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}

	peerTokenMu.Lock()
	defer peerTokenMu.Unlock()

	var (
		k  PeerKey
		ok bool
	)
	switch strings.TrimSpace(req.GrantType) {
	case "pairing_code":
		k, ok = peerExchangePairingCode(w, r, strings.TrimSpace(req.PairingCode))
	case "refresh_token":
		k, ok = peerExchangeRefreshToken(w, r, strings.TrimSpace(req.RefreshToken))
	default:
		peerDeny(w, http.StatusBadRequest,
			`grant_type must be "pairing_code" (first contact, using the key from the serving instance) `+
				`or "refresh_token" (every time after)`)
		return
	}
	// Counted here rather than in the helpers so the bookkeeping lives with
	// the handler, which is where the sweep in cost_attribution_test.go looks
	// and where it belongs: an exchange is this key being alive, and a peer
	// that refreshes for a month without calling anything should not read as
	// unused in the admin table.
	if ok {
		touchPeerKey(k)
	}
}

// peerExchangePairingCode turns a peer key into a first token pair. The one and
// only way in, and the code is spent here.
func peerExchangePairingCode(w http.ResponseWriter, r *http.Request, code string) (PeerKey, bool) {
	if code == "" {
		peerDeny(w, http.StatusBadRequest, "pairing_code is required for this grant type")
		return PeerKey{}, false
	}
	k, ok := LookupPeerKey(code)
	if !ok {
		peerNoteAuthFailure(r)
		peerDeny(w, http.StatusUnauthorized, "unrecognized or disabled peer key")
		return PeerKey{}, false
	}
	// A spent pairing code is refused rather than re-honored. The whole point of
	// single use is that a copy of the code taken from a chat log or a
	// screenshot is worth nothing after the peer it was meant for has paired;
	// honoring it twice would leave the long-lived secret this replaces.
	if strings.TrimSpace(k.Paired) != "" {
		peerNoteAuthFailure(r)
		peerDeny(w, http.StatusUnauthorized,
			"this pairing code has already been exchanged — it is single use. "+
				"Re-issue the key from the serving instance's admin page to get a new one")
		return PeerKey{}, false
	}
	pair, err := mintPeerTokenPair(k.ID, UUIDv4())
	if err != nil {
		peerDeny(w, http.StatusInternalServerError, err.Error())
		return PeerKey{}, false
	}
	markPeerKeyPaired(k.ID)
	Log("[peer] %s paired and took a token family", k.Label)
	writeJSON(w, pair)
	return k, true
}

// peerExchangeRefreshToken rotates a family forward, or reports a theft.
func peerExchangeRefreshToken(w http.ResponseWriter, r *http.Request, secret string) (PeerKey, bool) {
	if secret == "" {
		peerDeny(w, http.StatusBadRequest, "refresh_token is required for this grant type")
		return PeerKey{}, false
	}
	t, ok := getPeerRefreshToken(secret)
	if !ok {
		peerNoteAuthFailure(r)
		peerDeny(w, http.StatusUnauthorized, "unrecognized refresh token")
		return PeerKey{}, false
	}
	if time.Now().After(t.Expires) {
		RootDB.Unset(peerRefreshTable, secret)
		peerDeny(w, http.StatusUnauthorized,
			"this refresh token has expired — re-pair from the serving instance's admin page")
		return PeerKey{}, false
	}
	// The grant must still exist and still be enabled. Checked on every refresh
	// rather than only at pairing, so revoking a key stops the peer at its next
	// rotation instead of whenever its last access token happens to run out.
	k, ok := peerGrantByID(t.GrantID)
	if !ok || k.Disabled {
		revokePeerTokenFamily(t.Family)
		peerDeny(w, http.StatusUnauthorized, "the grant behind this token has been revoked")
		return PeerKey{}, false
	}

	if t.Consumed {
		// Inside the grace window this is a retry: answer with the successor
		// this token already produced, so a caller that lost the response ends
		// up on the same chain the server is tracking.
		if time.Since(t.ConsumedAt) <= peerRefreshGrace {
			if replay, ok := peerReplaySuccessor(t); ok {
				Debug("[peer] %s replayed a refresh inside the grace window", k.Label)
				writeJSON(w, replay)
				return k, true
			}
		}
		// Outside it, someone is holding a token the legitimate peer retired.
		// The entire family goes, and so does the grant: a peer whose refresh
		// token leaked is a peer whose next request cannot be trusted, and
		// leaving it able to re-pair with the code would defeat the detection.
		n := revokePeerTokenFamily(t.Family)
		SetPeerKeyDisabled(k.ID, true)
		Log("[peer] SECURITY: %s presented a refresh token consumed %s ago — "+
			"revoked %d token(s) and disabled the grant. Either the token leaked, or this peer "+
			"restored an old copy of its state; re-pair from the admin page to resume",
			k.Label, time.Since(t.ConsumedAt).Round(time.Second), n)
		peerNoteAuthFailure(r)
		peerDeny(w, http.StatusUnauthorized,
			"this refresh token was already used — the grant has been disabled as a precaution "+
				"and must be re-paired from the serving instance's admin page")
		return PeerKey{}, false
	}

	pair, err := mintPeerTokenPair(t.GrantID, t.Family)
	if err != nil {
		peerDeny(w, http.StatusInternalServerError, err.Error())
		return PeerKey{}, false
	}
	t.Consumed, t.ConsumedAt = true, time.Now()
	t.NextAccess, t.NextRefresh = pair.AccessToken, pair.RefreshToken
	RootDB.Set(peerRefreshTable, secret, t)
	writeJSON(w, pair)
	return k, true
}

// peerReplaySuccessor rebuilds the pair a consumed token produced, with the
// access token's REMAINING life rather than a fresh TTL — reporting a full
// window for a token minted 20 seconds ago would have the caller renew late.
func peerReplaySuccessor(t peerRefreshToken) (peerTokenPair, bool) {
	access, ok := getPeerAccessToken(t.NextAccess)
	if !ok || t.NextRefresh == "" {
		return peerTokenPair{}, false
	}
	if _, ok := getPeerRefreshToken(t.NextRefresh); !ok {
		return peerTokenPair{}, false
	}
	remaining := int(time.Until(access.Expires) / time.Second)
	if remaining < 0 {
		remaining = 0
	}
	return peerTokenPair{
		AccessToken:  t.NextAccess,
		TokenType:    "Bearer",
		ExpiresIn:    remaining,
		RefreshToken: t.NextRefresh,
	}, true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// --- grant lookup ------------------------------------------------------------

// peerGrantByID reads a grant record by its id.
func peerGrantByID(id string) (PeerKey, bool) {
	var pk PeerKey
	if RootDB == nil || strings.TrimSpace(id) == "" {
		return pk, false
	}
	if !RootDB.Get(peerKeysTable, id, &pk) {
		return pk, false
	}
	return pk, true
}

// markPeerKeyPaired stamps a rotating grant's code as spent.
func markPeerKeyPaired(id string) {
	pk, ok := peerGrantByID(id)
	if !ok {
		return
	}
	pk.Paired = time.Now().Format(time.RFC3339)
	RootDB.Set(peerKeysTable, id, pk)
}

// peerKeyFromAccessToken resolves a presented access token to its grant.
//
// Expiry is enforced HERE rather than by a sweep, because a sweep that has not
// run yet must not be the difference between a token being valid and not.
func peerKeyFromAccessToken(secret string) (PeerKey, bool) {
	t, ok := getPeerAccessToken(secret)
	if !ok {
		return PeerKey{}, false
	}
	if time.Now().After(t.Expires) {
		RootDB.Unset(peerAccessTable, secret)
		return PeerKey{}, false
	}
	k, ok := peerGrantByID(t.GrantID)
	if !ok || k.Disabled {
		return PeerKey{}, false
	}
	return k, true
}

// RepairPeerKey issues a fresh pairing code for a grant, keeping its
// capabilities, owner and appliance scope.
//
// The replacement for the admin page's re-display affordance, which a rotating
// grant cannot offer: once the code is spent there is nothing current to show,
// and the operator's real need — "get this peer connected again" — is a new
// code rather than a look at the old one. Existing tokens are revoked with it,
// because leaving the previous chain alive would mean two credentials for one
// grant and no way to tell which machine holds which.
func RepairPeerKey(id string) (PeerKey, error) {
	if RootDB == nil {
		return PeerKey{}, fmt.Errorf("no database available")
	}
	pk, ok := peerGrantByID(id)
	if !ok {
		return PeerKey{}, fmt.Errorf("no peer key with id %q", id)
	}
	n := RevokePeerGrantTokens(id)
	pk.Key = strings.ReplaceAll(UUIDv4()+UUIDv4(), "-", "")
	pk.Paired = ""
	pk.Disabled = false
	RootDB.Set(peerKeysTable, id, pk)
	Log("[peer] re-paired key %q — new pairing code issued, %d old token(s) revoked", pk.Label, n)
	return pk, nil
}
