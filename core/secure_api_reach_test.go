package core

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// reachFixture is a fresh credential store plus an auth store where root is
// an administrator and alice is not.
func reachFixture(t *testing.T) *SecureAPI {
	t.Helper()
	secureAPITestStore(t)
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev })
	return Secure()
}

// A personal credential reaches internal addresses only when an administrator
// owns it: refused when set up, and refused on dispatch for one saved before
// the rule. Deployment credentials keep their reach, and so does the legacy
// no_auth path lose its: it has the reach of a plain fetch.
func TestPersonalCredentialsKeepToPublicAddresses(t *testing.T) {
	s := reachFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) }))
	defer srv.Close()

	for _, base := range []string{"http://127.0.0.1:8080", "http://169.254.169.254", "https://10.0.0.5", "https://nas.local"} {
		if err := s.Save(SecureCredential{Name: "inside", Type: SecureCredBearer, BaseURL: base, Owner: "alice"}, "k"); err == nil {
			t.Errorf("alice's credential at %s was saved", base)
		}
		if err := s.SaveAPIDraft(SecureCredential{Name: "inside", Type: SecureCredBearer, BaseURL: base, Owner: "alice"}); err == nil {
			t.Errorf("alice's draft at %s was saved", base)
		}
	}
	if err := s.Save(SecureCredential{Name: "fw", Type: SecureCredBearer, BaseURL: "https://192.168.1.1", Owner: "root"}, "k"); err != nil {
		t.Errorf("an administrator's own credential may name the LAN: %v", err)
	}
	if err := s.Save(SecureCredential{Name: "local", Type: SecureCredBearer, BaseURL: srv.URL}, "k"); err != nil {
		t.Fatalf("a deployment credential may name an internal host: %v", err)
	}
	if _, err := s.DispatchToolCall(&ToolSession{Username: "alice"}, "local", srv.URL+"/x", "GET", ""); err != nil {
		t.Errorf("and reach it: %v", err)
	}

	// A record from before the rule is held at dispatch.
	s.db.Set(secureAPITable, credStoreKey("alice", "old"), SecureCredential{Name: "old", Type: SecureCredNone, BaseURL: srv.URL, Owner: "alice"})
	if _, err := s.DispatchToolCall(&ToolSession{Username: "alice"}, "old", srv.URL+"/x", "GET", ""); err == nil || !strings.Contains(err.Error(), "public addresses only") {
		t.Errorf("alice's older credential reached an internal host: %v", err)
	}
	if _, err := s.DispatchToolCall(&ToolSession{Username: "root"}, "no_auth", strings.Replace(srv.URL, "http://", "https://", 1)+"/x", "GET", ""); err == nil || !strings.Contains(err.Error(), "public addresses only") {
		t.Errorf("the no_auth path reached an internal host: %v", err)
	}
}

// A credential name cannot contain "__", the separator the store names a
// credential's secret with, so no name lands on another credential's key.
func TestCredentialNamesCannotLandOnASecret(t *testing.T) {
	s := reachFixture(t)
	if err := s.Save(SecureCredential{Name: "gh", Type: SecureCredBearer, BaseURL: "https://api.github.com"}, "real-key"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"gh__secret", "gh__password", "gh__usertok__alice"} {
		if err := s.SaveOAuthDraft(SecureCredential{Name: name, Grant: OAuthGrantClientCredentials, TokenURL: "https://sso.example/token", BaseURL: "https://x.example"}); err == nil {
			t.Errorf("an oauth draft named %s was saved", name)
		}
		if err := s.SaveAPIDraft(SecureCredential{Name: name, Type: SecureCredBearer, BaseURL: "https://x.example"}); err == nil {
			t.Errorf("an api draft named %s was saved", name)
		}
		if err := s.Save(SecureCredential{Name: name, Type: SecureCredBearer, BaseURL: "https://x.example"}, "k"); err == nil {
			t.Errorf("a credential named %s was saved", name)
		}
	}
	if sec, _ := s.loadSecret("gh"); sec != "real-key" {
		t.Errorf("gh's key was touched: %q", sec)
	}
	if _, err := s.TestMintFromPosted(nil, SecureCredential{Name: "@u:alice:gh", Type: SecureCredOAuth2, Grant: OAuthGrantClientCredentials, TokenURL: "https://elsewhere.example/token"}, ""); err == nil {
		t.Error("Test token reached a secret by a name that is not a credential's")
	}
}

// A lend does not take over a deployment credential's name: it cannot be
// made while one exists, and one made before resolves to the deployment's.
func TestALendDoesNotTakeADeploymentName(t *testing.T) {
	s := reachFixture(t)
	if err := s.Save(SecureCredential{Name: "wiki", Type: SecureCredBearer, BaseURL: "https://wiki.example", Owner: "alice"}, "alice-key"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCredentialShares("alice", "wiki", nil, []string{"bob"}); err != nil {
		t.Fatalf("lend with no deployment credential of the name: %v", err)
	}
	if c, _ := s.Resolve("wiki", "bob"); c.Owner != "alice" {
		t.Fatalf("bob gets alice's lend: %+v", c)
	}
	if err := s.Save(SecureCredential{Name: "wiki", Type: SecureCredBearer, BaseURL: "https://wiki.corp.example"}, "deploy-key"); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Resolve("wiki", "bob"); c.Owner != "" {
		t.Errorf("the deployment's wiki is bob's wiki, not alice's lend: %+v", c)
	}
	if err := s.SetCredentialShares("alice", "wiki", nil, []string{"carol"}); err == nil {
		t.Error("a new lend under a deployment credential's name is refused")
	}
}

// A tool somebody else wrote is not handed the runner's raw key.
func TestAForeignToolGetsNoRawKey(t *testing.T) {
	s := reachFixture(t)
	if err := s.Save(SecureCredential{Name: "gh", Type: SecureCredBearer, BaseURL: "https://api.github.com", Owner: "alice"}, "alice-key"); err != nil {
		t.Fatal(err)
	}
	own := &SandboxHook{Capabilities: []string{"secret:gh"}, Sess: &ToolSession{Username: "alice"}, ToolName: "mine"}
	if got := hookSecret(t, own, "gh"); !strings.Contains(got, "alice-key") {
		t.Fatalf("alice's own tool gets her key: %s", got)
	}
	foreign := &SandboxHook{Capabilities: []string{"secret:gh"}, Sess: &ToolSession{Username: "alice"}, ToolName: "shared", ForeignTool: true}
	if got := hookSecret(t, foreign, "gh"); strings.Contains(got, "alice-key") || !strings.Contains(got, "fetch_via") {
		t.Errorf("a colleague's tool got alice's key: %s", got)
	}
}
