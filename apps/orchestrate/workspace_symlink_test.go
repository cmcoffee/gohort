package orchestrate

// The sandbox writes the workspace, so a symlink in it must not have the host
// read what it points at and send it out as an attachment.

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestAWorkspaceSymlinkIsNotFollowedOut(t *testing.T) {
	ws := t.TempDir()
	outside := filepath.Join(t.TempDir(), "host-secret.txt")
	_ = os.WriteFile(outside, []byte("host secret"), 0600)
	_ = os.WriteFile(filepath.Join(ws, "chart.png"), []byte("png bytes"), 0600)
	if err := os.Symlink(outside, filepath.Join(ws, "innocent.png")); err != nil {
		t.Skip("symlinks unavailable")
	}
	sess := &ToolSession{WorkspaceDir: ws}
	if got := resolveWorkspaceImages(sess, []string{"innocent.png", "../x", "/etc/passwd"}); len(got) != 0 {
		t.Fatalf("an escape was read: %d item(s)", len(got))
	}
	if got := resolveWorkspaceImages(sess, []string{"chart.png"}); len(got) != 1 {
		t.Fatal("an ordinary workspace file should still resolve")
	}
}
