package orchestrate

import (
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Dismissing a card takes it out of the stored conversation, so a reload does
// not bring it back, and leaves the others where they were.
func TestDismissRemovesTheCard(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	T := &OrchestrateApp{AppCore: AppCore{DB: root}}
	db := UserDB(root, "u")
	seedBlockSession(t, db, "ag", "s1",
		UIBlock{Type: "link_hint", ID: "link-1", Title: "My app", URL: "/apps/x/"},
		UIBlock{Type: "credential_setup", ID: "cred-1", Title: "acme"},
	)
	if code := resolveBlock(t, T, db, "ag", "s1", "link-1", `{"dismiss":true}`); code != 204 {
		t.Fatalf("dismiss: %d", code)
	}
	s, _ := loadChatSession(db, "ag", "s1")
	if len(s.UIBlocks) != 1 || s.UIBlocks[0].ID != "cred-1" || s.UIBlocks[0].Resolved != "" {
		t.Fatalf("after dismiss: %+v", s.UIBlocks)
	}
	// Again, or for a card already gone: quietly nothing.
	if code := resolveBlock(t, T, db, "ag", "s1", "link-1", `{"dismiss":true}`); code != 204 {
		t.Fatalf("second dismiss: %d", code)
	}
}

// A card keeps the time it was first drawn, so a reload puts it back where
// it appeared; re-announcing it (show_link to the same page) keeps that time.
func TestACardKeepsItsPlace(t *testing.T) {
	s := &ChatSession{}
	s.upsert_ui_block(UIBlock{Type: "link_hint", ID: "link-1", URL: "/apps/x/", Title: "My app"}, nil)
	first := s.UIBlocks[0].At
	if first.IsZero() {
		t.Fatal("a new card has no time to be put back at")
	}
	time.Sleep(5 * time.Millisecond)
	same := func(b *UIBlock) bool { return b.URL == "/apps/x/" }
	s.upsert_ui_block(UIBlock{Type: "link_hint", ID: "link-1", URL: "/apps/x/", Title: "My app, updated"}, same)
	if len(s.UIBlocks) != 1 || s.UIBlocks[0].Title != "My app, updated" || !s.UIBlocks[0].At.Equal(first) {
		t.Errorf("an update moved the card: %+v (first drawn %v)", s.UIBlocks, first)
	}
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s.upsert_ui_block(UIBlock{Type: "orchestrate_plan", ID: "plan-1", At: at}, nil)
	if !s.UIBlocks[1].At.Equal(at) {
		t.Errorf("a caller's own time was replaced: %v", s.UIBlocks[1].At)
	}
}
