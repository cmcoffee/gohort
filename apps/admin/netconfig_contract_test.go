package admin

import (
	"encoding/json"
	"net/http"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/looptune"
	"github.com/cmcoffee/snugforge/kvlite"
)

// What the web search, page rendering, mail, network and agent-loop panels
// get back, through the real routes and the gate in front of them: each config
// read and saved, its secret kept on a save that sends the placeholder and
// never sent back, the hooks the search engine may be, and the search and mail
// tests (which reach nothing past this machine: no search tool is registered
// here, and the mail server is a closed local port). Each as an administrator,
// as a user who is not one, by the wrong method and with a broken or missing
// body, and then what the activity log says happened. Compared with
// testdata/netconfig_contract.golden.
//
// Run with UPDATE_GOLDEN=1 to rewrite the golden after a change that is meant.
func TestNetConfigRoutesContract(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prevAuth, prevRoot, prevBrowse, prevLoop := AuthDB, RootDB, LoadBrowseConfig(), looptune.GetAgentLoopTuning()
	AuthDB = func() Database { return adb }
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() {
		AuthDB, RootDB = prevAuth, prevRoot
		SetBrowseConfig(prevBrowse)
		looptune.SetAgentLoopTuning(prevLoop)
	})
	SetBrowseConfig(BrowseConfig{})

	ct := &contract{t: t, secrets: []string{"s3cr3t-search", "s3cr3t-mail"}}
	app := &AdminApp{}
	mux := http.NewServeMux()
	app.RegisterRoutes(mux, "")
	do := func(user, method, path, body string) { ct.run(mux, adb, user, method, path, body) }
	keeps := func(table, key, want string) {
		t.Helper()
		var got string
		adb.Get(table, key, &got)
		if got != want {
			t.Errorf("a save sending the placeholder left %s's %s as %q", table, key, got)
		}
	}

	for _, p := range []string{"/api/web-search", "/api/browse", "/api/mail", "/api/network", "/api/agent-loop-tuning"} {
		do("", "GET", p, "")
		do("alice", "GET", p, "")
		do("root", "GET", p, "")
		do("root", "PUT", p, "{}")
	}
	// The hooks are the process's (other tests add theirs): whether the
	// reply is a list is this test's to hold, not what is in it.
	ct.shape = func(out string) string {
		var rows []json.RawMessage
		if json.Unmarshal([]byte(out), &rows) == nil {
			return "a list"
		}
		return out
	}
	do("", "GET", "/api/web-search/hooks", "")
	do("alice", "GET", "/api/web-search/hooks", "")
	do("root", "GET", "/api/web-search/hooks", "")
	do("root", "POST", "/api/web-search/hooks", "{}")
	ct.shape = nil
	for _, p := range []string{"/api/web-search/test", "/api/mail/test"} {
		do("alice", "POST", p, `{}`)
		do("root", "GET", p, "")
		do("root", "POST", p, `{"provider":`)
		do("root", "POST", p, ``)
	}

	do("alice", "POST", "/api/web-search", `{"provider":"brave","api_key":"s3cr3t-search-x"}`)
	do("root", "POST", "/api/web-search", `{"provider":"brave","api_key":"s3cr3t-search","endpoint":"","hook":"left over"}`)
	do("root", "GET", "/api/web-search", "")
	do("root", "POST", "/api/web-search", `{"provider":"brave","api_key":"(unchanged)"}`)
	keeps(SearchTable, "api_key", "s3cr3t-search")
	do("root", "POST", "/api/web-search", `{"provider":"hook","hook":"zz_nope","api_key":"(unchanged)"}`)
	do("root", "POST", "/api/web-search", `{"source":"peer:nope"}`)
	do("root", "POST", "/api/web-search", `{"provider":`)
	do("root", "POST", "/api/web-search/test", `{"provider":""}`)
	do("root", "POST", "/api/web-search/test", `{"source":"peer:nope"}`)
	do("root", "POST", "/api/web-search/test", `{"provider":"brave","api_key":"(unchanged)"}`)

	do("alice", "POST", "/api/browse", `{"source":"local"}`)
	do("root", "POST", "/api/browse", `{"source":"peer:nope"}`)
	do("root", "POST", "/api/browse", `{"source":" local "}`)
	do("root", "GET", "/api/browse", "")
	do("root", "POST", "/api/browse", `{"source":""}`)
	do("root", "POST", "/api/browse", `{"source":`)

	do("alice", "POST", "/api/mail", `{"server":"smtp.example.test:587","password":"s3cr3t-mail-x"}`)
	do("root", "POST", "/api/mail", `{"server":"127.0.0.1:1","from":"ops@example.test","recipient":"","username":"ops","password":"s3cr3t-mail"}`)
	do("root", "GET", "/api/mail", "")
	do("root", "POST", "/api/mail", `{"server":"127.0.0.1:1","from":"ops@example.test","recipient":"admin@example.test","username":"ops","password":"(unchanged)"}`)
	keeps(MailTable, "password", "s3cr3t-mail")
	do("root", "POST", "/api/mail", `{"server":`)
	do("root", "POST", "/api/mail/test", `{"server":"127.0.0.1:1","from":"ops@example.test","password":"(unchanged)"}`)
	do("root", "POST", "/api/mail/test", `{"server":"127.0.0.1:1","from":"ops@example.test","recipient":"admin@example.test","password":"(unchanged)"}`)

	do("alice", "POST", "/api/network", `{"connect_timeout_seconds":5}`)
	do("root", "POST", "/api/network", `{"connect_timeout_seconds":5,"request_timeout_seconds":0}`)
	do("root", "GET", "/api/network", "")
	do("root", "POST", "/api/network", `{"connect_timeout_seconds":-1,"request_timeout_seconds":30}`)
	do("root", "GET", "/api/network", "")
	do("root", "POST", "/api/network", `{"connect_timeout_seconds":`)

	do("alice", "POST", "/api/agent-loop-tuning", `{"history_budget_percent":50}`)
	do("root", "POST", "/api/agent-loop-tuning", `{"history_budget_percent":50}`)
	do("root", "GET", "/api/agent-loop-tuning", "")
	do("root", "POST", "/api/agent-loop-tuning", `{"history_budget_percent":500}`)
	do("root", "GET", "/api/agent-loop-tuning", "")
	do("root", "POST", "/api/agent-loop-tuning", `{"history_budget_percent":`)

	ct.activity(mux, adb, "root")
	ct.check(t, "testdata/netconfig_contract.golden")
}
