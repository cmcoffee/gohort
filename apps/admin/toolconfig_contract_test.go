package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// What Admin › Tools and the template-built backends get back, through the
// real routes and the gate in front of them: every user's tools by where they
// live, one tool's access list, approving, rejecting, sharing and restricting
// a tool, deleting one; the scope pill's state and toggles; and building a
// tool or a backend from a template, detecting a template's values from what
// was pasted, and reading one back for its edit form. Each as an
// administrator, as a user who is not one, by the wrong method and with a
// broken or missing body. Nothing reaches past this machine. Compared with
// testdata/toolconfig_contract.golden.
//
// Run with UPDATE_GOLDEN=1 to rewrite the golden after a change that is meant.
func TestToolConfigRoutesContract(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	prevAuth, prevRoot := AuthDB, RootDB
	AuthDB = func() Database { return adb }
	RootDB = adb
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot })
	if err := AdminPersistTempTool(adb, "root", TempTool{Name: "ct_shared", Description: "Shared one.", CommandTemplate: "echo shared"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ct_pending", "ct_unwanted"} {
		if err := QueuePendingTempTool(adb, "bob", TempTool{Name: name, Description: "Waiting.", CommandTemplate: "echo " + name}, "s1"); err != nil {
			t.Fatal(err)
		}
	}
	// A scope backend of the test's own: the real ones are orchestrate's.
	scoped := map[string]bool{}
	RegisterScopeProvider("ct", ScopeProvider{
		State: func(db Database, owner, name string) (ToolScopeState, bool) {
			if name != "ct_shared" {
				return ToolScopeState{}, false
			}
			return ToolScopeState{Global: scoped["global"]}, true
		},
		Set: func(db Database, owner, name, target string, on bool) error {
			if target == "nowhere" {
				return errors.New("no such agent")
			}
			scoped[target] = on
			return nil
		},
	})

	ct := &contract{dates: true, uuids: true}
	app := &AdminApp{}
	mux := http.NewServeMux()
	app.RegisterRoutes(mux, "")
	do := func(user, method, path, body string) {
		ct.shape = nil
		// An edit form's schema is its template's fields, which are the
		// code's: recorded as its keys.
		if method == "GET" && (strings.HasPrefix(path, "/api/tool-config") || strings.HasPrefix(path, "/api/connector-config")) {
			ct.shape = replyShape
		}
		// The overview's lists follow the store's order of owners, which is
		// not one: each compared sorted, as a top-level list is.
		if method == "GET" && path == "/api/persistent-tools" {
			ct.shape = sortEachList
		}
		ct.run(mux, adb, user, method, path, body)
	}
	const spec = `{\"openapi\":\"3.0.0\",\"servers\":[{\"url\":\"https://api.example.test\"}],\"paths\":{\"/items\":{\"get\":{\"operationId\":\"listItems\",\"summary\":\"List items\"}}}}`

	for _, p := range []string{"/api/persistent-tools", "/api/persistent-tools?allowed_users=ct_shared", "/api/tool-scope?kind=ct&name=ct_shared",
		"/api/tool-config?tool=ct_shared", "/api/connector-config?connector=nope"} {
		do("", "GET", p, "")
		do("alice", "GET", p, "")
		do("root", "GET", p, "")
		do("root", "PUT", p, "{}")
	}

	do("alice", "POST", "/api/persistent-tools?action=approve&owner=bob&name=ct_pending", "")
	do("root", "POST", "/api/persistent-tools?action=approve&owner=bob", "")
	do("root", "POST", "/api/persistent-tools?action=approve&owner=bob&name=ct_pending", "")
	do("root", "POST", "/api/persistent-tools?action=reject&owner=bob&name=ct_unwanted", "")
	do("root", "POST", "/api/persistent-tools?action=approve&owner=bob&name=ct_nope", "")
	do("root", "POST", "/api/persistent-tools?action=share&name=ct_shared", "")
	do("root", "POST", "/api/persistent-tools?action=set_allowed_users&name=ct_shared", `{"allowed_users":["alice"]}`)
	do("root", "GET", "/api/persistent-tools?allowed_users=ct_shared", "")
	do("root", "POST", "/api/persistent-tools?action=set_allowed_users&name=ct_shared", ``)
	do("root", "POST", "/api/persistent-tools?action=set_allowed_users&name=ct_shared", `{"allowed_users":`)
	do("root", "GET", "/api/persistent-tools?allowed_users=ct_shared", "")
	do("root", "POST", "/api/persistent-tools?action=unshare&name=ct_shared", "")
	do("root", "POST", "/api/persistent-tools?action=orphan_promote&name=ct_gone", "")
	do("root", "POST", "/api/persistent-tools?action=orphan_attach&name=ct_gone", "")
	do("root", "POST", "/api/persistent-tools?action=orphan_delete&name=ct_gone", "")
	do("root", "POST", "/api/persistent-tools?action=sideways&name=ct_shared", "")
	do("root", "GET", "/api/persistent-tools", "")
	do("alice", "DELETE", "/api/persistent-tools?owner=bob&name=ct_pending", "")
	do("root", "DELETE", "/api/persistent-tools?owner=bob", "")
	do("root", "DELETE", "/api/persistent-tools?owner=bob&name=ct_pending", "")
	do("root", "GET", "/api/persistent-tools", "")

	do("root", "GET", "/api/tool-scope?kind=ct", "")
	do("root", "GET", "/api/tool-scope?kind=elsewhere&name=ct_shared", "")
	do("root", "GET", "/api/tool-scope?kind=ct&name=ct_nope", "")
	do("alice", "POST", "/api/tool-scope?kind=ct&name=ct_shared", `{"target":"global","on":true}`)
	do("root", "POST", "/api/tool-scope?kind=ct&name=ct_shared", `{"target":"global","on":true}`)
	do("root", "GET", "/api/tool-scope?kind=ct&name=ct_shared", "")
	do("root", "POST", "/api/tool-scope?kind=ct&name=ct_shared", `{"target":" "}`)
	do("root", "POST", "/api/tool-scope?kind=ct&name=ct_shared", `{"target":"nowhere","on":true}`)
	do("root", "POST", "/api/tool-scope?kind=ct&name=ct_shared", `{"target":`)

	do("alice", "POST", "/api/tool-detect?template=openapi_tool", `{"values":{"spec":"`+spec+`"}}`)
	do("root", "POST", "/api/tool-detect?template=openapi_tool", `{"values":{"spec":"`+spec+`"}}`)
	do("root", "POST", "/api/tool-detect?template=openapi_tool", `{"values":{"spec":"not a spec"}}`)
	do("root", "POST", "/api/tool-detect?template=nope", `{"values":{}}`)
	do("root", "POST", "/api/tool-detect?template=openapi_tool", `{"values":`)
	do("alice", "POST", "/api/tool-config", `{"template":"openapi_tool","name":"ct_items","values":{"spec":"`+spec+`"}}`)
	do("root", "POST", "/api/tool-config", `{"template":"openapi_tool","name":"ct_items","values":{"spec":"`+spec+`"}}`)
	do("root", "POST", "/api/tool-config", `{"template":"nope","name":"ct_x","values":{}}`)
	do("root", "POST", "/api/tool-config", `{"template":"openapi_tool","values":{}}`)
	do("root", "POST", "/api/tool-config", `{"template":`)
	do("root", "GET", "/api/tool-config?tool=ct_items", "")
	do("root", "GET", "/api/tool-config?tool=ct_nope", "")
	do("root", "POST", "/api/tool-config", `{"template":"openapi_tool","connector":"ct_items","values":{"spec":"`+spec+`","description":"Edited."}}`)

	do("alice", "POST", "/api/connector-detect?template=a1111", `{"values":{}}`)
	do("root", "POST", "/api/connector-detect?template=a1111", `{"values":{}}`)
	do("root", "POST", "/api/connector-detect?template=nope", `{"values":{}}`)
	do("alice", "POST", "/api/connector-config", `{"template":"a1111","name":"ct-art","values":{"base_url":"http://127.0.0.1:1"}}`)
	do("root", "POST", "/api/connector-config", `{"template":"a1111","name":"ct-art","values":{"base_url":"http://127.0.0.1:1"}}`)
	do("root", "POST", "/api/connector-config", `{"template":"a1111","name":"ct-art","values":{"base_url":"http://127.0.0.1:1"}}`)
	do("root", "POST", "/api/connector-config", `{"template":"a1111","name":" ","values":{"base_url":"http://127.0.0.1:1"}}`)
	do("root", "POST", "/api/connector-config", `{"template":"nope","name":"ct-x","values":{}}`)
	do("root", "POST", "/api/connector-config", `{"template":"a1111","connector":"ct-nope","values":{"base_url":"http://127.0.0.1:1"}}`)
	do("root", "POST", "/api/connector-config", `{"template":`)
	do("root", "GET", "/api/connector-config?connector=ct-art", "")
	do("root", "POST", "/api/connector-config", `{"template":"a1111","connector":"ct-art","values":{"base_url":"http://127.0.0.1:2"}}`)
	do("root", "GET", "/api/connector-config?connector=ct-art", "")

	ct.activity(mux, adb, "root")
	ct.check(t, "testdata/toolconfig_contract.golden")
}

// sortEachList sorts every list an object reply holds.
func sortEachList(out string) string {
	var m map[string][]json.RawMessage
	if json.Unmarshal([]byte(out), &m) != nil {
		return out
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		rows := make([]string, len(m[k]))
		for i, r := range m[k] {
			rows[i] = string(r)
		}
		sort.Strings(rows)
		parts = append(parts, fmt.Sprintf("%q:[%s]", k, strings.Join(rows, ",")))
	}
	return "{" + strings.Join(parts, ",") + "}"
}
