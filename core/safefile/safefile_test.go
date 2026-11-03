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
