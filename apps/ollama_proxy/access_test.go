// The proxy is a separate http.Server on its own port, so nothing the
// dashboard does reaches it: not AuthMiddleware, not the admin IP allowlist,
// not TLS. Whatever guards it has to live here. It used to have none, and to
// bind every interface, which on a host with a public address is an open
// inference endpoint.
package ollama_proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/modelaccess"
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
	for _, c := range []struct{ addr, host string }{
		{"127.0.0.1:5555", "127.0.0.1:11435"}, {"[::1]:5555", "[::1]:11435"}, {"127.0.0.1:5555", "localhost:11435"},
	} {
		r := from(c.addr)
		r.Host = c.host
		w := httptest.NewRecorder()
		if !p.allow(w, r) {
			t.Errorf("loopback caller %s to %s was refused (%d)", c.addr, c.host, w.Code)
		}
	}
}

// A web page open in a browser on this machine reaches 127.0.0.1 too, by a
// name it re-points here (DNS rebinding). Without a key it is refused: a
// request that names another host, or carries what a browser sends, is not a
// local program's.
func TestABrowserOnThisMachineNeedsAKey(t *testing.T) {
	p := &ollamaProxy{}
	for name, mod := range map[string]func(*http.Request){
		"another host":    func(r *http.Request) { r.Host = "attacker.example:11435" },
		"an origin":       func(r *http.Request) { r.Header.Set("Origin", "http://attacker.example:11435") },
		"sec-fetch":       func(r *http.Request) { r.Header.Set("Sec-Fetch-Mode", "cors") },
		"a page's accept": func(r *http.Request) { r.Header.Set("Accept", "text/html") },
	} {
		r := from("127.0.0.1:5555")
		r.Host = "127.0.0.1:11435"
		mod(r)
		w := httptest.NewRecorder()
		if p.allow(w, r) {
			t.Errorf("%s: a keyless loopback request was let through", name)
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
	tok := MintAccountToken("alice", "workstation")
	p := &ollamaProxy{}

	// Model access is closed until an administrator grants it: a valid key
	// alone is not enough.
	req := from("203.0.113.9:5555")
	req.Header.Set("X-API-Key", tok.Token)
	if p.allow(httptest.NewRecorder(), req) {
		t.Fatal("a key reached the proxy without Model access granted")
	}
	SetFeatureAllowedUsers(RootDB, ModelAccessFeature, []string{"alice"})

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
	for _, path := range []string{"/api/chat", "/api/generate", "/api/embed", "/api/show"} {
		check(path, "127.0.0.1:5555", "", true)
	}
	// The OpenAI shape is the main server's /v1 endpoint, not this proxy's:
	// here it only ever worked against an Ollama backend and 404ed on
	// llama.cpp, so a client is told where to go instead of which backend
	// is behind the curtain.
	for _, path := range []string{"/v1/chat/completions", "/v1/models", "/v1/models/gohort"} {
		check(path, "127.0.0.1:5555", "", false)
		check(path, "203.0.113.9:5555", adminKey, false)
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

// From off the box the proxy wants a raw-models key: a personal token holding
// Model access. A desktop key, an MCP key, or a Model API key (made to reach
// agents) is refused.
func TestTheProxyWantsAModelScopedToken(t *testing.T) {
	prevRoot, prevAuth := RootDB, AuthDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return RootDB }
	t.Cleanup(func() { RootDB, AuthDB = prevRoot, prevAuth })
	desktop, _ := MintDesktopKey("alice")
	mcpOnly := MintAccountTokenScoped("alice", "mcp", &TokenScope{Features: []string{"mcp"}})
	modelAPI := MintAccountTokenScoped("alice", "agents", &TokenScope{Features: []string{"openai"}, Targets: []string{"agent:a1"}})
	models := MintAccountTokenScoped("alice", "models", &TokenScope{Features: KeyKindDoors(KeyKindModels), Targets: []string{"worker"}})
	proxyOnly := MintAccountTokenScoped("alice", "proxy", &TokenScope{Features: []string{ModelAccessFeature}})
	SetFeatureAllowedUsers(RootDB, ModelAccessFeature, []string{"alice"})

	p := &ollamaProxy{}
	try := func(key string) bool {
		r := from("203.0.113.9:5555")
		r.Header.Set("X-API-Key", key)
		return p.allow(httptest.NewRecorder(), r)
	}
	if try(desktop) || try(mcpOnly.Token) {
		t.Error("a key not scoped for model access used the proxy")
	}
	if try(modelAPI.Token) {
		t.Error("a Model API key, made to reach agents, used the raw models")
	}
	if !try(models.Token) || !try(proxyOnly.Token) {
		t.Error("a raw-models key was refused")
	}
}

// A non-admin request stays on the model the proxy lends and cannot unload it.
func TestTheProxyHoldsCallersToTheLentModel(t *testing.T) {
	if _, ok := holdToLentModel([]byte(`{"model":"other-model:7b","prompt":"x"}`), "qwen3"); ok {
		t.Error("another installed model was offered")
	}
	out, ok := holdToLentModel([]byte(`{"model":"gohort","keep_alive":0}`), "qwen3")
	if !ok || strings.Contains(string(out), "keep_alive") {
		t.Errorf("keep_alive reached the backend: %s %v", out, ok)
	}
	for _, m := range []string{"gohort:no-think", "qwen3"} {
		if _, ok := holdToLentModel([]byte(`{"model":"`+m+`"}`), "qwen3"); !ok {
			t.Errorf("%s was refused", m)
		}
	}
}

// A keyed request counts against its owner's Model access limits; one from
// this machine with no key is not counted.
func TestAKeyedRequestCountsAgainstModelAccessLimits(t *testing.T) {
	prevRoot, prevAuth := RootDB, AuthDB
	RootDB = testRoot()
	AuthDB = func() Database { return RootDB }
	t.Cleanup(func() { RootDB, AuthDB = prevRoot, prevAuth })
	if err := modelaccess.Save(modelaccess.Limits{PerMinute: 100, AtOnce: 1}); err != nil {
		t.Fatal(err)
	}
	tok := MintAccountToken("alice", "workstation")
	p := &ollamaProxy{}
	keyed := func() *http.Request {
		r := from("203.0.113.9:5555")
		r.Header.Set("X-API-Key", tok.Token)
		return r
	}
	done, ok := p.admit(httptest.NewRecorder(), keyed())
	if !ok {
		t.Fatal("the first request was refused")
	}
	w := httptest.NewRecorder()
	if _, ok := p.admit(w, keyed()); ok || w.Code != http.StatusTooManyRequests {
		t.Errorf("a second request at once past a limit of one: ok=%v code=%d", ok, w.Code)
	}
	for i := 0; i < 3; i++ {
		if _, ok := p.admit(httptest.NewRecorder(), from("127.0.0.1:5555")); !ok {
			t.Error("a keyless request from this machine was counted")
		}
	}
	done()
	if _, ok := p.admit(httptest.NewRecorder(), keyed()); !ok {
		t.Error("a request finishing did not free its place")
	}
}

// Through the proxy's own router, a keyed caller past the per-minute limit is
// turned away before the model is asked.
func TestTheRouterHoldsCallersToModelAccessLimits(t *testing.T) {
	prevRoot, prevAuth := RootDB, AuthDB
	RootDB = testRoot()
	AuthDB = func() Database { return RootDB }
	t.Cleanup(func() { RootDB, AuthDB = prevRoot, prevAuth })
	// A user of its own: the limits are shared across the package, and a
	// changed limit keeps what each person has already used.
	SetFeatureAllowedUsers(RootDB, ModelAccessFeature, []string{"dora"})
	if err := modelaccess.Save(modelaccess.Limits{PerMinute: 1, AtOnce: 5}); err != nil {
		t.Fatal(err)
	}
	tok := MintAccountToken("dora", "workstation")
	mux := (&ollamaProxy{}).routes()
	send := func() int {
		r := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"model":"gohort"}`))
		r.RemoteAddr = "203.0.113.9:5555"
		r.Header.Set("X-API-Key", tok.Token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	if code := send(); code == http.StatusTooManyRequests {
		t.Fatal("the first request was refused for a limit")
	}
	if code := send(); code != http.StatusTooManyRequests {
		t.Errorf("past the per-minute limit through the router: %d, want 429", code)
	}
}

// With the worker's maximum effort set to off, a proxy call cannot turn
// thinking on for itself: its "think" becomes false, as /v1 caps it. An
// administrator's call is left as sent, and so is every call under any other
// maximum.
func TestTheProxyHoldsThinkingToTheWorkersMaximum(t *testing.T) {
	prev := RootDB
	RootDB = testRoot()
	t.Cleanup(func() { RootDB = prev })
	req := func() *http.Request { return from("127.0.0.1:5555") }
	body := []byte(`{"model":"gohort","think":true,"messages":[]}`)

	if got := capThinking(req(), body); string(got) != string(body) {
		t.Errorf("with no maximum set, the call was changed: %s", got)
	}
	RootDB.Set(LLMTable, "max_effort", "off")
	got := capThinking(req(), body)
	var obj map[string]any
	if err := json.Unmarshal(got, &obj); err != nil || obj["think"] != false || obj["model"] != "gohort" {
		t.Errorf("with the maximum off, the call kept its thinking: %s (%v)", got, err)
	}
	if got := capThinking(req(), []byte("not json")); string(got) != "not json" {
		t.Errorf("a body that is not an object was changed: %s", got)
	}
}
