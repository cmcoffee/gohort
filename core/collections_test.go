package core

// What keeps a collection user-scoped is not a check anybody wrote; it is that
// no handler reads a scope off a request. See Collection.Scope.

import (
	"os"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/peershare"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A user CAN now ask for deployment scope, and that is the point: the third
// rung exists. What must hold is that asking is not the same as getting.
//
// This test replaced one that pinned the absence of any scope read at all.
// That was the right test while nothing could widen a collection and the wrong
// one the moment something could — a guard that forbids the mechanism rather
// than the outcome has to be rewritten to ship the feature, and a guard
// rewritten to ship a feature guards nothing. This one pins the outcome.
func TestWideningACollectionGoesThroughAnAdministrator(t *testing.T) {
	raw, err := os.ReadFile("../apps/orchestrate/collections.go")
	if err != nil {
		t.Fatalf("reading the handler: %v", err)
	}
	src := string(raw)
	// The request path exists...
	if !strings.Contains(src, "CreatePromotionRequest(AuthDB(), user, CollectionPromotionKind") {
		t.Error("widening no longer files a promotion request; a user could mint a deployment-wide collection")
	}
	// ...and the direct path is reachable only by an admin.
	if !strings.Contains(src, "if RequestIsAdmin(r) {") {
		t.Error("the direct promote is not gated on an admin")
	}
	// Only the owner decides either way.
	if !strings.Contains(src, `http.Error(w, "only the owner can change who this is shared with"`) {
		t.Error("a non-owner can change a collection's scope")
	}
	// And the approver is what actually promotes, so approving is the act.
	if !strings.Contains(string(mustRead(t, "collections.go")), "promotion.RegisterApprover(CollectionPromotionKind") {
		t.Error("nothing registers an approver, so an approved request would be refused as unsupported")
	}
}

func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return b
}

// Peer sharing on collections, end to end from the recipient's side.
//
// A recipient gets READ: it appears in their list, resolves by id, and searches
// through the shared VectorDB. They cannot delete it, because a share is not
// co-ownership and the owner's copy is the only copy.
func TestASharedCollectionReachesItsRecipient(t *testing.T) {
	savedRoot, savedVec := RootDB, VectorDB
	root := &DBase{Store: kvlite.MemStore()}
	RootDB, VectorDB = root, &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB, VectorDB = savedRoot, savedVec })

	owner := UserDB(CollectionsDB(), "alice")
	if owner == nil {
		t.Skip("no per-user store in this configuration")
	}
	c := Collection{ID: "col-1", Owner: "alice", Name: "Runbooks", AllowedUsers: []string{"bob"}}
	SaveCollection(owner, c)

	// Bob sees it and can resolve it.
	var found bool
	for _, got := range sharedCollectionsFor("bob") {
		if got.ID == "col-1" {
			found = true
		}
	}
	if !found {
		t.Fatal("the recipient cannot see what was shared with them")
	}
	if _, ok := LoadCollection(UserDB(CollectionsDB(), "bob"), "bob", "col-1"); !ok {
		t.Error("the recipient cannot resolve it by id, so they could not attach it")
	}
	// Somebody not named cannot.
	if got := sharedCollectionsFor("dana"); len(got) != 0 {
		t.Errorf("an unnamed user sees it: %+v", got)
	}

	// And a recipient cannot destroy the owner's documents.
	if n := DeleteCollection(UserDB(CollectionsDB(), "bob"), VectorDB, "bob", "col-1"); n != 0 {
		t.Error("a recipient deleted a collection they were only shared")
	}
	if _, ok := LoadCollection(owner, "alice", "col-1"); !ok {
		t.Fatal("the owner's collection is gone")
	}

	// Revoking takes it back on the next read, because the index follows the
	// record in the same write.
	c.AllowedUsers = nil
	SaveCollection(owner, c)
	if got := sharedCollectionsFor("bob"); len(got) != 0 {
		t.Errorf("a revoked recipient still has it: %+v", got)
	}
}

// Without a VectorDB a shared collection's chunks stay in the owner's own
// store, where a recipient's search cannot reach them. Offering it anyway would
// be offering something that attaches cleanly and never matches anything.
func TestSharingIsNotOfferedWhereItCouldNotSearch(t *testing.T) {
	savedRoot, savedVec := RootDB, VectorDB
	RootDB, VectorDB = &DBase{Store: kvlite.MemStore()}, nil
	t.Cleanup(func() { RootDB, VectorDB = savedRoot, savedVec })

	owner := UserDB(CollectionsDB(), "alice")
	if owner == nil {
		t.Skip("no per-user store in this configuration")
	}
	SaveCollection(owner, Collection{ID: "col-2", Owner: "alice", Name: "Runbooks", AllowedUsers: []string{"bob"}})
	if got := sharedCollectionsFor("bob"); len(got) != 0 {
		t.Errorf("a collection was offered that the recipient could not search: %+v", got)
	}
}

// Promotion MOVES the record: one copy, now read from the global pool. The
// chunks do not move at all, because they live in the shared VectorDB keyed by
// collection source, so the corpus is reachable the moment the scope changes.
func TestPromotingMovesTheRecordAndKeepsTheCorpus(t *testing.T) {
	savedRoot, savedVec := RootDB, VectorDB
	RootDB, VectorDB = &DBase{Store: kvlite.MemStore()}, &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB, VectorDB = savedRoot, savedVec })

	owner := UserDB(CollectionsDB(), "alice")
	if owner == nil {
		t.Skip("no per-user store in this configuration")
	}
	SaveCollection(owner, Collection{ID: "col-1", Owner: "alice", Name: "Runbooks", AllowedUsers: []string{"bob"}})

	if err := PromoteCollectionToDeployment("alice", "col-1"); err != nil {
		t.Fatalf("promote: %v", err)
	}
	// Anybody's agent can now attach it.
	if _, ok := LoadCollection(UserDB(CollectionsDB(), "dana"), "dana", "col-1"); !ok {
		t.Error("a deployment collection is not reachable by another user")
	}
	// One copy: the per-user row is gone, so an edit cannot land in a pool
	// nobody reads.
	var stale Collection
	if owner.Get(CollectionsTable, "col-1", &stale) && stale.ID != "" {
		t.Error("the per-user row survived, so there are now two copies")
	}
	// The owner is kept: it is who to ask about the contents.
	c, _ := LoadCollection(nil, "", "col-1")
	if c.Owner != "alice" {
		t.Errorf("the deployment copy has no owner: %+v", c)
	}
	// The peer shares go, because everybody has it: an ACL naming three people
	// decides nothing now, and leaving it would silently restore it on a later
	// narrowing.
	if got := peershare.List(RootDB, sharedCollectionsTable, "bob"); len(got) != 0 {
		t.Errorf("peer shares survived promotion: %+v", got)
	}

	// And narrowing is the owner's, taking it back to private with no
	// recipients rather than guessing which ones they meant.
	if err := NarrowCollectionToOwner("bob", "col-1"); err == nil {
		t.Error("a non-owner narrowed somebody else's collection")
	}
	if err := NarrowCollectionToOwner("alice", "col-1"); err != nil {
		t.Fatalf("narrow: %v", err)
	}
	if _, ok := LoadCollection(UserDB(CollectionsDB(), "dana"), "dana", "col-1"); ok {
		t.Error("it is still deployment-wide after narrowing")
	}
}
