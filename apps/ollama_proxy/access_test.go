// The proxy is a separate http.Server on its own port, so nothing the
// dashboard does reaches it: not AuthMiddleware, not the admin IP allowlist,
// not TLS. Whatever guards it has to live here. It used to have none, and to
// bind every interface, which on a host with a public address is an open
// inference endpoint.
package ollama_proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func testRoot() Database { return &DBase{Store: kvlite.MemStore()} }

// from builds a request whose real TCP peer is addr.
func from(addr string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/chat", nil)
	r.RemoteAddr = addr
	return r
}

func TestLoopbackNeedsNoKey(t *testing.T) {
	// Anything on the box can reach the model server directly, and Ollama
	// clients speak no authentication at all, so a key here would cost the
	// ordinary case and buy nothing.
	p := &ollamaProxy{}
	for _, addr := range []string{"127.0.0.1:5555", "[::1]:5555"} {
		w := httptest.NewRecorder()
		if !p.allow(w, from(addr)) {
			t.Errorf("loopback caller %s was refused (%d)", addr, w.Code)
		}
	}
}

func TestRemoteWithoutAKeyIsRefused(t *testing.T) {
	p := &ollamaProxy{}
	w := httptest.NewRecorder()
	if p.allow(w, from("203.0.113.9:5555")) {
		t.Fatal("an off-box caller reached the proxy with no credential")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", w.Code)
	}
	if w.Header().Get("WWW-Authenticate") == "" {
		t.Error("the refusal should say how to authenticate")
	}
}

func TestRemoteWithAKeyIsAllowed(t *testing.T) {
	prev := RootDB
	RootDB = testRoot()
	t.Cleanup(func() { RootDB = prev })
	tok := MintAccountToken("craig", "workstation")

	p := &ollamaProxy{}
	for _, set := range []func(*http.Request){
		func(r *http.Request) { r.Header.Set("X-API-Key", tok.Token) },
		func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok.Token) },
	} {
		req := from("203.0.113.9:5555")
		set(req)
		w := httptest.NewRecorder()
		if !p.allow(w, req) {
			t.Errorf("a valid key was refused (%d): %s", w.Code, w.Body.String())
		}
	}
}

func TestSpoofedLoopbackHeaderDoesNotAdmit(t *testing.T) {
	// The bypass keys on the real TCP peer. Honoring X-Forwarded-For here
	// would let anyone claim to be local by setting a header.
	p := &ollamaProxy{}
	req := from("203.0.113.9:5555")
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	w := httptest.NewRecorder()
	if p.allow(w, req) {
		t.Fatal("a forged X-Forwarded-For got in")
	}
}

func TestSchedulerKeyIgnoresForwardedFor(t *testing.T) {
	// callerIP is the fair-queue key. Trusting the header let one client
	// present a fresh identity per request and take every slot while other
	// callers waited their turn.
	req := from("198.51.100.4:5555")
	req.Header.Set("X-Forwarded-For", "10.0.0.1")
	if got := callerIP(req); got != "198.51.100.4" {
		t.Errorf("scheduler key honored a caller-supplied header: %q", got)
	}
}

func TestBindDefaultIsLoopback(t *testing.T) {
	if !isLoopbackBind(defaultBind) {
		t.Fatalf("the default bind %q must reach only this machine", defaultBind)
	}
	for _, host := range []string{"127.0.0.1", "localhost", "::1"} {
		if !isLoopbackBind(host) {
			t.Errorf("%q should read as loopback", host)
		}
	}
	// The empty string is Go's "every interface" and must not read as safe —
	// that is exactly the value the old ":port" bind used.
	for _, host := range []string{"", "0.0.0.0", "192.168.1.10", "::"} {
		if isLoopbackBind(host) {
			t.Errorf("%q must not read as loopback", host)
		}
	}
}

// The Ollama pass-through forwarded any path to the real model server, so a
// caller admitted for inference (or anything on loopback, which needs no key)
// could delete, pull or replace the deployment's models. Only the client
// endpoints pass; management needs an administrator's key.
func TestProxyPathsAreNarrowed(t *testing.T) {
	prevRoot, prevAuth := RootDB, AuthDB
	RootDB = testRoot()
	AuthDB = func() Database { return RootDB }
	t.Cleanup(func() { RootDB, AuthDB = prevRoot, prevAuth })
	AuthSetUser(RootDB, "boss", "pw", true)
	AuthSetUser(RootDB, "user1", "pw", false)
	adminKey := MintAccountToken("boss", "k").Token
	userKey := MintAccountToken("user1", "k").Token

	p := &ollamaProxy{}
	check := func(path, addr, key string, want bool) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, nil)
		r.RemoteAddr = addr
		if key != "" {
			r.Header.Set("X-API-Key", key)
		}
		w := httptest.NewRecorder()
		if got := p.allowPath(w, r); got != want {
			t.Errorf("%s from %s key=%v: allowed=%v want %v (%d)", path, addr, key != "", got, want, w.Code)
		}
	}
	for _, path := range []string{"/api/chat", "/api/generate", "/api/embed", "/api/show", "/v1/chat/completions", "/v1/models/gohort"} {
		check(path, "127.0.0.1:5555", "", true)
	}
	for _, path := range []string{"/api/delete", "/api/pull", "/api/push", "/api/create", "/api/copy", "/api/blobs/sha256:00"} {
		check(path, "127.0.0.1:5555", "", false)
		check(path, "203.0.113.9:5555", userKey, false)
		check(path, "203.0.113.9:5555", adminKey, true)
	}
	check("/api/anything-else", "127.0.0.1:5555", "", false)
}

// The proxy is its own http.Server, so it does not inherit the main server's
// limits and needs them set here.
func TestProxyServerHasHeaderTimeoutAndBodyCap(t *testing.T) {
	srv := newProxyServer("127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			http.Error(w, "too large", http.StatusRequestEntityTooLarge)
		}
	}))
	if srv.ReadHeaderTimeout == 0 || srv.IdleTimeout == 0 {
		t.Error("the proxy server has no header or idle timeout")
	}
	if srv.ReadTimeout != 0 || srv.WriteTimeout != 0 {
		t.Error("a whole-request deadline would cut streamed completions")
	}
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, httptest.NewRequest("POST", "/api/chat", strings.NewReader(strings.Repeat("x", proxyMaxBodyBytes+1))))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("a body over the cap was read in full: %d", w.Code)
	}
}
