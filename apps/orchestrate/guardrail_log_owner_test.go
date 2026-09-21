package orchestrate

// A block on somebody ELSE'S run of a shared agent.
//
// It is recorded with the account that ran it, because "one of my rules fired"
// and "the person I shared this with cannot get past it" are different facts
// and only the second is worth acting on.
//
// It is not SENT anywhere. One pass did file a folded notice to the owner, and
// two things undid it: notices.Record returns a repeat unread on purpose, so
// the fold collapsed the row but not the attention and the bell never stayed
// cleared; and the recipient is no longer silent, since they have a standing
// line naming whose agent this is and a button to ask for what they need. A
// person deciding something matters is worth an interruption. A rule doing its
// job is not.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/notices"
	"github.com/cmcoffee/snugforge/kvlite"
)

// sharedRunTurn is a peer-shared web run: a real account driving an agent that
// belongs to somebody else, which is what runner_http builds when an agent's
// Owner is not the signed-in user.
func sharedRunTurn(root Database, owner, runBy, agentID string) *chatTurn {
	return &chatTurn{
		agent:     AgentRecord{ID: agentID, Name: "Runbooks", Owner: owner},
		user:      runBy,
		udb:       UserDB(root, runBy),
		ownerUser: owner,
		ownerDB:   UserDB(root, owner),
	}
}

// The owner's own run has nobody to name, which is every run on an unshared
// agent, so an existing log reads exactly as it did.
func TestTheOwnersOwnRunNamesNobody(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	turn := logTurn(root, "alice", "alice", "a1", "chat-1")
	turn.recordGuardrailBlock("no salary talk", guardHookPreOutput, "named a figure")

	got := listGuardrailBlocks(UserDB(root, "alice"), "a1", 10)
	if len(got) != 1 || got[0].RanBy != "" {
		t.Fatalf("the owner's own run must name nobody: %+v", got)
	}
}

// A channel inbound also runs under a different identity, and it is excluded:
// "phantom:<chatID>" names a conversation, not a person, and Sender/Channel
// already carry what is known about that requester.
func TestAChannelInboundIsNotAnAccount(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	turn := logTurn(root, "alice", "phantom:9921", "a1", "chat-2")
	turn.recordGuardrailBlock("no salary talk", guardHookPreOutput, "named a figure")

	got := listGuardrailBlocks(UserDB(root, "alice"), "a1", 10)
	if len(got) != 1 {
		t.Fatalf("expected one entry: %+v", got)
	}
	if got[0].RanBy != "" {
		t.Errorf("a synthetic per-chat identity must not be recorded as an account: %q", got[0].RanBy)
	}
	if got[0].Sender == "" && got[0].Channel == "" {
		t.Error("the channel fields are what carries a channel requester; both are empty")
	}
}

// The shared case: recorded with the account, in the OWNER's store, where the
// review surfaces read. And nothing is sent.
func TestABlockOnSomebodyElsesRunIsRecordedAndNotSent(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	pinRootDB(t)
	turn := sharedRunTurn(root, "alice", "bob", "a1")
	turn.recordGuardrailBlock("never send anything outside the company", guardHookPreAction, "the draft named an external address")

	got := listGuardrailBlocks(UserDB(root, "alice"), "a1", 10)
	if len(got) != 1 {
		t.Fatalf("expected one entry in the owner's log: %+v", got)
	}
	if got[0].RanBy != "bob" {
		t.Errorf("the run's account was not recorded: %+v", got[0])
	}
	// And it says so on the row the console draws.
	if w := guardrailRowWhere(got[0]); !strings.Contains(w, "run by bob") {
		t.Errorf("the console row does not say whose run it was: %q", w)
	}

	// Nobody is interrupted. A rule doing its job is something to look at
	// later, which is what the log is; the bell is for somebody waiting.
	if n := notices.List(RootDB, "alice"); len(n) != 0 {
		t.Errorf("a block notified the owner: %+v", n)
	}

	// Repeats are all kept: the pattern is the thing the log is for.
	for i := 0; i < 10; i++ {
		turn.recordGuardrailBlock("never send anything outside the company", guardHookPreAction, "again")
	}
	if got := listGuardrailBlocks(UserDB(root, "alice"), "a1", 100); len(got) != 11 {
		t.Errorf("the log collapsed repeats; it must not: %d entries", len(got))
	}
	if n := notices.List(RootDB, "alice"); len(n) != 0 {
		t.Errorf("eleven blocks reached the bell: %+v", n)
	}
}

// The person stopped gets nothing here either: the block is filed in the
// owner's store, and the rule is the owner's to see.
func TestThePersonStoppedIsNotToldWhichRule(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	pinRootDB(t)
	turn := sharedRunTurn(root, "alice", "bob", "a1")
	turn.recordGuardrailBlock("never mention project atlas", guardHookPreOutput, "the draft named it")

	if n := notices.List(RootDB, "bob"); len(n) != 0 {
		t.Fatalf("the person stopped was handed the owner's rules: %+v", n)
	}
	if got := listGuardrailBlocks(UserDB(root, "bob"), "a1", 10); len(got) != 0 {
		t.Errorf("the block was filed in the recipient's store: %+v", got)
	}
}
