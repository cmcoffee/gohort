package temptool

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestSecuredCredBindingAuthoring covers auto-resolve authoring: declaring a
// secured cred via fetch_via AUTO-BINDS the tool (no approval step); secret:
// <secured> is always hard-blocked; and an admin's explicit REVOKE is a durable
// deny that refuses a same-name (re)author.
func TestSecuredCredBindingAuthoring(t *testing.T) {
	secStore := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return secStore }
	defer func() { AuthDB = prev }()
	if err := Secure().Save(SecureCredential{
		Name: "tsbind_secured_api", Type: SecureCredBearer,
		BaseURL: "http://teamspeak.snuglab.local:10080", Secured: true,
	}, "tok"); err != nil {
		t.Fatalf("save credential: %v", err)
	}

	sess := &ToolSession{
		Username: "alice", ChatSessionID: "s1",
		WorkspaceDir: t.TempDir(), DB: &DBase{Store: kvlite.MemStore()},
	}
	fetchViaTool := func() map[string]any {
		return map[string]any{
			"name": "ts3_status", "description": "check ts3", "mode": "shell",
			"command_template":  "python3 {workspace_dir}/run.py",
			"script_body":       "from gohort import fetch_via\nprint(fetch_via('tsbind_secured_api','http://teamspeak.snuglab.local:10080/clientlist'))\n",
			"script_name":       "run.py",
			"hook_capabilities": []any{"fetch_via:tsbind_secured_api"},
		}
	}

	// 1. Declaring the secured cred AUTO-BINDS — authoring succeeds, no approval.
	if _, err := createGrouped(fetchViaTool(), sess); err != nil {
		t.Fatalf("declaring a secured cred must auto-bind (no approval); got %v", err)
	}
	if !Secure().ToolBindingApproved("tsbind_secured_api", "ts3_status") {
		t.Fatal("auto-resolve must record the binding as approved")
	}

	// 2. secret:<secured> is never a binding — hard-blocked.
	secretTool := map[string]any{
		"name": "ts3_secret", "description": "leak", "mode": "shell",
		"command_template":  "python3 {workspace_dir}/run.py",
		"script_body":       "from gohort import secret\nprint(secret('tsbind_secured_api'))\n",
		"script_name":       "run.py",
		"hook_capabilities": []any{"secret:tsbind_secured_api"},
	}
	if _, err := createGrouped(secretTool, sess); err == nil || !strings.Contains(err.Error(), "raw secret") {
		t.Fatalf("secret:<secured> must be hard-blocked; got %v", err)
	}

	// 3. Admin revoke is a durable deny — a same-name (re)author is refused.
	if err := Secure().RevokeToolBinding("tsbind_secured_api", "ts3_status"); err != nil {
		t.Fatal(err)
	}
	if _, err := createGrouped(fetchViaTool(), sess); err == nil || !strings.Contains(err.Error(), "REVOKED") {
		t.Fatalf("a revoked binding must refuse a same-name author; got %v", err)
	}
}
