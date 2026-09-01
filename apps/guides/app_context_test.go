package guides

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

// The open guide is stamped onto the send body server-side, which is the only
// place that knows it on the turn that CREATES the session — the one turn no
// client could name a session id for.
func TestStampAppContextFilesTheConversation(t *testing.T) {
	r := httptest.NewRequest("POST", "/chat/send", strings.NewReader(
		`{"message":"add an intro","session_id":"","images":["abc"]}`))
	stampAppContext(r, "guide-42")

	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("the rewritten body must still be the JSON orchestrate decodes: %v", err)
	}
	if got["app_context"] != "guide-42" {
		t.Errorf("app_context = %v, want the open guide", got["app_context"])
	}
	// Everything else must survive: this body is the user's actual message and
	// its attachments, not a form we get to rebuild.
	if got["message"] != "add an intro" {
		t.Errorf("the message was lost: %v", got["message"])
	}
	if imgs, ok := got["images"].([]any); !ok || len(imgs) != 1 {
		t.Errorf("attachments were lost: %v", got["images"])
	}
	if r.ContentLength != int64(len(raw)) {
		t.Errorf("ContentLength %d does not match the %d bytes now in the body", r.ContentLength, len(raw))
	}
}

// Failing to file a conversation is not a reason to refuse to have it. Every
// bail-out path must leave a body orchestrate can still read.
func TestStampAppContextNeverEatsTheRequest(t *testing.T) {
	cases := []struct{ name, body, guide string }{
		{"unparseable body", `not json at all`, "guide-1"},
		{"no guide open", `{"message":"hi"}`, ""},
		{"json that is not an object", `["a","b"]`, "guide-1"},
		{"null body", `null`, "guide-1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/chat/send", strings.NewReader(c.body))
			stampAppContext(r, c.guide)
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("body unreadable after stamping: %v", err)
			}
			if string(raw) != c.body {
				t.Errorf("body was altered on a path that should pass it through:\n got %q\nwant %q", raw, c.body)
			}
		})
	}
}
