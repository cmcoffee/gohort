package media

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The names a speaker is likely to say reach a peer's transcriber as its
// prompt, on both roads a clip leaves by, and nothing goes when there are
// none.
func TestATranscriptionPromptReachesTheTranscriber(t *testing.T) {
	var prompt string
	var hadPrompt bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(1 << 20)
		_, hadPrompt = r.MultipartForm.Value["prompt"]
		prompt = r.FormValue("prompt")
		io.WriteString(w, "ok")
	}))
	defer srv.Close()
	prevUpload := GovernedUploadFunc
	t.Cleanup(func() { GovernedUploadFunc = prevUpload })
	GovernedUploadFunc = nil
	cfg := TranscribeConfig{Endpoint: srv.URL, Enabled: true, Provider: "peer:den"}

	ctx := WithTranscribePrompt(context.Background(), "  Talking with Pemberly.  ")
	if _, err := TranscribeWith(ctx, cfg, []byte("audio"), "a.wav"); err != nil || prompt != "Talking with Pemberly." {
		t.Errorf("direct: prompt %q, err %v", prompt, err)
	}
	if _, err := TranscribeWith(context.Background(), cfg, []byte("audio"), "a.wav"); err != nil || hadPrompt {
		t.Errorf("no prompt on the context should send none: had %v, err %v", hadPrompt, err)
	}

	var fields map[string]string
	GovernedUploadFunc = func(_ context.Context, u UploadRequest) (string, error) { fields = u.Fields; return "ok", nil }
	if _, err := TranscribeWith(ctx, cfg, []byte("audio"), "a.wav"); err != nil || fields["prompt"] != "Talking with Pemberly." {
		t.Errorf("governed: fields %v, err %v", fields, err)
	}
	if _, err := TranscribeWith(context.Background(), cfg, []byte("audio"), "a.wav"); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["prompt"]; ok {
		t.Errorf("governed with no prompt should send none: %v", fields)
	}

	long := WithTranscribePrompt(context.Background(), strings.Repeat("é", transcribePromptMax+50))
	if got := []rune(TranscribePrompt(long)); len(got) != transcribePromptMax {
		t.Errorf("a long prompt is cut to %d runes, got %d", transcribePromptMax, len(got))
	}
	if TranscribePrompt(WithTranscribePrompt(context.Background(), "   ")) != "" {
		t.Error("a blank prompt is none")
	}
}

// Transcription is done by a connector or a peer: settings naming neither,
// as this instance's own endpoint once was, are refused without sending.
func TestATranscriberThatIsNeitherConnectorNorPeerIsRefused(t *testing.T) {
	sent := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = true
		io.WriteString(w, "heard")
	}))
	defer srv.Close()
	prevUpload, prevService := GovernedUploadFunc, TranscribeService
	t.Cleanup(func() { GovernedUploadFunc, TranscribeService = prevUpload, prevService })
	GovernedUploadFunc = nil
	for _, p := range []string{"", "local"} {
		_, err := TranscribeWith(context.Background(), TranscribeConfig{Enabled: true, Endpoint: srv.URL, Provider: p}, []byte("audio"), "a.wav")
		if err == nil || !strings.Contains(err.Error(), "not set to a connector or a peer") || sent {
			t.Errorf("provider %q: %v, sent %v", p, err, sent)
		}
	}
	var got string
	TranscribeService = func(ctx context.Context, connector string, audio []byte, filename string) (string, error) {
		got = connector + "/" + filename + "/" + TranscribePrompt(ctx)
		return "heard", nil
	}
	ctx := WithTranscribePrompt(context.Background(), "Pemberly")
	if text, err := TranscribeWith(ctx, TranscribeConfig{Enabled: true, Provider: "stt"}, []byte("audio"), "a.wav"); err != nil || text != "heard" || got != "stt/a.wav/Pemberly" || sent {
		t.Errorf("a connector: %q %v %q", text, err, got)
	}
}

// A setting names a connection by its plain name. A peer, a name a page
// keeps for something built in, and nothing are not connections; nor is
// the old "<job>:<name>" shape, which is read only to say which connection
// it meant.
func TestASettingNamesItsConnectionPlainly(t *testing.T) {
	for _, v := range []string{"serper-search", " local-embeddings "} {
		if name, ok := ChosenConnection(v); !ok || name != strings.TrimSpace(v) {
			t.Errorf("ChosenConnection(%q) = %q %v", v, name, ok)
		}
	}
	for _, v := range []string{"peer:den", "duckduckgo", "llama.cpp", "OpenAI", "none", "smtp", "local", "", "llm:anthropic",
		"search:serper-search", "http://x.example:8080"} {
		if IsConnection(v) {
			t.Errorf("%q read as a connection", v)
		}
	}
	for v, want := range map[string]string{"llm:anthropic": "anthropic", "service:studio": "studio", " embed:local-embeddings ": "local-embeddings",
		"search:serper-search": "serper-search"} {
		if name, ok := LegacyConnection(v); !ok || name != want {
			t.Errorf("LegacyConnection(%q) = %q %v", v, name, ok)
		}
	}
	for _, v := range []string{"peer:den", "serper-search", "llm:", "http://x.example:8080", "duckduckgo"} {
		if name, ok := LegacyConnection(v); ok {
			t.Errorf("%q read as the old shape, naming %q", v, name)
		}
	}
}
