// Inbound relay: a peer lending its PUBLIC ADDRESS. The serving half comes
// first; the consuming half (the peer_relay connector kind) follows it.
//
// Some instances can make outbound connections and accept none. Everything
// push-shaped is closed to them: a Teams bot (Microsoft POSTs every activity to
// the bot's endpoint and offers nothing to poll), a Graph change notification,
// a Slack or generic webhook. A peer that does have a public https address can
// lend it, and that is all this lends.
//
// The shape, and why it is the right one under "a peer key lends resources,
// never the instance":
//
//   - The consuming instance dials OUT to /api/peer/v1/relay with its peer
//     credential and holds a WebSocket open.
//   - This instance answers anything arriving at /api/peer/relay/<id>/<path> by
//     sending it down that socket as one frame and writing back the frame that
//     answers it.
//   - Nothing here reads the request. No app of this instance runs, nothing is
//     stored, no store is consulted, and the credential that authenticates the
//     webhook (Microsoft's signed token, a provider's HMAC) is checked on the
//     far side, by the routes that already check it. This side is a pipe with a
//     size limit, a rate limit and a timeout.
//
// What it guards against is what a public pipe could otherwise leak or cost:
// this instance's own session cookies and keys are stripped before forwarding
// (a browser that wanders onto a relay URL carries them), the peer's
// Set-Cookie is stripped on the way back (it would land on THIS origin), and
// the per-key rate limit is charged for every relayed request, so a flood at
// the public URL is bounded by the operator's own ceiling for that key.
package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// peerRelayConnectPath is where a consuming instance opens its socket.
	peerRelayConnectPath = "/api/peer/v1/relay"

	// peerRelayInboundPrefix is the public door. The relay id follows, then the
	// path on the far side: /api/peer/relay/<id>/bridges/api/bot/<name>.
	peerRelayInboundPrefix = "/api/peer/relay/"

	// peerRelayMaxBody caps a relayed body in either direction. Webhook payloads
	// are small; a Teams activity is a few kilobytes.
	peerRelayMaxBody = 1 << 20

	// peerRelayTimeout is how long a relayed request may wait for its answer.
	// Under the 15 seconds Bot Framework gives a bot and the 10 Graph gives a
	// validation handshake, so the caller hears a clean 504 from us rather than
	// timing out on its own and retrying into the same wait.
	peerRelayTimeout = 9 * time.Second

	// peerRelayMaxInFlight bounds requests waiting on one socket. Past it the
	// caller is told 503 and retries later, rather than queueing behind a peer
	// that has stopped answering.
	peerRelayMaxInFlight = 32

	// peerRelayPingInterval keeps the socket alive through proxies that drop
	// idle connections (nginx 60s, Cloudflare 100s), and is also how often a
	// connected key is re-checked against its grant.
	peerRelayPingInterval = 25 * time.Second

	// peerRelayReadDeadline drops a socket that has sent nothing, not even a
	// pong, for this long. Above the ping interval plus a slow round trip.
	peerRelayReadDeadline = 70 * time.Second

	// peerRelayWriteDeadline bounds one frame write.
	peerRelayWriteDeadline = 10 * time.Second
)

// peerRelayFrame is the one message shape on the socket, in both directions.
type peerRelayFrame struct {
	Type   string      `json:"type"` // hello | request | response
	ID     string      `json:"id,omitempty"`
	Method string      `json:"method,omitempty"`
	Path   string      `json:"path,omitempty"` // escaped path on the far side (request)
	Query  string      `json:"query,omitempty"`
	Header http.Header `json:"header,omitempty"`
	Body   []byte      `json:"body,omitempty"`
	Status int         `json:"status,omitempty"` // response
	Remote string      `json:"remote,omitempty"` // the caller's address as this side saw it (request)
	Relay  string      `json:"relay,omitempty"`  // hello: the public path prefix this socket answers
	Reason string      `json:"reason,omitempty"` // why a request could not be answered (response)
}

// PeerRelayInfo is the manifest entry for the relay capability.
type PeerRelayInfo struct {
	Connect string `json:"connect"` // where to open the socket
	Inbound string `json:"inbound"` // the public path prefix that reaches it
}

// peerRelayID is the public name of a key's relay. Derived rather than stored:
// it is stable across reconnects and restarts, needs no record, and does not
// put the key's own id in a URL that gets pasted into other companies' portals.
func peerRelayID(keyID string) string {
	sum := sha256.Sum256([]byte("gohort-peer-relay:" + keyID))
	return hex.EncodeToString(sum[:12])
}

// peerRelayInboundPath is the public path prefix for a key's relay.
func peerRelayInboundPath(keyID string) string {
	return strings.TrimSuffix(peerRelayInboundPrefix, "/") + "/" + peerRelayID(keyID)
}

// peerRelayDropHeaders never cross the relay, in either direction. Three
// groups: hop-by-hop headers that describe one connection, not the request;
// credentials that mean something to the instance they were sent TO (a session
// cookie for this origin, a deployment or peer key, the internal-call token);
// and forwarding headers, which the receiving side would otherwise read as its
// own proxy's word about who is calling.
var peerRelayDropHeaders = func() map[string]bool {
	m := map[string]bool{}
	for _, h := range []string{
		"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
		"Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
		"Host", "Content-Length",
		"Cookie", "Set-Cookie",
		peerKeyHeader, deploymentKeyHeader, internalAuthHeader, "X-API-Key",
		"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-IP",
	} {
		m[http.CanonicalHeaderKey(h)] = true
	}
	return m
}()

// peerRelayHeaders copies h without the headers that must not cross.
func peerRelayHeaders(h http.Header) http.Header {
	out := http.Header{}
	for k, v := range h {
		ck := http.CanonicalHeaderKey(k)
		if peerRelayDropHeaders[ck] || strings.HasPrefix(ck, "Sec-Websocket-") {
			continue
		}
		out[ck] = append([]string(nil), v...)
	}
	return out
}

// --- sessions ----------------------------------------------------------------

var (
	errPeerRelayBusy   = errors.New("too many requests waiting on this relay")
	errPeerRelayClosed = errors.New("the relay connection closed")
)

// peerRelaySession is one connected peer's socket.
type peerRelaySession struct {
	key     PeerKey // the grant it connected under, secret cleared
	relayID string
	conn    *websocket.Conn
	since   time.Time

	wmu sync.Mutex // gorilla allows one concurrent writer

	mu      sync.Mutex
	pending map[string]chan peerRelayFrame

	done      chan struct{}
	closeOnce sync.Once

	relayed atomic.Int64
}

var peerRelaySessions = struct {
	mu sync.Mutex
	by map[string]*peerRelaySession // relay id -> session
}{by: map[string]*peerRelaySession{}}

func lookupPeerRelay(relayID string) *peerRelaySession {
	peerRelaySessions.mu.Lock()
	defer peerRelaySessions.mu.Unlock()
	return peerRelaySessions.by[relayID]
}

// takePeerRelay removes and returns the session for a relay id.
func takePeerRelay(relayID string) *peerRelaySession {
	peerRelaySessions.mu.Lock()
	defer peerRelaySessions.mu.Unlock()
	s := peerRelaySessions.by[relayID]
	delete(peerRelaySessions.by, relayID)
	return s
}

func (s *peerRelaySession) register() {
	peerRelaySessions.mu.Lock()
	peerRelaySessions.by[s.relayID] = s
	peerRelaySessions.mu.Unlock()
}

// unregister removes s only if it is still the registered session, so a socket
// superseded by a newer one cannot take its replacement down with it.
func (s *peerRelaySession) unregister() {
	peerRelaySessions.mu.Lock()
	if peerRelaySessions.by[s.relayID] == s {
		delete(peerRelaySessions.by, s.relayID)
	}
	peerRelaySessions.mu.Unlock()
}

func (s *peerRelaySession) write(f peerRelayFrame) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.conn.SetWriteDeadline(time.Now().Add(peerRelayWriteDeadline))
	return s.conn.WriteJSON(f)
}

// close ends the session once, telling the peer why.
func (s *peerRelaySession) close(reason string) {
	s.closeOnce.Do(func() {
		close(s.done)
		s.unregister()
		msg := websocket.FormatCloseMessage(websocket.CloseNormalClosure, reason)
		s.conn.WriteControl(websocket.CloseMessage, msg, time.Now().Add(time.Second))
		s.conn.Close()
	})
}

// roundTrip sends one request frame and waits for its answer.
func (s *peerRelaySession) roundTrip(ctx context.Context, f peerRelayFrame) (peerRelayFrame, error) {
	ch := make(chan peerRelayFrame, 1)
	s.mu.Lock()
	if len(s.pending) >= peerRelayMaxInFlight {
		s.mu.Unlock()
		return peerRelayFrame{}, errPeerRelayBusy
	}
	s.pending[f.ID] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, f.ID)
		s.mu.Unlock()
	}()
	if err := s.write(f); err != nil {
		return peerRelayFrame{}, errPeerRelayClosed
	}
	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		return peerRelayFrame{}, ctx.Err()
	case <-s.done:
		return peerRelayFrame{}, errPeerRelayClosed
	}
}

// readLoop delivers each response frame to the request waiting on it, until
// the socket ends.
func (s *peerRelaySession) readLoop() {
	// Base64 grows a body by a third; the rest is headers and framing.
	s.conn.SetReadLimit(peerRelayMaxBody*4/3 + 64<<10)
	s.conn.SetReadDeadline(time.Now().Add(peerRelayReadDeadline))
	s.conn.SetPongHandler(func(string) error {
		s.conn.SetReadDeadline(time.Now().Add(peerRelayReadDeadline))
		return nil
	})
	for {
		var f peerRelayFrame
		if err := s.conn.ReadJSON(&f); err != nil {
			return
		}
		s.conn.SetReadDeadline(time.Now().Add(peerRelayReadDeadline))
		if f.Type != "response" || f.ID == "" {
			continue
		}
		s.mu.Lock()
		ch := s.pending[f.ID]
		s.mu.Unlock()
		if ch != nil {
			select {
			case ch <- f:
			default: // a duplicate answer; the first one won
			}
		}
	}
}

// watch pings the peer and ends the session when the key stops granting the
// relay. Revocation by Disable arrives on ctx (cancelPeerLive); a narrowed
// grant or a deleted key does not, so it is re-read on every tick.
func (s *peerRelaySession) watch(ctx context.Context) {
	tick := time.NewTicker(peerRelayPingInterval)
	defer tick.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ctx.Done():
			s.close("the key was disabled")
			return
		case <-tick.C:
			if !peerKeyStillAllows(s.key.ID, PeerCapRelay) {
				s.close("the key no longer grants relay")
				return
			}
			if err := s.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(peerRelayWriteDeadline)); err != nil {
				s.close("ping failed")
				return
			}
		}
	}
}

// peerKeyStillAllows re-reads a key and reports whether it still grants a
// capability, for a grant held open longer than one request.
func peerKeyStillAllows(id, capability string) bool {
	if RootDB == nil || strings.TrimSpace(id) == "" {
		return false
	}
	var pk PeerKey
	if !RootDB.Get(peerKeysTable, id, &pk) {
		return false
	}
	return pk.Allows(capability) && peerKeyOwnerExists(pk.Owner)
}

// PeerRelayConnected reports whether a key has a relay socket open, and since
// when, for the admin table on this side.
func PeerRelayConnected(keyID string) (since time.Time, relayed int64, ok bool) {
	s := lookupPeerRelay(peerRelayID(keyID))
	if s == nil {
		return time.Time{}, 0, false
	}
	return s.since, s.relayed.Load(), true
}

// --- handlers ----------------------------------------------------------------

var peerRelayUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// A relay is opened by an instance, which sends no Origin. The handler
	// refuses one that does before getting here; this only has to not undo it.
	CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" },
}

// HandlePeerRelayConnect serves GET /api/peer/v1/relay: a consuming instance
// opening the socket its inbound traffic will arrive on.
//
// One socket per key. A newer connection supersedes an older one rather than
// being refused, because the older one is usually the same peer's previous
// connection that has not noticed it is dead yet, and refusing would leave the
// peer locked out until that one timed out.
func HandlePeerRelayConnect(w http.ResponseWriter, r *http.Request) {
	k, ok := peerAuthorize(w, r, PeerCapRelay)
	if !ok {
		return
	}
	if !websocket.IsWebSocketUpgrade(r) {
		peerDeny(w, http.StatusBadRequest, "the relay is a WebSocket: connect with an Upgrade request")
		return
	}
	if r.Header.Get("Origin") != "" {
		peerDeny(w, http.StatusForbidden, "a relay is opened by an instance, not from a browser page")
		return
	}
	relayID := peerRelayID(k.ID)
	if old := takePeerRelay(relayID); old != nil {
		old.close("superseded by a newer connection from the same key")
	}
	r, release, ok := peerHold(w, r, k, PeerCapRelay)
	if !ok {
		return
	}
	defer release()
	conn, err := peerRelayUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return // the upgrader has answered
	}
	k.Key = ""
	s := &peerRelaySession{
		key:     k,
		relayID: relayID,
		conn:    conn,
		since:   time.Now(),
		pending: map[string]chan peerRelayFrame{},
		done:    make(chan struct{}),
	}
	s.register()
	// Before the hello, so a peer that has read the hello has seen everything
	// this handler records about the connection.
	touchPeerKey(k)
	if err := s.write(peerRelayFrame{Type: "hello", Relay: peerRelayInboundPath(k.ID)}); err != nil {
		s.close("hello failed")
		return
	}
	Log("[peer] %q opened an inbound relay at %s", k.Label, peerRelayInboundPath(k.ID))
	go s.watch(r.Context())
	s.readLoop()
	s.close("connection ended")
	Log("[peer] %q inbound relay closed after %s, %d request(s) relayed",
		k.Label, time.Since(s.since).Round(time.Second), s.relayed.Load())
}

// HandlePeerRelayInbound serves /api/peer/relay/<id>/<path>: anything a third
// party sends to a peer's borrowed address. It is answered by the peer.
func HandlePeerRelayInbound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		peerDeny(w, http.StatusMethodNotAllowed, "a relay carries GET and POST only")
		return
	}
	rest := strings.TrimPrefix(r.URL.EscapedPath(), peerRelayInboundPrefix)
	relayID, tail, _ := strings.Cut(rest, "/")
	if relayID == "" || tail == "" {
		peerDeny(w, http.StatusNotFound, "a relay address is /api/peer/relay/<id>/<path on the peer>")
		return
	}
	s := lookupPeerRelay(relayID)
	if s == nil {
		// 503, not 404: the caller should retry, and the usual reason is a peer
		// reconnecting. An unknown id and an absent peer read the same, which
		// also means this answer reveals nothing about which ids exist.
		w.Header().Set("Retry-After", "30")
		peerDeny(w, http.StatusServiceUnavailable, "no instance is connected on this relay right now")
		return
	}
	k := s.key
	if !peerRateAllow(k) {
		w.Header().Set("Retry-After", peerRetryAfterHeader(peerRateRetryAfter(k)))
		peerDeny(w, http.StatusTooManyRequests, "rate limit reached for this relay")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, peerRelayMaxBody))
	if err != nil {
		peerDeny(w, http.StatusRequestEntityTooLarge, "the request body is larger than a relay carries")
		return
	}
	f := peerRelayFrame{
		Type:   "request",
		ID:     UUIDv4(),
		Method: r.Method,
		Path:   "/" + tail,
		Query:  r.URL.RawQuery,
		Header: peerRelayHeaders(r.Header),
		Body:   body,
		Remote: RequestSource(r),
	}
	ctx, cancel := context.WithTimeout(r.Context(), peerRelayTimeout)
	defer cancel()
	resp, err := s.roundTrip(ctx, f)
	switch {
	case errors.Is(err, errPeerRelayBusy):
		w.Header().Set("Retry-After", "5")
		peerDeny(w, http.StatusServiceUnavailable, err.Error())
		return
	case errors.Is(err, context.DeadlineExceeded):
		// No path: it is the peer's, and the access log keeps it out too.
		Debug("[peer] relay for %q: a %s got no answer in %s", k.Label, f.Method, peerRelayTimeout)
		peerDeny(w, http.StatusGatewayTimeout, "the connected instance did not answer in time")
		return
	case err != nil:
		w.Header().Set("Retry-After", "30")
		peerDeny(w, http.StatusBadGateway, "the connected instance went away before answering")
		return
	}
	s.relayed.Add(1)
	touchPeerKey(k)

	for hk, hv := range peerRelayHeaders(resp.Header) {
		w.Header()[hk] = hv
	}
	status := resp.Status
	if status < 100 || status > 999 {
		status = http.StatusOK
	}
	if len(resp.Body) > peerRelayMaxBody {
		peerDeny(w, http.StatusBadGateway, "the connected instance answered with more than a relay carries")
		return
	}
	if len(resp.Body) == 0 && resp.Reason != "" {
		peerDeny(w, status, resp.Reason)
		return
	}
	w.WriteHeader(status)
	w.Write(resp.Body)
}

// --- consuming half: the peer_relay connector kind ----------------------------
//
// Inbound webhooks for an instance that can only dial out, received at a
// peer's public address (the serving half above).
//
// A Teams bot, a Graph subscription, a Slack or generic webhook all need an
// address the provider can POST to. This kind opens a WebSocket OUT to a peer
// granting the relay capability, and every request that arrives at the peer's
// relay prefix comes down it and is answered here, by the route it names.
//
// What a relayed request may reach is the narrow part, and it is decided HERE,
// not by the peer. Only a route an app has registered as relayable is served,
// and only if it is also a public path: a route that authenticates the
// provider itself (Microsoft's signed token, a webhook HMAC). Everything else
// is refused before any handler sees it, so lending an address can never turn
// into the peer, or anyone posting to the peer, reaching this instance's pages
// or its admin API.
//
// A relayed request goes through the same middleware chain a direct one does
// (the handler ServeDashboard serves), so body limits, app availability and
// the access log apply unchanged, and the route cannot tell the two apart.
//
// Approval is MANDATORY (no ConnectorAutoApprover): it makes routes on this
// instance reachable from the internet through another machine.

// PeerRelayConnectorKind is the Kind value for a peer relay.
const PeerRelayConnectorKind = "peer_relay"

const (
	// peerRelayDispatchTimeout bounds one relayed request on this side. The
	// serving side gives up sooner (peerRelayTimeout); this only stops a
	// handler that never returns from holding a slot forever.
	peerRelayDispatchTimeout = 30 * time.Second

	// peerRelayClientInFlight bounds relayed requests running at once here.
	peerRelayClientInFlight = 16

	peerRelayBackoffMin = 2 * time.Second
	peerRelayBackoffMax = time.Minute
)

// PeerRelaySpec is the Spec payload for a peer_relay connector.
type PeerRelaySpec struct {
	// Peer is a registered remote peer (Admin > Capabilities > Peers) whose key
	// grants relay. Its credential, renewal and address all come from that
	// record; nothing secret is in the spec.
	Peer string `json:"peer"`
}

// --- relayable routes ----------------------------------------------------------

var peerRelayable = struct {
	mu       sync.RWMutex
	prefixes []string
}{}

// RegisterRelayablePath marks a path prefix as reachable through a peer relay.
// Call it beside RegisterPublicPath for a route that authenticates its caller
// on its own and is meant to be posted to by a third party. A prefix ends in
// "/" to match everything under it, as RegisterPublicPath's do.
func RegisterRelayablePath(prefix string) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || !strings.HasPrefix(prefix, "/") {
		return
	}
	peerRelayable.mu.Lock()
	defer peerRelayable.mu.Unlock()
	for _, p := range peerRelayable.prefixes {
		if p == prefix {
			return
		}
	}
	peerRelayable.prefixes = append(peerRelayable.prefixes, prefix)
	sort.Strings(peerRelayable.prefixes)
}

// relayablePaths lists the registered prefixes, for a summary to name.
func relayablePaths() []string {
	peerRelayable.mu.RLock()
	defer peerRelayable.mu.RUnlock()
	return append([]string(nil), peerRelayable.prefixes...)
}

// isRelayablePath reports whether p falls under a registered prefix.
func isRelayablePath(p string) bool {
	peerRelayable.mu.RLock()
	defer peerRelayable.mu.RUnlock()
	for _, pre := range peerRelayable.prefixes {
		if p == pre || (strings.HasSuffix(pre, "/") && strings.HasPrefix(p, pre)) {
			return true
		}
	}
	return false
}

// --- connector kind ------------------------------------------------------------

func init() { RegisterConnectorKind(PeerRelayConnectorKind, peerRelayHandlerKind{}) }

type peerRelayHandlerKind struct{}

func (peerRelayHandlerKind) parse(c Connector) (PeerRelaySpec, error) {
	var s PeerRelaySpec
	if len(c.Spec) > 0 {
		if err := json.Unmarshal(c.Spec, &s); err != nil {
			return s, fmt.Errorf("bad peer_relay spec: %w", err)
		}
	}
	s.Peer = strings.ToLower(strings.TrimSpace(s.Peer))
	return s, nil
}

func (h peerRelayHandlerKind) Validate(c Connector) error {
	s, err := h.parse(c)
	if err != nil {
		return err
	}
	if s.Peer == "" {
		return fmt.Errorf("peer is required: the name of a registered peer (Admin > Capabilities > Peers) whose key grants relay")
	}
	p, ok := GetRemotePeer(s.Peer)
	if !ok {
		return fmt.Errorf("no peer named %q is registered here: add it under Admin > Capabilities > Peers first", s.Peer)
	}
	if !p.Offers(PeerCapRelay) {
		return fmt.Errorf("peer %q does not offer relay to this instance: on that instance, add Relay to this key's grants, then refresh the peer here", s.Peer)
	}
	// Two connectors on one peer would hold two sockets on one key, and the
	// serving side keeps the newer: each would knock the other off in turn.
	for _, o := range ListConnectors(RootDB) {
		if o.Kind != PeerRelayConnectorKind || o.Name == c.Name {
			continue
		}
		if other, err := h.parse(o); err == nil && other.Peer == s.Peer {
			return fmt.Errorf("connector %q already relays through peer %q: one relay per peer", o.Name, s.Peer)
		}
	}
	return nil
}

func (h peerRelayHandlerKind) Materialize(c Connector) error {
	s, err := h.parse(c)
	if err != nil {
		return err
	}
	startPeerRelayClient(c.Name, s.Peer)
	return nil
}

func (peerRelayHandlerKind) Teardown(c Connector) error {
	stopPeerRelayClient(c.Name)
	return nil
}

func (h peerRelayHandlerKind) Summary(c Connector) string {
	s, err := h.parse(c)
	if err != nil {
		return "peer relay (unreadable spec)"
	}
	routes := strings.Join(relayablePaths(), ", ")
	if routes == "" {
		routes = "no relayable routes registered"
	}
	return fmt.Sprintf("inbound webhooks through peer %s, serving only %s: %s", s.Peer, routes, peerRelayClientStatus(c.Name, c.Approved))
}

// --- client --------------------------------------------------------------------

// peerRelayClient is one connector's running socket and what it has done.
type peerRelayClient struct {
	name, peer string
	cancel     context.CancelFunc

	mu      sync.Mutex
	state   string // connecting | connected | waiting
	since   time.Time
	public  string // the address a provider is given: peer base + relay prefix
	relayed int64
	lastErr string
}

var peerRelayClients = struct {
	mu sync.Mutex
	by map[string]*peerRelayClient
}{by: map[string]*peerRelayClient{}}

// startPeerRelayClient starts (or restarts) a connector's socket. Idempotent:
// approval, a re-save and a startup reload all land here.
func startPeerRelayClient(name, peer string) {
	stopPeerRelayClient(name)
	ctx, cancel := context.WithCancel(context.Background())
	rc := &peerRelayClient{name: name, peer: peer, cancel: cancel, state: "connecting", since: time.Now()}
	peerRelayClients.mu.Lock()
	peerRelayClients.by[name] = rc
	peerRelayClients.mu.Unlock()
	go rc.run(ctx)
}

func stopPeerRelayClient(name string) {
	peerRelayClients.mu.Lock()
	rc := peerRelayClients.by[name]
	delete(peerRelayClients.by, name)
	peerRelayClients.mu.Unlock()
	if rc != nil {
		rc.cancel()
	}
}

// peerRelayClientStatus words a connector's state for the admin table.
func peerRelayClientStatus(name string, approved bool) string {
	peerRelayClients.mu.Lock()
	rc := peerRelayClients.by[name]
	peerRelayClients.mu.Unlock()
	if rc == nil {
		if !approved {
			return "not running until approved"
		}
		return "not running"
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	switch rc.state {
	case "connected":
		return fmt.Sprintf("connected since %s, %d relayed. Give providers %s<route>, e.g. %s/bridges/api/bot/<connector>",
			rc.since.Format("Jan 2 15:04"), rc.relayed, rc.public, rc.public)
	case "waiting":
		return fmt.Sprintf("not connected: %s (retrying)", rc.lastErr)
	}
	return "connecting"
}

func (rc *peerRelayClient) set(fn func()) {
	rc.mu.Lock()
	fn()
	rc.mu.Unlock()
}

// run keeps the socket up until the connector is torn down, backing off
// between failed attempts.
func (rc *peerRelayClient) run(ctx context.Context) {
	backoff := peerRelayBackoffMin
	for ctx.Err() == nil {
		started := time.Now()
		err := rc.session(ctx)
		if ctx.Err() != nil {
			return
		}
		// A socket that stayed up a while was healthy; start the next wait
		// short rather than where an earlier bad stretch left it.
		if time.Since(started) > peerRelayBackoffMax {
			backoff = peerRelayBackoffMin
		}
		msg := "the connection ended"
		if err != nil {
			msg = err.Error()
		}
		rc.set(func() { rc.state, rc.lastErr, rc.since = "waiting", msg, time.Now() })
		Warn("[peer] relay %q through %q: %s; retrying in %s", rc.name, rc.peer, msg, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, peerRelayBackoffMax)
	}
}

// peerRelaySocketURL turns a peer's base address into its relay socket URL.
func peerRelaySocketURL(base string) (string, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(base), "/"))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("the peer's address %q is not a URL", base)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("the peer's address %q is not http(s)", base)
	}
	u.Path = strings.TrimRight(u.Path, "/") + peerRelayConnectPath
	return u.String(), nil
}

// session runs one socket from dial to close.
func (rc *peerRelayClient) session(ctx context.Context) error {
	p, ok := GetRemotePeer(rc.peer)
	if !ok {
		return fmt.Errorf("no peer named %q is registered here", rc.peer)
	}
	sockURL, err := peerRelaySocketURL(p.BaseURL)
	if err != nil {
		return err
	}
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+PeerCredentialNow(ctx, p))
	dialer := websocket.Dialer{HandshakeTimeout: 20 * time.Second, Proxy: http.ProxyFromEnvironment}
	conn, resp, err := dialer.DialContext(ctx, sockURL, hdr)
	if err != nil {
		if resp != nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusUnauthorized {
				// The next attempt exchanges for a fresh token rather than
				// presenting the refused one again.
				InvalidatePeerAccessToken(p.Name)
			}
			return fmt.Errorf("%s refused the relay (%d): %s", p.BaseURL, resp.StatusCode, peerErrorBody(resp.Body))
		}
		return fmt.Errorf("cannot reach %s: %v", p.BaseURL, err)
	}
	defer conn.Close()

	// Closing the socket is the only way to interrupt a blocked read.
	sctx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		<-sctx.Done()
		conn.Close()
	}()

	conn.SetReadLimit(peerRelayMaxBody*4/3 + 64<<10)
	conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	var hello peerRelayFrame
	if err := conn.ReadJSON(&hello); err != nil || hello.Type != "hello" || hello.Relay == "" {
		return fmt.Errorf("%s did not open the relay", p.BaseURL)
	}
	public := strings.TrimRight(p.BaseURL, "/") + hello.Relay
	rc.set(func() { rc.state, rc.since, rc.public, rc.lastErr = "connected", time.Now(), public, "" })
	Log("[peer] relay %q connected through %q: providers reach this instance at %s<route>", rc.name, rc.peer, public)

	var wmu sync.Mutex
	conn.SetPingHandler(func(data string) error {
		conn.SetReadDeadline(time.Now().Add(peerRelayReadDeadline))
		return conn.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(peerRelayWriteDeadline))
	})
	lg := NewLimitGroup(peerRelayClientInFlight)
	for {
		conn.SetReadDeadline(time.Now().Add(peerRelayReadDeadline))
		var f peerRelayFrame
		if err := conn.ReadJSON(&f); err != nil {
			if ce, ok := err.(*websocket.CloseError); ok && ce.Text != "" {
				return fmt.Errorf("%s closed the relay: %s", p.BaseURL, ce.Text)
			}
			return nil
		}
		if f.Type != "request" || f.ID == "" {
			continue
		}
		answer := func(out peerRelayFrame) {
			wmu.Lock()
			defer wmu.Unlock()
			conn.SetWriteDeadline(time.Now().Add(peerRelayWriteDeadline))
			conn.WriteJSON(out)
		}
		// Never block the read loop on a full pool: pings are read here, and a
		// stalled loop is a socket the far side drops as dead.
		if !lg.Try() {
			answer(peerRelayFrame{Type: "response", ID: f.ID, Status: http.StatusServiceUnavailable, Reason: "busy"})
			continue
		}
		go func(f peerRelayFrame) {
			defer lg.Done()
			out := servePeerRelayFrame(sctx, f)
			if out.Status < 400 {
				rc.set(func() { rc.relayed++ })
			}
			answer(out)
		}(f)
	}
}

// --- dispatch ------------------------------------------------------------------

// peerRelayRequest builds the local request a relayed frame describes, or says
// why it is not one this instance will serve.
func peerRelayRequest(ctx context.Context, f peerRelayFrame) (*http.Request, error) {
	if f.Method != http.MethodPost && f.Method != http.MethodGet {
		return nil, fmt.Errorf("method %q is not relayed", f.Method)
	}
	u, err := url.Parse(f.Path)
	if err != nil || u.Scheme != "" || u.Host != "" || u.RawQuery != "" || u.Fragment != "" || !strings.HasPrefix(u.Path, "/") {
		return nil, fmt.Errorf("not a path")
	}
	if !isRelayablePath(u.Path) || (u.RawPath != "" && !isRelayablePath(u.RawPath)) {
		return nil, fmt.Errorf("not a route this instance relays")
	}
	u.RawQuery = f.Query
	req, err := http.NewRequestWithContext(ctx, f.Method, u.String(), bytes.NewReader(f.Body))
	if err != nil {
		return nil, fmt.Errorf("unreadable request")
	}
	req.RequestURI = u.RequestURI()
	req.Header = peerRelayHeaders(f.Header)
	req.ContentLength = int64(len(f.Body))
	req.RemoteAddr = peerRelayRemoteAddr(f.Remote)
	if d, err := url.Parse(DashboardURL()); err == nil && d.Host != "" {
		req.Host = d.Host
	}
	// Relayable is the narrow list; public is the property that makes a route
	// safe on it at all. Both, checked the way the auth middleware checks.
	if !isPublicRequest(req) {
		return nil, fmt.Errorf("not a route this instance relays")
	}
	return req, nil
}

// peerRelayRemoteAddr is the caller address a relayed request carries: the one
// the serving side saw, as long as it cannot be mistaken for a local caller.
func peerRelayRemoteAddr(remote string) string {
	ip := net.ParseIP(strings.TrimSpace(remote))
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
		ip = net.IPv4zero
	}
	return net.JoinHostPort(ip.String(), "0")
}

// servePeerRelayFrame answers one relayed request through the local handler.
func servePeerRelayFrame(ctx context.Context, f peerRelayFrame) peerRelayFrame {
	out := peerRelayFrame{Type: "response", ID: f.ID}
	h := servedHandler()
	if h == nil {
		out.Status, out.Reason = http.StatusServiceUnavailable, "this instance is still starting"
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, peerRelayDispatchTimeout)
	defer cancel()
	req, err := peerRelayRequest(ctx, f)
	if err != nil {
		Debug("[peer] relay refused %s %s: %v", f.Method, f.Path, err)
		out.Status, out.Reason = http.StatusNotFound, err.Error()
		return out
	}
	rw := &peerRelayResponse{header: http.Header{}}
	h.ServeHTTP(rw, req)
	if rw.overflow {
		out.Status, out.Reason = http.StatusBadGateway, "response too large to relay"
		return out
	}
	out.Status = rw.status
	if out.Status == 0 {
		out.Status = http.StatusOK
	}
	out.Header = peerRelayHeaders(rw.header)
	out.Body = rw.body.Bytes()
	return out
}

// peerRelayResponse collects a relayed request's answer, up to what a relay
// carries. Past the cap it stops keeping bytes rather than buffering the rest
// of a response it is going to refuse anyway.
type peerRelayResponse struct {
	header   http.Header
	status   int
	body     bytes.Buffer
	overflow bool
}

func (rw *peerRelayResponse) Header() http.Header { return rw.header }

func (rw *peerRelayResponse) WriteHeader(code int) {
	if rw.status == 0 {
		rw.status = code
	}
}

func (rw *peerRelayResponse) Write(p []byte) (int, error) {
	rw.WriteHeader(http.StatusOK)
	if rw.overflow || rw.body.Len()+len(p) > peerRelayMaxBody {
		rw.overflow = true
		return len(p), nil
	}
	return rw.body.Write(p)
}
