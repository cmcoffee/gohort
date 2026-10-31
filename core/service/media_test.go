package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/media"
)

// mp3ish is enough of an MP3 for the audio check: an ID3 tag, then bytes.
var mp3ish = append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), make([]byte, 64)...)

// Transcription set to a service goes through it: the OpenAI template's own
// spec, its audio as a multipart file, an optional field left out rather than
// sent empty, and the speaker's names as the prompt when there are some.
func TestTranscriptionGoesThroughAService(t *testing.T) {
	world(t)
	var fields []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			w.Write([]byte(`{"data":[]}`))
			return
		}
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		got := map[string]string{}
		for k, v := range r.MultipartForm.Value {
			got[k] = v[0]
		}
		if f, h, err := r.FormFile("file"); err == nil {
			b, _ := io.ReadAll(f)
			got["file"] = h.Filename + " " + string(b[:4])
		}
		fields = append(fields, got)
		w.Write([]byte(`{"text":"hello there"}`))
	}))
	defer srv.Close()
	install(t, "openai-transcription", templateSpec(t, "openai-transcription", srv.URL))
	cfg := media.TranscribeConfig{Enabled: true, Provider: media.ServiceProviderPrefix + "openai-transcription"}
	text, err := media.TranscribeWith(context.Background(), cfg, []byte("RIFF....WAVEfake"), "note.wav")
	if err != nil || text != "hello there" {
		t.Fatalf("transcribe: %q %v", text, err)
	}
	ctx := media.WithTranscribePrompt(context.Background(), "Gohort, Scribe")
	media.TranscribeWith(ctx, cfg, []byte("RIFF....WAVEfake"), "note.wav")
	if len(fields) != 2 {
		t.Fatalf("calls: %v", fields)
	}
	if f := fields[0]; f["model"] != "whisper-1" || !strings.HasSuffix(strings.Fields(f["file"])[0], ".wav") || f["prompt"] != "" || f["language"] != "" {
		t.Errorf("first request: %v", f)
	}
	if _, sent := fields[0]["prompt"]; sent {
		t.Error("an empty prompt was sent as a field")
	}
	if fields[1]["prompt"] != "Gohort, Scribe" {
		t.Errorf("the speaker's names: %v", fields[1])
	}
	if reps, err := Test(context.Background(), "openai-transcription"); err != nil || !reps[0].OK {
		t.Errorf("contract test with a tone: %+v %v", reps, err)
	}
}

// Speech set to a service goes through it: the ElevenLabs template's own
// spec, its voice in the path (the settings' voice over the template's),
// audio as the reply body, priced per character when the spec says so.
func TestSpeechGoesThroughAService(t *testing.T) {
	world(t)
	var paths, bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"voices":[]}`))
			return
		}
		b, _ := io.ReadAll(r.Body)
		paths, bodies = append(paths, r.URL.Path+"?"+r.URL.RawQuery), append(bodies, string(b))
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write(mp3ish)
	}))
	defer srv.Close()
	spec := templateSpec(t, "elevenlabs-speech", srv.URL)
	binding(spec)["cost"] = map[string]any{"per_unit": 0.001, "unit": "character"}
	install(t, "elevenlabs-speech", spec)
	cfg := media.SpeechConfig{Enabled: true, Provider: media.ServiceProviderPrefix + "elevenlabs-speech"}
	data, ext, err := media.SpeakWith(context.Background(), cfg, "Hello there.")
	if err != nil || ext != ".mp3" || string(data[:3]) != "ID3" {
		t.Fatalf("speak: %q %v", ext, err)
	}
	cfg.Voice = "myvoice"
	media.SpeakWith(context.Background(), cfg, "Again.")
	if len(paths) != 2 || paths[0] != "/v1/text-to-speech/21m00Tcm4TlvDq8ikWAM?output_format=mp3_44100_128" || !strings.HasPrefix(paths[1], "/v1/text-to-speech/myvoice?") {
		t.Errorf("paths: %v", paths)
	}
	var body map[string]string
	json.Unmarshal([]byte(bodies[0]), &body)
	if body["text"] != "Hello there." || body["model_id"] != "eleven_multilingual_v2" {
		t.Errorf("body: %s", bodies[0])
	}
	if spend := RecordOf("elevenlabs-speech").Capabilities["speech.synthesize"].Spend; spend < 0.017 || spend > 0.019 {
		t.Errorf("priced per character: 18 characters at $0.001 came to %v", spend)
	}
	if reps, err := Test(context.Background(), "elevenlabs-speech"); err != nil || !reps[0].OK {
		t.Errorf("contract test: %+v %v", reps, err)
	}
}

// A music service is a tool an agent can be given: the song is saved in its
// workspace under its title and attached to the reply.
func TestAMusicServiceIsATool(t *testing.T) {
	world(t)
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write(mp3ish)
	}))
	defer srv.Close()
	install(t, "tunes", map[string]any{
		"provider": map[string]any{"transport": "http", "base_url": srv.URL, "auth": map[string]any{"none": true},
			"operations": map[string]any{"song": map[string]any{"method": "POST", "path": "/songs",
				"body": map[string]any{"prompt": "{prompt}", "lyrics": "{lyrics}", "title": "{title}"}, "read": "file"}}},
		"provides": []any{map[string]any{"capability": "music.generate", "steps": []any{map[string]any{"call": "song"}},
			"output": map[string]any{"audio": map[string]any{"from": "body"}}}},
	})
	var tool ChatTool
	for _, ct := range RegisteredChatTools() {
		if ct.Name() == "generate_music_tunes" {
			tool = ct
		}
	}
	if tool == nil {
		t.Fatal("the music service is not a tool")
	}
	ws := t.TempDir()
	sess := &ToolSession{WorkspaceDir: ws}
	out, err := tool.(SessionChatTool).RunWithSession(map[string]any{"prompt": "a calm song", "title": "Money in the Cloud", "lyrics": "la la"}, sess)
	if err != nil || !strings.Contains(out, "money-in-the-cloud.mp3") {
		t.Fatalf("run: %q %v", out, err)
	}
	if b, err := os.ReadFile(ws + "/money-in-the-cloud.mp3"); err != nil || string(b[:3]) != "ID3" {
		t.Errorf("the song in the workspace: %v", err)
	}
	if len(sess.Videos) != 1 || !strings.Contains(strings.Join(sess.StagedFiles(), ","), "money-in-the-cloud.mp3") {
		t.Error("the song is not attached and staged")
	}
	if !strings.Contains(got, `"lyrics":"la la"`) {
		t.Errorf("request: %s", got)
	}
}

// Embeddings set to a service go through it: the OpenAI template's own spec,
// the texts sent as one list with the settings' model, one vector read from
// each element of the reply. A reply with no vector is refused.
func TestEmbeddingsGoThroughAService(t *testing.T) {
	world(t)
	var bodies []map[string]any
	short := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			w.Write([]byte(`{"data":[]}`))
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		inputs, _ := body["input"].([]any)
		var data []map[string]any
		for i := range inputs {
			data = append(data, map[string]any{"index": i, "embedding": []float64{float64(i) + 0.5, 1, 2}})
		}
		if short {
			data = data[:len(data)-1]
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()
	install(t, "openai-embeddings", templateSpec(t, "openai-embeddings", srv.URL))
	cfg := EmbeddingConfig{Enabled: true, Provider: media.ServiceProviderPrefix + "openai-embeddings", Model: "text-embedding-3-small", QueryPrefix: "query: "}
	vec, err := EmbedWith(context.Background(), cfg, "where is the cat")
	if err != nil || len(vec) != 3 || vec[0] != 0.5 {
		t.Fatalf("embed: %v %v", vec, err)
	}
	if b := bodies[0]; b["model"] != "text-embedding-3-small" || len(b["input"].([]any)) != 1 || b["input"].([]any)[0] != "query: where is the cat" {
		t.Errorf("request: %v", b)
	}
	res, err := Call(context.Background(), nil, "openai-embeddings", "embed.text", map[string]any{"texts": []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if vecs, err := Vectors(res); err != nil || len(vecs) != 2 || vecs[1][0] != 1.5 {
		t.Errorf("two texts: %v %v", vecs, err)
	}
	if reps, err := Test(context.Background(), "openai-embeddings"); err != nil || !reps[0].OK {
		t.Errorf("contract test: %+v %v", reps, err)
	}
	short = true
	if _, err := EmbedWith(context.Background(), cfg, "again"); err == nil || !strings.Contains(err.Error(), "no vectors came back") {
		t.Errorf("a reply short a vector: %v", err)
	}
}

// each is for a json output, with a path; vectors of two lengths fail the
// check, since they come from two spaces.
func TestEachReadsAListAndVectorsAgree(t *testing.T) {
	spec := templateSpec(t, "openai-embeddings", "http://127.0.0.1:1")
	out := binding(spec)["output"].(map[string]any)["vectors"].(map[string]any)
	out["each"] = "data[="
	if _, err := specFrom(t, spec); err == nil || !strings.Contains(err.Error(), "each") {
		t.Errorf("a bad each path: %v", err)
	}
	check := checkVectors("vectors")
	if err := check(Result{"vectors": {Text: `[[1,2,3],[1,2]]`}}); err == nil {
		t.Error("vectors of two lengths passed")
	}
	if err := check(Result{"vectors": {Text: `[]`}}); err == nil {
		t.Error("no vectors passed")
	}
	if err := check(Result{"vectors": {Text: `[[1,2],[3,4]]`}}); err != nil {
		t.Errorf("good vectors: %v", err)
	}
	piece, err := jsonOutput(map[string]any{"data": []any{map[string]any{"v": "abc"}, map[string]any{"v": []any{1.0}}}}, Output{Path: "data", Each: "v"})
	if err != nil || piece.Text != `["abc",[1]]` {
		t.Errorf("string and list pieces: %q %v", piece.Text, err)
	}
}

// A model connector is an LLM tier: its dialect's client, built from the
// connector's address and its credential's key, talks to the model as a
// tier's own would; each call is counted and priced on the connector's row;
// its test is a short real chat. The key is given only for an address the
// credential allows, and a spec naming a host its dialect is not spoken at
// is refused.
func TestAModelConnectorIsAnLLMTier(t *testing.T) {
	world(t)
	var auths, paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths, paths = append(auths, r.Header.Get("Authorization")), append(paths, r.URL.Path)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"c1","object":"chat.completion","model":"` + fmt.Sprint(body["model"]) + `","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000,"completion_tokens":200}}`))
	}))
	defer srv.Close()
	if err := Secure().Save(SecureCredential{Name: "hosted_llm", Type: SecureCredBearer, BaseURL: srv.URL + "/v1"}, "sk-test"); err != nil {
		t.Fatal(err)
	}
	install(t, "hosted", map[string]any{
		"provider": map[string]any{"transport": "http", "base_url": srv.URL + "/v1", "auth": map[string]any{"credential": "hosted_llm"},
			"operations": map[string]any{"models": map[string]any{"path": "/models"}}},
		"provides": []any{map[string]any{"capability": "llm.chat", "dialect": "openai", "defaults": map[string]any{"model": "fast-model"},
			"cost": map[string]any{"per_million_input": 2.0, "per_million_output": 10.0}}},
	})
	llm, err := NewLLMFromConfig(LLMProviderConfig{Provider: "service:hosted"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := llm.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err != nil || resp.Content != "OK" || len(paths) != 1 || paths[0] != "/v1/chat/completions" || auths[0] != "Bearer sk-test" {
		t.Fatalf("chat: %+v %v %v %v", resp, err, paths, auths)
	}
	rec := RecordOf("hosted").Capabilities["llm.chat"]
	if rec == nil || rec.Hours[0].Calls+rec.Hours[1].Calls != 1 || rec.Spend < 0.0039 || rec.Spend > 0.0041 {
		t.Errorf("the call on the connector's row (1000 in at $2/M and 200 out at $10/M is $0.004): %+v", rec)
	}
	if reps, err := Test(context.Background(), "hosted"); err != nil || !reps[0].OK || !strings.Contains(reps[0].Detail, `"OK"`) {
		t.Errorf("the model's test: %+v %v", reps, err)
	}
	if !ProviderHasNativeTools("service:hosted") {
		t.Error("a model connector's tier would parse tool calls out of text")
	}
	if private, why := ProviderLooksPrivate("service:hosted", ""); !private || !strings.Contains(why, "loopback") {
		t.Errorf("judged by the connector's own address (a loopback test server): %v %q", private, why)
	}
	if private, _ := ProviderLooksPrivate("service:missing", ""); private {
		t.Error("a model connector that cannot be read was judged private")
	}

	// The key, only for an address the credential allows.
	if _, err := Secure().ModelKey("hosted_llm", "https://elsewhere.example/v1/"); err == nil {
		t.Error("the key was given for an address the credential does not allow")
	}
	for want, b := range map[string]map[string]any{
		"spoken only at api.anthropic.com": {"capability": "llm.chat", "dialect": "anthropic"},
		"gohort speaks openai":             {"capability": "llm.chat", "dialect": "cobol"},
		"no steps or outputs":              {"capability": "llm.chat", "dialect": "openai", "steps": []any{map[string]any{"call": "models"}}},
		"priced by its tokens":             {"capability": "llm.chat", "dialect": "openai", "cost": map[string]any{"per_call": 0.01}},
		"llm.chat only":                    {"capability": "speech.synthesize", "steps": []any{map[string]any{"call": "models"}}, "output": map[string]any{"audio": map[string]any{"from": "body"}}, "cost": map[string]any{"per_million_input": 1.0}},
	} {
		_, err := specFrom(t, map[string]any{
			"provider": map[string]any{"transport": "http", "base_url": srv.URL + "/v1", "auth": map[string]any{"none": true},
				"operations": map[string]any{"models": map[string]any{"path": "/models", "read": "file"}}},
			"provides": []any{b}})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v", want, err)
		}
	}
}
