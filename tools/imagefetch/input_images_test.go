package imagefetch

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A render names the pictures it was given and hands back the bytes behind
// them, so the framework can keep back a step this turn was sending and the
// result can say what went into it.
func TestARenderNamesThePicturesItBuildsOn(t *testing.T) {
	sess := &ToolSession{Username: "alice", WorkspaceDir: t.TempDir()}
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sess.WorkspaceDir, "edit-step.png"), buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	refs, data := (&ImageTool{}).InputImages(map[string]any{"action": "edit", "images": []any{"edit-step.png", "image#nowhere"}}, sess)
	if len(refs) != 2 || refs[0] != "edit-step.png" || refs[1] != "image#nowhere" {
		t.Errorf("refs = %q, want both as given", refs)
	}
	if len(data) != 1 || !bytes.Equal(data[0], buf.Bytes()) {
		t.Errorf("want the bytes of the one that resolves, got %d", len(data))
	}
	if refs, _ := (&ImageTool{}).InputImages(map[string]any{"action": "find", "images": []any{"edit-step.png"}}, sess); len(refs) != 0 {
		t.Error("only a render builds on pictures")
	}
}
