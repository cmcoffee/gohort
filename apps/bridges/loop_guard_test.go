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

	noteOutbound(chat, "[Assistant] On my way.")
	// Same text, from-me, coming back: ours.
	if !isOwnEcho(chat, "[Assistant] On my way.", true) {
		t.Fatal("an exact echo of what we just sent must be recognized")
	}
	// Consumed — a genuine SECOND send of the same words deserves an answer
	// rather than being swallowed forever.
	if isOwnEcho(chat, "[Assistant] On my way.", true) {
		t.Error("the echo should be consumed once, not suppress that text indefinitely")
	}
}

func TestEchoGuardIgnoresOtherPeople(t *testing.T) {
	LoopGuardReset()
	const chat = "chat;-;+15551234567"
	noteOutbound(chat, "the answer is 42")

	// Someone else quoting our exact words is theirs to answer.
	if isOwnEcho(chat, "the answer is 42", false) {
		t.Error("a message from another person must never be treated as our echo")
	}
	// Different conversation, same text — not ours.
	if isOwnEcho("chat;-;other", "the answer is 42", true) {
		t.Error("the fingerprint must be scoped to the conversation")
	}
}

func TestEchoGuardNormalizesWhitespace(t *testing.T) {
	LoopGuardReset()
	const chat = "c1"
	noteOutbound(chat, "Hello   there\nfriend")
	if !isOwnEcho(chat, "hello there friend", true) {
		t.Error("transports reflow whitespace and case; the fingerprint should survive it")
	}
}

func TestEchoGuardIgnoresEmpty(t *testing.T) {
	LoopGuardReset()
	noteOutbound("c1", "   ")
	if isOwnEcho("c1", "", true) {
		t.Error("an empty message must not match everything")
	}
}

// TestContentDedupeCatchesIDLessDuplicates — seenMessage keys on the message
// id and returns false when there isn't one, so an id-less re-delivery is
// treated as new every time. That is the duplicate that starts the loop.
func TestContentDedupeCatchesIDLessDuplicates(t *testing.T) {
	LoopGuardReset()
	const chat = "c1"
	if seenContent(chat, "are you there?") {
		t.Fatal("first sighting must pass")
	}
	if !seenContent(chat, "are you there?") {
		t.Error("the immediate duplicate must be caught")
	}
	if seenContent(chat, "something else") {
		t.Error("different text is a different message")
	}
	if seenContent("c2", "are you there?") {
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
		tripped = noteReply(chat)
	}
	if !tripped {
		t.Fatalf("budget of %d replies should have tripped", replyBudget)
	}
	if !loopTripped(chat) {
		t.Error("a tripped conversation must stay cut for its cooldown")
	}
	// Scoped per conversation — one runaway thread must not mute the others.
	if loopTripped("a-different-chat") {
		t.Error("the cut must not spread to other conversations")
	}
}

// TestReplyBudgetToleratesNormalTraffic — the cap has to sit above real
// back-and-forth or it becomes the bug.
func TestReplyBudgetToleratesNormalTraffic(t *testing.T) {
	LoopGuardReset()
	const chat = "busy"
	for i := 0; i < replyBudget-1; i++ {
		if noteReply(chat) {
			t.Fatalf("tripped after %d replies — the budget is too tight for a normal exchange", i+1)
		}
	}
	if loopTripped(chat) {
		t.Error("a busy but bounded conversation must not be cut")
	}
}

// TestReplyBudgetIsPerConversation — a loop in one thread must not silence
// unrelated ones.
func TestReplyBudgetIsPerConversation(t *testing.T) {
	LoopGuardReset()
	for i := 0; i < replyBudget+1; i++ {
		noteReply("runaway")
	}
	for i := 0; i < 3; i++ {
		if noteReply(fmt.Sprintf("normal-%d", i)) {
			t.Error("an unrelated conversation should be nowhere near its budget")
		}
	}
	if !loopTripped("runaway") {
		t.Error("the runaway thread should still be cut")
	}
}

func TestNoteReplyIgnoresEmptyChat(t *testing.T) {
	LoopGuardReset()
	for i := 0; i < replyBudget+5; i++ {
		if noteReply("") {
			t.Fatal("an empty chat id must not accumulate a budget")
		}
	}
}
