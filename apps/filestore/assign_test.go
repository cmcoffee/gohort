// Two editors write one store record: the form (name, path, retention…) and
// the access picker (members, full_access). Neither sends the other's fields.
package filestore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// asStoreAdmin is asAdmin with the Admin bit set: every endpoint here is
// admin-only, which is the point of a store being a deployment decision.
func asStoreAdmin(t *testing.T, r *http.Request) *http.Request {
	t.Helper()
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev })
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(adb, "root")})
	return r
}

func assignFixture(t *testing.T) *FileStoreApp {
	t.Helper()
	app := &FileStoreApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	dir := t.TempDir()
	if _, err := SaveStore(app.DB, Store{Name: "Support bundles", Path: dir,
		Members: []string{"ana", "bo"}, FullAccess: []string{"ana", "bo"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return app
}

// The failure this guards: drop full_access from the form and a save that
// decodes into a BLANK record erases an assignment the picker just made. The
// admin sees the store they edited, and the ACL they set five minutes ago is
// gone with nothing said.
func TestSavingTheFormKeepsAnAssignmentItDoesNotCarry(t *testing.T) {
	app := assignFixture(t)
	st, _ := LoadStore(app.DB, "support_bundles")

	// What the edit form posts: every field it holds, and not full_access.
	body, _ := json.Marshal(map[string]any{
		"slug": st.Slug, "name": "Support bundles", "path": st.Path,
		"description": "one folder per ticket", "retention_days": 30,
	})
	r := httptest.NewRequest("POST", "/filestore/api/stores", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	app.handleStores(w, asStoreAdmin(t, r))
	if w.Code != http.StatusOK {
		t.Fatalf("save failed: %d %s", w.Code, w.Body.String())
	}

	after, _ := LoadStore(app.DB, "support_bundles")
	if strings.Join(after.FullAccess, ",") != "ana,bo" {
		t.Errorf("the form save erased an ACL it never edited: %v", after.FullAccess)
	}
	if after.Description != "one folder per ticket" || after.RetentionDays != 30 {
		t.Errorf("the fields the form DID carry should have landed: %+v", after)
	}
}

// And the reverse: the picker posts the record it read back with only
// full_access changed, which must not disturb anything else.
func TestTheAssignmentPickerChangesOnlyTheAssignment(t *testing.T) {
	app := assignFixture(t)
	st, _ := LoadStore(app.DB, "support_bundles")
	st.FullAccess = []string{"ana"}
	body, _ := json.Marshal(st)

	r := httptest.NewRequest("POST", "/filestore/api/stores", strings.NewReader(string(body)))
	app.handleStores(httptest.NewRecorder(), asStoreAdmin(t, r))

	after, _ := LoadStore(app.DB, "support_bundles")
	if strings.Join(after.FullAccess, ",") != "ana" {
		t.Errorf("the assignment should have changed: %v", after.FullAccess)
	}
	if after.Name != "Support bundles" || after.Path != st.Path {
		t.Errorf("nothing else should have moved: %+v", after)
	}
}

// Clearing has to be sayable. An EMPTY list is present in the body and means
// nobody: the one case a merge could wrongly read as "unchanged".
func TestUntickingTheLastUserClearsTheList(t *testing.T) {
	app := assignFixture(t)
	st, _ := LoadStore(app.DB, "support_bundles")
	body, _ := json.Marshal(map[string]any{
		"slug": st.Slug, "name": st.Name, "path": st.Path, "members": []string{}, "full_access": []string{},
	})
	r := httptest.NewRequest("POST", "/filestore/api/stores", strings.NewReader(string(body)))
	app.handleStores(httptest.NewRecorder(), asStoreAdmin(t, r))

	after, _ := LoadStore(app.DB, "support_bundles")
	if len(after.Members) != 0 || len(after.FullAccess) != 0 {
		t.Errorf("an emptied list must survive the merge: %v %v", after.Members, after.FullAccess)
	}
	if after.AllowsUser("ana") || after.AllowsUser("anyone") {
		t.Error("with nobody assigned, somebody still reaches it")
	}
}

// An action is added from the store's own row, so the handle comes from the
// row rather than being read off another column and retyped. The URL is what
// says which store; a body that disagrees loses.
func TestAnActionTakesItsStoreFromTheURL(t *testing.T) {
	app := assignFixture(t)
	body, _ := json.Marshal(map[string]any{
		// No slug in the body at all — the form no longer asks for one.
		"name": "decrypt", "label": "Decrypt bundle", "command": "/bin/true",
	})
	r := httptest.NewRequest("POST", "/filestore/api/commands?slug=support_bundles", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	app.handleCommands(w, asStoreAdmin(t, r))
	if w.Code != http.StatusOK {
		t.Fatalf("save failed: %d %s", w.Code, w.Body.String())
	}
	acts := actionsOf(app, "support_bundles")
	if len(acts) != 1 || acts[0].Name != "decrypt" {
		t.Fatalf("the action should be filed under the store from the URL: %+v", acts)
	}

	// And a body naming a different store does not win: the row somebody
	// clicked is the more explicit statement of which store this is for.
	other, _ := json.Marshal(map[string]any{
		"slug": "somewhere_else", "name": "redact", "label": "Redact", "command": "/bin/true",
	})
	r2 := httptest.NewRequest("POST", "/filestore/api/commands?slug=support_bundles", strings.NewReader(string(other)))
	app.handleCommands(httptest.NewRecorder(), asStoreAdmin(t, r2))
	if got := actionsOf(app, "support_bundles"); len(got) != 2 {
		t.Errorf("the URL's store should have taken it, got %d action(s)", len(got))
	}
}

// actionsOf is the store's actions, since the lister is deployment-wide.
func actionsOf(app *FileStoreApp, slug string) []StoreCommand {
	var out []StoreCommand
	for _, a := range ListStoreCommands(app.DB) {
		if a.Slug == slug {
			out = append(out, a)
		}
	}
	return out
}

// The page as it runs: the settings form loads a view without the list, the
// picker PATCHes the list alone, and Save posts back everything the form
// loaded. Neither undoes the other.
func TestTheSettingsFormAndThePickerDoNotUndoEachOther(t *testing.T) {
	app := assignFixture(t)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		w := httptest.NewRecorder()
		app.handleStores(w, asStoreAdmin(t, r))
		return w
	}
	w := call("GET", "/filestore/api/stores?slug=support_bundles&view=form", "")
	var form map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &form); err != nil {
		t.Fatalf("form load: %d %s", w.Code, w.Body.String())
	}
	if _, has := form["members"]; has {
		t.Fatal("the form's load carries members, so Save would write it back stale")
	}
	if _, has := form["full_access"]; has {
		t.Fatal("the form's load carries full_access, so Save would write it back stale")
	}
	if w := call("PATCH", "/filestore/api/stores?slug=support_bundles", `{"full_access":["ana"]}`); w.Code != http.StatusOK {
		t.Fatalf("picker: %d %s", w.Code, w.Body.String())
	}
	form["description"] = "edited after the chip"
	body, _ := json.Marshal(form)
	if w := call("POST", "/filestore/api/stores", string(body)); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	after, _ := LoadStore(app.DB, "support_bundles")
	if strings.Join(after.FullAccess, ",") != "ana" {
		t.Errorf("Save reverted the picker: %v", after.FullAccess)
	}
	if after.Description != "edited after the chip" {
		t.Errorf("the form's edit should land: %q", after.Description)
	}
	if w := call("PATCH", "/filestore/api/stores?slug=nope", `{"full_access":["ana"]}`); w.Code != http.StatusNotFound {
		t.Errorf("a PATCH must not create a store: %d", w.Code)
	}
}

// The access picker offers Everyone first, then the admin's own account,
// marked, then the rest; adding someone starts them Read-Only, and pressing
// their level gives Full Access.
func TestTheAccessPickerOffersEveryoneThenYourAccount(t *testing.T) {
	app := assignFixture(t)
	r := asStoreAdmin(t, httptest.NewRequest("GET", "/filestore/api/access-candidates", nil))
	w := httptest.NewRecorder()
	app.handleAccessCandidates(w, r)
	var got []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got) < 2 {
		t.Fatalf("candidates: %d %s", w.Code, w.Body.String())
	}
	if got[0]["value"] != accessEveryone || got[1]["value"] != "root" || got[1]["label"] != "root (your account)" {
		t.Errorf("first two candidates: %v, %v", got[0], got[1])
	}
	// People start with Full Access when added; Everyone does not.
	if got[0]["full_default"] == true || got[1]["full_default"] != true {
		t.Errorf("starting levels: Everyone %v, a person %v", got[0]["full_default"], got[1]["full_default"])
	}
	call := func(body string) {
		w := httptest.NewRecorder()
		app.handleStores(w, asStoreAdmin(t, httptest.NewRequest("PATCH", "/filestore/api/stores?slug=support_bundles", strings.NewReader(body))))
		if w.Code != http.StatusOK {
			t.Fatalf("patch: %d %s", w.Code, w.Body.String())
		}
	}
	// What the picker sends: the members, and the ones flagged Full Access.
	call(`{"members":["ana","bo","*"],"full_access":["ana","bo"]}`)
	after, _ := LoadStore(app.DB, "support_bundles")
	if !after.FullAccessFor("ana") || !after.AllowsUser("carol") || after.FullAccessFor("carol") {
		t.Errorf("Everyone Read-Only beside ana and bo at Full Access: %+v", after)
	}
	call(`{"members":["ana","*"],"full_access":["ana"]}`)
	if after, _ := LoadStore(app.DB, "support_bundles"); after.FullAccessFor("bo") || !after.AllowsUser("bo") {
		t.Errorf("bo removed by name should still read through Everyone, at Read-Only: %+v", after)
	}
}

// A store nobody can reach says so: a warning on its Manage panel, and a
// mark on its row, both gone once somebody is on it.
func TestAStoreNobodyCanReachIsMarked(t *testing.T) {
	app := assignFixture(t)
	get := func(path string) string {
		w := httptest.NewRecorder()
		app.handleStores(w, asStoreAdmin(t, httptest.NewRequest("GET", path, nil)))
		return w.Body.String()
	}
	if body := get("/filestore/api/stores?slug=support_bundles&view=reach"); strings.Contains(body, "Nobody") {
		t.Errorf("a store with members warned: %s", body)
	}
	st, _ := LoadStore(app.DB, "support_bundles")
	st.Members, st.FullAccess = nil, nil
	SaveStore(app.DB, st)
	if body := get("/filestore/api/stores?slug=support_bundles&view=reach"); !strings.Contains(body, "Nobody can reach this store yet") {
		t.Errorf("no warning on an empty store: %s", body)
	}
	if body := get("/filestore/api/stores"); !strings.Contains(body, `"unreachable":true`) {
		t.Errorf("the row is not marked: %s", body)
	}
}
