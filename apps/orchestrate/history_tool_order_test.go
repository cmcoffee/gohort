package orchestrate

import "testing"

// A stored reply that used tools replays in the order it happened: the calls,
// their results, then the reply written after them. The reply used to ride
// the call message as if it came first, so the turn ended on tool results, a
// turn the model had not answered; the next user message was merged into the
// results on Gemini, and the model finished that message in the user's voice.
func TestAReplyReplaysAfterItsToolResults(t *testing.T) {
	hist := toLLMMessages([]ChatMessage{
		{Role: "user", Content: "rename the machine"},
		{Role: "assistant", Content: "Renamed it.", ToolCalls: []PersistedToolCall{{Name: "machine", Args: map[string]any{"action": "update"}, Result: "Updated"}}},
		{Role: "user", Content: "can you make sure that pipeline works correctly"},
	})
	if len(hist) != 5 {
		t.Fatalf("want user, calls, results, reply, user; got %d: %+v", len(hist), hist)
	}
	if hist[1].Role != "assistant" || len(hist[1].ToolCalls) != 1 || hist[1].Content != "" {
		t.Errorf("the calls come first, without the reply: %+v", hist[1])
	}
	if len(hist[2].ToolResults) != 1 {
		t.Errorf("then their results: %+v", hist[2])
	}
	if hist[3].Role != "assistant" || hist[3].Content != "Renamed it." {
		t.Errorf("then the reply written after them: %+v", hist[3])
	}
	if hist[4].Role != "user" || hist[4].Content != "can you make sure that pipeline works correctly" {
		t.Errorf("and the next message stands on its own: %+v", hist[4])
	}
	// A turn that ended on its calls with no text adds nothing after them.
	bare := toLLMMessages([]ChatMessage{{Role: "assistant", ToolCalls: []PersistedToolCall{{Name: "web_search", Result: "hits"}}}})
	if len(bare) != 2 {
		t.Errorf("no reply, no trailing message: %+v", bare)
	}
}
