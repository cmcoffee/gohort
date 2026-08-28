package core

// dispatch writes for a MODEL: it prefixes "HTTP 200 OK\n" and reports a failed
// status inside that line rather than as an error, because a model needs to
// read the failure. Handed straight back to the media package, that made every
// transcript in the build start with a junk status line — the transcribe tool
// framed "HTTP 200 OK\nthe quick brown fox" as the spoken content — and turned
// an HTTP 500 into a successful transcription whose text was the error page.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/media"
)

func TestMediaUploadBodyStripsTheModelFacingStatusLine(t *testing.T) {
	got, err := mediaUploadBody("HTTP 200 OK\nthe quick brown fox")
	if err != nil {
		t.Fatalf("a 200 must not be an error: %v", err)
	}
	if got != "the quick brown fox" {
		t.Errorf("body = %q — the status line is still in the transcript", got)
	}

	// Multi-line transcripts keep every line after the first.
	got, err = mediaUploadBody("HTTP 200 OK\nline one\nline two\n")
	if err != nil {
		t.Fatal(err)
	}
	if got != "line one\nline two\n" {
		t.Errorf("multi-line body = %q", got)
	}
}

func TestMediaUploadBodyPromotesAFailingStatusToAnError(t *testing.T) {
	// This is the one that silently corrupted data: no error, so Transcribe
	// returned the error page AS the transcript and every caller believed it.
	_, err := mediaUploadBody("HTTP 500 Internal Server Error\nmodel failed to load")
	if err == nil {
		t.Fatal("a 500 came back as a successful transcription")
	}
	if !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "model failed to load") {
		t.Errorf("the error should carry the status and the body: %v", err)
	}
}

func TestMediaUploadBodyLeavesAnUnrecognizedShapeAlone(t *testing.T) {
	// No status line to strip: eating the first line might eat content.
	for _, in := range []string{"just a transcript", "first line\nsecond line"} {
		got, err := mediaUploadBody(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got != in {
			t.Errorf("mediaUploadBody(%q) = %q, want it untouched", in, got)
		}
	}
}

// The governed upload path synthesized an UNAUTHENTICATED credential, so a
// local whisper.cpp kept working while every authenticated endpoint — real
// OpenAI, a peer instance — lost its key on the way out and answered 401 to a
// configuration that was correct. The key cannot ride as a request header
// (dispatch strips caller-supplied Authorization on purpose, so auth always
// comes from the credential), so it rides on the credential itself, inline.
func TestAnAuthenticatedMediaUploadCarriesItsKey(t *testing.T) {
	peerImageDB(t)
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte("transcribed"))
	}))
	t.Cleanup(srv.Close)

	out, err := media.GovernedUploadFunc(t.Context(), media.UploadRequest{
		URL: srv.URL + "/audio/transcriptions", FieldName: "file", FileName: "a.mp3",
		Body: []byte("audio"), Fields: map[string]string{"response_format": "text"}, Bearer: "sk-secret",
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if gotAuth != "Bearer sk-secret" {
		t.Errorf("Authorization = %q — an authenticated endpoint would 401", gotAuth)
	}
	if strings.TrimSpace(out) != "transcribed" {
		t.Errorf("body = %q", out)
	}

	// And with no key the call stays unauthenticated, which is what a local
	// whisper.cpp expects.
	gotAuth = ""
	if _, err := media.GovernedUploadFunc(t.Context(), media.UploadRequest{
		URL: srv.URL + "/audio/transcriptions", FieldName: "file", FileName: "a.mp3",
		Body: []byte("audio"),
	}); err != nil {
		t.Fatalf("unauthenticated upload: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("an unauthenticated upload sent %q", gotAuth)
	}
}

// The inline secret must never reach the store or a serialized credential — it
// is an in-process hand-off, not a registration.
func TestInlineSecretIsNotSerializedOrStored(t *testing.T) {
	c := SecureCredential{Name: "media_local", Type: SecureCredBearer, inlineSecret: "sk-secret"}
	blob, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), "sk-secret") {
		t.Errorf("the inline secret serialized into the credential JSON: %s", blob)
	}
}
