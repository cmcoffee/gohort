package orchestrate

// A message a channel turn addresses to the conversation it is answering is
// that turn's reply, sent once. It used to go out at once, and then the turn's
// own closing words followed it as a second message saying the same thing.

import (
	"os"
	"regexp"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestAMessageToThisConversationIsTheReply(t *testing.T) {
	const held = "Drain is in the group chat: 3:14 of dark industrial techno."
	df := newDispatchFixture(t,
		FakeTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "send_message", Args: map[string]any{"to": "Room", "text": held}}}},
		FakeTurn{Content: "Done. It is in the chat now.", Repeat: true})
	// The agent holds a channel on a transport whose conversations are fixed.
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

	// A channel turn answering chat-1, which the model replies to with send_message.
	res, err := df.app.RunAgentSyncContinuingRich(t.Context(), AgentSyncRun{
		AgentOwner: "u", AgentKey: df.agent.ID, Kind: "channel", SubSessionID: "room-1",
		Message: "can you attach it?", MessageSender: "Alice", ReplyAuthorizedKey: operatorRecipientKey("chat-1", ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != held {
		t.Errorf("the reply should be the message the turn addressed here, got %q", res.Text)
	}
	s := df.thread("room-1")
	if n := len(s.Messages); n == 0 || s.Messages[n-1].Content != held {
		t.Errorf("the thread should record what the conversation received: %+v", s.Messages)
	}
	if diagsOf(df.app.DB, "u", df.agent.ID, "room-1", "in-thread-reply") != 1 {
		t.Error("the closing text that was not sent left no trace")
	}
	sent := false
	for i := 0; i < df.llm.Calls(); i++ {
		for _, m := range df.llm.Sent(i) {
			for _, r := range m.ToolResults {
				if strings.Contains(r.Content, "That is your reply to") {
					sent = true
				}
			}
		}
	}
	if !sent {
		t.Error("the model was not told its message is the reply")
	}
}

// Both doors a turn can write to its own conversation through make it the
// reply rather than sending it beside the reply.
func TestEveryInThreadSendIsTheReply(t *testing.T) {
	var src strings.Builder
	for _, f := range []string{"channel_tools.go", "operator_contact_tools.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src.Write(b)
	}
	re := regexp.MustCompile(`if isReplyToActiveInbound\(sess, recip\) \{\s*return replyInThread\(`)
	if n := len(re.FindAllString(src.String(), -1)); n != 2 {
		t.Errorf("%d in-thread send(s) go through replyInThread, want send_message's and message_contact's", n)
	}
	if strings.Contains(src.String(), "(replying in-thread)") {
		t.Error("an in-thread send still goes out at once")
	}
}
