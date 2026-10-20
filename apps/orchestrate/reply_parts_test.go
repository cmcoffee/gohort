package orchestrate

// A person in a group chat asked for five separate messages. The agent had no
// way to send them: a channel turn sends one message, and a second send to the
// same recipient is held. send_message's parts are that way, and each part goes
// out as its own message.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestPartsAskedForGoOutAsSeparateMessages(t *testing.T) {
	df := newDispatchFixture(t,
		// The person who spoke, not the conversation: refused, and pointed at it.
		FakeTurn{ToolCalls: []ToolCall{{ID: "c0", Name: "send_message", Args: map[string]any{"to": "Alice", "text": "one"}}}},
		FakeTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "send_message", Args: map[string]any{"to": "chat-1", "text": "one", "parts": []any{"two", " ", "three"}}}}},
		FakeTurn{Content: "Sent all three.", Repeat: true})
	prevRoot := RootDB
	RootDB = df.app.DB
	t.Cleanup(func() { RootDB = prevRoot })
	prevThreads, had := ActiveChannelThreads()
	RegisterChannelThreads(strangerThreads{})
	t.Cleanup(func() {
		if had {
			RegisterChannelThreads(prevThreads)
		} else {
			RegisterChannelThreads(nil)
		}
	})
	SaveChannel(RootDB, Channel{ID: "c1", Owner: "u", Service: "sms", AgentID: df.agent.ID})
	registerChannelAgentRunner(df.app)
	t.Cleanup(func() { RegisterChannelAgentRunner(nil) })

	reply, err := RunChannelAgent(t.Context(), ChannelInbound{Owner: "u", AgentID: df.agent.ID, SessionID: "room-1",
		ChatID: "chat-1", SenderName: "Alice", Text: "no, 3 different messages"})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Text != "one" || strings.Join(reply.More, "|") != "two|three" {
		t.Errorf("want three messages one|two|three, got %q + %q", reply.Text, reply.More)
	}
	// The thread keeps them as the conversation got them: three messages, the
	// turn's tool calls on the first.
	s := df.thread("room-1")
	n := len(s.Messages)
	if n < 4 {
		t.Fatalf("thread too short: %+v", s.Messages)
	}
	last := s.Messages[n-3:]
	for i, want := range []string{"one", "two", "three"} {
		if last[i].Role != "assistant" || last[i].Content != want {
			t.Errorf("stored message %d = %s %q, want assistant %q", i, last[i].Role, last[i].Content, want)
		}
	}
	if len(last[0].ToolCalls) == 0 || len(last[1].ToolCalls)+len(last[2].ToolCalls) != 0 {
		t.Errorf("the tool calls should ride the first message only: %d %d %d", len(last[0].ToolCalls), len(last[1].ToolCalls), len(last[2].ToolCalls))
	}
	if s.Messages[n-4].Role != "user" {
		t.Errorf("the three should follow the message they answer: %+v", s.Messages[n-4])
	}
	var hint, told bool
	for i := 0; i < df.llm.Calls(); i++ {
		for _, m := range df.llm.Sent(i) {
			for _, r := range m.ToolResults {
				hint = hint || (r.ID == "c0" && strings.Contains(r.Content, `to="chat-1"`))
				told = told || (r.ID == "c1" && strings.Contains(r.Content, "3 separate messages"))
			}
		}
	}
	if !hint {
		t.Error("naming the speaker should point at the conversation being answered")
	}
	if !told {
		t.Error("the model was not told its parts go out as separate messages")
	}
}
