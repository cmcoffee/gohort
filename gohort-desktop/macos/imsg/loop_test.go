//go:build darwin

package imsg

import (
	"testing"
	"time"
)

// Messaging your own number writes TWO rows for one message: is_from_me=1 (the
// sent copy) and is_from_me=0 (the received copy, because you are also the
// recipient). The skip used to live entirely inside the is_from_me=1 branch, so
// the received copy of our OWN reply went up as an ordinary inbound with a
// fresh ROWID — nothing downstream could recognize it, and the agent answered
// itself. These cover the keying that lets the received leg be identified.

func TestSentIdentityIgnoresTransport(t *testing.T) {
	// iMessage natively and the same thread over SMS/MMS are different chat
	// ids for one person — a reply sent on one leg comes back on the other.
	native := sentIdentity("iMessage;-;+16505550142", "")
	sms := sentIdentity("SMS;-;+16505550142", "")
	if native != sms {
		t.Errorf("transport must not change identity: %q vs %q", native, sms)
	}
	if native != "+16505550142" {
		t.Errorf("identity = %q, want the bare handle", native)
	}
	// Formatting differences are the same person.
	if sentIdentity("", "+1 (650) 555-0142") != native {
		t.Error("a formatted handle is the same person as its bare form")
	}
	// A group has no single handle and keeps its own id.
	if got := sentIdentity("chat;+;group-abc", ""); got != "chat;+;groupabc" {
		t.Errorf("a group must keep its own id, got %q", got)
	}
	// Chat id missing entirely — fall back to the handle.
	if sentIdentity("", "+16505550142") != native {
		t.Error("with no chat id the handle identifies the person")
	}
}

func TestSentKeyRefusesShortText(t *testing.T) {
	// Short replies recur naturally ("ok", "sure"); matching on them would
	// swallow real messages.
	if k := sentKey("iMessage;-;+1650", "", "ok"); k != "" {
		t.Errorf("short text must not be matchable, got %q", k)
	}
	if k := sentKey("iMessage;-;+1650", "", "this is long enough"); k == "" {
		t.Error("ordinary text must produce a key")
	}
	// Same words to DIFFERENT people are different keys.
	a := sentKey("iMessage;-;+1111111111", "", "the meeting is at four")
	b := sentKey("iMessage;-;+2222222222", "", "the meeting is at four")
	if a == b {
		t.Error("the same text to two people must not collide")
	}
}

func TestMatchesRecentSentTextAcrossTransports(t *testing.T) {
	sentTextMu.Lock()
	sentText = map[string]time.Time{}
	sentTextMu.Unlock()

	const text = "[Gohort] Yep! Just keeping everything humming along."

	rememberSentText("iMessage;-;+16505550142", "+16505550142", text)

	// The received copy arrives on the SMS leg of the same thread.
	if !matchesRecentSentText("SMS;-;+16505550142", "+16505550142", text) {
		t.Error("our own message must be recognized whichever transport returned it")
	}
	// A different person saying the same thing is theirs.
	if matchesRecentSentText("iMessage;-;+15559998888", "+15559998888", text) {
		t.Error("another conversation must not match")
	}
	// Different text in the same thread is a real message.
	if matchesRecentSentText("iMessage;-;+16505550142", "+16505550142", "something we did not send") {
		t.Error("text we never sent must not match")
	}
}
