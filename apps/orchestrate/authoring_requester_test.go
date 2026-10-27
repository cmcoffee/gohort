package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// ownerHandleLink is a messaging bridge that knows one handle as the owner's.
// Only IsOwnerHandle is answered; anything else the embedded nil interface
// would panic on, which is the point: this check must not reach further.
type ownerHandleLink struct {
	MessagingLink
	owner, handle string
}

func (l ownerHandleLink) IsOwnerHandle(owner, handle string) bool {
	return owner == l.owner && handle == l.handle
}

func withOwnerHandleLink(t *testing.T, owner, handle string) {
	prev, _ := ActiveMessagingLink()
	RegisterMessagingLink(ownerHandleLink{owner: owner, handle: handle})
	t.Cleanup(func() { RegisterMessagingLink(prev) })
}

func TestChannelSenderIsOwnerOnlyByTheTransportHandle(t *testing.T) {
	withOwnerHandleLink(t, "owner", "+15550100")
	cases := []struct {
		name string
		run  AgentSyncRun
		want bool
	}{
		{"a scheduled fire names no sender", AgentSyncRun{}, true},
		{"the owner's own phone, as the bridge saw it", AgentSyncRun{Kind: "channel", SenderHandle: "+15550100", SenderIsOwner: true}, true},
		{"a contact in the group", AgentSyncRun{Kind: "channel", SenderHandle: "+15550199"}, false},
		{"a contact calling themselves the owner", AgentSyncRun{Kind: "channel", SenderHandle: "+15550199", MessageSender: "owner"}, false},
		{"a channel message with no handle", AgentSyncRun{Kind: "channel"}, false},
		{"the owner's handle, but the bridge did not say so", AgentSyncRun{Kind: "channel", SenderHandle: "+15550100"}, false},
	}
	for _, c := range cases {
		if got := channelSenderIsOwner("owner", c.run); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// The owner's own iMessage arrives with the handle cleared. The bridge, which
// knows the service, says that is the owner (SenderIsOwner); nothing reads
// ownership off an empty handle, which on any other service is a sender
// nobody could name.
func TestTheOwnersOwnMessageWithAClearedHandleIsTheOwner(t *testing.T) {
	if !channelSenderIsOwner("owner", AgentSyncRun{Kind: "channel", SenderHandle: "", SenderIsOwner: true}) {
		t.Error("the bridge's verdict that the owner sent it is the answer")
	}
	if channelSenderIsOwner("owner", AgentSyncRun{Kind: "channel", SenderHandle: ""}) {
		t.Error("an empty handle the bridge did not vouch for is not the owner")
	}
}

func TestAChannelHandleIsNotTheOwnerWithNoBridgeToAsk(t *testing.T) {
	prev, _ := ActiveMessagingLink()
	RegisterMessagingLink(nil)
	t.Cleanup(func() { RegisterMessagingLink(prev) })
	if channelSenderIsOwner("owner", AgentSyncRun{Kind: "channel", SenderHandle: "+15550100"}) {
		t.Error("with no bridge verdict, a handle cannot be shown to be the owner's")
	}
	if channelSenderIsOwner("owner", AgentSyncRun{Kind: "channel"}) {
		t.Error("with no bridge, an empty handle cannot be shown to be the owner's either")
	}
}

// A channel run executes as the owner's account, so the guardrails' owner test
// reads the bridge's verdict on the sender, not the account.
func TestAChannelSenderIsNotTheOwnerBecauseTheRunIs(t *testing.T) {
	stranger := &chatTurn{user: "owner", ownerUser: "owner", requesterChannel: "channel"}
	if stranger.requester().Owner {
		t.Error("a stranger on a channel was told to the guardrails as the owner")
	}
	owner := &chatTurn{user: "owner", ownerUser: "owner", requesterChannel: "channel", requesterOwnerHandle: true}
	if !owner.requester().Owner {
		t.Error("the owner on their own channel is the owner")
	}
	if !(&chatTurn{user: "owner", ownerUser: "owner"}).requester().Owner {
		t.Error("the owner's own web or scheduled run is the owner")
	}
}

func TestDispatchedAuthoringAnswersToTheOwnerOnly(t *testing.T) {
	author := AgentRecord{ID: "seed-builder", Name: "Builder", Owner: "owner"}
	plain := AgentRecord{ID: "a2", Name: "Plain", Owner: "owner"}
	bg := context.Background()
	marked := withNonOwnerRequester(bg)

	if grant, why := dispatchAuthoring(bg, author, "owner", "owner"); !grant || why != "" {
		t.Errorf("the owner's own run: got (%v, %q), want the grant", grant, why)
	}
	if grant, why := dispatchAuthoring(bg, plain, "owner", "owner"); grant || why != "" {
		t.Errorf("an agent that cannot author: got (%v, %q), want nothing and no reason", grant, why)
	}
	if grant, why := dispatchAuthoring(marked, author, "owner", "owner"); grant || !strings.Contains(why, "other than the owner") {
		t.Errorf("a run a contact started: got (%v, %q), want it withheld", grant, why)
	}
	if grant, why := dispatchAuthoring(bg, author, "owner", "someone"); grant || !strings.Contains(why, "someone") {
		t.Errorf("a run for another account: got (%v, %q), want it withheld", grant, why)
	}
	// The record's own owner wins over the store it came from.
	if grant, _ := dispatchAuthoring(bg, author, "someone", "someone"); grant {
		t.Error("a run for an account that does not own the agent was granted authoring because the caller named it as the store")
	}
	seed := AgentRecord{ID: "seed-builder", Name: "Builder", Owner: seedOwner}
	if grant, _ := dispatchAuthoring(bg, seed, seedOwner, "owner"); !grant {
		t.Error("the Builder seed belongs to everyone, as on the direct path")
	}
}

// The mark is only as good as its reach: a delegation derives its context from
// the run that started it, and a handoff that outlives the turn drops the
// cancel but must not drop this.
func TestTheNonOwnerMarkSurvivesDerivedContexts(t *testing.T) {
	ctx := withNonOwnerRequester(context.Background())
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	if !nonOwnerRequester(child) || !nonOwnerRequester(context.WithoutCancel(ctx)) {
		t.Error("the non-owner mark was lost on a derived context")
	}
	if nonOwnerRequester(context.Background()) || nonOwnerRequester(nil) {
		t.Error("an unmarked context read as marked")
	}
}

// Every place that appends the authoring catalog asks the one check. The
// dispatch paths each grew their own `if agentCanAuthor(target)`, which is how
// none of them asked who the run was for; a fourth written the same way would
// reopen it with every test above still green.
func TestEveryAuthoringGrantAsksWhoTheRunIsFor(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Skip("package sources unavailable")
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		lines := strings.Split(string(data), "\n")
		for i, l := range lines {
			if !strings.Contains(l, "builderAuthoringTools(") || strings.Contains(l, "func builderAuthoringTools") ||
				strings.HasPrefix(strings.TrimSpace(l), "//") {
				continue
			}
			// The grant must sit under a condition that came from the check:
			// look back to the nearest if.
			gated := false
			for j := i - 1; j >= 0 && j >= i-4; j-- {
				if strings.Contains(lines[j], "if ") {
					gated = strings.Contains(lines[j], "mayAuthor") || strings.Contains(lines[j], "forOrchestrator")
					break
				}
			}
			switch f {
			case "builder_tools.go", "runner_tool_catalog.go":
				// Builder's own catalog assembly and the direct path, which checks
				// the turn's owner itself (ownerRun).
				continue
			}
			if !gated {
				t.Errorf("%s:%d appends the authoring catalog without dispatchAuthoring deciding it", f, i+1)
			}
		}
	}
}

// Building answers to the owner, so a Builder handoff under work a contact
// started is refused up front, with the reason, rather than run with its
// catalog withheld.
func TestAContactCannotSetBuilderToWork(t *testing.T) {
	turn, _ := newRunGateTurn(t, "")
	turn.agent.AllowBuilderDispatch = true
	if _, _, err := turn.agentsRunGate(map[string]any{"agent": "seed-builder", "message": "build a tool"}); err != nil {
		t.Fatalf("the owner's own run should reach Builder: %v", err)
	}
	turn.ctx = withNonOwnerRequester(turn.ctx)
	_, _, err := turn.agentsRunGate(map[string]any{"agent": "seed-builder", "message": "build a tool"})
	if err == nil || !strings.Contains(err.Error(), "owner") {
		t.Fatalf("a contact's request must not reach Builder, and the refusal should say why: %v", err)
	}
}

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
		if td.Tool.Name == "schedule" {
			if out, _ := td.Handler(context.Background(), map[string]any{"action": "list"}); strings.Contains(out, "is the owner's to use") {
				t.Errorf("the owner's own run was refused: %q", out)
			}
		}
	}
	rec, err := (&chatTurn{}).recurringToolDef().Handler(stranger, map[string]any{"action": "list"})
	if err != nil || !strings.Contains(rec, "owner's to use") {
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
