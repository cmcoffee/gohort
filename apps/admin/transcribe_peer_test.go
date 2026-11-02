package admin

// Transcription settings when a PEER is doing the work.
//
// The form names a peer and nothing else, so it submits no endpoint, model or
// key. Every consumer of a submitted config therefore has to RESOLVE before it
// inspects those fields: the embeddings side learned this when its Test
// button reported "endpoint is required" against a form that was showing no
// endpoint field to fill in.

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

	// The serving instance transcribes with a connector of its own.
	SetTranscribeConfig(TranscribeConfig{Enabled: true, Provider: "stt", Model: "whisper-1"})

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
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	p, err := SaveRemotePeer(t.Context(), "gpu-box", srv.URL, pk.Key)
	if err != nil {
		t.Fatalf("add peer: %v", err)
	}
	return p
}

// The picker appears once a peer offers STT, offering that peer and not this
// instance, and the form has no endpoint, model or key: those are the
// connector's or the peer's. Testing only the empty case would let a change
// that drops the picker pass.
func TestTranscribeFormShowsThePeerPicker(t *testing.T) {
	transcribePeer(t, PeerCapTranscribe)

	fields := transcribeFormFields()
	provider, ok := findField(fields, "provider")
	if !ok {
		t.Fatal("a peer offers transcription but the provider dropdown is absent")
	}
	if f, ok := findField(fields, "enabled"); !ok || f.Help != "" {
		t.Errorf("the toggle stays, with no pointer while there is a choice: %+v", f)
	}
	if len(provider.Options) != 1 || provider.Options[0].Value != PeerProviderValue("gpu-box") {
		t.Errorf("dropdown should offer the peer alone, got %+v", provider.Options)
	}
	for _, name := range []string{"endpoint", "model", "api_key"} {
		if _, ok := findField(fields, name); ok {
			t.Errorf("field %q is on the transcription form: it is the connector's or the peer's", name)
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
	if !strings.Contains(errMsg, "no transcription of its own") {
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
