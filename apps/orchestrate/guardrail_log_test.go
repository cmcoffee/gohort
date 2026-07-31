package orchestrate

// The guardrail block log — the record the owner reviews.
//
// It replaced a push notification, and the swap changed what the record is
// allowed to hold: an alert left the deployment, so it had to be redacted down
// to "a rule fired". This never leaves, so it carries the reason and the hook
// in full — which is the whole reason it is worth reading.

import (
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

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
