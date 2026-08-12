package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resetGohortLibDir clears the process-level cache so a test controls the lib
// dir rather than inheriting whatever an earlier test populated.
func resetGohortLibDir(t *testing.T) {
	t.Helper()
	// Restore afterwards: these are process-wide, and a later test that runs a
	// real sandbox would otherwise bind a temp dir this one has already
	// deleted.
	prev := WorkspacesDir()
	t.Cleanup(func() {
		SetWorkspacesDir(prev)
		gohortLibDirMu.Lock()
		gohortLibDirPath = ""
		gohortLibDirMu.Unlock()
	})
	SetWorkspacesDir(filepath.Join(t.TempDir(), "workspaces"))
	gohortLibDirMu.Lock()
	gohortLibDirPath = ""
	gohortLibDirMu.Unlock()
}

// Without bwrap nothing is bind-mounted, so PYTHONPATH must name the HOST
// directories. It named the in-sandbox mount points instead — paths that exist
// only inside a sandbox that was never entered.
//
// Observed on a macOS deployment, where bwrap does not exist at all: every
// shell tool doing `from gohort import fetch_url` died with ModuleNotFoundError
// on its first line, while the hook socket sat there working. That combination
// sends you looking for a broken install rather than a wrong path, and looking
// fails too — the helper really is on disk, just nowhere PYTHONPATH mentions.
// A tool author burned a session rediscovering the socket protocol by hand and
// shipping a shim for a module the framework already provides.
func TestSandboxPythonPathPointsAtRealDirsWithoutBwrap(t *testing.T) {
	resetGohortLibDir(t)

	got := sandboxPythonPath(false, "")
	if got == "" {
		t.Fatal("no PYTHONPATH at all — `from gohort import fetch_url` cannot resolve")
	}
	if strings.Contains(got, SandboxGohortLibMountPath) {
		t.Errorf("PYTHONPATH names the in-sandbox mount %q with no sandbox to mount it: %q",
			SandboxGohortLibMountPath, got)
	}

	// Every entry must exist, and one of them must actually hold the package.
	var found bool
	for _, p := range strings.Split(got, ":") {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			t.Errorf("PYTHONPATH entry %q does not exist: %v", p, err)
			continue
		}
		if _, err := os.Stat(filepath.Join(p, "gohort", "__init__.py")); err == nil {
			found = true
		}
	}
	if !found {
		t.Errorf("no PYTHONPATH entry contains gohort/__init__.py: %q", got)
	}
}

// Under bwrap the mount paths ARE the right answer: the host dirs are bound
// there and the host paths do not exist inside the namespace. Pinning both
// directions keeps a fix for one from silently breaking the other.
func TestSandboxPythonPathUsesMountPathsUnderBwrap(t *testing.T) {
	resetGohortLibDir(t)

	got := sandboxPythonPath(true, "")
	for _, want := range []string{SandboxGohortLibMountPath, SandboxPyDepsMountPath} {
		if !strings.Contains(got, want) {
			t.Errorf("PYTHONPATH %q missing the sandbox mount %q", got, want)
		}
	}
}

// The shim bin dir is the other half of the bridge and broke the same way. The
// old code prepended the mount path unconditionally and called it harmless — "a
// dead PATH entry, which the PATH search simply skips". Skipping it is exactly
// the problem: `fetch_url https://…` becomes command-not-found on a host whose
// hook is present and granted, so the documented shell interface is silently
// absent everywhere bwrap is not installed.
func TestSandboxShimBinDirResolvesWithoutBwrap(t *testing.T) {
	resetGohortLibDir(t)

	dir := sandboxShimBinDir(false)
	if dir == "" {
		t.Fatal("no shim bin dir — fetch_url / browse_page are unreachable as commands")
	}
	if dir == SandboxGohortBinMountPath {
		t.Fatalf("returned the in-sandbox mount %q with no sandbox to mount it", dir)
	}
	for _, shim := range []string{"fetch_url", "fetch_via", "browse_page"} {
		fi, err := os.Stat(filepath.Join(dir, shim))
		if err != nil {
			t.Errorf("shim %s not present at %s: %v", shim, dir, err)
			continue
		}
		if fi.Mode().Perm()&0111 == 0 {
			t.Errorf("shim %s is not executable (mode %v) — PATH would find it and fail", shim, fi.Mode())
		}
	}
}

// Under bwrap the mount path is correct: that is where the host bin dir is
// bound, and the host path does not resolve inside the namespace.
func TestSandboxShimBinDirUsesMountPathUnderBwrap(t *testing.T) {
	resetGohortLibDir(t)

	if dir := sandboxShimBinDir(true); dir != SandboxGohortBinMountPath {
		t.Errorf("shim bin dir = %q, want the mount %q", dir, SandboxGohortBinMountPath)
	}
}

// A caller-supplied PYTHONPATH has to survive — clobbering it would break a
// tool that ships its own helper modules alongside its script.
func TestSandboxPythonPathKeepsCallerEntries(t *testing.T) {
	resetGohortLibDir(t)

	got := sandboxPythonPath(true, "/caller/libs")
	if !strings.Contains(got, "/caller/libs") {
		t.Errorf("caller PYTHONPATH dropped: %q", got)
	}
	// Framework entries come first so a caller cannot shadow `gohort`.
	if strings.Index(got, SandboxGohortLibMountPath) > strings.Index(got, "/caller/libs") {
		t.Errorf("caller entry precedes the framework helper, so it can shadow it: %q", got)
	}
}
