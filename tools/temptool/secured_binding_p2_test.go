package temptool

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestSecuredBindingEditLock covers P2 authoring behavior: a MATERIAL edit of a
// secured-bound tool re-opens review (binding → pending), a COSMETIC edit keeps
// the approval, and DELETE clears the binding so a same-name recreate can't
// inherit it.
func TestSecuredBindingEditLock(t *testing.T) {
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
	tool := map[string]any{
		"name": "p2_tool", "description": "d", "mode": "shell",
		"command_template":  "python3 {workspace_dir}/run.py",
		"script_body":       "from gohort import fetch_via\nprint(fetch_via('" + cred + "','http://ts.local:10080/x'))\n",
		"script_name":       "run.py",
		"hook_capabilities": []any{"fetch_via:" + cred},
	}

	// Approve + author.
	if err := Secure().ApproveToolBinding(cred, "p2_tool"); err != nil {
		t.Fatal(err)
	}
	if _, err := createGrouped(tool, sess); err != nil {
		t.Fatalf("author: %v", err)
	}
	if !Secure().ToolBindingApproved(cred, "p2_tool") {
		t.Fatal("tool should be an approved binding after authoring")
	}

	// Cosmetic edit (description only) → approval preserved.
	if _, err := updateGrouped(map[string]any{"name": "p2_tool", "description": "d2"}, sess); err != nil {
		t.Fatalf("cosmetic update: %v", err)
	}
	if !Secure().ToolBindingApproved(cred, "p2_tool") {
		t.Fatal("a cosmetic (description-only) edit must NOT re-pend the binding")
	}

	// Material edit (script_body) → re-pended (dispatch would refuse until re-approved).
	if _, err := updateGrouped(map[string]any{
		"name":        "p2_tool",
		"script_body": "from gohort import fetch_via\nprint('changed', fetch_via('" + cred + "','http://ts.local:10080/y'))\n",
	}, sess); err != nil {
		t.Fatalf("material update: %v", err)
	}
	if Secure().ToolBindingApproved(cred, "p2_tool") {
		t.Fatal("a MATERIAL edit must re-pend the binding (drop approval)")
	}
	if c, _ := Secure().Load(cred); !contains(c.PendingToolBindings, "p2_tool") {
		t.Fatal("material edit must leave the binding pending re-approval")
	}

	// Re-approve, then delete → binding cleared from all lists.
	Secure().ApproveToolBinding(cred, "p2_tool")
	if _, err := deleteGrouped(map[string]any{"name": "p2_tool"}, sess); err != nil {
		t.Fatalf("delete: %v", err)
	}
	c, _ := Secure().Load(cred)
	if contains(c.ApprovedToolBindings, "p2_tool") || contains(c.PendingToolBindings, "p2_tool") || contains(c.RevokedToolBindings, "p2_tool") {
		t.Fatal("delete must clear the binding so a same-name recreate can't inherit approval")
	}
}

func contains(list []string, v string) bool {
	for _, e := range list {
		if e == v {
			return true
		}
	}
	return false
}
