package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
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

// selfClearingLink is the iMessage shape: the daemon clears the handle on the
// owner's own messages, and the bridge counts an empty handle as the owner.
type selfClearingLink struct {
	MessagingLink
	owner string
}

func (l selfClearingLink) IsOwnerHandle(owner, handle string) bool {
	return owner == l.owner && handle == ""
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
		{"the owner's own phone", AgentSyncRun{Kind: "channel", SenderHandle: "+15550100"}, true},
		{"a contact in the group", AgentSyncRun{Kind: "channel", SenderHandle: "+15550199"}, false},
		{"a contact calling themselves the owner", AgentSyncRun{Kind: "channel", SenderHandle: "+15550199", MessageSender: "owner"}, false},
		{"a channel message with no handle", AgentSyncRun{Kind: "channel"}, false},
	}
	for _, c := range cases {
		if got := channelSenderIsOwner("owner", c.run); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// The owner's own group-chat message arrives with the handle cleared, and the
// bridge says that is the owner. The check asked nothing and refused the
// owner's own request to have something built.
func TestTheOwnersOwnMessageWithAClearedHandleIsTheOwner(t *testing.T) {
	prev, _ := ActiveMessagingLink()
	RegisterMessagingLink(selfClearingLink{owner: "owner"})
	t.Cleanup(func() { RegisterMessagingLink(prev) })
	if !channelSenderIsOwner("owner", AgentSyncRun{Kind: "channel", SenderHandle: ""}) {
		t.Error("the bridge counts a cleared handle as the owner, and the check must ask it")
	}
	if channelSenderIsOwner("owner", AgentSyncRun{Kind: "channel", SenderHandle: "+15550199"}) {
		t.Error("a contact's handle is still not the owner")
	}
}

func TestAChannelHandleIsNotTheOwnerWithNoBridgeToAsk(t *testing.T) {
	prev, _ := ActiveMessagingLink()
	RegisterMessagingLink(nil)
	t.Cleanup(func() { RegisterMessagingLink(prev) })
	if channelSenderIsOwner("owner", AgentSyncRun{Kind: "channel", SenderHandle: "+15550100"}) {
		t.Error("with no bridge to compare against, a handle cannot be shown to be the owner's")
	}
	if channelSenderIsOwner("owner", AgentSyncRun{Kind: "channel"}) {
		t.Error("with no bridge, an empty handle cannot be shown to be the owner's either")
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
