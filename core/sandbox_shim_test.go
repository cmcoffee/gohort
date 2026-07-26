package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// EnsureGohortLibDir must drop the executable fetch-family shims into the
// <lib>/bin dir so a sandboxed script can call fetch_url / fetch_via /
// browse_page as ordinary commands instead of subprocessing an LLM tool name
// (the FileNotFoundError footgun). The dir is a subpath of the RO lib mount,
// so the existing bind covers it — no separate mount needed.
func TestEnsureGohortLibDirWritesShims(t *testing.T) {
	SetWorkspacesDir(filepath.Join(t.TempDir(), "workspaces"))
	// Reset the process-level cache so this test controls the lib dir rather
	// than inheriting whatever an earlier test in the package populated.
	gohortLibDirMu.Lock()
	gohortLibDirPath = ""
	gohortLibDirMu.Unlock()

	libBase := EnsureGohortLibDir()
	if libBase == "" {
		t.Fatal("EnsureGohortLibDir returned empty")
	}
	binDir := filepath.Join(libBase, "bin")
	for _, name := range []string{"_gohort_shim.py", "fetch_url", "fetch_via", "browse_page"} {
		fi, err := os.Stat(filepath.Join(binDir, name))
		if err != nil {
			t.Fatalf("shim %s missing: %v", name, err)
		}
		if fi.Mode().Perm()&0111 == 0 {
			t.Errorf("shim %s not executable (mode %v)", name, fi.Mode())
		}
	}
	disp, _ := os.ReadFile(filepath.Join(binDir, "_gohort_shim.py"))
	if !strings.Contains(string(disp), "def main(op, argv)") {
		t.Error("_gohort_shim.py missing main(op, argv)")
	}
	// The bare wrappers must delegate to the shared dispatcher, not duplicate it.
	w, _ := os.ReadFile(filepath.Join(binDir, "fetch_via"))
	if !strings.Contains(string(w), "from _gohort_shim import main") {
		t.Error("fetch_via wrapper doesn't delegate to _gohort_shim")
	}
	if SandboxGohortBinMountPath != SandboxGohortLibMountPath+"/bin" {
		t.Errorf("bin mount %q must be a subpath of lib mount %q (so the RO bind covers it)",
			SandboxGohortBinMountPath, SandboxGohortLibMountPath)
	}
}

// The shim bin dir must sit at the FRONT of the sandbox PATH so its
// fetch_url/browse_page win over any same-named host binary.
func TestSandboxEnvPrependsShimBin(t *testing.T) {
	var path string
	for _, kv := range sandboxEnv() {
		if strings.HasPrefix(kv, "PATH=") {
			path = kv[len("PATH="):]
			break
		}
	}
	if path == "" {
		t.Fatal("sandboxEnv produced no PATH")
	}
	if !strings.HasPrefix(path, SandboxGohortBinMountPath+":") {
		t.Errorf("PATH %q not prefixed with shim bin %q", path, SandboxGohortBinMountPath)
	}
}
