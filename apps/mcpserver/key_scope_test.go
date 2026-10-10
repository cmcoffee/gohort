package mcpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A personal token scoped to MCP alone reaches MCP, without also being scoped
// for the desktop bridge (which would let it announce tools into every agent).
func TestAnMCPScopedKeyNeedsNoDesktopScope(t *testing.T) {
	prevAuth, prevRoot := AuthDB, RootDB
	root := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(root, "user-a", "pw-a-123", false)
	AuthDB = func() Database { return root }
	RootDB = root
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot })

	app := &MCPServer{}
	app.DB = root.Bucket("mcpserver")
	tok := MintAccountTokenScoped("user-a", "claude", &TokenScope{Features: []string{MCPFeatureKey}})
	r := httptest.NewRequest(http.MethodPost, "/mcp/", nil)
	r.Header.Set("X-API-Key", tok.Token)
	owner, refusal, status := app.authorize(r, "test")
	if status != http.StatusOK || owner != "user-a" {
		t.Errorf("an MCP-scoped key was refused: %d %s", status, refusal)
	}
}

// A key narrowed to some agents sees those agents' runs only, and an app's
// tools run only for a user the app is granted to.
func TestANarrowedKeySeesItsOwnAgentsAndApps(t *testing.T) {
	prevAuth, prevRoot := AuthDB, RootDB
	root := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(root, "admin", "pw-admin-1", true)
	root.Set(AuthTable, "user:user-a", AuthUser{Username: "user-a", Apps: []string{"/other"}})
	AuthDB = func() Database { return root }
	RootDB = root
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot })

	RecordRun(root, RunRecord{Owner: "user-a", Agent: "Helper", Subject: "agent:a1", Summary: "mine", Status: RunOK})
	RecordRun(root, RunRecord{Owner: "user-a", Agent: "Other", Subject: "agent:a2", Summary: "not this key's", Status: RunOK})
	app := &MCPServer{}
	tok := &AccountToken{Owner: "user-a", Scope: &TokenScope{Targets: []string{"agent:a1"}}}
	out, err := app.recentResults("user-a", tok, map[string]any{})
	if err != nil || !strings.Contains(out, "mine") || strings.Contains(out, "not this key's") {
		t.Errorf("narrowed key's runs: %q %v", out, err)
	}

	if userMayUseApp("user-a", "/servitor") {
		t.Error("an app the user was not granted ran its tool for them")
	}
	if !userMayUseApp("user-a", "/other") || !userMayUseApp("admin", "/servitor") || !userMayUseApp("user-a", "") {
		t.Error("a granted app, an admin, or a tool with no app was refused")
	}
}
