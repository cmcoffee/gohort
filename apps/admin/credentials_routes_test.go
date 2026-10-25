package admin

// Admin › APIs reads and changes the deployment's credentials: the list, one
// record for the edit form, its audit, the tools that declare it and the
// bindings of a secured one; the toggles; the upsert; delete; and the token
// test. These pin what each request answers and what each change does.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

type credsFixture struct {
	mux   *http.ServeMux
	token string
}

func newCredsFixture(t *testing.T) *credsFixture {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	prevRoot, prevAuth, prevRes := RootDB, AuthDB, CredentialToolsResolver
	RootDB = db
	AuthDB = func() Database { return db }
	CredentialToolsResolver = nil
	t.Cleanup(func() { RootDB, AuthDB, CredentialToolsResolver = prevRoot, prevAuth, prevRes })
	AuthSetUser(db, "root", "pw", true)
	f := &credsFixture{mux: http.NewServeMux(), token: AuthCreateSession(db, "root")}
	(&AdminApp{db: db}).registerCredentialsRoutes(f.mux)
	return f
}

func (f *credsFixture) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: f.token})
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

func (f *credsFixture) ok(t *testing.T, method, path, body string) {
	t.Helper()
	if w := f.do(t, method, path, body); w.Code != http.StatusNoContent {
		t.Fatalf("%s %s answered %d %s", method, path, w.Code, w.Body.String())
	}
}

// credRow finds one credential in the list the page draws.
func (f *credsFixture) credRow(t *testing.T, name string) map[string]any {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal(f.do(t, "GET", "/api/secure-api", "").Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r["name"] == name {
			return r
		}
	}
	return nil
}

func TestTheAPIsPageSavesAndListsACredential(t *testing.T) {
	f := newCredsFixture(t)
	f.ok(t, "POST", "/api/secure-api", `{"name":" zz_cred_list ","type":"bearer","base_url":"https://api.example.test","description":" d ","secret":"s-1"}`)
	row := f.credRow(t, "zz_cred_list")
	if row == nil || row["access_level"] != "open" || row["namespace"] != "global" || row["description"] != "d" {
		t.Fatalf("the saved credential should be listed, trimmed, open and global: %+v", row)
	}
	w := f.do(t, "GET", "/api/secure-api?name=zz_cred_list", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"base_url":"https://api.example.test"`) || strings.Contains(w.Body.String(), "s-1") {
		t.Errorf("the edit form's record: %d %s", w.Code, w.Body.String())
	}
	if w := f.do(t, "GET", "/api/secure-api?name=zz_nothing", ""); w.Code != 404 {
		t.Errorf("an unknown record answered %d", w.Code)
	}
	if w := f.do(t, "GET", "/api/secure-api?audit=zz_cred_list", ""); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "null" {
		t.Errorf("a credential never called has no audit: %d %s", w.Code, w.Body.String())
	}

	// The declaring tools come from the resolver, and an unwired one lists none.
	if w := f.do(t, "GET", "/api/secure-api?tools=zz_cred_list", ""); strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("no resolver should list no tools: %s", w.Body.String())
	}
	CredentialToolsResolver = func(cred string) []CredentialToolRef {
		return []CredentialToolRef{{Agent: "Scout", Tool: "use_" + cred}}
	}
	if w := f.do(t, "GET", "/api/secure-api?tools=zz_cred_list", ""); !strings.Contains(w.Body.String(), `"tool":"use_zz_cred_list"`) {
		t.Errorf("the declaring tools: %s", w.Body.String())
	}

	f.ok(t, "DELETE", "/api/secure-api?name=zz_cred_list", "")
	if f.credRow(t, "zz_cred_list") != nil {
		t.Error("DELETE left the credential")
	}
}

func TestTheAPIsPageTogglesWhatItSays(t *testing.T) {
	f := newCredsFixture(t)
	f.ok(t, "POST", "/api/secure-api", `{"name":"zz_cred_tog","type":"bearer","base_url":"https://api.example.test","secret":"s-1"}`)
	load := func() SecureCredential {
		c, _ := Secure().Load("zz_cred_tog")
		return c
	}
	f.ok(t, "POST", "/api/secure-api?action=disable&name=zz_cred_tog", "")
	if !load().Disabled {
		t.Error("disable did not disable it")
	}
	f.ok(t, "POST", "/api/secure-api?action=enable&name=zz_cred_tog", "")
	if load().Disabled {
		t.Error("enable did not enable it")
	}
	f.ok(t, "POST", "/api/secure-api?action=secure&name=zz_cred_tog", "")
	if !load().Secured {
		t.Error("secure did not secure it")
	}
	// Secured and declared by nothing: nothing can reach it, and the row says so.
	CredentialToolsResolver = func(string) []CredentialToolRef { return nil }
	if row := f.credRow(t, "zz_cred_tog"); row["access_level"] != "secured" || row["orphaned"] != true {
		t.Errorf("a secured credential nothing declares should read orphaned: %+v", row)
	}
	f.ok(t, "POST", "/api/secure-api?action=unsecure&name=zz_cred_tog", "")
	if load().Secured {
		t.Error("unsecure did not release it")
	}
	f.ok(t, "POST", "/api/secure-api?action=access&name=zz_cred_tog", `{"access_level":"secured"}`)
	if !load().Secured {
		t.Error("access=secured did not secure it")
	}
	f.ok(t, "POST", "/api/secure-api?action=access&name=zz_cred_tog", `{"access_level":"open"}`)
	if load().Secured {
		t.Error("access=open did not open it")
	}

	f.ok(t, "POST", "/api/secure-api?action=approve_binding&name=zz_cred_tog&tool=reader", "")
	f.ok(t, "POST", "/api/secure-api?action=revoke_binding&name=zz_cred_tog&tool=writer", "")
	w := f.do(t, "GET", "/api/secure-api?bindings=zz_cred_tog", "")
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, r := range rows {
		got = append(got, r["tool"].(string)+":"+r["status"].(string))
	}
	if strings.Join(got, " ") != "reader:bound writer:revoked" {
		t.Errorf("bindings: %v", got)
	}

	// The token test answers in its own shape, never as an error status.
	w = f.do(t, "POST", "/api/secure-api?action=test&name=zz_cred_tog", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":false`) {
		t.Errorf("a token test of a bearer key: %d %s", w.Code, w.Body.String())
	}
}

// Adding only the secret to a drafted OAuth credential keeps its config.
func TestAddingTheSecretKeepsTheOAuthConfig(t *testing.T) {
	f := newCredsFixture(t)
	f.ok(t, "POST", "/api/secure-api", `{"name":"zz_cred_oauth","type":"oauth2","base_url":"https://api.example.test","grant":"client_credentials","token_url":"https://auth.example.test/token","client_id":"cid","scope":"read","secret":"first"}`)
	f.ok(t, "POST", "/api/secure-api", `{"name":"zz_cred_oauth","type":"oauth2","base_url":"https://api.example.test","secret":"second"}`)
	c, _ := Secure().Load("zz_cred_oauth")
	if c.Grant != "client_credentials" || c.TokenURL != "https://auth.example.test/token" || c.ClientID != "cid" || c.Scope != "read" {
		t.Errorf("the OAuth config should be kept when only the secret is sent: %+v", c)
	}
}

func TestTheAPIsPageRefusesWhatItCannotDo(t *testing.T) {
	f := newCredsFixture(t)
	for _, tc := range []struct {
		method, path, body string
		code               int
		want               string
	}{
		{"POST", "/api/secure-api?action=enable", "", 400, "missing name"},
		{"POST", "/api/secure-api?action=zz&name=x", "", 400, "action must be enable|disable"},
		{"POST", "/api/secure-api?action=access&name=x", `{"access_level":"wide"}`, 400, "access_level must be open|secured"},
		{"POST", "/api/secure-api?action=approve_binding&name=x", "", 400, "missing tool"},
		{"POST", "/api/secure-api?action=revoke_binding&name=x", "", 400, "missing tool"},
		{"POST", "/api/secure-api", "not json", 400, "invalid JSON"},
		{"DELETE", "/api/secure-api", "", 400, "missing name"},
		{"PUT", "/api/secure-api", "", 405, "method not allowed"},
		{"GET", "/api/secure-api/test", "", 405, "method not allowed"},
		{"POST", "/api/secure-api/test", "not json", 400, "invalid JSON"},
		{"POST", "/api/secure-api/test", `{"name":"zz_unsaved","type":"oauth2"}`, 200, `"ok":false`},
	} {
		w := f.do(t, tc.method, tc.path, tc.body)
		if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.want) {
			t.Errorf("%s %s answered %d %q, want %d with %q", tc.method, tc.path, w.Code, w.Body.String(), tc.code, tc.want)
		}
	}
}
