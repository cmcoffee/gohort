package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// What the Connectors, Source hooks and Import sections get back, through the
// real routes and the gate in front of them: the backends listed, approved,
// unapproved, their spec read and replaced, and deleted; the source hooks
// listed, read for their edit form (the key never sent back), saved (the key
// kept on a blank), exposed, hidden, switched and deleted; and a bundle
// previewed and imported. Each as an administrator, as a user who is not one,
// by the wrong method and with a broken or missing body. Compared with
// testdata/connectors_contract.golden.
//
// Run with UPDATE_GOLDEN=1 to rewrite the golden after a change that is meant.
func TestConnectorsRoutesContract(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prevAuth, prevRoot := AuthDB, RootDB
	AuthDB = func() Database { return adb }
	RootDB = adb
	t.Cleanup(func() {
		AuthDB, RootDB = prevAuth, prevRoot
	})
	tmpl, ok := GetConnectorTemplate("a1111")
	if !ok {
		t.Fatal("the a1111 template is gone")
	}
	spec, _, err := tmpl.BuildSpec(map[string]any{"base_url": "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveConnector(adb, Connector{Name: "ct-art", Kind: tmpl.Kind, Template: tmpl.Name, Owner: "root", Desc: "a test backend", Spec: spec}); err != nil {
		t.Fatal(err)
	}

	ct := &contract{
		t:       t,
		secrets: []string{"s3cr3t-hook"},
		uuids:   true,
		dates:   true,
		keep: func(row string) bool {
			return !strings.Contains(row, `"name":`) || strings.Contains(row, `"name":"ct-`) || strings.Contains(row, `"name":"ct_`)
		},
	}
	app := &AdminApp{}
	mux := http.NewServeMux()
	app.RegisterRoutes(mux, "")
	do := func(user, method, path, body string) {
		ct.shape = nil
		// The spec is the a1111 template's, which is the code's.
		if strings.HasPrefix(path, "/api/connectors/spec?name=ct-art") {
			ct.shape = replyShape
		}
		ct.run(mux, adb, user, method, path, body)
	}
	const bundle = `{"bundle":"gohort.bundle/v1","artifacts":[{"type":"tool","name":"ct_imported","recipe":{"name":"ct_imported","description":"Came in a bundle.","command_template":"echo hi","mode":"shell"}}]}`

	for _, p := range []string{"/api/connectors", "/api/connectors/spec?name=ct-art", "/api/research-sources"} {
		do("", "GET", p, "")
		do("alice", "GET", p, "")
		do("root", "GET", p, "")
		do("root", "PUT", p, "{}")
	}
	for _, p := range []string{"/api/artifacts/preview", "/api/artifacts/import"} {
		do("alice", "POST", p, bundle)
		do("root", "GET", p, "")
	}

	do("alice", "POST", "/api/connectors?action=approve&name=ct-art", "")
	do("root", "POST", "/api/connectors?action=approve", "")
	do("root", "POST", "/api/connectors?action=approve&name=ct-art", "")
	do("root", "POST", "/api/connectors?action=approve&name=ct-nope", "")
	do("root", "POST", "/api/connectors?action=unapprove&name=ct-art", "")
	do("root", "POST", "/api/connectors?action=sideways&name=ct-art", "")
	do("root", "POST", "/api/connectors?action=update_spec&name=ct-art", `not json`)
	do("root", "POST", "/api/connectors?action=update_spec&name=ct-art", ``)
	do("root", "POST", "/api/connectors?action=update_spec&name=ct-nope", `{}`)
	do("root", "POST", "/api/connectors?action=update_spec&name=ct-art", strings.Replace(string(spec), "127.0.0.1:1", "127.0.0.1:2", 1))
	do("root", "GET", "/api/connectors", "")
	do("root", "GET", "/api/connectors/spec?name=ct-nope", "")

	// Research Sources: a search connector put to use for research.
	if err := SaveConnector(adb, Connector{Name: "ct-find", Kind: "service", Spec: json.RawMessage(`{"provider":{"transport":"http","base_url":"https://find.example.test","auth":{"none":true},"operations":{"find":{"path":"/s?q={query}"}}},"provides":[{"capability":"web.search","steps":[{"call":"find"}],"output":{"results":{"path":"hits","fields":{"title":"t","url":"u"}}}}]}`)}); err != nil {
		t.Fatal(err)
	}
	do("alice", "POST", "/api/research-sources", `{"connector":"ct-find","topics":["legal"]}`)
	do("root", "POST", "/api/research-sources", `{"connector":"ct-art","always":true}`)
	do("root", "POST", "/api/research-sources", `{"connector":"ct-find"}`)
	do("root", "POST", "/api/research-sources", `{"connector":`)
	do("root", "POST", "/api/research-sources", `{"connector":"ct-find","topics":[" Legal "],"offer":true,"tool_description":"Court cases."}`)
	do("root", "GET", "/api/research-sources", "")
	do("root", "GET", "/api/research-sources?name=ct-find", "")
	do("root", "GET", "/api/research-sources?name=ct-nope", "")
	do("root", "POST", "/api/research-sources?action=off&name=ct-find", "")
	do("root", "POST", "/api/research-sources?action=on&name=ct-nope", "")
	do("root", "GET", "/api/research-sources", "")
	do("alice", "DELETE", "/api/research-sources?name=ct-find", "")
	do("root", "DELETE", "/api/research-sources?name=ct-find", "")
	do("root", "GET", "/api/research-sources", "")

	do("root", "POST", "/api/artifacts/preview", bundle)
	do("root", "POST", "/api/artifacts/preview", `{"pack":`+jsonString(bundle)+`}`)
	do("root", "POST", "/api/artifacts/preview", `{"bundle":"something else"}`)
	do("root", "POST", "/api/artifacts/preview", ``)
	do("root", "POST", "/api/artifacts/import", bundle)
	do("root", "POST", "/api/artifacts/import", `not json`)

	do("alice", "DELETE", "/api/connectors?name=ct-art", "")
	do("root", "DELETE", "/api/connectors", "")
	do("root", "DELETE", "/api/connectors?name=ct-art", "")
	do("root", "GET", "/api/connectors", "")

	ct.check(t, "testdata/connectors_contract.golden")
}

// jsonString is s as a JSON string, for a body that wraps one.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
