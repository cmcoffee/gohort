package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/kvlite"
)

// peerModelDB gives a test a scratch auth store and restores the globals.
func peerModelDB(t *testing.T) Database {
	t.Helper()
	prevRoot, prevAuth := RootDB, AuthDB
	prevSecure := secureAPIInstance
	t.Cleanup(func() {
		RootDB, AuthDB = prevRoot, prevAuth
		secureAPIInstanceMu.Lock()
		secureAPIInstance = prevSecure
		secureAPIInstanceMu.Unlock()
	})
	RootDB = &DBase{Store: kvlite.MemStore()}
	resetPeerTokenCache() // see peerTestDB: the credential cache outlives the store
	InvalidatePeerResolution()
	auth := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return auth }
	secureAPIInstanceMu.Lock()
	secureAPIInstance = nil
	secureAPIInstanceMu.Unlock()
	return auth
}

// setTier writes one tier's provider config.
func setTier(db Database, table, provider, model, endpoint string) {
	db.Set(table, "provider", provider)
	db.Set(table, "model", model)
	db.Set(table, "endpoint", endpoint)
}

// TestHostedProvidersAreNeverLent — the money guard. Proxying a hosted provider
// would spend the operator's API key on a peer's prompts with no per-caller
// accounting, so the endpoint must refuse rather than forward.
func TestHostedProvidersAreNeverLent(t *testing.T) {
	db := peerModelDB(t)
	for _, provider := range []string{"anthropic", "openai", "gemini", "bedrock"} {
		setTier(db, LLMTable, provider, "some-model", "")
		db.Unset(LeadLLMTable, "provider")
		if peerModelsServed() {
			t.Errorf("provider %q is advertised as lendable", provider)
		}
		if _, err := resolvePeerTier(""); err == nil {
			t.Errorf("provider %q was resolved for lending", provider)
		}
	}
}

// TestBorrowedInferenceIsNotRelayed — an instance already borrowing its models
// from a peer must not serve them onward. A→B→A is a loop neither side can see,
// and it fails as a hang rather than an error.
func TestBorrowedInferenceIsNotRelayed(t *testing.T) {
	db := peerModelDB(t)
	setTier(db, LLMTable, "llama.cpp", "qwen", "https://den.example/api/peer/v1")
	if peerModelsServed() {
		t.Error("an instance borrowing its own inference offered to relay it")
	}
}

// TestBothTiersAreOfferedAndWorkerIsTheDefault — a peer with a fast worker and
// a slow lead is offering a choice, and the caller has to be able to see and
// make it. A bare request takes the worker.
func TestBothTiersAreOfferedAndWorkerIsTheDefault(t *testing.T) {
	db := peerModelDB(t)
	setTier(db, LLMTable, "llama.cpp", "qwen-27b", "http://127.0.0.1:8080/v1")
	setTier(db, LeadLLMTable, "llama.cpp", "qwen-72b", "http://127.0.0.1:8081/v1")

	tiers := peerServableTiers()
	if len(tiers) != 2 {
		t.Fatalf("got %d servable tiers, want 2: %+v", len(tiers), tiers)
	}
	if tiers[0].Tier != "worker" {
		t.Errorf("the worker tier is not first, so a bare request would default to %q", tiers[0].Tier)
	}
	got, err := resolvePeerTier("")
	if err != nil || got.Model != "qwen-27b" {
		t.Errorf("a request naming no model resolved to %+v (err %v), want the worker", got, err)
	}
	// By model id, and by tier alias.
	if got, err := resolvePeerTier("qwen-72b"); err != nil || got.Tier != "lead" {
		t.Errorf("naming the lead model resolved to %+v (err %v)", got, err)
	}
	if got, err := resolvePeerTier("lead"); err != nil || got.Model != "qwen-72b" {
		t.Errorf("the tier alias did not resolve: %+v (err %v)", got, err)
	}
}

// TestTiersSharingAModelAreOfferedOnce — advertising the same model twice
// presents a choice that is not one.
func TestTiersSharingAModelAreOfferedOnce(t *testing.T) {
	db := peerModelDB(t)
	setTier(db, LLMTable, "llama.cpp", "qwen-27b", "http://127.0.0.1:8080/v1")
	setTier(db, LeadLLMTable, "llama.cpp", "qwen-27b", "http://127.0.0.1:8080/v1")
	if n := len(peerServableTiers()); n != 1 {
		t.Errorf("the same model was advertised %d times", n)
	}
}

// TestAnUnknownModelIsRefusedNotSubstituted — the same rule the embeddings
// endpoint enforces. Silently answering from a different model returns text the
// caller attributes to a model that never ran, and nothing downstream can
// detect that.
func TestAnUnknownModelIsRefusedNotSubstituted(t *testing.T) {
	db := peerModelDB(t)
	setTier(db, LLMTable, "llama.cpp", "qwen-27b", "http://127.0.0.1:8080/v1")
	_, err := resolvePeerTier("gpt-4o")
	if err == nil {
		t.Fatal("a model this instance does not serve was accepted")
	}
	// The error has to name what IS served, or the caller cannot fix it.
	if !strings.Contains(err.Error(), "qwen-27b") {
		t.Errorf("the refusal does not say what is available: %v", err)
	}
}

// TestUpstreamURLMatchesTheOrdinaryClient — a peer must reach exactly the
// endpoint a local caller would. A mismatch here is a 404 that looks like the
// model is down.
func TestUpstreamURLMatchesTheOrdinaryClient(t *testing.T) {
	cases := map[string]string{
		"http://localhost:8080":     "http://localhost:8080/v1/chat/completions",
		"http://localhost:8080/v1":  "http://localhost:8080/v1/chat/completions",
		"http://localhost:8080/v1/": "http://localhost:8080/v1/chat/completions",
		"http://localhost:11434":    "http://localhost:11434/v1/chat/completions",
	}
	for in, want := range cases {
		got, err := upstreamChatURL(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
	if _, err := upstreamChatURL(""); err == nil {
		t.Error("an empty endpoint produced a URL")
	}
}

// TestTheGrantIsRequired — a key granted embeddings must not be able to spend
// the GPU on inference.
func TestTheGrantIsRequired(t *testing.T) {
	db := peerModelDB(t)
	setTier(db, LLMTable, "llama.cpp", "qwen", "http://127.0.0.1:8080/v1")
	pk, _ := MintPeerKey("mac", []string{PeerCapEmbeddings}, 0)

	r := httptest.NewRequest(http.MethodPost, "/api/peer/v1/chat/completions",
		strings.NewReader(`{"messages":[]}`))
	r.Header.Set(peerKeyHeader, peerAuth(t, pk))
	w := httptest.NewRecorder()
	HandlePeerChatCompletions(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("a key without the grant got %d, want 403", w.Code)
	}
}

// TestTheProxyForwardsTheBodyVerbatim — the property the whole file exists for.
// Tool schemas, images, sampling parameters and thinking budgets all ride in
// fields this code does not model, and a proxy that reconstructs the request
// drops whatever it does not know about.
func TestTheProxyForwardsTheBodyVerbatim(t *testing.T) {
	db := peerModelDB(t)
	var seen map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("upstream hit at %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &seen)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"hi"}}]}`)
	}))
	defer upstream.Close()
	setTier(db, LLMTable, "llama.cpp", "qwen-27b", upstream.URL+"/v1")
	pk, _ := MintPeerKey("mac", []string{PeerCapModels}, 0)

	sent := `{"model":"qwen-27b","messages":[{"role":"user","content":"hi"}],` +
		`"tools":[{"type":"function","function":{"name":"probe"}}],` +
		`"temperature":0.15,"chat_template_kwargs":{"enable_thinking":false},"stop":["</x>"]}`
	r := httptest.NewRequest(http.MethodPost, "/api/peer/v1/chat/completions", strings.NewReader(sent))
	r.Header.Set(peerKeyHeader, peerAuth(t, pk))
	w := httptest.NewRecorder()
	HandlePeerChatCompletions(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	for _, field := range []string{"tools", "temperature", "chat_template_kwargs", "stop", "messages"} {
		if _, ok := seen[field]; !ok {
			t.Errorf("the proxy dropped %q — a field it does not model did not reach the model", field)
		}
	}
	if !strings.Contains(w.Body.String(), `"hi"`) {
		t.Errorf("the upstream response did not reach the caller: %s", w.Body.String())
	}
}

// TestATierAliasIsRewrittenToTheModelId — the one field the proxy does change,
// because the alias is a convenience this endpoint invented and upstream has
// never heard of it.
func TestATierAliasIsRewrittenToTheModelId(t *testing.T) {
	db := peerModelDB(t)
	var seenModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
			Tools []any  `json:"tools"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		seenModel = body.Model
		if len(body.Tools) != 1 {
			t.Error("rewriting the model dropped another field")
		}
		fmt.Fprint(w, `{}`)
	}))
	defer upstream.Close()
	setTier(db, LLMTable, "llama.cpp", "qwen-27b", upstream.URL+"/v1")
	pk, _ := MintPeerKey("mac", []string{PeerCapModels}, 0)

	r := httptest.NewRequest(http.MethodPost, "/api/peer/v1/chat/completions",
		strings.NewReader(`{"model":"worker","tools":[{"name":"x"}],"messages":[]}`))
	r.Header.Set(peerKeyHeader, peerAuth(t, pk))
	HandlePeerChatCompletions(httptest.NewRecorder(), r)

	if seenModel != "qwen-27b" {
		t.Errorf("upstream received model %q, want the resolved id", seenModel)
	}
}

// TestStreamingReachesTheCallerIncrementally — SSE buffered into one blob is
// indistinguishable from a model that produces nothing and then everything,
// which is exactly the property a streaming caller asked for.
func TestStreamingReachesTheCallerIncrementally(t *testing.T) {
	db := peerModelDB(t)
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"one\"}}]}\n\n")
		f.Flush()
		<-release // hold the second chunk back until the test has seen the first
		fmt.Fprint(w, "data: [DONE]\n\n")
		f.Flush()
	}))
	defer upstream.Close()
	setTier(db, LLMTable, "llama.cpp", "qwen", upstream.URL+"/v1")
	pk, _ := MintPeerKey("mac", []string{PeerCapModels}, 0)

	// A recorder cannot observe flushing, so drive the handler over a real
	// connection: the assertion is that bytes ARRIVE before the response ends.
	srv := httptest.NewServer(http.HandlerFunc(HandlePeerChatCompletions))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"stream":true,"messages":[]}`))
	req.Header.Set(peerKeyHeader, peerAuth(t, pk))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "event-stream") {
		t.Errorf("content type %q is not SSE", ct)
	}

	first := make(chan string, 1)
	go func() {
		buf := make([]byte, 256)
		n, _ := resp.Body.Read(buf)
		first <- string(buf[:n])
	}()
	select {
	case got := <-first:
		if !strings.Contains(got, "one") {
			t.Errorf("first read was %q, want the first event", got)
		}
	case <-time.After(3 * time.Second):
		t.Error("no bytes arrived before the stream finished — the proxy is buffering SSE")
	}
	close(release)
	io.Copy(io.Discard, resp.Body)
}

// TestUpstreamFailuresReachTheCallerAsThemselves — a 400 from the model is a
// fixable problem, and paraphrasing it as a peer error hides what to fix.
func TestUpstreamFailuresReachTheCallerAsThemselves(t *testing.T) {
	db := peerModelDB(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"context window exceeded"}}`)
	}))
	defer upstream.Close()
	setTier(db, LLMTable, "llama.cpp", "qwen", upstream.URL+"/v1")
	pk, _ := MintPeerKey("mac", []string{PeerCapModels}, 0)

	r := httptest.NewRequest(http.MethodPost, "/api/peer/v1/chat/completions",
		strings.NewReader(`{"messages":[]}`))
	r.Header.Set(peerKeyHeader, peerAuth(t, pk))
	w := httptest.NewRecorder()
	HandlePeerChatCompletions(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("upstream 400 became %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "context window exceeded") {
		t.Errorf("the upstream error text was lost: %s", w.Body.String())
	}
}

// TestTheModelListMatchesWhatWillBeServed — a picker that offers a model the
// chat endpoint then refuses is worse than an empty picker.
func TestTheModelListMatchesWhatWillBeServed(t *testing.T) {
	db := peerModelDB(t)
	setTier(db, LLMTable, "llama.cpp", "qwen-27b", "http://127.0.0.1:8080/v1")
	setTier(db, LeadLLMTable, "anthropic", "claude-opus-5", "")
	pk, _ := MintPeerKey("mac", []string{PeerCapModels}, 0)

	r := httptest.NewRequest(http.MethodGet, "/api/peer/v1/models", nil)
	r.Header.Set(peerKeyHeader, peerAuth(t, pk))
	w := httptest.NewRecorder()
	HandlePeerModels(w, r)

	var out struct {
		Data []struct{ ID string } `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if len(out.Data) != 1 || out.Data[0].ID != "qwen-27b" {
		t.Fatalf("the list is %+v, want only the local model", out.Data)
	}
	for _, m := range out.Data {
		if _, err := resolvePeerTier(m.ID); err != nil {
			t.Errorf("the picker offers %q but the chat endpoint refuses it: %v", m.ID, err)
		}
	}
}

// TestTheManifestSaysWhyItIsNotServing — "not served" has two causes here, and
// only one of them is something the operator can fix.
func TestTheManifestSaysWhyItIsNotServing(t *testing.T) {
	db := peerModelDB(t)
	setTier(db, LLMTable, "anthropic", "claude-opus-5", "")
	pk, _ := MintPeerKey("mac", []string{PeerCapModels}, 0)

	r := httptest.NewRequest(http.MethodGet, "/api/peer/manifest", nil)
	r.Header.Set(peerKeyHeader, peerAuth(t, pk))
	w := httptest.NewRecorder()
	HandlePeerManifest(w, r)

	var m PeerManifest
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(m.Models) != 0 {
		t.Errorf("a hosted-only instance advertised models: %+v", m.Models)
	}
	for _, e := range m.Capabilities {
		if e.Name != PeerCapModels {
			continue
		}
		if e.Served {
			t.Error("a hosted-only instance reports inference as served")
		}
		if !strings.Contains(e.Note, "no local model") {
			t.Errorf("the note does not explain the real reason: %q", e.Note)
		}
		return
	}
	t.Error("the manifest never mentioned the models capability")
}

// TestTheManifestAdvertisesTheServedModel — the far side configures from this.
func TestTheManifestAdvertisesTheServedModel(t *testing.T) {
	db := peerModelDB(t)
	setTier(db, LLMTable, "llama.cpp", "qwen-27b", "http://127.0.0.1:8080/v1")
	pk, _ := MintPeerKey("mac", []string{PeerCapModels}, 0)

	r := httptest.NewRequest(http.MethodGet, "/api/peer/manifest", nil)
	r.Header.Set(peerKeyHeader, peerAuth(t, pk))
	w := httptest.NewRecorder()
	HandlePeerManifest(w, r)

	var m PeerManifest
	json.Unmarshal(w.Body.Bytes(), &m)
	if len(m.Models) != 1 {
		t.Fatalf("manifest models = %+v", m.Models)
	}
	got := m.Models[0]
	if got.Model != "qwen-27b" || got.Tier != "worker" || got.Path != "/api/peer/v1" {
		t.Errorf("advertised %+v", got)
	}
}

// TestAPeerProviderResolvesFreshEveryTime — the bug this design exists to
// avoid. Snapshotting the peer's key into the stored config is what made a
// rotated key present as a 401 from a config that looked correct.
func TestAPeerProviderResolvesFreshEveryTime(t *testing.T) {
	peerModelDB(t)
	putTestPeer(RemotePeer{Name: "den", BaseURL: "https://den.example", Key: "first-key",
		Caps: []string{PeerCapModels}})
	stored := LLMProviderConfig{Provider: PeerProviderValue("den")}

	got, err := ResolveModelProvider(stored, stored.Provider)
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != "llama.cpp" {
		t.Errorf("provider resolved to %q", got.Provider)
	}
	if got.Endpoint != "https://den.example/api/peer/v1" {
		t.Errorf("endpoint resolved to %q", got.Endpoint)
	}
	if got.APIKey != "first-key" {
		t.Errorf("key resolved to %q", got.APIKey)
	}

	// Rotate the peer's key. The STORED config is untouched — it still just
	// says "peer:den" — so the next resolution must pick the new key up.
	putTestPeer(RemotePeer{Name: "den", BaseURL: "https://den.example", Key: "second-key",
		Caps: []string{PeerCapModels}})
	got, err = ResolveModelProvider(stored, stored.Provider)
	if err != nil {
		t.Fatal(err)
	}
	if got.APIKey != "second-key" {
		t.Errorf("after rotation the key is still %q — the config snapshotted it, "+
			"which is the failure this resolves-at-use design exists to prevent", got.APIKey)
	}
}

// TestAPeerNotOfferingInferenceIsRefused — and an ordinary provider passes
// through untouched, or every non-peer config would be rewritten.
func TestAPeerNotOfferingInferenceIsRefused(t *testing.T) {
	peerModelDB(t)
	putTestPeer(RemotePeer{Name: "den", BaseURL: "https://den.example", Key: "k",
		Caps: []string{PeerCapEmbeddings}})

	if _, err := ResolveModelProvider(LLMProviderConfig{}, PeerProviderValue("den")); err == nil {
		t.Error("a peer that does not offer inference was accepted")
	}
	if _, err := ResolveModelProvider(LLMProviderConfig{}, PeerProviderValue("nobody")); err == nil {
		t.Error("an unregistered peer was accepted")
	}
	in := LLMProviderConfig{Provider: "llama.cpp", Endpoint: "http://127.0.0.1:8080/v1", APIKey: "x"}
	out, err := ResolveModelProvider(in, in.Provider)
	if err != nil || out != in {
		t.Errorf("an ordinary provider was altered: %+v (err %v)", out, err)
	}
}

// putTestPeer registers a peer record without going through the network probe
// SaveRemotePeer performs.
func putTestPeer(p RemotePeer) {
	RootDB.Set(remotePeersTable, strings.ToLower(p.Name), p)
}

// --- scheduling -----------------------------------------------------------

// TestAPeerRequestQueuesBehindTheLocalScheduler — the correctness half.
//
// The serializer is enforced CLIENT-side, inside gohort's llama.cpp client, and
// this endpoint is a raw proxy — so a peer's request used to go straight at the
// inference server while local turns waited behind the mutex. That is not a
// fairness nicety: stock llama.cpp is single-threaded and answers 503 under
// concurrent load, so an overlapping peer turn could fail a local one outright.
func TestAPeerRequestQueuesBehindTheLocalScheduler(t *testing.T) {
	db := peerModelDB(t)
	StartLlamacppScheduler(1)

	// Hold the only slot, as a local turn would.
	if err := AcquireLlamacppSlot(context.Background(), "local"); err != nil {
		t.Fatalf("acquire: %v", err)
	}

	var reached atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
		fmt.Fprint(w, `{}`)
	}))
	defer upstream.Close()
	setTier(db, LLMTable, "llama.cpp", "qwen", upstream.URL+"/v1")
	pk, _ := MintPeerKey("mac", []string{PeerCapModels}, 0)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r := httptest.NewRequest(http.MethodPost, "/api/peer/v1/chat/completions",
			strings.NewReader(`{"messages":[]}`)).WithContext(ctx)
		r.Header.Set(peerKeyHeader, peerAuth(t, pk))
		HandlePeerChatCompletions(httptest.NewRecorder(), r)
	}()

	// While the local slot is held the peer must NOT reach the model.
	time.Sleep(150 * time.Millisecond)
	if reached.Load() {
		t.Error("a peer request reached the inference server while a local turn held the only slot — " +
			"the two now race, and llama.cpp answers 503 under concurrent load")
	}

	ReleaseLlamacppSlot("local")
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("the peer request never proceeded after the local slot was released")
	}
	if !reached.Load() {
		t.Error("the peer request never reached the model even after the slot freed")
	}
	cancel()
}

// TestTheSlotIsHeldForTheWholeStream — releasing at the response header would
// make the serialization fictional for exactly the requests that need it most:
// a streaming generation returns headers in milliseconds and runs for minutes.
func TestTheSlotIsHeldForTheWholeStream(t *testing.T) {
	db := peerModelDB(t)
	StartLlamacppScheduler(1)

	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {}\n\n")
		f.Flush() // headers and first byte are out; the generation continues
		<-release
	}))
	defer upstream.Close()
	setTier(db, LLMTable, "llama.cpp", "qwen", upstream.URL+"/v1")
	pk, _ := MintPeerKey("mac", []string{PeerCapModels}, 0)

	srv := httptest.NewServer(http.HandlerFunc(HandlePeerChatCompletions))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"stream":true,"messages":[]}`))
	req.Header.Set(peerKeyHeader, peerAuth(t, pk))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 64)
	resp.Body.Read(buf) // the stream is live and mid-generation

	// The slot must still be taken. Ask for it with a deadline rather than
	// blocking, so a regression is a failure and not a hung test.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := AcquireLlamacppSlot(ctx, "local"); err == nil {
		ReleaseLlamacppSlot("local")
		t.Error("the slot was free mid-stream — it is being released at the response header, " +
			"so a long generation serializes against nothing")
	}
	close(release)
	io.Copy(io.Discard, resp.Body)
}

// TestTheSchedulerLabelsThePeer — the scheduler round-robins BETWEEN callers and
// already counts in-flight and queued per caller, so labelling is both what
// stops a peer's batch monopolizing the queue and what a live indicator would
// read. Anonymous, it looks like local work.
func TestTheSchedulerLabelsThePeer(t *testing.T) {
	db := peerModelDB(t)
	StartLlamacppScheduler(1)

	seen := make(chan map[string]int, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case seen <- LlamacppSchedulerStats().Callers:
		default:
		}
		fmt.Fprint(w, `{}`)
	}))
	defer upstream.Close()
	setTier(db, LLMTable, "llama.cpp", "qwen", upstream.URL+"/v1")
	pk, _ := MintPeerKey("studio-mac", []string{PeerCapModels}, 0)

	r := httptest.NewRequest(http.MethodPost, "/api/peer/v1/chat/completions",
		strings.NewReader(`{"messages":[]}`))
	r.Header.Set(peerKeyHeader, peerAuth(t, pk))
	HandlePeerChatCompletions(httptest.NewRecorder(), r)

	select {
	case callers := <-seen:
		if callers["peer:studio-mac"] == 0 {
			t.Errorf("the scheduler does not know which peer holds the slot: %v — "+
				"a peer's work is indistinguishable from local work", callers)
		}
	default:
		t.Fatal("upstream was never reached")
	}
}
