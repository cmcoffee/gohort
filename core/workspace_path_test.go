package core

// ResolveWorkspacePath is the containment check for every host-side read or
// write the gohort process does on a sandbox's behalf (tool state copies
// among them), so each escape it exists to stop is pinned here.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveWorkspacePathContainsEveryEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "data", "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	for _, ok := range []string{"data", "data/sub", "new/dir", "state"} {
		got, err := ResolveWorkspacePath(root, ok)
		if err != nil {
			t.Errorf("%q should resolve: %v", ok, err)
			continue
		}
		if want := filepath.Join(root, ok); got != want {
			t.Errorf("%q resolved to %q, want %q", ok, got, want)
		}
	}
	for _, bad := range []string{
		"/etc", "../x", "data/../../x", "link", "link/inner", "data/../link",
	} {
		if got, err := ResolveWorkspacePath(root, bad); err == nil {
			t.Errorf("%q should be refused, resolved to %q", bad, got)
		}
	}
}
