package core

// What keeps a collection user-scoped is not a check anybody wrote; it is that
// no handler reads a scope off a request. See Collection.Scope.

import (
	"os"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// The comment on Collection.Scope used to say deployment scope was
// "admin-authored only at the HTTP layer". There was no such endpoint: it
// described a gate that had never been built, which is worse than describing
// none, because the next person to add a write path reads it and believes the
// check lives somewhere else.
//
// What actually holds it shut is that nothing reads a scope off a request. This
// pins that, so the claim and the code cannot drift apart again.
func TestNothingLetsAUserMintADeploymentCollection(t *testing.T) {
	for _, f := range []string{"../apps/orchestrate/collections.go"} {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		src := string(raw)
		// A create/update handler that started taking a scope from the caller
		// is the whole risk, and it would look innocuous in a diff.
		for _, leak := range []string{`body.Scope`, `Get("scope")`, `"scope"`} {
			if strings.Contains(src, leak) {
				t.Errorf("%s reads a scope from the request (%s); a user could mint a deployment-wide collection", f, leak)
			}
		}
	}
	// And the one that does exist is the framework's own, not somebody's.
	if DeploymentKnowledgeCollectionID == "" {
		t.Error("the framework's own deployment collection lost its id")
	}
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
	for _, got := range SharedCollectionsFor("bob") {
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
	if got := SharedCollectionsFor("dana"); len(got) != 0 {
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
	if got := SharedCollectionsFor("bob"); len(got) != 0 {
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
	if got := SharedCollectionsFor("bob"); len(got) != 0 {
		t.Errorf("a collection was offered that the recipient could not search: %+v", got)
	}
}
