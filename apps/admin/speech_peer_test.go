package admin

// Speech settings when a PEER does the speaking. With a peer picked, the
// endpoint, model and key are hidden and come from the peer; the voice stays,
// chosen here from the peer's own list.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/snugforge/kvlite"
)

// speechPeer stands up a serving instance offering caps and registers it.
func speechPeer(t *testing.T, caps ...string) RemotePeer {
	t.Helper()
	prevDB, prevCfg := RootDB, media.GetSpeechConfig()
	t.Cleanup(func() { RootDB = prevDB; media.SetSpeechConfig(prevCfg) })
	RootDB = &DBase{Store: kvlite.MemStore()}
	media.SetSpeechConfig(media.SpeechConfig{Enabled: true, Endpoint: "http://127.0.0.1:1/v1", Model: "kokoro"})

	pk, err := MintPeerKey("consumer", caps, 0)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/peer/manifest", HandlePeerManifest)
	mux.HandleFunc("/api/peer/v1/token", HandlePeerToken)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	p, err := SaveRemotePeer(t.Context(), "gpu-box", srv.URL, pk.Key)
	if err != nil {
		t.Fatalf("add peer: %v", err)
	}
	return p
}

func TestSpeechFormShowsThePeerPicker(t *testing.T) {
	speechPeer(t, PeerCapSpeech)

	fields := speechFormFields()
	provider, ok := findField(fields, "provider")
	if !ok {
		t.Fatal("a peer offers speech but the provider dropdown is absent")
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
	if f, _ := findField(fields, "voice"); f.ShowWhen != "enabled" {
		t.Errorf("the voice is chosen here for a peer too, got ShowWhen %q", f.ShowWhen)
	}
}

func TestSpeechFormHidesAPeerWithoutTheGrant(t *testing.T) {
	speechPeer(t, PeerCapTranscribe)
	if _, ok := findField(speechFormFields(), "provider"); ok {
		t.Error("a peer that does not offer speech is being offered as a speech provider")
	}
	for _, f := range speechFormFields() {
		if strings.Contains(f.ShowWhen, "provider:") {
			t.Errorf("with no picker, field %q must not wait on one: %q", f.Field, f.ShowWhen)
		}
	}
}

// What the form posts with a peer picked: enabled, a provider and a voice.
func TestResolveFillsASpeechConfigWhoseFieldsWereHidden(t *testing.T) {
	p := speechPeer(t, PeerCapSpeech)

	got, err := ResolveSpeechProvider(media.SpeechConfig{Enabled: true, Voice: "am_adam"}, PeerProviderValue("gpu-box"))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Endpoint != p.SpeechURL() || got.APIKey == "" || got.Model != "kokoro" || got.Voice != "am_adam" {
		t.Errorf("resolved = %+v, want the peer's endpoint, a credential, its model and the voice kept", got)
	}
}
