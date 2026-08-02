package core

// Streaming file upload through the governed dispatch. The point is not that
// multipart works — stdlib does that — but that a file transfer is covered by
// the same allow-list, audit, and cancellation as every other outbound call,
// and that the bytes are never held twice.

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// scopedFor mirrors what a LAN endpoint gets in production: an unauthenticated
// credential pinned to that one scheme+host. The legacy "no_auth" credential is
// https-only, so it cannot reach an httptest server — and shouldn't.
func scopedFor(url string) SecureCredential {
	return SecureCredential{
		Name:              "upload_test_local",
		Type:              SecureCredNone,
		AllowedURLPattern: imageHostPattern(url),
	}
}

func dispatchUploadScoped(t *testing.T, sess *ToolSession, url string, up FileUpload) (string, error) {
	t.Helper()
	return Secure().dispatch(scopedFor(url), map[string]any{
		"url": url, "method": "POST", secureUploadArg: &up,
	}, sess)
}

type uploadSeen struct {
	contentType string
	length      int64
	field       string
	filename    string
	body        []byte
	fields      map[string]string
}

func uploadEchoServer(t *testing.T, seen *uploadSeen, reply string) *httptest.Server {
	t.Helper()
	seen.fields = map[string]string{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.contentType = r.Header.Get("Content-Type")
		seen.length = r.ContentLength
		mt, params, err := mime.ParseMediaType(seen.contentType)
		if err != nil || !strings.HasPrefix(mt, "multipart/") {
			http.Error(w, "not multipart", http.StatusUnsupportedMediaType)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			raw, _ := io.ReadAll(part)
			if part.FileName() != "" {
				seen.field, seen.filename, seen.body = part.FormName(), part.FileName(), raw
			} else {
				seen.fields[part.FormName()] = string(raw)
			}
		}
		_, _ = w.Write([]byte(reply))
	}))
}

func TestDispatchUploadStreamsTheFile(t *testing.T) {
	secureAPITestStore(t)
	var seen uploadSeen
	srv := uploadEchoServer(t, &seen, `{"ok":true}`)
	defer srv.Close()

	payload := bytes.Repeat([]byte("abcdefgh"), 4096) // 32 KB
	_, err := dispatchUploadScoped(t, &ToolSession{}, srv.URL+"/upload", FileUpload{
		Reader:    bytes.NewReader(payload),
		FieldName: "file",
		FileName:  "clip.mp3",
		Fields:    map[string]string{"model": "whisper-1", "response_format": "text"},
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if !bytes.Equal(seen.body, payload) {
		t.Errorf("received %d bytes, sent %d", len(seen.body), len(payload))
	}
	if seen.field != "file" || seen.filename != "clip.mp3" {
		t.Errorf("part = %q/%q", seen.field, seen.filename)
	}
	if seen.fields["model"] != "whisper-1" || seen.fields["response_format"] != "text" {
		t.Errorf("extra fields = %v", seen.fields)
	}
	// ContentLength -1 is the signal that mimebody wrapped the body in a
	// stream rather than buffering it to measure a length.
	if seen.length != -1 {
		t.Errorf("ContentLength = %d, want -1 (chunked/streamed)", seen.length)
	}
}

func TestUploadObeysTheCredentialAllowList(t *testing.T) {
	// The reason for routing uploads here at all. An http.Client of its own
	// would post user audio anywhere the endpoint config named.
	secureAPITestStore(t)
	var seen uploadSeen
	srv := uploadEchoServer(t, &seen, `{}`)
	defer srv.Close()

	scoped := SecureCredential{
		Name:              "scoped_test",
		Type:              SecureCredNone,
		AllowedURLPattern: imageHostPattern(srv.URL),
	}
	_, err := Secure().dispatch(scoped, map[string]any{
		"url": "http://example.invalid/upload", "method": "POST",
		secureUploadArg: &FileUpload{Reader: bytes.NewReader([]byte("x")), FileName: "a.txt"},
	}, &ToolSession{})
	if err == nil {
		t.Fatal("an upload to a host outside the allow-list must be refused")
	}
	if len(seen.body) != 0 {
		t.Error("nothing should have reached the server")
	}
}

func TestUploadRefusedInPrivateMode(t *testing.T) {
	// Private mode is a turn-level kill switch on network egress. A file
	// transfer is exactly the call it most needs to stop.
	secureAPITestStore(t)
	var seen uploadSeen
	srv := uploadEchoServer(t, &seen, `{}`)
	defer srv.Close()

	sess := &ToolSession{Network: NewNetworkConnector(true)}
	_, err := dispatchUploadScoped(t, sess, srv.URL+"/upload", FileUpload{
		Reader: bytes.NewReader([]byte("secret audio")), FileName: "a.mp3",
	})
	if err == nil {
		t.Fatal("Private mode must block an upload")
	}
	if !strings.Contains(err.Error(), "Private mode") {
		t.Errorf("error should name the cause: %v", err)
	}
	if len(seen.body) != 0 {
		t.Error("no bytes may leave the machine with egress off")
	}
}

func TestUploadFieldNameDefaults(t *testing.T) {
	secureAPITestStore(t)
	var seen uploadSeen
	srv := uploadEchoServer(t, &seen, `{}`)
	defer srv.Close()

	if _, err := dispatchUploadScoped(t, &ToolSession{}, srv.URL+"/u", FileUpload{
		Reader: bytes.NewReader([]byte("hi")), FileName: "a.txt",
	}); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if seen.field != "file" {
		t.Errorf("field = %q, want the \"file\" default", seen.field)
	}
}

func TestUploadTimeoutIsItsOwnTunable(t *testing.T) {
	// A file takes as long as the link allows; the 30s cap that suits an API
	// call would abort a photo on a slow uplink.
	up, ok := LookupTunable("tune_secure_api_upload_timeout")
	if !ok {
		t.Fatal("upload timeout is not a registered tunable")
	}
	req, _ := LookupTunable("tune_secure_api_request_timeout")
	if up.Default <= req.Default {
		t.Errorf("upload default %v must exceed the request default %v", up.Default, req.Default)
	}
	if up.Category != "Timeouts" {
		t.Errorf("category = %q", up.Category)
	}
}
