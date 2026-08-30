package bundle

// The staging half's promises: a filename we are willing to create, a path
// inside the configured base, and a purge that cannot be talked into removing
// something else.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withStagingDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := StagingDir
	StagingDir = func() string { return dir }
	t.Cleanup(func() { StagingDir = prev })
	return dir
}

// A browser-supplied filename is untrusted, but the EXTENSION has to survive:
// the expander dispatches on it, so a ".tar.gz" sanitized down to "dump" is a
// bundle nobody can open.
func TestSafeUploadNameSanitizes(t *testing.T) {
	cases := map[string]string{
		"dump.tar.gz":         "dump.tar.gz",
		"../../etc/passwd":    "passwd",
		`C:\Users\x\logs.zip`: "logs.zip",
		"weird;name|pipe.log": "weird_name_pipe.log",
		".hidden":             "hidden",
		"/":                   "",
		"..":                  "",
	}
	for in, want := range cases {
		if got := SafeUploadName(in); got != want {
			t.Errorf("SafeUploadName(%q) = %q, want %q", in, got, want)
		}
	}
}

// An unconfigured staging directory is an error naming the config key, not an
// empty path. Joining onto "" would put a customer's dump at the filesystem
// root, and the person who can fix it needs to be told which setting is missing.
func TestStagingRootRefusesAnUnconfiguredBase(t *testing.T) {
	prev := StagingDir
	StagingDir = nil
	t.Cleanup(func() { StagingDir = prev })
	if _, err := StagingRoot("u1", "b1"); err == nil {
		t.Fatal("an unconfigured staging dir must be an error")
	} else if !strings.Contains(err.Error(), "bundle_dir") {
		t.Errorf("the error should name the config key, got: %v", err)
	}
	StagingDir = func() string { return "/var/tmp/x" }
	for _, bad := range [][2]string{{"", "b1"}, {"u1", ""}, {"", ""}} {
		if _, err := StagingRoot(bad[0], bad[1]); err == nil {
			t.Errorf("StagingRoot(%q, %q) should refuse an incomplete identity", bad[0], bad[1])
		}
	}
}

// Identity components become directory names, so a crafted one must not be able
// to climb out of the base.
func TestStagingRootContainsATraversingIdentity(t *testing.T) {
	base := withStagingDir(t)
	got, err := StagingRoot("../../etc", "../../../root")
	if err != nil {
		t.Fatalf("StagingRoot: %v", err)
	}
	if !strings.HasPrefix(got, filepath.Clean(base)+string(os.PathSeparator)) {
		t.Errorf("a traversing identity escaped the base: %q not under %q", got, base)
	}
}

// PurgeStaging deletes a tree. The guard that it only ever deletes inside the
// configured base is the one thing here worth a test of its own: if StagingDir
// changes between constructing a path and purging it, the purge must decline
// rather than remove whatever the old path now points at.
func TestPurgeStagingWillNotDeleteOutsideTheConfiguredBase(t *testing.T) {
	base := withStagingDir(t)
	stage, err := StagingRoot("u1", "b1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "evidence.log"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Base moves out from under it. The path PurgeStaging now computes lives
	// under the NEW base, so the old tree must survive untouched.
	elsewhere := t.TempDir()
	StagingDir = func() string { return elsewhere }
	PurgeStaging("u1", "b1")
	if _, err := os.Stat(filepath.Join(stage, "evidence.log")); err != nil {
		t.Errorf("purge removed a tree outside the configured base: %v", err)
	}

	// Pointed back at the real base, it does its job.
	StagingDir = func() string { return base }
	PurgeStaging("u1", "b1")
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Errorf("purge left the staged tree behind: %v", err)
	}
}
