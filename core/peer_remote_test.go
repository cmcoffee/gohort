package core

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// peerServer stands up a real serving instance backed by a fake embedder, and
// points a fresh RootDB at it — so the consumer tests exercise the actual
// manifest handler rather than a hand-written stub of it.
func peerServer(t *testing.T, caps ...string) (base, key string) {
	t.Helper()
	prevDB, prevCfg := RootDB, GetEmbeddingConfig()
	t.Cleanup(func() { RootDB = prevDB; SetEmbeddingConfig(prevCfg) })
	RootDB = &DBase{Store: kvlite.MemStore()}

	// A local embedder for the manifest's dimension probe to reach.
	embedder := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3,0.4]}]}`))
	}))
	t.Cleanup(embedder.Close)
	SetEmbeddingConfig(EmbeddingConfig{Enabled: true, Endpoint: embedder.URL, Model: "nomic-embed-text"})

	pk, err := MintPeerKey("consumer", caps, 0)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/peer/manifest", HandlePeerManifest)
	mux.HandleFunc("/api/peer/v1/embeddings", HandlePeerEmbeddings)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, pk.Key
}

// Adding a peer probes it, and what gets stored is what the remote actually
// SERVES and this key was GRANTED — the intersection. Storing the raw grant
// would put a capability in the picker that fails at first use.
func TestSaveRemotePeerStoresTheUsableIntersection(t *testing.T) {
	unbuilt := anUnservedCap(t)
	base, key := peerServer(t, PeerCapEmbeddings, unbuilt)

	p, err := SaveRemotePeer(t.Context(), "gpu-box", base, key)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !p.Offers(PeerCapEmbeddings) {
		t.Error("embeddings is served and granted but not stored as usable")
	}
	// Granted, but this build does not serve it.
	if p.Offers(unbuilt) {
		t.Errorf("%s is granted but unserved — it must not be offered as usable: %q", unbuilt, p.Caps)
	}
	if p.EmbedModel != "nomic-embed-text" {
		t.Errorf("embed model = %q, want the remote's", p.EmbedModel)
	}
	if p.EmbedDim != 4 {
		t.Errorf("embed dim = %d, want 4 from the probe", p.EmbedDim)
	}
}

// A key that can use nothing is refused at add time rather than stored as a
// peer that looks fine and does nothing.
func TestSaveRemotePeerRefusesAKeyThatGrantsNothingUsable(t *testing.T) {
	base, key := peerServer(t, anUnservedCap(t)) // granted, but unserved

	_, err := SaveRemotePeer(t.Context(), "gpu-box", base, key)
	if err == nil {
		t.Fatal("a peer offering nothing usable should be refused")
	}
	if !strings.Contains(err.Error(), "can use nothing") {
		t.Errorf("the error should say the key can use nothing there, got: %v", err)
	}
}

// The probe's failures have to be distinguishable — an operator seeing "could
// not reach" goes to the network, "did not recognize that key" goes to the
// other instance's key list. A single "failed to connect" sends them to both.
func TestProbeDistinguishesItsFailures(t *testing.T) {
	base, key := peerServer(t, PeerCapEmbeddings)

	if _, err := ProbeRemotePeer(t.Context(), base, "not-a-real-key"); err == nil {
		t.Error("a bad key should fail")
	} else if !strings.Contains(err.Error(), "did not recognize that key") {
		t.Errorf("bad key error should name the key, got: %v", err)
	}

	if _, err := ProbeRemotePeer(t.Context(), "gpu-box.example", key); err == nil {
		t.Error("a schemeless address should fail")
	} else if !strings.Contains(err.Error(), "http://") {
		t.Errorf("schemeless error should name the scheme, got: %v", err)
	}

	if _, err := ProbeRemotePeer(t.Context(), base, "  "); err == nil {
		t.Error("a blank key should fail")
	}
}

// An operator copying from the serving instance's help card brings a path with
// them. Trimming it beats an error about a URL that looks correct.
func TestNormalizePeerBaseURLTrimsPastedPaths(t *testing.T) {
	for _, in := range []string{
		"https://host.example",
		"https://host.example/",
		"https://host.example/api/peer",
		"https://host.example/api/peer/",
		"https://host.example/api/peer/v1",
		"https://host.example/api/peer/manifest",
		"https://host.example/api/peer/v1/embeddings",
	} {
		if got := NormalizePeerBaseURL(in); got != "https://host.example" {
			t.Errorf("normalize(%q) = %q", in, got)
		}
	}
}

// Selecting a peer must produce a COMPLETE ordinary config. The whole design
// rests on this: resolve at save time so Embed, EmbedVersion and the vector
// store never learn that peers exist.
func TestResolveEmbeddingProviderFillsFromThePeer(t *testing.T) {
	base, key := peerServer(t, PeerCapEmbeddings)
	p, err := SaveRemotePeer(t.Context(), "gpu-box", base, key)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	// The manual fields are hidden while a peer is picked, so they arrive stale.
	got, err := ResolveEmbeddingProvider(EmbeddingConfig{
		Enabled: true, Provider: PeerProviderValue("gpu-box"),
		Endpoint: "http://leftover:11434/api", Model: "stale-model", APIKey: "stale",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Endpoint != p.EmbeddingsURL() {
		t.Errorf("endpoint = %q, want the peer's %q", got.Endpoint, p.EmbeddingsURL())
	}
	if got.Model != "nomic-embed-text" {
		t.Errorf("model = %q, want the peer's", got.Model)
	}
	if got.APIKey != key {
		t.Error("the peer key was not carried into the config, so every embed would 401")
	}
	if !strings.HasSuffix(got.Endpoint, "/api/peer/v1") {
		t.Errorf("endpoint %q is not the peer embedding base", got.Endpoint)
	}
}

// With a peer picked, the endpoint/model/key fields are HIDDEN, so the form
// submits them empty. Every consumer of a submitted config has to resolve
// before it inspects those fields.
//
// The Test-embed button did not, and reported "endpoint is required" against a
// form that was showing no endpoint field to fill in — a correct configuration
// failing its own connectivity check, with the remedy invisible.
func TestResolveFillsAConfigWhoseFieldsWereHidden(t *testing.T) {
	base, key := peerServer(t, PeerCapEmbeddings)
	if _, err := SaveRemotePeer(t.Context(), "gpu-box", base, key); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Exactly what the form posts when a peer is selected: enabled, a provider,
	// and nothing else.
	got, err := ResolveEmbeddingProvider(EmbeddingConfig{
		Enabled: true, Provider: PeerProviderValue("gpu-box"),
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Endpoint == "" {
		t.Error("endpoint still empty after resolving — any caller checking it will refuse a valid peer")
	}
	if got.APIKey == "" {
		t.Error("key still empty after resolving — the embed would 401")
	}
	if got.Model == "" {
		t.Error("model still empty after resolving")
	}
}

// An unknown peer is an error, never a silent fall back to local. Falling back
// would point the vector store at a DIFFERENT embedder than the one selected,
// and mixing spaces degrades every comparison without failing.
func TestResolveEmbeddingProviderRefusesAnUnknownPeer(t *testing.T) {
	prev := RootDB
	t.Cleanup(func() { RootDB = prev })
	RootDB = &DBase{Store: kvlite.MemStore()}

	cfg := EmbeddingConfig{Enabled: true, Provider: PeerProviderValue("ghost"),
		Endpoint: "http://local:11434/api", Model: "local-model"}
	got, err := ResolveEmbeddingProvider(cfg)
	if err == nil {
		t.Fatalf("an unknown peer should be refused, got config %+v", got)
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("the error should name the missing peer, got: %v", err)
	}
}

// Local and blank both mean "this instance", and neither may disturb the typed
// fields. Blank is what every config written before peers existed says.
func TestResolveEmbeddingProviderLeavesLocalAlone(t *testing.T) {
	for _, provider := range []string{"", EmbeddingProviderLocal} {
		cfg := EmbeddingConfig{Enabled: true, Provider: provider,
			Endpoint: "http://localhost:11434/api", Model: "nomic-embed-text", APIKey: "k"}
		got, err := ResolveEmbeddingProvider(cfg)
		if err != nil {
			t.Fatalf("provider %q: %v", provider, err)
		}
		if got.Endpoint != cfg.Endpoint || got.Model != cfg.Model || got.APIKey != cfg.APIKey {
			t.Errorf("provider %q disturbed the local config: %+v", provider, got)
		}
		if got.Provider != EmbeddingProviderLocal {
			t.Errorf("provider %q should normalize to %q, got %q", provider, EmbeddingProviderLocal, got.Provider)
		}
	}
}

// End to end: a consumer configured from a peer can actually embed through it.
// The two halves were built against the same wire format on purpose; this is
// what proves they meet.
func TestEmbedThroughAConfiguredPeer(t *testing.T) {
	base, key := peerServer(t, PeerCapEmbeddings)
	if _, err := SaveRemotePeer(t.Context(), "gpu-box", base, key); err != nil {
		t.Fatalf("save: %v", err)
	}
	cfg, err := ResolveEmbeddingProvider(EmbeddingConfig{
		Enabled: true, Provider: PeerProviderValue("gpu-box"),
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	vec, err := EmbedWith(t.Context(), cfg, "hello from the consumer")
	if err != nil {
		t.Fatalf("embedding through the peer failed: %v", err)
	}
	if len(vec) != 4 {
		t.Errorf("vector length = %d, want the 4 the remote embedder returns", len(vec))
	}
}

// Forgetting a peer must not silently disable an embedder already configured
// from it — the endpoint and key were copied in at save time, and cutting them
// mid-conversation is worse than leaving a stale reference visible.
func TestForgettingAPeerLeavesConfiguredEmbeddingsWorking(t *testing.T) {
	base, key := peerServer(t, PeerCapEmbeddings)
	if _, err := SaveRemotePeer(t.Context(), "gpu-box", base, key); err != nil {
		t.Fatalf("save: %v", err)
	}
	cfg, _ := ResolveEmbeddingProvider(EmbeddingConfig{
		Enabled: true, Provider: PeerProviderValue("gpu-box"),
	})
	if !DeleteRemotePeer("gpu-box") {
		t.Fatal("delete failed")
	}
	if _, err := EmbedWith(t.Context(), cfg, "still working?"); err != nil {
		t.Errorf("an already-configured peer embedder stopped working when the peer was forgotten: %v", err)
	}
	// And it is gone from the choices.
	if len(PeersOffering(PeerCapEmbeddings)) != 0 {
		t.Error("a forgotten peer is still offered as a choice")
	}
}
