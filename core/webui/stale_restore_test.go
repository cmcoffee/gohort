package webui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A save stamps, a GET / cross-origin / failed request does not, and only a
// page restored from before the stamp reloads. Skipped without node.
func TestStaleRestoreScript(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	body := strings.TrimSpace(StaleRestoreScript)
	body = strings.TrimSuffix(strings.TrimPrefix(body, "<script>"), "</script>")
	script := filepath.Join(t.TempDir(), "stale.js")
	if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, filepath.Join("testdata", "stale_restore_harness.js"), script).CombinedOutput()
	if err != nil || strings.Contains(string(out), "FAIL") {
		t.Fatalf("harness: %v\n%s", err, out)
	}
}
