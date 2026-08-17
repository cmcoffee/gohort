package orchestrate

import (
	"strings"
	"testing"
)

// A group room stores who typed each message in ChatMessage.Sender. The web-chat
// history builder used to drop that field, so every participant in the room
// arrived at the model as one anonymous "user" — and a model answering the owner
// reads an anonymous user turn as the owner's own. That is how somebody else's
// sentence came back quoted to the owner as something they had said.
func TestToLLMMessagesKeepsGroupSpeakers(t *testing.T) {
	msgs := toLLMMessages([]ChatMessage{
		{Role: "user", Content: "I'll bring the wine", Sender: "Craig Coffee"},
		{Role: "user", Content: "I'm making pasta", Sender: "Dana"},
		{Role: "assistant", Content: "Sounds good", Sender: "Wiwee"},
	})
	if len(msgs) != 3 {
		t.Fatalf("want 3 messages, got %d", len(msgs))
	}
	if !strings.HasPrefix(msgs[0].Content, "Craig Coffee: ") {
		t.Errorf("owner turn lost its speaker: %q", msgs[0].Content)
	}
	if !strings.HasPrefix(msgs[1].Content, "Dana: ") {
		t.Errorf("participant turn lost its speaker: %q", msgs[1].Content)
	}
	// Assistant turns are the agent's own voice; naming them would put a second
	// speaker label on a message that already has one.
	if strings.HasPrefix(msgs[2].Content, "Wiwee: ") {
		t.Errorf("assistant turn should not be prefixed: %q", msgs[2].Content)
	}
}

// The mirror image, on the other builder: an observation card is an
// assistant-role message whose body is something that HAPPENED — often another
// person's message. The dispatch builder used to render it bare, so the agent
// read a participant's words as its own past statements.
func TestLLMHistoryContentMarksReportCards(t *testing.T) {
	got := llmHistoryContent(ChatMessage{
		Role:       "assistant",
		ReportFrom: "Dana · iPhone (iMessage)",
		Content:    "I'm making pasta",
	})
	if !strings.Contains(got, "Dana · iPhone (iMessage)") {
		t.Errorf("report card lost its origin: %q", got)
	}
	if !strings.HasPrefix(got, "<gohort-meta>") {
		t.Errorf("origin marker must be fenced so an echo is scrubbed: %q", got)
	}
	if !strings.HasSuffix(got, "I'm making pasta") {
		t.Errorf("card body was rewritten: %q", got)
	}
}

// Plain web sessions carry neither field and must stay exactly as they were —
// anonymous you/assistant bubbles, no prefix, no marker.
func TestLLMHistoryContentLeavesPlainSessionsAlone(t *testing.T) {
	for _, role := range []string{"user", "assistant"} {
		if got := llmHistoryContent(ChatMessage{Role: role, Content: "hello"}); got != "hello" {
			t.Errorf("%s turn was rewritten: %q", role, got)
		}
	}
}

// Both builders render history through the same function. A builder that grows
// its own copy of this logic is how the two fields came to be applied one each.
func TestBothHistoryBuildersRenderIdentically(t *testing.T) {
	stored := []ChatMessage{
		{Role: "user", Content: "I'm making pasta", Sender: "Dana"},
		{Role: "assistant", ReportFrom: "Dana · iPhone (iMessage)", Content: "I'm making pasta"},
	}
	web := toLLMMessages(stored)
	for i, m := range stored {
		if web[i].Content != llmHistoryContent(m) {
			t.Errorf("builders disagree on message %d:\n web: %q\ndisp: %q", i, web[i].Content, llmHistoryContent(m))
		}
	}
}
