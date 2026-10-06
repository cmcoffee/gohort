package scribe

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The Publish dialog's script parses, with el() and the helpers it is
// handed defined around it. A syntax slip in a Go string is invisible until a
// browser refuses the whole script and the Publish button does nothing.
func TestPublishDialogScriptParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping JS syntax check")
	}
	f := filepath.Join(t.TempDir(), "publish.js")
	if err := os.WriteFile(f, []byte("var act = "+guidePublishAction+";\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, "--check", f).CombinedOutput(); err != nil {
		t.Fatalf("the Publish dialog script does not parse:\n%s", out)
	}
}
