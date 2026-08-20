// Two editors write one store record: the form (name, path, retention…) and
// the assignment picker (allowed_users). Neither sends the other's fields.
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
		AllowedUsers: []string{"ana", "bo"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return app
}

// The failure this guards: drop allowed_users from the form and a save that
// decodes into a BLANK record erases an assignment the picker just made. The
// admin sees the store they edited, and the ACL they set five minutes ago is
// gone with nothing said.
func TestSavingTheFormKeepsAnAssignmentItDoesNotCarry(t *testing.T) {
	app := assignFixture(t)
	st, _ := LoadStore(app.DB, "support_bundles")

	// What the edit form posts: every field it holds, and not allowed_users.
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
	if strings.Join(after.AllowedUsers, ",") != "ana,bo" {
		t.Errorf("the form save erased an ACL it never edited: %v", after.AllowedUsers)
	}
	if after.Description != "one folder per ticket" || after.RetentionDays != 30 {
		t.Errorf("the fields the form DID carry should have landed: %+v", after)
	}
}

// And the reverse: the picker posts the record it read back with only
// allowed_users changed, which must not disturb anything else.
func TestTheAssignmentPickerChangesOnlyTheAssignment(t *testing.T) {
	app := assignFixture(t)
	st, _ := LoadStore(app.DB, "support_bundles")
	st.AllowedUsers = []string{"ana"}
	body, _ := json.Marshal(st)

	r := httptest.NewRequest("POST", "/filestore/api/stores", strings.NewReader(string(body)))
	app.handleStores(httptest.NewRecorder(), asStoreAdmin(t, r))

	after, _ := LoadStore(app.DB, "support_bundles")
	if strings.Join(after.AllowedUsers, ",") != "ana" {
		t.Errorf("the assignment should have changed: %v", after.AllowedUsers)
	}
	if after.Name != "Support bundles" || after.Path != st.Path {
		t.Errorf("nothing else should have moved: %+v", after)
	}
}

// Clearing has to be sayable. An EMPTY list is present in the body and means
// "everyone" — the one case a merge could wrongly read as "unchanged".
func TestUntickingTheLastUserClearsTheList(t *testing.T) {
	app := assignFixture(t)
	st, _ := LoadStore(app.DB, "support_bundles")
	body, _ := json.Marshal(map[string]any{
		"slug": st.Slug, "name": st.Name, "path": st.Path, "allowed_users": []string{},
	})
	r := httptest.NewRequest("POST", "/filestore/api/stores", strings.NewReader(string(body)))
	app.handleStores(httptest.NewRecorder(), asStoreAdmin(t, r))

	after, _ := LoadStore(app.DB, "support_bundles")
	if len(after.AllowedUsers) != 0 {
		t.Errorf("an empty list means every user, and must survive the merge: %v", after.AllowedUsers)
	}
	if !after.AllowsUser("anyone") {
		t.Error("with nobody assigned, everybody reaches it")
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
	r := httptest.NewRequest("POST", "/filestore/api/actions?slug=support_bundles", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	app.handleActions(w, asStoreAdmin(t, r))
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
	r2 := httptest.NewRequest("POST", "/filestore/api/actions?slug=support_bundles", strings.NewReader(string(other)))
	app.handleActions(httptest.NewRecorder(), asStoreAdmin(t, r2))
	if got := actionsOf(app, "support_bundles"); len(got) != 2 {
		t.Errorf("the URL's store should have taken it, got %d action(s)", len(got))
	}
}

// actionsOf is the store's actions, since the lister is deployment-wide.
func actionsOf(app *FileStoreApp, slug string) []StoreAction {
	var out []StoreAction
	for _, a := range ListStoreActions(app.DB) {
		if a.Slug == slug {
			out = append(out, a)
		}
	}
	return out
}
