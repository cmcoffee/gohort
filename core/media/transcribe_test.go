package media

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The names a speaker is likely to say reach the transcriber as its prompt,
// on both roads a clip leaves by, and nothing goes when there are none.
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
	cfg := TranscribeConfig{Endpoint: srv.URL, Enabled: true}

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
