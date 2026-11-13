package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/media"
)

// A connector whose host answered "too many requests" is left alone until
// the time it named: a call that cannot wait that long fails at once without
// sending, rather than adding to the refusals.
func TestARateLimitedConnectorIsWaitedOut(t *testing.T) {
	world(t)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "30")
		http.Error(w, `{"error":"rate limit reached"}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()
	install(t, "busy", embedSpec("m"))
	c, _ := GetConnector(RootDB, "busy")
	if err := SaveSettings(c, "", map[string]string{"endpoint": srv.URL}, map[string]string{"api_key": "k"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Call(context.Background(), nil, "busy", "embed.text", map[string]any{"texts": []string{"a"}}); err == nil {
		t.Fatal("the refused call reported success")
	}
	if d := media.PausedFor(srv.URL); d < 25*time.Second || d > 30*time.Second {
		t.Errorf("paused for %s, want the 30s Retry-After", d)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := Call(ctx, nil, "busy", "embed.text", map[string]any{"texts": []string{"b"}})
	if err == nil || !strings.Contains(err.Error(), "rate limited") || atomic.LoadInt32(&calls) != 1 {
		t.Errorf("err %v after %d call(s)", err, calls)
	}
}

// A picture an API takes inline in its JSON goes as base64 with its type, and
// the parts for pictures not given are no parts. Gemini edits from a request
// whose contents carry the prompt and each picture as inlineData; sending an
// empty object for a second picture that was never passed is a 400.
func TestAJSONBodyCarriesAFileAsBase64(t *testing.T) {
	path := filepath.Join(t.TempDir(), "in.png")
	data := []byte("\x89PNG\r\n\x1a\nnot really a picture")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	part := func(n string) map[string]any {
		return map[string]any{"inlineData": map[string]any{"mimeType": "{" + n + ".type}", "data": "{" + n + ".base64}"}}
	}
	op := Operation{Body: map[string]any{"contents": []any{map[string]any{"parts": []any{
		map[string]any{"text": "{prompt}"}, part("image"), part("image2")}}}}}
	body, ctype, err := renderRequest(op, map[string]any{"prompt": "make it blue", "image": &File{Path: path, Type: "image/png", Size: int64(len(data))}})
	if err != nil || ctype != "application/json" {
		t.Fatalf("render: %v (%s)", err, ctype)
	}
	var got struct {
		Contents []struct {
			Parts []map[string]any `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("not JSON: %s", body)
	}
	parts := got.Contents[0].Parts
	if len(parts) != 2 {
		t.Fatalf("want the prompt and one picture, got %d parts: %s", len(parts), body)
	}
	inline, _ := parts[1]["inlineData"].(map[string]any)
	if inline["mimeType"] != "image/png" || inline["data"] != base64.StdEncoding.EncodeToString(data) {
		t.Errorf("the picture is not inline as base64 with its type: %v", inline)
	}
}

// A file field is the whole string, and names one of the contract's file
// inputs: written into text, or naming a text input, it is refused when the
// spec is checked rather than sending a broken request.
func TestAFileFieldIsAWholeStringNamingAFileInput(t *testing.T) {
	c, ok := Lookup("image.edit")
	if !ok {
		t.Fatal("no image.edit contract")
	}
	for _, body := range []any{
		map[string]any{"data": "prefix {image.base64}"},
		map[string]any{"data": "{prompt.base64}"},
	} {
		if err := c.checkFileSend("edit", Operation{Body: body}); err == nil {
			t.Errorf("%v was accepted", body)
		}
	}
	if err := c.checkFileSend("edit", Operation{Body: map[string]any{"data": "{image.base64}", "mime": "{image.type}"}}); err != nil {
		t.Errorf("a whole-string file field was refused: %v", err)
	}
}

// A request is logged by its path, never its query, where a key may ride, and
// with its pictures shown by size.
func TestARequestIsLoggedWithoutKeysOrData(t *testing.T) {
	if got := pathOnly("https://api.example.test/v1/models?key=SECRET#x"); got != "https://api.example.test/v1/models" {
		t.Errorf("path: %q", got)
	}
	body := `{"prompt":"make it snowy","data":"` + strings.Repeat("QUJD", 300) + `"}`
	got := elideData(body)
	if strings.Contains(got, "QUJDQUJD") || !strings.Contains(got, "make it snowy") || !strings.Contains(got, "of data>") {
		t.Errorf("body: %s", got)
	}
}

// A JSON reply is read to the connector's own size limit, as a file reply
// is. Gemini's image edits come back as JSON carrying the picture as base64:
// held to a fixed 4 MB, a larger picture was cut off mid-reply and failed as
// "the reply is not JSON". A reply past the limit says so, not that it is
// not JSON.
func TestALargeJSONReplyIsReadWhole(t *testing.T) {
	picture := base64.StdEncoding.EncodeToString(make([]byte, 5<<20)) // ~6.7 MB of base64
	body, _ := json.Marshal(map[string]any{"candidates": []any{map[string]any{"data": picture}}})
	reply := func() *http.Response {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body))}
	}
	r, err := transport{}.readResponse("edit", Operation{}, reply(), nil)
	if err != nil {
		t.Fatalf("a %d-byte JSON reply: %v", len(body), err)
	}
	if r.node == nil {
		t.Error("the reply was not read as JSON")
	}
	small := transport{p: Provider{Limits: &Limits{MaxBytes: 1 << 20}}}
	_, err = small.readResponse("edit", Operation{}, reply(), nil)
	if err == nil || !strings.Contains(err.Error(), "larger than this connector's limit") || strings.Contains(err.Error(), "not JSON") {
		t.Errorf("a reply past the connector's limit: %v", err)
	}
}

// The same through the governed dispatch a JSON operation takes, which is
// where Gemini's edits go: its piped read stopped at 4 MB with no marker, so a
// larger reply arrived cut and failed as "not JSON". The reply is read to the
// connector's limit, and one past it says it is too large.
func TestALargeJSONReplyThroughTheConnectionIsReadWhole(t *testing.T) {
	world(t)
	pad := strings.Repeat("A", 6<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"embedding":[0.1,0.2,0.3,0.4]}],"pad":"`+pad+`"}`)
	}))
	defer srv.Close()
	for _, tc := range []struct {
		name  string
		limit int64
		want  string
	}{{"big", 0, ""}, {"small", 1 << 20, "larger than the"}} {
		spec := embedSpec("m")
		if tc.limit > 0 {
			spec["provider"].(map[string]any)["limits"] = map[string]any{"max_bytes": tc.limit}
		}
		install(t, tc.name, spec)
		c, _ := GetConnector(RootDB, tc.name)
		if err := SaveSettings(c, "", map[string]string{"endpoint": srv.URL}, map[string]string{"api_key": "k"}); err != nil {
			t.Fatal(err)
		}
		_, err := Call(context.Background(), nil, tc.name, "embed.text", map[string]any{"texts": []string{"a"}})
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: a %d MB JSON reply within the limit: %v", tc.name, len(pad)>>20, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "not JSON")):
			t.Errorf("%s: a reply past the connector's limit: %v", tc.name, err)
		}
	}
}
