package orchestrate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Every collection route takes the methods named here and no other, refuses
// a caller who is not signed in, and puts the one write gate in front of
// what it does: a reader the collection was shared with may look but not
// add, a contributor may add but not rename, a stranger finds nothing.
//
// Only reads and requests that are refused are sent, so nothing here
// ingests, researches or calls a model.
func TestEveryCollectionRouteTakesItsMethodsAndKeepsTheGate(t *testing.T) {
	prevRoot, prevAuth, prevBase, prevVec := RootDB, AuthDB, orchestrateBaseDB, VectorDB
	t.Cleanup(func() { RootDB, AuthDB, orchestrateBaseDB, VectorDB = prevRoot, prevAuth, prevBase, prevVec })
	RootDB = &DBase{Store: kvlite.MemStore()}
	orchestrateBaseDB = RootDB
	VectorDB = &DBase{Store: kvlite.MemStore()}
	signedUp(t, "alice", "bob", "carol", "dave")
	saveCollection(UserDB(CollectionsDB(), "alice"), Collection{ID: "col-a", Owner: "alice", Name: "Notes",
		AllowedUsers: []string{"bob", "carol"}, Contributors: []string{"carol"}})
	h := routed(&OrchestrateApp{AppCore: AppCore{DB: CollectionsDB()}})
	do := func(user, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if user != "" {
			r = withSession(r, user)
		}
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}

	takes := map[string]string{
		"":                    "GET PATCH DELETE",
		"steward":             "GET POST",
		"paste":               "POST",
		"sources":             "GET",
		"sources/x":           "DELETE",
		"search":              "GET",
		"autofill":            "POST",
		"research":            "POST",
		"audit":               "POST",
		"suggest-description": "POST",
	}
	for action, methods := range takes {
		path := "/api/collections/col-a"
		if action != "" {
			path += "/" + action
		}
		for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
			if !strings.Contains(" "+methods+" ", " "+m+" ") {
				if w := do("alice", m, path, "{}"); w.Code != http.StatusMethodNotAllowed {
					t.Errorf("%s %s answered %d, want 405", m, path, w.Code)
				}
				continue
			}
			if w := do("", m, path, "{}"); w.Code != http.StatusUnauthorized {
				t.Errorf("signed out, %s %s answered %d, want 401", m, path, w.Code)
			}
			// A stranger finds no such collection.
			if w := do("dave", m, path, "{}"); w.Code != http.StatusNotFound {
				t.Errorf("dave, %s %s answered %d, want 404", m, path, w.Code)
			}
		}
	}
	for _, action := range []string{"upload", "export"} {
		if w := do("", "GET", "/api/collections/col-a/"+action, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("signed out, GET %s answered %d, want 401", action, w.Code)
		}
	}
	// The reader looks.
	for _, path := range []string{"/api/collections/col-a", "/api/collections/col-a/sources"} {
		if w := do("bob", "GET", path, ""); w.Code != http.StatusOK {
			t.Errorf("bob, a reader, GET %s answered %d %s", path, w.Code, w.Body.String())
		}
	}
	if w := do("bob", "GET", "/api/collections/col-a/export", ""); w.Code != http.StatusOK {
		t.Errorf("bob, a reader, could not export: %d %s", w.Code, w.Body.String())
	}
	// The reader does not add, and the contributor gets past the gate to the
	// paste's own refusal of an empty document.
	paste := `{"title":"t","text":""}`
	if w := do("bob", "POST", "/api/collections/col-a/paste", paste); w.Code != http.StatusForbidden {
		t.Errorf("bob, a reader, pasted: %d %s", w.Code, w.Body.String())
	}
	if w := do("carol", "POST", "/api/collections/col-a/paste", paste); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "too short") {
		t.Errorf("carol, a contributor, was stopped before the paste: %d %s", w.Code, w.Body.String())
	}
	if w := do("bob", "POST", "/api/collections/col-a/upload", `{"name":""}`); w.Code != http.StatusForbidden {
		t.Errorf("bob, a reader, uploaded: %d %s", w.Code, w.Body.String())
	}
	// Neither of them renames it, re-scopes it, deletes from it or names its
	// curator.
	for _, u := range []string{"bob", "carol"} {
		for _, c := range []struct{ method, path, body string }{
			{"PATCH", "/api/collections/col-a", `{"name":"Mine now"}`},
			{"DELETE", "/api/collections/col-a", ""},
			{"DELETE", "/api/collections/col-a/sources/x", ""},
			{"POST", "/api/collections/col-a/steward", `{"curator_agent":"x"}`},
		} {
			if w := do(u, c.method, c.path, c.body); w.Code != http.StatusForbidden {
				t.Errorf("%s %s %s answered %d, want 403", u, c.method, c.path, w.Code)
			}
		}
	}
	if got, ok := loadCollection(UserDB(CollectionsDB(), "alice"), "alice", "col-a"); !ok || got.Name != "Notes" {
		t.Errorf("the collection changed: %+v %v", got, ok)
	}
}

// Asking for deployment scope is not getting it. The owner's request is filed
// for an administrator and the collection stays theirs; an administrator's
// own promotes at once; nobody but the owner asks.
func TestWideningACollectionGoesThroughAnAdministrator(t *testing.T) {
	prevRoot, prevAuth, prevBase, prevVec := RootDB, AuthDB, orchestrateBaseDB, VectorDB
	t.Cleanup(func() { RootDB, AuthDB, orchestrateBaseDB, VectorDB = prevRoot, prevAuth, prevBase, prevVec })
	RootDB = &DBase{Store: kvlite.MemStore()}
	orchestrateBaseDB = RootDB
	VectorDB = &DBase{Store: kvlite.MemStore()}
	signedUp(t, "alice", "carol")
	AuthSetUser(AuthDB(), "root", "pw", true)
	saveCollection(UserDB(CollectionsDB(), "alice"), Collection{ID: "col-a", Owner: "alice", Name: "Notes",
		AllowedUsers: []string{"carol"}, Contributors: []string{"carol"}})
	saveCollection(UserDB(CollectionsDB(), "root"), Collection{ID: "col-r", Owner: "root", Name: "Shared"})
	h := routed(&OrchestrateApp{AppCore: AppCore{DB: CollectionsDB()}})
	widen := func(user, id string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h(w, withSession(httptest.NewRequest("PATCH", "/api/collections/"+id, strings.NewReader(`{"scope":"deployment"}`)), user))
		return w
	}
	if w := widen("carol", "col-a"); w.Code != http.StatusForbidden {
		t.Errorf("a contributor changed the scope: %d %s", w.Code, w.Body.String())
	}
	if w := widen("alice", "col-a"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"requested":true`) {
		t.Errorf("the owner's ask: %d %s", w.Code, w.Body.String())
	}
	if c, ok := loadCollection(UserDB(CollectionsDB(), "alice"), "alice", "col-a"); !ok || IsDeploymentScope(c) {
		t.Errorf("asking promoted the collection: %+v %v", c, ok)
	}
	if w := widen("root", "col-r"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"requested":false`) {
		t.Errorf("an administrator's own: %d %s", w.Code, w.Body.String())
	}
}

// Sharing a collection used to hand over write access nobody granted.
//
// The route gate resolves with LoadCollection, which admits the owner, anybody
// it was shared with, and every user when it is deployment-wide. That is right
// for reading, and it was being used for everything: a recipient could upload
// into somebody else's corpus, run an autofill or a research pass into it, or
// rename and re-scope it. It mattered more than an extra file — an agent takes
// its collections as ground truth, so whoever can write to one decides what
// every agent reading it believes.

func TestARecipientMayReadACollectionAndNothingElse(t *testing.T) {
	c := Collection{ID: "col-1", Owner: "alice", Name: "Runbooks",
		AllowedUsers: []string{"bob"}}

	for _, action := range []string{"search", "export"} {
		if why := collectionWriteRefusal(c, "bob", action, http.MethodPost, false); why != "" {
			t.Errorf("a reader was refused %q: %s", action, why)
		}
	}
	if why := collectionWriteRefusal(c, "bob", "", http.MethodGet, false); why != "" {
		t.Errorf("a reader was refused the record itself: %s", why)
	}

	// Everything that changes the corpus, or the record around it.
	for _, action := range []string{"upload", "paste", "sources", "autofill", "research", "steward"} {
		why := collectionWriteRefusal(c, "bob", action, http.MethodPost, false)
		if why == "" {
			t.Errorf("a recipient could still %s into somebody else's collection", action)
			continue
		}
		// The refusal says whose it is and what to do instead, or the next
		// thing somebody tries is the same call with a different spelling.
		if !strings.Contains(why, "alice") || !strings.Contains(why, "copy") {
			t.Errorf("the refusal for %q does not explain itself: %s", action, why)
		}
	}
	if collectionWriteRefusal(c, "bob", "", http.MethodPost, false) == "" {
		t.Error("a recipient could rename or re-scope somebody else's collection")
	}
}

// An action nobody has written yet is a write until its author says otherwise.
// The read list is short and explicit; the default is the safe direction.
func TestAnUnknownActionIsTreatedAsAWrite(t *testing.T) {
	c := Collection{ID: "col-1", Owner: "alice", AllowedUsers: []string{"bob"}}
	if collectionWriteRefusal(c, "bob", "some-new-thing", http.MethodPost, false) == "" {
		t.Error("an unrecognised action defaulted to allowed")
	}
}

// The owner may do anything, and a collection with no owner at all — a record
// from before ownership, or the framework's own — is nobody's to be refused.
func TestTheOwnerIsNeverRefused(t *testing.T) {
	c := Collection{ID: "col-1", Owner: "alice"}
	for _, action := range []string{"upload", "research", ""} {
		if why := collectionWriteRefusal(c, "alice", action, http.MethodPost, false); why != "" {
			t.Errorf("the owner was refused %q: %s", action, why)
		}
	}
	if why := collectionWriteRefusal(Collection{ID: "old"}, "bob", "upload", http.MethodPost, false); why != "" {
		t.Errorf("an unowned collection refused somebody: %s", why)
	}
}

// A deployment-wide collection is readable by everybody and writable by its
// author. Publishing it widened who could CHANGE it, which is the same hole
// with a larger blast radius.
func TestPublishingACollectionDoesNotHandOutWrites(t *testing.T) {
	c := Collection{ID: "col-1", Owner: "alice", Name: "Handbook",
		Scope: CollectionScopeDeployment}
	if why := collectionWriteRefusal(c, "dana", "search", http.MethodPost, false); why != "" {
		t.Errorf("a deployment collection refused a reader: %s", why)
	}
	if collectionWriteRefusal(c, "dana", "upload", http.MethodPost, false) == "" {
		t.Error("anybody in the deployment could write to a published collection")
	}
}

// A contributor may ADD to the corpus, and nothing more. Somebody trusted to
// put a document in has not thereby been handed the collection.
func TestAContributorAddsButDoesNotOwn(t *testing.T) {
	c := Collection{ID: "col-1", Owner: "alice", Name: "Runbooks",
		AllowedUsers: []string{"bob", "carol"}, Contributors: []string{"bob"}}

	for _, action := range []string{"upload", "paste", "sources", "autofill", "research"} {
		if why := collectionWriteRefusal(c, "bob", action, http.MethodPost, false); why != "" {
			t.Errorf("a contributor was refused %q: %s", action, why)
		}
	}
	// Not the collection itself: renaming, re-scoping, re-sharing.
	why := collectionWriteRefusal(c, "bob", "", http.MethodPost, false)
	if why == "" {
		t.Error("a contributor could rename or re-scope the collection")
	} else if !strings.Contains(why, "add to it") {
		t.Errorf("the refusal does not distinguish adding from owning: %s", why)
	}
	// Reorganising what is already there is a different trust from adding to
	// it: the curator can move or drop what its owner put in.
	if collectionWriteRefusal(c, "bob", "steward", http.MethodPost, false) == "" {
		t.Error("a contributor could turn the curator loose on somebody else's corpus")
	}
	// And a reader who is not a contributor still only reads.
	if collectionWriteRefusal(c, "carol", "upload", http.MethodPost, false) == "" {
		t.Error("a plain recipient could upload")
	}
}

// Contributors are a subset of the people it is shared with. Somebody who
// cannot see what is already in a corpus would be writing into it blind, and a
// list that outlived the share would be waiting to hand write back.
func TestAContributorMustBeSomebodyItIsSharedWith(t *testing.T) {
	if got := keepOnly([]string{"bob", "dana"}, []string{"bob"}); len(got) != 1 || got[0] != "bob" {
		t.Errorf("the contributor list kept somebody it is not shared with: %+v", got)
	}
	if got := keepOnly([]string{"dana"}, []string{"bob"}); got != nil {
		t.Errorf("an entirely stale list survived: %+v", got)
	}
}

// Nobody holds this by default. Before the write gate every recipient could
// write because nothing checked, which is not the same as having been given it.
func TestContributorsAreEmptyByDefault(t *testing.T) {
	c := Collection{ID: "col-1", Owner: "alice", AllowedUsers: []string{"bob"}}
	if CollectionContributor(c, "bob") {
		t.Error("a plain share made somebody a contributor")
	}
	if !CollectionContributor(c, "alice") {
		t.Error("the owner is not a contributor to their own collection")
	}
}

// The deployment's own collection is everybody's to read and an
// administrator's to change: every agent without curated collections takes it
// as ground truth.
func TestTheDeploymentKnowledgeIsAnAdministratorsToChange(t *testing.T) {
	c := Collection{ID: DeploymentKnowledgeCollectionID, Name: "Deployment Knowledge", Scope: CollectionScopeDeployment}
	if why := collectionWriteRefusal(c, "bob", "search", http.MethodPost, false); why != "" {
		t.Errorf("a user could not search it: %s", why)
	}
	for _, action := range []string{"upload", "paste", "remove_doc", ""} {
		if collectionWriteRefusal(c, "bob", action, http.MethodPost, false) == "" {
			t.Errorf("a user could %q the deployment's knowledge", action)
		}
		if why := collectionWriteRefusal(c, "root", action, http.MethodPost, true); why != "" {
			t.Errorf("an admin was refused %q: %s", action, why)
		}
	}
	if collectionWriteRefusal(c, "bob", "", http.MethodDelete, false) == "" {
		t.Error("a user could delete the deployment's knowledge")
	}
}

// A reader is handed the collection's page, and that page lists the documents
// with a GET of "sources". It was gated as a corpus write, so every reader who
// was not also a contributor saw a shared collection as empty. Listing is a
// read; removing one document (sources/<id>) still is not.
func TestAReaderCanListTheDocumentsInASharedCollection(t *testing.T) {
	c := Collection{ID: "col-1", Owner: "alice", Name: "Runbooks",
		AllowedUsers: []string{"bob"}}
	if why := collectionWriteRefusal(c, "bob", "sources", http.MethodGet, false); why != "" {
		t.Errorf("a reader could not list the documents: %s", why)
	}
	if collectionWriteRefusal(c, "bob", "sources/doc-1", http.MethodDelete, false) == "" {
		t.Error("a reader could remove a document from somebody else's collection")
	}
	dk := Collection{ID: DeploymentKnowledgeCollectionID, Name: "Deployment Knowledge", Scope: CollectionScopeDeployment}
	if why := collectionWriteRefusal(dk, "bob", "sources", http.MethodGet, false); why != "" {
		t.Errorf("a user could not list the deployment's knowledge: %s", why)
	}
}

// Collection names are not unique across owners: a user reads their own, the
// ones colleagues shared with them, and the deployment's, and any of those can
// be called "Legal". What these hold is that a name never quietly resolves to
// somebody else's, and that a user cannot make two of their own they could not
// tell apart.

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

// Collections belong to the people who make them, so the routes that serve
// them are not part of the admin workbench.
//
// They were. apps/knowledge is where an ordinary person manages theirs, every
// call it makes lands on these routes, and the gate meant a non-admin opening
// Knowledge got "Agents is admin-only" and an empty page. The gate protected
// nothing: each handler already resolves by the session user, and what they
// may see and change is decided by LoadCollection and collectionWriteRefusal
// underneath.

func TestTheCollectionRoutesAreNotAdminGated(t *testing.T) {
	// alice is an ordinary user, not an administrator.
	app, udb, user := newTestOrchestrate(t)
	saveCollection(udb, Collection{ID: "col-1", Owner: user, Name: "Notes"})
	for _, path := range []string{"/api/collections", "/api/collections/col-1", "/api/collections/col-1/sources"} {
		w := httptest.NewRecorder()
		routed(app)(w, asUser(httptest.NewRequest(http.MethodGet, path, nil), user))
		if w.Code != http.StatusOK {
			t.Errorf("a non-admin's GET %s answered %d %s", path, w.Code, w.Body.String())
		}
	}
	// The agent routes came out from behind the gate too, later and for the
	// same reason: a user owns their agents as they own their collections.
	// What the gate is FOR now is the console — one administrator's view
	// across every user — which keeps it route by route in console.go.
	console, err := os.ReadFile("console.go")
	if err != nil {
		t.Fatalf("reading the console routes: %v", err)
	}
	if !strings.Contains(string(console), "T.adminOpt()") {
		t.Error("the console gave up its admin gate; that is the half the gate is for")
	}
}
