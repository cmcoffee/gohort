package temptool

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestSecuredBindingAutoResolveEdits covers auto-resolve edit/delete behavior:
// a material edit keeps the binding (re-resolves, no re-review), and an admin's
// REVOKE is a durable deny that survives a delete + same-name recreate.
func TestSecuredBindingAutoResolveEdits(t *testing.T) {
	const cred = "tsbind_p2_api"
	secStore := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return secStore }
	defer func() { AuthDB = prev }()
	if err := Secure().Save(SecureCredential{
		Name: cred, Type: SecureCredBearer,
		BaseURL: "http://ts.local:10080", Secured: true,
	}, "tok"); err != nil {
		t.Fatalf("save credential: %v", err)
	}

	sess := &ToolSession{
		Username: "alice", ChatSessionID: "s1",
		WorkspaceDir: t.TempDir(), DB: &DBase{Store: kvlite.MemStore()},
	}
	tool := func(body string) map[string]any {
		return map[string]any{
			"name": "p2_tool", "description": "d", "mode": "shell",
			"command_template":  "python3 {workspace_dir}/run.py",
			"script_body":       body,
			"script_name":       "run.py",
			"hook_capabilities": []any{"fetch_via:" + cred},
		}
	}
	body1 := "from gohort import fetch_via\nprint(fetch_via('" + cred + "','http://ts.local:10080/x'))\n"

	// Author → auto-bound.
	if _, err := createGrouped(tool(body1), sess); err != nil {
		t.Fatalf("author: %v", err)
	}
	if !Secure().ToolBindingApproved(cred, "p2_tool") {
		t.Fatal("declaring tool should be auto-bound")
	}

	// Material edit (script_body) → STILL bound (auto-resolves; no re-review).
	if _, err := updateGrouped(map[string]any{
		"name":        "p2_tool",
		"script_body": "from gohort import fetch_via\nprint('changed', fetch_via('" + cred + "','http://ts.local:10080/y'))\n",
	}, sess); err != nil {
		t.Fatalf("material update: %v", err)
	}
	if !Secure().ToolBindingApproved(cred, "p2_tool") {
		t.Fatal("a material edit must keep the binding under auto-resolve")
	}

	// Revoke → delete → the revoke tombstone survives the delete.
	if err := Secure().RevokeToolBinding(cred, "p2_tool"); err != nil {
		t.Fatal(err)
	}
	if _, err := deleteGrouped(map[string]any{"name": "p2_tool"}, sess); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !Secure().ToolBindingRevoked(cred, "p2_tool") {
		t.Fatal("delete must PRESERVE the revoke tombstone (durable deny)")
	}

	// Recreate with the same name → refused (durable deny survived delete).
	if _, err := createGrouped(tool(body1), sess); err == nil || !strings.Contains(err.Error(), "REVOKED") {
		t.Fatalf("a same-name recreate after revoke+delete must be refused; got %v", err)
	}
}
