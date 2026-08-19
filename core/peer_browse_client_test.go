package core

// Choosing where pages render, and having every caller follow.
//
// The routing is a swap on BrowserFetchFunc rather than a new call path, so
// browse_page, the sandbox hook, and find_image's render escalation all obey
// the setting without any of them learning that peers exist.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrowseRoutesToThePeerAndBackToLocal(t *testing.T) {
	peerImageDB(t)
	prevFetch, prevCfg := BrowserFetchFunc, LoadBrowseConfig()
	t.Cleanup(func() { BrowserFetchFunc = prevFetch; SetBrowseConfig(prevCfg) })

	localCalls := 0
	RegisterBrowserFetch(func(url string, maxChars int) (string, error) {
		localCalls++
		return "local render", nil
	})
	RootDB.Set(remotePeersTable, "gpu-box", RemotePeer{
		Name: "gpu-box", BaseURL: "https://gpu.example", Key: "k", Caps: []string{PeerCapBrowse},
	})

	// Default: local.
	if _, err := BrowserFetchFunc("https://example.com", 100); err != nil {
		t.Fatalf("local browse: %v", err)
	}
	if localCalls != 1 {
		t.Fatalf("default should render locally, local calls = %d", localCalls)
	}

	// Point at the peer. Every existing caller follows, because the variable
	// they all call is the one that changed — that is the whole design.
	SetBrowseConfig(BrowseConfig{Source: PeerProviderValue("gpu-box")})
	if p, ok := BrowsePeer(); !ok || p.Name != "gpu-box" {
		t.Fatalf("the peer is not selected for rendering (ok=%v)", ok)
	}
	// The local browser must now be bypassed. It answers unreachable-host
	// rather than rendering, which is the proof: the call left this machine.
	if _, err := BrowserFetchFunc("https://example.com/article", 100); err == nil {
		t.Error("browsing succeeded against an unreachable peer — the call never left, so routing did not happen")
	}
	if localCalls != 1 {
		t.Errorf("the LOCAL browser was used while a peer was selected (calls = %d)", localCalls)
	}

	// Back to local.
	SetBrowseConfig(BrowseConfig{Source: EmbeddingProviderLocal})
	if _, err := BrowserFetchFunc("https://example.com", 100); err != nil {
		t.Fatal(err)
	}
	if localCalls != 2 {
		t.Errorf("switching back to local did not restore the local browser (calls = %d)", localCalls)
	}
}

// The client half against a REAL serving endpoint. Split from the routing test
// because a single process cannot both borrow rendering and serve it — the
// relay guard refuses, correctly — so this drives peerBrowseFetch directly
// while the global config stays local.
func TestPeerBrowseClientRendersOnTheFarSide(t *testing.T) {
	peerImageDB(t)
	prevFetch := BrowserFetchFunc
	t.Cleanup(func() { BrowserFetchFunc = prevFetch })

	farSide := 0
	RegisterBrowserFetch(func(url string, maxChars int) (string, error) {
		farSide++
		return "rendered on the far side", nil
	})
	pk, _ := MintPeerKey("consumer", []string{PeerCapBrowse}, 0)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/peer/v1/browse", HandlePeerBrowse)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	p := pairedPeer(t, RemotePeer{Name: "gpu-box", BaseURL: srv.URL, Key: pk.Key, Caps: []string{PeerCapBrowse}}, pk)
	out, err := peerBrowseFetch(p, "https://example.com/article", 500)
	if err != nil {
		t.Fatalf("peer browse: %v", err)
	}
	if farSide != 1 {
		t.Errorf("the far side rendered %d times, want 1", farSide)
	}
	if out != "rendered on the far side" {
		t.Errorf("text = %q", out)
	}
}

// A peer selection that has gone stale — forgotten, or its grant pulled — must
// fall back to the local browser rather than failing every browse.
func TestBrowseFallsBackWhenThePeerStopsOffering(t *testing.T) {
	peerImageDB(t)
	prevFetch, prevCfg := BrowserFetchFunc, LoadBrowseConfig()
	t.Cleanup(func() { BrowserFetchFunc = prevFetch; SetBrowseConfig(prevCfg) })

	local := 0
	RegisterBrowserFetch(func(url string, maxChars int) (string, error) { local++; return "local", nil })
	RootDB.Set(remotePeersTable, "gpu-box", RemotePeer{
		Name: "gpu-box", BaseURL: "https://gone.example", Key: "k",
		Caps: []string{PeerCapEmbeddings}, // browse no longer granted
	})
	SetBrowseConfig(BrowseConfig{Source: PeerProviderValue("gpu-box")})

	if _, err := BrowserFetchFunc("https://example.com", 100); err != nil {
		t.Fatalf("browse: %v", err)
	}
	if local != 1 {
		t.Error("a peer that no longer grants browsing should fall back to the local browser, not fail")
	}
}

// The non-public-host guard applies to the peer path too, before anything
// leaves — an agent asking for a LAN address gets a straight answer rather than
// a round trip and a remote 400.
func TestPeerBrowseClientRefusesPrivateHostsLocally(t *testing.T) {
	peerImageDB(t)
	prevCfg := LoadBrowseConfig()
	t.Cleanup(func() { SetBrowseConfig(prevCfg) })
	RootDB.Set(remotePeersTable, "gpu-box", RemotePeer{
		Name: "gpu-box", BaseURL: "https://gpu.example", Key: "k", Caps: []string{PeerCapBrowse},
	})
	p, _ := GetRemotePeer("gpu-box")

	if _, err := peerBrowseFetch(p, "http://192.168.1.1/", 100); err == nil {
		t.Error("a private address was sent to the peer")
	}
}

// Borrowed rendering is not ours to sub-let: an instance that browses on a peer
// must neither advertise browsing nor serve it, or A→B→A becomes a loop.
func TestABorrowingInstanceDoesNotServeBrowse(t *testing.T) {
	peerImageDB(t)
	prevFetch, prevCfg := BrowserFetchFunc, LoadBrowseConfig()
	t.Cleanup(func() { BrowserFetchFunc = prevFetch; SetBrowseConfig(prevCfg) })
	RegisterBrowserFetch(func(url string, maxChars int) (string, error) { return "x", nil })
	RootDB.Set(remotePeersTable, "upstream", RemotePeer{
		Name: "upstream", BaseURL: "https://up.example", Key: "k", Caps: []string{PeerCapBrowse},
	})
	SetBrowseConfig(BrowseConfig{Source: PeerProviderValue("upstream")})

	if peerBrowseServed() {
		t.Error("an instance that borrows rendering still advertises it")
	}
	pk, _ := MintPeerKey("mac", []string{PeerCapBrowse}, 0)
	body, _ := json.Marshal(map[string]any{"url": "https://example.com"})
	r := httptest.NewRequest(http.MethodPost, "/api/peer/v1/browse", strings.NewReader(string(body)))
	r.Header.Set(peerKeyHeader, peerAuth(t, pk))
	w := httptest.NewRecorder()
	HandlePeerBrowse(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("relay → %d, want 503: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "will not relay") {
		t.Errorf("the refusal should say it will not relay: %s", w.Body.String())
	}
}
