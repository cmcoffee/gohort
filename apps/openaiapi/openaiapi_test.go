package openaiapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTextContentShapes: clients send message content either as a plain string
// or as an array of typed parts. Mishandling the array form yields an empty
// prompt and a confident answer to nothing.
func TestTextContentShapes(t *testing.T) {
	if got := textContent("hello"); got != "hello" {
		t.Errorf("string form: %q", got)
	}
	var parts any
	_ = json.Unmarshal([]byte(`[{"type":"text","text":"who is "},{"type":"text","text":"on call?"}]`), &parts)
	if got := textContent(parts); got != "who is on call?" {
		t.Errorf("array form: %q", got)
	}
	// Non-text parts are skipped, not stringified into the prompt.
	var mixed any
	_ = json.Unmarshal([]byte(`[{"type":"image_url","image_url":{"url":"x"}},{"type":"text","text":"hi"}]`), &mixed)
	if got := textContent(mixed); got != "hi" {
		t.Errorf("mixed form: %q", got)
	}
	if got := textContent(nil); got != "" {
		t.Errorf("nil: %q", got)
	}
	if got := textContent(42); got != "" {
		t.Errorf("unexpected type should be empty, got %q", got)
	}
}

// TestSessionKeyPrecedence: without a stable conversation key every request
// starts a fresh thread and the agent forgets what it just said mid-call.
func TestSessionKeyPrecedence(t *testing.T) {
	req := chatReq{User: "u1"}
	req.Call.ID = "call-abc"

	r := newReq(map[string]string{"X-Session-Id": "hdr-1"})
	if got := sessionKey(r, req); got != "ext:hdr-1" {
		t.Errorf("header wins: %q", got)
	}
	r = newReq(nil)
	if got := sessionKey(r, req); got != "ext:call-abc" {
		t.Errorf("call id next: %q", got)
	}
	req.Call.ID = ""
	if got := sessionKey(r, req); got != "ext:u1" {
		t.Errorf("user field next: %q", got)
	}
	req.User = ""
	if got := sessionKey(r, req); got != "ext:default" {
		t.Errorf("fallback: %q", got)
	}
}

// TestSentencesSplitsForTTS: the agent path has no token stream, so the reply
// is chunked into speakable units — a consumer can start on sentence one.
func TestSentencesSplitsForTTS(t *testing.T) {
	got := sentences("Two things. First, the deploy is green! Second?")
	if len(got) != 3 {
		t.Fatalf("expected 3 chunks, got %d: %q", len(got), got)
	}
	if strings.Join(got, "") != "Two things. First, the deploy is green! Second?" {
		t.Errorf("chunks must reassemble to the original: %q", got)
	}
	// No terminator: one chunk, nothing lost.
	if got := sentences("no terminator here"); len(got) != 1 || got[0] != "no terminator here" {
		t.Errorf("unterminated: %q", got)
	}
	if got := sentences(""); len(got) != 1 || got[0] != "" {
		t.Errorf("empty input should yield the input, got %q", got)
	}
	// Trailing text after the last terminator still ships.
	got = sentences("Done. And one more thing")
	if len(got) != 2 || !strings.Contains(got[1], "one more thing") {
		t.Errorf("trailing fragment dropped: %q", got)
	}
}

// TestCompletionEnvelopeShape pins the non-streaming response to the shape an
// OpenAI client library parses.
func TestCompletionEnvelopeShape(t *testing.T) {
	env := completionEnvelope("worker", "hi there", nil)
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct{} `json:"usage"`
	}
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Object != "chat.completion" || len(back.Choices) != 1 {
		t.Fatalf("bad envelope: %s", b)
	}
	if back.Choices[0].Message.Role != "assistant" || back.Choices[0].Message.Content != "hi there" {
		t.Errorf("message: %+v", back.Choices[0].Message)
	}
	if back.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason: %q", back.Choices[0].FinishReason)
	}
	// usage is omitted when no Response carried token counts.
	if back.Usage != nil {
		t.Errorf("usage should be absent without a response")
	}
}

// newReq builds a bare request carrying the given headers.
func newReq(headers map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

// TestTurnInputPicksLastUserAndFirstSystem: a voice platform resends the whole
// transcript each turn. The LAST user message is this turn's input — taking the
// first would answer the opening line over and over.
func TestTurnInputPicksLastUserAndFirstSystem(t *testing.T) {
	var req chatReq
	body := `{"messages":[
		{"role":"system","content":"you are terse"},
		{"role":"user","content":"first thing"},
		{"role":"assistant","content":"ok"},
		{"role":"user","content":"second thing"},
		{"role":"system","content":"ignored second system"}
	]}`
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	input, system := turnInput(req)
	if input != "second thing" {
		t.Errorf("input = %q, want the LAST user message", input)
	}
	if system != "you are terse" {
		t.Errorf("system = %q, want the FIRST system message", system)
	}
}

// TestCallerNamePrecedence: the speaker is labeled in the thread, so a group
// transcript reads as who-said-what rather than attributing a call to the room.
func TestCallerNamePrecedence(t *testing.T) {
	req := chatReq{User: "+15551234567"}
	if got := callerName(newReq(map[string]string{"X-Caller-Name": "Alice"}), req); got != "Alice" {
		t.Errorf("header wins: %q", got)
	}
	if got := callerName(newReq(nil), req); got != "+15551234567" {
		t.Errorf("user field next: %q", got)
	}
	if got := callerName(newReq(nil), chatReq{}); got != "Voice caller" {
		t.Errorf("fallback should be honest, got %q", got)
	}
}

// TestIsTierName pins which model strings mean "raw model" rather than "resolve
// this to a conversation". Getting this wrong sends a caller who named a chat
// to the bare worker tier — which answers plausibly with no persona, tools,
// memory or thread, and is therefore the hardest failure to notice.
func TestIsTierName(t *testing.T) {
	for _, m := range []string{"worker", "lead", "WORKER", " Lead ", "gohort-worker", "default"} {
		if !isTierName(m) {
			t.Errorf("%q should name a tier", m)
		}
	}
	for _, m := range []string{"any;+;chat872212368359368118", "seed-chat", "Family", "", "gpt-4"} {
		if isTierName(m) {
			t.Errorf("%q should NOT name a tier", m)
		}
	}
}
