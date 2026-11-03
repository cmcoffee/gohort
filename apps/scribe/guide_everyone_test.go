package scribe

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/shareledger"
)

// A guide shared with everyone is on the administrator's list of what is
// shared with everyone, and taking it back takes Everyone away while the
// people it is shared with by name keep it.
func TestAnEveryoneGuideIsListedAndTakenBack(t *testing.T) {
	T, appDB := shareFixture(t)
	if c := share(t, T, "alice", `{"allowed_users":["bob"]}`); c != 200 {
		t.Fatalf("share answered %d", c)
	}
	if err := T.approveEveryone("alice", "g1:"+guideLevelEdit); err != nil {
		t.Fatal(err)
	}
	listed := false
	for _, g := range shareledger.Published() {
		if g.Kind == guidePromotionKind && g.ID == "g1" && g.Owner == "alice" && g.Reach == "Everyone can edit" {
			listed = true
		}
	}
	if !listed {
		t.Fatalf("not listed: %+v", shareledger.Published())
	}
	if err := shareledger.Unpublish(guidePromotionKind, "alice", "g1"); err != nil {
		t.Fatal(err)
	}
	if g, _ := loadGuide(UserDB(appDB, "alice"), "g1"); g.Shared {
		t.Fatal("Everyone is still on")
	}
	if reach(T, "dan") != "" {
		t.Error("everyone still reaches it")
	}
	if reach(T, "bob") == "" {
		t.Error("taking Everyone back took bob's share too")
	}
}
