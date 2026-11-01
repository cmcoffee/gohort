package core

// Speech over the peer link: text in, audio out, in the OpenAI /audio/speech
// shape core/media's SpeakWith already sends, with the /audio/voices list
// beside it so a consuming instance's voice pickers fill in.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/media"
)

// peerFakeMP3 is enough of an mp3 for SpeakWith to take it as audio.
var peerFakeMP3 = []byte("ID3\x04\x00\x00\x00\x00\x00\x00spoken-audio")

// ttsCalls counts every request the fake speech server has had, so a refusal
// can prove the server was never reached.
var ttsCalls int

// ttsServer stands up a fake OpenAI-compatible speech server, points this
// instance at it through a connector, and returns what it was last asked.
func ttsServer(t *testing.T) *map[string]string {
	t.Helper()
	asked := map[string]string{}
	ttsCalls = 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ttsCalls++
		if r.URL.Path == "/audio/voices" {
			w.Write([]byte(`{"voices":["af_heart","am_adam"]}`))
			return
		}
		json.NewDecoder(r.Body).Decode(&asked)
		w.Write(peerFakeMP3)
	}))
	t.Cleanup(srv.Close)
	useConnectorSpeaker(t, srv.URL, "kokoro", "af_heart")
	return &asked
}

// useConnectorSpeaker points speech at a service connector at endpoint,
// standing in for core/service (which imports this package): it posts the
// text, model and voice as JSON to endpoint/audio/speech, as the connector
// the migration writes does, and reads the voice list from
// endpoint/audio/voices.
func useConnectorSpeaker(t *testing.T, endpoint, model, voice string) {
	t.Helper()
	savedSpeak, savedVoices, savedCfg := media.SpeechService, media.SpeechVoicesService, media.GetSpeechConfig()
	t.Cleanup(func() {
		media.SpeechService, media.SpeechVoicesService = savedSpeak, savedVoices
		media.SetSpeechConfig(savedCfg)
	})
	media.SpeechService = func(ctx context.Context, _, text, v string) ([]byte, string, error) {
		b, _ := json.Marshal(map[string]string{"input": text, "model": model, "voice": v})
		req, _ := http.NewRequestWithContext(ctx, "POST", endpoint+"/audio/speech", bytes.NewReader(b))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, "", err
		}
		defer resp.Body.Close()
		audio, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return nil, "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, audio)
		}
		return audio, ".mp3", nil
	}
	media.SpeechVoicesService = func(ctx context.Context, _ string) ([]media.Voice, error) {
		req, _ := http.NewRequestWithContext(ctx, "GET", endpoint+"/audio/voices", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		var body struct{ Voices []json.RawMessage }
		json.NewDecoder(resp.Body).Decode(&body)
		return media.ReadVoices(body.Voices), nil
	}
	media.SetSpeechConfig(media.SpeechConfig{Enabled: true, Provider: media.Choice(media.JobSpeak, "voice"), Model: model, Voice: voice})
}

func speechRequest(key, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/peer/v1/audio/speech", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(peerKeyHeader, key)
	return r
}

func TestPeerSpeechReturnsTheAudio(t *testing.T) {
	peerImageDB(t)
	asked := ttsServer(t)
	pk, _ := MintPeerKey("mac", []string{PeerCapSpeech}, 0)

	w := httptest.NewRecorder()
	handlePeerSpeech(w, speechRequest(peerAuth(t, pk), `{"model":"other","voice":"am_adam","input":"Good morning."}`))
	if w.Code != http.StatusOK {
		t.Fatalf("speech → %d, want 200: %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Content-Type") != "audio/mpeg" || w.Body.String() != string(peerFakeMP3) {
		t.Errorf("want the server's audio as audio/mpeg, got %q %q", w.Header().Get("Content-Type"), w.Body.String())
	}
	got := *asked
	if got["input"] != "Good morning." || got["voice"] != "am_adam" {
		t.Errorf("the words and the caller's voice should reach the server: %v", got)
	}
	if got["model"] != "kokoro" {
		t.Errorf("the model is this instance's, not the caller's: %v", got)
	}

	// No voice asked for is this instance's own.
	handlePeerSpeech(httptest.NewRecorder(), speechRequest(peerAuth(t, pk), `{"input":"Hi."}`))
	if (*asked)["voice"] != "af_heart" {
		t.Errorf("no voice should be this instance's: %v", *asked)
	}
}

func TestPeerSpeechRefusals(t *testing.T) {
	peerImageDB(t)
	ttsServer(t)
	granted, _ := MintPeerKey("mac", []string{PeerCapSpeech}, 0)
	other, _ := MintPeerKey("pc", []string{PeerCapTranscribe}, 0)

	cases := []struct {
		name string
		r    *http.Request
		code int
	}{
		{"a key without the grant", speechRequest(peerAuth(t, other), `{"input":"x"}`), http.StatusForbidden},
		{"nothing to say", speechRequest(peerAuth(t, granted), `{"input":"  "}`), http.StatusBadRequest},
		{"not JSON", speechRequest(peerAuth(t, granted), `input=x`), http.StatusBadRequest},
		{"a body past the cap", speechRequest(peerAuth(t, granted), `{"input":"`+strings.Repeat("a", maxPeerSpeechBytes)+`"}`), http.StatusBadRequest},
		{"GET", httptest.NewRequest(http.MethodGet, "/api/peer/v1/audio/speech", nil), http.StatusMethodNotAllowed},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		handlePeerSpeech(w, c.r)
		if w.Code != c.code {
			t.Errorf("%s → %d, want %d: %s", c.name, w.Code, c.code, w.Body.String())
		}
	}
	if ttsCalls != 0 {
		t.Errorf("a refused request still reached the speech server %d time(s)", ttsCalls)
	}

	media.SetSpeechConfig(media.SpeechConfig{Enabled: false, Endpoint: "http://127.0.0.1:1/v1"})
	w := httptest.NewRecorder()
	handlePeerSpeech(w, speechRequest(peerAuth(t, granted), `{"input":"x"}`))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "disabled") {
		t.Errorf("speech off here → %d %s, want 503 saying so", w.Code, w.Body.String())
	}
}

// Relaying makes A to B to A a loop neither side can see, and it fails as a
// hang rather than an error.
func TestPeerSpeechWillNotRelayABorrowedSpeechServer(t *testing.T) {
	peerImageDB(t)
	ttsServer(t)
	pk, _ := MintPeerKey("mac", []string{PeerCapSpeech}, 0)
	media.SetSpeechConfig(media.SpeechConfig{Enabled: true, Endpoint: "https://other.example/api/peer/v1"})

	w := httptest.NewRecorder()
	handlePeerSpeech(w, speechRequest(peerAuth(t, pk), `{"input":"x"}`))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "relay") {
		t.Errorf("speech relay → %d %s, want 503 saying it will not relay", w.Code, w.Body.String())
	}
	r := httptest.NewRequest(http.MethodGet, "/api/peer/v1/audio/voices", nil)
	r.Header.Set(peerKeyHeader, peerAuth(t, pk))
	w = httptest.NewRecorder()
	handlePeerSpeechVoices(w, r)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "relay") {
		t.Errorf("voices relay → %d %s, want 503 saying it will not relay", w.Code, w.Body.String())
	}
}

func TestPeerSpeechVoices(t *testing.T) {
	peerImageDB(t)
	ttsServer(t)
	pk, _ := MintPeerKey("mac", []string{PeerCapSpeech}, 0)
	other, _ := MintPeerKey("pc", []string{PeerCapTranscribe}, 0)

	get := func(key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/peer/v1/audio/voices", nil)
		r.Header.Set(peerKeyHeader, key)
		w := httptest.NewRecorder()
		handlePeerSpeechVoices(w, r)
		return w
	}
	w := get(peerAuth(t, pk))
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"voices":["af_heart","am_adam"]}` {
		t.Errorf("voices → %d %s", w.Code, w.Body.String())
	}
	before := ttsCalls
	if w := get(peerAuth(t, other)); w.Code != http.StatusForbidden || ttsCalls != before {
		t.Errorf("a key without the grant reached the voice list: %d, %d call(s)", w.Code, ttsCalls-before)
	}
}

// The manifest must not advertise a path that answers "disabled" to everything.
func TestManifestOffersSpeechOnlyWhenConfigured(t *testing.T) {
	prev := media.GetSpeechConfig()
	t.Cleanup(func() { media.SetSpeechConfig(prev) })

	media.SetSpeechConfig(media.SpeechConfig{Enabled: false, Endpoint: "http://localhost:8880/v1"})
	if info := peerSpeechInfo("/api/peer/v1"); info != nil {
		t.Errorf("speech advertised while disabled: %+v", info)
	}
	media.SetSpeechConfig(media.SpeechConfig{Enabled: true, Provider: "service:voice", Model: "kokoro"})
	if info := peerSpeechInfo("/api/peer/v1"); info == nil || info.Model != "kokoro" || info.Path != "/api/peer/v1" {
		t.Errorf("manifest entry = %+v", info)
	}
	// Settings naming neither a connector nor a peer speak nothing.
	media.SetSpeechConfig(media.SpeechConfig{Enabled: true, Endpoint: "http://localhost:8880/v1", Model: "kokoro"})
	if info := peerSpeechInfo("/api/peer/v1"); info != nil {
		t.Errorf("an endpoint no connector reaches is advertised: %+v", info)
	}
	if !PeerCapabilityServed(PeerCapSpeech) {
		t.Error("speech is implemented and must report as served")
	}
}

// End to end: a consumer configured from a peer speaks through it and lists
// its voices, with no peer awareness in core/media.
func TestSpeakThroughAConfiguredPeer(t *testing.T) {
	peerImageDB(t)
	asked := ttsServer(t)
	far := media.GetSpeechConfig()
	base, key := peerServer(t, PeerCapSpeech)
	media.SetSpeechConfig(far)

	p, err := SaveRemotePeer(t.Context(), "gpu-box", base, key)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !p.Offers(PeerCapSpeech) || p.SpeechModel != "kokoro" {
		t.Fatalf("the peer's speech was not recorded: caps %v model %q", p.Caps, p.SpeechModel)
	}
	cfg, err := ResolveSpeechProvider(media.SpeechConfig{Voice: "am_adam"}, PeerProviderValue("gpu-box"))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !cfg.Enabled || cfg.Endpoint != p.SpeechURL() || cfg.Voice != "am_adam" || cfg.Provider != PeerProviderValue("gpu-box") {
		t.Errorf("resolved config = %+v", cfg)
	}
	if cfg.APIKey == key {
		t.Error("the config carries the raw pairing code, which authenticates nothing")
	}
	// Not installed globally: this process is also the serving instance, and
	// pointing its own config at the peer would trip the no-relay guard.
	audio, ext, err := media.SpeakWith(t.Context(), cfg, "Through the peer.")
	if err != nil || ext != ".mp3" || string(audio) != string(peerFakeMP3) {
		t.Fatalf("speaking through the peer: %v %q %q", err, ext, audio)
	}
	if (*asked)["input"] != "Through the peer." || (*asked)["voice"] != "am_adam" {
		t.Errorf("the far side's server was asked %v", *asked)
	}
	if v := media.SpeechVoices(t.Context(), cfg); len(v) != 2 || v[0].ID != "af_heart" {
		t.Errorf("the peer's voices: %v", v)
	}
	// A config read a while ago carries the credential live then. The request
	// goes out on the peer's transport, which sends the current one.
	cfg.APIKey = "stale-credential"
	if _, _, err := media.SpeakWith(t.Context(), cfg, "Still through."); err != nil {
		t.Errorf("a stale credential in the config should not matter: %v", err)
	}
}

func TestResolveSpeechProvider(t *testing.T) {
	restore := scratchPeerStore(t)
	defer restore()
	RootDB.Set(remotePeersTable, "embed-only", RemotePeer{Name: "embed-only", BaseURL: "https://e.example", Caps: []string{PeerCapEmbeddings}})
	InvalidatePeerResolution()

	if _, err := ResolveSpeechProvider(media.SpeechConfig{}, PeerProviderValue("ghost")); err == nil {
		t.Error("an unknown peer should be refused, not quietly spoken locally")
	}
	if _, err := ResolveSpeechProvider(media.SpeechConfig{}, PeerProviderValue("embed-only")); err == nil || !strings.Contains(err.Error(), "does not offer speech") {
		t.Errorf("a peer without the grant should be refused by name, got %v", err)
	}
	// This instance has no speaker of its own: choosing it is refused while
	// speech is on, and off stays off.
	local := media.SpeechConfig{Enabled: true, Endpoint: "http://localhost:8880/v1", Voice: "af_heart"}
	for _, provider := range []string{"", EmbeddingProviderLocal} {
		if _, err := ResolveSpeechProvider(local, provider); err == nil || !strings.Contains(err.Error(), "choose what speaks") {
			t.Errorf("provider %q: this instance was taken: %v", provider, err)
		}
		off := local
		off.Enabled = false
		if got, err := ResolveSpeechProvider(off, provider); err != nil || !reflect.DeepEqual(got, off) {
			t.Errorf("provider %q changed an off config: %+v %v", provider, got, err)
		}
	}
	// Blank means the picker was not drawn: a peer selection stays.
	held := media.SpeechConfig{Enabled: true, Provider: PeerProviderValue("den"), Endpoint: "https://den.example/api/peer/v1"}
	if got, err := ResolveSpeechProvider(held, ""); err != nil || !reflect.DeepEqual(got, held) {
		t.Errorf("a blank provider dropped the peer selection: %+v %v", got, err)
	}
}

// A stored speech config naming a peer follows that peer's current record on
// every read, so a rotated credential or a moved peer needs no new save; a
// local one is untouched; a peer gone or no longer offering speech keeps the
// last endpoint that worked.
func TestSpeechConfigResolvesThePeerOnEveryRead(t *testing.T) {
	restore := scratchPeerStore(t)
	defer restore()
	prev := media.GetSpeechConfig()
	defer media.SetSpeechConfig(prev)

	RootDB.Set(remotePeersTable, "den", RemotePeer{Name: "den", BaseURL: "https://den.example", Key: "first-key",
		SpeechModel: "kokoro", Caps: []string{PeerCapSpeech}})
	InvalidatePeerResolution()
	stored := media.SpeechConfig{Enabled: true, Provider: PeerProviderValue("den"), Voice: "af_heart",
		Endpoint: "https://den.example/api/peer/v1", Model: "kokoro", APIKey: "first-key"}
	media.SetSpeechConfig(stored)

	RootDB.Set(remotePeersTable, "den", RemotePeer{Name: "den", BaseURL: "https://den2.example", Key: "second-key",
		SpeechModel: "kokoro-2", Caps: []string{PeerCapSpeech}})
	InvalidatePeerResolution()
	got := media.GetSpeechConfig()
	if got.APIKey != "second-key" || got.Endpoint != "https://den2.example/api/peer/v1" || got.Model != "kokoro-2" || got.Voice != "af_heart" {
		t.Errorf("after rotation and a move: %+v", got)
	}

	RootDB.Set(remotePeersTable, "den", RemotePeer{Name: "den", BaseURL: "https://den3.example", Key: "third-key",
		Caps: []string{PeerCapEmbeddings}})
	InvalidatePeerResolution()
	if got := media.GetSpeechConfig(); !reflect.DeepEqual(got, stored) {
		t.Errorf("a peer that dropped speech repointed the config: %+v", got)
	}
	RootDB.Unset(remotePeersTable, "den")
	InvalidatePeerResolution()
	if got := media.GetSpeechConfig(); !reflect.DeepEqual(got, stored) {
		t.Errorf("a deleted peer altered the config: %+v", got)
	}

	local := media.SpeechConfig{Enabled: true, Endpoint: "http://localhost:8880/v1", APIKey: "k"}
	media.SetSpeechConfig(local)
	if got := media.GetSpeechConfig(); !reflect.DeepEqual(got, local) {
		t.Errorf("a local config was altered: %+v", got)
	}
}
