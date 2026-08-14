package core

// The gohort python helper is deployed best-effort, and every failure
// path used to be Debug-only or silent. That is the wrong volume for
// this particular failure: the only symptom that reaches anyone is a
// ModuleNotFoundError on the first line of a script, which names the
// script's import rather than the deployment that never happened.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGohortLibReportsWhyItCouldNotDeploy(t *testing.T) {
	// A run with no workspaces dir configured said nothing at all, at
	// any level — the case that leaves an operator with a broken tool
	// and an empty log.
	prevDir := WorkspacesDir()
	SetWorkspacesDir("")
	gohortLibDirMu.Lock()
	gohortLibDirPath, gohortLibWarned = "", false
	gohortLibDirMu.Unlock()
	t.Cleanup(func() {
		SetWorkspacesDir(prevDir)
		gohortLibDirMu.Lock()
		gohortLibDirPath, gohortLibWarned = "", false
		gohortLibDirMu.Unlock()
	})

	var lines []string
	prevLog := Log
	Log = func(v ...any) {
		if len(v) > 0 {
			lines = append(lines, fmt.Sprint(v[0]))
		}
	}
	t.Cleanup(func() { Log = prevLog })

	if got := EnsureGohortLibDir(); got != "" {
		t.Fatalf("with no workspaces dir there is nowhere to deploy, got %q", got)
	}
	if len(lines) == 0 {
		t.Fatal("the failure was silent — the only symptom left is ModuleNotFoundError inside a script")
	}
	joined := strings.Join(lines, "\n")
	// It must name the consequence, not just the fault: whoever reads
	// this is about to be told by an agent that a tool is broken.
	for _, want := range []string{"ModuleNotFoundError", "workspaces directory"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the warning should mention %q: %s", want, joined)
		}
	}

	// Once per process, not once per dispatch: this runs on every
	// sandboxed tool call.
	before := len(lines)
	EnsureGohortLibDir()
	EnsureGohortLibDir()
	if len(lines) != before {
		t.Errorf("warned again on later dispatches: %d → %d lines", before, len(lines))
	}
}

// And the happy path still deploys something importable.
func TestGohortLibDeploysAnImportablePackage(t *testing.T) {
	dir := t.TempDir()
	prevDir := WorkspacesDir()
	SetWorkspacesDir(filepath.Join(dir, "workspaces"))
	gohortLibDirMu.Lock()
	gohortLibDirPath, gohortLibWarned = "", false
	gohortLibDirMu.Unlock()
	t.Cleanup(func() {
		SetWorkspacesDir(prevDir)
		gohortLibDirMu.Lock()
		gohortLibDirPath, gohortLibWarned = "", false
		gohortLibDirMu.Unlock()
	})

	lib := EnsureGohortLibDir()
	if lib == "" {
		t.Fatal("deployment failed on a writable path")
	}
	// PYTHONPATH points at the directory CONTAINING the package, so
	// `import gohort` resolves the package dir beneath it.
	if _, err := os.Stat(filepath.Join(lib, "gohort", "__init__.py")); err != nil {
		t.Errorf("no importable gohort package under %s: %v", lib, err)
	}
}
