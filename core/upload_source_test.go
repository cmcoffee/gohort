package core

import (
	"os"
	"path/filepath"
	"testing"
)

// An upload or a source image is read from inside the workspace and held to
// its limit by the file opened, not by a check of a path that a script still
// running can change.
func TestAnUploadIsReadInsideTheWorkspace(t *testing.T) {
	base := t.TempDir()
	ws, outside := filepath.Join(base, "ws"), filepath.Join(base, "server-data")
	os.MkdirAll(ws, 0o700)
	os.MkdirAll(outside, 0o700)
	os.WriteFile(filepath.Join(outside, "key.pem"), []byte("private"), 0o600)
	if err := os.Symlink(outside, filepath.Join(ws, "out")); err != nil {
		t.Skip("no symlinks here")
	}
	if _, err := readWorkspaceFileMax(ws, filepath.Join(ws, "out", "key.pem"), 1<<20); err == nil {
		t.Error("read through a folder link out of the workspace")
	}
	os.WriteFile(filepath.Join(ws, "photo.png"), make([]byte, 2048), 0o600)
	if _, err := readWorkspaceFileMax(ws, filepath.Join(ws, "photo.png"), 1024); err == nil {
		t.Error("a file over the limit was read")
	}
	if b, err := readWorkspaceFileMax(ws, filepath.Join(ws, "photo.png"), 4096); err != nil || len(b) != 2048 {
		t.Errorf("a file of the workspace's own: %d %v", len(b), err)
	}
}
