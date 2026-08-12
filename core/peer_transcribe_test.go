package core

// Transcription over the peer link. The wire shape is the OpenAI
// /audio/transcriptions multipart one on purpose: core/media's Transcribe
// already POSTs exactly that, so a consuming instance points its ordinary
// TranscribeConfig at <peer>/api/peer/v1 and every caller keeps working with no
// idea the whisper model is on another machine.

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sttServer stands up a fake OpenAI-compatible STT endpoint and points this
// instance at it, returning the endpoint's recorded state.
func sttServer(t *testing.T, reply string) (gotFile string, gotBytes *int) {
	t.Helper()
	prev := GetTranscribeConfig()
	t.Cleanup(func() { SetTranscribeConfig(prev) })
	n := 0
	gotBytes = &n
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(8 << 20)
		if f, h, err := r.FormFile("file"); err == nil {
			defer f.Close()
			var buf bytes.Buffer
			buf.ReadFrom(f)
			n = buf.Len()
			gotFile = h.Filename
		}
		w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	SetTranscribeConfig(TranscribeConfig{Enabled: true, Endpoint: srv.URL, Model: "whisper-1"})
	return gotFile, gotBytes
}

func audioRequest(t *testing.T, key, filename, format string, audio []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(audio)
	if format != "" {
		mw.WriteField("response_format", format)
	}
	mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/peer/v1/audio/transcriptions", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set(peerKeyHeader, key)
	return r
}

func TestPeerTranscribeReturnsTheText(t *testing.T) {
	peerImageDB(t)
	_, gotBytes := sttServer(t, "the quick brown fox")
	pk, _ := MintPeerKey("mac", []string{PeerCapTranscribe}, 0)

	w := httptest.NewRecorder()
	HandlePeerTranscribe(w, audioRequest(t, pk.Key, "note.m4a", "text", []byte("fake-audio-bytes")))

	if w.Code != http.StatusOK {
		t.Fatalf("transcribe → %d, want 200: %s", w.Code, w.Body.String())
	}
	if got := strings.TrimSpace(w.Body.String()); got != "the quick brown fox" {
		t.Errorf("body = %q, want the transcript", got)
	}
	if *gotBytes != len("fake-audio-bytes") {
		t.Errorf("the STT endpoint received %d bytes, want the %d that were uploaded", *gotBytes, len("fake-audio-bytes"))
	}
}

// response_format is what every gohort client asks for, but a non-gohort client
// pointed here must still get the OpenAI JSON envelope it expects.
func TestPeerTranscribeAnswersJSONWhenTextWasNotAsked(t *testing.T) {
	peerImageDB(t)
	sttServer(t, "hello there")
	pk, _ := MintPeerKey("mac", []string{PeerCapTranscribe}, 0)

	w := httptest.NewRecorder()
	HandlePeerTranscribe(w, audioRequest(t, pk.Key, "note.mp3", "", []byte("x")))

	if w.Code != http.StatusOK {
		t.Fatalf("→ %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("not JSON: %s", w.Body.String())
	}
	if out.Text != "hello there" {
		t.Errorf("text = %q", out.Text)
	}
}

// A key granted embeddings must not be able to spend the STT model — the
// capability allowlist is the whole point of granting one thing not everything.
func TestPeerTranscribeRefusesAKeyWithoutTheGrant(t *testing.T) {
	peerImageDB(t)
	sttServer(t, "should never run")
	pk, _ := MintPeerKey("mac", []string{PeerCapEmbeddings}, 0)

	w := httptest.NewRecorder()
	HandlePeerTranscribe(w, audioRequest(t, pk.Key, "a.mp3", "text", []byte("x")))
	if w.Code != http.StatusForbidden {
		t.Fatalf("ungranted key → %d, want 403: %s", w.Code, w.Body.String())
	}
}

// Relaying makes A→B→A a loop neither side can see, and the failure is a hang
// rather than an error. Same refusal the embeddings endpoint makes.
func TestPeerTranscribeWillNotRelayAPeersOwnBorrowedSTT(t *testing.T) {
	peerImageDB(t)
	prev := GetTranscribeConfig()
	t.Cleanup(func() { SetTranscribeConfig(prev) })
	SetTranscribeConfig(TranscribeConfig{
		Enabled: true, Endpoint: "https://other.example/api/peer/v1", Model: "whisper-1",
	})
	pk, _ := MintPeerKey("mac", []string{PeerCapTranscribe}, 0)

	w := httptest.NewRecorder()
	HandlePeerTranscribe(w, audioRequest(t, pk.Key, "a.mp3", "text", []byte("x")))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("relay → %d, want 503: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "relay") {
		t.Errorf("the refusal should say it will not relay: %s", w.Body.String())
	}
}

// The manifest must not advertise a path that answers "disabled" to everything.
func TestManifestOffersTranscriptionOnlyWhenConfigured(t *testing.T) {
	peerImageDB(t)
	prev := GetTranscribeConfig()
	t.Cleanup(func() { SetTranscribeConfig(prev) })

	SetTranscribeConfig(TranscribeConfig{Enabled: false})
	if info := peerTranscribeInfo("/api/peer/v1"); info != nil {
		t.Errorf("transcription advertised while disabled: %+v", info)
	}
	SetTranscribeConfig(TranscribeConfig{Enabled: true, Endpoint: "http://localhost:8089/v1", Model: "whisper-1"})
	info := peerTranscribeInfo("/api/peer/v1")
	if info == nil {
		t.Fatal("a configured STT is not advertised")
	}
	if info.Model != "whisper-1" || info.Path != "/api/peer/v1" {
		t.Errorf("manifest entry = %+v", info)
	}
}

// transcribe is speech-to-text; transcode is a container change. They read
// alike and share four letters, and wiring one to the other's grant would give
// a peer a capability nobody meant to hand over.
func TestTranscribeAndTranscodeAreDistinctCapabilities(t *testing.T) {
	if PeerCapTranscribe == PeerCapTranscode {
		t.Fatal("transcribe and transcode collapsed into one grant")
	}
	if !PeerCapabilityServed(PeerCapTranscribe) {
		t.Error("transcribe is implemented and must report as served")
	}
	if PeerCapabilityServed(PeerCapTranscode) {
		t.Error("transcode is not implemented and must not report as served")
	}
	var found bool
	for _, c := range PeerCapabilities() {
		if c == PeerCapTranscribe {
			found = true
		}
	}
	if !found {
		t.Error("transcribe is missing from the capability vocabulary, so no key can grant it")
	}
}
