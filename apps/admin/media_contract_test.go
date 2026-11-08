package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/snugforge/kvlite"
)

// What the Capabilities panels for embeddings, transcription, speech and image
// generation get back, through the real routes and the gate in front of them:
// each config read and saved, its key kept on a save that sends the
// placeholder and never sent back, its model list, and its connectivity test
// against servers standing in for a provider. Each as an administrator, as a
// user who is not one, by the wrong method and with a broken or missing body,
// and then what the activity log says happened. No test here reaches past this
// machine. Compared with testdata/media_contract.golden.
//
// Run with UPDATE_GOLDEN=1 to rewrite the golden after a change that is meant.
func TestMediaRoutesContract(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prevAuth, prevRoot, prevEmbed, prevSTT, prevTTS := AuthDB, RootDB, GetEmbeddingConfig(), GetTranscribeConfig(), media.GetSpeechConfig()
	AuthDB = func() Database { return adb }
	RootDB = adb
	t.Cleanup(func() {
		AuthDB, RootDB = prevAuth, prevRoot
		SetEmbeddingConfig(prevEmbed)
		SetTranscribeConfig(prevSTT)
		media.SetSpeechConfig(prevTTS)
	})

	// One server stands in for every provider, by path: an embedder and a
	// transcriber (each reached through a connector, as every one is), a
	// transcriber that refuses, and a speech server with a voice list.
	stub := http.NewServeMux()
	stub.HandleFunc("/embed/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3]}]}`))
	})
	stub.HandleFunc("/stt-ok/v1/audio/transcriptions", func(w http.ResponseWriter, r *http.Request) {
		if f, h, err := r.FormFile("file"); err != nil || !strings.HasSuffix(h.Filename, ".wav") || r.FormValue("model") != "whisper-1" {
			http.Error(w, "no audio", http.StatusBadRequest)
			return
		} else {
			f.Close()
		}
		w.Write([]byte(`{"text":""}`)) // a tone has no words
	})
	stub.HandleFunc("/stt-deny/v1/audio/transcriptions", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusForbidden)
	})
	// A Kokoro-FastAPI: speaks with a voice it has, lists its voices, and
	// refuses a voice it does not have.
	stub.HandleFunc("/tts-ok/v1/audio/speech", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["voice"] != "af_heart" {
			http.Error(w, `{"detail":"voice not found"}`, http.StatusBadRequest)
			return
		}
		w.Write([]byte("ID3\x04\x00\x00\x00\x00\x00\x00clip"))
	})
	stub.HandleFunc("/tts-ok/v1/audio/voices", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"voices":["af_heart","bm_george"]}`))
	})
	srv := httptest.NewServer(stub)
	defer srv.Close()

	ct := &contract{
		t:       t,
		secrets: []string{"s3cr3t-embed", "s3cr3t-stt", "s3cr3t-tts"},
		rewrite: strings.NewReplacer(srv.URL, "<stub>"),
	}
	app := &AdminApp{}
	mux := http.NewServeMux()
	app.RegisterRoutes(mux, "")
	do := func(user, method, path, body string) { ct.run(mux, adb, user, method, path, body) }

	for _, p := range []string{"/api/embeddings", "/api/transcribe", "/api/speech", "/api/media-backends", "/api/speech/voices"} {
		do("", "GET", p, "")
		do("alice", "GET", p, "")
		do("root", "GET", p, "")
		do("root", "PUT", p, "{}")
	}
	for _, p := range []string{"/api/embeddings/test", "/api/transcribe/test", "/api/speech/test", "/api/media-backends/role", "/api/media-backends/uses"} {
		do("alice", "POST", p, `{"enabled":true}`)
		do("root", "GET", p, "")
		do("root", "POST", p, `{"enabled":`)
		do("root", "POST", p, ``)
	}

	// Embeddings are made by a connector or a peer: one that embeds at the
	// stub, one whose server is not there, and none on this instance itself.
	embedConnector(t, "vectors", srv.URL+"/embed/v1", "nomic-embed-text")
	embedConnector(t, "gone", "http://127.0.0.1:1/v1", "m")
	SetEmbeddingConfig(EmbeddingConfig{})
	do("alice", "POST", "/api/embeddings", `{"enabled":true,"provider":"vectors"}`)
	do("root", "POST", "/api/embeddings", `{"enabled":true,"provider":"local","endpoint":"`+srv.URL+`/embed/v1","api_key":"s3cr3t-embed-1"}`)
	do("root", "POST", "/api/embeddings", `{"enabled":true,"provider":"vectors","model":"typed-in"}`)
	do("root", "GET", "/api/embeddings", "")
	do("root", "POST", "/api/embeddings", `{"enabled":true,"provider":"nope"}`)
	do("root", "POST", "/api/embeddings", `{"enabled":true,"provider":"peer:nope"}`)
	do("root", "POST", "/api/embeddings", `{"provider":"none"}`)
	do("root", "POST", "/api/embeddings", `{"enabled":`)
	do("root", "POST", "/api/embeddings", ``)
	do("root", "POST", "/api/embeddings/test", `{"provider":"none"}`)
	do("root", "POST", "/api/embeddings/test", `{"enabled":true,"model":"m"}`)
	do("root", "POST", "/api/embeddings/test", `{"enabled":true,"provider":"peer:nope"}`)
	do("root", "POST", "/api/embeddings/test", `{"enabled":true,"provider":"vectors"}`)
	do("root", "POST", "/api/embeddings/test", `{"enabled":true,"provider":"gone"}`)

	// Transcription is done by a connector or a peer too: one that hears at
	// the stub, one the stub refuses, and none on this instance itself.
	transcribeConnector(t, "ears", srv.URL+"/stt-ok/v1", "whisper-1")
	transcribeConnector(t, "deaf", srv.URL+"/stt-deny/v1", "")
	SetTranscribeConfig(TranscribeConfig{})
	do("alice", "POST", "/api/transcribe", `{"enabled":true,"provider":"ears"}`)
	do("root", "POST", "/api/transcribe", `{"enabled":true,"provider":"local","endpoint":"`+srv.URL+`/stt-ok/v1","api_key":"s3cr3t-stt"}`)
	do("root", "POST", "/api/transcribe", `{"enabled":true,"provider":"ears","endpoint":"http://127.0.0.1:1/v1","model":"typed-in"}`)
	if got := GetTranscribeConfig(); got.Provider != "ears" || got.Model != "whisper-1" || got.Endpoint != "" || got.APIKey != "" {
		t.Errorf("a save naming a connector stores it and the model its spec declares: %+v", got)
	}
	do("root", "GET", "/api/transcribe", "")
	do("root", "POST", "/api/transcribe", `{"enabled":true,"provider":"nope"}`)
	do("root", "POST", "/api/transcribe", `{"enabled":true,"provider":"peer:nope"}`)
	do("root", "POST", "/api/transcribe", `{"provider":"none"}`)
	do("root", "POST", "/api/transcribe", `{"enabled":`)
	do("root", "POST", "/api/transcribe/test", `{"provider":"none"}`)
	do("root", "POST", "/api/transcribe/test", `{"enabled":true}`)
	do("root", "POST", "/api/transcribe/test", `{"enabled":true,"provider":"peer:nope"}`)
	do("root", "POST", "/api/transcribe/test", `{"enabled":true,"provider":"ears"}`)
	do("root", "POST", "/api/transcribe/test", `{"enabled":true,"provider":"deaf"}`)

	// Speech too: one that speaks at the stub and lists its voices, one
	// whose server is not there, and none on this instance itself.
	speakConnector(t, "voice", srv.URL+"/tts-ok/v1", "kokoro")
	speakConnector(t, "mute", "http://127.0.0.1:1/v1", "")
	media.SetSpeechConfig(media.SpeechConfig{})
	do("alice", "POST", "/api/speech", `{"enabled":true,"provider":"voice","voice":"af_heart"}`)
	do("root", "POST", "/api/speech", `{"enabled":true,"provider":"local","endpoint":"`+srv.URL+`/tts-ok/v1","api_key":"s3cr3t-tts"}`)
	do("root", "POST", "/api/speech", `{"enabled":true,"provider":"voice","endpoint":"http://127.0.0.1:1/v1","model":"typed-in","voice":"af_heart"}`)
	if got := media.GetSpeechConfig(); !got.Enabled || got.Provider != "voice" || got.Model != "kokoro" || got.Voice != "af_heart" || got.Endpoint != "" || got.APIKey != "" {
		t.Errorf("a save naming a connector stores it, the model its spec declares and the voice: %+v", got)
	}
	do("root", "GET", "/api/speech", "")
	do("root", "GET", "/api/speech/voices", "")
	do("root", "POST", "/api/speech", `{"enabled":`)
	do("root", "POST", "/api/speech/test", `{"provider":"none"}`)
	do("root", "POST", "/api/speech/test", `{"enabled":true,"voice":"af_heart"}`)
	do("root", "POST", "/api/speech/test", `{"enabled":true,"provider":"voice","voice":"nobody"}`)
	do("root", "POST", "/api/speech/test", `{"enabled":true,"provider":"mute"}`)
	do("root", "POST", "/api/speech/test", `{"enabled":true,"provider":"peer:nope"}`)
	do("root", "POST", "/api/speech", `{"enabled":true,"provider":"peer:nope"}`)
	do("root", "POST", "/api/speech", `{"enabled":true,"provider":"nope"}`)

	do("alice", "POST", "/api/media-backends/role?name=openai&role=default", "")
	do("root", "GET", "/api/media-backends", "")
	do("root", "POST", "/api/media-backends/role?name=openai&role=default", "")
	do("root", "POST", "/api/media-backends/role?name=openai&role=sometimes", "")
	do("root", "POST", "/api/media-backends/uses", `{"name":"openai","uses":"edit"}`)
	// Refused, it stores nothing.
	if adb.Get(ImageTable, "provider", new(string)) || adb.Get(ImageTable, "uses", new(map[string]string)) {
		t.Error("a refused image role or use stored something")
	}

	ct.activity(mux, adb, "root")
	ct.check(t, "testdata/media_contract.golden")
}
