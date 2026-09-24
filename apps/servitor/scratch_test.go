package servitor

// The scratch directory is where writing is free, so it must really be the
// run's own directory. These run the real setup command through sh against
// /tmp, the way a local command appliance does.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func localShell(ctx context.Context, cmd string) (string, error) {
	out, err := exec.CommandContext(ctx, "sh", "-c", cmd).CombinedOutput()
	return string(out), err
}

func testScratchDir(t *testing.T) string {
	t.Helper()
	dir := scratch_dir(fmt.Sprintf("test-%d-%d", os.Getpid(), time.Now().UnixNano()))
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestScratchSetupRefusesAPlantedSymlink(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	target := t.TempDir()
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := testScratchDir(t)
	if err := os.Symlink(target, dir); err != nil {
		t.Fatal(err)
	}
	if err := scratch_setup(context.Background(), localShell, dir); err == nil {
		t.Fatal("a symlink planted at the scratch path was accepted as the scratch directory")
	}
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("setup followed the planted link and changed its target to %v", fi.Mode().Perm())
	}
}

func TestScratchSetupCreatesAPrivateDirectory(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := testScratchDir(t)
	if err := scratch_setup(context.Background(), localShell, dir); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Errorf("scratch is %v, want a 0700 directory", fi.Mode())
	}
	// A second setup against our own directory (a re-run) is fine.
	if err := scratch_setup(context.Background(), localShell, dir); err != nil {
		t.Errorf("re-using our own scratch directory failed: %v", err)
	}
	if filepath.Dir(dir) != "/tmp" {
		t.Errorf("scratch escaped /tmp: %s", dir)
	}
}
