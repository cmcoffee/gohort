package core

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An administrator's switch-off and stopped lending hold: the owner's own
// switch cannot undo them, and only an administrator lifts them.
func TestAnAdminsHoldOnAUsersCredentialHolds(t *testing.T) {
	s := reachFixture(t)
	if err := s.Save(SecureCredential{Name: "wiki", Type: SecureCredBearer, BaseURL: "https://wiki.example", Owner: "alice"}, "k"); err != nil {
		t.Fatal(err)
	}
	if err := s.AdminSetDisabledOwned("alice", "wiki", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDisabledOwned("alice", "wiki", false); err == nil {
		t.Error("alice switched back on what an administrator switched off")
	}
	if err := s.AdminSetDisabledOwned("alice", "wiki", false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDisabledOwned("alice", "wiki", true); err != nil {
		t.Errorf("once lifted, her own switch works: %v", err)
	}
	s.SetDisabledOwned("alice", "wiki", false)

	if err := s.SetCredentialShares("alice", "wiki", []string{"bob"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.AdminRevokeShares("alice", "wiki"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCredentialShares("alice", "wiki", []string{"bob"}, nil); err == nil {
		t.Error("alice lent again what an administrator stopped")
	}
	if err := s.AdminAllowLending("alice", "wiki"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCredentialShares("alice", "wiki", []string{"bob"}, nil); err != nil {
		t.Errorf("once allowed, she may lend: %v", err)
	}
	c, _ := s.LoadUser("alice", "wiki")
	if err := s.Save(c, ""); err != nil {
		t.Fatal(err)
	}
	s.AdminSetDisabledOwned("alice", "wiki", true)
	c, _ = s.LoadUser("alice", "wiki")
	c.Description = "edited"
	s.Save(c, "")
	if got, _ := s.LoadUser("alice", "wiki"); !got.AdminDisabled {
		t.Error("an edit of the credential cleared the administrator's hold")
	}
}

// A secured deployment key is bound to a new tool only by an administrator's
// run; anyone else's new tool is refused until one approves it.
func TestSecuredDeploymentKeysBindOnlyForAnAdmin(t *testing.T) {
	s := reachFixture(t)
	if err := s.Save(SecureCredential{Name: "team", Type: SecureCredBearer, BaseURL: "https://team.example"}, "k"); err != nil {
		t.Fatal(err)
	}
	s.SetSecured("team", true)
	if err := s.EnforceSecuredBinding("team", "alices_tool", "alice"); err == nil || !strings.Contains(err.Error(), "not bound") {
		t.Errorf("alice's new tool reached the deployment's secured key: %v", err)
	}
	if err := s.EnforceSecuredBinding("team", "admins_tool", "root"); err != nil {
		t.Errorf("an administrator's run binds its tool: %v", err)
	}
	if err := s.EnforceSecuredBinding("team", "admins_tool", "alice"); err != nil {
		t.Errorf("a bound tool runs for anyone allowed the key: %v", err)
	}
}

// secret: does not hand out a per-person credential's stored value, which is
// only what the admin typed when creating it.
func TestSecretHookRefusesAPerPersonCredential(t *testing.T) {
	s := reachFixture(t)
	if err := s.Save(SecureCredential{Name: "each", Type: SecureCredBearer, BaseURL: "https://each.example", CredScope: "per_user"}, "admin-typed"); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Load("each")
	c.CredScope = "per_user"
	s.db.Set(secureAPITable, "each", c)
	h := &SandboxHook{Capabilities: []string{"secret:each"}, Sess: &ToolSession{Username: "alice"}, ToolName: "t"}
	if got := hookSecret(t, h, "each"); strings.Contains(got, "admin-typed") {
		t.Errorf("a per-person credential's admin value reached a script: %s", got)
	}
}

// Where a credential's methods are limited, a method-override header does not
// reach the server.
func TestAMethodOverrideDoesNotSlipALimit(t *testing.T) {
	s := reachFixture(t)
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-HTTP-Method-Override")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	if err := s.Save(SecureCredential{Name: "ro", Type: SecureCredBearer, BaseURL: srv.URL, AllowedMethods: []string{"GET"}}, "k"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DispatchToolCallArgs(&ToolSession{Username: "root"}, "ro", map[string]any{"url": srv.URL + "/x",
		"request_headers": map[string]any{"X-HTTP-Method-Override": "DELETE"}}); err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("the override reached the server: %q", got)
	}
}

// Tokens and per-person keys given for an address do not follow the
// credential when its token host moves.
func TestMovingATokenHostDropsItsTokens(t *testing.T) {
	s := reachFixture(t)
	if err := s.Save(SecureCredential{Name: "sso", Type: SecureCredOAuth2, Grant: OAuthGrantClientCredentials, ClientID: "c",
		TokenURL: "https://sso.example/token", BaseURL: "https://api.example"}, "client-secret"); err != nil {
		t.Fatal(err)
	}
	s.db.CryptSet(secureAPITable, secureCredUserTokenKey("sso", "alice"), CredOAuthToken{AccessToken: "a", RefreshToken: "r"})
	s.db.CryptSet(secureAPITable, secureCredUserSecretKey("sso", "bob"), "bobs-key")
	c, _ := s.Load("sso")
	c.TokenURL = "https://collector.example/token"
	if err := s.Save(c, ""); err != nil {
		t.Fatal(err)
	}
	var v any
	if s.db.Get(secureAPITable, secureCredUserTokenKey("sso", "alice"), &v) || s.db.Get(secureAPITable, secureCredUserSecretKey("sso", "bob"), &v) {
		t.Error("a token or key given for the old token host survived the move")
	}
}

// A watch polls as its agent: a credential the agent switched off stays off.
func TestAWatchKeepsItsAgentsCredentialScope(t *testing.T) {
	secureAPITestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) }))
	defer srv.Close()
	url := srv.URL
	if err := Secure().Save(SecureCredential{Name: "team", Type: SecureCredBearer, BaseURL: srv.URL}, "k"); err != nil {
		t.Fatal(err)
	}
	RegisterWatchCredentialScope(func(owner, agentID string) map[string]bool {
		if agentID == "quiet" {
			return map[string]bool{"team": true}
		}
		return nil
	})
	t.Cleanup(func() { RegisterWatchCredentialScope(nil) })
	args := map[string]any{"url": url + "/x"}
	if _, err := InvokeWatchTool("bob", "quiet", "call_team", args); err == nil || !strings.Contains(err.Error(), "switched off") {
		t.Errorf("the agent's switched-off credential was polled: %v", err)
	}
	if _, err := InvokeWatchTool("bob", "busy", "call_team", args); err != nil {
		t.Errorf("another agent of bob's may: %v", err)
	}
}
