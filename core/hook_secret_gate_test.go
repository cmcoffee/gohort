package core

// The secret: hook hands a script the raw key only when every gate a
// server-side path checks would pass: the user is allowed the credential, it
// is not somebody else's lent key, and it is not an OAuth client secret.

import (
	"bufio"
	"net"
	"strings"
	"testing"
)

func hookSecret(t *testing.T, h *SandboxHook, name string) string {
	t.Helper()
	server, client := net.Pipe()
	defer client.Close()
	go func() {
		h.handleSecret(server, map[string]interface{}{"name": name})
		server.Close()
	}()
	line, _ := bufio.NewReader(client).ReadString('\n')
	return line
}

func TestTheSecretHookRefusesWhatTheUserMayNotHave(t *testing.T) {
	secureAPITestStore(t)
	s := Secure()
	if err := s.Save(SecureCredential{Name: "team_key", Type: SecureCredBearer, BaseURL: "https://api.example.com", AllowedUsers: []string{"alice"}}, "tk-secret-1"); err != nil {
		t.Skipf("secure store unavailable: %v", err)
	}
	_ = s.Save(SecureCredential{Name: "sso", Type: SecureCredOAuth2, BaseURL: "https://sso.example.com", TokenURL: "https://sso.example.com/token", ClientID: "c"}, "client-secret")

	grant := func(user, cred string) *SandboxHook {
		return &SandboxHook{Capabilities: []string{"secret:" + cred}, Sess: &ToolSession{Username: user}}
	}
	if got := hookSecret(t, grant("alice", "team_key"), "team_key"); !strings.Contains(got, "tk-secret-1") {
		t.Fatalf("alice is allowed the key: %s", got)
	}
	if got := hookSecret(t, grant("bob", "team_key"), "team_key"); strings.Contains(got, "tk-secret-1") || !strings.Contains(got, "not shared") {
		t.Fatalf("bob is not on the credential's list: %s", got)
	}
	if got := hookSecret(t, grant("alice", "sso"), "sso"); strings.Contains(got, "client-secret") {
		t.Fatalf("an OAuth client secret reached a script: %s", got)
	}
}
