package core

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
)

// relaySeen is what the far side's route was handed, recorded by the test
// handler so a test can assert on what crossed.
type relaySeen struct {
	mu     sync.Mutex
	method string
	path   string
	query  string
	header http.Header
	body   string
	remote string
	calls  int
}

func (s *relaySeen) get() relaySeen {
	s.mu.Lock()
	defer s.mu.Unlock()
	return relaySeen{method: s.method, path: s.path, query: s.query, header: s.header.Clone(), body: s.body, remote: s.remote, calls: s.calls}
}

// relayRig stands up both halves in one process: the serving instance's relay
// routes on an httptest server, and a consuming instance whose peer record
// points at it, connected through a real peer_relay client. The consuming
// side's routes sit behind the real dashboard middleware chain, which is what
// a relayed request meets in production.
func relayRig(t *testing.T) (base string, k PeerKey, seen *relaySeen) {
	t.Helper()
	peerTestDB(t)
	var err error
	k, err = MintPeerKey("edge", []string{PeerCapRelay}, 0)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc(peerRelayConnectPath, HandlePeerRelayConnect)
	mux.HandleFunc(peerRelayInboundPrefix, HandlePeerRelayInbound)
	mux.HandleFunc("/api/peer/manifest", HandlePeerManifest)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// The consuming side's routes. hook is the shape a webhook door has:
	// public and relayable. private is relayable but NOT public, and
	// publiconly is the reverse; neither may be reached.
	seen = &relaySeen{}
	RegisterPublicPath("/relaytest/hook/")
	RegisterRelayablePath("/relaytest/hook/")
	RegisterRelayablePath("/relaytest/private")
	RegisterPublicPath("/relaytest/publiconly/")
	local := http.NewServeMux()
	record := func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen.mu.Lock()
		seen.method, seen.path, seen.query = r.Method, r.URL.Path, r.URL.RawQuery
		seen.header, seen.body, seen.remote = r.Header.Clone(), string(b), r.RemoteAddr
		seen.calls++
		seen.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "farside", Value: "1"})
		w.Header().Set("X-Answer", "from-the-peer")
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, "ack:"+string(b))
	}
	local.HandleFunc("/relaytest/hook/", record)
	local.HandleFunc("/relaytest/private", record)
	local.HandleFunc("/relaytest/publiconly/", record)
	prevServed := servedHandler()
	setServedHandler(dashboardChain(local))
	t.Cleanup(func() { setServedHandler(prevServed) })

	p := RemotePeer{Name: "edge", BaseURL: srv.URL, Key: k.Key, Caps: []string{PeerCapRelay}}
	p = pairedPeer(t, p, k)
	RootDB.Set(remotePeersTable, p.Name, p)
	InvalidatePeerResolution()

	startPeerRelayClient("edge_relay", "edge")
	t.Cleanup(func() { stopPeerRelayClient("edge_relay") })
	waitFor(t, func() bool { return lookupPeerRelay(peerRelayID(k.ID)) != nil })
	return srv.URL, k, seen
}

// A webhook posted to the serving side's relay address is answered by the
// route on the far side: method, path, query and body arrive, and the answer
// comes back with its status and headers.
//
// What must NOT cross is the half of this that matters. A cookie or key sent to
// the serving instance means something to THAT instance, a forwarding header
// would be read as the far side's own proxy vouching for an address, and a
// Set-Cookie from the far side would land on the serving instance's origin.
func TestARelayedWebhookIsAnsweredByThePeer(t *testing.T) {
	base, k, seen := relayRig(t)
	req, _ := http.NewRequest(http.MethodPost, base+peerRelayInboundPath(k.ID)+"/relaytest/hook/teams?validationToken=v1", strings.NewReader(`{"type":"message"}`))
	req.Header.Set("Authorization", "Bearer provider-signed-token")
	req.Header.Set("X-Slack-Signature", "v0=abc")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", "gohort_session=serving-side-session")
	req.Header.Set(deploymentKeyHeader, "serving-side-deployment-key")
	req.Header.Set(internalAuthHeader, "serving-side-internal-token")
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusAccepted || string(body) != `ack:{"type":"message"}` {
		t.Fatalf("answer: %d %q, want 202 and the far side's body", resp.StatusCode, body)
	}
	if resp.Header.Get("X-Answer") != "from-the-peer" {
		t.Error("the far side's response headers did not come back")
	}
	if resp.Header.Get("Set-Cookie") != "" {
		t.Errorf("the far side set a cookie on the serving instance's origin: %q", resp.Header.Get("Set-Cookie"))
	}

	got := seen.get()
	if got.calls != 1 || got.method != http.MethodPost || got.path != "/relaytest/hook/teams" || got.query != "validationToken=v1" {
		t.Errorf("the far side saw %d call(s): %s %s ?%s", got.calls, got.method, got.path, got.query)
	}
	if got.header.Get("Authorization") != "Bearer provider-signed-token" || got.header.Get("X-Slack-Signature") != "v0=abc" {
		t.Error("the provider's own credentials did not reach the route that checks them")
	}
	for _, h := range []string{"Cookie", deploymentKeyHeader, internalAuthHeader, "X-Forwarded-For"} {
		if v := got.header.Get(h); v != "" {
			t.Errorf("%s crossed the relay: %q", h, v)
		}
	}
	// The forwarding header itself is gone, but the address the serving side
	// resolved from it (loopback is a trusted proxy) is the one that arrives.
	if got.remote != "203.0.113.9:0" {
		t.Errorf("the far side saw the caller as %q, want the address the serving side resolved", got.remote)
	}
}

// A relayed request never arrives looking like a local caller, whatever
// address the serving side reports.
func TestARelayedCallerIsNeverLocal(t *testing.T) {
	for in, want := range map[string]string{
		"127.0.0.1":   "0.0.0.0:0",
		"::1":         "0.0.0.0:0",
		"0.0.0.0":     "0.0.0.0:0",
		"":            "0.0.0.0:0",
		"not an ip":   "0.0.0.0:0",
		"203.0.113.9": "203.0.113.9:0",
		"2001:db8::1": "[2001:db8::1]:0",
	} {
		if got := peerRelayRemoteAddr(in); got != want {
			t.Errorf("peerRelayRemoteAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

// Lending an address must not become a way into the instance borrowing it.
// Only a route that is BOTH registered relayable and public (so it checks its
// provider itself) is served; anything else is refused before a handler runs.
func TestARelayReachesOnlyRelayablePublicRoutes(t *testing.T) {
	base, k, seen := relayRig(t)
	for _, p := range []string{"/relaytest/private", "/relaytest/publiconly/x", "/admin/api/users"} {
		resp, err := http.Post(base+peerRelayInboundPath(k.ID)+p, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatalf("post %s: %v", p, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s through the relay: %d, want 404", p, resp.StatusCode)
		}
	}
	if n := seen.get().calls; n != 0 {
		t.Errorf("a handler ran %d time(s) for a route the relay must not reach", n)
	}

	// Paths that only reach a relayable route by walking out of it.
	for _, p := range []string{"/relaytest/hook/../private", "/relaytest/hook/%2e%2e/private", "/relaytest/hook/%2E%2E%2Fprivate", "//relaytest/hook/x", "/relaytest/hook/./../private", "http://elsewhere/relaytest/hook/x"} {
		if _, err := peerRelayRequest(t.Context(), peerRelayFrame{Method: http.MethodPost, Path: p}); err == nil {
			t.Errorf("%s was accepted as a relayable request", p)
		}
	}
	if _, err := peerRelayRequest(t.Context(), peerRelayFrame{Method: http.MethodDelete, Path: "/relaytest/hook/x"}); err == nil {
		t.Error("a DELETE was accepted; a relay carries GET and POST only")
	}
}

// Disabling the key takes the socket down with it, and the public address
// then answers as a retry rather than reaching anything.
func TestARelayEndsWithItsKey(t *testing.T) {
	base, k, _ := relayRig(t)
	SetPeerKeyDisabled(k.ID, true)
	waitFor(t, func() bool { return lookupPeerRelay(peerRelayID(k.ID)) == nil })
	resp, err := http.Post(base+peerRelayInboundPath(k.ID)+"/relaytest/hook/x", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") == "" {
		t.Errorf("after revocation: %d (Retry-After %q), want 503 with Retry-After", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
}

// The socket is a peer's, and only a key granting relay opens it. A browser
// page cannot open one on a visitor's behalf.
func TestARelaySocketNeedsTheGrantAndNoBrowser(t *testing.T) {
	peerTestDB(t)
	relayKey, _ := MintPeerKey("edge", []string{PeerCapRelay}, 0)
	otherKey, _ := MintPeerKey("gpu", []string{PeerCapEmbeddings}, 0)
	srv := httptest.NewServer(http.HandlerFunc(HandlePeerRelayConnect))
	t.Cleanup(srv.Close)
	ws := "ws" + strings.TrimPrefix(srv.URL, "http")

	dial := func(cred, origin string) int {
		h := http.Header{}
		h.Set("Authorization", "Bearer "+cred)
		if origin != "" {
			h.Set("Origin", origin)
		}
		c, resp, err := websocket.DefaultDialer.Dial(ws, h)
		if err == nil {
			// Read the hello, then close and let the handler unwind, so
			// nothing it does outlives this test's store.
			var hello peerRelayFrame
			if err := c.ReadJSON(&hello); err != nil || hello.Type != "hello" {
				t.Errorf("no hello on an opened relay: %v %+v", err, hello)
			}
			c.Close()
			waitFor(t, func() bool { return lookupPeerRelay(peerRelayID(relayKey.ID)) == nil })
			return http.StatusSwitchingProtocols
		}
		if resp == nil {
			t.Fatalf("dial: %v", err)
		}
		return resp.StatusCode
	}
	if code := dial(peerAuth(t, otherKey), ""); code != http.StatusForbidden {
		t.Errorf("a key without relay: %d, want 403", code)
	}
	if code := dial(peerAuth(t, relayKey), "https://some-page.example"); code != http.StatusForbidden {
		t.Errorf("a socket opened from a browser page: %d, want 403", code)
	}
	if code := dial(relayKey.Key, ""); code != http.StatusUnauthorized {
		t.Errorf("the pairing code itself: %d, want 401", code)
	}
	if code := dial(peerAuth(t, relayKey), ""); code != http.StatusSwitchingProtocols {
		t.Errorf("a granted key: %d, want the upgrade", code)
	}
}

// A connector names a peer that exists, grants relay, and is not already
// relayed for by another connector (two sockets on one key knock each other
// off in turn).
func TestAPeerRelayConnectorValidatesItsPeer(t *testing.T) {
	peerTestDB(t)
	RootDB.Set(remotePeersTable, "edge", RemotePeer{Name: "edge", BaseURL: "https://edge.example", Caps: []string{PeerCapRelay}})
	RootDB.Set(remotePeersTable, "gpu", RemotePeer{Name: "gpu", BaseURL: "https://gpu.example", Caps: []string{PeerCapEmbeddings}})
	InvalidatePeerResolution()
	h := peerRelayHandlerKind{}
	mk := func(name, peer string) Connector {
		raw, _ := json.Marshal(PeerRelaySpec{Peer: peer})
		return Connector{Name: name, Kind: PeerRelayConnectorKind, Spec: raw}
	}
	for _, tc := range []struct {
		peer, want string
	}{
		{"", "peer is required"},
		{"nowhere", "no peer named"},
		{"gpu", "does not offer relay"},
	} {
		if err := h.Validate(mk("r1", tc.peer)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("peer %q: %v, want an error containing %q", tc.peer, err, tc.want)
		}
	}
	if err := h.Validate(mk("r1", "Edge")); err != nil {
		t.Fatalf("a peer granting relay: %v", err)
	}
	if err := saveConnectorDraft(RootDB, mk("r1", "edge")); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := h.Validate(mk("r2", "edge")); err == nil || !strings.Contains(err.Error(), "one relay per peer") {
		t.Errorf("a second connector on the same peer: %v", err)
	}
	if err := h.Validate(mk("r1", "edge")); err != nil {
		t.Errorf("re-validating the connector itself: %v", err)
	}
}

// The manifest tells a key granted relay where its socket and address are,
// and tells a key without it nothing.
func TestTheManifestNamesTheRelayOnlyWhenGranted(t *testing.T) {
	peerTestDB(t)
	for _, tc := range []struct {
		caps []string
		want bool
	}{{[]string{PeerCapRelay}, true}, {[]string{PeerCapEmbeddings}, false}} {
		k, _ := MintPeerKey("edge", tc.caps, 0)
		r := httptest.NewRequest(http.MethodGet, "/api/peer/manifest", nil)
		r.Header.Set(peerKeyHeader, peerAuth(t, k))
		w := httptest.NewRecorder()
		HandlePeerManifest(w, r)
		var m PeerManifest
		json.Unmarshal(w.Body.Bytes(), &m)
		if got := m.Relay != nil; got != tc.want {
			t.Errorf("caps %v: relay in manifest = %v, want %v", tc.caps, got, tc.want)
		}
		if m.Relay != nil && (m.Relay.Connect != peerRelayConnectPath || m.Relay.Inbound != peerRelayInboundPath(k.ID)) {
			t.Errorf("manifest relay: %+v", m.Relay)
		}
	}
}

// The answer is collected up to what a relay carries and no further: a route
// that answers with more is refused as too large rather than buffered whole,
// and a route that writes without a status is a 200, as it is on the wire.
func TestARelayedAnswerIsBounded(t *testing.T) {
	RegisterPublicPath("/relaytest/answer/")
	RegisterRelayablePath("/relaytest/answer/")
	mux := http.NewServeMux()
	mux.HandleFunc("/relaytest/answer/big", func(w http.ResponseWriter, r *http.Request) {
		chunk := strings.Repeat("x", 64<<10)
		for i := 0; i < 40; i++ { // 2.5 MB in 64 KB writes
			io.WriteString(w, chunk)
		}
	})
	mux.HandleFunc("/relaytest/answer/small", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	})
	mux.HandleFunc("/relaytest/answer/silent", func(w http.ResponseWriter, r *http.Request) {})
	prev := servedHandler()
	setServedHandler(mux)
	t.Cleanup(func() { setServedHandler(prev) })

	big := servePeerRelayFrame(t.Context(), peerRelayFrame{Type: "request", ID: "1", Method: http.MethodGet, Path: "/relaytest/answer/big"})
	if big.Status != http.StatusBadGateway || len(big.Body) != 0 {
		t.Errorf("an oversized answer: %d with %d body bytes, want 502 and none", big.Status, len(big.Body))
	}
	small := servePeerRelayFrame(t.Context(), peerRelayFrame{Type: "request", ID: "2", Method: http.MethodGet, Path: "/relaytest/answer/small"})
	if small.Status != http.StatusOK || string(small.Body) != "ok" {
		t.Errorf("a plain answer: %d %q, want 200 \"ok\"", small.Status, small.Body)
	}

	if silent := servePeerRelayFrame(t.Context(), peerRelayFrame{Type: "request", ID: "4", Method: http.MethodPost, Path: "/relaytest/answer/silent"}); silent.Status != http.StatusOK {
		t.Errorf("a route that writes nothing: %d, want 200 as net/http sends", silent.Status)
	}

	setServedHandler(nil)
	if early := servePeerRelayFrame(t.Context(), peerRelayFrame{Type: "request", ID: "3", Method: http.MethodGet, Path: "/relaytest/answer/small"}); early.Status != http.StatusServiceUnavailable {
		t.Errorf("before the listener is up: %d, want 503", early.Status)
	}
}
