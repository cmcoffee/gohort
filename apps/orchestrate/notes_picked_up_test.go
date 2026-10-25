package orchestrate

import (
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A note the agent picks up is saved into the thread with its note id, and the
// "picked up" event carries its text: a page that arrives later draws it from
// the event, and one that redraws the thread knows the stored note is it.
func TestAPickedUpNoteIsKeptWithItsID(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	sess, err := saveChatSession(udb, ChatSession{AgentID: "a1", Messages: []ChatMessage{{Role: "user", Content: "first"}}})
	if err != nil {
		t.Fatal(err)
	}
	q := RegisterInjectionQueue("notes-test-"+sess.ID, "u", "a1")
	id, _ := q.Push("check the disk too")
	buf := &syncBuf{}
	turn := &chatTurn{user: "u", udb: udb, session: &sess, queue: q, sse: &sseWriter{live: buf}}
	turn.drainNotes()

	got, _ := loadChatSession(udb, "a1", sess.ID)
	if n := len(got.Messages); n != 2 || got.Messages[1].Content != "check the disk too" || got.Messages[1].NoteID != id {
		t.Fatalf("the note was not kept with its id: %+v", got.Messages)
	}
	out := buf.String()
	if !strings.Contains(out, `"ui_notes_consumed"`) || !strings.Contains(out, `"text":"check the disk too"`) {
		t.Errorf("the picked-up event does not carry the note's text: %s", out)
	}
}

// A page joining a running turn asks for the notes still waiting, and gets
// only its own run's.
func TestWaitingNotesAreServedToTheirOwnerOnly(t *testing.T) {
	q := RegisterInjectionQueue("waiting-notes-test", "alice", "a1")
	defer ReleaseInjectionQueue("waiting-notes-test")
	q.Push("check the disk too")
	get := func(user string) string {
		w := httptest.NewRecorder()
		agenthost.ServeWaitingNotes(w, httptest.NewRequest("GET", "/api/inject?id=waiting-notes-test", nil), user)
		return w.Body.String()
	}
	if got := get("alice"); !strings.Contains(got, `"text":"check the disk too"`) {
		t.Errorf("the owner did not get the waiting note: %s", got)
	}
	if got := get("mallory"); strings.Contains(got, "disk") {
		t.Errorf("somebody else read the waiting note: %s", got)
	}
	q.Drain()
	if got := get("alice"); strings.Contains(got, "disk") {
		t.Errorf("a picked-up note is still listed as waiting: %s", got)
	}
}

// A background task's result reaches a running turn through the same queue as
// the person's notes, but it is not theirs: kept hidden (as its short record),
// never drawn as a note bubble, never listed as waiting, never editable, and
// read by the model in full under its own marker.
func TestABackgroundResultIsNotShownAsTheUsersNote(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	sess, _ := saveChatSession(udb, ChatSession{AgentID: "a1", Messages: []ChatMessage{{Role: "user", Content: "render it"}}})
	key := "notes-test-" + sess.ID
	q := RegisterInjectionQueue(key, "u", "a1")
	defer ReleaseInjectionQueue(key)
	full := taskWakeMarker + " The work you started earlier is done: render.\nline one\nline two of a long output"
	id, _ := q.PushFramework(full, taskWakeMarker+" render done: image#3")

	w := httptest.NewRecorder()
	agenthost.ServeWaitingNotes(w, httptest.NewRequest("GET", "/api/inject?id="+key, nil), "u")
	if strings.Contains(w.Body.String(), "line one") {
		t.Errorf("a background result was listed as the person's waiting note: %s", w.Body.String())
	}
	if q.Update(id, "edited") || q.Delete(id) {
		t.Error("the person could edit or delete the framework's note")
	}

	buf := &syncBuf{}
	turn := &chatTurn{user: "u", udb: udb, session: &sess, queue: q, sse: &sseWriter{live: buf}}
	taken := turn.drainNotes()
	got, _ := loadChatSession(udb, "a1", sess.ID)
	last := got.Messages[len(got.Messages)-1]
	if !last.Hidden || last.Content != taskWakeMarker+" render done: image#3" {
		t.Errorf("kept as %+v", last)
	}
	if strings.Contains(buf.String(), "ui_notes_consumed") {
		t.Errorf("a note bubble was drawn for it: %s", buf.String())
	}
	block := notesContextBlock(taken)
	if strings.Contains(block, "The user added") || !strings.Contains(block, "line one\nline two") {
		t.Errorf("the model read it as: %q", block)
	}
}

// Conversations that saved such notes as the person's messages before show
// them hidden when served.
func TestOldFrameworkNotesAreHiddenWhenServed(t *testing.T) {
	msgs := []ChatMessage{{Role: "user", Content: "hi"}, {Role: "user", Content: taskWakeMarker + " done: x"},
		{Role: "user", Content: frameworkNoteTag + "The user DENIED it."}, {Role: "assistant", Content: taskWakeMarker}}
	hideFrameworkNotes(msgs)
	if msgs[0].Hidden || !msgs[1].Hidden || !msgs[2].Hidden || msgs[3].Hidden {
		t.Errorf("hidden: %v %v %v %v", msgs[0].Hidden, msgs[1].Hidden, msgs[2].Hidden, msgs[3].Hidden)
	}
}
