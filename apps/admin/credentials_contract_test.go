package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// What Admin › APIs gets back, through the real routes and the gate in front of
// them: the credential list, one record, its audit, declaring tools and
// bindings; saving a bearer and an oauth2 credential; every toggle; the token
// test, saved and unsaved; and delete. Each as an administrator, as a user who
// is not one, by the wrong method and with a broken or missing body, and then
// what the activity log says happened. No reply may carry a secret that was
// saved. Compared with testdata/credentials_contract.golden.
//
// Run with UPDATE_GOLDEN=1 to rewrite the golden after a change that is meant.
func TestCredentialsRoutesContract(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prevAuth, prevRes := AuthDB, CredentialToolsResolver
	AuthDB = func() Database { return adb }
	CredentialToolsResolver = func(name string) []CredentialToolRef {
		if name == "ct_bearer" {
			return []CredentialToolRef{{Agent: "ct_agent", Tool: "ct_tool", Via: "fetch_via"}}
		}
		return nil
	}
	t.Cleanup(func() { AuthDB, CredentialToolsResolver = prevAuth, prevRes })
	// The vault is the process's own and outlives a test: start from none of
	// this test's credentials.
	for _, c := range Secure().ListWithPending() {
		if strings.HasPrefix(c.Name, "ct_") {
			Secure().Delete(c.Name)
		}
	}

	tokens := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"s3cr3t-minted-token","token_type":"bearer","expires_in":3600}`))
	}))
	defer tokens.Close()

	ct := &contract{
		t:       t,
		secrets: []string{"s3cr3t-bearer", "s3cr3t-client", "s3cr3t-pw", "s3cr3t-minted-token"},
		keep: func(row string) bool {
			return !strings.Contains(row, `"name":`) || strings.Contains(row, `"name":"ct_`)
		},
		rewrite: strings.NewReplacer(tokens.URL, "<tokens>"),
	}
	app := &AdminApp{}
	mux := http.NewServeMux()
	app.RegisterRoutes(mux, "")
	do := func(user, method, path, body string) { ct.run(mux, adb, user, method, path, body) }

	do("", "GET", "/api/secure-api", "")
	do("alice", "GET", "/api/secure-api", "")
	do("root", "PUT", "/api/secure-api", "{}")
	do("alice", "POST", "/api/secure-api", `{"name":"ct_mallory","type":"bearer","secret":"s3cr3t-bearer-x"}`)

	do("root", "POST", "/api/secure-api", `{"name":" ct_bearer ","type":"bearer","base_url":"https://api.example.test","description":" d ","secret":"s3cr3t-bearer-1"}`)
	do("root", "POST", "/api/secure-api", `{"name":"ct_oauth","type":"oauth2","grant":"client_credentials","token_url":"`+tokens.URL+`/token","client_id":"cid","base_url":"`+tokens.URL+`","secret":"s3cr3t-client-1"}`)
	do("root", "POST", "/api/secure-api", `{"name":"ct_oauth","type":"oauth2","description":"only the description resent"}`)
	do("root", "POST", "/api/secure-api", `{"name":"ct_pw","type":"oauth2","grant":"password","token_url":"`+tokens.URL+`/token","client_id":"cid","username":"svc","base_url":"`+tokens.URL+`","secret":"s3cr3t-client-2","password":"s3cr3t-pw-1"}`)
	do("root", "POST", "/api/secure-api", `{"name":"","type":"bearer","secret":"s3cr3t-bearer-2"}`)
	do("root", "POST", "/api/secure-api", `{"name":"Bad Name","type":"bearer","secret":"s3cr3t-bearer-3"}`)
	do("root", "POST", "/api/secure-api", `{"name":`)
	do("root", "POST", "/api/secure-api", ``)

	do("root", "GET", "/api/secure-api", "")
	for _, q := range []string{"name=ct_bearer", "name=ct_oauth", "name=ct_pw", "name=nope", "audit=ct_bearer", "audit=ct_bearer&owner=bob",
		"tools=ct_bearer", "tools=ct_oauth", "bindings=ct_bearer"} {
		do("root", "GET", "/api/secure-api?"+q, "")
		do("alice", "GET", "/api/secure-api?"+q, "")
	}

	for _, a := range []string{"disable", "enable", "secure", "unsecure"} {
		do("alice", "POST", "/api/secure-api?action="+a+"&name=ct_bearer", "")
		do("root", "POST", "/api/secure-api?action="+a+"&name=ct_bearer", "")
		do("root", "POST", "/api/secure-api?action="+a+"&name=nope", "")
	}
	do("root", "POST", "/api/secure-api?action=enable", "")
	do("root", "POST", "/api/secure-api?action=bogus&name=ct_bearer", "")
	do("root", "POST", "/api/secure-api?action=access&name=ct_bearer", `{"access_level":"secured"}`)
	do("root", "GET", "/api/secure-api?name=ct_bearer", "")
	do("root", "POST", "/api/secure-api?action=access&name=ct_bearer", `{"access_level":"open"}`)
	do("root", "POST", "/api/secure-api?action=access&name=ct_bearer", `{"access_level":"wide"}`)
	do("root", "POST", "/api/secure-api?action=access&name=ct_bearer", `{"access_level":`)
	do("root", "POST", "/api/secure-api?action=access&name=ct_bearer", ``)
	do("root", "POST", "/api/secure-api?action=secure&name=ct_bearer", "")
	do("root", "POST", "/api/secure-api?action=approve_binding&name=ct_bearer", "")
	do("root", "POST", "/api/secure-api?action=approve_binding&name=ct_bearer&tool=ct_tool", "")
	do("root", "POST", "/api/secure-api?action=revoke_binding&name=ct_bearer&tool=ct_other", "")
	do("root", "POST", "/api/secure-api?action=revoke_binding&name=ct_bearer", "")
	do("root", "GET", "/api/secure-api?bindings=ct_bearer", "")
	do("root", "POST", "/api/secure-api?action=test&name=ct_bearer", "")
	do("root", "POST", "/api/secure-api?action=test&name=ct_oauth", "")
	do("root", "POST", "/api/secure-api?action=test&name=ct_pw", "")
	do("root", "POST", "/api/secure-api?action=test&name=nope", "")
	do("alice", "POST", "/api/secure-api?action=test&name=ct_oauth", "")

	do("alice", "POST", "/api/secure-api/test", `{"name":"ct_oauth","type":"oauth2","grant":"client_credentials","token_url":"`+tokens.URL+`/token"}`)
	do("root", "GET", "/api/secure-api/test", "")
	do("root", "POST", "/api/secure-api/test", `{"name":"ct_oauth","type":"oauth2","grant":"client_credentials","token_url":"`+tokens.URL+`/token","client_id":"cid","base_url":"`+tokens.URL+`"}`)
	do("root", "POST", "/api/secure-api/test", `{"name":"ct_fresh","type":"oauth2","grant":"client_credentials","token_url":"`+tokens.URL+`/token","client_id":"cid","secret":"s3cr3t-client-3"}`)
	do("root", "POST", "/api/secure-api/test", `{"name":"ct_fresh","type":"oauth2","grant":"client_credentials","token_url":"`+tokens.URL+`/token"}`)
	do("root", "POST", "/api/secure-api/test", `{"name":"ct_oauth","type":"oauth2","grant":"client_credentials","token_url":"https://elsewhere.example.test/token","client_id":"cid"}`)
	do("root", "POST", "/api/secure-api/test", `{"name":"ct_bearer","type":"bearer"}`)
	do("root", "POST", "/api/secure-api/test", `{"name":`)
	do("root", "POST", "/api/secure-api/test", ``)

	do("alice", "DELETE", "/api/secure-api?name=ct_pw", "")
	do("root", "DELETE", "/api/secure-api", "")
	do("root", "DELETE", "/api/secure-api?name=nope", "")
	do("root", "DELETE", "/api/secure-api?name=ct_pw", "")
	do("root", "GET", "/api/secure-api", "")
	ct.activity(mux, adb, "root")

	ct.check(t, "testdata/credentials_contract.golden")
}
