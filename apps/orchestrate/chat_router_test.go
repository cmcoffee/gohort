package orchestrate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// The body peek restores what it reads, so anything it truncates is corrupt
// JSON handed to the real handler. At 1 MiB that fired on any chat send
// carrying a photo — base64 is ~1.37x the file — and the attachment was simply
// not in the request the handler decoded. The model then answered, truthfully,
// that no image was attached.

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

// An edit cuts the thread at the message it edits, named by its text as well
// as its position: a position the page counted misses what reached the thread
// without a bubble (a background result's turn and the picture it delivered),
// and the cut there took them with it. A position that names another message
// is corrected to the latest with that text, and a text the thread does not
// have is refused, rather than cut anywhere.
func TestAnEditCutsAtTheMessageItEdits(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "user", Content: "make a frame"},
		{Role: "assistant", Content: "on it"},
		{Role: "user", Content: "[BACKGROUND TASK FINISHED] the frame", Hidden: true},
		{Role: "assistant", Content: "here is the frame"},
		{Role: "user", Content: "make it into a video"},
	}
	str := func(s string) *string { return &s }
	// The page counted 2 visible messages before the edited one: the
	// background turn was not among them.
	if at, ok := cutPoint(msgs, 2, str("make it into a video ")); !ok || at != 4 {
		t.Errorf("an edit whose page position missed the background turn cuts at %d (%v), want 4", at, ok)
	}
	if at, ok := cutPoint(msgs, 4, str("make it into a video")); !ok || at != 4 {
		t.Errorf("a right position stays: %d %v", at, ok)
	}
	if at, ok := cutPoint(msgs, 3, str("not something anyone sent")); ok {
		t.Errorf("a message the thread does not have is cut at %d, want refused", at)
	}
	if at, ok := cutPoint(msgs, 2, nil); !ok || at != 2 {
		t.Errorf("a cut with no text is where it says: %d %v", at, ok)
	}
	// Assistant text matching is not the user message being edited.
	if at, ok := cutPoint(msgs, 0, str("here is the frame")); ok {
		t.Errorf("an assistant message is not an edit point: %d", at)
	}
}

// Both of the chat's cuts (Edit, and Delete-from-here) send the message's text.
func TestThePageSendsTheTextOfWhatItCuts(t *testing.T) {
	raw, err := os.ReadFile("../../core/ui/assets/runtime/30_agent_loop_panel.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	for _, want := range []string{
		"return truncateSession(at, was).then(function() {",
		"truncateSession(at, userBubbleText(bubble)).then(function() {",
		"if (typeof text === 'string') cut.text = text;",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the page's cut does not carry the message's text: no %q", want)
		}
	}
}

// An app's own agent is part of its app: gohort's own chat does not open it by
// id on any route that names one (a send, its sessions, its threads, a
// cancel), and answers as it does for an agent that does not exist. The
// person's own agent beside it opens as before. The app's page talks to it on
// the app's own routes, which bind it.
func TestGohortsChatDoesNotOpenAnAppsOwnAgent(t *testing.T) {
	df := newDispatchFixture(t)
	prevRoot := RootDB
	RootDB = df.app.DB
	t.Cleanup(func() { RootDB = prevRoot })
	part, err := saveAgent(df.udb, AgentRecord{Name: "Librarian", Owner: "u", OwningApp: "reading-list", OrchestratorPrompt: "You suggest books.", appEdit: "Builder"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, a := range []AgentRecord{df.agent, part} {
		df.udb.Set(sessionTable(a.ID), "s1", ChatSession{ID: "s1", AgentID: a.ID, LastAt: now})
		df.udb.Set(sessionTable(a.ID), MCPThreadPrefix+":k1", ChatSession{ID: MCPThreadPrefix + ":k1", AgentID: a.ID, LastAt: now})
	}
	for _, c := range []struct {
		a     AgentRecord
		opens bool
	}{{df.agent, true}, {part, false}} {
		q := "?agent_id=" + c.a.ID
		call := &jsonapi.Call{R: httptest.NewRequest("GET", "/api/sessions"+q, nil), User: "u", UDB: df.udb}
		if out, _ := df.app.handleSessionList(call); (len(out.([]sessionListItem)) == 1) != c.opens {
			t.Errorf("%s: its session list %v, opens %v", c.a.Name, out, c.opens)
		}
		call.R = httptest.NewRequest("GET", "/api/threads"+q, nil)
		if out, _ := df.app.handleThreadList(call); (len(out.([]threadRow)) == 1) != c.opens {
			t.Errorf("%s: its threads %v, opens %v", c.a.Name, out, c.opens)
		}
		call.R = httptest.NewRequest("GET", "/api/sessions/s1"+q, nil)
		if _, err := df.app.sessionAgent(call); (err == nil) != c.opens {
			t.Errorf("%s: its session: %v, opens %v", c.a.Name, err, c.opens)
		}
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/send", strings.NewReader(`{"agent_id":"`+c.a.ID+`","message":"hi"}`))
		if _, ok := df.app.resolveAgent(w, r, df.udb, "u"); ok != c.opens || (!ok && w.Code != http.StatusNotFound) {
			t.Errorf("%s: a send reached it %v (code %d), opens %v", c.a.Name, ok, w.Code, c.opens)
		}
		call.R = httptest.NewRequest("POST", "/api/cancel"+q, nil)
		if _, err := df.app.cancelOwnTurn(call, &cancelTarget{}); (err == errNotFound) == c.opens {
			t.Errorf("%s: a cancel: %v, opens %v", c.a.Name, err, c.opens)
		}
	}
}
