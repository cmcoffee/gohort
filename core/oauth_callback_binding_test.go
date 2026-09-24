package core

// An OAuth consent is completed by the account that started it, or not at all:
// the state alone cannot tell a colleague who was sent the consent link from
// the person who asked for it.

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAnOAuthCallbackRefusesADifferentAccount(t *testing.T) {
	oauthPendingMu.Lock()
	oauthPending_["st-1"] = oauthPending{cred: "jira", user: "alice", at: time.Now()}
	oauthPendingMu.Unlock()
	_, _, err := (&SecureAPI{}).OAuthCallback(context.Background(), "st-1", "code", "bob")
	if err == nil || !strings.Contains(err.Error(), "different account") {
		t.Fatalf("bob completed alice's flow: %v", err)
	}
	oauthPendingMu.Lock()
	_, still := oauthPending_["st-1"]
	oauthPendingMu.Unlock()
	if still {
		t.Error("a refused state should be burned, not left to retry")
	}

	oauthPendingMu.Lock()
	oauthPending_["st-old"] = oauthPending{cred: "jira", user: "alice", at: time.Now().Add(-time.Hour)}
	oauthPendingMu.Unlock()
	if _, _, err := (&SecureAPI{}).OAuthCallback(context.Background(), "st-old", "code", "alice"); err == nil {
		t.Error("an hour-old state should have expired")
	}

	m := &MCPManager{oauthPending: map[string]mcpPendingAuth{
		"mcp-1": {user: "alice", server: "atlassian", created: time.Now()},
	}}
	if err := m.CompleteOAuth("mcp-1", "code", "bob"); err == nil || !strings.Contains(err.Error(), "different account") {
		t.Fatalf("bob completed alice's MCP flow: %v", err)
	}
}

// A registered MCP client gains new callback PATHS on its own hosts, never a
// new host: the host comes from the request when no External URL is set.
func TestAnMCPClientIsNeverReRegisteredToANewHost(t *testing.T) {
	reg := []string{"https://gohort.example/account/mcp/callback"}
	if !redirectHostKnown(reg, "https://gohort.example/admin/api/mcp-servers/oauth/callback") {
		t.Error("a sibling path on the same host should be allowed")
	}
	for _, u := range []string{"https://evil.example/account/mcp/callback", "http://gohort.example/account/mcp/callback", "not a url"} {
		if redirectHostKnown(reg, u) {
			t.Errorf("%s should not count as a known host", u)
		}
	}
}
