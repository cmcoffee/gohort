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
	if cfg.APIKey != key {
		t.Error("the peer key was not carried into the config, so the call would 401")
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
