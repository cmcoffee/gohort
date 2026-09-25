package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// request_build queues a build_agent authorization stamped with the requester,
// and refuses a blank brief. The approval side (Builder dispatch, OwnedBy
// stamping) is covered by the dispatch path; this locks the queue contract.
func TestRequestBuildQueuesAuthorization(t *testing.T) {
	prev := RootDB
	defer func() { RootDB = prev }()
	RootDB = &DBase{Store: kvlite.MemStore()}

	td := requestBuildTool(nil, "alice", "agent-moltbook", "Moltbook")

	// Blank brief refused.
	if _, err := td.Handler(context.Background(), map[string]any{"brief": "   "}); err == nil {
		t.Error("blank brief must be refused")
	}

	out, err := td.Handler(context.Background(), map[string]any{"brief": "A viral-post researcher for Moltbook.", "name": "Viral Researcher"})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !strings.Contains(out, "approval") {
		t.Errorf("result should tell the agent it's queued for approval: %q", out)
	}

	// Exactly one build_agent authorization, stamped with the requester + builder.
	found := 0
	for _, a := range ListAuthorizations(RootDB, "alice") {
		if a.Action == buildAgentAction {
			found++
			if a.FromAgent != "agent-moltbook" {
				t.Errorf("FromAgent should be the requester, got %q", a.FromAgent)
			}
			if a.Agent != "builder" {
				t.Errorf("approval should dispatch builder, got %q", a.Agent)
			}
			if !strings.Contains(a.Brief, "Viral Researcher") {
				t.Errorf("suggested name should ride in the brief: %q", a.Brief)
			}
		}
	}
	if found != 1 {
		t.Fatalf("expected exactly one build_agent authorization, got %d", found)
	}
}

// The approved build reports back to the conversation that asked for it. It
// used to queue with no origin, so runApprovedDelegation returned before
// delivering: the build ran, Builder wrote its reply, and the agent that asked
// was never told.
func TestRequestBuildRemembersWhereItWasAskedFrom(t *testing.T) {
	prev := RootDB
	defer func() { RootDB = prev }()
	RootDB = &DBase{Store: kvlite.MemStore()}

	sess := &ToolSession{Username: "alice", ChatSessionID: "s-ask", ChannelChatID: "chat-1", Network: NewNetworkConnector(true)}
	prompted := false
	sess.PendingApprovalPrompt = func(Authorization) { prompted = true }
	out, err := requestBuildTool(sess, "alice", "agent-moltbook", "Moltbook").Handler(context.Background(),
		map[string]any{"brief": "A viral-post researcher."})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "comes back to you here") {
		t.Errorf("the asking agent should hear the result comes back:\n%s", out)
	}
	if !prompted {
		t.Error("the queued build should raise the in-chat approval card, as a queued delegation does")
	}
	auths := ListAuthorizations(RootDB, "alice")
	if len(auths) != 1 {
		t.Fatalf("authorizations = %+v", auths)
	}
	if a := auths[0]; a.FromSession != "s-ask" || a.FromChatID != "chat-1" || a.FromAgent != "agent-moltbook" || !a.FromPrivate {
		t.Errorf("the queued build should carry its origin and privacy: %+v", a)
	}
}
