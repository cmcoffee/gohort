package orchestrate

// The body peek restores what it reads, so anything it truncates is corrupt
// JSON handed to the real handler. At 1 MiB that fired on any chat send
// carrying a photo — base64 is ~1.37x the file — and the attachment was simply
// not in the request the handler decoded. The model then answered, truthfully,
// that no image was attached.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sendBodyWithImage builds a chat send whose base64 attachment is n bytes.
func sendBodyWithImage(n int) []byte {
	b, _ := json.Marshal(map[string]any{
		"agent_id": "wren",
		"message":  "What is this image?",
		"images":   []string{strings.Repeat("A", n)},
	})
	return b
}

func TestBodyPeekSurvivesAPhotoSizedAttachment(t *testing.T) {
	// 4 MiB of base64 — a perfectly ordinary phone photo.
	body := sendBodyWithImage(4 << 20)
	r := httptest.NewRequest(http.MethodPost, "/api/send", bytes.NewReader(body))

	raw, err := readAndRestoreBody(r)
	if err != nil {
		t.Fatalf("peek failed on a %d-byte body: %v", len(body), err)
	}
	if len(raw) != len(body) {
		t.Fatalf("peek read %d of %d bytes", len(raw), len(body))
	}
	// What the real handler decodes must still be the whole request.
	var req struct {
		Message string   `json:"message"`
		Images  []string `json:"images"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Fatalf("restored body no longer decodes: %v", err)
	}
	if len(req.Images) != 1 || len(req.Images[0]) != 4<<20 {
		t.Fatalf("the attachment did not survive the peek: %d image(s)", len(req.Images))
	}
	if req.Message != "What is this image?" {
		t.Errorf("message = %q", req.Message)
	}
}

// Past the cap it must REFUSE, not hand back a short read — a truncated body
// restored is worse than no body at all, because the failure surfaces as a
// decode error somewhere else entirely.
func TestBodyPeekRefusesRatherThanTruncates(t *testing.T) {
	huge := bytes.Repeat([]byte("x"), (64<<20)+2048)
	r := httptest.NewRequest(http.MethodPost, "/api/send", bytes.NewReader(huge))
	raw, err := readAndRestoreBody(r)
	if err != ErrBodyTooLarge {
		t.Fatalf("err = %v (%d bytes returned), want ErrBodyTooLarge", err, len(raw))
	}
}

// Guard the shape of the failure this fixes: a peek that truncates produces
// JSON that no longer parses, which is exactly how the attachment vanished.
func TestTruncatedBodyIsUnparseable(t *testing.T) {
	body := sendBodyWithImage(2 << 20)
	cut := body[:1<<20] // what the old 1 MiB cap restored
	var req struct{ Message string }
	if err := json.Unmarshal(cut, &req); err == nil {
		t.Fatal("a truncated send body parsed cleanly; this test no longer guards anything")
	} else if !strings.Contains(fmt.Sprint(err), "unexpected end") {
		t.Logf("truncated body failed to parse as expected: %v", err)
	}
}
