package admin

// Per-user MCP OAuth uses a DIFFERENT redirect URI from the admin
// Connect button, and a provider with a pre-registered client must have
// both.
//
// Found the hard way: an Atlassian connector established from the admin
// page worked, and Reconnect from Extensions came back with "the app's
// callback URL is invalid" — because the admin form's help named only
// the admin path, so that is the only one anybody registers.

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestBothMCPRedirectURIsAreDocumentedWhereTheyAreRegistered(t *testing.T) {
	src, err := os.ReadFile("page.go")
	if err != nil {
		t.Fatal(err)
	}
	help := string(src)
	// The form that asks for a pre-registered client ID is the only
	// place somebody learns what to register.
	for _, want := range []string{
		"/admin/api/mcp-servers/oauth/callback",
		"/account/mcp/callback",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("the Client ID help does not name %s, so nobody registers it", want)
		}
	}
	// And it says WHY there are two, or the second reads as a typo.
	if !strings.Contains(help, "admin area is admin-only") {
		t.Error("the help should say why the paths differ")
	}

	// The admin callback really is behind the admin gate — which is the
	// reason the per-user flow cannot share it. If this ever stops being
	// true the two paths could merge, and the help above would be wrong.
	r := httptest.NewRequest("GET", "/admin/api/mcp-servers/oauth/callback", nil)
	if strings.Contains(mcpOAuthCallbackURL(r), "/account/") {
		t.Error("the admin flow should keep its own callback path")
	}
	if !strings.HasSuffix(mcpOAuthCallbackURL(r), "/admin/api/mcp-servers/oauth/callback") {
		t.Errorf("unexpected admin callback: %s", mcpOAuthCallbackURL(r))
	}
}
