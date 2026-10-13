package servitor

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
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
