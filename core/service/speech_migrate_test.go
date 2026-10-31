package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/gohort/core/migrate"
)

// ttsClip is enough of an mp3 for the audio check.
var ttsClip = append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), make([]byte, 32)...)

// ttsServer is a Kokoro-FastAPI at /v1: it speaks, keeping each request's
// key and body, and lists its voices unless noVoices.
func ttsServer(t *testing.T, noVoices bool) (*httptest.Server, *[]string, *[]map[string]any) {
	t.Helper()
	var keys []string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/audio/voices" && !noVoices:
			w.Write([]byte(`{"voices":["af_heart","bm_george"]}`))
		case r.URL.Path == "/v1/audio/speech":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			keys, bodies = append(keys, r.Header.Get("Authorization")), append(bodies, body)
			w.Write(ttsClip)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &keys, &bodies
}

func keepSpeechConfig(t *testing.T) {
	saved := media.GetSpeechConfig()
	t.Cleanup(func() { media.SetSpeechConfig(saved) })
}

// Speech this instance made itself becomes a connector once, at startup: the
// endpoint, model, voice and key it spoke with, and the server's voice list.
// The key moves to a credential, the settings name the connector and keep
// the voice, and speech works through it as before. Run again, it changes
// nothing.
func TestSpeechSettingsBecomeAConnector(t *testing.T) {
	world(t)
	keepSpeechConfig(t)
	srv, keys, bodies := ttsServer(t, false)
	before := media.SpeechConfig{Enabled: true, Endpoint: srv.URL + "/v1/", Model: "kokoro", Voice: "af_heart", APIKey: "sk-voice"}
	RootDB.Set(media.SpeechTable, "current", before)
	media.SetSpeechConfig(before)

	MigrateSpeech(RootDB)

	var after media.SpeechConfig
	RootDB.Get(media.SpeechTable, "current", &after)
	if after.Provider != "service:speech" || after.APIKey != "" || after.Endpoint != "" || after.Model != "kokoro" || after.Voice != "af_heart" || !after.Enabled {
		t.Fatalf("the settings after: %+v", after)
	}
	c, ok := GetConnector(RootDB, "speech")
	if !ok || !c.Approved || !strings.Contains(string(c.Spec), `"credential":"speech_key"`) || !Provides(c, "speech.voices") {
		t.Fatalf("the connector: %+v %v", c, ok)
	}
	audio, ext, err := media.SpeakWith(context.Background(), media.GetSpeechConfig(), "Good morning.")
	if err != nil || ext != ".mp3" || len(audio) != len(ttsClip) {
		t.Fatalf("speaking through the connector: %v %q %d", err, ext, len(audio))
	}
	b := (*bodies)[0]
	if (*keys)[0] != "Bearer sk-voice" || b["input"] != "Good morning." || b["model"] != "kokoro" || b["voice"] != "af_heart" || b["response_format"] != "mp3" {
		t.Errorf("the request: %v %v", *keys, b)
	}
	// An agent's own voice is sent in place of the settings'.
	if _, _, err := media.SpeakIn(context.Background(), "Hello.", "bm_george"); err != nil || (*bodies)[1]["voice"] != "bm_george" {
		t.Errorf("an agent's voice: %v %v", err, (*bodies)[1])
	}
	// The connector speaks in the settings' voice on its own too, as a flow
	// calling it directly does.
	if _, _, err := speakVia(context.Background(), "speech", "Hi.", ""); err != nil || (*bodies)[2]["voice"] != "af_heart" {
		t.Errorf("the connector's own voice: %v %v", err, (*bodies)[2])
	}
	voices := media.SpeechVoices(context.Background(), media.GetSpeechConfig())
	if len(voices) != 2 || voices[1].ID != "bm_george" {
		t.Errorf("the voices: %+v", voices)
	}
	var m migrate.MigrationMarker
	if !RootDB.Get(migrate.MigrationsTable, "core:speech_to_connector:v1:", &m) || !m.Done || m.Error != "" || m.Changed == 0 {
		t.Errorf("Admin > Migrations shows: %+v", m)
	}

	RootDB.Set(media.SpeechTable, "current", before)
	MigrateSpeech(RootDB)
	if _, again := GetConnector(RootDB, "speech-2"); again {
		t.Error("the migration ran twice")
	}
}

// A server that says it has no voice list (OpenAI's /audio/speech) is
// written without one, and one with no model or voice set is sent none, so
// it uses its own.
func TestASpeechServerWithoutAVoiceListMoves(t *testing.T) {
	world(t)
	keepSpeechConfig(t)
	srv, keys, bodies := ttsServer(t, true)
	RootDB.Set(media.SpeechTable, "current", media.SpeechConfig{Enabled: true, Provider: "local", Endpoint: srv.URL + "/v1"})
	if changed, err := migrateSpeech(RootDB, speechHasVoices); err != nil || changed != 2 {
		t.Fatalf("migrate: %d %v", changed, err)
	}
	c, _ := GetConnector(RootDB, "speech")
	if Provides(c, "speech.voices") {
		t.Errorf("a server with no voice list was given one: %s", c.Spec)
	}
	if _, _, err := media.SpeakWith(context.Background(), media.GetSpeechConfig(), "Hi."); err != nil {
		t.Fatal(err)
	}
	if _, sent := (*bodies)[0]["model"]; sent || (*keys)[0] != "" {
		t.Errorf("a model or key nobody set was sent: %v %v", *keys, (*bodies)[0])
	}
	if _, sent := (*bodies)[0]["voice"]; sent {
		t.Errorf("a voice nobody chose was sent: %v", (*bodies)[0])
	}
	if v := media.SpeechVoices(context.Background(), media.GetSpeechConfig()); len(v) != 0 {
		t.Errorf("voices from a connector with no list: %+v", v)
	}
	// A server that cannot be reached as the move is made keeps its list:
	// only one that answers that it has none loses it.
	if !speechHasVoices("http://127.0.0.1:1/v1", "") {
		t.Error("an unreachable server lost its voice list")
	}
}

// Settings with no endpoint, or that already name a connector or a peer,
// are left alone.
func TestSpeechWithNothingToMoveIsLeftAlone(t *testing.T) {
	world(t)
	keepSpeechConfig(t)
	probed := false
	probe := func(string, string) bool { probed = true; return true }
	if changed, err := migrateSpeech(RootDB, probe); err != nil || changed != 0 {
		t.Errorf("never set up: %d %v", changed, err)
	}
	for _, cfg := range []media.SpeechConfig{
		{Enabled: true, Voice: "af_heart"},
		{Enabled: true, Provider: "peer:den", Endpoint: "https://den.example/api/peer/v1"},
		{Enabled: true, Provider: "service:other"},
	} {
		RootDB.Set(media.SpeechTable, "current", cfg)
		if changed, err := migrateSpeech(RootDB, probe); err != nil || changed != 0 {
			t.Errorf("%+v was moved: %d %v", cfg, changed, err)
		}
	}
	if probed || len(ListConnectors(RootDB)) != 0 {
		t.Errorf("probed %v, connectors %+v", probed, ListConnectors(RootDB))
	}
}

// The voice list contract reads a list of names or objects, and its check
// refuses entries that name no voice.
func TestTheVoiceListIsChecked(t *testing.T) {
	ct, _ := Lookup("speech.voices")
	for list, ok := range map[string]bool{
		`["af_heart",{"voice_id":"21m00","name":"Rachel"}]`: true,
		`[]`:                       true,
		`[{"category":"premade"}]`: false,
		`{"voices":[]}`:            false,
	} {
		if err := ct.Check(Result{"voices": Value{Text: list}}); (err == nil) != ok {
			t.Errorf("%s: %v", list, err)
		}
	}
	vs, _ := Voices(Result{"voices": Value{Text: `["af_heart",{"voice_id":"21m00","name":"Rachel"}]`}})
	if len(vs) != 2 || vs[1] != (media.Voice{ID: "21m00", Name: "Rachel"}) {
		t.Errorf("voices: %+v", vs)
	}
}

// The local template speaks in its default voice and lists the server's
// voices; ElevenLabs lists its account's voices by name.
func TestTheSpeechTemplatesListVoices(t *testing.T) {
	world(t)
	srv, _, bodies := ttsServer(t, false)
	install(t, "local-speech", templateSpec(t, "openai-compatible-speech", srv.URL+"/v1"))
	if audio, _, err := speakVia(context.Background(), "local-speech", "Hi.", ""); err != nil || len(audio) == 0 {
		t.Fatalf("speak: %v", err)
	}
	if b := (*bodies)[0]; b["voice"] != "af_heart" || b["model"] != "kokoro" {
		t.Errorf("the request: %v", b)
	}
	if v, err := voicesVia(context.Background(), "local-speech"); err != nil || len(v) != 2 {
		t.Errorf("voices: %+v %v", v, err)
	}
	if h, err := CheckHealth(context.Background(), "local-speech"); err != nil || !h.OK {
		t.Errorf("health: %+v %v", h, err)
	}

	el := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"voices":[{"voice_id":"21m00","name":"Rachel","category":"premade"}]}`))
	}))
	defer el.Close()
	eleven := templateSpec(t, "elevenlabs-speech", el.URL)
	if !strings.Contains(mustJSON(t, eleven), `"speech.voices"`) {
		t.Fatal("the ElevenLabs template lists no voices")
	}
	install(t, "eleven", eleven)
	if v, err := voicesVia(context.Background(), "eleven"); err != nil || len(v) != 1 || v[0] != (media.Voice{ID: "21m00", Name: "Rachel"}) {
		t.Errorf("ElevenLabs voices: %+v %v", v, err)
	}
	// A connector that declares no list has none, and is not called for one.
	install(t, "plain", speechMigrationSpec(media.SpeechConfig{Endpoint: "http://127.0.0.1:1/v1"}, "", false))
	if v, err := voicesVia(context.Background(), "plain"); err != nil || v != nil {
		t.Errorf("a connector with no list: %+v %v", v, err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A speech server that answers in ogg keeps its extension, so the clip is
// sent and played as what it is.
func TestAnOggClipKeepsItsExtension(t *testing.T) {
	world(t)
	ogg := append([]byte("OggS\x00\x02"), make([]byte, 64)...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(ogg) }))
	defer srv.Close()
	install(t, "oggy", speechMigrationSpec(media.SpeechConfig{Endpoint: srv.URL}, "", false))
	if _, ext, err := speakVia(context.Background(), "oggy", "Hi.", ""); err != nil || ext != ".ogg" {
		t.Errorf("an ogg clip: %q %v", ext, err)
	}
}
