package servitor

import (
	"encoding/json"
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

// What the Servitor page gets back from each JSON endpoint, through the real
// routes, as the systems' owner (alice), a user a system is shared with (bob),
// a user it is not (dana), an administrator (root) and nobody: by the wrong
// method, and with no body, a broken one or a missing field. The endpoints that
// start real work (mapping a command over SSH, re-cloning a repository, making
// an agent) are driven down their refusals, which is where the checks are.
// Compared with testdata/routes_contract.golden, so a change that moves who may
// read or change a system shows up as a diff.
//
// Run with UPDATE_GOLDEN=1 to rewrite the golden after a change that is meant.
func TestRoutesContract(t *testing.T) {
	auth := &DBase{Store: kvlite.MemStore()}
	auth.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	auth.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	auth.Set(AuthTable, "user:dana", AuthUser{Username: "dana"})
	auth.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prevAuth := AuthDB
	AuthDB = func() Database { return auth }
	t.Cleanup(func() { AuthDB = prevAuth })

	app := &Servitor{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	udb := UserDB(app.DB, "alice")
	box := Appliance{ID: "box", Name: "Lab Box", Type: "ssh", Owner: "alice", Profile: "a lab machine",
		Password: "pw-secret", EnvVars: []string{"TOKEN=tok-secret"}, AllowedUsers: []string{"bob"}}
	udb.Set(applianceTable, box.ID, box)
	app.indexNamedShares("alice", box.ID, box.AllowedUsers)
	// The guide target is a process-wide registry another test in this package
	// fills; this one starts from an empty one of its own.
	RegisterDocumentTarget(&fakeGuides{refs: map[string][]string{}, titles: map[string]string{}, appended: map[string]int{}})
	mux := http.NewServeMux()
	app.routes(mux)

	var log strings.Builder
	guideFlag := regexp.MustCompile(`"guide":(true|false)`)
	uuid := regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	stamp := regexp.MustCompile(`\d{4}-\d\d-\d\d[T ]\d\d:\d\d(:\d\d(\.\d+)?)?(Z|[+-]\d\d:\d\d)?`)
	do := func(user, method, path, body string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if user != "" {
			r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(auth, user)})
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		out := strings.TrimSpace(w.Body.String())
		// The password never leaves the server, and the env values only go to
		// the systems' owner, on her own edit form.
		if strings.Contains(out, "pw-secret") || (user != "alice" && strings.Contains(out, "tok-secret")) {
			t.Errorf("%s %s %s: a stored secret reached the reply: %s", user, method, path, out)
		}
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
		// Whether a guide target is registered is the process's, not this
		// endpoint's: the test registers its own, so it always reads true.
		out = guideFlag.ReplaceAllString(out, `"guide":<registered>`)
		out = stamp.ReplaceAllString(out, "<time>")
		who := user
		if who == "" {
			who = "(signed out)"
		}
		fmt.Fprintf(&log, "%s %s %s %q\n  %d %s\n", who, method, path, body, w.Code, out)
	}

	for _, p := range []string{"/api/user-candidates", "/api/access-agents", "/api/appliance-tools", "/api/collections", "/api/save_destinations", "/api/peer-appliances", "/api/permissions/always", "/api/command-grants"} {
		do("", "GET", p, "")
		do("alice", "GET", p, "")
		do("alice", "PUT", p, "{}")
	}

	for _, u := range []string{"alice", "bob", "dana"} {
		do(u, "GET", "/api/profile?appliance_id=box", "")
		do(u, "GET", "/api/appliance/box", "")
		do(u, "GET", "/api/rules?appliance_id=box", "")
		do(u, "GET", "/api/appliance-peers?appliance_id=box", "")
		do(u, "GET", "/api/bindable-tools?appliance_id=box", "")
		do(u, "GET", "/api/bundle/move-targets?appliance_id=box", "")
		do(u, "GET", "/api/push-to-guide?appliance_id=box", "")
	}
	do("alice", "GET", "/api/profile", "")
	do("alice", "GET", "/api/appliance/", "")
	do("alice", "GET", "/api/rules", "")
	do("alice", "PUT", "/api/appliance/box", "{}")
	do("alice", "POST", "/api/bindable-tools?appliance_id=box", "{}")

	do("alice", "GET", "/api/facts", "")
	do("alice", "GET", "/api/facts?id=box", "")
	do("alice", "POST", "/api/facts", `{"appliance_id":"box","key":"os","value":"linux"}`)
	do("alice", "POST", "/api/facts", `{"appliance_id":"nope","key":"os","value":"linux"}`)
	do("alice", "POST", "/api/facts", `{"appliance_id":"box"}`)
	do("alice", "POST", "/api/facts", `{"appliance_id":`)
	do("alice", "GET", "/api/facts?id=box", "")
	do("alice", "DELETE", "/api/facts", "")
	do("alice", "DELETE", "/api/facts?key=nope", "")
	do("alice", "PUT", "/api/facts", "{}")

	do("alice", "POST", "/api/rules?appliance_id=box", `{"appliance_id":"box","rule":"never reboot"}`)
	do("bob", "POST", "/api/rules", `{"appliance_id":"box","rule":"mine now"}`)
	do("dana", "POST", "/api/rules", `{"appliance_id":"box","rule":"x"}`)
	do("root", "POST", "/api/rules", `{"appliance_id":"box","rule":"admin rule"}`)
	do("alice", "POST", "/api/rules", `{"appliance_id":"box"}`)
	do("alice", "POST", "/api/rules", `{"appliance_id":`)
	do("alice", "PUT", "/api/rules", "{}")
	do("bob", "DELETE", "/api/rules/box:nope", "")
	do("dana", "DELETE", "/api/rules/box:nope", "")
	do("alice", "DELETE", "/api/rules/box:nope", "")
	do("alice", "DELETE", "/api/rules/", "")
	do("alice", "GET", "/api/rules/box:nope", "")

	do("alice", "GET", "/api/permissions", "")
	do("alice", "POST", "/api/permissions", `{}`)
	do("alice", "POST", "/api/permissions", `{"read":`)
	do("alice", "PUT", "/api/permissions", "{}")
	do("alice", "POST", "/api/permissions/always", `{"appliance_id":"box"}`)
	do("alice", "POST", "/api/permissions/always", `{"appliance_id":"box","command":"ls","rule":"ls -l"}`)
	do("alice", "POST", "/api/permissions/always", `{"appliance_id":"box","command":"ls"}`)
	do("alice", "POST", "/api/permissions/always", `{"appliance_id":`)

	do("alice", "POST", "/api/command-grants", `{}`)
	do("alice", "POST", "/api/command-grants", `{"agent_id":"a1"}`)
	do("alice", "POST", "/api/command-grants", `{"agent_id":"a1","appliance_id":"box"}`)
	do("alice", "GET", "/api/command-grants", "")
	do("alice", "DELETE", "/api/command-grants", "")
	do("alice", "DELETE", "/api/command-grants?agent_id=a1&appliance_id=box", "")
	do("alice", "POST", "/api/command-grants", `{"agent_id":`)

	do("alice", "POST", "/api/appliance-tool", "")
	do("alice", "POST", "/api/appliance-tool?appliance=box&name=x&approved=1", "")
	do("alice", "DELETE", "/api/appliance-tool?appliance=box&name=x", "")
	do("alice", "GET", "/api/appliance-tool?appliance=box&name=x", "")

	do("alice", "GET", "/api/sessions", "")
	do("alice", "GET", "/api/sessions?appliance_id=box", "")
	do("alice", "POST", "/api/sessions?appliance_id=box", "{}")
	do("alice", "GET", "/api/sessions/s1?appliance_id=box", "")
	do("alice", "GET", "/api/sessions/s1/export?appliance_id=box", "")
	do("alice", "PATCH", "/api/sessions/s1?appliance_id=box", `{"at":0}`)
	do("alice", "PATCH", "/api/sessions/s1?appliance_id=box", `{"at":`)
	do("alice", "DELETE", "/api/sessions/s1?appliance_id=box", "")
	do("alice", "GET", "/api/sessions/s1", "")
	do("alice", "PUT", "/api/sessions/s1?appliance_id=box", "{}")

	do("bob", "POST", "/api/appliance-peers", `{"appliance_id":"box","key_id":"k1","granted":true}`)
	do("dana", "POST", "/api/appliance-peers", `{"appliance_id":"box","key_id":"k1","granted":true}`)
	do("alice", "POST", "/api/appliance-peers", `{"appliance_id":"box","key_id":"k1","granted":true}`)
	do("alice", "POST", "/api/appliance-peers", `{"appliance_id":`)
	do("alice", "DELETE", "/api/appliance-peers", "")

	do("alice", "POST", "/api/save_article", `{"text":"# Findings\nall good"}`)
	do("alice", "POST", "/api/save_article", `{}`)
	do("alice", "GET", "/api/save_article", "")
	do("alice", "POST", "/api/save_snippet", `{"text":"ls -l"}`)
	do("alice", "GET", "/api/save_snippet", "")

	do("alice", "POST", "/api/push-to-guide", `{}`)
	do("alice", "POST", "/api/push-to-guide", `{"text":"x"}`)
	do("alice", "POST", "/api/push-to-guide", `{"text":"x","guide_id":"g1","appliance_id":"box"}`)
	do("alice", "POST", "/api/push-to-guide", `{"text":"x","guide_id":"g1","appliance_id":"box","link":true}`)
	do("dana", "POST", "/api/push-to-guide", `{"text":"x","new_title":"Mine","appliance_id":"box"}`)
	do("alice", "POST", "/api/push-to-guide", `{"text":`)
	do("alice", "PUT", "/api/push-to-guide", "{}")

	for _, p := range []string{"/api/repo/refresh", "/api/bundle/ingest", "/api/workspace/agent", "/api/mapapp"} {
		do("alice", "GET", p, "")
		do("alice", "POST", p, `{}`)
		do("alice", "POST", p, `{"appliance_id":`)
		do("dana", "POST", p, `{"appliance_id":"box","command":"ls"}`)
		if p != "/api/mapapp" { // a shared user may map; that starts a real SSH session
			do("bob", "POST", p, `{"appliance_id":"box","command":"ls"}`)
		}
		do("alice", "POST", p, `{"appliance_id":"nope","command":"ls"}`)
	}
	do("alice", "POST", "/api/repo/refresh", `{"appliance_id":"box"}`)
	do("alice", "POST", "/api/bundle/ingest", `{"appliance_id":"box"}`)
	do("alice", "POST", "/api/workspace/agent", `{"appliance_id":"box"}`)
	do("alice", "GET", "/api/bundle/move?appliance_id=box", "")
	do("dana", "GET", "/api/bundle/move?appliance_id=box", "")

	do("bob", "POST", "/api/memory/clear", `{"appliance_id":"box"}`)
	do("dana", "POST", "/api/memory/clear", `{"appliance_id":"box"}`)
	do("alice", "POST", "/api/memory/clear", `{}`)
	do("alice", "POST", "/api/memory/clear", `{"appliance_id":`)
	do("alice", "GET", "/api/memory/clear", "")
	do("alice", "POST", "/api/memory/clear", `{"appliance_id":"box"}`)

	do("bob", "DELETE", "/api/appliance/box", "")
	do("dana", "DELETE", "/api/appliance/box", "")
	do("alice", "DELETE", "/api/appliance/box", "")
	do("alice", "GET", "/api/appliance/box", "")

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
