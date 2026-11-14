package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/deps"
)

// The bin dir's python3 runs the chosen interpreter, and where that path is
// absent (a container image) the system's own python3, never itself.
func TestThePythonShimRunsTheChosenInterpreterOrTheSystems(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	chosen := filepath.Join(dir, "chosen-python")
	if err := os.WriteFile(chosen, []byte("#!/bin/sh\necho chosen \"$@\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(dir, "python3")
	if err := os.WriteFile(shim, []byte(PythonShim(chosen)), 0755); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(sh, shim, "-c", "x").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "chosen -c x" {
		t.Fatalf("the shim did not hand its arguments to the chosen interpreter: %q %v", out, err)
	}
	if err := os.WriteFile(shim, []byte(PythonShim(filepath.Join(dir, "missing"))), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("/usr/bin/python3"); err == nil {
		if out, err := exec.Command(sh, shim, "-c", "print(1)").CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "1" {
			t.Errorf("with the chosen path absent, the system's python3 did not run: %q %v", out, err)
		}
	}
	if strings.Contains(PythonShim("/x"), "command -v python3") || strings.Contains(PythonShim("/x"), "which python3") {
		t.Error("the shim searches PATH for python3, which finds itself")
	}
}

// A script run's python3 is the interpreter gohort chose, on a backend whose
// userland is the host's: what the sandboxed script reports is what
// deps.ChosenPython says. A container keeps its image's own python3.
func TestASandboxedScriptRunsOnTheChosenPython(t *testing.T) {
	if got := scriptInterpreter(containerSandbox{kind: "podman"}, "python3"); got != "python3" {
		t.Errorf("a container's script run was pointed at the host's interpreter: %q", got)
	}
	if got := scriptInterpreter(noSandbox{}, "bash"); got != "bash" {
		t.Errorf("a non-Python interpreter was changed: %q", got)
	}
	want := deps.ChosenPython()
	if !want.OK {
		t.Skip("no Python interpreter on this host")
	}
	res := RunSandboxedScript(context.Background(), "python3", "import sys; print('%d.%d' % sys.version_info[:2])", "")
	if res.Err != nil {
		t.Skipf("the sandbox could not run here: %v", res.Err)
	}
	if got := strings.TrimSpace(res.Stdout); got != want.Version() {
		t.Errorf("the sandboxed script ran on Python %s, want the chosen %s (%s)", got, want.Version(), want.Path)
	}
}
