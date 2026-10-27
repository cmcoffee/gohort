package servitor

import (
	"net/http"
	"net/http/httptest"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/changes"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Notes written mid-run and picked up are saved with the turn, between the
// message and the reply, like any message the user sent.
func TestPickedUpNotesAreSavedWithTheTurn(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	id := ensureSession(udb, "ap1", "", "why is it slow")
	appendTurn(udb, "ap1", id, "why is it slow", []string{"check the disk too"}, "The disk is full.")
	s, _ := loadSession(udb, "ap1", id)
	var got []string
	for _, m := range s.Messages {
		got = append(got, m.Role+":"+m.Content)
	}
	want := []string{"user:why is it slow", "user:check the disk too", "assistant:The disk is full."}
	if len(got) != len(want) {
		t.Fatalf("turn saved as %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("turn saved as %v, want %v", got, want)
		}
	}
}

// The picked-up event carries each note's text beside its id.
func TestPickedUpEventCarriesTheText(t *testing.T) {
	ev := translateProbeEvent(probeEvent{Kind: "notes_consumed", IDs: []string{"n1"}, Notes: []string{"check the disk too"}})
	notes, _ := ev["notes"].([]map[string]string)
	if len(notes) != 1 || notes[0]["id"] != "n1" || notes[0]["text"] != "check the disk too" {
		t.Errorf("event: %+v", ev)
	}
}

// The chat's Edit and ✕ on a message reach Servitor's sessions as they reach
// orchestrate's: {at} cuts the thread from a message on, {delete_at} takes
// out one message and keeps the rest. Without the PATCH the page offered no
// controls at all, since it shows them only when an app can carry them out.
func TestASessionMessageCanBeEditedOrDeleted(t *testing.T) {
	twoUserAuth(t)
	app := &Servitor{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	udb := UserDB(app.DB, "craig")
	saveSession(udb, "box-1", chatSession{ID: "s1", Name: "disk", Messages: []sessionMsg{
		{Role: "user", Content: "how full is /var"}, {Role: "assistant", Content: "82%"},
		{Role: "user", Content: "and /home"}, {Role: "assistant", Content: "40%"},
	}})
	patch := func(user, body string) int {
		w := httptest.NewRecorder()
		routed(app)(w, reqAs(t, http.MethodPatch, "/api/sessions/s1?appliance_id=box-1", user, body))
		return w.Code
	}
	contents := func() []string {
		s, _ := loadSession(udb, "box-1", "s1")
		var out []string
		for _, m := range s.Messages {
			out = append(out, m.Content)
		}
		return out
	}

	if code := patch("craig", `{"delete_at":1}`); code != http.StatusOK {
		t.Fatalf("delete one: %d", code)
	}
	if got := contents(); len(got) != 3 || got[1] != "and /home" {
		t.Fatalf("one message out, the rest kept: %v", got)
	}
	if code := patch("craig", `{"at":1}`); code != http.StatusOK {
		t.Fatalf("cut: %d", code)
	}
	if got := contents(); len(got) != 1 || got[0] != "how full is /var" {
		t.Fatalf("everything from the cut on is gone: %v", got)
	}
	if code := patch("dana", `{"at":0}`); code != http.StatusNotFound {
		t.Fatalf("another person's session is not theirs to cut: %d", code)
	}
	if got := contents(); len(got) != 1 {
		t.Fatalf("a refused cut changes nothing: %v", got)
	}
}

// A servitor session saved or deleted wakes its owner's open pages to re-read
// the Sessions rail (core/changes "sessions").

func TestServitorSessionsWakeTheirOwnersPages(t *testing.T) {
	udb := UserDB(&DBase{Store: kvlite.MemStore()}, "u-servitor")
	topic := changes.ForUser("sessions", "u-servitor")
	before, _ := changes.Version(topic)
	s := saveSession(udb, "box-1", chatSession{Name: "why?"})
	afterSave, _ := changes.Version(topic)
	if afterSave == before {
		t.Error("a save did not wake the owner's pages")
	}
	deleteSession(udb, "box-1", s.ID)
	if afterDelete, _ := changes.Version(topic); afterDelete == afterSave {
		t.Error("a delete did not wake the owner's pages")
	}
}
