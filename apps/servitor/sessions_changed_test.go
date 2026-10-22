package servitor

// A servitor session saved or deleted wakes its owner's open pages to re-read
// the Sessions rail (core/changes "sessions").

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/changes"
	"github.com/cmcoffee/snugforge/kvlite"
)

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
