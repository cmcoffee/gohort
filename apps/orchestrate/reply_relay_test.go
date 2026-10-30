package orchestrate

// An agent asked someone a question for the owner and said it would pass the
// answer on; the answer came, woke that person's own channel, and the owner's
// conversation never heard it. A send from a conversation now waits for the
// person's next message and wakes that conversation with it.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/messaging"
)

// wokenWith waits for the conversation's queue to receive a note.
func wokenWith(t *testing.T, q *injectionQueue) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if notes := q.Drain(); len(notes) > 0 {
			return notes[0].Text
		}
		time.Sleep(10 * time.Millisecond)
	}
	return ""
}

func TestAnAnswerComesBackToTheConversationThatAsked(t *testing.T) {
	depStores(t, "u")
	withReplyLink(t)
	SetContactPreAuthorized(RootDB, "u", "lead", operatorRecipientKey("chat-+15550100", "+15550100"), true)
	sid := "room-" + UUIDv4()
	sess := sentAgentSession(t, orchRef, sid)
	out := messageContact(t, sess, "lead", map[string]any{"to": "+15550100", "text": "Can you log in today?"})
	if !strings.Contains(out, "comes back to you here") {
		t.Errorf("the send should say the answer comes back: %q", out)
	}
	armed := loadReplyRelays("u")
	if len(armed) != 1 || armed[0].Origin.SessionID != sid || armed[0].Origin.AgentID != "wiwee" || armed[0].Sent != "Can you log in today?" {
		t.Fatalf("armed %+v", armed)
	}
	q := registerInjectionQueue(sid, "u", "wiwee")
	t.Cleanup(func() { releaseInjectionQueue(sid) })

	// Our own message echoed back, and someone else's, answer nothing.
	orchRef.relayReply(messaging.InboundEvent{Owner: "u", ChatID: "chat-+15550100", Handle: "+15550100", Text: "Can you log in today?"})
	orchRef.relayReply(messaging.InboundEvent{Owner: "u", ChatID: "chat-+15550199", Handle: "+15550199", Text: "hi"})
	orchRef.relayReply(messaging.InboundEvent{Owner: "someone-else", Handle: "+15550100", Text: "yes"})
	if len(loadReplyRelays("u")) != 1 {
		t.Fatal("a message that is not their answer used up the relay")
	}

	// Their answer, arriving under the handle form only.
	orchRef.relayReply(messaging.InboundEvent{Owner: "u", Handle: "+15550100", Sender: "Pat", Text: "Yes, on in five.", Addresses: []string{"+15550100"}})
	note := wokenWith(t, q)
	for _, want := range []string{replyWakeMarker, "Pat answered", `"Can you log in today?"`, `"Yes, on in five."`, "Tell the user what they said"} {
		if !strings.Contains(note, want) {
			t.Errorf("the wake lacks %q:\n%s", want, note)
		}
	}
	if !isTaskWake(note) {
		t.Error("the answer should wake the conversation as a result, not a scheduled brief")
	}
	if len(loadReplyRelays("u")) != 0 {
		t.Error("the relay should be spent by the answer")
	}
	orchRef.relayReply(messaging.InboundEvent{Owner: "u", Handle: "+15550100", Text: "also, see you then"})
	if again := wokenWith(t, q); again != "" {
		t.Errorf("a second message woke the conversation again: %q", again)
	}
}

// A queued send waits for nothing until it is approved and goes out; then
// the answer comes back to the conversation that queued it.
func TestAnApprovedSendIsWaitedOnOnceItGoesOut(t *testing.T) {
	depStores(t, "u")
	link := withReplyLink(t)
	sid := "room-" + UUIDv4()
	sess := sentAgentSession(t, orchRef, sid)
	messageContact(t, sess, "lead", map[string]any{"to": "+15550100", "text": "Are you free Friday?"})
	if n := len(loadReplyRelays("u")); n != 0 {
		t.Fatalf("%d relay(s) armed before the send was approved", n)
	}
	auths := ListAuthorizations(RootDB, "u")
	if len(auths) != 1 || auths[0].FromSession != sid {
		t.Fatalf("the queued send should record where it was asked from: %+v", auths)
	}
	app := &OrchestrateApp{AppCore: AppCore{DB: orchestrateBaseDB}}
	r := httptest.NewRequest(http.MethodPost, "/api/console/approvals/approve?id="+auths[0].ID, nil)
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(AuthDB(), "u")})
	w := httptest.NewRecorder()
	routed(app)(w, r)
	if w.Code != http.StatusNoContent || len(link.sent) != 1 {
		t.Fatalf("approve answered %d, sent %v", w.Code, link.sent)
	}
	armed := loadReplyRelays("u")
	if len(armed) != 1 || armed[0].Origin.SessionID != sid || armed[0].Origin.AgentID != "wiwee" || armed[0].ChatID != "chat-+15550100" {
		t.Fatalf("armed %+v", armed)
	}
}

// A relay waits on one person for one conversation, for a day, and not on the
// person whose own conversation it is.
func TestARelayKeepsToItsTerms(t *testing.T) {
	depStores(t, "u")
	origin := taskOrigin{SessionID: "s-1", User: "u", AgentID: "lead"}
	armReplyRelay("u", "chat-1", "+15550100", "first", origin)
	armReplyRelay("u", "chat-1", "+15550100", "second", origin)
	if armed := loadReplyRelays("u"); len(armed) != 1 || armed[0].Sent != "second" {
		t.Errorf("asking the same person again should re-arm, not stack: %+v", armed)
	}
	armReplyRelay("u", "chat-1", "+15550100", "from elsewhere", taskOrigin{SessionID: "s-2", User: "u", AgentID: "lead"})
	if n := len(loadReplyRelays("u")); n != 2 {
		t.Errorf("two conversations waiting on one person should both be kept, got %d", n)
	}

	depStores(t, "u")
	armReplyRelay("u", "chat-1", "+15550100", "hi", taskOrigin{SessionID: "s-1", User: "u", AgentID: "lead", ChatID: "chat-1"})
	armReplyRelay("u", "chat-1", "+15550100", "hi", taskOrigin{SessionID: "", User: "u", AgentID: "lead"})
	armReplyRelay("u", "", "", "hi", origin)
	if n := len(loadReplyRelays("u")); n != 0 {
		t.Errorf("their own conversation, no conversation, or no one: %d relay(s) armed", n)
	}

	armReplyRelay("u", "chat-1", "+15550100", "hi", origin)
	rs := loadReplyRelays("u")
	rs[0].Until = time.Now().Add(-time.Minute)
	saveReplyRelays("u", rs)
	if due := takeReplyRelays(messaging.InboundEvent{Owner: "u", ChatID: "chat-1", Text: "late answer"}); len(due) != 0 {
		t.Errorf("an expired relay woke: %+v", due)
	}
	if n := len(loadReplyRelays("u")); n != 0 {
		t.Errorf("an expired relay was kept")
	}

	msgs := []ChatMessage{{Role: "user", Content: replyWakeMarker + " Pat answered"}, {Role: "user", Content: "a real message"}}
	hideFrameworkNotes(msgs)
	if !msgs[0].Hidden || msgs[1].Hidden {
		t.Errorf("the wake is the framework's, not something the person wrote: %+v", msgs)
	}
}

// A turn whose reply streamed as a mid-turn bubble still called the tools it
// called: the bubble carried them off, and the promise beside the send was
// recorded as unkept.
func TestATurnThatCalledAToolCountsItWhateverCarriedIt(t *testing.T) {
	turn := &chatTurn{}
	if turn.calledAnyTool() {
		t.Fatal("a turn with no calls called a tool")
	}
	turn.recordToolCall(toolCallRecord{Name: "message_contact"})
	turn.lastBubbleToolIdx = 1
	if len(turn.persistedToolCalls()) != 0 || !turn.calledAnyTool() {
		t.Errorf("persisted %d, called %v", len(turn.persistedToolCalls()), turn.calledAnyTool())
	}
}
