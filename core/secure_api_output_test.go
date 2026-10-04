package core

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/kvlite"
)

// A URL that reads as one path to the allow-list and another to the server is
// not sent with a credential: traversal, encoded separators, fragments,
// backslashes, a user name. And a "*" in a pattern's host matches only hosts,
// never a query or fragment that happens to end like one.
func TestCredentialURLsAreMatchedAsTheServerReadsThem(t *testing.T) {
	for _, u := range []string{
		"https://api.example/v1/../admin", "https://api.example/v1/%2e%2e/admin", "https://api.example/v1%2Fadmin",
		"https://api.example/v1#/other", `https://api.example/v1\admin`, "https://user@api.example/v1",
	} {
		if why := credentialURLTrick(u); why == "" {
			t.Errorf("%s passed as plain", u)
		}
	}
	if why := credentialURLTrick("https://api.example/v1/items?q=a/b"); why != "" {
		t.Errorf("a plain URL is refused: %s", why)
	}
	c := SecureCredential{Name: "corp", AllowedURLPattern: "https://*.corp.example/**"}
	if urlAllowedByCredential(c, "https://evil.example?.corp.example/x") {
		t.Error("a query that ends like the host matched a host wildcard")
	}
	if !urlAllowedByCredential(c, "https://api.corp.example/x") {
		t.Error("a real subdomain is allowed")
	}
	c = SecureCredential{Name: "gh", BaseURL: "https://api.example", AllowedEndpoints: []string{"/v1/*"}, DeniedURLPatterns: []string{"https://api.example/v1/admin/**"}}
	if urlAllowedByCredential(c, "https://api.example/v1/../admin/x") {
		t.Error("traversal left the allowed endpoints")
	}

	secureAPITestStore(t)
	for _, b := range []string{"https://*.corp.example", "https://u:p@api.example", "https://api.example?x=1"} {
		if err := Secure().Save(SecureCredential{Name: "bad", Type: SecureCredBearer, BaseURL: b}, "k"); err == nil {
			t.Errorf("base url %s was saved", b)
		}
	}
}

// A key is scrubbed in every form it can come back in: raw, percent-encoded in
// a quoted URL, JSON-escaped in a body that is re-encoded for display, and in
// a text reply saved to the workspace.
func TestAKeyIsScrubbedInEveryForm(t *testing.T) {
	secureAPITestStore(t)
	const key = "k/ey+with=chars99"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"query":%q,"escaped":"%s","raw":%q}`, r.URL.RawQuery, strings.ReplaceAll(key, "/", `\/`), key)
	}))
	defer srv.Close()
	if err := Secure().Save(SecureCredential{Name: "echo", Type: SecureCredQuery, ParamName: "api_key", BaseURL: srv.URL}, key); err != nil {
		t.Fatal(err)
	}
	out, err := Secure().DispatchToolCall(&ToolSession{Username: "root"}, "echo", srv.URL+"/x", "GET", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, form := range []string{key, url.QueryEscape(key), strings.ReplaceAll(key, "/", `\/`)} {
		if strings.Contains(out, form) {
			t.Errorf("the key came back as %q:\n%s", form, out)
		}
	}

	ws := t.TempDir()
	if _, err := Secure().DispatchToolCallArgs(&ToolSession{Username: "root", WorkspaceDir: ws}, "echo", map[string]any{"url": srv.URL + "/x", "save_to": "reply.json"}); err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(filepath.Join(ws, "reply.json"))
	if len(saved) == 0 || strings.Contains(string(saved), key) || strings.Contains(string(saved), url.QueryEscape(key)) {
		t.Errorf("the saved reply keeps the key:\n%s", saved)
	}
}

// The daily cap counts on its own, so a cap above the audit ring's 50 rows is
// reached; a call that never reached its server gives its place back.
func TestTheDailyCapCountsPastTheAuditRing(t *testing.T) {
	secureAPITestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) }))
	defer srv.Close()
	if err := Secure().Save(SecureCredential{Name: "capped", Type: SecureCredBearer, BaseURL: srv.URL, MaxCallsPerDay: 60}, "k"); err != nil {
		t.Fatal(err)
	}
	sess := &ToolSession{Username: "root"}
	for i := 0; i < 60; i++ {
		if _, err := Secure().DispatchToolCall(sess, "capped", srv.URL+"/x", "GET", ""); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
	if _, err := Secure().DispatchToolCall(sess, "capped", srv.URL+"/x", "GET", ""); err == nil || !strings.Contains(err.Error(), "daily cap") {
		t.Errorf("the 61st call is over the cap: %v", err)
	}
	c, _ := Secure().Load("capped")
	Secure().releaseDailyCall(c)
	if _, err := Secure().DispatchToolCall(sess, "capped", srv.URL+"/x", "GET", ""); err != nil {
		t.Errorf("a place given back is usable: %v", err)
	}
}

// An OAuth credential's token goes only inside its allow-list, and a token
// request follows no redirect.
func TestOAuthTokensStayWhereTheyBelong(t *testing.T) {
	secureAPITestStore(t)
	if err := Secure().Save(SecureCredential{Name: "sso", Type: SecureCredOAuth2, Grant: OAuthGrantClientCredentials, ClientID: "c",
		TokenURL: "https://sso.example/token", BaseURL: "https://api.example"}, "client-secret"); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", "https://elsewhere.example/mcp", nil)
	if err := Secure().AuthorizeRequest("sso", req); err == nil || !strings.Contains(err.Error(), "does not cover") {
		t.Errorf("a token went outside its credential: %v", err)
	}
	moved := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://attacker.example/steal", http.StatusTemporaryRedirect)
	}))
	defer moved.Close()
	if _, err := tokenHTTPClient().Post(moved.URL, "application/x-www-form-urlencoded", strings.NewReader("client_secret=x")); err == nil || !strings.Contains(err.Error(), "not followed") {
		t.Errorf("a token request followed a redirect: %v", err)
	}
}

// An MCP server moved to another address keeps none of the tokens it was
// given for the old one, and one moved within its own address keeps them.
func TestAMovedMCPServerDropsItsTokens(t *testing.T) {
	m := &MCPManager{db: &DBase{Store: kvlite.MemStore()}, conns: map[string]*mcpConn{}, userConns: map[string]*mcpConn{},
		proxies: map[string]*mcpProxyTool{}, dialFail: map[string]time.Time{}}
	if err := m.Save(MCPServerConfig{Name: "docs", URL: "https://docs.example/mcp", AuthMode: MCPAuthBearer}, "bearer-1"); err != nil {
		t.Fatal(err)
	}
	m.db.CryptSet(mcpServersTable, mcpOAuthTokKey("docs", "alice"), "alice-token")
	m.db.CryptSet(mcpServersTable, mcpOAuthCfgKey("docs"), "cfg")
	if err := m.Save(MCPServerConfig{Name: "docs", URL: "https://docs.example/v2/mcp", AuthMode: MCPAuthBearer}, ""); err != nil {
		t.Fatal(err)
	}
	var v string
	if !m.db.Get(mcpServersTable, mcpOAuthTokKey("docs", "alice"), &v) {
		t.Fatal("a move within the same address dropped the tokens")
	}
	if err := m.Save(MCPServerConfig{Name: "docs", URL: "https://collector.example/mcp", AuthMode: MCPAuthBearer}, ""); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{mcpTokenKey("docs"), mcpOAuthTokKey("docs", "alice"), mcpOAuthCfgKey("docs")} {
		if m.db.Get(mcpServersTable, k, &v) {
			t.Errorf("%s survived a move to another address", k)
		}
	}
}

// A raw key handed to a script is scrubbed from what it prints.
func TestAScriptDoesNotPrintAKeyItWasGiven(t *testing.T) {
	h := &SandboxHook{}
	h.handedOut = []string{"s3cret/key+1"}
	out := h.ScrubHandedOut("GET https://api.example/x?key=" + url.QueryEscape("s3cret/key+1") + " failed; key was s3cret/key+1")
	if strings.Contains(out, "s3cret") {
		t.Errorf("the key is in the output: %s", out)
	}
}

// A credential with no endpoint list allows its base URL itself, with or
// without a query, as well as everything under it; not a longer host or a
// sibling path that only starts with the same text.
func TestACredentialAllowsItsOwnBaseURL(t *testing.T) {
	c := SecureCredential{Name: "jobs", BaseURL: "http://127.0.0.1:37185/fixture/jobs/"}
	for _, u := range []string{
		"http://127.0.0.1:37185/fixture/jobs",
		"http://127.0.0.1:37185/fixture/jobs/",
		"http://127.0.0.1:37185/fixture/jobs?q=nurse",
		"http://127.0.0.1:37185/fixture/jobs/v1/jobs?q=nurse",
	} {
		if !urlAllowedByCredential(c, u) {
			t.Errorf("%s was refused", u)
		}
	}
	for _, u := range []string{
		"http://127.0.0.1:37185/fixture/jobsx",
		"http://127.0.0.1:37185/fixture",
		"http://127.0.0.1:371850/fixture/jobs",
	} {
		if urlAllowedByCredential(c, u) {
			t.Errorf("%s was allowed", u)
		}
	}
}

// A URL on a credential's host but outside its base path is not covered, and
// the credential is named so a refusal can say what was meant; a covered URL,
// or one on another host, names none.
func TestSameHostCredentialsNamesTheBaseTheURLMissed(t *testing.T) {
	secureAPITestStore(t)
	if err := Secure().Save(SecureCredential{Name: "todo", Type: SecureCredBearer, BaseURL: "http://127.0.0.1:37185/fixture/todo"}, "k"); err != nil {
		t.Fatal(err)
	}
	got := Secure().SameHostCredentials("http://127.0.0.1:37185/v1/tasks?status=open", "")
	if len(got) != 1 || got[0] != "todo (http://127.0.0.1:37185/fixture/todo)" {
		t.Fatalf("near = %v", got)
	}
	for _, u := range []string{"http://127.0.0.1:37185/fixture/todo/v1/tasks", "http://127.0.0.1:37185/fixture/todo?status=open", "http://127.0.0.1:9999/v1/tasks"} {
		if got := Secure().SameHostCredentials(u, ""); len(got) != 0 {
			t.Errorf("%s named %v", u, got)
		}
	}
	if name, err := Secure().AutoRouteCredential("http://127.0.0.1:37185/fixture/todo?status=open"); err != nil && !strings.Contains(err.Error(), "todo") || err == nil && name != "todo" {
		t.Errorf("the base with a query is not routed to its credential: %q %v", name, err)
	}
}
