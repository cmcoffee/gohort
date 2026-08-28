package core

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	resetPeerTokenCache() // see peerTestDB: the credential cache outlives the store
	InvalidatePeerResolution()
	// See peerTestDB: the consuming side's credential cache is keyed by peer
	// name and outlives the store, so every test naming its peer "gpu-box"
	// would otherwise inherit the previous one's token.
	resetPeerTokenCache()
	InvalidatePeerResolution()

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
	// Exchange is mandatory now, so a fake peer that does not serve the token
	// endpoint is not a peer any client can pair with — the same 404 a real
	// instance on an older build would answer with.
	mux.HandleFunc("/api/peer/v1/token", HandlePeerToken)
	mux.HandleFunc("/api/peer/v1/embeddings", HandlePeerEmbeddings)
	mux.HandleFunc("/api/peer/v1/audio/transcriptions", HandlePeerTranscribe)
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
// peerGrantOnly returns the single grant a fake peer server minted.
func peerGrantOnly(t *testing.T) (PeerKey, bool) {
	t.Helper()
	keys := ListPeerKeys()
	if len(keys) != 1 {
		return PeerKey{}, false
	}
	return keys[0], true
}

func TestProbeDistinguishesItsFailures(t *testing.T) {
	base, key := peerServer(t, PeerCapEmbeddings)

	// The FAR SIDE's words, because it is the only one that can tell an unknown
	// key from a spent pairing code — and those need opposite actions from the
	// reader. Replacing its answer with a guess sent an operator hunting for a
	// paste error in a code that was correct and already used.
	if _, err := ProbeRemotePeer(t.Context(), base, "not-a-real-key"); err == nil {
		t.Error("a bad key should fail")
	} else if !strings.Contains(err.Error(), "unrecognized or disabled peer key") {
		t.Errorf("bad key error should carry the far side's reason, got: %v", err)
	}

	// A SPENT code says so, and says what to do about it.
	pk, ok := peerGrantOnly(t)
	if !ok {
		t.Fatal("expected the fake server's grant")
	}
	markPeerKeyPaired(pk.ID)
	if _, err := ProbeRemotePeer(t.Context(), base, pk.Key); err == nil {
		t.Error("a spent pairing code should fail")
	} else if !strings.Contains(err.Error(), "already exchanged") {
		t.Errorf("a spent code should say it is spent, got: %v", err)
	} else if !strings.Contains(err.Error(), "Re-issue") {
		t.Errorf("a spent code should name the way out, got: %v", err)
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
	// A CREDENTIAL, not the key. The key authenticates nothing since exchange
	// became mandatory, so a config carrying it would 401 on every embed —
	// which is exactly what this used to assert, inverted by that change.
	if got.APIKey == key {
		t.Error("the config carries the raw pairing code, which authenticates nothing")
	}
	if _, live := peerKeyFromAccessToken(got.APIKey); !live {
		t.Errorf("the config's credential is not a live access token: %q", got.APIKey)
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
	resetPeerTokenCache() // see peerTestDB: the credential cache outlives the store
	InvalidatePeerResolution()

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

// What this instance believes about a peer used to be written once, at save
// time, and never revisited unless an operator opened the admin page and
// clicked Refresh. Anything that moved on the far side — a renderer reshaped, a
// capability withdrawn — stayed wrong locally for as long as nobody looked.
func TestStoredPeersAreRefreshedOnTheClock(t *testing.T) {
	reconcilersMu.Lock()
	_, registered := reconcilers["peer_refresh"]
	reconcilersMu.Unlock()
	if !registered {
		t.Error("no peer reconciler is registered — a peer's record only updates when an operator clicks Refresh")
	}

	base, key := peerServer(t, PeerCapEmbeddings)
	if _, err := SaveRemotePeer(t.Context(), "gpu-box", base, key); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Blank what the save recorded, so only a sweep can put it back.
	p, _ := GetRemotePeer("gpu-box")
	p.LastChecked, p.LastError = "", "stale failure from before"
	RootDB.Set(remotePeersTable, p.Name, p)

	RefreshRemotePeers(t.Context())

	p, ok := GetRemotePeer("gpu-box")
	if !ok {
		t.Fatal("the peer vanished during a refresh sweep")
	}
	if p.LastChecked == "" {
		t.Error("the sweep did not re-probe a stored peer")
	}
	if p.LastError != "" {
		t.Errorf("a reachable peer kept a stale error after being re-probed: %q", p.LastError)
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

// --- live resolution ---------------------------------------------------------

// TestEmbeddingConfigResolvesThePeerOnEveryRead — the bug this exists to fix. A
// rotated peer key used to leave a stored config that looked correct and
// returned 401, with nothing on either screen to say which record was stale.
func TestEmbeddingConfigResolvesThePeerOnEveryRead(t *testing.T) {
	restore := scratchPeerStore(t)
	defer restore()

	RootDB.Set(remotePeersTable, "den", RemotePeer{
		Name: "den", BaseURL: "https://den.example", Key: "first-key",
		EmbedModel: "nomic-embed-text", Caps: []string{PeerCapEmbeddings}})
	InvalidatePeerResolution()

	// The stored config is a POINTER: it names the peer and carries whatever
	// was last known, and must never be the thing that decides.
	SetEmbeddingConfig(EmbeddingConfig{Enabled: true, Provider: PeerProviderValue("den"),
		Endpoint: "https://den.example/api/peer/v1", Model: "nomic-embed-text", APIKey: "first-key"})

	if got := GetEmbeddingConfig(); got.APIKey != "first-key" {
		t.Fatalf("initial key resolved to %q", got.APIKey)
	}

	RootDB.Set(remotePeersTable, "den", RemotePeer{
		Name: "den", BaseURL: "https://den2.example", Key: "second-key",
		EmbedModel: "nomic-embed-text", Caps: []string{PeerCapEmbeddings}})
	InvalidatePeerResolution()

	got := GetEmbeddingConfig()
	if got.APIKey != "second-key" {
		t.Errorf("after rotation the key is still %q — the config snapshotted it", got.APIKey)
	}
	if got.Endpoint != "https://den2.example/api/peer/v1" {
		t.Errorf("after a move the endpoint is still %q", got.Endpoint)
	}
	// The STORED record must be untouched: resolution is a read-time overlay,
	// and writing back would reintroduce the snapshot it replaces.
	embedCfgMu.RLock()
	stored := embedCfg
	embedCfgMu.RUnlock()
	if stored.APIKey != "first-key" {
		t.Errorf("resolution wrote back into the stored config (key is now %q)", stored.APIKey)
	}
}

// TestALocalEmbeddingConfigIsUntouched — the overlay must not reach configs
// that never named a peer, which is every config stored before peers existed.
func TestALocalEmbeddingConfigIsUntouched(t *testing.T) {
	restore := scratchPeerStore(t)
	defer restore()

	for _, provider := range []string{"", EmbeddingProviderLocal} {
		in := EmbeddingConfig{Enabled: true, Provider: provider,
			Endpoint: "http://localhost:11434/api", Model: "nomic-embed-text", APIKey: "k"}
		SetEmbeddingConfig(in)
		if got := GetEmbeddingConfig(); got != in {
			t.Errorf("provider %q: a local config was altered: %+v", provider, got)
		}
	}
}

// TestAMissingPeerKeepsTheLastKnownEndpoint — blanking the config would turn
// every search into a silent no-result, which is worse than an endpoint that
// fails with a diagnosable error.
func TestAMissingPeerKeepsTheLastKnownEndpoint(t *testing.T) {
	restore := scratchPeerStore(t)
	defer restore()
	InvalidatePeerResolution()

	SetEmbeddingConfig(EmbeddingConfig{Enabled: true, Provider: PeerProviderValue("gone"),
		Endpoint: "https://gone.example/api/peer/v1", Model: "nomic-embed-text", APIKey: "k"})

	got := GetEmbeddingConfig()
	if got.Endpoint != "https://gone.example/api/peer/v1" || got.APIKey != "k" {
		t.Errorf("a deleted peer blanked the config: %+v", got)
	}
	if !got.Enabled {
		t.Error("a deleted peer silently disabled embeddings")
	}
}

// TestAPeerThatStoppedOfferingEmbeddingsDoesNotOverwrite — a peer whose grant
// was revoked must not have its (now meaningless) fields adopted.
func TestAPeerThatStoppedOfferingEmbeddingsDoesNotOverwrite(t *testing.T) {
	restore := scratchPeerStore(t)
	defer restore()

	RootDB.Set(remotePeersTable, "den", RemotePeer{
		Name: "den", BaseURL: "https://moved.example", Key: "new-key",
		Caps: []string{PeerCapImages}})
	InvalidatePeerResolution()

	SetEmbeddingConfig(EmbeddingConfig{Enabled: true, Provider: PeerProviderValue("den"),
		Endpoint: "https://den.example/api/peer/v1", APIKey: "old-key"})

	got := GetEmbeddingConfig()
	if got.Endpoint != "https://den.example/api/peer/v1" || got.APIKey != "old-key" {
		t.Errorf("a peer no longer offering embeddings was still adopted: %+v", got)
	}
}

// TestASingleModelPeerDoesNotInvalidateCachedVectors — EmbedVersion is
// model@endpoint, so overwriting a set model with the empty string a
// single-model backend reports would silently re-embed the entire corpus.
func TestASingleModelPeerDoesNotInvalidateCachedVectors(t *testing.T) {
	restore := scratchPeerStore(t)
	defer restore()

	RootDB.Set(remotePeersTable, "den", RemotePeer{
		Name: "den", BaseURL: "https://den.example", Key: "k",
		EmbedModel: "", Caps: []string{PeerCapEmbeddings}})
	InvalidatePeerResolution()

	SetEmbeddingConfig(EmbeddingConfig{Enabled: true, Provider: PeerProviderValue("den"),
		Endpoint: "https://den.example/api/peer/v1", Model: "bge-m3", APIKey: "k"})

	if got := GetEmbeddingConfig(); got.Model != "bge-m3" {
		t.Errorf("a single-model peer blanked the model to %q, which changes EmbedVersion "+
			"and re-embeds the whole corpus", got.Model)
	}
}

// scratchPeerStore swaps in an empty peer store and restores the embedding
// config, which is process-wide.
func scratchPeerStore(t *testing.T) func() {
	t.Helper()
	prevRoot := RootDB
	prevCfg := GetEmbeddingConfig()
	RootDB = &DBase{Store: kvlite.MemStore()}
	resetPeerTokenCache() // see peerTestDB: the credential cache outlives the store
	InvalidatePeerResolution()
	InvalidatePeerResolution()
	return func() {
		RootDB = prevRoot
		SetEmbeddingConfig(prevCfg)
		InvalidatePeerResolution()
	}
}

// Re-pairing an existing peer with a new code keeps its identity: same name,
// same address, fresh credentials.
//
// The recovery path for the serving side's Re-issue. Forget-and-re-add is not
// equivalent — it drops the image backends the peer contributed and every
// setting naming it — so an operator recovering from a rotation would take a
// second outage to fix the first.
func TestUpdatingAPeerKeyRePairsInPlace(t *testing.T) {
	base, key := peerServer(t, PeerCapEmbeddings)
	p, err := SaveRemotePeer(t.Context(), "gpu-box", base, key)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	first := PeerCredential(p)
	if first == "" || first == key {
		t.Fatalf("precondition: the peer should be holding a token, got %q", first)
	}

	// The far side re-issues: the old credentials die and a new code appears.
	keys := ListPeerKeys()
	if len(keys) != 1 {
		t.Fatalf("expected the fake server's one grant, got %d", len(keys))
	}
	reissued, rerr := RepairPeerKey(keys[0].ID)
	if rerr != nil {
		t.Fatalf("re-issue: %v", rerr)
	}
	if _, live := peerKeyFromAccessToken(first); live {
		t.Error("re-issuing left the old access token alive")
	}

	got, uerr := UpdateRemotePeerKey(t.Context(), "gpu-box", reissued.Key)
	if uerr != nil {
		t.Fatalf("update key: %v", uerr)
	}
	if got.Name != "gpu-box" || got.BaseURL != p.BaseURL {
		t.Errorf("re-pairing moved the peer: %q at %q", got.Name, got.BaseURL)
	}
	if cred := PeerCredential(got); cred == "" || cred == reissued.Key {
		t.Errorf("the peer is not holding a fresh token: %q", cred)
	} else if _, live := peerKeyFromAccessToken(cred); !live {
		t.Error("the credential after re-pairing is not a live access token")
	}
	// An unknown peer is a refusal, not a silent create.
	if _, err := UpdateRemotePeerKey(t.Context(), "nobody", "x"); err == nil {
		t.Error("updating the key of a peer that does not exist should be refused")
	}
}

// A re-key must not leave a window where the record still names the revoked
// code.
//
// Seen live: Update key logged "now requires credential exchange" and then
// immediately "could not renew the credential … unrecognized or disabled peer
// key". Clearing the tokens before writing the new key meant every reader in
// that window found no credential, kicked off a background renewal, read the
// record, and spent the code the far side had just revoked. The operator's
// paste was correct and the log said otherwise.
func TestARekeyNeverExposesTheOldCode(t *testing.T) {
	base, key := peerServer(t, PeerCapEmbeddings)
	if _, err := SaveRemotePeer(t.Context(), "gpu-box", base, key); err != nil {
		t.Fatalf("save: %v", err)
	}
	keys := ListPeerKeys()
	if len(keys) != 1 {
		t.Fatalf("expected one grant, got %d", len(keys))
	}
	reissued, err := RepairPeerKey(keys[0].ID)
	if err != nil {
		t.Fatalf("re-issue: %v", err)
	}

	// The instant the credential is cleared, whatever the record names IS what
	// a racing renewal will spend. Pin that it is never the dead one.
	var seen []string
	watch := func() {
		if p, ok := GetRemotePeer("gpu-box"); ok {
			if PeerCredential(p) == "" || !p.UseTokens {
				seen = append(seen, p.Key)
			}
		}
	}
	watch()
	if _, err := UpdateRemotePeerKey(t.Context(), "gpu-box", reissued.Key); err != nil {
		t.Fatalf("update key: %v", err)
	}
	watch()
	for _, k := range seen {
		if k == key {
			t.Error("the record named the revoked code while holding no credential — a renewal in that window spends it and 401s")
		}
	}
	p, _ := GetRemotePeer("gpu-box")
	if p.Key != reissued.Key {
		t.Errorf("the record kept the old key: %q", p.Key)
	}
	if _, live := peerKeyFromAccessToken(PeerCredential(p)); !live {
		t.Error("the peer is not holding a live token after the re-key")
	}
}

// A re-key must not lose a race with the renewal it provokes.
//
// Seen live, twice. Clearing the credential makes every reader notice there is
// none, and a background renewal starting in that window reads the record and
// spends the operator's brand-new pairing code — which is SINGLE USE, so the
// re-key's own probe then finds it already exchanged and reports that the far
// side "did not recognize that key". The operator's paste was correct both
// times. The lock is what makes this an operation rather than a race.
func TestARekeyIsNotRacedByTheRenewalItProvokes(t *testing.T) {
	base, key := peerServer(t, PeerCapEmbeddings)
	if _, err := SaveRemotePeer(t.Context(), "gpu-box", base, key); err != nil {
		t.Fatalf("save: %v", err)
	}
	pk, ok := peerGrantOnly(t)
	if !ok {
		t.Fatal("expected one grant")
	}
	reissued, err := RepairPeerKey(pk.ID)
	if err != nil {
		t.Fatalf("re-issue: %v", err)
	}

	// Readers hammering the credential throughout, which is what a live
	// deployment does — every page render and every capability check asks.
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				if p, ok := GetRemotePeer("gpu-box"); ok {
					_ = PeerCredential(p) // schedules a renewal whenever there is no token
				}
			}
		}
	}()
	_, uerr := UpdateRemotePeerKey(t.Context(), "gpu-box", reissued.Key)
	close(stop)
	<-done
	waitPeerTokenIdle()

	if uerr != nil {
		t.Fatalf("re-key lost the race with a renewal: %v", uerr)
	}
	p, _ := GetRemotePeer("gpu-box")
	if cred := PeerCredential(p); cred == "" || cred == reissued.Key {
		t.Errorf("the peer is not holding a token after the re-key: %q", cred)
	} else if _, live := peerKeyFromAccessToken(cred); !live {
		t.Error("the credential after a contested re-key is not live")
	}
}

// The startup burst: "unrecognized or disabled peer key", once per tool, from
// tool indexing.
//
// These go through Embed, NOT EmbedWith with a config resolved up front, and
// that is the whole point. Embed reads the config on every call, and the read
// path (resolveEmbeddingPeer, under GetEmbeddingConfig) is hot enough that it
// answers without blocking — so with no live access token it hands back the
// static key. The key stopped authenticating anything when exchange became
// mandatory: it is a pairing code, and the far side must refuse it. A test
// holding a config snapshotted while the token was alive proves nothing,
// because it never asks the question.
//
// An expired access token with the refresh token intact is the state a process
// comes back in after being down longer than a token's life.
func TestEmbedResolvesACredentialAtSendTimeNotReadTime(t *testing.T) {
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
	// resolveEmbeddingPeer + EmbedWith is exactly what Embed does — Embed is
	// EmbedWith(GetEmbeddingConfig()), and GetEmbeddingConfig is the stored
	// config put through resolveEmbeddingPeer. Composed by hand here because
	// both sides of this test are ONE process: installing a peer-backed config
	// globally would make the SERVING half believe it borrows its embeddings
	// and refuse to relay them, which is a different guard entirely.

	// Age the access token out, keeping the refresh token — coming back from
	// downtime, not losing the pairing.
	aged := loadPeerTokens("gpu-box")
	if aged.Refresh == "" {
		t.Fatal("precondition: pairing should have produced a refresh token")
	}
	aged.Expires = time.Now().Add(-time.Hour)
	storePeerTokens("gpu-box", aged)

	// What a config READ yields in this state is the pairing code — the
	// credential that used to reach the wire.
	if stale := resolveEmbeddingPeer(cfg); stale.APIKey != strings.TrimSpace(key) {
		t.Logf("note: the non-blocking read returned %q, not the static key", stale.APIKey)
	}

	// Several in a row, because the symptom was a BURST: the first exchanges,
	// the rest ride what it landed.
	for i := 0; i < 3; i++ {
		vec, err := EmbedWith(t.Context(), resolveEmbeddingPeer(cfg), "tool description to index")
		if err != nil {
			t.Fatalf("embed %d failed against a peer that was reachable throughout: %v", i+1, err)
		}
		if len(vec) != 4 {
			t.Fatalf("embed %d returned %d dimensions, want 4", i+1, len(vec))
		}
	}

	if cred := PeerCredential(mustPeer(t, "gpu-box")); cred == strings.TrimSpace(key) {
		t.Error("the peer is still presenting its pairing code as a credential")
	} else if _, live := peerKeyFromAccessToken(cred); !live {
		t.Errorf("expected a live access token after embedding, got %q", cred)
	}
}

// The state nothing could recover from: a token this side believes in and the
// far side has dropped.
//
// EnsurePeerToken returns early while the local clock says the access token is
// inside its life, which is right for expiry and wrong for every other way a
// token dies — the peer restarting and losing its table, reuse detection
// deleting the family, a revoked grant. In all of those this side keeps
// presenting a credential it believes in, the peer answers "unrecognized or
// disabled peer key" to every call, and nothing reconciles it: the renewal that
// would fix it is never attempted. Before rotation there was no such state.
//
// A 401 is the only evidence available, so it has to be the trigger.
func TestARefusedTokenIsRenewedAndTheEmbedRetried(t *testing.T) {
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
	// See the note in the test above: the read path is composed by hand
	// because this process is also the peer.

	// The far side forgets this access token while our clock still trusts it: a
	// long life on a credential that is already dead over there.
	live := loadPeerTokens("gpu-box")
	if live.Access == "" || live.Refresh == "" {
		t.Fatal("precondition: pairing should have produced both tokens")
	}
	dead := live
	dead.Access = "dead-" + live.Access
	dead.Expires = time.Now().Add(time.Hour)
	storePeerTokens("gpu-box", dead)

	// Nothing else would fix this: from here the credential looks healthy, so
	// no renewal is due and none will be attempted.
	if got := PeerCredential(mustPeer(t, "gpu-box")); got != dead.Access {
		t.Fatalf("precondition: expected the dead token to be presented, got %q", got)
	}

	vec, err := EmbedWith(t.Context(), resolveEmbeddingPeer(cfg), "an embed that meets a refused token")
	if err != nil {
		t.Fatalf("a refused token was not recovered from: %v", err)
	}
	if len(vec) != 4 {
		t.Errorf("vector length = %d, want 4", len(vec))
	}
	if got := PeerCredential(mustPeer(t, "gpu-box")); got == dead.Access {
		t.Error("the refused token is still installed — the next embed fails the same way")
	} else if _, ok := peerKeyFromAccessToken(got); !ok {
		t.Errorf("expected a live access token after recovery, got %q", got)
	}

	// The refresh token has to SURVIVE the recovery. Clearing both would put
	// the peer back on a pairing code that has already been spent, which means
	// re-pairing by hand — the exact outage this is meant to prevent.
	if loadPeerTokens("gpu-box").Refresh == "" {
		t.Error("recovery discarded the refresh token, leaving nothing to renew with")
	}
}

func mustPeer(t *testing.T, name string) RemotePeer {
	t.Helper()
	p, ok := GetRemotePeer(name)
	if !ok {
		t.Fatalf("peer %q vanished", name)
	}
	return p
}
