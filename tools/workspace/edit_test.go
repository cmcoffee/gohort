package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// edit changes text in place: one exact match, or every match with all; a
// find that matches nothing or several places is refused, saying so.
func TestEditChangesOneExactMatch(t *testing.T) {
	dir := t.TempDir()
	sess := &ToolSession{WorkspaceDir: dir, Username: "u"}
	p := filepath.Join(dir, "data", "now.py")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("a = d.get('max_temp')\nb = d.get('max_temp')\nc = 1\n"), 0o644)

	if _, err := handleEdit(map[string]any{"path": "data/now.py", "find": "c = 1", "replace": "c = 2"}, sess); err != nil {
		t.Fatal(err)
	}
	if _, err := handleEdit(map[string]any{"path": "data/now.py", "find": "d.get('max_temp')", "replace": "x"}, sess); err == nil || !strings.Contains(err.Error(), "2 times") {
		t.Fatalf("ambiguous find: %v", err)
	}
	if _, err := handleEdit(map[string]any{"path": "data/now.py", "find": "not there", "replace": "x"}, sess); err == nil || !strings.Contains(err.Error(), "does not appear") {
		t.Fatalf("missing find: %v", err)
	}
	if _, err := handleEdit(map[string]any{"path": "data/now.py", "find": "max_temp", "replace": "temp_high_f", "all": true}, sess); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "a = d.get('temp_high_f')\nb = d.get('temp_high_f')\nc = 2\n" {
		t.Fatalf("file = %q", b)
	}
	if _, err := handleEdit(map[string]any{"path": "../escape.py", "find": "x", "replace": "y"}, sess); err == nil {
		t.Fatal("an edit outside the workspace was allowed")
	}
}

// An edit is a read and a write, and a folder a script swaps for a link in
// between leads neither outside the workspace.
func TestEditNeverReachesThroughASwappedFolder(t *testing.T) {
	base := t.TempDir()
	ws, outside := filepath.Join(base, "ws"), filepath.Join(base, "server-data")
	os.MkdirAll(filepath.Join(ws, "out"), 0o700)
	os.MkdirAll(outside, 0o700)
	secret := filepath.Join(outside, "conf.py")
	os.WriteFile(secret, []byte("token = 'private'\n"), 0o600)
	out := filepath.Join(ws, "out")
	quit, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-quit:
				return
			default:
			}
			os.RemoveAll(out)
			os.Symlink(outside, out)
			os.Remove(out)
			os.Mkdir(out, 0o700)
			os.WriteFile(filepath.Join(out, "conf.py"), []byte("token = 'mine'\n"), 0o600)
		}
	}()
	sess := &ToolSession{WorkspaceDir: ws, Username: "u"}
	for i := 0; i < 3000; i++ {
		handleEdit(map[string]any{"path": "out/conf.py", "find": "token", "replace": "stolen"}, sess)
	}
	close(quit)
	<-done
	if b, _ := os.ReadFile(secret); string(b) != "token = 'private'\n" {
		t.Fatalf("an edit reached the server's file: %q", b)
	}
}

// Attaching reads the file it opened, inside the workspace, held to its cap.
func TestAnAttachedFileIsReadInsideTheWorkspace(t *testing.T) {
	base := t.TempDir()
	ws, outside := filepath.Join(base, "ws"), filepath.Join(base, "server-data")
	os.MkdirAll(ws, 0o700)
	os.MkdirAll(outside, 0o700)
	os.WriteFile(filepath.Join(outside, "key.pem"), []byte("private"), 0o600)
	os.Symlink(outside, filepath.Join(ws, "out"))
	if _, err := readWorkspaceFile(ws, filepath.Join(ws, "out", "key.pem"), 1<<20); err == nil {
		t.Error("read through a folder link out of the workspace")
	}
	os.WriteFile(filepath.Join(ws, "big.bin"), make([]byte, 2048), 0o600)
	if _, err := readWorkspaceFile(ws, filepath.Join(ws, "big.bin"), 1024); err == nil {
		t.Error("a file over the cap was read")
	}
	if b, err := readWorkspaceFile(ws, filepath.Join(ws, "big.bin"), 4096); err != nil || len(b) != 2048 {
		t.Errorf("a file of the workspace's own: %d %v", len(b), err)
	}
}
