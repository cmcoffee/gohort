package orchestrate

import (
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/notices"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The guardrail block log — the record the owner reviews.
//
// It replaced a push notification, and the swap changed what the record is
// allowed to hold: an alert left the deployment, so it had to be redacted down
// to "a rule fired". This never leaves, so it carries the reason and the hook
// in full — which is the whole reason it is worth reading.

func logTurn(root Database, owner, runtimeUser, agentID, sessionID string) *chatTurn {
	return &chatTurn{
		agent:            AgentRecord{ID: agentID, Name: "X", Owner: owner},
		user:             runtimeUser,
		udb:              UserDB(root, runtimeUser),
		ownerUser:        owner,
		ownerDB:          UserDB(root, owner),
		diagSessionID:    sessionID,
		requesterChannel: "channel",
		requesterName:    "Alex Rivera",
	}
}

// TestBlockIsRecordedWithFullDetail — the record stays on the box, so unlike
// the alert it replaced it may carry what the check objected to.
func TestBlockIsRecordedWithFullDetail(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	turn := logTurn(root, "u", "u", "a1", "chat-42")
	turn.recordGuardrailBlock("never discuss compensation", guardHookPreOutput, "the draft named a salary figure")

	got := listGuardrailBlocks(UserDB(root, "u"), "a1", 10)
	if len(got) != 1 {
		t.Fatalf("expected one entry, got %d", len(got))
	}
	e := got[0]
	if e.Rule != "never discuss compensation" || e.Hook != guardHookPreOutput {
		t.Errorf("rule/hook not recorded: %+v", e)
	}
	if e.Reason != "the draft named a salary figure" {
		t.Errorf("the reason is the reviewable part and must survive: %+v", e)
	}
	if e.Channel != "channel" || e.Sender != "Alex Rivera" {
		t.Errorf("lost where it happened: %+v", e)
	}
	if e.Session != "chat-42" {
		t.Errorf("lost the thread pointer, so the full trail can't be found: %+v", e)
	}
	if e.At.IsZero() {
		t.Error("entry has no timestamp")
	}
}

// TestRepeatsAreKept — a rule tripping over and over is the pattern most worth
// seeing. Collapsing repeats would hide exactly what the log is for.
func TestRepeatsAreKept(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	turn := logTurn(root, "u", "u", "a1", "chat-42")
	for i := 0; i < 5; i++ {
		turn.recordGuardrailBlock("never send money", guardHookPreAction, "tried again")
	}
	if got := listGuardrailBlocks(UserDB(root, "u"), "a1", 50); len(got) != 5 {
		t.Errorf("expected five entries, got %d", len(got))
	}
}

// TestNewestFirstAndCapped — the question is almost always "what just
// happened", and the list has to stay readable and small.
func TestNewestFirstAndCapped(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	db := UserDB(root, "u")
	for i := 0; i < guardrailLogKept+10; i++ {
		appendGuardrailBlock(db, "a1", GuardrailBlock{At: time.Now(), Rule: "rule-" + itoa(i)})
	}
	all := listGuardrailBlocks(db, "a1", 0)
	if len(all) != guardrailLogKept {
		t.Fatalf("retention cap not applied: %d entries", len(all))
	}
	if all[0].Rule != "rule-"+itoa(guardrailLogKept+9) {
		t.Errorf("not newest-first: head is %q", all[0].Rule)
	}
	if n := len(listGuardrailBlocks(db, "a1", 5)); n != 5 {
		t.Errorf("limit ignored: got %d", n)
	}
}

// TestRecordedInTheOwnersStore — a channel turn runs as a synthetic per-chat
// identity; a log filed under that identity is filed where nobody will look.
func TestRecordedInTheOwnersStore(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	turn := logTurn(root, "u", "phantom:chat123", "a1", "chat-42")
	turn.recordGuardrailBlock("no addresses", guardHookPreInput, "asked for a home address")

	if got := listGuardrailBlocks(UserDB(root, "u"), "a1", 10); len(got) != 1 {
		t.Errorf("owner cannot see the record: %+v", got)
	}
	if got := listGuardrailBlocks(UserDB(root, "phantom:chat123"), "a1", 10); len(got) != 0 {
		t.Errorf("filed under the synthetic identity: %+v", got)
	}
}

// TestNothingIsSentAnywhere — the record is local by construction. This pins
// the absence of the delivery path the owner asked to remove, so a future
// "helpful" reinstatement has to delete a test that says why.
func TestNothingIsSentAnywhere(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	turn := logTurn(root, "u", "u", "a1", "chat-42")
	// No messaging link and no mail configured: recording must still succeed
	// and must not error, because it never attempts delivery.
	turn.recordGuardrailBlock("never discuss pay", guardHookPreOutput, "reason")
	if got := listGuardrailBlocks(UserDB(root, "u"), "a1", 10); len(got) != 1 {
		t.Fatalf("recording depends on something external: %+v", got)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

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
