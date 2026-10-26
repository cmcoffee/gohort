package admin

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/flows"
	"github.com/cmcoffee/snugforge/kvlite"
)

// What the System Hooks section gets back, through the real routes and the
// gate in front of them: the table and its walk of what cannot run, adding a
// format of the deployment's own and editing it, adapting one of gohort's
// jobs, the Agents switch, Becomes's choices, one flow's form and a job's
// Adapt form. Each as an administrator, as a user who is not one, by the wrong
// method and with a broken or missing body. gohort's own jobs and whether
// this host can run them are the host's, so only this test's flows are
// recorded. Compared with testdata/sysdeps_contract.golden.
//
// Run with UPDATE_GOLDEN=1 to rewrite the golden after a change that is meant.
func TestSystemDepsRoutesContract(t *testing.T) {
	needDepsSandbox(t)
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prevAuth, prevRoot := AuthDB, RootDB
	AuthDB = func() Database { return adb }
	RootDB = adb
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot })
	if cache, err := os.UserCacheDir(); err == nil {
		if root, err := os.MkdirTemp(cache, "gohort-admin-sysdeps-contract-"); err == nil {
			flows.SetWorkBase(filepath.Join(root, "data", "workspaces", "_flows"))
			t.Cleanup(func() { flows.SetWorkBase(""); os.RemoveAll(root) })
		}
	}
	lastTests.Lock()
	lastTests.m = map[string]testOutcome{}
	lastTests.Unlock()

	ours := func(row string) bool {
		return strings.Contains(row, `"id":"ct`) || strings.Contains(row, `"id":"read-heic-adapted`)
	}
	aList := func(out string) string {
		var rows []json.RawMessage
		if json.Unmarshal([]byte(out), &rows) == nil {
			return "a list"
		}
		return out
	}
	ct := &contract{keep: func(row string) bool { return !strings.Contains(row, `"id":`) || ours(row) }}
	app := &AdminApp{}
	mux := http.NewServeMux()
	app.RegisterRoutes(mux, "")
	do := func(user, method, path, body string) {
		ct.shape = nil
		// The walk and Becomes's choices are the host's and the code's.
		if strings.Contains(path, "?fix=") || strings.HasPrefix(path, "/api/system-deps/becomes") {
			ct.shape = aList
		}
		ct.run(mux, adb, user, method, path, body)
	}
	const strip = `{"name":"ctstrip","direction":"input","takes":"*","becomes":"image","steps":"dd if={in} of={out} bs=8 skip=1"}`

	for _, p := range []string{"/api/system-deps", "/api/system-deps?custom=1", "/api/system-deps?fix=1", "/api/system-deps/becomes",
		"/api/system-deps/one?name=ctstrip", "/api/system-deps/adapt?id=read.heic"} {
		do("", "GET", p, "")
		do("alice", "GET", p, "")
		do("root", "GET", p, "")
		do("root", "PUT", p, "{}")
	}

	do("alice", "POST", "/api/system-deps", strip)
	do("root", "POST", "/api/system-deps", strip)
	do("root", "POST", "/api/system-deps", strip)
	do("root", "POST", "/api/system-deps", strings.Replace(strings.Replace(strip, `"ctstrip"`, `"ctnoin"`, 1), " if={in}", "", 1))
	do("root", "POST", "/api/system-deps", `{"name":"ctheic","direction":"input","takes":"heic","becomes":"image","steps":"dd if={in} of={out}"}`)
	do("root", "POST", "/api/system-deps?job=no.such.job", `{"name":"ctjob","steps":"true"}`)
	do("root", "POST", "/api/system-deps?job=read.heic", `{"name":"read-heic-adapted","steps":"heif-convert {in} {out.png}"}`)
	do("root", "POST", "/api/system-deps", `{"name":`)
	do("root", "POST", "/api/system-deps", ``)
	do("root", "GET", "/api/system-deps?custom=1", "")
	do("root", "GET", "/api/system-deps", "")
	do("root", "GET", "/api/system-deps/one?name=ctstrip", "")
	do("root", "GET", "/api/system-deps/one?name=read-heic-adapted", "")
	do("root", "GET", "/api/system-deps/one?name=nope", "")
	do("root", "POST", "/api/system-deps?edit=ctstrip", strings.Replace(strip, "bs=8 skip=1", "bs=4 skip=2", 1))
	do("root", "GET", "/api/system-deps/one?name=ctstrip", "")
	do("root", "GET", "/api/system-deps/adapt?id=read.heic", "")
	do("root", "GET", "/api/system-deps/adapt?id=nope", "")

	do("alice", "POST", "/api/system-deps/agents?id=ctstrip", `{"agents":true}`)
	do("root", "GET", "/api/system-deps/agents?id=ctstrip", "")
	do("root", "POST", "/api/system-deps/agents?id=ctstrip", `{"agents":true}`)
	do("root", "POST", "/api/system-deps/agents?id=read.heic", `{"agents":true}`)
	do("root", "POST", "/api/system-deps/agents?id=read-heic-adapted", `{"agents":true}`)
	do("root", "POST", "/api/system-deps/agents?id=nope", `{"agents":true}`)
	do("root", "POST", "/api/system-deps/agents?id=ctstrip", `{"agents":`)
	do("root", "GET", "/api/system-deps?custom=1", "")
	do("root", "POST", "/api/system-deps/agents?id=ctstrip", `{"agents":false}`)
	do("root", "GET", "/api/system-deps?custom=1", "")

	ct.check(t, "testdata/sysdeps_contract.golden")
}
