package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
