package temptool

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestSecuredCredBindingAuthoring covers P1 authoring: a NEW tool binding a
// secured cred via fetch_via is held for admin approval (request recorded);
// after ApproveToolBinding it authors; secret:<secured> is always hard-blocked.
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

	// 1. New binding → refused, pending recorded.
	if _, err := createGrouped(fetchViaTool(), sess); err == nil || !strings.Contains(err.Error(), "must be APPROVED") {
		t.Fatalf("new secured binding must be held for approval; got %v", err)
	}
	if Secure().ToolBindingApproved("tsbind_secured_api", "ts3_status") {
		t.Fatal("binding must not be approved by merely requesting it")
	}
	c, _ := Secure().Load("tsbind_secured_api")
	if !hasStr(c.PendingToolBindings, "ts3_status") {
		t.Fatalf("expected a pending binding request; got %v", c.PendingToolBindings)
	}

	// 2. Admin approves → authoring now succeeds.
	if err := Secure().ApproveToolBinding("tsbind_secured_api", "ts3_status"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := createGrouped(fetchViaTool(), sess); err != nil {
		t.Fatalf("approved binding must author; got %v", err)
	}

	// 3. secret:<secured> is never a binding — hard-blocked even after approval.
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
}

func hasStr(list []string, v string) bool {
	for _, e := range list {
		if e == v {
			return true
		}
	}
	return false
}
