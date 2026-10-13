package orchestrate

import (
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
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
		ServeWaitingNotes(w, httptest.NewRequest("GET", "/api/inject?id=waiting-notes-test", nil), user)
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
