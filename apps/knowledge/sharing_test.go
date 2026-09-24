package knowledge

// The share editor is OWNER only, and gated by loading the record rather than
// by trusting the page. A recipient reaches the same URL — resolving a shared
// collection is the whole point — and showing them a share editor would offer a
// control the endpoint refuses. An absent section says "not yours" more
// honestly than a disabled one.

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func withStores(t *testing.T) {
	t.Helper()
	savedRoot, savedVec := RootDB, VectorDB
	RootDB, VectorDB = &DBase{Store: kvlite.MemStore()}, &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB, VectorDB = savedRoot, savedVec })
}

func TestOnlyTheOwnerSeesTheShareEditor(t *testing.T) {
	withStores(t)
	app := &KnowledgeApp{}
	owner := UserDB(CollectionsDB(), "alice")
	if owner == nil {
		t.Skip("no per-user store in this configuration")
	}
	SaveCollection(owner, Collection{ID: "col-1", Owner: "alice", Name: "Runbooks", AllowedUsers: []string{"bob"}})

	if _, ok := app.sharingSection("alice", "col-1"); !ok {
		t.Error("the owner cannot reach the share editor for their own collection")
	}
	// Bob resolves the collection (that is the share working) and still gets no
	// editor, because re-sharing somebody else's documents is not his to do.
	if _, ok := app.sharingSection("bob", "col-1"); ok {
		t.Error("a recipient is offered a control the endpoint would refuse")
	}
	// And a stranger, who cannot resolve it at all.
	if _, ok := app.sharingSection("dana", "col-1"); ok {
		t.Error("a stranger is offered the share editor")
	}
	if _, ok := app.sharingSection("alice", ""); ok {
		t.Error("a blank id produced a section")
	}
}

// The picker saves with PATCH, because the collections endpoint treats an
// absent field as unchanged: a POST of the whole record would carry back
// whatever else the picker happened to read.
func TestTheShareEditorPatches(t *testing.T) {
	withStores(t)
	app := &KnowledgeApp{}
	owner := UserDB(CollectionsDB(), "alice")
	if owner == nil {
		t.Skip("no per-user store")
	}
	SaveCollection(owner, Collection{ID: "col-1", Owner: "alice", Name: "Runbooks"})

	s, ok := app.sharingSection("alice", "col-1")
	if !ok {
		t.Fatal("no section")
	}
	// The component marshals to the JSON the runtime mounts, which is where the
	// method and the sources actually live.
	blob, err := json.Marshal(s.Body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rendered := strings.ToLower(string(blob))
	if !strings.Contains(rendered, "patch") {
		t.Errorf("the picker does not save with PATCH:\n%s", rendered)
	}
	// The candidate list is served by THIS app, so sharing does not 404 when a
	// sibling app is disabled.
	if !strings.Contains(rendered, "/knowledge/api/user-candidates") {
		t.Errorf("the candidate list is not this app's:\n%s", rendered)
	}
}

// The steward form is owner only for the same reason: a reader reaches this
// page, the steward endpoint refuses them, and the form then fails to load.
func TestOnlyTheOwnerSeesTheStewardForm(t *testing.T) {
	withStores(t)
	app := &KnowledgeApp{}
	owner := UserDB(CollectionsDB(), "alice")
	if owner == nil {
		t.Skip("no per-user store in this configuration")
	}
	SaveCollection(owner, Collection{ID: "col-1", Owner: "alice", Name: "Runbooks", AllowedUsers: []string{"bob"}})
	if _, ok := app.stewardSection("alice", "col-1"); !ok {
		t.Error("the owner cannot reach the steward form for their own collection")
	}
	if _, ok := app.stewardSection("bob", "col-1"); ok {
		t.Error("a reader is offered a form the endpoint refuses")
	}
}
