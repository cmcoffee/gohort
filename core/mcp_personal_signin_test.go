package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// A person's own MCP server signs in with one of their credentials as a
// dispatch would: their own name resolves, its key goes only where its
// allow-list says, and a credential that cannot make the POST every MCP
// request is (or asks before it) is refused rather than half used.
func TestAuthorizeRequestAsHoldsToTheCredential(t *testing.T) {
	s := reachFixture(t)
	if err := s.Save(SecureCredential{Name: "tracker", Type: SecureCredBearer, BaseURL: "https://mcp.example.com", Owner: "root"}, "k-123"); err != nil {
		t.Fatal(err)
	}
	sign := func(user, cred, u string) (string, error) {
		req, _ := http.NewRequest(http.MethodPost, u, nil)
		err := s.AuthorizeRequestAs(user, cred, req)
		return req.Header.Get("Authorization"), err
	}
	if got, err := sign("root", "tracker", "https://mcp.example.com/mcp"); err != nil || got != "Bearer k-123" {
		t.Fatalf("the owner's request was not signed: %q %v", got, err)
	}
	if got, err := sign("root", "tracker", "https://elsewhere.example.com/mcp"); err == nil || got != "" {
		t.Errorf("the key went outside the credential's allow-list: %q %v", got, err)
	}
	if got, err := sign("alice", "tracker", "https://mcp.example.com/mcp"); err == nil || got != "" {
		t.Errorf("someone else's own credential signed alice's request: %q %v", got, err)
	}
	if err := s.mutateOwned("root", "tracker", func(c *SecureCredential) { c.ConfirmWrites = true }); err != nil {
		t.Fatal(err)
	}
	if got, err := sign("root", "tracker", "https://mcp.example.com/mcp"); err == nil || got != "" || !strings.Contains(err.Error(), "asks before writes") {
		t.Errorf("a credential that asks before writes signed an MCP POST: %q %v", got, err)
	}
	if err := s.mutateOwned("root", "tracker", func(c *SecureCredential) { c.ConfirmWrites, c.AllowedMethods = false, []string{"GET"} }); err != nil {
		t.Fatal(err)
	}
	if _, err := sign("root", "tracker", "https://mcp.example.com/mcp"); err == nil || !strings.Contains(err.Error(), "POST") {
		t.Errorf("a credential limited to GET signed an MCP POST: %v", err)
	}
}

// A person's own server's OAuth client is held to public addresses, its
// discovery and registration included: the server's metadata names the
// endpoints this machine then calls. Without the hold the same flow goes.
func TestAPersonalOAuthClientKeepsToPublicAddresses(t *testing.T) {
	as := newStubAS(t)
	defer as.Close()
	rsMux := http.NewServeMux()
	rs := httptest.NewServer(rsMux)
	defer rs.Close()
	rsMux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"resource": rs.URL + "/mcp", "authorization_servers": []string{as.URL}})
	})

	m := &MCPManager{db: &DBase{Store: kvlite.MemStore()}, oauthPending: map[string]mcpPendingAuth{}}
	st := MCPOAuthStart{User: "alice", Server: "personal:alice/x", URL: rs.URL + "/mcp", RedirectURI: "https://gohort.example.com/account/mcp/callback", PublicOnly: true}
	if u, err := m.StartOAuthFor(st); err == nil {
		t.Fatalf("discovery reached a loopback address under the hold: %s", u)
	}
	if _, ok := m.loadOAuthCfg(st.Server); ok {
		t.Error("a client was registered under the hold")
	}
	st.PublicOnly = false
	u, err := m.StartOAuthFor(st)
	if err != nil || !strings.HasPrefix(u, as.URL+"/authorize") {
		t.Fatalf("without the hold the flow should start: %q %v", u, err)
	}
	if oc, _ := m.loadOAuthCfg(st.Server); oc.PublicOnly {
		t.Error("the client records a hold it was not given")
	}
	// A held client's token refreshes are held too.
	if _, err := mcpRefreshAccess(context.Background(), mcpOAuthConfig{TokenEndpoint: as.URL + "/token", ClientID: "c", PublicOnly: true}, mcpOAuthToken{RefreshToken: "r"}); err == nil {
		t.Error("a held client's refresh reached a loopback address")
	}
	if _, err := mcpRefreshAccess(context.Background(), mcpOAuthConfig{TokenEndpoint: as.URL + "/token", ClientID: "c"}, mcpOAuthToken{RefreshToken: "r"}); err != nil {
		t.Errorf("an unheld refresh: %v", err)
	}
	// And no administrator's server name can be taken for one.
	if _, err := m.StartOAuthFor(MCPOAuthStart{User: "alice", Server: "jira", URL: rs.URL + "/mcp", RedirectURI: st.RedirectURI}); err == nil {
		t.Error("a personal sign-in was started under an administrator's server name")
	}
}
