package admin

// Transcription settings when a PEER is doing the work.
//
// With a peer picked, the endpoint/model/key fields are hidden, so the form
// submits them empty. Every consumer of a submitted config therefore has to
// RESOLVE before it inspects those fields — the embeddings side learned this
// when its Test button reported "endpoint is required" against a form that was
// showing no endpoint field to fill in.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// transcribePeer stands up a serving instance offering STT and registers it.
func transcribePeer(t *testing.T, caps ...string) RemotePeer {
	t.Helper()
	prevDB, prevCfg := RootDB, GetTranscribeConfig()
	t.Cleanup(func() { RootDB = prevDB; SetTranscribeConfig(prevCfg) })
	RootDB = &DBase{Store: kvlite.MemStore()}

	whisper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	t.Cleanup(whisper.Close)
	SetTranscribeConfig(TranscribeConfig{Enabled: true, Endpoint: whisper.URL, Model: "whisper-1"})

	pk, err := MintPeerKey("consumer", caps, 0)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/peer/manifest", HandlePeerManifest)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	p, err := SaveRemotePeer(t.Context(), "gpu-box", srv.URL, pk.Key)
	if err != nil {
		t.Fatalf("add peer: %v", err)
	}
	return p
}

// The picker appears only when a peer actually offers STT, and the manual
// fields hide behind it — same conditional shape as embeddings, and testing
// only the empty case would let a change that drops the clause pass.
func TestTranscribeFormShowsThePeerPicker(t *testing.T) {
	transcribePeer(t, PeerCapTranscribe)

	fields := transcribeFormFields()
	provider, ok := findField(fields, "provider")
	if !ok {
		t.Fatal("a peer offers transcription but the provider dropdown is absent")
	}
	if len(provider.Options) < 2 {
		t.Errorf("dropdown should offer local plus the peer, got %+v", provider.Options)
	}
	for _, name := range []string{"endpoint", "model", "api_key"} {
		f, _ := findField(fields, name)
		if !strings.Contains(f.ShowWhen, "provider:local") {
			t.Errorf("field %q must hide while a peer is selected, got ShowWhen %q", name, f.ShowWhen)
		}
	}
}

// A peer that offers only embeddings must not appear in the STT picker at all.
func TestTranscribeFormHidesAPeerWithoutTheGrant(t *testing.T) {
	transcribePeer(t, PeerCapEmbeddings)
	if _, ok := findField(transcribeFormFields(), "provider"); ok {
		t.Error("a peer that does not offer transcription is being offered as an STT provider")
	}
}

// Resolving is what the Test button must do before it looks at Endpoint. This
// pins the shape the form actually posts when a peer is selected: enabled, a
// provider, and nothing else.
func TestResolveFillsATranscribeConfigWhoseFieldsWereHidden(t *testing.T) {
	p := transcribePeer(t, PeerCapTranscribe)

	got, err := ResolveTranscribeProvider(TranscribeConfig{Enabled: true}, PeerProviderValue("gpu-box"))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Endpoint == "" {
		t.Fatal("endpoint still empty after resolving — the Test button reports \"endpoint is required\" for a valid peer")
	}
	if got.Endpoint != p.TranscribeURL() {
		t.Errorf("endpoint = %q, want the peer's %q", got.Endpoint, p.TranscribeURL())
	}
	if got.APIKey == "" {
		t.Error("key still empty after resolving — every transcription would 401")
	}
	if got.Model != "whisper-1" {
		t.Errorf("model = %q, want the peer's", got.Model)
	}
}

// The Test button, for a peer. Each failure has to name its own remedy: a
// status code from the wrong URL cannot tell "your key was revoked" from "you
// never granted transcribe" from "that instance has no whisper configured".
func TestPeerTranscribeTestResultNamesEachFailure(t *testing.T) {
	// Granted and configured — the happy path reports the model.
	p := transcribePeer(t, PeerCapTranscribe)
	ok, msg, errMsg := peerTranscribeTestResult(t.Context(), p)
	if !ok {
		t.Fatalf("a granted, configured peer failed its test: %s", errMsg)
	}
	if !strings.Contains(msg, "gpu-box") || !strings.Contains(msg, "whisper-1") {
		t.Errorf("the success message should name the peer and its model: %q", msg)
	}

	// Reachable, but the grant is missing. This is the case an operator hits
	// straight after a capability ships, so it has to say what to go and do.
	q := transcribePeer(t, PeerCapEmbeddings)
	ok, _, errMsg = peerTranscribeTestResult(t.Context(), q)
	if ok {
		t.Fatal("a peer without the transcribe grant passed its test")
	}
	if !strings.Contains(errMsg, "grant") || !strings.Contains(errMsg, "Refresh") {
		t.Errorf("the failure should name the remedy on both sides: %q", errMsg)
	}

	// Granted, but the far side has no STT of its own — a different problem
	// with a different fix, and the manifest is what distinguishes them.
	r := transcribePeer(t, PeerCapTranscribe)
	SetTranscribeConfig(TranscribeConfig{Enabled: false})
	ok, _, errMsg = peerTranscribeTestResult(t.Context(), r)
	if ok {
		t.Fatal("a peer with no STT configured passed its test")
	}
	if !strings.Contains(errMsg, "no transcription endpoint") {
		t.Errorf("the failure should distinguish a missing endpoint from a missing grant: %q", errMsg)
	}

	// Unreachable is the probe's own error, unmodified.
	dead := RemotePeer{Name: "gone", BaseURL: "http://127.0.0.1:1", Key: "k"}
	if ok, _, errMsg = peerTranscribeTestResult(t.Context(), dead); ok {
		t.Error("an unreachable peer passed its test")
	} else if errMsg == "" {
		t.Error("an unreachable peer produced no explanation")
	}
}
