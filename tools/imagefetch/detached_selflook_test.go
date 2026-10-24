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
	msg, err := saveImageResult(sess, &ImageGenResult{URL: stagedPNG(t)}, "pic", "generated: a cat", ImageFromGenerated, false)
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
	msg, err := saveImageResult(sess, &ImageGenResult{URL: stagedPNG(t)}, "gen", "generated: a cat", ImageFromGenerated, false)
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

// A pass that is a step toward a later one (deliver=false) is kept, not sent:
// a four-picture blend on a two-picture editor sent the person every
// in-between picture as its own message. The held picture is still shown to
// the model and named by the id the next pass takes it as.
func TestAHeldPassIsKeptAndNotSent(t *testing.T) {
	sess := &ToolSession{Username: "alice", WorkspaceDir: t.TempDir(), Detached: true}
	msg, err := saveImageResult(sess, &ImageGenResult{URL: stagedPNG(t)}, "edit", "edited: two of four", ImageFromEdited, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Images) != 0 {
		t.Errorf("a held pass was attached: %d image(s)", len(sess.Images))
	}
	held, refs := sess.ClaimHeldImages()
	if len(held) != 1 || len(refs) != 1 || refs[0] == "" || !strings.Contains(msg, refs[0]) || !strings.Contains(msg, "NOT sent") {
		t.Errorf("held %d picture(s) as %q: %s", len(held), refs, msg)
	}
	if len(sess.DrainViewImages()) != 1 {
		t.Error("the model should still see the step it is building on")
	}
	if holdsResult(map[string]any{}) || holdsResult(map[string]any{"deliver": true}) || !holdsResult(map[string]any{"deliver": false}) {
		t.Error("only deliver=false holds; left out, a render is sent")
	}

	// Inline, nothing is sent until attached: the held step is told not to be.
	inline := &ToolSession{Username: "alice", WorkspaceDir: t.TempDir(), LLM: &Session{}}
	msg, err = saveImageResult(inline, &ImageGenResult{URL: stagedPNG(t)}, "edit", "edited: two of four", ImageFromEdited, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "Do not attach it") || strings.Contains(msg, `workspace(action="attach"`) {
		t.Errorf("an inline held pass should say not to attach it: %s", msg)
	}
}
