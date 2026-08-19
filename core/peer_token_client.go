// Rotating peer credentials, consuming half.
//
// The serving side (peer_token.go) will hand out a short-lived access token and
// a refresh token that rotates on every use. This is the machinery that holds
// one, spends it, and renews it before it runs out.
//
// TWO PROPERTIES SHAPE EVERYTHING HERE.
//
// The static key is never discarded. It stays on the record and remains the
// fallback whenever there is no usable token — at first contact, after a
// restart with an expired pair, or when the token endpoint is unreachable. A
// peer whose serving side has not switched on rotation therefore behaves
// exactly as it did, and one that HAS keeps working through any failure the
// token flow can have short of the grant itself being revoked.
//
// Resolving a credential never blocks on the network. PeerCredential sits under
// GetTranscribeConfig and LoadWebSearchConfig, which are called to answer
// questions as small as "is transcription enabled" — putting an HTTP round trip
// there would make a page render wait on another machine. It returns what it
// has and schedules the renewal in the background; the synchronous exchange
// happens where a round trip is already expected, at probe and refresh time.
package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// peerRenewLead is how long before expiry a token is treated as spent.
	//
	// Renewing early is the whole defense against the stampede that reuse
	// detection would read as a theft: a token replaced at 80% of its life is
	// replaced ONCE, by one background pass, while every caller still holds a
	// working credential. Waiting for a 401 means every in-flight capability
	// discovers the expiry at the same instant and races the same refresh.
	peerRenewLead = 3 * time.Minute

	// peerTokenExchangeTimeout bounds one exchange. Short: it is one POST to a
	// machine we are already talking to, and a slow answer must not hold the
	// single-flight lock long enough to matter.
	peerTokenExchangeTimeout = 20 * time.Second
)

// peerTokenFlight serializes exchanges per peer.
//
// One in-flight refresh per peer, and the rest wait for its result rather than
// starting their own. Without this the consuming side manufactures exactly the
// event the serving side treats as a stolen token: two goroutines presenting
// the same refresh token, one of them arriving after the other has consumed it.
// The grace window on the far side is the second line of defense; this is the
// first, and it is the one that should be doing the work.
// peerTokenFlight serializes every operation that spends or replaces a peer's
// credential, one peer at a time.
//
// A MUTEX rather than a sync.Once, and the difference is the bug it was written
// for. Once has join-the-one-already-running semantics, which is right for a
// renewal (a second one is redundant) and catastrophically wrong for a re-key,
// which must always run its own work. Worse, nothing serialized the two against
// each other at all: re-keying cleared the credential, a reader noticed and
// started a renewal, that renewal spent the operator's brand-new SINGLE-USE
// pairing code, and the re-key's own probe then found it already exchanged and
// reported that the far side "did not recognize that key".
//
// So: a re-key takes the lock and holds it across probe and exchange. A renewal
// TRIES the lock and gives up if it cannot have it — something else is already
// putting this peer right, and the renewal exists to fix neglect, not to race.
var peerTokenFlight = struct {
	mu sync.Mutex
	in map[string]*sync.Mutex
}{in: map[string]*sync.Mutex{}}

// peerOpMutex returns the per-peer lock, creating it on first use.
func peerOpMutex(name string) *sync.Mutex {
	key := strings.ToLower(strings.TrimSpace(name))
	peerTokenFlight.mu.Lock()
	defer peerTokenFlight.mu.Unlock()
	m, ok := peerTokenFlight.in[key]
	if !ok {
		m = new(sync.Mutex)
		peerTokenFlight.in[key] = m
	}
	return m
}

// lockPeerCredential blocks until this peer's credential is nobody else's
// business, and returns the release. For work that MUST happen: a re-key.
func lockPeerCredential(name string) func() {
	m := peerOpMutex(name)
	m.Lock()
	return m.Unlock
}

// tryLockPeerCredential is the same lock for work that is only worth doing if
// nothing else is doing it. Returns ok=false when the peer is busy.
func tryLockPeerCredential(name string) (func(), bool) {
	m := peerOpMutex(name)
	if !m.TryLock() {
		return func() {}, false
	}
	return m.Unlock, true
}

// peerTokenBackground counts renewals running detached from any caller.
//
// PeerCredential answers immediately and leaves the exchange running behind it,
// which means there is a moment where work is in flight that nothing is holding.
// In the process that is fine — the renewal either lands or logs. It is not fine
// for anything that tears down the state underneath it, so there is one way to
// wait for quiet: see waitPeerTokenIdle.
var peerTokenBackground sync.WaitGroup

// schedulePeerRenewal kicks off a detached renewal, tracked so it can be waited
// on rather than merely hoped about.
func schedulePeerRenewal(name string) {
	peerTokenBackground.Add(1)
	go func() {
		defer peerTokenBackground.Done()
		renewPeerTokenAsync(name)
	}()
}

// waitPeerTokenIdle blocks until no background renewal is running.
func waitPeerTokenIdle() { peerTokenBackground.Wait() }

// peerTokenState is the in-memory view of a peer's current credential.
//
// Cached alongside the stored record because PeerCredential is called on paths
// that run per request, and reading the peer out of the database to answer
// "which bearer" would put a store read in front of every borrowed call.
var peerTokenState = struct {
	mu sync.RWMutex
	by map[string]peerTokens
}{by: map[string]peerTokens{}}

type peerTokens struct {
	Access  string
	Refresh string
	Expires time.Time
}

// --- record fields -----------------------------------------------------------

// peerTokensFromRecord reads the stored credential onto the in-memory view.
func peerTokensFromRecord(p RemotePeer) peerTokens {
	t := peerTokens{Access: p.AccessToken, Refresh: p.RefreshToken}
	if s := strings.TrimSpace(p.AccessExpires); s != "" {
		if at, err := time.Parse(time.RFC3339, s); err == nil {
			t.Expires = at
		}
	}
	return t
}

// loadPeerTokens returns the cached credential, falling back to the stored one.
func loadPeerTokens(name string) peerTokens {
	key := strings.ToLower(strings.TrimSpace(name))
	peerTokenState.mu.RLock()
	t, hit := peerTokenState.by[key]
	peerTokenState.mu.RUnlock()
	if hit {
		return t
	}
	p, ok := lookupPeerCached(key)
	if !ok {
		return peerTokens{}
	}
	t = peerTokensFromRecord(p)
	peerTokenState.mu.Lock()
	peerTokenState.by[key] = t
	peerTokenState.mu.Unlock()
	return t
}

// storePeerTokens persists a rotated credential and republishes it everywhere a
// copy lives.
//
// THE CHOKEPOINT. Peer-backed image rendering does not resolve its key at read
// time the way embeddings, search, transcription and LLM tiers now do — the key
// is copied into a managed SecureCredential that the generated connectors name.
// A rotation that updated only the peer record would leave image generation
// holding a dead token while everything else kept working, which is the same
// class of bug as the save-time snapshots, just inverted. Every write of a peer
// credential goes through here so that cannot happen again.
func storePeerTokens(name string, t peerTokens) {
	key := strings.ToLower(strings.TrimSpace(name))
	peerTokenState.mu.Lock()
	peerTokenState.by[key] = t
	peerTokenState.mu.Unlock()

	if RootDB == nil {
		return
	}
	var p RemotePeer
	if !RootDB.Get(remotePeersTable, key, &p) {
		return
	}
	p.AccessToken, p.RefreshToken = t.Access, t.Refresh
	p.AccessExpires = ""
	if !t.Expires.IsZero() {
		p.AccessExpires = t.Expires.Format(time.RFC3339)
	}
	RootDB.Set(remotePeersTable, key, p)
	InvalidatePeerResolution()
	republishPeerImageCredential(p)
}

// resetPeerTokenCache drops every in-memory peer credential. The cache is keyed
// by peer name and lives as long as the process, so a caller that replaces the
// store beneath it (only tests do) has to clear it or a token minted against
// the old database is presented against the new one.
func resetPeerTokenCache() {
	peerTokenState.mu.Lock()
	peerTokenState.by = map[string]peerTokens{}
	peerTokenState.mu.Unlock()
}

// forgetPeerTokens purges the in-memory credential for a peer that no longer
// exists. Separate from clearPeerTokens, which writes through to a record —
// there is none left to write to.
func forgetPeerTokens(name string) {
	key := strings.ToLower(strings.TrimSpace(name))
	peerTokenState.mu.Lock()
	delete(peerTokenState.by, key)
	peerTokenState.mu.Unlock()
}

// clearPeerTokens drops a peer's credential, putting it back on the static key.
func clearPeerTokens(name string) {
	storePeerTokens(name, peerTokens{})
}

// --- credential resolution ---------------------------------------------------

// PeerCredential returns the bearer to present to this peer right now.
//
// Never blocks and never fails: a peer with no usable token gets its static
// key, which is what every peer had before rotation existed and what a peer
// whose serving side has not switched on still uses. When the token is inside
// its renewal lead, a background exchange is kicked off and the CURRENT token
// is returned — the caller's request goes out on a credential that is still
// valid rather than waiting for its replacement.
func PeerCredential(p RemotePeer) string {
	if !p.UseTokens {
		return strings.TrimSpace(p.Key)
	}
	t := loadPeerTokens(p.Name)
	if t.Access != "" && time.Now().Before(t.Expires) {
		if time.Until(t.Expires) < peerRenewLead {
			schedulePeerRenewal(p.Name)
		}
		return t.Access
	}
	// Nothing usable. Schedule the work and answer with the fallback, which
	// succeeds unless the serving side has made rotation mandatory — and in
	// that case the 401 it earns is the honest report that this peer needs
	// re-pairing, rather than a hang while we try to fix it inline.
	schedulePeerRenewal(p.Name)
	return strings.TrimSpace(p.Key)
}

// renewPeerTokenAsync runs one exchange in the background, at most one per peer.
func renewPeerTokenAsync(name string) {
	key := strings.ToLower(strings.TrimSpace(name))
	release, got := tryLockPeerCredential(key)
	if !got {
		// Somebody is already replacing this peer's credential — most likely an
		// operator part-way through a re-key, whose pairing code is single use
		// and would be spent by this. Renewal is for neglect; this is not that.
		return
	}
	defer release()
	func() {
		p, ok := GetRemotePeer(key)
		if !ok || !p.UseTokens {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), peerTokenExchangeTimeout)
		defer cancel()
		if err := EnsurePeerToken(ctx, p); err != nil {
			// Logged once per distinct problem: a peer that is simply down
			// would otherwise produce a line per renewal attempt.
			//
			// The old wording ended "still using its static key", which was
			// true while a key was also a credential and is now actively
			// misleading: exchange is mandatory, so falling back to the key
			// means falling back to something the far side refuses. An
			// authentication failure here is a peer that is DISCONNECTED, and
			// saying so is the difference between an operator who re-issues a
			// key and one who waits for a retry that cannot work.
			note := fmt.Sprintf("could not renew the credential for peer %q (%v)", key, err)
			if isPeerAuthFailure(err) {
				note += " — that peer is not connected until it is given a new key: " +
					"re-issue one under its Resource Sharing settings and paste it into Update key here"
			} else {
				note += " — retrying; the peer is unreachable rather than refusing"
			}
			warnPeerResolveOnce("token:"+key, note)
			return
		}
		warnPeerResolveOnce("token:"+key, "")
	}()
}

// --- exchange ----------------------------------------------------------------

// EnsurePeerToken obtains a usable access token for a peer, refreshing or
// pairing as needed. Synchronous: called where a round trip is already expected.
//
// Refresh is tried first and pairing is the fallback, not the other way round.
// On a grant the serving side has made rotating, the pairing code is SINGLE USE
// — spending it to recover from an ordinary expired-refresh would burn the one
// credential that can re-establish the link, and the operator would have to
// re-pair by hand for what was a transient failure.
func EnsurePeerToken(ctx context.Context, p RemotePeer) error {
	if !p.UseTokens {
		return nil
	}
	t := loadPeerTokens(p.Name)
	if t.Access != "" && time.Until(t.Expires) > peerRenewLead {
		return nil
	}
	if t.Refresh != "" {
		got, err := peerPostToken(ctx, p, map[string]string{
			"grant_type": "refresh_token", "refresh_token": t.Refresh,
		})
		if err == nil {
			storePeerTokens(p.Name, got)
			return nil
		}
		// A 401 is DEFINITIVE — the token will not become valid again, whatever
		// the far side's wording. Reuse detection deletes the family, a revoked
		// grant deletes it, and an expired one is gone too, so the message
		// varies while the meaning does not; matching on the text left the dead
		// pair in place and the next renewal looping on it. Cleared here, which
		// also puts the peer back on its static key if pairing then fails.
		if !isPeerAuthFailure(err) {
			// Anything else — unreachable, 5xx, a timeout — is transient. Keep
			// the pair and do NOT fall through to pairing: on a rotating grant
			// the code is single use, and spending it on a network blip burns
			// the one credential that can re-establish the link.
			return err
		}
		clearPeerTokens(p.Name)
	}
	got, err := peerPostToken(ctx, p, map[string]string{
		"grant_type": "pairing_code", "pairing_code": strings.TrimSpace(p.Key),
	})
	if err != nil {
		return err
	}
	storePeerTokens(p.Name, got)
	return nil
}

// peerTokenErr marks a refusal the caller must not retry through.
type peerTokenErr struct {
	status int
	msg    string
}

func (e peerTokenErr) Error() string { return e.msg }

// isPeerAuthFailure reports whether the far side refused the CREDENTIAL, as
// opposed to the exchange having a bad day. Status, not message: the reasons a
// refresh token stops being accepted all answer 401 and word it differently.
func isPeerAuthFailure(err error) bool {
	var e peerTokenErr
	if !errors.As(err, &e) {
		return false
	}
	return e.status == http.StatusUnauthorized || e.status == http.StatusForbidden
}

// peerPostToken performs one exchange.
func peerPostToken(ctx context.Context, p RemotePeer, body map[string]string) (peerTokens, error) {
	base := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	if base == "" {
		return peerTokens{}, fmt.Errorf("peer %q has no address", p.Name)
	}
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/peer/v1/token", bytes.NewReader(payload))
	if err != nil {
		return peerTokens{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: peerTokenExchangeTimeout}).Do(req)
	if err != nil {
		return peerTokens{}, fmt.Errorf("reaching peer %q: %w", p.Name, err)
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken  string `json:"access_token"`
		ExpiresIn    int    `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
		Error        string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(out.Error)
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return peerTokens{}, peerTokenErr{status: resp.StatusCode,
			msg: fmt.Sprintf("peer %q refused the exchange: %s", p.Name, msg)}
	}
	if out.AccessToken == "" {
		return peerTokens{}, fmt.Errorf("peer %q returned no access token", p.Name)
	}
	// Trusting expires_in rather than a timestamp keeps this immune to clock
	// skew between the two machines, which is the failure that would otherwise
	// show up as a token treated as expired the moment it arrives.
	ttl := time.Duration(out.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = peerAccessTTL
	}
	return peerTokens{
		Access:  out.AccessToken,
		Refresh: out.RefreshToken,
		Expires: time.Now().Add(ttl),
	}, nil
}

// --- image credential republication -----------------------------------------

// republishPeerImageCredential rewrites the managed credential the peer's image
// connectors name, so a rotated bearer reaches the one consumer that cannot
// resolve it at read time. No-op for a peer contributing no renderers.
func republishPeerImageCredential(p RemotePeer) {
	if len(p.ImageConnectors) == 0 || RootDB == nil {
		return
	}
	credName := peerCredentialName(p.Name)
	if exists, _, _ := Secure().CredentialStatus(credName); !exists {
		return
	}
	if err := Secure().Save(SecureCredential{
		Name:              credName,
		Type:              SecureCredHeader,
		ParamName:         peerKeyHeader,
		BaseURL:           p.BaseURL,
		AllowedURLPattern: imageHostPattern(p.BaseURL),
		Description:       "Resource-sharing key for peer " + p.Name + ". Created with the peer; removed when it is forgotten.",
		Secured:           true,
		Managed:           "peer",
	}, PeerCredential(p)); err != nil {
		Log("[peer] could not republish the image credential for %q: %v", p.Name, err)
	}
}
