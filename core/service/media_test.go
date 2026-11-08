package service

import (
	"context"
	"encoding/json"
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
	cfg := media.TranscribeConfig{Enabled: true, Provider: "openai-transcription"}
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
	cfg := media.SpeechConfig{Enabled: true, Provider: "elevenlabs-speech"}
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
	// Offered only while the Music page offers it.
	if _, params := tool.(DynamicChatTool).SchemaWithSession(nil); params != nil {
		t.Error("a music connector the Music page does not offer is offered")
	}
	RootDB.Set(MusicTable, "agents", []string{"tunes"})
	if _, params := tool.(DynamicChatTool).SchemaWithSession(nil); params == nil {
		t.Error("a music connector the Music page offers is not")
	}
	// Not to an agent switched off it, which cannot call it either.
	off := &ToolSession{Username: "u", WorkspaceDir: t.TempDir(), DeniedCredentials: map[string]bool{"connection:tunes": true}}
	if _, params := tool.(DynamicChatTool).SchemaWithSession(off); params != nil {
		t.Error("an agent with the music connection switched off is offered it")
	}
	if _, err := tool.(SessionChatTool).RunWithSession(map[string]any{"prompt": "a calm song", "title": "x", "lyrics": "la"}, off); err == nil || got != "" {
		t.Errorf("a barred agent's song: %v, sent %q", err, got)
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
	cfg := EmbeddingConfig{Enabled: true, Provider: "openai-embeddings", Model: "text-embedding-3-small", QueryPrefix: "query: "}
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

// A speech server that answers in ogg keeps its extension, so the clip is
// sent and played as what it is.
func TestAnOggClipKeepsItsExtension(t *testing.T) {
	world(t)
	ogg := append([]byte("OggS\x00\x02"), make([]byte, 64)...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(ogg) }))
	defer srv.Close()
	install(t, "oggy", speakSpec(srv.URL))
	if _, ext, err := speakVia(context.Background(), "oggy", "Hi.", ""); err != nil || ext != ".ogg" {
		t.Errorf("an ogg clip: %q %v", ext, err)
	}
}

// speakSpec is a keyless speech connector at base, OpenAI's /audio/speech.
func speakSpec(base string) map[string]any {
	return map[string]any{
		"provider": map[string]any{"transport": "http", "base_url": base, "auth": map[string]any{"none": true},
			"operations": map[string]any{"speak": map[string]any{"method": "POST", "path": "/audio/speech",
				"body": map[string]any{"input": "{text}", "voice": "{voice}"}, "read": "file"}}},
		"provides": []any{map[string]any{"capability": "speech.synthesize", "steps": []any{map[string]any{"call": "speak"}},
			"output": map[string]any{"audio": map[string]any{"from": "body"}}}},
	}
}

// A media.generate connector is a tool named for the kind it declares,
// offered only while Media Generation offers it and only to an agent that may
// reach its connection. What it makes is saved in the workspace under its
// kind and attached; a file passed from the workspace goes along to work from.
func TestAMediaServiceIsAToolNamedForItsKind(t *testing.T) {
	world(t)
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		w.Header().Set("Content-Type", "video/mp4")
		w.Write([]byte("\x00\x00\x00\x18ftypmp42 not really a video"))
	}))
	defer srv.Close()
	install(t, "clips", map[string]any{
		"provider": map[string]any{"transport": "http", "base_url": srv.URL, "auth": map[string]any{"none": true},
			"operations": map[string]any{"make": map[string]any{"method": "POST", "path": "/videos",
				"body": map[string]any{"prompt": "{prompt}", "start": "{file.base64}"}, "read": "file"}}},
		"provides": []any{map[string]any{"capability": "media.generate", "kind": "video", "about": "Makes a short video clip from a prompt.",
			"steps": []any{map[string]any{"call": "make"}}, "output": map[string]any{"file": map[string]any{"from": "body"}}}},
	})
	var tool ChatTool
	for _, ct := range RegisteredChatTools() {
		if ct.Name() == "generate_video_clips" {
			tool = ct
		}
	}
	if tool == nil {
		t.Fatal("the media service is not a tool named for its kind")
	}
	if _, params := tool.(DynamicChatTool).SchemaWithSession(nil); params != nil {
		t.Error("a media connector Media Generation does not offer is offered")
	}
	RootDB.Set(MediaTable, "agents", []string{"clips"})
	desc, params := tool.(DynamicChatTool).SchemaWithSession(nil)
	if params == nil || !strings.HasPrefix(desc, "Makes a short video clip from a prompt.") {
		t.Errorf("offered: %q %v", desc, params)
	}
	off := &ToolSession{Username: "u", WorkspaceDir: t.TempDir(), DeniedCredentials: map[string]bool{"connection:clips": true}}
	if _, params := tool.(DynamicChatTool).SchemaWithSession(off); params != nil {
		t.Error("an agent with the connection switched off is offered it")
	}
	ws := t.TempDir()
	os.WriteFile(ws+"/start.png", []byte("\x89PNG\r\n\x1a\nstart frame"), 0o600)
	sess := &ToolSession{WorkspaceDir: ws}
	out, err := tool.(SessionChatTool).RunWithSession(map[string]any{"prompt": "a cat on a skateboard", "file": "start.png"}, sess)
	if err != nil || !strings.Contains(out, "video") {
		t.Fatalf("run: %q %v", out, err)
	}
	if len(sess.Videos) != 1 || len(sess.StagedFiles()) != 1 {
		t.Errorf("the clip is not attached and staged: %v", sess.StagedFiles())
	}
	if !strings.Contains(got, `"prompt":"a cat on a skateboard"`) || !strings.Contains(got, `"start":"iVBORw0K`) {
		t.Errorf("the request did not carry the prompt and the start frame: %s", got)
	}
}

// kind and about belong to media.generate, and a kind is a short name in
// words, since it names the agent's tool.
func TestAMediaKindIsAShortNameOnMediaGenerateOnly(t *testing.T) {
	op := map[string]any{"make": map[string]any{"method": "POST", "path": "/x", "body": map[string]any{"prompt": "{prompt}"}, "read": "file"}}
	spec := func(capability, kind string) []byte {
		b, _ := json.Marshal(map[string]any{
			"provider": map[string]any{"transport": "http", "base_url": "https://media.example.test", "auth": map[string]any{"none": true}, "operations": op},
			"provides": []any{map[string]any{"capability": capability, "kind": kind, "steps": []any{map[string]any{"call": "make"}},
				"output": map[string]any{map[bool]string{true: "file", false: "audio"}[capability == "media.generate"]: map[string]any{"from": "body"}}}},
		})
		return b
	}
	if s, err := Parse(spec("media.generate", "3D model")); err != nil || s.Validate() != nil {
		t.Errorf("a 3D model kind was refused: %v %v", err, s.Validate())
	}
	for _, c := range []struct{ capability, kind string }{{"music.generate", "song"}, {"media.generate", "video; rm -rf"}} {
		s, err := Parse(spec(c.capability, c.kind))
		if err == nil {
			err = s.Validate()
		}
		if err == nil {
			t.Errorf("%s with kind %q was accepted", c.capability, c.kind)
		}
	}
}

// Each music and media connector's tool claims AI Media Generation, a
// category of its own with a description for the model, since its name is
// only known once a connector is set up.
func TestTheMediaToolsAreFiledTogether(t *testing.T) {
	world(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(mp3ish) }))
	defer srv.Close()
	prov := map[string]any{"transport": "http", "base_url": srv.URL, "auth": map[string]any{"none": true},
		"operations": map[string]any{"make": map[string]any{"method": "POST", "path": "/m", "body": map[string]any{"prompt": "{prompt}"}, "read": "file"}}}
	install(t, "tunes", map[string]any{"provider": prov, "provides": []any{map[string]any{"capability": "music.generate",
		"steps": []any{map[string]any{"call": "make"}}, "output": map[string]any{"audio": map[string]any{"from": "body"}}}}})
	install(t, "clips", map[string]any{"provider": prov, "provides": []any{map[string]any{"capability": "media.generate", "kind": "video",
		"steps": []any{map[string]any{"call": "make"}}, "output": map[string]any{"file": map[string]any{"from": "body"}}}}})
	found := map[string]string{}
	for _, ct := range RegisteredChatTools() {
		if ct.Name() == "generate_music_tunes" || ct.Name() == "generate_video_clips" {
			found[ct.Name()] = ToolCategory(ct)
		}
	}
	for _, name := range []string{"generate_music_tunes", "generate_video_clips"} {
		if found[name] != MediaGenerationToolCategory {
			t.Errorf("%s is filed under %q", name, found[name])
		}
	}
	var described bool
	for _, g := range LoadToolGroups(AuthDB()) {
		if g.Name == MediaGenerationToolCategory && strings.TrimSpace(g.Description) != "" {
			described = true
		}
	}
	if !described {
		t.Error("AI Media Generation is not a category with a description")
	}
}

// A media tool takes files as a list, in order, as many as its connector's
// spec sends (file, then file2 to file6), each a workspace file or a
// picture's id; more than it takes is refused, never quietly cut, and one
// that takes none offers no files at all.
func TestAMediaToolTakesItsFilesInOrder(t *testing.T) {
	world(t)
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		w.Write([]byte("\x00\x00\x00\x18ftypmp42 clip"))
	}))
	defer srv.Close()
	spec := func(body map[string]any) map[string]any {
		return map[string]any{
			"provider": map[string]any{"transport": "http", "base_url": srv.URL, "auth": map[string]any{"none": true},
				"operations": map[string]any{"make": map[string]any{"method": "POST", "path": "/v", "body": body, "read": "file"}}},
			"provides": []any{map[string]any{"capability": "media.generate", "kind": "video",
				"steps": []any{map[string]any{"call": "make"}}, "output": map[string]any{"file": map[string]any{"from": "body"}}}}}
	}
	install(t, "clips", spec(map[string]any{"prompt": "{prompt}", "refs": []any{
		map[string]any{"type": "image", "data": "{file.base64}"}, map[string]any{"type": "image", "data": "{file2.base64}"}}}))
	install(t, "plain", spec(map[string]any{"prompt": "{prompt}"}))
	RootDB.Set(MediaTable, "agents", []string{"clips", "plain"})
	tools := map[string]ChatTool{}
	for _, ct := range RegisteredChatTools() {
		tools[ct.Name()] = ct
	}
	clips, plain := tools["generate_video_clips"], tools["generate_video_plain"]
	if clips == nil || plain == nil {
		t.Fatal("the media tools are not registered")
	}
	if _, params := clips.(DynamicChatTool).SchemaWithSession(nil); params["files"].Type != "array" {
		t.Errorf("a two-picture generator's params: %+v", params)
	}
	if desc, params := plain.(DynamicChatTool).SchemaWithSession(nil); params == nil || params["files"].Type != "" || strings.Contains(desc, "pictures or files") {
		t.Errorf("a prompt-only generator offers files: %q %+v", desc, params)
	}
	ws := t.TempDir()
	os.WriteFile(ws+"/a.png", []byte("\x89PNG\r\n\x1a\nfirst"), 0o600)
	os.WriteFile(ws+"/b.png", []byte("\x89PNG\r\n\x1a\nsecond"), 0o600)
	os.WriteFile(ws+"/c.png", []byte("\x89PNG\r\n\x1a\nthird"), 0o600)
	sess := &ToolSession{WorkspaceDir: ws}
	run := func(ct ChatTool, files ...any) error {
		_, err := ct.(SessionChatTool).RunWithSession(map[string]any{"prompt": "a cat", "files": files}, sess)
		return err
	}
	if err := run(clips, "a.png", "b.png"); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Refs []map[string]string `json:"refs"`
	}
	json.Unmarshal([]byte(got), &body)
	if len(body.Refs) != 2 || body.Refs[0]["data"] != "iVBORw0KGgpmaXJzdA==" || body.Refs[1]["data"] != "iVBORw0KGgpzZWNvbmQ=" {
		t.Errorf("two pictures, in order: %s", got)
	}
	got = ""
	if err := run(clips, "a.png", "b.png", "c.png"); err == nil || !strings.Contains(err.Error(), "at most 2") || got != "" {
		t.Errorf("three pictures to a two-picture generator: %v, sent %q", err, got)
	}
	if err := run(plain, "a.png"); err == nil || !strings.Contains(err.Error(), "takes no pictures") || got != "" {
		t.Errorf("a picture to a prompt-only generator: %v, sent %q", err, got)
	}
	if err := run(clips, "image#r.nothere"); err == nil || strings.Contains(err.Error(), "workspace") {
		t.Errorf("a picture id is read as a picture, not a workspace file: %v", err)
	}
}

// A stored music or media connector's tool is claimed before it is
// registered: an agent naming it keeps it through a restart, which serves
// the connectors after the agents are first read. A name no connector makes
// is not claimed.
func TestAStoredConnectorClaimsItsTool(t *testing.T) {
	world(t)
	prov := map[string]any{"transport": "http", "base_url": "https://api.example.test", "auth": map[string]any{"none": true},
		"operations": map[string]any{"make": map[string]any{"method": "POST", "path": "/m", "body": map[string]any{"prompt": "{prompt}"}, "read": "file"}}}
	for name, binding := range map[string]map[string]any{
		"tunes": {"capability": "music.generate", "steps": []any{map[string]any{"call": "make"}}, "output": map[string]any{"audio": map[string]any{"from": "body"}}},
		"clips": {"capability": "media.generate", "kind": "video", "steps": []any{map[string]any{"call": "make"}}, "output": map[string]any{"file": map[string]any{"from": "body"}}},
	} {
		raw, _ := json.Marshal(map[string]any{"provider": prov, "provides": []any{binding}})
		if err := SaveConnector(RootDB, Connector{Name: name, Kind: Kind, Spec: raw}); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"generate_music_tunes", "generate_video_clips"} {
		if !ToolNameClaimed(name) {
			t.Errorf("%s, made by a stored connector, is not claimed", name)
		}
	}
	for _, name := range []string{"generate_video_gone", "generate_music_clips", "web_search"} {
		if ToolNameClaimed(name) {
			t.Errorf("%s is claimed, and no connector makes it", name)
		}
	}
}
