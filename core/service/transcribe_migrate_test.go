package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/gohort/core/migrate"
)

// sttServer is a transcriber at /v1/audio/transcriptions answering text,
// keeping each request's key and form.
type sttRequest struct {
	key, file string
	fields    map[string]string
}

func sttServer(t *testing.T, text string) (*httptest.Server, *[]sttRequest) {
	t.Helper()
	var got []sttRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		req := sttRequest{key: r.Header.Get("Authorization"), fields: map[string]string{}}
		for k, v := range r.MultipartForm.Value {
			req.fields[k] = v[0]
		}
		if fh := r.MultipartForm.File["file"]; len(fh) > 0 {
			req.file = fh[0].Filename
		}
		got = append(got, req)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"text":"` + text + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

// keepTranscribeConfig restores the process-wide transcription config after
// a test.
func keepTranscribeConfig(t *testing.T) {
	saved := GetTranscribeConfig()
	t.Cleanup(func() { SetTranscribeConfig(saved) })
}

// Transcription this instance did itself becomes a connector once, at
// startup: the endpoint, model and key it sent audio with. The key moves to
// a credential, the settings name the connector, and transcription works
// through it as it did before, the words the speaker is likely to say still
// going as its prompt. Run again, it changes nothing.
func TestTranscriptionSettingsBecomeAConnector(t *testing.T) {
	world(t)
	keepTranscribeConfig(t)
	srv, got := sttServer(t, "meet me at noon")
	before := TranscribeConfig{Enabled: true, Endpoint: srv.URL + "/v1/", Model: "whisper-1", APIKey: "sk-ears"}
	RootDB.Set(TranscribeTable, "current", before)
	SetTranscribeConfig(before)

	MigrateTranscription(RootDB)

	var after TranscribeConfig
	RootDB.Get(TranscribeTable, "current", &after)
	if after.Provider != "service:transcription" || after.APIKey != "" || after.Endpoint != "" || after.Model != "whisper-1" || !after.Enabled {
		t.Fatalf("the settings after: %+v", after)
	}
	c, ok := GetConnector(RootDB, "transcription")
	if !ok || !c.Approved || c.Kind != Kind || !strings.Contains(string(c.Spec), `"credential":"transcription_key"`) {
		t.Fatalf("the connector: %+v %v", c, ok)
	}
	ctx := media.WithTranscribePrompt(context.Background(), "Talking with Quill.")
	text, err := TranscribeWith(ctx, GetTranscribeConfig(), []byte("RIFF....WAVEfmt "), "memo.wav")
	if err != nil || text != "meet me at noon" {
		t.Fatalf("transcribing through the connector: %q %v", text, err)
	}
	if len(*got) != 1 {
		t.Fatalf("requests: %+v", *got)
	}
	r := (*got)[0]
	if r.key != "Bearer sk-ears" || r.file == "" || !strings.HasSuffix(r.file, ".wav") || r.fields["model"] != "whisper-1" || r.fields["prompt"] != "Talking with Quill." || r.fields["response_format"] != "json" {
		t.Errorf("the request: %+v", r)
	}
	if _, sent := r.fields["language"]; sent {
		t.Errorf("a language nobody gave was sent: %+v", r.fields)
	}
	var m migrate.MigrationMarker
	if !RootDB.Get(migrate.MigrationsTable, "core:transcription_to_connector:v1:", &m) || !m.Done || m.Error != "" || m.Changed == 0 {
		t.Errorf("Admin > Migrations shows: %+v", m)
	}

	// Once: settings put back the old way are not moved again.
	RootDB.Set(TranscribeTable, "current", before)
	MigrateTranscription(RootDB)
	if _, again := GetConnector(RootDB, "transcription-2"); again {
		t.Error("the migration ran twice")
	}
}

// A whisper.cpp server takes no model and is sent none, and no key means no
// credential. The connector's name is the first one free.
func TestAModelLessTranscriberMoves(t *testing.T) {
	world(t)
	keepTranscribeConfig(t)
	srv, got := sttServer(t, "")
	install(t, "transcription", transcribeMigrationSpec(TranscribeConfig{Endpoint: "https://api.example.test/v1"}, ""))
	RootDB.Set(TranscribeTable, "current", TranscribeConfig{Enabled: true, Provider: "local", Endpoint: srv.URL + "/v1"})
	if changed, err := migrateTranscription(RootDB); err != nil || changed != 2 {
		t.Fatalf("migrate: %d %v", changed, err)
	}
	cfg := GetTranscribeConfig()
	if cfg.Provider != "service:transcription-2" {
		t.Fatalf("the settings: %+v", cfg)
	}
	c, _ := GetConnector(RootDB, "transcription-2")
	if !strings.Contains(string(c.Spec), `"none":true`) || !strings.Contains(c.Desc, "the server's own model") {
		t.Errorf("the connector: %s %s", c.Spec, c.Desc)
	}
	// A clip with no speech in it transcribes to nothing, which is no error.
	if text, err := TranscribeWith(context.Background(), GetTranscribeConfig(), []byte("audio"), "a.mp3"); err != nil || text != "" {
		t.Errorf("silence: %q %v", text, err)
	}
	if _, sent := (*got)[0].fields["model"]; sent {
		t.Errorf("a model-less server was sent a model: %+v", (*got)[0].fields)
	}
}

// Settings with no endpoint have nothing to move (transcription stays off
// until a connector or peer is chosen), and settings that already name a
// connector or a peer are left alone.
func TestTranscriptionWithNothingToMoveIsLeftAlone(t *testing.T) {
	world(t)
	keepTranscribeConfig(t)
	if changed, err := migrateTranscription(RootDB); err != nil || changed != 0 {
		t.Errorf("never set up: %d %v", changed, err)
	}
	for _, cfg := range []TranscribeConfig{
		{Enabled: false},
		{Enabled: true, Provider: "peer:den", Endpoint: "https://den.example/api/peer/v1"},
		{Enabled: true, Provider: "service:other"},
	} {
		RootDB.Set(TranscribeTable, "current", cfg)
		if changed, err := migrateTranscription(RootDB); err != nil || changed != 0 {
			t.Errorf("%+v was moved: %d %v", cfg, changed, err)
		}
	}
	if cs := ListConnectors(RootDB); len(cs) != 0 {
		t.Errorf("connectors made: %+v", cs)
	}
}

// The local template transcribes at its server's /audio/transcriptions with
// the model it was given, and reads the text back.
func TestTheLocalTranscriptionTemplateTranscribes(t *testing.T) {
	world(t)
	srv, got := sttServer(t, "hello there")
	install(t, "local-transcription", templateSpec(t, "openai-compatible-transcription", srv.URL+"/v1"))
	text, err := transcribeVia(context.Background(), "local-transcription", []byte("audio"), "clip.m4a")
	if err != nil || text != "hello there" {
		t.Fatalf("transcribe: %q %v", text, err)
	}
	if r := (*got)[0]; r.fields["model"] != "whisper-1" || !strings.HasSuffix(r.file, ".m4a") || r.key != "" {
		t.Errorf("the request: %+v", r)
	}
}
