package files

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// swapFixture is a workspace with a folder a script keeps swapping for a link
// to the server's own data while the test runs, and that data. stop ends the
// swapping.
func swapFixture(t *testing.T) (sess *ToolSession, secret string, stop func()) {
	t.Helper()
	base := t.TempDir()
	ws, outside := filepath.Join(base, "ws"), filepath.Join(base, "server-data")
	os.MkdirAll(filepath.Join(ws, "out"), 0o700)
	os.MkdirAll(outside, 0o700)
	secret = filepath.Join(outside, "gohort.db")
	os.WriteFile(secret, []byte("private"), 0o600)
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
			os.WriteFile(filepath.Join(out, "gohort.db"), []byte("mine"), 0o600)
		}
	}()
	return &ToolSession{WorkspaceDir: ws, Username: "u"}, secret, func() { close(quit); <-done }
}

// A script still running swaps a folder for a link to the server's data while
// the model writes into it, in every write mode. Nothing may land outside.
func TestWriteFileNeverWritesThroughASwappedFolder(t *testing.T) {
	sess, secret, stop := swapFixture(t)
	w := &WriteFileTool{}
	for i := 0; i < 1500; i++ {
		for _, mode := range []string{"overwrite", "append", "create"} {
			w.RunWithSession(map[string]any{"path": "out/gohort.db", "content": "overwritten", "mode": mode}, sess)
		}
		// A deeper path, whose folders are made on the way: none of them
		// may be made outside either.
		w.RunWithSession(map[string]any{"path": "out/sub/gohort.db", "content": "overwritten"}, sess)
	}
	stop()
	if b, _ := os.ReadFile(secret); string(b) != "private" {
		t.Fatalf("a write escaped the workspace: the server's file reads %q", b)
	}
	if entries, _ := os.ReadDir(filepath.Dir(secret)); len(entries) != 1 {
		t.Errorf("something landed outside the workspace: %v", entries)
	}
}

// And reading: what comes back is never the server's file.
func TestReadFileNeverReadsThroughASwappedFolder(t *testing.T) {
	sess, _, stop := swapFixture(t)
	r := &ReadFileTool{}
	for i := 0; i < 3000; i++ {
		if out, _ := r.RunWithSession(map[string]any{"path": "out/gohort.db"}, sess); strings.Contains(out, "private") {
			stop()
			t.Fatalf("read the server's file through a swapped folder: %q", out)
		}
	}
	stop()
}
