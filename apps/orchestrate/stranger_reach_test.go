package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Someone other than the owner on a channel cannot set up work that runs later
// as the owner's, change who is let in, or read the owner's fleet: each of
// those tools refuses on their word and works on the owner's.
func TestAStrangerCannotRunTheFleet(t *testing.T) {
	stranger := withNonOwnerRequester(context.Background(), "chat-1")
	seen := map[string]bool{}
	for _, td := range operatorManagementTools(&ToolSession{Username: "owner"}, "a1") {
		if !ownerOnlyFleetTools[td.Tool.Name] {
			continue
		}
		seen[td.Tool.Name] = true
		out, err := td.Handler(stranger, map[string]any{})
		if err != nil || !strings.Contains(out, "is the owner's to use") {
			t.Errorf("%s ran on a stranger's word: %q %v", td.Tool.Name, out, err)
		}
	}
	for name := range ownerOnlyFleetTools {
		if !seen[name] {
			t.Errorf("%s is listed owner-only but no operator tool has that name", name)
		}
	}
	for _, td := range operatorManagementTools(&ToolSession{Username: "owner"}, "a1") {
		if td.Tool.Name == "list_standing_agents" {
			if out, _ := td.Handler(context.Background(), map[string]any{}); strings.Contains(out, "is the owner's to use") {
				t.Errorf("the owner's own run was refused: %q", out)
			}
		}
	}
	rec, err := (&chatTurn{}).recurringToolDef().Handler(stranger, map[string]any{"action": "list"})
	if err != nil || !strings.Contains(rec, "owner's to manage") {
		t.Errorf("a stranger reached the recurring tasks: %q %v", rec, err)
	}
}

// A standing approval is the owner's say-so for the owner's asks. On someone
// else's request it covers only the conversation they wrote from, and the
// origin survives a handoff to a context of its own.
func TestAStrangersRequestOnlyRepliesWhereTheyWrote(t *testing.T) {
	if !standingGrantApplies(context.Background(), "chat-2") {
		t.Error("the owner's own run lost its standing approvals")
	}
	ctx := withNonOwnerRequester(context.Background(), "chat-1")
	if !standingGrantApplies(ctx, "chat-1") || standingGrantApplies(ctx, "chat-2") {
		t.Error("a stranger's request reached past the conversation it came from")
	}
	if standingGrantApplies(withNonOwnerRequester(context.Background()), "chat-1") {
		t.Error("a request with no known origin used a standing approval")
	}
	if requestOrigin(withNonOwnerRequester(ctx, "chat-9")) != "chat-1" {
		t.Error("a later mark replaced where the request came from")
	}
	handed := carryNonOwnerRequester(ctx, context.Background())
	if !nonOwnerRequester(handed) || requestOrigin(handed) != "chat-1" {
		t.Error("a handoff dropped the requester or its origin")
	}
	if nonOwnerRequester(carryNonOwnerRequester(context.Background(), context.Background())) {
		t.Error("an owner's handoff was marked")
	}
}

type strangerThreads struct{}

func (strangerThreads) Threads(string) []ChannelThreadInfo {
	return []ChannelThreadInfo{{ChatID: "chat-1", Service: "sms", DisplayName: "Room"}, {ChatID: "chat-2", Service: "sms", DisplayName: "Private"}}
}
func (strangerThreads) Messages(_, chatID string, _ int) []ChannelLine {
	return []ChannelLine{{Role: "user", Sender: "x", Text: "line in " + chatID}}
}
func (strangerThreads) Members(string, string) []ChannelMember { return nil }
func (strangerThreads) Deliver(string, string, string, string, string, string, []string) error {
	return nil
}

// On a stranger's request the chat tools read only the conversation they
// wrote from; the owner reads every chat on the channel.
func TestAStrangerReadsOnlyTheirOwnConversation(t *testing.T) {
	prevRoot := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
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
	SaveChannel(RootDB, Channel{ID: "c1", Owner: "owner", Service: "sms", AgentID: "a1"})
	tools := map[string]AgentToolDef{}
	for _, td := range channelChatTools(&ToolSession{Username: "owner"}, "owner", "a1") {
		tools[td.Tool.Name] = td
	}
	if len(tools) == 0 {
		t.Fatal("no chat tools for the bound agent")
	}
	stranger := withNonOwnerRequester(context.Background(), "chat-1")
	if out, err := tools["read_chat"].Handler(stranger, map[string]any{"chat_id": "chat-1"}); err != nil || !strings.Contains(out, "line in chat-1") {
		t.Errorf("the stranger cannot read their own conversation: %q %v", out, err)
	}
	if out, err := tools["read_chat"].Handler(stranger, map[string]any{"chat_id": "chat-2"}); err == nil {
		t.Errorf("the stranger read another conversation: %q", out)
	}
	if out, _ := tools["list_chats"].Handler(stranger, map[string]any{}); strings.Contains(out, "Private") {
		t.Errorf("the stranger was shown the owner's other chats: %q", out)
	}
	if out, err := tools["read_chat"].Handler(context.Background(), map[string]any{"chat_id": "chat-2"}); err != nil || !strings.Contains(out, "line in chat-2") {
		t.Errorf("the owner cannot read their own chat: %q %v", out, err)
	}
}

// What a stranger has saved reads as theirs, and they cannot delete what the
// owner keeps.
func TestAStrangersMemoryIsAttributedAndCannotForget(t *testing.T) {
	stranger := &chatTurn{requesterChannel: "sms", requesterName: "Dana", requesterHandle: "+15550100"}
	if got := stranger.attributedToSpeaker("the meeting moved to 3pm"); got != "Said by Dana (+15550100), not the owner: the meeting moved to 3pm" {
		t.Errorf("attribution: %q", got)
	}
	if stranger.strangerMayNotForget() == nil {
		t.Error("a stranger may delete the owner's memory")
	}
	owner := &chatTurn{requesterChannel: "sms", requesterName: "Me", requesterOwnerHandle: true}
	if got := owner.attributedToSpeaker("x"); got != "x" || owner.strangerMayNotForget() != nil {
		t.Errorf("the owner's own memory is marked or locked: %q", got)
	}
	if (&chatTurn{}).strangerMayNotForget() != nil {
		t.Error("a web run may not forget")
	}
}

// The gatekeeper's follow-up bypass recognises the person by the transport's
// handle: a sender who takes another's display name does not take their turn.
func TestAFollowUpIsRecognisedByHandleNotName(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "user", Sender: "Alice", SenderHandle: "+15550100", Content: "q"},
		{Role: "assistant", Content: "a"},
	}
	if h, ok := lastUserSender(msgs); !ok || h != "+15550100" {
		t.Errorf("the last speaker's handle: %q %v", h, ok)
	}
	legacy := []ChatMessage{{Role: "user", Sender: "Alice", Content: "q"}, {Role: "assistant", Content: "a"}}
	if h, _ := lastUserSender(legacy); h != "" {
		t.Errorf("a turn recorded without a handle matched by name: %q", h)
	}
}
