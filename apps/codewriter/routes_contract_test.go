package codewriter

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// What the code editor panel gets back from each JSON endpoint, through the
// real routes: signed out, as the records' owner, as another user, by the
// wrong method, and with no body, a broken one or a missing field. Compared
// with testdata/routes_contract.golden, so a change to how a handler is
// written that moves a status, a reply, or whose records a user can reach
// shows up as a diff.
//
// Run with UPDATE_GOLDEN=1 to rewrite the golden after a change that is meant.
func TestRoutesContract(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	prevAuth := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prevAuth })

	fake := &FakeLLM{Turns: []FakeTurn{{Content: "`hello_world`"}, {Err: errors.New("provider at 10.0.0.5 refused: quota")}}}
	T := &CodeWriterAgent{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}, LLM: fake}}
	udb := UserDB(T.DB, "alice")
	udb.Set(snippetTable, "snip-1", SnippetRecord{ID: "snip-1", Name: "hello", Lang: "go", Code: "package main", Date: "2026-01-01T00:00:00Z"})
	udb.Set(valueTable, "val-1", SavedValue{ID: "val-1", Name: "token", Value: "abc", Date: "2026-01-01T00:00:00Z"})
	udb.Set(contextTable, "ctx-1", ContextRecord{ID: "ctx-1", Name: "style", Body: "tabs", Date: "2026-01-01T00:00:00Z"})
	udb.Set(writerTable, "wr-1", WriterRecord{ID: "wr-1", Name: "terse", Date: "2026-01-01T00:00:00Z"})
	udb.Set(templateTable, "tpl-1", TemplateRecord{ID: "tpl-1", Name: "cli", Body: "func main() {}", Date: "2026-01-01T00:00:00Z"})
	mux := http.NewServeMux()
	T.RegisterRoutes(mux, "")

	var log strings.Builder
	uuid := regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	stamp := regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?(Z|[+-]\d\d:\d\d)`)
	do := func(user, method, path, body string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if user != "" {
			r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(adb, user)})
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
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
		out = uuid.ReplaceAllString(out, "<uuid>")
		out = stamp.ReplaceAllString(out, "<time>")
		who := user
		if who == "" {
			who = "(signed out)"
		}
		fmt.Fprintf(&log, "%s %s %s %q\n  %d %s\n", who, method, path, body, w.Code, out)
	}

	for _, p := range []string{"/api/snippets", "/api/values", "/api/contexts", "/api/writers", "/api/templates", "/api/collections", "/api/reference-sources"} {
		do("", "GET", p, "")
		do("alice", "GET", p, "")
		do("bob", "GET", p, "")
		do("alice", "PUT", p, "{}")
	}
	do("alice", "POST", "/api/collections", "{}")
	do("alice", "POST", "/api/reference-sources", "{}")

	for _, one := range []struct{ path, id string }{
		{"/api/snippet/", "snip-1"}, {"/api/value/", "val-1"}, {"/api/context/", "ctx-1"},
		{"/api/writer/", "wr-1"}, {"/api/template/", "tpl-1"},
	} {
		do("", "GET", one.path+one.id, "")
		do("alice", "GET", one.path+one.id, "")
		do("bob", "GET", one.path+one.id, "")
		do("alice", "GET", one.path+"nope", "")
		do("alice", "GET", one.path, "")
		do("alice", "PUT", one.path+one.id, "{}")
		do("bob", "DELETE", one.path+one.id, "")
		do("alice", "GET", one.path+one.id, "")
		do("alice", "DELETE", one.path, "")
	}

	do("alice", "POST", "/api/snippets", `{"id":"snip-1","name":"hello","lang":"go","code":"package main // v2"}`)
	do("alice", "POST", "/api/snippets", `{"name":"fresh","lang":"py","code":"print(1)"}`)
	do("alice", "POST", "/api/snippets", `{"name":"no code"}`)
	do("alice", "POST", "/api/snippets", `{"name":`)
	do("alice", "POST", "/api/snippets", ``)
	do("alice", "GET", "/api/revisions/snip-1", "")
	do("bob", "GET", "/api/revisions/snip-1", "")
	do("alice", "GET", "/api/revisions/", "")
	do("alice", "GET", "/api/revision/nope", "")
	do("alice", "GET", "/api/revision/", "")
	do("alice", "POST", "/api/revisions/snip-1", "{}")

	do("alice", "POST", "/api/values", `{"name":"key","value":"v"}`)
	do("alice", "POST", "/api/values", `{"value":"v"}`)
	do("alice", "POST", "/api/values", `{"name":`)
	do("alice", "POST", "/api/contexts", `{"name":"tone","body":"plain"}`)
	do("alice", "POST", "/api/contexts", `{"body":"plain"}`)
	do("alice", "POST", "/api/contexts", ``)
	do("alice", "POST", "/api/writers", `{"name":"wordy"}`)
	do("alice", "POST", "/api/writers", `{"name":"  "}`)
	do("alice", "POST", "/api/writers", `{"name":`)
	do("alice", "POST", "/api/templates", `{"name":"cli","body":"func main() { run() }"}`)
	do("alice", "POST", "/api/templates", `{"name":"web"}`)
	do("alice", "POST", "/api/templates", `{"body":"x"}`)
	do("alice", "POST", "/api/templates", `{"name":`)
	do("alice", "GET", "/api/templates", "")

	do("", "POST", "/api/suggest-name", `{"code":"print(1)","lang":"python"}`)
	do("alice", "POST", "/api/suggest-name", `{"code":"print(1)","lang":"python"}`)
	do("alice", "POST", "/api/suggest-name", `{"code":"print(2)"}`)
	do("alice", "POST", "/api/suggest-name", `{}`)
	do("alice", "POST", "/api/suggest-name", `{"code":`)
	do("alice", "GET", "/api/suggest-name", "")

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
