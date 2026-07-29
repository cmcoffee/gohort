package bridges

import (
	"fmt"
	"testing"
)

// The loop these guard against: in a SELF thread every message is is_from_me,
// so the agent's own reply arrives back looking exactly like the owner typing.
// It carries a fresh message id, so the id dedupe misses it, and it routes to
// the channel that produced it.

func TestEchoGuardRecognizesOurOwnMessage(t *testing.T) {
	LoopGuardReset()
	const chat = "chat;-;+15551234567"

	noteOutbound(chat, "", "[Assistant] On my way.")
	// Same text, from-me, coming back: ours.
	if !isOwnEcho(chat, "", "[Assistant] On my way.", true) {
		t.Fatal("an exact echo of what we just sent must be recognized")
	}
	// Consumed — a genuine SECOND send of the same words deserves an answer
	// rather than being swallowed forever.
	if isOwnEcho(chat, "", "[Assistant] On my way.", true) {
		t.Error("the echo should be consumed once, not suppress that text indefinitely")
	}
}

func TestEchoGuardIgnoresOtherPeople(t *testing.T) {
	LoopGuardReset()
	const chat = "chat;-;+15551234567"
	noteOutbound(chat, "", "the answer is 42")

	// Someone else quoting our exact words is theirs to answer.
	if isOwnEcho(chat, "", "the answer is 42", false) {
		t.Error("a message from another person must never be treated as our echo")
	}
	// Different conversation, same text — not ours.
	if isOwnEcho("chat;-;other", "", "the answer is 42", true) {
		t.Error("the fingerprint must be scoped to the conversation")
	}
}

func TestEchoGuardNormalizesWhitespace(t *testing.T) {
	LoopGuardReset()
	const chat = "c1"
	noteOutbound(chat, "", "Hello   there\nfriend")
	if !isOwnEcho(chat, "", "hello there friend", true) {
		t.Error("transports reflow whitespace and case; the fingerprint should survive it")
	}
}

func TestEchoGuardIgnoresEmpty(t *testing.T) {
	LoopGuardReset()
	noteOutbound("c1", "", "   ")
	if isOwnEcho("c1", "", "", true) {
		t.Error("an empty message must not match everything")
	}
}

// TestContentDedupeCatchesIDLessDuplicates — seenMessage keys on the message
// id and returns false when there isn't one, so an id-less re-delivery is
// treated as new every time. That is the duplicate that starts the loop.
func TestContentDedupeCatchesIDLessDuplicates(t *testing.T) {
	LoopGuardReset()
	const chat = "c1"
	if seenContent(chat, "", "are you there?") {
		t.Fatal("first sighting must pass")
	}
	if !seenContent(chat, "", "are you there?") {
		t.Error("the immediate duplicate must be caught")
	}
	if seenContent(chat, "", "something else") {
		t.Error("different text is a different message")
	}
	if seenContent("c2", "", "are you there?") {
		t.Error("the same text in another conversation is a different message")
	}
}

// TestReplyBudgetTerminatesALoop is the guarantee: whatever the agent says,
// a conversation cannot absorb unbounded replies.
func TestReplyBudgetTerminatesALoop(t *testing.T) {
	LoopGuardReset()
	const chat = "loopy"
	tripped := false
	for i := 0; i < replyBudget+3 && !tripped; i++ {
		tripped = noteReply(chat, "", false)
	}
	if !tripped {
		t.Fatalf("budget of %d replies should have tripped", replyBudget)
	}
	if !loopTripped(chat, "") {
		t.Error("a tripped conversation must stay cut for its cooldown")
	}
	// Scoped per conversation — one runaway thread must not mute the others.
	if loopTripped("a-different-chat", "") {
		t.Error("the cut must not spread to other conversations")
	}
}

// TestReplyBudgetToleratesNormalTraffic — the cap has to sit above real
// back-and-forth or it becomes the bug.
func TestReplyBudgetToleratesNormalTraffic(t *testing.T) {
	LoopGuardReset()
	const chat = "busy"
	for i := 0; i < replyBudget-1; i++ {
		if noteReply(chat, "", false) {
			t.Fatalf("tripped after %d replies — the budget is too tight for a normal exchange", i+1)
		}
	}
	if loopTripped(chat, "") {
		t.Error("a busy but bounded conversation must not be cut")
	}
}

// TestReplyBudgetIsPerConversation — a loop in one thread must not silence
// unrelated ones.
func TestReplyBudgetIsPerConversation(t *testing.T) {
	LoopGuardReset()
	for i := 0; i < replyBudget+1; i++ {
		noteReply("runaway", "", false)
	}
	for i := 0; i < 3; i++ {
		if noteReply(fmt.Sprintf("normal-%d", i), "", false) {
			t.Error("an unrelated conversation should be nowhere near its budget")
		}
	}
	if !loopTripped("runaway", "") {
		t.Error("the runaway thread should still be cut")
	}
}

func TestNoteReplyIgnoresEmptyChat(t *testing.T) {
	LoopGuardReset()
	for i := 0; i < replyBudget+5; i++ {
		if noteReply("", "", false) {
			t.Fatal("an empty chat id must not accumulate a budget")
		}
	}
}

// TestGuardsSurviveTheTransportSplit — the failure that got through the first
// version. iMessage delivers natively or falls back to SMS/MMS, and those are
// different chat ids for the SAME thread: the reply went out one way and came
// back the other, so a chat-id-keyed fingerprint never matched and the reply
// budget filled two half-buckets instead of one full one.
func TestGuardsSurviveTheTransportSplit(t *testing.T) {
	LoopGuardReset()
	const native = "iMessage;-;+16504401019"
	const sms = "SMS;-;+16504401019"

	noteOutbound(native, "", "[Gohort] On my way.")
	if !isOwnEcho(sms, "", "[Gohort] On my way.", true) {
		t.Error("a reply sent over iMessage and reflected over SMS is still ours")
	}

	// The budget must accumulate across both legs, not split.
	LoopGuardReset()
	tripped := false
	for i := 0; i < replyBudget && !tripped; i++ {
		chat := native
		if i%2 == 1 {
			chat = sms // alternating transports, one conversation
		}
		tripped = noteReply(chat, "", false)
	}
	if !tripped {
		t.Error("a loop alternating between transports must still fill ONE budget")
	}
	if !loopTripped(sms, "") || !loopTripped(native, "") {
		t.Error("the cut must apply to the conversation, whichever leg it arrives on")
	}
}

// TestIdentityNormalization — the same person written differently is one bucket.
func TestIdentityNormalization(t *testing.T) {
	if loopIdentity("any;-;+16504401019", "") != loopIdentity("", "+1 (650) 440-1019") {
		t.Error("a formatted handle and a chat-id handle are the same person")
	}
	if loopIdentity("", "Craig@Example.com") != loopIdentity("", "craig@example.com") {
		t.Error("addresses differing only in case are one mailbox")
	}
	// A group keeps its own identity — chatHandle returns "" for one, so it must
	// not collapse onto a member.
	if loopIdentity("chat;+;group-abc", "") == loopIdentity("", "+16504401019") {
		t.Error("a group must never collapse onto a member's identity")
	}
}

// TestTagGuardCatchesRephrasedEchoes — the conclusive signal. The live loop
// showed the agent receiving "[Gohort] " + its own previous reply; the tag is
// something WE put on the wire, so anything wearing it is ours coming back,
// no matter how it was worded or how long ago.
func TestTagGuardCatchesRephrasedEchoes(t *testing.T) {
	LoopGuardReset()
	if carriesOurTag("[Gohort] anything at all") {
		t.Fatal("no tag has been emitted yet — nothing should match")
	}
	noteOutboundTag("[Gohort] ")

	if !carriesOurTag("[Gohort] Yep! Just keeping everything humming along.") {
		t.Error("an inbound wearing our tag is our own message returning")
	}
	// Different words, same tag — this is the case the fingerprint cannot catch.
	if !carriesOurTag("[gohort] a completely different sentence") {
		t.Error("the tag must hold regardless of the text after it, and of case")
	}
	// Someone else's bracketed text is not ours.
	if carriesOurTag("[Emily] are you around?") {
		t.Error("another sender's bracket must not be treated as our tag")
	}
	if carriesOurTag("no bracket here") || carriesOurTag("") {
		t.Error("untagged text must never match")
	}
}

// TestSelfThreadBudgetIsStrict — a loop is only possible in a thread addressed
// to yourself, so that thread is policed hard while conversations with real
// people keep the generous backstop.
func TestSelfThreadBudgetIsStrict(t *testing.T) {
	LoopGuardReset()
	tripped := false
	for i := 0; i < selfThreadBudget && !tripped; i++ {
		tripped = noteReply("iMessage;-;+16504401019", "", true)
	}
	if !tripped {
		t.Errorf("a self thread should cut at %d replies", selfThreadBudget)
	}

	// The same count in a real conversation is nowhere near its limit.
	LoopGuardReset()
	for i := 0; i < selfThreadBudget+2; i++ {
		if noteReply("iMessage;-;+15559998888", "", false) {
			t.Fatalf("a conversation with another person must not cut at %d replies", i+1)
		}
	}
}
