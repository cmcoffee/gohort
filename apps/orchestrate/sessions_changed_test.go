package orchestrate

// A session saved, renamed, read or deleted wakes its owner's open pages to
// re-read their session lists (core/changes "sessions"), and nobody else's.

import (
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/changes"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestSessionWritesWakeTheirOwnersPages(t *testing.T) {
	base := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(base, "u-sess")
	mine, others := changes.ForUser("sessions", "u-sess"), changes.ForUser("sessions", "someone-else")
	moved := func(what string, do func()) {
		t.Helper()
		before, _ := changes.Version(mine)
		other, _ := changes.Version(others)
		do()
		if after, _ := changes.Version(mine); after == before {
			t.Errorf("%s did not wake the owner's pages", what)
		}
		if o, _ := changes.Version(others); o != other {
			t.Errorf("%s woke someone else's", what)
		}
	}
	var s ChatSession
	moved("a save", func() {
		var err error
		s, err = saveChatSession(udb, ChatSession{AgentID: "a1", Title: "t"})
		if err != nil {
			t.Fatal(err)
		}
	})
	moved("a rename", func() { renameChatSession(udb, "a1", s.ID, "renamed") })
	s.LastAt = time.Now().Add(time.Hour) // unread
	udb.Set(sessionTable("a1"), s.ID, s)
	moved("reading it", func() { markSessionSeen(udb, "a1", s.ID) })
	moved("a delete", func() { deleteChatSession(udb, "a1", s.ID) })
}
