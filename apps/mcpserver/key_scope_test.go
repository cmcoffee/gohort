package mcpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	. "github.com/cmcoffee/gohort/core"
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
