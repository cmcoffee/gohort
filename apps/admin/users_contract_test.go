package admin

import (
	"net/http"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// What the Users and Feature access pages get back, through the real routes
// and the gate in front of them: listing, adding and inviting accounts;
// changing, approving, rejecting and deleting one; resetting its password,
// signing it out everywhere, setting its apps and groups, and moving or
// clearing its data. Each as an administrator, as a user who is not one, by
// the wrong method and with a broken or missing body, and then what the
// activity log says happened. Compared with testdata/users_contract.golden.
//
// Run with UPDATE_GOLDEN=1 to rewrite the golden after a change that is meant.
func TestUsersRoutesContract(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	adb.Set(AuthTable, "user:pat", AuthUser{Username: "pat", Pending: true})
	adb.Set(AuthTable, "user:quinn", AuthUser{Username: "quinn", Pending: true})
	prevAuth, prevRoot := AuthDB, RootDB
	AuthDB = func() Database { return adb }
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot })

	ct := &contract{}
	app := &AdminApp{}
	mux := http.NewServeMux()
	app.RegisterRoutes(mux, "")
	do := func(user, method, path, body string) { ct.run(mux, adb, user, method, path, body) }

	for _, p := range []string{"/api/users", "/api/feature-access", "/api/feature-access?feature=desktop", "/api/users/alice", "/api/users/alice/data"} {
		do("", "GET", p, "")
		do("alice", "GET", p, "")
		do("root", "GET", p, "")
		do("root", "PATCH", p, "{}")
	}

	do("alice", "POST", "/api/users", `{"username":"mallory","password":"pw-123456","admin":true}`)
	do("root", "POST", "/api/users", `{"username":"carol","password":"pw-123456"}`)
	do("root", "POST", "/api/users", `{"username":"carol","password":"pw-123456"}`)
	do("root", "POST", "/api/users", `{"username":"  ","password":"pw-123456"}`)
	do("root", "POST", "/api/users", `{"username":"dave"}`)
	do("root", "POST", "/api/users", `{"username":"erin","invite":true}`)
	do("root", "POST", "/api/users", `{"username":`)
	do("root", "POST", "/api/users", ``)

	do("alice", "POST", "/api/feature-access?feature=desktop", `{"allowed_users":["alice"]}`)
	do("root", "POST", "/api/feature-access?feature=desktop", `{"allowed_users":["alice"]}`)
	do("root", "POST", "/api/feature-access?feature=nope", `{"allowed_users":["alice"]}`)
	do("root", "POST", "/api/feature-access", `{}`)
	do("root", "POST", "/api/feature-access?feature=desktop", `{"allowed_users":`)
	do("root", "POST", "/api/feature-access?feature=desktop", ``)
	do("root", "GET", "/api/feature-access", "")

	do("root", "GET", "/api/users/nobody", "")
	do("root", "GET", "/api/users/", "")
	do("root", "PUT", "/api/users/carol", `{"admin":true}`)
	do("root", "PUT", "/api/users/carol", `{"admin":false,"password":"pw-654321"}`)
	do("root", "PUT", "/api/users/root", `{"admin":false}`)
	do("root", "PUT", "/api/users/nobody", `{"admin":true}`)
	do("root", "PUT", "/api/users/carol", `{"admin":`)
	do("alice", "PUT", "/api/users/alice", `{"admin":true}`)

	for _, a := range []struct{ method, action, body string }{
		{"PUT", "apps", `{"apps":["/chat"]}`},
		{"PUT", "groups", `{"groups":["g1"]}`},
		{"POST", "approve", ""},
		{"POST", "reject", ""},
		{"POST", "reset-password", ""},
		{"POST", "revoke-sessions", ""},
		{"POST", "data-action", `{"app":"nope","action":"purge"}`},
	} {
		do("alice", a.method, "/api/users/carol/"+a.action, a.body)
		do("root", "GET", "/api/users/carol/"+a.action, "")
		do("root", a.method, "/api/users/nobody/"+a.action, a.body)
	}
	do("alice", "GET", "/api/users/carol/data", "")
	do("root", "POST", "/api/users/carol/data", "")
	do("root", "GET", "/api/users/carol/nope", "")
	do("root", "GET", "/api/users/carol/", "")

	do("root", "PUT", "/api/users/carol/apps", `{"apps":["/chat"]}`)
	do("root", "PUT", "/api/users/carol/apps", `{"apps":`)
	do("root", "PUT", "/api/users/carol/groups", `{"groups":["g1"]}`)
	do("root", "PUT", "/api/users/carol/groups", ``)
	do("root", "GET", "/api/users/carol", "")

	do("root", "POST", "/api/users/pat/approve", "")
	do("root", "POST", "/api/users/pat/approve", "")
	do("root", "POST", "/api/users/quinn/reject", "")
	do("root", "POST", "/api/users/alice/reject", "")

	do("root", "POST", "/api/users/carol/reset-password", "")
	do("root", "POST", "/api/users/carol/reset-password", `{"mode":"set","password":"short"}`)
	do("root", "POST", "/api/users/carol/reset-password", `{"mode":"set","password":"pw-777777"}`)
	do("root", "POST", "/api/users/carol/reset-password", `{"mode":"set","password":`)

	do("root", "POST", "/api/users/alice/revoke-sessions", "")

	do("root", "POST", "/api/users/carol/data-action", `{}`)
	do("root", "POST", "/api/users/carol/data-action", `{"app":"nope","action":"purge"}`)
	do("root", "POST", "/api/users/carol/data-action", `{"app":`)
	for _, h := range RegisteredUserDataHandlers() {
		do("root", "POST", "/api/users/carol/data-action", `{"app":"`+h.AppName()+`","action":"reassign"}`)
		do("root", "POST", "/api/users/carol/data-action", `{"app":"`+h.AppName()+`","action":"reassign","target":"nobody"}`)
		do("root", "POST", "/api/users/carol/data-action", `{"app":"`+h.AppName()+`","action":"bogus"}`)
		do("root", "POST", "/api/users/carol/data-action", `{"app":"`+h.AppName()+`","action":"reassign","target":"alice"}`)
		do("root", "POST", "/api/users/carol/data-action", `{"app":"`+h.AppName()+`","action":"purge"}`)
	}

	do("alice", "DELETE", "/api/users/carol", "")
	do("root", "DELETE", "/api/users/root", "")
	do("root", "DELETE", "/api/users/nobody", "")
	do("root", "DELETE", "/api/users/carol", "")
	do("root", "DELETE", "/api/users/carol", "")
	do("root", "GET", "/api/users", "")
	ct.activity(mux, adb, "root")

	ct.check(t, "testdata/users_contract.golden")
}
