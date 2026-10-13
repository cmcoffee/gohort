package orchestrate

import (
	"strings"
	"testing"
	"time"
)

// A card nobody answered in time is kept: the call waits, the card is saved
// with the turn, and the model is told it is waiting rather than refused.
// Answering it later grants a pass for exactly that call, used once.
func TestAnUnansweredApprovalIsKeptAndAnsweredLater(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	prev := toolConfirmTimeout
	toolConfirmTimeout = 20 * time.Millisecond
	t.Cleanup(func() { toolConfirmTimeout = prev })

	sess := &ChatSession{ID: "s-keep", AgentID: "a-wren"}
	turn := &chatTurn{user: "alice", udb: udb, agent: AgentRecord{ID: "a-wren", Name: "Wren"},
		sse: &sseWriter{live: &syncBuf{}}, session: sess}
	args := "query: acme merger terms"
	if turn.escalateToolConfirm(toolConfirmRequest{tool: "web_search", prompt: "Allow web_search to run?",
		detail: confirmCallSummary(nil, "web_search", args), because: "asks", args: args}) {
		t.Fatal("an unanswered card let the call through")
	}
	why := turn.confirmRefusal("web_search")
	if !strings.Contains(why, "NOT RUN YET") || !strings.Contains(why, "waiting for their approval") {
		t.Errorf("the model is not told it is waiting: %q", why)
	}
	if again := turn.confirmRefusal("web_search"); again != "" {
		t.Errorf("the waiting note was given twice: %q", again)
	}
	bubbles := turn.drainMidTurnBubbles()
	if len(bubbles) != 1 || bubbles[0].Block["type"] != "kept_approval" || bubbles[0].Block["keep_open"] != true {
		t.Fatalf("the kept card is not saved with the turn: %+v", bubbles)
	}
	id, _ := bubbles[0].Block["id"].(string)

	// Saved in the conversation, then answered by somebody else: refused.
	sess.Messages = append(sess.Messages, bubbles...)
	if _, err := saveChatSession(udb, *sess); err != nil {
		t.Fatal(err)
	}
	if _, err := app.answerPendingApproval("mallory", id, "allow"); err == nil {
		t.Error("somebody else answered the owner's approval")
	}
	if takeResumePass("alice", "a-wren", "web_search", args) {
		t.Fatal("a pass existed before anyone answered")
	}

	// The owner approves: a pass for exactly that call, once, and the saved
	// card says it was answered.
	if _, err := app.answerPendingApproval("alice", id, "allow"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.answerPendingApproval("alice", id, "deny"); err == nil {
		t.Error("an answered approval was answered again")
	}
	if takeResumePass("alice", "a-wren", "web_search", "query: something else") {
		t.Error("the pass covered a different call")
	}
	if !takeResumePass("alice", "a-wren", "web_search", args) {
		t.Error("the approved call has no pass")
	}
	if takeResumePass("alice", "a-wren", "web_search", args) {
		t.Error("the pass was usable twice")
	}
	stored, _ := loadChatSession(udb, "a-wren", "s-keep")
	if len(stored.Messages) != 1 || stored.Messages[0].Block["answer"] != "allow" {
		t.Errorf("the saved card does not show the answer: %+v", stored.Messages)
	}
}

// With no conversation to keep a card in, a timeout denies as before.
func TestATimeoutWithNoConversationStillDenies(t *testing.T) {
	prev := toolConfirmTimeout
	toolConfirmTimeout = 20 * time.Millisecond
	t.Cleanup(func() { toolConfirmTimeout = prev })
	turn := &chatTurn{user: "alice", sse: &sseWriter{live: &syncBuf{}}}
	if turn.escalateToolConfirm(toolConfirmRequest{tool: "web_search", prompt: "Allow?", args: "query: x"}) {
		t.Fatal("allowed")
	}
	if why := turn.confirmRefusal("web_search"); why != "" {
		t.Errorf("a plain timeout told the model it is waiting: %q", why)
	}
}
