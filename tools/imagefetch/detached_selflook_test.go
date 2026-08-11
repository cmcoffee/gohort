package imagefetch

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A detached render is the one path where being wrong is invisible: the turn
// that asked has ended, and the model still has to write "here it is, it shows
// X". Without the picture on that round it writes that line from the prompt,
// having never seen the result — which is exactly when a bad render ships.
func TestDetachedRenderShowsTheModelWhatItMade(t *testing.T) {
	sess := &ToolSession{Username: "alice", WorkspaceDir: t.TempDir(), Detached: true}
	msg, err := saveImageResult(sess, &ImageGenResult{URL: stagedPNG(t)}, "pic", "generated: a cat", ImageFromGenerated)
	if err != nil {
		t.Fatalf("saveImageResult: %v", err)
	}

	if got := len(sess.DrainViewImages()); got != 1 {
		t.Errorf("view images queued = %d, want 1 — the agent cannot check a render it was never shown", got)
	}
	if !strings.Contains(msg, "LOOK AT IT") {
		t.Errorf("the result must tell the model to look before describing:\n%s", msg)
	}
	// Delivery is a SEPARATE channel; showing it must not queue a second copy.
	if got := len(sess.Images); got != 1 {
		t.Errorf("attached images = %d, want exactly 1 — the picture must not be sent twice", got)
	}
}

// The inline path already looked, and must keep doing so.
func TestInlineRenderStillShowsTheModel(t *testing.T) {
	sess := &ToolSession{Username: "alice", WorkspaceDir: t.TempDir(), LLM: &Session{}}
	msg, err := saveImageResult(sess, &ImageGenResult{URL: stagedPNG(t)}, "gen", "generated: a cat", ImageFromGenerated)
	if err != nil {
		t.Fatalf("saveImageResult: %v", err)
	}
	if len(sess.DrainViewImages()) != 1 {
		t.Error("the inline path must still show the render to the model")
	}
	if !strings.Contains(msg, "LOOK AT IT") {
		t.Errorf("missing the look instruction:\n%s", msg)
	}
}
