package workspace

// An agent reads its own folder and nothing outside it. Reads used to fall
// back to the user's root, which every agent could reach, so a file one agent
// kept there was every agent's to send.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestAnAgentReadsOnlyItsOwnFolder(t *testing.T) {
	saved := WorkspacesDir()
	SetWorkspacesDir(t.TempDir())
	t.Cleanup(func() { SetWorkspacesDir(saved) })
	root, _ := EnsureWorkspaceDir("alice")
	mine, _ := EnsureAgentWorkspaceDir("alice", "wren")
	os.WriteFile(filepath.Join(root, "private.png"), []byte("not really a png"), 0600)
	os.WriteFile(filepath.Join(mine, "mine.png"), []byte("not really a png"), 0600)
	sess := &ToolSession{Username: "alice", AgentID: "wren", WorkspaceDir: mine}

	if _, err := AttachWorkspaceFile(sess, "mine.png", "", false); err != nil {
		t.Fatalf("its own file: %v", err)
	}
	if _, err := AttachWorkspaceFile(sess, "private.png", "", false); err == nil {
		t.Error("attached a file from the user's root")
	}
	if _, err := handleViewImage(map[string]any{"path": "private.png"}, sess); err == nil || !strings.Contains(err.Error(), "private.png") {
		t.Errorf("view_image read a file from the user's root: %v", err)
	}
}
