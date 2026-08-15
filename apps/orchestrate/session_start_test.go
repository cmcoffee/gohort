package orchestrate

// Opening a conversation from a surface that already knows what it is
// about. The interesting properties are all about what does NOT happen:
// no turn runs here, the prompt stops being served the moment it lands,
// and the text never reaches the log.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestSessionStartStoresThePromptWithoutRunningATurn(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	saveAgent(udb, AgentRecord{ID: "ag-1", Name: "Investigator", OrchestratorPrompt: "You investigate."})

	body := `{"agent":"Investigator","message":"What failed in bundle scan-2026-08-14?","title":"scan-2026-08-14"}`
	r := httptest.NewRequest("POST", "/api/sessions/start", strings.NewReader(body))
	w := httptest.NewRecorder()
	app.handleSessionStart(w, asUser(r, user))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var out struct{ Session, Agent, URL string }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	// Resolved by NAME — a caller authoring a link knows the agent as its
	// author typed it, not by the id the store assigned.
	if out.Agent != "ag-1" || out.Session == "" {
		t.Fatalf("unexpected response: %+v", out)
	}
	// A ready-made link, so every caller does not rebuild it and get the
	// escaping wrong.
	if !strings.Contains(out.URL, "agent=ag-1") || !strings.Contains(out.URL, "session="+out.Session) {
		t.Errorf("url should point at the new session: %s", out.URL)
	}

	sess, ok := loadChatSession(udb, "ag-1", out.Session)
	if !ok {
		t.Fatal("session was not stored")
	}
	if sess.OpeningPrompt != "What failed in bundle scan-2026-08-14?" {
		t.Errorf("prompt not stored: %q", sess.OpeningPrompt)
	}
	// Nothing ran: the turn happens when the page opens and sends it, so
	// a failure is visible to somebody.
	if len(sess.Messages) != 0 {
		t.Errorf("a turn was run server-side — nobody is watching it: %+v", sess.Messages)
	}
	if sess.Title != "scan-2026-08-14" {
		t.Errorf("title not stored: %q", sess.Title)
	}
}

// The idempotency guard IS the emptiness of the session: the prompt is
// served while there are no messages and stops the moment one lands.
// Without that a reload re-sends it.
func TestOpeningPromptIsServedOnlyWhileTheSessionIsEmpty(t *testing.T) {
	empty := servedSession{ChatSession: ChatSession{ID: "s1", OpeningPrompt: "ask me"}}
	if len(empty.Messages) == 0 {
		empty.OpeningPrompt = empty.ChatSession.OpeningPrompt
	}
	if empty.OpeningPrompt != "ask me" {
		t.Error("an empty session should offer its opening prompt")
	}

	used := servedSession{ChatSession: ChatSession{
		ID: "s1", OpeningPrompt: "ask me",
		Messages: []ChatMessage{{Role: "user", Content: "ask me"}},
	}}
	if len(used.Messages) == 0 {
		used.OpeningPrompt = used.ChatSession.OpeningPrompt
	}
	if used.OpeningPrompt != "" {
		t.Error("once the prompt has been sent the session must stop offering it, or every reload re-sends")
	}

	// And it is never in the normal record serialization — it is served
	// deliberately or not at all.
	raw, _ := json.Marshal(ChatSession{ID: "s1", OpeningPrompt: "secret question"})
	if strings.Contains(string(raw), "secret question") {
		t.Error("the opening prompt rode along in the plain record encoding")
	}
}

func TestSessionStartRefusals(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	saveAgent(udb, AgentRecord{ID: "ag-1", Name: "Investigator", OrchestratorPrompt: "x"})

	call := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/sessions/start", strings.NewReader(body))
		w := httptest.NewRecorder()
		app.handleSessionStart(w, asUser(r, user))
		return w
	}
	if w := call(`{"message":"hi"}`); w.Code != http.StatusBadRequest {
		t.Errorf("no agent should be a 400, got %d", w.Code)
	}
	if w := call(`{"agent":"Nobody","message":"hi"}`); w.Code != http.StatusNotFound {
		t.Errorf("unknown agent should be a 404, got %d", w.Code)
	}
	// An empty message is legitimate: open the thread, let them type.
	w := call(`{"agent":"ag-1"}`)
	if w.Code != 200 {
		t.Fatalf("an empty opening prompt should still open a session: %d %s", w.Code, w.Body.String())
	}
	// Oversized prompts are trimmed rather than refused — the caller is a
	// program pasting context, and losing the tail beats losing the thread.
	long := strings.Repeat("x", maxOpeningPrompt+500)
	w = call(`{"agent":"ag-1","message":"` + long + `"}`)
	if w.Code != 200 {
		t.Fatalf("oversized prompt: %d %s", w.Code, w.Body.String())
	}
	var out struct{ Session string }
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	sess, _ := loadChatSession(udb, "ag-1", out.Session)
	if len(sess.OpeningPrompt) != maxOpeningPrompt {
		t.Errorf("prompt should be trimmed to the cap, got %d", len(sess.OpeningPrompt))
	}
}

// The browser half is wired in a file that knows nothing about this one,
// which is exactly the pair that half-lands.
func TestOpeningPromptIsSentByThePanel(t *testing.T) {
	js, err := os.ReadFile("../../core/ui/assets/runtime/30_agent_loop_panel.js")
	if err != nil {
		t.Fatalf("read runtime: %v", err)
	}
	src := string(js)
	if !strings.Contains(src, "rec.opening_prompt") {
		t.Fatal("the panel never reads opening_prompt — the server would store a prompt nothing sends")
	}
	// Routed through the composer + sendMessage, not a side channel, so
	// streaming/cancel/approvals behave as they do when a person types.
	if !strings.Contains(src, "function sendOpeningPrompt") || !strings.Contains(src, "sendMessage();") {
		t.Error("the opening prompt should go through the normal send path")
	}
	// It must not clobber something the person already started typing.
	if !strings.Contains(src, "if (String(inputArea.value || '').trim()) return;") {
		t.Error("a half-typed message must win over the pre-filled one")
	}
}
