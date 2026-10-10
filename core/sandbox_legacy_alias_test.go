package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A tool or app script written before the rename imports gohort. It keeps
// working: the alias package binds that name to the oddjob module, so every
// import shape the helper documents resolves to the same objects.
func TestScriptsThatImportGohortStillWork(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	lib := t.TempDir()
	for _, pkg := range []struct{ name, src string }{{"oddjob", SandboxHookPythonShim}, {"gohort", SandboxHookLegacyAlias}} {
		dir := filepath.Join(lib, pkg.name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "__init__.py"), []byte(pkg.src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// -I ignores PYTHONPATH on purpose, so the directory goes in by argument.
	script := `
import sys
sys.path.insert(0, sys.argv[1])
import gohort
from gohort import fetch_url, HookError
from gohort import gohort
import oddjob
assert gohort is oddjob.oddjob, "the singleton is the same object"
assert fetch_url is oddjob.fetch_url, "a function is the same object"
assert HookError is oddjob.HookError
print("OK")
`
	cmd := exec.Command("python3", "-I", "-c", script, lib)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("legacy imports failed: %v\n%s", err, out)
	}
}
