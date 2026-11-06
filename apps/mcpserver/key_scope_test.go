package mcpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
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

// MCP takes a personal API key that is an MCP key. A desktop key, or a key
// some other validator resolves (a bridge's connector key), names its owner
// but carries no scope, so it is refused, as /v1 refuses it; so is a key of
// another kind.
func TestMCPTakesOnlyAnMCPKey(t *testing.T) {
	prevAuth, prevRoot := AuthDB, RootDB
	root := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(root, "user-a", "pw-a-123", false)
	AuthDB = func() Database { return root }
	RootDB = root
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot })
	RegisterAPIKeyValidator(func(secret string) (string, bool) { return "user-a", secret == "connector-secret" })

	app := &MCPServer{}
	app.DB = root.Bucket("mcpserver")
	desktop, _ := MintDesktopKey("user-a")
	modelAPI := MintAccountTokenScoped("user-a", "voice", &TokenScope{Features: []string{"openai"}})
	try := func(key string) int {
		r := httptest.NewRequest(http.MethodPost, "/mcp/", nil)
		r.Header.Set("X-API-Key", key)
		_, _, status := app.authorize(r, "test")
		return status
	}
	for name, key := range map[string]string{"desktop": desktop, "connector": "connector-secret", "Model API": modelAPI.Token} {
		if got := try(key); got != http.StatusForbidden {
			t.Errorf("a %s key on MCP: %d, want 403", name, got)
		}
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

// A key presented beside a session must be the same person's: one user's key
// scope is not applied to another user's run.
func TestAKeyBesideASessionMustBeTheSamePersons(t *testing.T) {
	prevAuth, prevRoot := AuthDB, RootDB
	root := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(root, "user-a", "pw-a-123", false)
	AuthSetUser(root, "user-b", "pw-b-123", false)
	AuthDB = func() Database { return root }
	RootDB = root
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot })

	app := &MCPServer{}
	app.DB = root.Bucket("mcpserver")
	bobsKey := MintAccountTokenScoped("user-b", "claude", &TokenScope{Features: []string{MCPFeatureKey}})
	r := httptest.NewRequest(http.MethodGet, "/mcp/", nil)
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(root, "user-a")})
	r.Header.Set("X-API-Key", bobsKey.Token)
	if _, _, status := app.authorize(r, "test"); status != http.StatusForbidden {
		t.Errorf("user-a's session with user-b's key: %d, want 403", status)
	}
}
