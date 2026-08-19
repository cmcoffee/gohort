package core

// End to end: a consumer configured from a peer can actually transcribe through
// it. The two halves were built against the same wire format on purpose — the
// serving endpoint speaks OpenAI /audio/transcriptions because core/media's
// Transcribe already POSTs exactly that — and this is what proves they meet.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTranscribeThroughAConfiguredPeer(t *testing.T) {
	// Transcribe reaches the network through the governed dispatch, which needs
	// the secure-api store; peerServer swaps RootDB afterwards on purpose.
	peerImageDB(t)
	// The far side's real STT, behind the peer endpoint.
	prevCfg := GetTranscribeConfig()
	t.Cleanup(func() { SetTranscribeConfig(prevCfg) })
	whisper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("meeting starts at four"))
	}))
	t.Cleanup(whisper.Close)

	base, key := peerServer(t, PeerCapTranscribe)
	// peerServer swapped RootDB; install the STT config the served instance uses.
	SetTranscribeConfig(TranscribeConfig{Enabled: true, Endpoint: whisper.URL, Model: "whisper-1"})

	p, err := SaveRemotePeer(t.Context(), "gpu-box", base, key)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !p.Offers(PeerCapTranscribe) {
		t.Fatalf("the peer does not offer transcription: %v", p.Caps)
	}
	if p.TranscribeModel != "whisper-1" {
		t.Errorf("the peer's speech model was not recorded: %q", p.TranscribeModel)
	}

	// Point this instance's ordinary config at the peer, exactly as the admin
	// save path does — no peer awareness anywhere downstream.
	cfg, err := ResolveTranscribeProvider(TranscribeConfig{Enabled: true}, PeerProviderValue("gpu-box"))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !strings.HasSuffix(cfg.Endpoint, "/api/peer/v1") {
		t.Errorf("endpoint = %q, want the peer's OpenAI base", cfg.Endpoint)
	}
	// A credential, not the key — see the embedding twin of this assertion.
	if cfg.APIKey == key {
		t.Error("the config carries the raw pairing code, which authenticates nothing")
	}
	if _, live := peerKeyFromAccessToken(cfg.APIKey); !live {
		t.Errorf("the config's credential is not a live access token: %q", cfg.APIKey)
	}
	// Deliberately NOT installed globally: this process is also the serving
	// instance, and pointing its own config at the peer would trip the
	// no-relay guard. TranscribeWith is what a resolved config is for.
	text, err := TranscribeWith(t.Context(), cfg, []byte("fake-audio"), "note.m4a")
	if err != nil {
		t.Fatalf("transcribing through the peer failed: %v", err)
	}
	if strings.TrimSpace(text) != "meeting starts at four" {
		t.Errorf("transcript = %q", text)
	}
}

// An unknown peer is an error rather than a silent fall back to local: falling
// back would send the audio somewhere the operator did not choose.
func TestResolveTranscribeProviderRefusesAnUnknownPeer(t *testing.T) {
	peerImageDB(t)
	if _, err := ResolveTranscribeProvider(TranscribeConfig{}, PeerProviderValue("ghost")); err == nil {
		t.Fatal("an unknown peer resolved silently")
	}
	// And a peer that exists but does not offer STT is refused by name.
	base, key := peerServer(t, PeerCapEmbeddings)
	if _, err := SaveRemotePeer(t.Context(), "embed-only", base, key); err != nil {
		t.Fatalf("save: %v", err)
	}
	_, err := ResolveTranscribeProvider(TranscribeConfig{}, PeerProviderValue("embed-only"))
	if err == nil || !strings.Contains(err.Error(), "does not offer transcription") {
		t.Errorf("a peer without the grant should be refused by name, got %v", err)
	}
}

// --- live resolution ---------------------------------------------------------

// TestTranscribeConfigResolvesThePeerOnEveryRead — transcription used to
// snapshot the peer's endpoint and key at save time, so rotating that key left
// a config that looked correct and answered 401. Embeddings had the same bug
// and was fixed; this is the same fix, and it is the precondition for peer
// credentials that rotate on their own.
func TestTranscribeConfigResolvesThePeerOnEveryRead(t *testing.T) {
	restore := scratchPeerStore(t)
	defer restore()
	prev := GetTranscribeConfig()
	defer SetTranscribeConfig(prev)

	RootDB.Set(remotePeersTable, "den", RemotePeer{
		Name: "den", BaseURL: "https://den.example", Key: "first-key",
		TranscribeModel: "whisper-1", Caps: []string{PeerCapTranscribe}})
	InvalidatePeerResolution()

	SetTranscribeConfig(TranscribeConfig{Enabled: true, Provider: PeerProviderValue("den"),
		Endpoint: "https://den.example/api/peer/v1", Model: "whisper-1", APIKey: "first-key"})

	if got := GetTranscribeConfig(); got.APIKey != "first-key" {
		t.Fatalf("initial key resolved to %q", got.APIKey)
	}

	RootDB.Set(remotePeersTable, "den", RemotePeer{
		Name: "den", BaseURL: "https://den2.example", Key: "second-key",
		TranscribeModel: "whisper-1", Caps: []string{PeerCapTranscribe}})
	InvalidatePeerResolution()

	got := GetTranscribeConfig()
	if got.APIKey != "second-key" {
		t.Errorf("after rotation the key is still %q — the config snapshotted it", got.APIKey)
	}
	if got.Endpoint != "https://den2.example/api/peer/v1" {
		t.Errorf("after a move the endpoint is still %q", got.Endpoint)
	}
}

// TestALocalTranscribeConfigIsUntouched — the overlay must not reach a config
// that never named a peer, which is every config stored before peers existed.
func TestALocalTranscribeConfigIsUntouched(t *testing.T) {
	restore := scratchPeerStore(t)
	defer restore()
	prev := GetTranscribeConfig()
	defer SetTranscribeConfig(prev)

	for _, provider := range []string{"", EmbeddingProviderLocal} {
		in := TranscribeConfig{Enabled: true, Provider: provider,
			Endpoint: "http://localhost:8080/v1", Model: "whisper-1", APIKey: "k"}
		SetTranscribeConfig(in)
		if got := GetTranscribeConfig(); got != in {
			t.Errorf("provider %q: a local config was altered: %+v", provider, got)
		}
	}
}

// TestAMissingPeerKeepsTheLastKnownTranscribeEndpoint — blanking it would read
// as "transcription is switched off" rather than "this peer is gone".
func TestAMissingPeerKeepsTheLastKnownTranscribeEndpoint(t *testing.T) {
	restore := scratchPeerStore(t)
	defer restore()
	prev := GetTranscribeConfig()
	defer SetTranscribeConfig(prev)

	in := TranscribeConfig{Enabled: true, Provider: PeerProviderValue("gone"),
		Endpoint: "https://gone.example/api/peer/v1", Model: "whisper-1", APIKey: "k"}
	SetTranscribeConfig(in)
	if got := GetTranscribeConfig(); got != in {
		t.Errorf("a deleted peer altered the config: %+v", got)
	}
}

// TestAPeerThatStoppedOfferingTranscriptionKeepsItsKey — a capability dropped
// from the far side must not silently repoint audio somewhere else.
func TestAPeerThatStoppedOfferingTranscriptionKeepsItsKey(t *testing.T) {
	restore := scratchPeerStore(t)
	defer restore()
	prev := GetTranscribeConfig()
	defer SetTranscribeConfig(prev)

	RootDB.Set(remotePeersTable, "den", RemotePeer{
		Name: "den", BaseURL: "https://den.example", Key: "rotated",
		Caps: []string{PeerCapEmbeddings}})
	InvalidatePeerResolution()

	SetTranscribeConfig(TranscribeConfig{Enabled: true, Provider: PeerProviderValue("den"),
		Endpoint: "https://den.example/api/peer/v1", Model: "whisper-1", APIKey: "first-key"})

	if got := GetTranscribeConfig(); got.APIKey != "first-key" {
		t.Errorf("a peer that dropped the capability still had its key applied: %q", got.APIKey)
	}
}
