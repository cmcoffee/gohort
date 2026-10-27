package media

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A bare mp3 frame header: what a speech server sends for response_format mp3
// when it writes no ID3 tag.
var fakeMP3 = append([]byte{0xFF, 0xFB, 0x90, 0x64}, make([]byte, 64)...)

// Speak asks an OpenAI-compatible server for mp3 with the configured model,
// voice and key, and hands back the clip.
func TestSpeakSendsTheRequestAndReturnsTheClip(t *testing.T) {
	var got map[string]string
	var auth, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write(fakeMP3)
	}))
	defer srv.Close()

	cfg := SpeechConfig{Endpoint: srv.URL + "/v1/", Model: "kokoro", Voice: "af_heart", APIKey: "k1", Enabled: true}
	audio, ext, err := SpeakWith(context.Background(), cfg, "  Hello there.  ")
	if err != nil {
		t.Fatalf("speak: %v", err)
	}
	if ext != ".mp3" || len(audio) != len(fakeMP3) {
		t.Errorf("clip: ext=%q len=%d", ext, len(audio))
	}
	if path != "/v1/audio/speech" || auth != "Bearer k1" {
		t.Errorf("request: path=%q auth=%q", path, auth)
	}
	if got["input"] != "Hello there." || got["model"] != "kokoro" || got["voice"] != "af_heart" || got["response_format"] != "mp3" {
		t.Errorf("body: %v", got)
	}
}

// Blank model and voice are left out, so a server's own defaults apply rather
// than an empty name it would refuse.
func TestSpeakLeavesBlankModelAndVoiceToTheServer(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Write(fakeMP3)
	}))
	defer srv.Close()
	if _, _, err := SpeakWith(context.Background(), SpeechConfig{Endpoint: srv.URL, Enabled: true}, "hi"); err != nil {
		t.Fatalf("speak: %v", err)
	}
	if _, ok := got["model"]; ok {
		t.Errorf("model sent blank: %v", got)
	}
	if _, ok := got["voice"]; ok {
		t.Errorf("voice sent blank: %v", got)
	}
}

// Every way of getting no clip is an error that says which, never a "clip" of
// an error page.
func TestSpeakRefusesWhatIsNotAClip(t *testing.T) {
	status, body := 0, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	defer srv.Close()
	on := SpeechConfig{Endpoint: srv.URL, Enabled: true}

	cases := []struct {
		name   string
		cfg    SpeechConfig
		text   string
		status int
		body   string
		want   string
	}{
		{"disabled", SpeechConfig{Endpoint: srv.URL}, "hi", 200, "", "disabled"},
		{"no endpoint", SpeechConfig{Enabled: true}, "hi", 200, "", "not configured"},
		{"no text", on, "   ", 200, "", "no text"},
		{"server error", on, "hi", 500, `{"error":"voice not found"}`, "HTTP 500: {\"error\":\"voice not found\"}"},
		{"a 200 with no audio", on, "hi", 200, `{"detail":"model loading"}`, "without audio"},
	}
	for _, c := range cases {
		status, body = c.status, c.body
		audio, _, err := SpeakWith(context.Background(), c.cfg, c.text)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err=%v, want it to mention %q", c.name, err, c.want)
		}
		if audio != nil {
			t.Errorf("%s: returned %d bytes of audio with an error", c.name, len(audio))
		}
	}
}

func TestSpeechExt(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		ext  string
		ok   bool
	}{
		{"id3 mp3", []byte("ID3\x04\x00\x00\x00\x00\x00\x00rest"), ".mp3", true},
		{"bare mp3", fakeMP3, ".mp3", true},
		{"wav", append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), make([]byte, 32)...), ".wav", true},
		{"ogg", append([]byte("OggS\x00"), make([]byte, 32)...), ".ogg", true},
		{"json", []byte(`{"error":"x"}`), "", false},
		{"html", []byte("<html><body>502</body></html>"), "", false},
		{"empty", nil, "", false},
	}
	for _, c := range cases {
		if ext, ok := speechExt(c.in); ext != c.ext || ok != c.ok {
			t.Errorf("%s: speechExt = %q,%v want %q,%v", c.name, ext, ok, c.ext, c.ok)
		}
	}
}

// SpeakIn speaks in the voice it is given, and in the configured one when
// given none.
func TestSpeakInAVoice(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		got = append(got, body["voice"])
		w.Write(fakeMP3)
	}))
	defer srv.Close()
	prev := GetSpeechConfig()
	SetSpeechConfig(SpeechConfig{Endpoint: srv.URL, Voice: "af_heart", Enabled: true})
	t.Cleanup(func() { SetSpeechConfig(prev) })
	SpeakIn(context.Background(), "hi", "bm_george")
	SpeakIn(context.Background(), "hi", "  ")
	if len(got) != 2 || got[0] != "bm_george" || got[1] != "af_heart" {
		t.Errorf("voices asked for: %q", got)
	}
	if GetSpeechConfig().Voice != "af_heart" {
		t.Error("speaking in another voice must not change the configured one")
	}
}

// The voice list reads a bare name or an object naming it, and is empty for a
// server without one.
func TestSpeechVoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/voices" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"voices":["af_heart",{"id":"bm_george"},{"name":"am_adam"},""]}`))
	}))
	defer srv.Close()
	if got := SpeechVoices(context.Background(), SpeechConfig{Endpoint: srv.URL + "/v1"}); strings.Join(got, ",") != "af_heart,bm_george,am_adam" {
		t.Errorf("voices: %q", got)
	}
	if got := SpeechVoices(context.Background(), SpeechConfig{Endpoint: srv.URL + "/none"}); len(got) != 0 {
		t.Errorf("a server without a list: %q", got)
	}
}
