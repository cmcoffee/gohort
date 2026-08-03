package imagefetch

// A render that outlives its turn. Ordinarily the tool stages the picture and
// tells the model to attach it — there is a next round, and the model is in it.
// A detached render has no next round: the turn that asked ended while it was
// still rendering. Whatever it produces has to be attached HERE or it is
// stored, announced as finished, and never sent to anyone.

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// stagedPNG writes a real (tiny) PNG somewhere the tool will read it from, the
// way a finished backend render arrives on disk.
func stagedPNG(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	path := filepath.Join(t.TempDir(), "render.png")
	if err := os.WriteFile(path, buf.Bytes(), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestDetachedRenderAttachesItself(t *testing.T) {
	sess := &ToolSession{Username: "alice", WorkspaceDir: t.TempDir(), Detached: true}
	msg, err := saveImageResult(sess, &ImageGenResult{URL: stagedPNG(t)}, "edit", "edited: a cat")
	if err != nil {
		t.Fatalf("saveImageResult: %v", err)
	}
	if len(sess.Images) != 1 {
		t.Fatalf("a detached render must attach its own output — nobody downstream will (%d attached)", len(sess.Images))
	}
	// And the text must not send the model after a path it should not touch:
	// attaching again is how the same picture went out twice.
	if strings.Contains(msg, "workspace(action=\"attach\"") && !strings.Contains(msg, "Do NOT call workspace") {
		t.Errorf("detached result should not ask for an attach it already did:\n%s", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "attached") {
		t.Errorf("the model needs to know the picture is already attached:\n%s", msg)
	}
}

func TestInlineRenderStillLeavesDeliveryToTheModel(t *testing.T) {
	// The inline path is unchanged on purpose. A generate can be an intermediate
	// step — the picture that gets blended, not the one that gets sent — so on a
	// turn that HAS a next round the model still decides what goes out.
	ws := t.TempDir()
	sess := &ToolSession{Username: "alice", WorkspaceDir: ws}
	msg, err := saveImageResult(sess, &ImageGenResult{URL: stagedPNG(t)}, "gen", "generated: a cat")
	if err != nil {
		t.Fatalf("saveImageResult: %v", err)
	}
	if len(sess.Images) != 0 {
		t.Errorf("inline renders must not auto-attach: %d attached", len(sess.Images))
	}
	if !strings.Contains(msg, "workspace(action=\"attach\"") {
		t.Errorf("inline result should still ask for the attach:\n%s", msg)
	}
	// Either way the file is staged in the workspace.
	entries, _ := os.ReadDir(ws)
	if len(entries) == 0 {
		t.Error("the render should be saved to the workspace")
	}
}
