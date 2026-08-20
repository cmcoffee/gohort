// The proxy is a separate http.Server on its own port, so nothing the
// dashboard does reaches it: not AuthMiddleware, not the admin IP allowlist,
// not TLS. Whatever guards it has to live here. It used to have none, and to
// bind every interface, which on a host with a public address is an open
// inference endpoint.
package ollama_proxy

import (
	"net/http"
	"net/http/httptest"
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
