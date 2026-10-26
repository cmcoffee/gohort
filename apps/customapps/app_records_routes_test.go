package customapps

// An app's records through its routes: each person who opens a shared app
// keeps their own, a record gets a key and a stamp when it is saved, reads
// back, and goes when deleted, and each of those is a change the page's long
// poll hears. Someone the app is shared with still needs the My Apps grant to
// open it, and the operator's allowlist reads back what was saved.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestAnAppsRecordsAreEachPersonsOwn(t *testing.T) {
	T := sharingTestApp(t)
	auth := &DBase{Store: kvlite.MemStore()}
	saved := AuthDB
	AuthDB = func() Database { return auth }
	t.Cleanup(func() { AuthDB = saved })
	for _, u := range []string{"alice", "bob", "carol"} {
		AuthSetUser(auth, u, "pw", false)
	}
	AuthSetUser(auth, "root", "pw", true)
	AuthSetUserApps(auth, "bob", []string{"/apps"})
	SaveAppSpec(AppSpec{Slug: "tally", Name: "Tally", Owner: "alice", RecordKey: "id", SharedWith: []string{"bob", "carol"}})
	SetSharedOwner(T.DB, sharedAppsIndex, "tally", "alice", true)
	h := routed(T)
	do := func(user, method, path, body string) (int, string) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(auth, user)})
		w := httptest.NewRecorder()
		h(w, r)
		return w.Code, w.Body.String()
	}
	version := func(user string) string {
		t.Helper()
		_, body := do(user, "GET", "/tally/changes", "")
		var v map[string]string
		json.Unmarshal([]byte(body), &v)
		return v["records"]
	}

	// carol is shared with but holds no My Apps grant.
	if code, _ := do("carol", "GET", "/tally/records", ""); code != http.StatusNotFound {
		t.Errorf("carol, with no grant, opened the app's records: %d", code)
	}

	before := version("bob")
	code, body := do("bob", "POST", "/tally/records", `{"name":"bobs"}`)
	var rec map[string]any
	json.Unmarshal([]byte(body), &rec)
	id, _ := rec["id"].(string)
	if code != 200 || id == "" || rec["created"] == nil {
		t.Fatalf("a new record: %d %s", code, body)
	}
	if v := version("bob"); v == before {
		t.Error("saving a record was not a change")
	}
	if _, body := do("alice", "GET", "/tally/records", ""); body != "[]\n" {
		t.Errorf("bob's record reached alice's store: %s", body)
	}
	if code, body := do("bob", "GET", "/tally/record?id="+id, ""); code != 200 || !strings.Contains(body, `"name":"bobs"`) {
		t.Errorf("reading it back: %d %s", code, body)
	}
	before = version("bob")
	if code, _ := do("bob", "DELETE", "/tally/record?id="+id, ""); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := do("bob", "GET", "/tally/record?id="+id, ""); code != http.StatusNotFound {
		t.Errorf("a deleted record still reads: %d", code)
	}
	if v := version("bob"); v == before {
		t.Error("deleting a record was not a change")
	}

	// The operator's allowlist reads back.
	if code, _ := do("root", "POST", "/_admin/reach?owner=alice&slug=tally", `{"users":["bob"]}`); code != 200 {
		t.Fatalf("allowlist save: %d", code)
	}
	if _, body := do("root", "GET", "/_admin/reach?owner=alice&slug=tally", ""); !strings.Contains(body, `"allowed":["bob"]`) {
		t.Errorf("allowlist read back: %s", body)
	}
}
