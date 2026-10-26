package admin

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/promotion"
	"github.com/cmcoffee/snugforge/kvlite"
)

var registerContractApp sync.Once

// What the publish queue, the global tool catalog and the app switches get
// back, through the real routes and the gate in front of them: the pending
// requests, approving one (its tool published) and denying one; the catalog,
// a tool's kept versions, and rolling back to one; and the apps listed and
// switched off and on. Each as an administrator, as a user who is not one, by
// the wrong method and with a broken or missing body, and then what the
// activity log says happened. Compared with
// testdata/governance_contract.golden.
//
// Run with UPDATE_GOLDEN=1 to rewrite the golden after a change that is meant.
func TestGovernanceRoutesContract(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	prevAuth, prevRoot := AuthDB, RootDB
	AuthDB = func() Database { return adb }
	RootDB = adb
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot })
	registerContractApp.Do(func() { RegisterApp(fakeWebApp{path: "/zzcontractapp"}) })
	for _, tt := range []TempTool{
		{Name: "ct_pub", Description: "Publish me.", CommandTemplate: "echo one"},
		{Name: "ct_nah", Description: "Not this one.", CommandTemplate: "echo nah"},
	} {
		if err := AdminPersistTempTool(adb, "bob", tt); err != nil {
			t.Fatal(err)
		}
	}

	var masks []string
	ct := &contract{
		dates: true,
		keep: func(row string) bool {
			if strings.Contains(row, `"path":`) {
				return strings.Contains(row, `"path":"/zzcontractapp"`)
			}
			return true
		},
	}
	app := &AdminApp{}
	mux := http.NewServeMux()
	app.RegisterRoutes(mux, "")
	do := func(user, method, path, body string) string {
		if len(masks) > 0 {
			ct.rewrite = strings.NewReplacer(masks...)
		}
		return ct.run(mux, adb, user, method, path, body)
	}
	// request files a publish request for one of bob's tools and names its
	// id in the record from then on.
	request := func(tool, as string) string {
		if err := promotion.CreatePromotionRequest(adb, "bob", "tool", tool, "please publish"); err != nil {
			t.Fatal(err)
		}
		for _, r := range promotion.ListPromotionRequests(adb, true) {
			if r.Name == tool {
				masks = append(masks, r.ID, as)
				return r.ID
			}
		}
		t.Fatalf("no request for %s", tool)
		return ""
	}
	pub := request("ct_pub", "<request-pub>")
	nah := request("ct_nah", "<request-nah>")

	for _, p := range []string{"/api/promotions", "/api/global-tools", "/api/global-tools?history=ct_pub", "/api/app-switches"} {
		do("", "GET", p, "")
		do("alice", "GET", p, "")
		do("root", "GET", p, "")
		do("root", "PUT", p, "{}")
	}

	do("alice", "POST", "/api/promotions?action=approve&id="+pub, "")
	do("root", "POST", "/api/promotions?action=approve", "")
	do("root", "POST", "/api/promotions?action=approve&id=nope", "")
	do("root", "POST", "/api/promotions?action=sideways&id="+pub, "")
	do("root", "POST", "/api/promotions?action=approve&id="+pub, "")
	do("root", "POST", "/api/promotions?action=deny&id="+nah, "")
	do("root", "GET", "/api/promotions", "")
	do("root", "GET", "/api/global-tools", "")

	// An update: bob changes the tool and asks again; approving it keeps v1.
	if err := AdminReconfigureTempTool(adb, "bob", TempTool{Name: "ct_pub", Description: "Publish me, again.", CommandTemplate: "echo two"}); err != nil {
		t.Fatal(err)
	}
	upd := request("ct_pub", "<request-update>")
	do("root", "GET", "/api/promotions", "")
	do("root", "GET", "/api/global-tools", "")
	do("root", "POST", "/api/promotions?action=approve&id="+upd, "")
	do("root", "GET", "/api/global-tools", "")
	do("root", "GET", "/api/global-tools?history=ct_pub", "")
	do("root", "GET", "/api/global-tools?history=ct_nope", "")

	do("alice", "POST", "/api/global-tools?action=rollback&tool=ct_pub&owner=bob&to=1", "")
	do("root", "POST", "/api/global-tools?action=undo&tool=ct_pub&owner=bob&to=1", "")
	do("root", "POST", "/api/global-tools?action=rollback&tool=ct_pub&owner=alice&to=1", "")
	do("root", "POST", "/api/global-tools?action=rollback&tool=ct_pub&owner=bob&to=one", "")
	do("root", "POST", "/api/global-tools?action=rollback&tool=ct_pub&owner=bob&to=99", "")
	do("root", "POST", "/api/global-tools?action=rollback&tool=ct_pub&owner=bob&to=1", "")
	do("root", "GET", "/api/global-tools", "")

	do("alice", "POST", "/api/app-switches?path=/zzcontractapp", `{"enabled":false}`)
	do("root", "POST", "/api/app-switches?path=/admin", `{"enabled":false}`)
	do("root", "POST", "/api/app-switches?path=/nope", `{"enabled":false}`)
	do("root", "POST", "/api/app-switches?path=/zzcontractapp", `{"enabled":`)
	do("root", "POST", "/api/app-switches?path=/zzcontractapp", ``)
	do("root", "POST", "/api/app-switches?path=/zzcontractapp", `{"enabled":false}`)
	do("root", "GET", "/api/app-switches", "")
	do("root", "POST", "/api/app-switches?path=/zzcontractapp", `{"enabled":true}`)
	do("root", "GET", "/api/app-switches", "")

	ct.activity(mux, adb, "root")
	ct.check(t, "testdata/governance_contract.golden")
}
