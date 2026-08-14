// Refusing to let the agent's own render stand in for a photograph.
//
// The case these cover came from a live session: an agent with no picture of
// anyone rendered a portrait unprompted, and on a later turn passed that render
// back in as "the reference photo" with the face to be kept recognizable. Both
// halves of the guard matter, and the second one is why this file has as many
// passing cases as refusing ones — iterating on your own render is the ring's
// whole purpose, and a check that broke it would be worse than the bug.
package imagefetch

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// refPNG is a real, decodable image — the space verifies what it stores.
func refPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.RGBA{B: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// ringSession builds a session whose ring holds the given entries, oldest
// first, and returns the stable id of each in the order they were added.
func ringSession(t *testing.T, entries ...ImageOrigin) (*ToolSession, []string) {
	t.Helper()
	SetImageDir(t.TempDir())
	sess := &ToolSession{Username: "craig", AgentID: "wren", ChatSessionID: "s1", WorkspaceDir: t.TempDir()}
	ids := make([]string, 0, len(entries))
	for _, o := range entries {
		_, stable := RecordRecentImageStable(sess, refPNG(t), "test: "+string(o), o)
		if stable == "" {
			t.Fatalf("image space unavailable for origin %q", o)
		}
		ids = append(ids, stable)
	}
	return sess, ids
}

func TestOwnRenderCannotBeAReferencePhoto(t *testing.T) {
	sess, ids := ringSession(t, ImageFromGenerated)

	err := refuseInventedReference(sess, "Keep the person's face recognizable from the reference photo", ids)
	if err == nil {
		t.Fatal("a render passed as a reference photo must be refused")
	}
	// The refusal has to say WHY, or the retry is the same call again.
	for _, want := range []string{"MADE yourself", "not a photograph"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must explain the problem, got: %v", err)
		}
	}
	// And it has to say there is nothing to pass instead, because there isn't.
	if !strings.Contains(err.Error(), "have not been given a picture") {
		t.Errorf("with an empty library the refusal must say so, got: %v", err)
	}
}

func TestIteratingOnYourOwnRenderStillWorks(t *testing.T) {
	sess, ids := ringSession(t, ImageFromGenerated)

	// No likeness claim: this is the ring's primary use and must not be caught.
	for _, prompt := range []string{
		"make it snowy",
		"warmer light, less contrast",
		"lose the hat",
		"crop it square",
	} {
		if err := refuseInventedReference(sess, prompt, ids); err != nil {
			t.Errorf("iteration must not be refused (%q): %v", prompt, err)
		}
	}
}

func TestOneRealPhotographIsEnough(t *testing.T) {
	sess, ids := ringSession(t, ImageFromGenerated, ImageFromUser)

	// A composite drawing a likeness from the real photo and scenery from the
	// render is exactly what the edit path is for.
	if err := refuseInventedReference(sess, "put their face on the reference photo", ids); err != nil {
		t.Errorf("a set containing a real photograph must pass: %v", err)
	}
}

func TestARealPhotographPointsTheRetryAtItself(t *testing.T) {
	sess, ids := ringSession(t, ImageFromUser, ImageFromGenerated)

	// Pass ONLY the render; the refusal should name the photo it should have used.
	err := refuseInventedReference(sess, "keep the same person", ids[1:])
	if err == nil {
		t.Fatal("passing only the render must still be refused")
	}
	if !strings.Contains(err.Error(), ids[0]) {
		t.Errorf("refusal must name the real picture to pass instead (%s), got: %v", ids[0], err)
	}
}

func TestEditedOutputCountsAsTheAgentsOwn(t *testing.T) {
	sess, ids := ringSession(t, ImageFromEdited)

	// An edit of an invention is still an invention — the whole point of the
	// drift this guards.
	if err := refuseInventedReference(sess, "same person, different background", ids); err == nil {
		t.Fatal("an edited render is agent-made and must be refused as a reference")
	}
}

func TestUnknownProvenanceIsNotTreatedAsInvented(t *testing.T) {
	sess, _ := ringSession(t, ImageFromUser)

	// A ref that resolves to nothing reads as "nobody said", not as the
	// agent's own — the same direction AgentMade takes everywhere else.
	if err := refuseInventedReference(sess, "keep their face", []string{"image#r.deadbeef"}); err != nil {
		t.Errorf("unknown provenance must not be refused: %v", err)
	}
}

func TestOriginForRefReadsEveryRefForm(t *testing.T) {
	sess, ids := ringSession(t, ImageFromUser, ImageFromGenerated)

	// Stable id, and the position it currently occupies. The generated one was
	// saved last, so it is image#1.
	if got := OriginForRef(sess, ids[1]); got != ImageFromGenerated {
		t.Errorf("stable id: want %q, got %q", ImageFromGenerated, got)
	}
	if got := OriginForRef(sess, "image#1"); got != ImageFromGenerated {
		t.Errorf("position 1: want %q, got %q", ImageFromGenerated, got)
	}
	if got := OriginForRef(sess, "image#2"); got != ImageFromUser {
		t.Errorf("position 2: want %q, got %q", ImageFromUser, got)
	}
	// Out of range and nonsense both read as "nobody said".
	if got := OriginForRef(sess, "image#99"); got != ImageOriginUnknown {
		t.Errorf("out of range: want unknown, got %q", got)
	}
}
