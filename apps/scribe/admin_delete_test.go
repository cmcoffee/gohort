package scribe

// An administrator deleting somebody else's guide vacuumed the research
// collection of that id in the ADMINISTRATOR's collection store, which is the
// wrong one, and left the owner's behind with its chunks still in the index.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestDeletingAGuideRemovesTheOwnersCollection(t *testing.T) {
	prevRoot := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prevRoot })

	appDB := &DBase{Store: kvlite.MemStore()}
	T := &Scribe{AppCore: AppCore{DB: appDB}}

	ownerUDB := UserDB(appDB, "owner-user")
	g := saveGuide(ownerUDB, Guide{ID: "g1", Title: "Runbook", Owner: "owner-user"})
	SetSharedOwner(appDB, sharedGuidesIndex, g.ID, "owner-user", true)
	collID, _ := ensureGuideCollection(ownerUDB, "owner-user", g)
	ownerColls := UserDB(CollectionsDB(), "owner-user")
	if _, ok := LoadCollection(ownerColls, "owner-user", collID); !ok {
		t.Fatal("setup: the owner's research collection was not created")
	}

	// No auth store in a test, so RequestIsAdmin says yes: this is the
	// administrator path.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/scribe/guide?id="+g.ID, nil)
	T.handleGuide(w, r, UserDB(appDB, "admin-user"), "admin-user")
	if w.Code != http.StatusOK {
		t.Fatalf("delete answered %d: %s", w.Code, w.Body.String())
	}
	if _, ok := loadGuide(ownerUDB, g.ID); ok {
		t.Fatal("the guide is still there")
	}
	if _, ok := LoadCollection(ownerColls, "owner-user", collID); ok {
		t.Error("the owner's research collection outlived the guide it belonged to")
	}
}
