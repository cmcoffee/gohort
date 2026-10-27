package orchestrate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func TestTrailingNotesMatch(t *testing.T) {
	msgs := []ChatMessage{{Role: "user", Content: "first"}, {Role: "assistant", Content: "ok"},
		{Role: "user", Content: "also check the logs"}, {Role: "user", Content: "and the disk"}}
	if !trailingNotesMatch(msgs, "also check the logs\n\nand the disk") || !trailingNotesMatch(msgs, "and the disk") {
		t.Error("the saved notes were not recognised")
	}
	if trailingNotesMatch(msgs, "something else") || trailingNotesMatch(msgs, "first") || trailingNotesMatch(msgs, "") {
		t.Error("matched what the conversation does not end on")
	}
}

// Notes a turn left unread, sent on by the panel as the next input, are the
// turn's input: the conversation is not given them a second time.
func TestUnreadNotesSentOnAreNotAddedTwice(t *testing.T) {
	T, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	rec, _ := saveAgent(udb, AgentRecord{Name: "Helper", Owner: user, OrchestratorPrompt: "You help."})
	sess := ChatSession{ID: "s-notes", AgentID: rec.ID, Messages: []ChatMessage{
		{Role: "user", Content: "look into it"}, {Role: "assistant", Content: "Done."},
		{Role: "user", Content: "also check the disk"}, // saved unread at the end of that turn
	}}
	if _, err := saveChatSession(udb, sess); err != nil {
		t.Fatal(err)
	}
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "The disk is fine."}}}
	T.LLM = fake
	agent, _ := loadAgent(udb, rec.ID)
	body, _ := json.Marshal(map[string]any{"message": "also check the disk", "session_id": "s-notes", "from_notes": true})
	w := httptest.NewRecorder()
	T.handleSend(w, asUser(httptest.NewRequest(http.MethodPost, "/api/send?agent_id="+rec.ID, strings.NewReader(string(body))), user), udb, user, agent)
	got, _ := loadChatSession(udb, rec.ID, "s-notes")
	n := 0
	for _, m := range got.Messages {
		if m.Role == "user" && strings.Contains(m.Content, "check the disk") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the note is in the conversation %d times", n)
	}
	if fake.Calls() == 0 {
		t.Fatal("no turn ran on the note")
	}
	last := ""
	for _, m := range fake.Sent(0) {
		if m.Role == "user" {
			last = m.Content
		}
	}
	if !strings.Contains(last, "check the disk") {
		t.Errorf("the turn's input was not the note: %q", last)
	}
}

// The send waits for the turn that left the notes to save them.
func TestUnreadNotesWaitForTheLastTurnToSaveThem(t *testing.T) {
	prev := leftoverNotesWait
	leftoverNotesWait = 2 * time.Second
	t.Cleanup(func() { leftoverNotesWait = prev })
	T, udb, _ := newTestOrchestrate(t)
	sess := ChatSession{ID: "s-wait", AgentID: "ag"}
	saveChatSession(udb, sess)
	registerInjectionQueue("s-wait", "u", "ag")
	go func() {
		time.Sleep(300 * time.Millisecond)
		latest, _ := loadChatSession(udb, "ag", "s-wait")
		latest.Messages = append(latest.Messages, ChatMessage{Role: "user", Content: "late note"})
		saveChatSession(udb, latest)
		releaseInjectionQueue("s-wait")
	}()
	got := T.awaitLeftoverNotes(udb, "ag", sess)
	if !trailingNotesMatch(got.Messages, "late note") {
		t.Errorf("did not wait for the note to be saved: %+v", got.Messages)
	}
}
