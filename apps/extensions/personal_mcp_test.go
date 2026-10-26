package extensions

// A person's own MCP servers, on their Extensions page: the administrator's
// policy decides whether a new one is refused, held or live, and an address
// on this network is refused for someone who is not an administrator.

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/governance"
	"github.com/cmcoffee/gohort/core/mcppersonal"
)

func TestAPersonalMCPServerFollowsThePolicy(t *testing.T) {
	app, req := authedExtensions(t)
	do := func(method, path, body string) (int, string) {
		r := req(method, path)
		if body != "" {
			r.Body = readCloser(body)
		}
		w := httptest.NewRecorder()
		serve(app)(w, r)
		return w.Code, w.Body.String()
	}
	// Needs approval is the default: added, held, and the page says so.
	code, body := do("POST", "/api/mcp-servers", `{"name":"linear","url":"https://mcp.invalid/mcp"}`)
	if code >= 300 || !strings.Contains(body, "approval") {
		t.Fatalf("add under the default: %d %s", code, body)
	}
	if s, _ := mcppersonal.Load("alice", "linear"); !s.AwaitingApproval {
		t.Errorf("it should wait for approval: %+v", s)
	}
	if code, _ := do("POST", "/api/mcp-servers?action=enable&name=linear", ""); code < 400 {
		t.Error("the owner switched on one that waits")
	}
	_, list := do("GET", "/api/mcp-servers", "")
	var rows []mcpServerRow
	json.Unmarshal([]byte(list), &rows)
	if len(rows) != 1 || !rows[0].Awaiting || rows[0].CanEnable {
		t.Errorf("the list: %s", list)
	}
	if strings.Contains(list, "token") && strings.Contains(list, "sekret") {
		t.Error("a token left the server")
	}

	if code, _ := do("POST", "/api/mcp-servers", `{"name":"inside","url":"https://192.168.1.5/mcp"}`); code != 400 {
		t.Errorf("a private address for someone who is not an administrator: %d", code)
	}

	if err := governance.Set(governance.MCPServers, string(governance.AdminOnly)); err != nil {
		t.Fatal(err)
	}
	if code, _ := do("POST", "/api/mcp-servers", `{"name":"another","url":"https://mcp.invalid/mcp"}`); code != 403 {
		t.Errorf("admin only should refuse a new one: %d", code)
	}
	if code, _ := do("POST", "/api/mcp-servers", `{"name":"linear","url":"https://mcp.invalid/v2"}`); code >= 300 {
		t.Errorf("an edit to one they have was refused: %d", code)
	}
	if code, _ := do("DELETE", "/api/mcp-servers?name=linear", ""); code >= 300 {
		t.Errorf("delete: %d", code)
	}
	if len(mcppersonal.List("alice")) != 0 {
		t.Error("delete left it behind")
	}
}

// The add form takes an API credential or OAuth as well, offers only the
// credentials the person may use, and has its ✨ helper.
func TestAPersonalMCPServerSignsInWithCredentialsOrOAuth(t *testing.T) {
	app, req := authedExtensions(t)
	do := func(method, path, body string) (int, string) {
		r := req(method, path)
		if body != "" {
			r.Body = readCloser(body)
		}
		w := httptest.NewRecorder()
		serve(app)(w, r)
		return w.Code, w.Body.String()
	}
	if err := Secure().Save(SecureCredential{Name: "mine", Type: SecureCredBearer, BaseURL: "https://mcp.invalid", Owner: "alice"}, "k"); err != nil {
		t.Fatal(err)
	}
	if err := Secure().Save(SecureCredential{Name: "theirs", Type: SecureCredBearer, BaseURL: "https://mcp.invalid", Owner: "bob"}, "k"); err != nil {
		t.Fatal(err)
	}
	_, opts := do("GET", "/api/mcp-servers?options=credentials", "")
	if !strings.Contains(opts, `"mine"`) || strings.Contains(opts, "theirs") {
		t.Errorf("the credential choices: %s", opts)
	}
	if code, body := do("POST", "/api/mcp-servers", `{"name":"viacred","url":"https://mcp.invalid/mcp","auth":"secure_api","credential":"theirs"}`); code != 400 {
		t.Errorf("someone else's credential was taken: %d %s", code, body)
	}
	if code, body := do("POST", "/api/mcp-servers", `{"name":"viacred","url":"https://mcp.invalid/mcp","auth":"secure_api","credential":"mine"}`); code >= 300 {
		t.Errorf("their own credential: %d %s", code, body)
	}
	code, body := do("POST", "/api/mcp-servers", `{"name":"hosted","url":"https://mcp.invalid/mcp","auth":"oauth"}`)
	if code >= 300 || !strings.Contains(body, "Connect") {
		t.Errorf("an OAuth server should say to sign in next: %d %s", code, body)
	}
	if code, body := do("POST", "/api/mcp-servers", `{"name":"manual","url":"https://mcp.invalid/mcp","auth":"oauth","oauth_client_id":"my-app","oauth_client_secret":"s","oauth_scopes":"read"}`); code >= 300 {
		t.Errorf("an OAuth server with its own app: %d %s", code, body)
	}
	if s, _ := mcppersonal.Load("alice", "manual"); s.OAuthClientID != "my-app" || s.OAuthScopes != "read" {
		t.Errorf("the app it signs in with: %+v", s)
	}
	_, list := do("GET", "/api/mcp-servers", "")
	if !strings.Contains(list, "API credential mine") || !strings.Contains(list, "OAuth, not signed in") {
		t.Errorf("the list says how each signs in: %s", list)
	}
	b, _ := json.Marshal(app.mcpServersSection("alice"))
	if !strings.Contains(string(b), `"oauth_client_id"`) || !strings.Contains(string(b), "/account/mcp/callback") {
		t.Error("the add form does not offer an app registered by hand, with its redirect URI")
	}
	if !strings.Contains(string(b), `"assist":true`) {
		t.Error("the add form has no ✨ helper")
	}
}
