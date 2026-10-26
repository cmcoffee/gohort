package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// What the Categories (tool groups) and App groups sections get back, through
// the real routes and the gate in front of them: each list, one group,
// creating, renaming (its members kept) and deleting one, reverting a
// built-in, the tools a group may be made of, and the pills editor that files
// a user's own tools under a category. Each as an administrator, as a user who
// is not one, by the wrong method and with a broken or missing body. Compared
// with testdata/groups_contract.golden.
//
// Run with UPDATE_GOLDEN=1 to rewrite the golden after a change that is meant.
func TestGroupsRoutesContract(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prevAuth, prevRoot := AuthDB, RootDB
	AuthDB = func() Database { return adb }
	RootDB = &DBase{Store: kvlite.MemStore()}
	// Both group stores keep a process-wide cache that a delete clears: one
	// now, so this test reads its own store, and one after, so the next test
	// reads its own.
	clearGroupCaches := func(db Database) {
		DeleteToolGroup(db, "zz-clear-cache")
		DeleteAppGroup(db, "zz-clear-cache")
	}
	clearGroupCaches(adb)
	t.Cleanup(func() {
		clearGroupCaches(adb)
		AuthDB, RootDB = prevAuth, prevRoot
	})
	for _, tt := range []TempTool{
		{Name: "ct_lookup", Description: "Look a thing up.\nMore detail.", CommandTemplate: "echo look"},
		{Name: "ct_report", Description: "Write a report.", CommandTemplate: "echo report", Category: "Elsewhere"},
	} {
		if err := AdminPersistTempTool(adb, "root", tt); err != nil {
			t.Fatal(err)
		}
	}
	if err := AdminPersistTempTool(RootDB, "bob", TempTool{Name: "ct_bobs", Description: "Bob's tool.", CommandTemplate: "echo bob"}); err != nil {
		t.Fatal(err)
	}

	// Left out of what is recorded: the framework's built-in groups and the
	// process's registered chat tools, both of which change with the code
	// rather than with what this test does.
	ct := &contract{
		dates: true,
		keep: func(row string) bool {
			if strings.Contains(row, `"is_builtin":true`) {
				return false
			}
			return !strings.Contains(row, `"source":`) || strings.Contains(row, `"source":"temp"`)
		},
	}
	var masks []string
	app := &AdminApp{}
	mux := http.NewServeMux()
	app.RegisterRoutes(mux, "")
	do := func(user, method, path, body string) string {
		if len(masks) > 0 {
			ct.rewrite = strings.NewReplacer(masks...)
		}
		return ct.run(mux, adb, user, method, path, body)
	}
	create := func(path, as, body string) string {
		var got struct{ ID string }
		reply := do("root", "POST", path, body)
		json.Unmarshal([]byte(reply), &got)
		if got.ID == "" {
			t.Fatalf("create %s: no id in %s", body, reply)
		}
		masks = append(masks, got.ID, as)
		return got.ID
	}

	for _, p := range []string{"/api/tool-groups", "/api/app-groups", "/api/tool-groups/registry", "/api/tool-groups/registry?exclude_grouped=true"} {
		do("", "GET", p, "")
		do("alice", "GET", p, "")
		do("root", "GET", p, "")
		do("root", "PUT", p, "{}")
	}

	do("alice", "POST", "/api/tool-groups", `{"name":"Mine"}`)
	tg := create("/api/tool-groups", "<tool-group>", `{"name":" Research ","description":"Looking things up","members":["ct_lookup","ct_lookup"," "]}`)
	do("root", "POST", "/api/tool-groups", `{"name":" "}`)
	do("root", "POST", "/api/tool-groups", `{"name":`)
	do("root", "POST", "/api/tool-groups", `{"id":"`+tg+`","name":"Research Desk","description":"renamed"}`)
	do("root", "GET", "/api/tool-groups/"+tg, "")
	do("root", "GET", "/api/tool-groups/nope", "")
	do("root", "POST", "/api/tool-groups/"+tg, "{}")
	do("root", "GET", "/api/tool-groups/registry?exclude_grouped=true", "")
	do("root", "GET", "/api/tool-groups/registry?exclude_grouped=true&except_group="+tg, "")
	do("root", "GET", "/api/tool-groups/registry?exclude=ct_bobs", "")
	do("root", "GET", "/api/tool-groups", "")

	do("alice", "GET", "/api/tool-groups/"+tg+"/members", "")
	do("alice", "POST", "/api/tool-groups/"+tg+"/members", `{"members":["ct_lookup"]}`)
	do("root", "GET", "/api/tool-groups/"+tg+"/members", "")
	do("root", "POST", "/api/tool-groups/"+tg+"/members", `{"members":["ct_lookup"," ","ct_report"]}`)
	do("root", "POST", "/api/tool-groups/"+tg+"/members", `{"members":[]}`)
	do("root", "POST", "/api/tool-groups/"+tg+"/members", `{"members":`)
	do("root", "PUT", "/api/tool-groups/"+tg+"/members", `{}`)
	do("root", "GET", "/api/tool-groups/nope/members", "")

	do("alice", "POST", "/api/app-groups", `{"name":"Mine"}`)
	ag := create("/api/app-groups", "<app-group>", `{"name":"Writers","apps":["/scribe","/codewriter"]}`)
	do("root", "POST", "/api/app-groups", `{"name":""}`)
	do("root", "POST", "/api/app-groups", `{"name":`)
	do("root", "GET", "/api/app-groups/"+ag, "")
	do("root", "GET", "/api/app-groups/nope", "")
	do("root", "POST", "/api/app-groups/"+ag, "{}")
	do("root", "GET", "/api/app-groups", "")

	do("alice", "DELETE", "/api/tool-groups?id="+tg, "")
	do("root", "DELETE", "/api/tool-groups", "")
	do("root", "DELETE", "/api/tool-groups?id=builtin-web-media", "")
	do("root", "DELETE", "/api/tool-groups?id="+tg, "")
	do("root", "GET", "/api/tool-groups/"+tg, "")
	do("alice", "DELETE", "/api/app-groups?id="+ag, "")
	do("root", "DELETE", "/api/app-groups", "")
	do("root", "DELETE", "/api/app-groups?id="+ag, "")
	do("root", "GET", "/api/app-groups", "")

	ct.check(t, "testdata/groups_contract.golden")
}
