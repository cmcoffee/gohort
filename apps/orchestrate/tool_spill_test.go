package orchestrate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A spill file holds the response itself. The banner and the status line in
// front of a JSON body made it unparseable, and an agent could not pull the
// audio out of a response it had in full.
func TestASpillFileHoldsTheRawBody(t *testing.T) {
	jsonBody := `{"id":"v1_x","steps":[{"content":[{"data":"SUQz"}]}]}`
	framed := untrustedContentFence + "HTTP 200 OK\n" + jsonBody
	if raw, cleaned := spillFileBody(framed); raw != jsonBody || !cleaned {
		t.Errorf("the banner and status line should come off a JSON body, got %q", raw)
	}
	if raw, _ := spillFileBody(untrustedContentFence + untrustedContentFence + jsonBody); raw != jsonBody {
		t.Errorf("repeated banners should all come off, got %q", raw)
	}
	text := "HTTP 404 Not Found\nno such route"
	if raw, _ := spillFileBody(untrustedContentFence + text); raw != text {
		t.Errorf("a plain-text body keeps its status line, got %q", raw)
	}

	dir := t.TempDir()
	sess := &ToolSession{WorkspaceDir: dir}
	big := `{"id":"v1_x","data":"` + strings.Repeat("A", spillThresholdBytes()) + `"}`
	stub, ok := maybeSpillToolResult(sess, "generate_music", untrustedContentFence+"HTTP 200 OK\n"+big)
	if !ok {
		t.Fatal("a body over the threshold should spill")
	}
	if !strings.Contains(stub, "ready to parse") || !strings.Contains(stub, "UNTRUSTED EXTERNAL CONTENT") {
		t.Errorf("the stub should keep the banner and say the file is the raw body:\n%s", stub[:300])
	}
	files, _ := filepath.Glob(filepath.Join(dir, spillDirName, "generate_music_*"))
	if len(files) != 1 || !strings.HasSuffix(files[0], ".json") {
		t.Fatalf("expected one .json spill file, got %v", files)
	}
	data, _ := os.ReadFile(files[0])
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Errorf("the spill file should parse as JSON: %v", err)
	}
}
