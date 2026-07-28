package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// NOTE ON THE FILENAME: this cannot be called runtime_js_test.go. Go reads a
// trailing _js as the GOOS build constraint (js/wasm) and silently drops the
// file from the package — `go test` reports "no tests to run" and everything
// here never executes. Same trap for _linux, _windows, _amd64, and friends.
//
// The client runtime is plain JS with no build step, so a regression in it
// cannot be caught by `go build`. These harnesses stub the DOM and drive the
// runtime's pure and near-pure pieces (markdown section parsing, the shared
// document-core builders) under node.
//
// Skipped when node isn't installed: it's a real gate where node exists and a
// non-event where it doesn't, rather than a hard dependency for building.
func TestRuntimeJS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping runtime JS tests")
	}
	dir := filepath.Join("assets", "runtime", "testdata")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	ran := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, "_test.js") {
			continue
		}
		ran++
		t.Run(strings.TrimSuffix(name, "_test.js"), func(t *testing.T) {
			cmd := exec.Command(node, filepath.Join(dir, name))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s failed: %v\n%s", name, err, out)
			}
			// A harness that prints failures but exits 0 would pass silently.
			if strings.Contains(string(out), "FAIL") {
				t.Fatalf("%s reported failures:\n%s", name, out)
			}
		})
	}
	if ran == 0 {
		t.Fatal("no runtime JS harnesses found — did testdata move?")
	}
}

// The runtime is one program split across ordered fragments, so a syntax error
// in any of them only shows up once they're concatenated. assembleRuntimeJS
// already panics on a missing file; this checks the assembled result parses.
func TestRuntimeJSParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping runtime parse check")
	}
	f, err := os.CreateTemp(t.TempDir(), "runtime-*.js")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(runtimeJS); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if out, err := exec.Command(node, "--check", f.Name()).CombinedOutput(); err != nil {
		t.Fatalf("assembled runtime does not parse: %v\n%s", err, out)
	}
}
