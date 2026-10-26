package admin

import (
	"crypto/sha1"
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

// What the admin pages get back from the JSON endpoints built on jsonapi,
// through the real routes and the gate in front of them: signed out, as a user
// who is not an administrator, as one who is, by the wrong method and with a
// missing field; and on an install with no accounts yet, which the gate opens
// to whoever reaches it. Compared with testdata/routes_contract.golden, so a
// change that moves who reaches an admin endpoint shows up as a diff.
//
// Run with UPDATE_GOLDEN=1 to rewrite the golden after a change that is meant.
func TestRoutesContract(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prevAuth := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prevAuth })

	ct := &contract{}
	run := ct.run

	app := &AdminApp{}
	mux := http.NewServeMux()
	app.RegisterRoutes(mux, "")
	do := func(user, method, path, body string) { run(mux, adb, user, method, path, body) }

	reads := []string{
		"/api/app-panels?path=/servitor", "/api/app-summary?path=/nope", "/api/tool-templates",
		"/api/embeddings/models", "/api/connector-templates", "/api/connector-templates?category=none",
		"/api/extensions", "/api/tool-template?name=nope", "/api/connector-template?name=nope",
		"/api/extension-template?name=nope", "/api/db/tables", "/api/db/keys?table=auth", "/api/db/keys",
		"/api/activity", "/api/activity?limit=2", "/api/user-agents", "/api/user-machines", "/api/user-pipelines",
		"/api/user-credentials", "/api/personal-mcp-servers", "/api/personal-integrations", "/api/user-candidates",
	}
	for _, p := range reads {
		do("", "GET", p, "")
		do("alice", "GET", p, "")
		do("root", "GET", p, "")
		do("root", "PUT", p, "{}")
	}

	for _, p := range []string{"/api/user-agents", "/api/user-machines", "/api/user-pipelines"} {
		do("alice", "POST", p+"?owner=bob&id=x&action=revoke_share", "")
		do("root", "POST", p, "")
		do("root", "POST", p+"?owner=bob&id=x&action=bogus", "")
		do("root", "POST", p+"?owner=bob&id=x&action=revoke_share", "")
	}
	do("root", "POST", "/api/user-agents?owner=bob&id=x&action=publish", "")
	do("alice", "POST", "/api/user-credentials?owner=bob&name=k&action=disable", "")
	do("root", "POST", "/api/user-credentials", "")
	do("root", "POST", "/api/user-credentials?owner=bob&name=k&action=bogus", "")
	do("root", "POST", "/api/user-credentials?owner=bob&name=k&action=disable", "")
	do("root", "DELETE", "/api/user-credentials", "")
	do("alice", "DELETE", "/api/user-credentials?owner=bob&name=k", "")
	do("root", "POST", "/api/personal-mcp-servers?owner=bob&name=s&action=bogus", "")
	do("root", "POST", "/api/personal-mcp-servers?owner=bob&name=s&action=enable", "")
	do("root", "DELETE", "/api/personal-mcp-servers?owner=bob&name=s", "")
	do("alice", "POST", "/api/personal-mcp-servers?owner=bob&name=s&action=enable", "")
	do("root", "POST", "/api/personal-integrations", `{}`)
	do("root", "PATCH", "/api/personal-integrations", `{"mcp":"nonsense"}`)
	do("root", "POST", "/api/personal-integrations", `{"mcp":`)
	do("root", "POST", "/api/personal-integrations", ``)
	do("alice", "POST", "/api/personal-integrations", `{}`)

	// An install with no accounts yet: the gate admits whoever reaches it.
	fresh := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return fresh }
	app2 := &AdminApp{}
	mux2 := http.NewServeMux()
	app2.RegisterRoutes(mux2, "")
	for _, p := range []string{"/api/db/tables", "/api/tool-templates", "/api/user-candidates", "/api/personal-integrations", "/api/user-agents"} {
		run(mux2, fresh, "", "GET", p, "")
	}
	run(mux2, fresh, "", "POST", "/api/user-agents?owner=bob&id=x&action=revoke_share", "")

	ct.check(t, "testdata/routes_contract.golden")
}

// contract records what each request through the real routes answered, one
// request and its reply per entry, normalized so a run compares equal to the
// last: ids and times masked, lists sorted, a long reply kept as its head and
// a digest.
type contract struct {
	log strings.Builder
	// keep, when set, is which rows of a list reply are recorded: a store
	// other tests share holds rows that are not this test's.
	keep func(row string) bool
	// t and secrets, when set, fail the test on any reply that carries one
	// of the secrets, wherever in the reply it sits.
	t       *testing.T
	secrets []string
	// rewrite, when set, masks what differs run to run (a test server's
	// address) in the request and its reply.
	rewrite *strings.Replacer
	// shape, while set, records what it makes of a reply instead of the reply
	// (replyShape: its keys): for a reply that describes the host (its
	// sandbox, its binaries), which differs between this machine and the next
	// one to run the test.
	shape func(out string) string
	// dates masks calendar dates (a window ending today), and drop leaves
	// out the top-level keys of an object reply it matches (a registry other
	// packages add to).
	dates bool
	drop  func(key string) bool
}

var (
	contractHex   = regexp.MustCompile(`\b[0-9a-f]{16,64}\b`)
	contractStamp = regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?(Z|[+-]\d\d:\d\d)`)
	contractDate  = regexp.MustCompile(`\b\d{4}-\d\d-\d\d( \d\d:\d\d:\d\d)?\b`)
)

// run sends one request as user ("" for signed out), records the reply, and
// returns it as it came.
func (ct *contract) run(mux *http.ServeMux, db Database, user, method, path, body string) string {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if user != "" {
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(db, user)})
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	raw := w.Body.String()
	out := strings.TrimSpace(raw)
	for _, sec := range ct.secrets {
		if strings.Contains(out, sec) {
			ct.t.Errorf("%s %s as %q answered with a secret: %s", method, path, user, out)
		}
	}
	if ct.rewrite != nil {
		// Before the sort, so a list's order does not follow its random ids.
		out, path, body = ct.rewrite.Replace(out), ct.rewrite.Replace(path), ct.rewrite.Replace(body)
	}
	if ct.drop != nil {
		var m map[string]json.RawMessage
		if json.Unmarshal([]byte(out), &m) == nil {
			for k := range m {
				if ct.drop(k) {
					delete(m, k)
				}
			}
			b, _ := json.Marshal(m)
			out = string(b)
		}
	}
	if ct.shape != nil {
		out = ct.shape(out)
	}
	var items []json.RawMessage
	if strings.HasPrefix(out, "[") && json.Unmarshal([]byte(out), &items) == nil {
		rows := make([]string, 0, len(items))
		for _, it := range items {
			if ct.keep == nil || ct.keep(string(it)) {
				rows = append(rows, string(it))
			}
		}
		sort.Strings(rows)
		out = "[" + strings.Join(rows, ",") + "]"
	}
	out = contractHex.ReplaceAllString(out, "<hex>")
	out = contractStamp.ReplaceAllString(out, "<time>")
	if ct.dates {
		out = contractDate.ReplaceAllString(out, "<date>")
	}
	if len(out) > 400 {
		out = fmt.Sprintf("%s… (%d bytes, sha1 %x)", out[:200], len(out), sha1.Sum([]byte(out)))
	}
	who := user
	if who == "" {
		who = "(signed out)"
	}
	fmt.Fprintf(&ct.log, "%s %s %s %q\n  %d %s\n", who, method, path, body, w.Code, out)
	return raw
}

// activity records what the activity log holds, one "kind user: detail"
// line per entry, sorted: what each change wrote there, without the ids and
// times that differ run to run.
func (ct *contract) activity(mux *http.ServeMux, db Database, user string) {
	r := httptest.NewRequest("GET", "/api/activity?limit=500", nil)
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(db, user)})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	var rows []struct{ Kind, User, Detail string }
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		fmt.Fprintf(&ct.log, "activity: %d %s\n", w.Code, w.Body.String())
		return
	}
	lines := make([]string, len(rows))
	for i, e := range rows {
		lines[i] = fmt.Sprintf("  %s %s: %s\n", e.Kind, e.User, e.Detail)
		if ct.rewrite != nil {
			lines[i] = ct.rewrite.Replace(lines[i])
		}
	}
	sort.Strings(lines)
	fmt.Fprintf(&ct.log, "activity log, %d entries:\n%s", len(rows), strings.Join(lines, ""))
}

// replyShape is a JSON reply's keys: an object's, or every key a list's
// objects carry (not how many there are, which is the host's too).
func replyShape(out string) string {
	keys := func(raw []byte) []string {
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			return nil
		}
		ks := make([]string, 0, len(m))
		for k := range m {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		return ks
	}
	var items []json.RawMessage
	if json.Unmarshal([]byte(out), &items) == nil {
		seen := map[string]bool{}
		for _, it := range items {
			for _, k := range keys(it) {
				seen[k] = true
			}
		}
		all := make([]string, 0, len(seen))
		for k := range seen {
			all = append(all, k)
		}
		sort.Strings(all)
		return fmt.Sprintf("list of objects with keys %s", strings.Join(all, ","))
	}
	if ks := keys([]byte(out)); ks != nil {
		return "object with keys " + strings.Join(ks, ",")
	}
	return out
}

// check compares the record with golden, or rewrites golden when
// UPDATE_GOLDEN is set.
func (ct *contract) check(t *testing.T, golden string) {
	t.Helper()
	got := ct.log.String()
	if ct.rewrite != nil {
		// Again over the whole record: a mask learned from a reply (a minted
		// id) also covers the lines recorded before it was known.
		got = ct.rewrite.Replace(got)
	}
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
