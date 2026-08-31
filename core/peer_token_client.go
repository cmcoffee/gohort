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

// InvalidatePeerAccessToken drops the access token a peer has REFUSED, keeping
// the refresh token so the next exchange can recover without re-pairing.
//
// This is the state nothing else could reach. EnsurePeerToken returns early
// while the local clock says the access token is still inside its life, which
// is right for expiry and wrong for every other way a token dies: the far side
// restarting and losing its table, reuse detection deleting the family, an
// operator revoking the grant. In all of those this side goes on presenting a
// credential it believes in, the peer answers "unrecognized or disabled peer
// key" to every call, and nothing reconciles the disagreement — the renewal
// that would fix it is never attempted, because by our clock there is nothing
// to renew. Before rotation there was no such state: a static key either worked
// or it did not.
//
// So a refusal is the signal to stop believing the clock. Only the ACCESS half
// is dropped: clearPeerTokens would take the refresh token with it, and that is
// the one thing that can re-establish the link without an operator pasting a
// new pairing code.
func InvalidatePeerAccessToken(name string) {
	key := strings.ToLower(strings.TrimSpace(name))
	t := loadPeerTokens(key)
	if t.Access == "" {
		return
	}
	t.Access, t.Expires = "", time.Time{}
	storePeerTokens(key, t)
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

// PeerCredentialNow is PeerCredential for a caller that is ABOUT TO SEND — it
// waits for the exchange instead of answering with something the far side will
// refuse.
//
// PeerCredential above must never block: it sits under GetTranscribeConfig and
// LoadWebSearchConfig, which are called to answer questions as small as "is
// transcription enabled", and a page render cannot wait on another machine. So
// with no usable token it schedules the work and hands back the static key.
//
// That fallback used to be right, and stopped being. It was written when a key
// was ALSO a credential, so the fallback usually worked; the serving side now
// advertises exchange as required and accepts access tokens only, so presenting
// the key is a request that cannot succeed. What that costs is a whole turn:
// the first message after a peer has been idle past its token's life goes out
// on the key, earns a 401, and the background renewal it kicked off finishes
// milliseconds later — the peer was fine, it needed three hundred milliseconds
// and was never asked for them. Observed exactly that way, a 401 and a
// successful renewal in the same second.
//
// So: block here, where a round trip is already happening and the wait is
// invisible against it. Still no error return — a caller with a request in hand
// has nothing better to do with one than send and find out, and the static key
// remains the last resort for a peer whose serving side never turned rotation
// on.
func PeerCredentialNow(ctx context.Context, p RemotePeer) string {
	if !p.UseTokens {
		return strings.TrimSpace(p.Key)
	}
	t := loadPeerTokens(p.Name)
	if t.Access != "" && time.Now().Before(t.Expires) {
		// Inside the renewal lead the CURRENT token is still good, so hand it
		// over and let the background pass replace it — exactly as
		// PeerCredential does. Waiting here would add a round trip to a request
		// that already has a working credential.
		if time.Until(t.Expires) < peerRenewLead {
			schedulePeerRenewal(p.Name)
		}
		return t.Access
	}
	release := lockPeerCredential(p.Name)
	defer release()
	// Re-read under the lock: a renewal that was already in flight when we
	// arrived has landed by now, and exchanging again would spend a refresh
	// token for nothing.
	if t := loadPeerTokens(p.Name); t.Access != "" && time.Now().Before(t.Expires) {
		return t.Access
	}
	if err := EnsurePeerToken(ctx, p); err != nil {
		Debug("[peer] %q could not be given a credential before this request: %v", p.Name, err)
		return strings.TrimSpace(p.Key)
	}
	if t := loadPeerTokens(p.Name); t.Access != "" {
		return t.Access
	}
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

// --- the transport ------------------------------------------------------------
//
// Everything above holds a credential. This spends it, and it is the ONLY place
// that does.
//
// Before this, every capability that borrowed something from a peer carried its
// own copy of the same three steps: resolve a credential, set a header, send.
// Eight call sites — embeddings, transcription, web search, browse, three in
// investigate, the manifest probe — each with its own http.Client and its own
// idea of which credential to present. That duplication is not a tidiness
// complaint. It meant a fix to how a peer is authenticated had to be made eight
// times to be true, and the credential-refused recovery below had in fact been
// made in ONE of them, so the rest went on failing in a way that looked like the
// peer being broken.
//
// A peer relationship is established ONCE, at pairing. After that, sending
// something to a peer should be sending something to a peer — the credential is
// the transport's business, not the caller's. So callers build ordinary
// requests, and this owns:
//
//   - Resolving the credential at SEND time, blocking for an exchange if there
//     is no live token. Resolved from the peer NAME on every request, never
//     snapshotted, so a config saved months ago cannot carry a stale secret.
//   - Repairing a refused one. A 401 is the only evidence available that a token
//     this side believes in has died on the far side; the transport drops it,
//     exchanges a new one, and replays the request once.
//
// The first was missing from most callers. The second was missing from all.

// peerTransport authenticates and repairs every request to one peer.
//
// It holds the peer's NAME, never its credential. That is the point: a stored
// credential is what goes stale, and a name is what stays true.
//
// held is the record a caller already had in hand. Most callers reach a peer
// BECAUSE they were given one — investigate and browse are handed a RemotePeer
// — and requiring the store to agree would break them the moment a caller works
// with a peer that is not (or not yet) written down. The store still wins when
// it has the peer, since a record read now is fresher than one passed in a
// while ago; held is what makes a caller-supplied peer work at all.
type peerTransport struct {
	name string
	held RemotePeer
	base http.RoundTripper
}

// peer returns the record to authenticate against: the stored one when there is
// one, else whatever the caller was already holding.
func (t *peerTransport) peer() (RemotePeer, bool) {
	if p, ok := lookupPeerCached(t.name); ok {
		return p, true
	}
	if strings.TrimSpace(t.held.BaseURL) != "" || strings.TrimSpace(t.held.Key) != "" {
		return t.held, true
	}
	return RemotePeer{}, false
}

func (t *peerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	p, ok := t.peer()
	if !ok {
		// Say so. This is one of three ways a request can leave here WITHOUT a
		// live credential, and all three produce the same bare 401 from the far
		// side: the config names no peer so this transport was never installed,
		// the peer record cannot be found so we are here but powerless, or the
		// record says tokens are not in use so a pairing code goes out instead.
		// Reaching the transport is not the guarantee it reads as, and an
		// operator who knows the request is "on the transport" has no way to
		// learn which of the three happened.
		warnPeerResolveOnce("transport:"+t.name, fmt.Sprintf(
			"a request is being sent to peer %q but no such peer is registered here, so it goes out with "+
				"whatever credential the caller had — which the far side will refuse. The config still "+
				"names that peer; the peer record is what is missing. Re-add it under Admin > Peers, or "+
				"repoint the setting that references it.",
			t.name))
		// The peer was forgotten out from under a live config. Send it as
		// written rather than inventing a credential — the caller's own error
		// will name the endpoint, which is the useful thing to see.
		return base.RoundTrip(req)
	}

	// Found it, so any standing "no such peer" warning is stale.
	warnPeerResolveOnce("transport:"+t.name, "")
	first := req.Clone(req.Context())
	setPeerAuth(first, PeerCredentialNow(req.Context(), p))
	resp, err := base.RoundTrip(first)
	if err != nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}

	// Refused. Everything below is the recovery that used to not exist.
	cred, renewed := renewRefusedPeerCredential(req, p)
	if !renewed {
		return resp, nil
	}
	retry, rerr := replayPeerRequest(req)
	if rerr != nil {
		return resp, nil
	}
	// The first response is being abandoned, so its body has to be released or
	// the connection is never reused.
	drainAndClose(resp)
	setPeerAuth(retry, cred)
	return base.RoundTrip(retry)
}

// setPeerAuth attaches a peer credential, overwriting whatever the caller put
// there. Callers that build an OpenAI-shaped request set their own
// Authorization from a config field, and that field is exactly the stale copy
// this transport exists to stop trusting.
func setPeerAuth(req *http.Request, cred string) {
	cred = strings.TrimSpace(cred)
	if cred == "" {
		return
	}
	req.Header.Set("Authorization", "Bearer "+cred)
	req.Header.Set(peerKeyHeader, cred)
}

// renewRefusedPeerCredential reacts to a 401 by dropping the access token the
// peer refused and exchanging a new one. It reports false unless it came back
// with something genuinely different that is not the static key: retrying with
// the same credential wastes a round trip, and retrying with the pairing code
// earns a second, differently-worded 401 that would replace an accurate error
// with a confusing one.
func renewRefusedPeerCredential(req *http.Request, p RemotePeer) (string, bool) {
	if !p.UseTokens {
		// The silent case, and the one worth a line.
		//
		// Three separate places decline to act on this and none of them said
		// so: PeerCredentialNow hands back the static key without comment
		// because the record says tokens are not in use, the far side refuses
		// it, and recovery declines here for the same reason. What an operator
		// sees is a 401 from another machine, on every peer-backed capability
		// at once, with nothing anywhere naming the cause — the failure is
		// entirely in the DISAGREEMENT between two records, and neither of them
		// is wrong on its own.
		//
		// Once per peer, and cleared when the peer adopts tokens, because this
		// fires on every request of a bulk ingest — seven 401s in one second is
		// what prompted writing it, and seven copies of the explanation would
		// have buried the ingest log it appeared in.
		warnPeerResolveOnce("tokens:"+p.Name, fmt.Sprintf(
			"peer %q refused our credential, and this instance is still sending it the static pairing key: "+
				"its local record does not say that peer requires credential exchange. "+
				"Press Re-check on it under Admin > Peers. If the key is still good that adopts the token "+
				"flow and everything recovers on its own; if it fails, the Status column then carries that "+
				"peer's OWN words, which are the only thing that can tell a spent pairing code from a "+
				"disabled one. Neither can be repaired from this side — a code is single use — so that case "+
				"ends at re-issuing it there and pasting the new one with Update key. Until then every "+
				"peer-backed capability (embeddings, search, transcription, images) answers 401.",
			p.Name))
		return "", false
	}
	// Recovered, or at least trying: clear any standing warning so a peer that
	// has since adopted tokens stops being reported as broken.
	warnPeerResolveOnce("tokens:"+p.Name, "")
	InvalidatePeerAccessToken(p.Name)
	InvalidatePeerResolution()
	if fresh, ok := GetRemotePeer(p.Name); ok {
		p = fresh
	}
	cred := PeerCredentialNow(req.Context(), p)
	if cred == "" || cred == strings.TrimSpace(p.Key) {
		return "", false
	}
	Log("[peer] %q refused our credential — exchanged a new one and retrying", p.Name)
	return cred, true
}

// replayPeerRequest rebuilds a request for the retry.
//
// A body is a stream and the first attempt consumed it, so a request without
// GetBody cannot be replayed. http.NewRequest sets GetBody for every in-memory
// body (bytes.Reader, bytes.Buffer, strings.Reader), which is every peer
// request we make; anything else keeps the original 401 rather than silently
// sending an empty body.
func replayPeerRequest(req *http.Request) (*http.Request, error) {
	retry := req.Clone(req.Context())
	if req.Body == nil || req.Body == http.NoBody {
		return retry, nil
	}
	if req.GetBody == nil {
		return nil, errPeerBodyNotReplayable
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	retry.Body = body
	return retry, nil
}

// errPeerBodyNotReplayable marks a request whose body cannot be sent twice.
const errPeerBodyNotReplayable = Error("this request's body cannot be replayed")

// drainAndClose releases an abandoned response so its connection is reused.
func drainAndClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_ = resp.Body.Close()
}

// PeerHTTPClient returns a client whose requests to the named peer authenticate
// themselves and recover from a refused credential. Callers build ordinary
// requests and never touch a key.
func PeerHTTPClient(peerName string, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: &peerTransport{name: strings.ToLower(strings.TrimSpace(peerName))},
	}
}

// PeerClientFor is PeerHTTPClient for a caller that already holds the peer
// record — investigate, browse, anything handed a RemotePeer. The record is
// used when the store has nothing to say, so a peer that is not written down
// still authenticates instead of silently going out bare.
func PeerClientFor(p RemotePeer, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: &peerTransport{name: strings.ToLower(strings.TrimSpace(p.Name)), held: p},
	}
}

// PeerClientForProvider is PeerHTTPClient for the capabilities configured by a
// PROVIDER string rather than by a peer — embeddings, transcription, web search.
// Those point at a peer sometimes and at a local or third-party endpoint the
// rest of the time, and a plain client is the right answer for the latter.
//
// This is what lets those callers stop branching on "is this a peer": they ask
// for a client and send. When the provider names a peer they get credential
// resolution and 401 recovery; when it does not they get net/http.
func PeerClientForProvider(provider string, timeout time.Duration) *http.Client {
	provider = strings.TrimSpace(provider)
	if !strings.HasPrefix(provider, peerProviderPrefix) {
		return &http.Client{Timeout: timeout}
	}
	return PeerHTTPClient(strings.TrimPrefix(provider, peerProviderPrefix), timeout)
}
