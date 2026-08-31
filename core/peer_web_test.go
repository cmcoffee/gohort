package core

// Search and browse over the peer link.
//
// Two capabilities that both say "reach the web" and are nothing alike
// underneath: search spends a metered third-party key, browse spends CPU on a
// headless Chromium. They get separate grants, separate limits, and — for
// browse — the guard that stops a peer key becoming a LAN proxy.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func withSearchProvider(t *testing.T, provider string, fn func(query, prov, key, endpoint string) (string, error)) {
	t.Helper()
	prevCfg, prevFn := LoadWebSearchConfigFunc, SearchWithProviderFunc
	t.Cleanup(func() { LoadWebSearchConfigFunc, SearchWithProviderFunc = prevCfg, prevFn })
	LoadWebSearchConfigFunc = func() WebSearchConfig {
		return WebSearchConfig{Provider: provider, APIKey: "sk-upstream"}
	}
	// Adapted rather than restated at each call site: these tests care about
	// the four values, not about the struct that now carries them.
	SearchWithProviderFunc = func(r SearchRequest) (string, error) {
		return fn(r.Query, r.Provider, r.APIKey, r.Endpoint)
	}
}

func searchRequest(t *testing.T, key, q string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/peer/v1/search?q="+q, nil)
	r.Header.Set(peerKeyHeader, key)
	return r
}

// The wire shape is SearXNG's because tools/websearch already drives it — the
// consuming side points an ordinary searxng config at the peer and needs no
// client of its own.
func TestPeerSearchAnswersInTheSearXNGShape(t *testing.T) {
	peerImageDB(t)
	withSearchProvider(t, "serper", func(q, prov, key, endpoint string) (string, error) {
		if key != "sk-upstream" {
			t.Errorf("the upstream key was not used: %q", key)
		}
		return "1. First Result\n   https://example.com/a\n   A snippet.\n\n2. Second\n   https://example.com/b\n   More.", nil
	})
	pk, _ := MintPeerKey("mac", []string{PeerCapSearch}, 0)

	w := httptest.NewRecorder()
	HandlePeerSearch(w, searchRequest(t, peerAuth(t, pk), "golang"))
	if w.Code != http.StatusOK {
		t.Fatalf("search → %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Results []struct {
			Title, URL, Content string
		} `json:"results"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("not SearXNG-shaped JSON: %s", w.Body.String())
	}
	if len(out.Results) != 2 {
		t.Fatalf("got %d results, want 2: %s", len(out.Results), w.Body.String())
	}
	if out.Results[0].Title != "First Result" {
		t.Errorf("title = %q — the ordinal should be stripped", out.Results[0].Title)
	}
	if out.Results[0].URL != "https://example.com/a" {
		t.Errorf("url = %q", out.Results[0].URL)
	}
	if out.Results[0].Content != "A snippet." {
		t.Errorf("content = %q", out.Results[0].Content)
	}
}

// Search is the first peer capability that spends money per call. The shared
// ceiling is 600/min — fine for an embedder grinding a corpus, a billing
// incident for a paid search API — so it carries its own, much lower limit.
func TestPeerSearchIsRateLimitedSeparately(t *testing.T) {
	peerImageDB(t)
	withSearchProvider(t, "serper", func(q, prov, key, endpoint string) (string, error) {
		return "1. R\n   https://e.com\n   x", nil
	})
	pk, _ := MintPeerKey("mac", []string{PeerCapSearch}, 0)

	if peerSearchRatePerMin >= defaultPeerRatePerMin {
		t.Fatalf("search ceiling %d is not below the general %d — it would inherit a limit sized for embeddings",
			peerSearchRatePerMin, defaultPeerRatePerMin)
	}
	var lastCode int
	for i := 0; i < peerSearchRatePerMin+1; i++ {
		w := httptest.NewRecorder()
		HandlePeerSearch(w, searchRequest(t, peerAuth(t, pk), "q"))
		lastCode = w.Code
	}
	if lastCode != http.StatusTooManyRequests {
		t.Errorf("call %d → %d, want 429 once the per-capability ceiling is spent",
			peerSearchRatePerMin+1, lastCode)
	}
}

// A key granted browse must not be able to spend the search key, and vice
// versa — the whole point of two grants rather than one "web" grant.
func TestSearchAndBrowseAreSeparateGrants(t *testing.T) {
	peerImageDB(t)
	withSearchProvider(t, "serper", func(q, prov, key, endpoint string) (string, error) {
		t.Error("a browse-only key reached the search provider")
		return "", nil
	})
	pk, _ := MintPeerKey("mac", []string{PeerCapBrowse}, 0)

	w := httptest.NewRecorder()
	HandlePeerSearch(w, searchRequest(t, peerAuth(t, pk), "q"))
	if w.Code != http.StatusForbidden {
		t.Errorf("browse-only key searching → %d, want 403: %s", w.Code, w.Body.String())
	}
}

// THE check for the browse endpoint. This instance sits inside a network the
// caller cannot otherwise reach, and the tool-layer guard lives on
// BrowsePageTool.Run — a handler calling BrowserFetchFunc directly sails past
// it. Without this a peer key is a LAN proxy.
func TestPeerBrowseRefusesThePrivateNetwork(t *testing.T) {
	peerImageDB(t)
	prev := BrowserFetchFunc
	t.Cleanup(func() { BrowserFetchFunc = prev })
	BrowserFetchFunc = func(url string, maxChars int) (string, error) {
		t.Errorf("the browser was reached for a non-public host: %s", url)
		return "", nil
	}
	pk, _ := MintPeerKey("mac", []string{PeerCapBrowse}, 0)

	for _, target := range []string{
		"http://127.0.0.1:8080/admin",
		"http://192.168.1.1/",
		"http://10.0.0.5/secrets",
		"http://localhost/",
		"http://nas.local/",
		"http://vault.internal/",
		"http://169.254.169.254/latest/meta-data/",
	} {
		body, _ := json.Marshal(map[string]any{"url": target})
		r := httptest.NewRequest(http.MethodPost, "/api/peer/v1/browse", strings.NewReader(string(body)))
		r.Header.Set(peerKeyHeader, peerAuth(t, pk))
		w := httptest.NewRecorder()
		HandlePeerBrowse(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("browse %s → %d, want 400", target, w.Code)
		}
		if !strings.Contains(w.Body.String(), "private network") {
			t.Errorf("the refusal for %s should say why: %s", target, w.Body.String())
		}
	}
}

func TestPeerBrowseRendersAPublicPage(t *testing.T) {
	peerImageDB(t)
	prev := BrowserFetchFunc
	t.Cleanup(func() { BrowserFetchFunc = prev })
	BrowserFetchFunc = func(url string, maxChars int) (string, error) {
		return "the rendered page text", nil
	}
	pk, _ := MintPeerKey("mac", []string{PeerCapBrowse}, 0)

	body, _ := json.Marshal(map[string]any{"url": "https://example.com/article"})
	r := httptest.NewRequest(http.MethodPost, "/api/peer/v1/browse", strings.NewReader(string(body)))
	r.Header.Set(peerKeyHeader, peerAuth(t, pk))
	w := httptest.NewRecorder()
	HandlePeerBrowse(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("browse → %d: %s", w.Code, w.Body.String())
	}
	var out struct{ Text string }
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.Text != "the rendered page text" {
		t.Errorf("text = %q", out.Text)
	}
}

// Selecting a peer must produce a COMPLETE ordinary config — a searxng provider
// pointed at the peer, carrying the peer key.
func TestResolveSearchProviderFillsFromThePeer(t *testing.T) {
	peerImageDB(t)
	p := RemotePeer{Name: "gpu-box", BaseURL: "https://gpu.example", Key: "peer-key",
		Caps: []string{PeerCapSearch}}
	RootDB.Set(remotePeersTable, p.Name, p)

	got, err := ResolveSearchProvider(WebSearchConfig{Provider: "duckduckgo"}, PeerProviderValue("gpu-box"))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Provider != "searxng" {
		t.Errorf("provider = %q, want searxng — that is the client shape the peer serves", got.Provider)
	}
	if got.Endpoint != p.SearchURL() {
		t.Errorf("endpoint = %q, want %q", got.Endpoint, p.SearchURL())
	}
	if got.APIKey != "peer-key" {
		t.Error("the peer key was not carried, so every search would 401")
	}

	// A peer that does not offer search is refused by name.
	q := RemotePeer{Name: "embed-only", BaseURL: "https://e.example", Key: "k",
		Caps: []string{PeerCapEmbeddings}}
	RootDB.Set(remotePeersTable, q.Name, q)
	if _, err := ResolveSearchProvider(WebSearchConfig{}, PeerProviderValue("embed-only")); err == nil ||
		!strings.Contains(err.Error(), "does not offer search") {
		t.Errorf("expected a named refusal, got %v", err)
	}
}

// End to end: a consumer configured from a peer actually searches through it.
// The two halves were built against the SearXNG wire format on purpose — this
// is what proves they meet.
func TestSearchThroughAConfiguredPeer(t *testing.T) {
	peerImageDB(t)
	withSearchProvider(t, "serper", func(q, prov, key, endpoint string) (string, error) {
		return "1. Peered Hit\n   https://example.com/x\n   From the far side.", nil
	})

	pk, _ := MintPeerKey("consumer", []string{PeerCapSearch}, 0)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/peer/manifest", HandlePeerManifest)
	// Exchange is mandatory now, so a fake peer that does not serve the token
	// endpoint is not a peer any client can pair with — the same 404 a real
	// instance on an older build would answer with.
	mux.HandleFunc("/api/peer/v1/token", HandlePeerToken)
	mux.HandleFunc("/api/peer/v1/search", HandlePeerSearch)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	p, err := SaveRemotePeer(t.Context(), "gpu-box", srv.URL, pk.Key)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !p.Offers(PeerCapSearch) {
		t.Fatalf("the peer does not offer search: %v", p.Caps)
	}
	if p.SearchProvider != "serper" {
		t.Errorf("the peer's upstream was not recorded: %q", p.SearchProvider)
	}

	cfg, err := ResolveSearchProvider(WebSearchConfig{}, PeerProviderValue("gpu-box"))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// Drive the resolved config the way tools/websearch would: a GET to
	// {endpoint}/search with the key as a bearer.
	req, _ := http.NewRequest(http.MethodGet, cfg.Endpoint+"/search?q=test&format=json", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("search through the peer: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("peer search → %d", resp.StatusCode)
	}
	var out struct {
		Results []struct{ Title string }
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if len(out.Results) != 1 || out.Results[0].Title != "Peered Hit" {
		t.Errorf("results = %+v", out.Results)
	}
}

// The key must travel as a bearer, or the peer 401s every search. The SearXNG
// client had no auth at all until this — a public instance needs none, a peer
// does.
func TestPeerSearchRequiresTheKey(t *testing.T) {
	peerImageDB(t)
	withSearchProvider(t, "serper", func(q, prov, key, endpoint string) (string, error) {
		return "1. x\n   https://e.com\n   y", nil
	})
	MintPeerKey("mac", []string{PeerCapSearch}, 0)

	r := httptest.NewRequest(http.MethodGet, "/api/peer/v1/search?q=test", nil) // no key
	w := httptest.NewRecorder()
	HandlePeerSearch(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated search → %d, want 401", w.Code)
	}
}

// --- live resolution ---------------------------------------------------------

// searchConfigForTest points LoadWebSearchConfig at a fixed stored config and
// restores the previous loader. The stored value is what the admin form wrote;
// LoadWebSearchConfig is where the peer overlay has to happen.
func searchConfigForTest(t *testing.T, cfg WebSearchConfig) {
	t.Helper()
	prev := LoadWebSearchConfigFunc
	LoadWebSearchConfigFunc = func() WebSearchConfig { return cfg }
	t.Cleanup(func() { LoadWebSearchConfigFunc = prev })
}

// TestSearchConfigResolvesThePeerOnEveryRead — search had the nastiest version
// of the snapshot bug: a stale key fails as an EMPTY RESULT SET rather than an
// error, so every search quietly returns nothing and the agent above it reports
// that it could find no sources.
func TestSearchConfigResolvesThePeerOnEveryRead(t *testing.T) {
	restore := scratchPeerStore(t)
	defer restore()

	RootDB.Set(remotePeersTable, "den", RemotePeer{
		Name: "den", BaseURL: "https://den.example", Key: "first-key",
		Caps: []string{PeerCapSearch}})
	InvalidatePeerResolution()

	searchConfigForTest(t, WebSearchConfig{Provider: "searxng", Source: PeerProviderValue("den"),
		Endpoint: "https://den.example/api/peer/v1", APIKey: "first-key"})

	if got := LoadWebSearchConfig(); got.APIKey != "first-key" {
		t.Fatalf("initial key resolved to %q", got.APIKey)
	}

	RootDB.Set(remotePeersTable, "den", RemotePeer{
		Name: "den", BaseURL: "https://den2.example", Key: "second-key",
		Caps: []string{PeerCapSearch}})
	InvalidatePeerResolution()

	got := LoadWebSearchConfig()
	if got.APIKey != "second-key" {
		t.Errorf("after rotation the key is still %q — the config snapshotted it", got.APIKey)
	}
	if got.Endpoint != "https://den2.example/api/peer/v1" {
		t.Errorf("after a move the endpoint is still %q", got.Endpoint)
	}
	if got.Provider != "searxng" {
		t.Errorf("the resolved config stopped being a searxng config: %q", got.Provider)
	}
}

// TestALocalSearchConfigIsUntouched — a real Brave or Google config must not be
// touched by the overlay. Source is empty on every config stored before peers
// existed, and that is the case the overlay must ignore.
func TestALocalSearchConfigIsUntouched(t *testing.T) {
	restore := scratchPeerStore(t)
	defer restore()

	for _, source := range []string{"", EmbeddingProviderLocal} {
		in := WebSearchConfig{Provider: "brave", Source: source,
			Endpoint: "", APIKey: "brave-key", CostPerCall: 0.005}
		searchConfigForTest(t, in)
		if got := LoadWebSearchConfig(); got != in {
			t.Errorf("source %q: a local config was altered: %+v", source, got)
		}
	}
}

// TestAMissingSearchPeerKeepsTheLastKnownEndpoint — a deleted peer must leave a
// config that fails diagnosably rather than one that searches nowhere.
func TestAMissingSearchPeerKeepsTheLastKnownEndpoint(t *testing.T) {
	restore := scratchPeerStore(t)
	defer restore()

	in := WebSearchConfig{Provider: "searxng", Source: PeerProviderValue("gone"),
		Endpoint: "https://gone.example/api/peer/v1", APIKey: "k"}
	searchConfigForTest(t, in)
	if got := LoadWebSearchConfig(); got != in {
		t.Errorf("a deleted peer altered the config: %+v", got)
	}
}

// TestAPeerThatStoppedOfferingSearchKeepsItsKey — search spends a metered
// third-party key on the far side, so a dropped grant must not be papered over
// with whatever that peer offers now.
func TestAPeerThatStoppedOfferingSearchKeepsItsKey(t *testing.T) {
	restore := scratchPeerStore(t)
	defer restore()

	RootDB.Set(remotePeersTable, "den", RemotePeer{
		Name: "den", BaseURL: "https://den.example", Key: "rotated",
		Caps: []string{PeerCapBrowse}})
	InvalidatePeerResolution()

	searchConfigForTest(t, WebSearchConfig{Provider: "searxng", Source: PeerProviderValue("den"),
		Endpoint: "https://den.example/api/peer/v1", APIKey: "first-key"})

	if got := LoadWebSearchConfig(); got.APIKey != "first-key" {
		t.Errorf("a peer that dropped the capability still had its key applied: %q", got.APIKey)
	}
}

// The silent 401. A peer whose local record says tokens are not in use gets
// sent the static pairing key; a peer that requires exchange refuses it; and
// recovery declines for the same reason. Three declines, no explanation — an
// operator sees a 401 from another machine on every peer-backed capability at
// once, with nothing naming the cause.
//
// The failure lives entirely in the DISAGREEMENT between two records, and
// neither is wrong on its own, so the line has to name the reconciliation.
func TestARefusedStaticKeyExplainsItselfOnce(t *testing.T) {
	var logged []string
	prev := peerResolveWarned
	peerResolveWarned = map[string]string{}
	t.Cleanup(func() { peerResolveWarned = prev })

	capture := func() {
		peerResolveMu.Lock()
		defer peerResolveMu.Unlock()
		if msg := peerResolveWarned["tokens:den"]; msg != "" {
			logged = append(logged, msg)
		}
	}

	p := RemotePeer{Name: "den", Key: "pairing-code", UseTokens: false}
	req, _ := http.NewRequest(http.MethodPost, "https://den.example/api/peer/v1/embeddings", nil)

	if _, ok := renewRefusedPeerCredential(req, p); ok {
		t.Fatal("a peer not using tokens has nothing to exchange; recovery must decline")
	}
	capture()
	if len(logged) != 1 {
		t.Fatalf("expected the decline to explain itself once, got %d message(s)", len(logged))
	}
	// It has to name the FIX. A line saying only that something was refused
	// leaves the operator exactly where they were.
	for _, want := range []string{"Admin > Peers", "pairing key", "den"} {
		if !strings.Contains(logged[0], want) {
			t.Errorf("the message should mention %q: %s", want, logged[0])
		}
	}

	// A bulk ingest sends hundreds of these. Seven 401s in one second is what
	// prompted the line; seven copies of it would bury the log it appears in.
	before := len(logged)
	for i := 0; i < 5; i++ {
		renewRefusedPeerCredential(req, p)
	}
	capture()
	if len(logged) != before+1 { // capture() re-reads the same stored message
		t.Error("the warning repeated; it must be once per peer")
	}
}

// An operator who follows the instruction should see the warning stop, rather
// than having to infer that refreshing worked.
func TestAdoptingTokensClearsTheWarning(t *testing.T) {
	prev := peerResolveWarned
	peerResolveWarned = map[string]string{"tokens:den": "peer \"den\" refused our credential…"}
	t.Cleanup(func() { peerResolveWarned = prev })

	required := true
	got := adoptPeerTokenFlow(RemotePeer{Name: "den", UseTokens: false},
		PeerManifest{Token: &PeerTokenInfo{Required: required}})
	if !got {
		t.Fatal("a manifest requiring exchange must flip the record")
	}
	peerResolveMu.Lock()
	defer peerResolveMu.Unlock()
	if msg := peerResolveWarned["tokens:den"]; msg != "" {
		t.Errorf("the warning should be cleared once the peer adopts tokens, still: %q", msg)
	}
}
