package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// ctTemplate is a template file of this test's own: one shell tool whose
// command a question fills in. The built-in templates are left out of what is
// recorded, so adding one does not move the golden.
const ctTemplate = `{
  "id": "ct-echo",
  "title": "Echo a greeting",
  "description": "A shell tool that echoes what it was set up with.",
  "category": "Tools",
  "questions": [{"name": "greeting", "label": "Greeting", "required": true}],
  "bundle": {
    "bundle": "gohort.bundle/v1",
    "artifacts": [{
      "type": "tool",
      "name": "ct_echo",
      "recipe": {"name": "ct_echo", "description": "Echo the greeting.", "command_template": "echo {{greeting}}", "mode": "shell"}
    }]
  }
}`

// What the Templates section gets back, through the real routes and the gate
// in front of them: the list, one template's questions, importing a template
// file (bare or wrapped as {"pack"}), adding it with answers, the pieces a new
// template can be made from, saving one from them, exporting and deleting.
// Each as an administrator, as a user who is not one, by the wrong method and
// with a broken or missing body. Compared with
// testdata/recipes_contract.golden.
//
// Run with UPDATE_GOLDEN=1 to rewrite the golden after a change that is meant.
func TestRecipesRoutesContract(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prevAuth, prevRoot := AuthDB, RootDB
	AuthDB = func() Database { return adb }
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot })

	// Kept out of what is recorded: the built-in templates (adding one would
	// move the golden) and credential pieces (the vault is the process's, and
	// other tests leave theirs in it).
	ct := &contract{
		keep: func(row string) bool {
			if strings.Contains(row, `"group":"credential"`) {
				return false
			}
			return !strings.Contains(row, `"kind":"template"`) || strings.Contains(row, `"_imported":true`)
		},
	}
	app := &AdminApp{}
	mux := http.NewServeMux()
	app.RegisterRoutes(mux, "")
	do := func(user, method, path, body string) { ct.run(mux, adb, user, method, path, body) }
	pack, _ := json.Marshal(map[string]string{"pack": strings.Replace(ctTemplate, `"ct-echo"`, `"ct-echo-2"`, 1)})

	for _, p := range []string{"/api/templates", "/api/templates/recipe?id=ct-echo", "/api/templates/pieces", "/api/templates/export?id=ct-echo"} {
		do("", "GET", p, "")
		do("alice", "GET", p, "")
		do("root", "GET", p, "")
	}
	for _, p := range []string{"/api/templates/install?id=ct-echo", "/api/templates/import", "/api/templates/delete?id=ct-echo", "/api/templates/save"} {
		do("alice", "POST", p, `{}`)
		do("root", "GET", p, "")
	}

	do("root", "POST", "/api/templates/import", ctTemplate)
	do("root", "POST", "/api/templates/import", string(pack))
	do("root", "POST", "/api/templates/import", strings.Replace(ctTemplate, `"ct-echo"`, `"system-info"`, 1))
	do("root", "POST", "/api/templates/import", `{"id":"ct-empty"}`)
	do("root", "POST", "/api/templates/import", `{"id":`)
	do("root", "POST", "/api/templates/import", ``)
	do("root", "GET", "/api/templates", "")
	do("root", "GET", "/api/templates/recipe?id=ct-echo", "")
	do("root", "GET", "/api/templates/recipe?id=nope", "")
	do("root", "GET", "/api/templates/export?id=ct-echo", "")
	do("root", "GET", "/api/templates/export?id=nope", "")

	do("root", "POST", "/api/templates/install?id=ct-echo", `{"answers":{}}`)
	do("root", "POST", "/api/templates/install?id=ct-echo", `{"answers":{"greeting":"hello"}}`)
	do("root", "POST", "/api/templates/install?id=nope", `{"answers":{}}`)
	do("root", "POST", "/api/templates/install?id=ct-echo", `{"answers":`)
	do("root", "POST", "/api/templates/install?id=ct-echo", ``)
	do("root", "GET", "/api/templates/pieces", "")

	do("root", "POST", "/api/templates/save", `{"title":"Nothing in it","pieces":[]}`)
	do("root", "POST", "/api/templates/save", `{"title":"Saved Echo!","pieces":["tool|ct_echo|root"],"questions":[{"name":"greeting","value":"hello"},{"name":" "}]}`)
	do("root", "POST", "/api/templates/save", `{"id":"system-info","title":"Clash","pieces":["tool|ct_echo|root"]}`)
	do("root", "POST", "/api/templates/save", `{"title":`)
	do("root", "GET", "/api/templates/recipe?id=saved-echo", "")

	do("root", "POST", "/api/templates/delete?id=system-info", "")
	do("root", "POST", "/api/templates/delete?id=nope", "")
	do("root", "POST", "/api/templates/delete?id=ct-echo-2", "")
	do("root", "PUT", "/api/templates/delete?id=ct-echo", "")
	do("root", "GET", "/api/templates", "")

	ct.check(t, "testdata/recipes_contract.golden")
}
