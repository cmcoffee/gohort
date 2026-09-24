package orchestrate

// Collection names are not unique across owners: a user reads their own, the
// ones colleagues shared with them, and the deployment's, and any of those can
// be called "Legal". What these hold is that a name never quietly resolves to
// somebody else's, and that a user cannot make two of their own they could not
// tell apart.

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// collectionNameStores pins RootDB and VectorDB (sharing and the list's chunk
// walk read both) and returns alice's tool session and carol's store.
func collectionNameStores(t *testing.T) (*ToolSession, Database) {
	t.Helper()
	savedRoot, savedVec := RootDB, VectorDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	VectorDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB, VectorDB = savedRoot, savedVec })
	alice := UserDB(CollectionsDB(), "alice")
	carol := UserDB(CollectionsDB(), "carol")
	if alice == nil || carol == nil {
		t.Fatal("no per-user store")
	}
	return &ToolSession{Username: "alice", DB: alice}, carol
}

func collectionsAction(t *testing.T, name string) *GroupedToolAction {
	t.Helper()
	a, ok := collectionsListTool().(*GroupedTool).Action(name)
	if !ok {
		t.Fatalf("collections tool has no %q action", name)
	}
	return &a
}

// TestCollectionsListSaysWhoseEachIs is the model choosing an id by name. Three
// "Legal"s: alice's, carol's shared with her, the deployment's. The list used
// to carry id, name and description only, sorted by last update, so the model
// took the first "Legal" and attached whoever's had been touched last.
func TestCollectionsListSaysWhoseEachIs(t *testing.T) {
	sess, carol := collectionNameStores(t)
	saveCollection(sess.DB, Collection{ID: "a-legal", Owner: "alice", Name: "Legal"})
	saveCollection(carol, Collection{ID: "c-legal", Owner: "carol", Name: "legal", AllowedUsers: []string{"alice"}})
	RootDB.Set(GlobalCollectionsTable, "d-legal", Collection{ID: "d-legal", Name: "Legal", Scope: CollectionScopeDeployment})
	saveCollection(sess.DB, Collection{ID: "a-runbooks", Owner: "alice", Name: "Runbooks"})

	out, err := collectionsAction(t, "list").Handler(map[string]any{}, sess)
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		ID           string   `json:"id"`
		Access       string   `json:"access"`
		NameAlsoUsed []string `json:"name_also_used"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("list output: %v\n%s", err, out)
	}
	want := map[string]string{"a-legal": "yours", "c-legal": "shared by carol", "d-legal": "deployment", "a-runbooks": "yours"}
	for _, r := range rows {
		if r.Access != want[r.ID] {
			t.Errorf("%s: access %q, want %q", r.ID, r.Access, want[r.ID])
		}
		switch r.ID {
		case "a-runbooks":
			if len(r.NameAlsoUsed) != 0 {
				t.Errorf("a unique name was flagged as repeated: %v", r.NameAlsoUsed)
			}
		default:
			if len(r.NameAlsoUsed) != 2 {
				t.Errorf("%s: the repeated name must list the other two, got %v", r.ID, r.NameAlsoUsed)
			}
		}
	}
	if len(rows) != 4 {
		t.Fatalf("expected 4 rows, got %s", out)
	}
}

// TestASecondOwnCollectionOfTheSameNameIsRefused covers create and rename, on
// the tool and on the helper the HTTP create and PATCH call. A colleague's or
// the deployment's name does not take it from the user.
func TestASecondOwnCollectionOfTheSameNameIsRefused(t *testing.T) {
	sess, carol := collectionNameStores(t)
	saveCollection(sess.DB, Collection{ID: "a-legal", Owner: "alice", Name: "Legal"})
	saveCollection(carol, Collection{ID: "c-hr", Owner: "carol", Name: "HR", AllowedUsers: []string{"alice"}})

	create := collectionsAction(t, "create")
	_, err := create.Handler(map[string]any{"name": " legal "}, sess)
	if err == nil || !strings.Contains(err.Error(), "a-legal") {
		t.Fatalf("a duplicate own name must be refused, pointing at the existing id: %v", err)
	}
	if why := duplicateCollectionName(sess.DB, "alice", "LEGAL", ""); why == "" {
		t.Error("the HTTP create path would accept a duplicate own name")
	}
	// Somebody else's name is free to use.
	if _, err := create.Handler(map[string]any{"name": "HR"}, sess); err != nil {
		t.Errorf("a name only a colleague's collection has was refused: %v", err)
	}
	// Renaming onto an own name is the same pair; keeping one's own name is not.
	if _, err := collectionsAction(t, "update").Handler(map[string]any{"id": "a-legal", "name": "hr"}, sess); err == nil {
		t.Error("a rename onto another own collection's name was accepted")
	}
	if why := duplicateCollectionName(sess.DB, "alice", "Legal", "a-legal"); why != "" {
		t.Errorf("a collection keeping its own name was refused: %s", why)
	}
}
