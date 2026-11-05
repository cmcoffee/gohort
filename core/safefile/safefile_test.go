//go:build !windows

package safefile

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A link a script left where the server reads or writes is not followed: a
// read refuses it, and a write replaces the link with the file and leaves
// what it pointed at alone. A FIFO is refused without hanging.
func TestALinkIsNeitherReadThroughNorWrittenThrough(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(t.TempDir(), "server.db")
	if err := os.WriteFile(secret, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	planted := filepath.Join(dir, "tool.py")
	if err := os.Symlink(secret, planted); err != nil {
		t.Skip("no symlinks here")
	}
	if b, err := ReadFile(planted); err == nil {
		t.Fatalf("read through the link: %q", b)
	}
	if err := WriteFile(planted, []byte("print(1)"), 0700); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(secret); string(b) != "private" {
		t.Fatalf("wrote through the link: %q", b)
	}
	if info, _ := os.Lstat(planted); !info.Mode().IsRegular() {
		t.Fatal("the link is still there")
	}
	if b, err := ReadFile(planted); err != nil || string(b) != "print(1)" {
		t.Fatalf("the file: %q %v", b, err)
	}

	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Skip("no FIFOs here")
	}
	if _, err := Open(fifo); err == nil {
		t.Fatal("opened a FIFO")
	}
}

// rootFixture is a workspace, a folder beside it standing for the server's
// own data, and a file there worth protecting.
func rootFixture(t *testing.T) (ws, outside, secret string) {
	t.Helper()
	base := t.TempDir()
	ws, outside = filepath.Join(base, "ws"), filepath.Join(base, "server-data")
	if err := os.MkdirAll(filepath.Join(ws, "out"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	secret = filepath.Join(outside, "gohort.db")
	if err := os.WriteFile(secret, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	return ws, outside, secret
}

// A folder on the path swapped for a link out of the workspace leads nowhere:
// the server writes, reads and makes folders only inside it.
func TestAFolderSwappedForALinkLeadsNowhere(t *testing.T) {
	ws, outside, secret := rootFixture(t)
	os.RemoveAll(filepath.Join(ws, "out"))
	if err := os.Symlink(outside, filepath.Join(ws, "out")); err != nil {
		t.Skip("no symlinks here")
	}
	if err := WriteFileIn(ws, filepath.Join(ws, "out", "gohort.db"), []byte("overwritten"), 0o600); err == nil {
		t.Error("wrote through a folder link")
	}
	if b, _ := os.ReadFile(secret); string(b) != "private" {
		t.Fatalf("the server's file was overwritten: %q", b)
	}
	if b, err := ReadFileIn(ws, "out/gohort.db"); err == nil {
		t.Errorf("read through a folder link: %q", b)
	}
	if err := MkdirAllIn(ws, filepath.Join(ws, "out", "a", "b"), 0o700); err == nil {
		t.Error("made folders through a folder link")
	}
	if _, err := os.Stat(filepath.Join(outside, "a")); err == nil {
		t.Error("a folder was made outside the workspace")
	}
}

// A link at the file itself: a read refuses it, a write replaces it and
// leaves what it pointed at alone. A path that leaves the workspace by name is
// refused, and a FIFO is refused without hanging.
func TestALinkAtTheFileInAWorkspace(t *testing.T) {
	ws, _, secret := rootFixture(t)
	planted := filepath.Join(ws, "tool.py")
	if err := os.Symlink(secret, planted); err != nil {
		t.Skip("no symlinks here")
	}
	if b, err := ReadFileIn(ws, planted); err == nil {
		t.Fatalf("read through the link: %q", b)
	}
	if err := WriteFileIn(ws, planted, []byte("print(1)"), 0o700); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(secret); string(b) != "private" {
		t.Fatalf("wrote through the link: %q", b)
	}
	if b, err := ReadFileIn(ws, "tool.py"); err != nil || string(b) != "print(1)" {
		t.Fatalf("the file: %q %v", b, err)
	}
	for _, out := range []string{secret, "../server-data/gohort.db", filepath.Join(ws, "..", "server-data", "gohort.db")} {
		if _, err := ReadFileIn(ws, out); err == nil {
			t.Errorf("read %s, outside the workspace", out)
		}
		if err := WriteFileIn(ws, out, []byte("x"), 0o600); err == nil {
			t.Errorf("wrote %s, outside the workspace", out)
		}
	}
	if b, _ := os.ReadFile(secret); string(b) != "private" {
		t.Fatalf("the server's file was overwritten: %q", b)
	}
	fifo := filepath.Join(ws, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("no FIFOs here")
	}
	if _, err := OpenIn(ws, fifo); err == nil {
		t.Fatal("opened a FIFO")
	}
}

// Ordinary use: folders made and a file written and read back, by absolute
// path or by one relative to the workspace.
func TestWritingInAWorkspace(t *testing.T) {
	ws, _, _ := rootFixture(t)
	if err := MkdirAllIn(ws, filepath.Join(ws, "a", "b"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileIn(ws, filepath.Join(ws, "a", "b", "c.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, err := ReadFileIn(ws, "a/b/c.txt"); err != nil || string(b) != "hi" {
		t.Fatalf("read back: %q %v", b, err)
	}
	if err := MkdirAllIn(ws, ws, 0o700); err != nil {
		t.Errorf("the workspace itself: %v", err)
	}
}

// The race itself: a script swapping a folder for a link to the server's data
// as fast as it can, while the server writes into that folder. Nothing may
// ever land outside the workspace.
func TestASwapRacingTheWriteNeverEscapes(t *testing.T) {
	ws, outside, secret := rootFixture(t)
	out := filepath.Join(ws, "out")
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			os.RemoveAll(out)
			os.Symlink(outside, out)
			os.Remove(out)
			os.Mkdir(out, 0o700)
		}
	}()
	for i := 0; i < 3000; i++ {
		MkdirAllIn(ws, out, 0o700)
		WriteFileIn(ws, filepath.Join(out, "gohort.db"), []byte("overwritten"), 0o600)
	}
	close(stop)
	<-done
	if b, _ := os.ReadFile(secret); string(b) != "private" {
		t.Fatalf("a write escaped the workspace: the server's file reads %q", b)
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 1 {
		t.Errorf("something landed outside the workspace: %v", entries)
	}
}

// A cleanup after a failed write deletes inside the workspace only.
func TestARemoveThroughASwappedFolderDeletesNothingOutside(t *testing.T) {
	ws, outside, secret := rootFixture(t)
	os.RemoveAll(filepath.Join(ws, "out"))
	if err := os.Symlink(outside, filepath.Join(ws, "out")); err != nil {
		t.Skip("no symlinks here")
	}
	if err := RemoveIn(ws, filepath.Join(ws, "out", "gohort.db")); err == nil {
		t.Error("removed through a folder link")
	}
	if _, err := os.Stat(secret); err != nil {
		t.Fatalf("the server's file was deleted: %v", err)
	}
	os.WriteFile(filepath.Join(ws, "mine.txt"), []byte("x"), 0o600)
	if err := RemoveIn(ws, "mine.txt"); err != nil {
		t.Errorf("removing a file of the workspace's own: %v", err)
	}
}

// OpenFileIn is for the opens CreateIn does not cover, held to the same rule:
// no link out on the way, no link at the file, a regular file only.
func TestOpenFileInFollowsNoLinkOut(t *testing.T) {
	ws, outside, secret := rootFixture(t)
	os.RemoveAll(filepath.Join(ws, "out"))
	if err := os.Symlink(outside, filepath.Join(ws, "out")); err != nil {
		t.Skip("no symlinks here")
	}
	if f, err := OpenFileIn(ws, filepath.Join(ws, "out", "gohort.db"), os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		f.Close()
		t.Error("opened through a folder link")
	}
	os.Symlink(secret, filepath.Join(ws, "app.log"))
	if f, err := OpenFileIn(ws, filepath.Join(ws, "app.log"), os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		f.Close()
		t.Error("appended through a link at the file")
	}
	f, err := OpenFileIn(ws, "new.txt", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("a new file of the workspace's own: %v", err)
	}
	f.Close()
	if b, _ := os.ReadFile(secret); string(b) != "private" {
		t.Fatalf("the server's file changed: %q", b)
	}
}
