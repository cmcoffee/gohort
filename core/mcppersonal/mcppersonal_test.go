package mcppersonal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func withStore(t *testing.T) {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(db, "boss", "pw", true)
	AuthSetUser(db, "dana", "pw", false)
	prev := AuthDB
	AuthDB = func() Database { return db }
	t.Cleanup(func() { AuthDB = prev })
}

// fakeMCP is a server with two tools that says what it was called with and
// with which token.
func fakeMCP(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(fakeMCPHandler))
	t.Cleanup(srv.Close)
	return srv
}

func fakeMCPHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     any            `json:"id"`
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if strings.HasPrefix(req.Method, "notifications/") {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var result any
	switch req.Method {
	case "initialize":
		result = map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "fake"}}
	case "tools/list":
		result = map[string]any{"tools": []map[string]any{
			{"name": "search_issues", "description": "Find issues.", "inputSchema": map[string]any{"type": "object",
				"properties": map[string]any{"q": map[string]any{"type": "string"}}, "required": []string{"q"}}},
			{"name": "create_issue", "description": "File an issue.", "inputSchema": map[string]any{"type": "object"}},
		}}
	case "tools/call":
		args, _ := req.Params["arguments"].(map[string]any)
		result = map[string]any{"content": []map[string]any{{"type": "text",
			"text": "called " + req.Params["name"].(string) + " q=" + strings.TrimSpace(func() string { s, _ := args["q"].(string); return s }()) + " auth=" + r.Header.Get("Authorization")}}}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
}

// Someone who is not an administrator may not point a server inward or send
// a token over plain http; an administrator may.
func TestAPersonalServerReachesPublicAddressesOnly(t *testing.T) {
	withStore(t)
	for _, u := range []string{"https://127.0.0.1/mcp", "https://10.0.0.4/mcp", "https://nas.local/mcp", "http://mcp.example.com/mcp"} {
		if err := Save(Server{Owner: "dana", Name: "x", URL: u}, "", false); err == nil {
			t.Errorf("%s was accepted for someone who is not an administrator", u)
		}
	}
	if err := Save(Server{Owner: "dana", Name: "pub", URL: "https://mcp.example.com/mcp"}, "", false); err != nil {
		t.Errorf("a public https server: %v", err)
	}
	if err := Save(Server{Owner: "boss", Name: "nas", URL: "http://10.0.0.4/mcp"}, "", false); err != nil {
		t.Errorf("an administrator's internal server: %v", err)
	}
	if err := Save(Server{Owner: "dana", Name: "Bad Name", URL: "https://mcp.example.com/mcp"}, "", false); err == nil {
		t.Error("a name with spaces was accepted")
	}
	if err := Save(Server{Owner: "dana", Name: "odd", URL: "https://mcp.example.com/mcp", Auth: "kerberos"}, "", false); err == nil {
		t.Error("a sign-in this does not know was accepted")
	}
}

// Held for approval when added under Needs approval: off, the owner's switch
// refused with the reason, and an administrator's enable approves it. An
// administrator's disable holds against the owner too.
func TestApprovalAndTheTwoSwitches(t *testing.T) {
	withStore(t)
	if err := Save(Server{Owner: "dana", Name: "held", URL: "https://mcp.example.com/mcp"}, "", true); err != nil {
		t.Fatal(err)
	}
	s, _ := Load("dana", "held")
	if s.Usable() || !s.AwaitingApproval {
		t.Fatalf("a new one under approval should wait: %+v", s)
	}
	if err := SetDisabled("dana", "held", false); err == nil || !strings.Contains(err.Error(), "waiting for an administrator") {
		t.Errorf("the owner switched on one that waits: %v", err)
	}
	// An edit keeps it waiting.
	if err := Save(Server{Owner: "dana", Name: "held", URL: "https://mcp.example.com/v2"}, "", false); err != nil {
		t.Fatal(err)
	}
	if s, _ := Load("dana", "held"); !s.AwaitingApproval {
		t.Error("an edit lifted the wait")
	}
	if err := AdminSetDisabled("dana", "held", false); err != nil {
		t.Fatal(err)
	}
	if s, _ := Load("dana", "held"); !s.Usable() {
		t.Errorf("approval should switch it on: %+v", s)
	}
	if err := AdminSetDisabled("dana", "held", true); err != nil {
		t.Fatal(err)
	}
	if err := SetDisabled("dana", "held", false); err == nil {
		t.Error("the owner lifted an administrator's hold")
	}
	if err := Remove("dana", "held"); err != nil || len(List("dana")) != 0 {
		t.Errorf("remove: %v %v", err, List("dana"))
	}
}

// Live: a check records what the server offers; the owner's tools are named
// and shaped as an administrator's server's would be, carry the token, ask
// first for one that changes things, and answer no one but the owner.
func TestTheOwnersToolsReachTheServer(t *testing.T) {
	withStore(t)
	srv := fakeMCP(t)
	// An administrator's own, so the test server's loopback address is allowed.
	if err := Save(Server{Owner: "boss", Name: "tracker", URL: srv.URL, Auth: AuthBearer}, "sekret", false); err != nil {
		t.Fatal(err)
	}
	s, err := Check(context.Background(), "boss", "tracker")
	if err != nil || len(s.Tools) != 2 {
		t.Fatalf("check: %v %+v", err, s.Tools)
	}
	defs := Tools(&ToolSession{Username: "boss"}, "boss", map[string]bool{"tracker_create_issue": true})
	byName := map[string]AgentToolDef{}
	for _, d := range defs {
		byName[d.Tool.Name] = d
	}
	search, ok := byName["tracker_search_issues"]
	if !ok || len(search.Tool.Required) != 1 || search.NeedsConfirm {
		t.Fatalf("the search tool: %+v (all: %v)", search, byName)
	}
	if _, clash := byName["tracker_create_issue"]; clash {
		t.Error("a taken name was reused")
	}
	for name, d := range byName {
		if strings.Contains(name, "create_issue") && !d.NeedsConfirm {
			t.Errorf("%s changes something and should ask first", name)
		}
	}
	out, err := search.Handler(context.Background(), map[string]any{"q": "login bug"})
	if err != nil || !strings.Contains(out, "called search_issues q=login bug auth=Bearer sekret") {
		t.Errorf("the call: %q %v", out, err)
	}
	other := Tools(&ToolSession{Username: "dana"}, "boss", nil)
	if len(other) == 0 {
		t.Fatal("no tools to try as someone else")
	}
	if _, err := other[0].Handler(context.Background(), map[string]any{"q": "x"}); err == nil {
		t.Error("someone else's run reached the owner's server")
	}
	if err := SetDisabled("boss", "tracker", true); err != nil {
		t.Fatal(err)
	}
	if got := Tools(&ToolSession{Username: "boss"}, "boss", nil); len(got) != 0 {
		t.Errorf("a switched-off server still offers tools: %d", len(got))
	}
}

// A server moved to another address loses its token and its tool list.
func TestMovingAServerDropsWhatItWasGiven(t *testing.T) {
	withStore(t)
	srv := fakeMCP(t)
	if err := Save(Server{Owner: "boss", Name: "tracker", URL: srv.URL, Auth: AuthBearer}, "sekret", false); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(context.Background(), "boss", "tracker"); err != nil {
		t.Fatal(err)
	}
	if err := Save(Server{Owner: "boss", Name: "tracker", URL: "https://elsewhere.example.com/mcp", Auth: AuthBearer}, "", false); err != nil {
		t.Fatal(err)
	}
	s, _ := Load("boss", "tracker")
	var tok string
	AuthDB().Get(table, tokenKey("boss", "tracker"), &tok)
	if len(s.Tools) != 0 || tok != "" {
		t.Errorf("the move kept the old server's tools or token: %d tools, token %q", len(s.Tools), tok)
	}
}

// The save check reads the URL as written; the connection is checked again on
// the address actually dialled, so a name that resolves inward is refused
// there for someone who is not an administrator.
func TestTheDialIsCheckedToo(t *testing.T) {
	withStore(t)
	srv := fakeMCP(t)
	if _, err := httpClient("dana").Get(srv.URL); err == nil {
		t.Error("someone who is not an administrator reached a loopback address")
	}
	if resp, err := httpClient("boss").Get(srv.URL); err != nil {
		t.Errorf("an administrator's client: %v", err)
	} else {
		resp.Body.Close()
	}
}

// Names are given across all of a person's servers, on or off, so switching
// one off never renames another's tools: a name sits on an agent's list.
func TestNamesStayPutWhenAServerIsSwitchedOff(t *testing.T) {
	withStore(t)
	srv := fakeMCP(t)
	// "a_search" + "issues" and "a" + "search_issues" both come out a_search_issues.
	for _, name := range []string{"a", "a_search"} {
		if err := Save(Server{Owner: "boss", Name: name, URL: srv.URL}, "", false); err != nil {
			t.Fatal(err)
		}
		if _, err := Check(context.Background(), "boss", name); err != nil {
			t.Fatal(err)
		}
	}
	before := map[string]string{}
	tools, _ := namedTools("boss", nil)
	for _, n := range tools {
		before[n.server.Name+"/"+n.raw] = n.name
	}
	if err := SetDisabled("boss", "a", true); err != nil {
		t.Fatal(err)
	}
	for _, d := range Tools(&ToolSession{Username: "boss"}, "boss", nil) {
		found := false
		for _, n := range before {
			if n == d.Tool.Name {
				found = true
			}
		}
		if !found {
			t.Errorf("switching another server off renamed %s", d.Tool.Name)
		}
	}
	if !Offers("boss", before["a/search_issues"], nil) {
		t.Error("a switched-off server's tool should still be one the owner's agents may list")
	}
}

// Offered as one tool: the server's own name, an action per tool. A read runs
// as it is; a tool whose name says it changes something asks in the chat,
// and is refused where nobody is watching to say yes.
func TestOneToolAndAskingFirst(t *testing.T) {
	withStore(t)
	srv := fakeMCP(t)
	if err := Save(Server{Owner: "boss", Name: "tracker", URL: srv.URL, OneTool: true}, "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(context.Background(), "boss", "tracker"); err != nil {
		t.Fatal(err)
	}
	asked := ""
	sess := &ToolSession{Username: "boss", AskInChat: func(prompt, detail string, yes []string) string { asked = prompt; return "" }}
	defs := Tools(sess, "boss", nil)
	if len(defs) != 1 || defs[0].Tool.Name != "tracker" {
		t.Fatalf("one tool named for the server: %v", defs)
	}
	out, err := defs[0].Handler(context.Background(), map[string]any{"action": "search_issues", "q": "login"})
	if err != nil || !strings.Contains(out, "called search_issues q=login") || asked != "" {
		t.Fatalf("a read through the one tool: %q %v (asked %q)", out, err, asked)
	}
	out, _ = defs[0].Handler(context.Background(), map[string]any{"action": "create_issue"})
	if asked == "" || !strings.Contains(out, "not approved") {
		t.Errorf("a write should ask, and a no should not run it: %q (asked %q)", out, asked)
	}
	unattended := Tools(&ToolSession{Username: "boss"}, "boss", nil)
	if _, err := unattended[0].Handler(context.Background(), map[string]any{"action": "create_issue"}); err == nil || !strings.Contains(err.Error(), "nobody is watching") {
		t.Errorf("a write with nobody to ask should be refused: %v", err)
	}
	yes := &ToolSession{Username: "boss", AskInChat: func(string, string, []string) string { return "Run it" }}
	if out, err := Tools(yes, "boss", nil)[0].Handler(context.Background(), map[string]any{"action": "create_issue"}); err != nil || !strings.Contains(out, "called create_issue") {
		t.Errorf("an approved write: %q %v", out, err)
	}
	// Either shape's names reach it: the one tool answers to its tools' names.
	if !Offers("boss", "tracker", nil) || !Offers("boss", "tracker_search_issues", nil) {
		t.Error("an agent may list the server's tools by either shape's names")
	}
	if a := Aliases("boss", nil)["tracker"]; len(a) != 2 {
		t.Errorf("the one tool should answer to both tools' names: %v", a)
	}
}

// A server can sign in with one of its owner's API credentials: the key goes
// on every request, held to the credential's own allow-list.
func TestASignInWithAnAPICredential(t *testing.T) {
	withStore(t)
	srv := fakeMCP(t)
	if err := Save(Server{Owner: "boss", Name: "creds", URL: srv.URL, Auth: AuthCredential, Credential: "nothing"}, "", false); err == nil {
		t.Error("a server was saved naming a credential that does not exist")
	}
	if err := Secure().Save(SecureCredential{Name: "trk", Type: SecureCredBearer, BaseURL: srv.URL, Owner: "boss"}, "cred-key"); err != nil {
		t.Fatal(err)
	}
	if err := Save(Server{Owner: "boss", Name: "creds", URL: srv.URL, Auth: AuthCredential, Credential: "trk"}, "", false); err != nil {
		t.Fatal(err)
	}
	if s, err := Check(context.Background(), "boss", "creds"); err != nil || len(s.Tools) != 2 {
		t.Fatalf("check: %v %+v", err, s.Tools)
	}
	var search AgentToolDef
	for _, d := range Tools(&ToolSession{Username: "boss"}, "boss", nil) {
		if d.Tool.Name == "creds_search_issues" {
			search = d
		}
	}
	if search.Handler == nil {
		t.Fatal("no search tool")
	}
	if out, err := search.Handler(context.Background(), map[string]any{"q": "x"}); err != nil || !strings.Contains(out, "auth=Bearer cred-key") {
		t.Errorf("the call: %q %v", out, err)
	}
	// Narrowed to another address, the key stops going.
	if err := Secure().Save(SecureCredential{Name: "trk", Type: SecureCredBearer, BaseURL: "https://elsewhere.example.com", Owner: "boss"}, "cred-key"); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(context.Background(), "boss", "creds"); err == nil || !strings.Contains(err.Error(), "does not cover") {
		t.Errorf("the key went past the credential's allow-list: %v", err)
	}
}

// fakeOAuthMCP is fakeMCP behind its own OAuth: discovery, registration and
// a token endpoint, with the MCP endpoint answering only its access token.
func fakeOAuthMCP(t *testing.T) *httptest.Server {
	t.Helper()
	return fakeOAuthMCPWith(t, true, nil)
}

// fakeOAuthMCPWith is fakeOAuthMCP, registering clients itself only when
// dcr is set, and handing each token request's form to seen.
func fakeOAuthMCPWith(t *testing.T, dcr bool, seen func(url.Values)) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	base := srv.URL
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"resource": base + "/mcp", "authorization_servers": []string{base}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		meta := map[string]any{"issuer": base, "authorization_endpoint": base + "/authorize", "token_endpoint": base + "/token"}
		if dcr {
			meta["registration_endpoint"] = base + "/register"
		}
		json.NewEncoder(w).Encode(meta)
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"client_id": "client-1"})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if seen != nil {
			seen(r.Form)
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "acc-1", "refresh_token": "ref-1", "expires_in": 3600})
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer acc-1" {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+base+`/.well-known/oauth-protected-resource"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fakeMCPHandler(w, r)
	})
	return srv
}

// A server can sign in with OAuth: listed under Connected accounts until its
// owner signs in, then its tools answer on the owner's token. Moving it, or
// removing it, drops the sign-in.
func TestASignInWithOAuth(t *testing.T) {
	withStore(t)
	srv := fakeOAuthMCP(t)
	if err := Save(Server{Owner: "boss", Name: "hosted", URL: srv.URL + "/mcp", Auth: AuthOAuth}, "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(context.Background(), "boss", "hosted"); err == nil || !strings.Contains(err.Error(), "Connect") {
		t.Errorf("a server not signed in to should say to connect: %v", err)
	}
	conns := Connections("boss")
	if len(conns) != 1 || conns[0].Connected || conns[0].Kind != ConnKind || !strings.Contains(conns[0].ConnectURL, "own=1") {
		t.Fatalf("the Connected accounts entry: %+v", conns)
	}
	if got := Connections("dana"); len(got) != 0 {
		t.Errorf("someone else's list shows it: %+v", got)
	}
	authURL, err := StartConnect("boss", "hosted", "https://gohort.example.com/account/mcp/callback")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(authURL)
	state := u.Query().Get("state")
	if err := MCP().CompleteOAuth(state, "code-1", "boss"); err != nil {
		t.Fatal(err)
	}
	AfterConnect(context.Background(), "boss")
	s, _ := Load("boss", "hosted")
	if !Connected("boss", "hosted") || len(s.Tools) != 2 || s.CheckErr != "" {
		t.Fatalf("after signing in: connected=%v %+v", Connected("boss", "hosted"), s)
	}
	if s.SignIn() != "OAuth, signed in" {
		t.Errorf("sign-in label: %q", s.SignIn())
	}
	var search AgentToolDef
	for _, d := range Tools(&ToolSession{Username: "boss"}, "boss", nil) {
		if d.Tool.Name == "hosted_search_issues" {
			search = d
		}
	}
	if search.Handler == nil {
		t.Fatal("no search tool")
	}
	if out, err := search.Handler(context.Background(), map[string]any{"q": "x"}); err != nil || !strings.Contains(out, "auth=Bearer acc-1") {
		t.Errorf("the call: %q %v", out, err)
	}
	// The same address keeps it; another drops it.
	if err := Save(Server{Owner: "boss", Name: "hosted", URL: srv.URL + "/mcp", Auth: AuthOAuth, OneTool: true}, "", false); err != nil || !Connected("boss", "hosted") {
		t.Errorf("an edit in place dropped the sign-in: %v", err)
	}
	if err := Save(Server{Owner: "boss", Name: "hosted", URL: "https://mcp.example.com/mcp", Auth: AuthOAuth}, "", false); err != nil || Connected("boss", "hosted") {
		t.Errorf("a server moved elsewhere kept the sign-in: %v", err)
	}
	if _, ok := MCP().Load("personal:boss/hosted"); ok {
		t.Error("the sign-in made an administrator's server record")
	}
}

// A provider that does not register clients itself takes one its owner
// registered there: its client ID and secret go to the token endpoint, and
// changing the client drops the sign-in made with the old one.
func TestASignInWithAClientRegisteredByHand(t *testing.T) {
	withStore(t)
	var form url.Values
	srv := fakeOAuthMCPWith(t, false, func(f url.Values) { form = f })
	const cb = "https://gohort.example.com/account/mcp/callback"
	if err := Save(Server{Owner: "boss", Name: "manual", URL: srv.URL + "/mcp", Auth: AuthOAuth}, "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := StartConnect("boss", "manual", cb); err == nil || !strings.Contains(err.Error(), "Client ID") {
		t.Fatalf("a provider with no registration should say to register an app: %v", err)
	}
	if err := Save(Server{Owner: "boss", Name: "manual", URL: srv.URL + "/mcp", Auth: AuthOAuth, OAuthClientID: "my-app", OAuthScopes: "read  write"}, "app-secret", false); err != nil {
		t.Fatal(err)
	}
	authURL, err := StartConnect("boss", "manual", cb)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(authURL)
	if u.Query().Get("client_id") != "my-app" || u.Query().Get("scope") != "read write" {
		t.Errorf("the authorize request: %s", authURL)
	}
	if err := MCP().CompleteOAuth(u.Query().Get("state"), "code-1", "boss"); err != nil {
		t.Fatal(err)
	}
	if form.Get("client_id") != "my-app" || form.Get("client_secret") != "app-secret" {
		t.Errorf("the token request: %v", form)
	}
	AfterConnect(context.Background(), "boss")
	if s, _ := Load("boss", "manual"); !Connected("boss", "manual") || len(s.Tools) != 2 {
		t.Fatalf("after signing in: %+v", s)
	}
	// Saved again as it is, the secret and the sign-in stay.
	if err := Save(Server{Owner: "boss", Name: "manual", URL: srv.URL + "/mcp", Auth: AuthOAuth, OAuthClientID: "my-app", OAuthScopes: "read write"}, "", false); err != nil || !Connected("boss", "manual") {
		t.Errorf("an unchanged save dropped the sign-in: %v", err)
	}
	if err := Save(Server{Owner: "boss", Name: "manual", URL: srv.URL + "/mcp", Auth: AuthOAuth, OAuthClientID: "other-app", OAuthScopes: "read write"}, "", false); err != nil || Connected("boss", "manual") {
		t.Errorf("a new client kept the old one's sign-in: %v", err)
	}
	authURL, err = StartConnect("boss", "manual", cb)
	if err != nil {
		t.Fatal(err)
	}
	u, _ = url.Parse(authURL)
	if err := MCP().CompleteOAuth(u.Query().Get("state"), "code-2", "boss"); err != nil {
		t.Fatal(err)
	}
	if form.Get("client_id") != "other-app" || form.Has("client_secret") {
		t.Errorf("the old app's secret went with the new one: %v", form)
	}
}
