package orchestrate

import (
	"context"
	"testing"
)

// A run somebody other than the owner started is judged as not the owner,
// though it runs as the owner's account: a webhook, or a stranger's channel
// message handed to another agent. The guardrail read only the turn's own
// fields, so "anyone but me" let the stranger through once the request
// reached a delegate.
func TestANonOwnersHandOffIsNotJudgedAsTheOwner(t *testing.T) {
	agent := AgentRecord{ID: "a", Owner: "alice"}
	own := &chatTurn{user: "alice", agent: agent, ctx: context.Background()}
	if who := own.requester(); !who.Owner {
		t.Fatal("the owner's own run is not the owner")
	}
	for name, ctx := range map[string]context.Context{
		"webhook":           withNonOwnerRequester(context.Background()),
		"stranger hand-off": withNonOwnerRequester(context.Background(), "chan-1"),
	} {
		turn := &chatTurn{user: "alice", agent: agent, ctx: ctx}
		if who := turn.requester(); who.Owner || who.Account != "" {
			t.Errorf("%s: judged as the owner: %+v", name, who)
		}
		// Nor as authenticated on the owner's account, which the run uses
		// whoever started it, though the roster lists that account.
		turn.agent.AuthorizedIdentities = []string{"alice"}
		if who := turn.requester(); who.Authorized {
			t.Errorf("%s: authorized on the owner's account: %+v", name, who)
		}
	}
}
