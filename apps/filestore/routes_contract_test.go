package filestore

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// What the Files pages and an administrator's browser get back from each JSON
// endpoint, through the real routes: signed out, as a user, as an
// administrator, by the wrong method, and with no body, a broken one or one
// that names something that is not there. Compared with
// testdata/routes_contract.golden, so a change to how a handler is written that
// moves a status, a reply, or an admin or Full Access check shows up as a diff.
//
// Run with UPDATE_GOLDEN=1 to rewrite the golden after a change that is meant.
func TestRoutesContract(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prevAuth := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prevAuth })

	full, readonly, newRoot := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(full, "bundle-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin, _ := fakeCommand(t, t.TempDir())
	app := &FileStoreApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	if _, err := SaveStore(app.DB, Store{Name: "Bundles", Path: full, Members: []string{accessEveryone}, FullAccess: []string{accessEveryone}}); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveStore(app.DB, Store{Name: "Archive", Path: readonly, Members: []string{"alice"}}); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"bundles", "archive"} {
		if _, err := SaveStoreCommand(app.DB, StoreCommand{Slug: slug, Name: "decrypt", Command: bin, TwoPhase: true, InputLabel: "Response key"}); err != nil {
			t.Fatal(err)
		}
	}
	mux := routed(app)

	var log strings.Builder
	hex := regexp.MustCompile(`\b[0-9a-f]{12,48}\b`)
	stamp := regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?(Z|[+-]\d\d:\d\d)`)
	paths := strings.NewReplacer(full, "<full>", readonly, "<readonly>", newRoot, "<new>", bin, "<bin>")
	do := func(user, method, path, body string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if user != "" {
			r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(adb, user)})
		}
		w := httptest.NewRecorder()
		mux(w, r)
		out := strings.TrimSpace(w.Body.String())
		var items []json.RawMessage
		if strings.HasPrefix(out, "[") && json.Unmarshal([]byte(out), &items) == nil {
			rows := make([]string, len(items))
			for i, it := range items {
				rows[i] = string(it)
			}
			sort.Strings(rows)
			out = "[" + strings.Join(rows, ",") + "]"
		}
		out = paths.Replace(out)
		out = hex.ReplaceAllString(out, "<hex>")
		out = stamp.ReplaceAllString(out, "<time>")
		who := user
		if who == "" {
			who = "(signed out)"
		}
		fmt.Fprintf(&log, "%s %s %s %q\n  %d %s\n", who, method, paths.Replace(path), paths.Replace(body), w.Code, out)
	}

	for _, p := range []string{"/api/store-options", "/api/app-options", "/api/access-candidates"} {
		do("", "GET", p, "")
		do("alice", "GET", p, "")
		do("root", "GET", p, "")
		do("root", "POST", p, "{}")
	}

	do("alice", "GET", "/api/folders?slug=bundles", "")
	do("alice", "GET", "/api/folders?slug=nope", "")
	do("root", "POST", "/api/folders?slug=bundles", "{}")

	do("alice", "GET", "/api/stores", "")
	do("root", "GET", "/api/stores", "")
	do("root", "GET", "/api/stores?slug=bundles", "")
	do("root", "GET", "/api/stores?slug=bundles&view=form", "")
	do("root", "GET", "/api/stores?slug=bundles&view=reach", "")
	do("root", "GET", "/api/stores?slug=bundles&view=about", "")
	do("root", "GET", "/api/stores?slug=nope", "")
	do("root", "POST", "/api/stores", `{"name":"Fresh","path":"`+newRoot+`","members":["alice"]}`)
	do("root", "POST", "/api/stores", `{"name":`)
	do("root", "POST", "/api/stores", ``)
	do("root", "POST", "/api/stores", `{"name":"Nowhere","path":"/no/such/dir"}`)
	do("root", "PATCH", "/api/stores?slug=nope", `{"slug":"nope","name":"x"}`)
	do("root", "PATCH", "/api/stores?slug=fresh", `{"slug":"fresh","name":"Fresher"}`)
	do("alice", "POST", "/api/stores", `{"name":"Mine","path":"`+newRoot+`"}`)
	do("root", "PUT", "/api/stores?slug=fresh", "{}")
	do("root", "DELETE", "/api/stores?slug=fresh", "")

	do("alice", "GET", "/api/commands", "")
	do("alice", "GET", "/api/commands?slug=archive", "")
	do("alice", "POST", "/api/commands?slug=bundles", `{"name":"x","command":"`+bin+`"}`)
	do("root", "POST", "/api/commands?slug=bundles", `{"name":"inspect","command":"`+bin+`","allowed_uses":"list\nshow"}`)
	do("root", "POST", "/api/commands?slug=bundles", `{"name":`)
	do("root", "POST", "/api/commands?slug=bundles", `{"name":"bad"}`)
	do("root", "DELETE", "/api/commands?id=bundles/inspect", "")
	do("root", "PUT", "/api/commands", "{}")

	do("alice", "POST", "/api/commands/run?slug=bundles&command=decrypt&within=bundle-1", "")
	do("alice", "POST", "/api/commands/run?slug=bundles&command=decrypt&within=bundle-1", `{"input":"ab12"}`)
	do("alice", "POST", "/api/commands/run?slug=bundles&command=decrypt&within=bundle-1", `{"input":"-x"}`)
	do("alice", "POST", "/api/commands/run?slug=bundles&command=decrypt&within=bundle-1", `{"input":`)
	do("alice", "POST", "/api/commands/run?slug=bundles&command=nope", "")
	do("alice", "POST", "/api/commands/run?slug=archive&command=decrypt", "")
	do("alice", "POST", "/api/commands/run?slug=nope&command=decrypt", "")
	do("alice", "POST", "/api/commands/run?slug=bundles&command=decrypt&within=../..", "")
	do("alice", "GET", "/api/commands/run?slug=bundles&command=decrypt", "")

	for _, p := range []string{"/api/commands/approve", "/api/commands/background"} {
		do("alice", "POST", p+"?id=bundles/decrypt", `{}`)
		do("root", "GET", p+"?id=bundles/decrypt", "")
		do("root", "POST", p+"?id=bundles/decrypt", `{"approved":true,"background_on":true}`)
		do("root", "POST", p+"?id=bundles/nope", `{}`)
		do("root", "POST", p+"?id=bundles/decrypt", `{"approved":`)
	}

	do("alice", "GET", "/api/commands/uses?id=bundles/decrypt", "")
	do("root", "GET", "/api/commands/uses?id=bundles/decrypt", "")
	do("root", "GET", "/api/commands/uses?id=bundles/nope", "")
	do("root", "POST", "/api/commands/uses?id=bundles/decrypt", `{"allowed_uses":"list\nshow","guide":"how","background":"always"}`)
	do("root", "POST", "/api/commands/uses?id=bundles/decrypt", `{"allowed_uses":"list; rm -rf"}`)
	do("root", "POST", "/api/commands/uses?id=bundles/decrypt", `{"allowed_uses":`)
	do("root", "PUT", "/api/commands/uses?id=bundles/decrypt", "{}")

	do("alice", "GET", "/api/map/try-args?id=bundles/decrypt", "")
	do("root", "GET", "/api/map/try-args?id=bundles/decrypt", "")
	do("root", "GET", "/api/map/try-args?id=bundles/nope", "")
	do("alice", "GET", "/api/commands/mapping?id=bundles/decrypt", "")
	do("root", "GET", "/api/commands/mapping?id=bundles/decrypt", "")
	do("root", "GET", "/api/commands/mapping?id=bundles/nope", "")

	do("alice", "GET", "/api/map/examples?id=bundles/decrypt", "")
	do("root", "GET", "/api/map/examples?id=bundles/decrypt", "")
	do("root", "GET", "/api/map/examples?id=bundles/nope", "")
	do("root", "GET", "/api/map/examples?id=bundles/decrypt&eid=nope", "")
	do("root", "PATCH", "/api/map/examples?id=bundles/decrypt&eid=nope", `{"note":"x"}`)
	do("root", "DELETE", "/api/map/examples?id=bundles/decrypt&eid=nope", "")
	do("root", "PUT", "/api/map/examples?id=bundles/decrypt", "{}")

	for _, base := range []string{"/api/pull", "/api/evidence/"} {
		sep := "/"
		if strings.HasSuffix(base, "/") {
			sep = ""
		}
		do("alice", "GET", base+sep+"items?slug=bundles", "")
		do("root", "GET", base+sep+"items?slug=bundles", "")
		do("root", "GET", base+sep+"items?slug=nope", "")
		do("root", "GET", base+sep+"progress?slug=bundles&item=bundle-1", "")
		do("root", "POST", base+sep+"cancel?slug=bundles&item=bundle-1", "")
		do("root", "POST", base+"?slug=bundles&item=nothing-here", "")
		do("root", "GET", base+"?slug=bundles&item=bundle-1", "")
	}

	do("alice", "GET", "/api/app-grants?slug=bundles", "")
	do("root", "GET", "/api/app-grants?slug=bundles", "")
	do("root", "GET", "/api/app-grants?slug=nope", "")
	do("root", "POST", "/api/app-grants?slug=bundles", `{"app":"alice/swing","read":true}`)
	do("root", "POST", "/api/app-grants?slug=bundles", `{"app":"no-owner"}`)
	do("root", "POST", "/api/app-grants?slug=bundles", `{"app":`)
	do("root", "GET", "/api/app-grants?slug=bundles", "")
	do("root", "DELETE", "/api/app-grants?slug=bundles&app=alice/swing", "")
	do("root", "PUT", "/api/app-grants?slug=bundles", "{}")

	got := log.String()
	const golden = "testdata/routes_contract.golden"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read %s: %v (run with UPDATE_GOLDEN=1 to create it)", golden, err)
	}
	if got != string(want) {
		gl, wl := strings.Split(got, "\n"), strings.Split(string(want), "\n")
		for i := 0; i < len(gl) || i < len(wl); i++ {
			var g, w string
			if i < len(gl) {
				g = gl[i]
			}
			if i < len(wl) {
				w = wl[i]
			}
			if g != w {
				t.Fatalf("reply differs from %s at line %d:\n got: %s\nwant: %s", golden, i+1, g, w)
			}
		}
	}
}
