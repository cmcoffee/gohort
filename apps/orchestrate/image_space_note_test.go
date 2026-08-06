// Handing over the recent-image ids BEFORE the model goes looking for them.
//
// The failure this closes: asked to add someone to "the picture of me wasting
// away in the garage", the turn listed the workspace, found dozens of
// edit-<id>.png names with nothing to tell them apart, said "there are too many
// files", guessed at one, and ended promising work it never did. The ids and
// their captions existed the whole time — nothing put them in front of it.
package orchestrate

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// notePNG is a real, decodable image — the space verifies what it stores.
func notePNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// noteSession builds a session whose image space holds the given captions,
// oldest first — so the last one listed becomes image#1.
func noteSession(t *testing.T, captions ...string) *ToolSession {
	t.Helper()
	SetImageDir(t.TempDir())
	sess := &ToolSession{Username: "craig", AgentID: "wiwee", WorkspaceDir: t.TempDir()}
	for _, c := range captions {
		if ref := RecordRecentImage(sess, notePNG(t), c, ImageFromUser); ref == "" {
			t.Fatalf("image space unavailable: %q", c)
		}
	}
	return sess
}

func TestTheManifestArrivesWhenTheUserNamesAPicture(t *testing.T) {
	sess := noteSession(t, "received: Alex portrait", "edited: me wasting away in the garage")

	note := imageSpaceNote(sess, "Add Alex to the picture of me wasting away in the garage")
	if note == "" {
		t.Fatal("a request naming a picture must get the manifest")
	}
	// The ids it can pass, and the captions that tell them apart — the two
	// things a workspace listing of edit-<id>.png names cannot give it.
	if !strings.Contains(note, "image#1") || !strings.Contains(note, "wasting away in the garage") {
		t.Errorf("note must name the ids and what they are:\n%s", note)
	}
	// Marked as the framework speaking. The ids are ours, not something the
	// user typed, and thanking them for it reads as a non sequitur.
	if !strings.Contains(note, "FRAMEWORK NOTE") {
		t.Errorf("note must be tagged as framework context:\n%s", note)
	}
}

func TestThePictureTriggerIsTheUsersOwnWords(t *testing.T) {
	sess := noteSession(t, "edited: a cat")

	// A request to change an existing image has to say what to change, so
	// unlike a model writing a caption, the user's words are reliable here.
	for _, ask := range []string{
		"Add Alex to the picture of me in the garage",
		"Blend these so the guy in the second photo is on the hood",
		"can you photoshop him out",
		"send me that meme again",
		"make the screenshot bigger",
		"Do you still have those pics?",
	} {
		if imageSpaceNote(sess, ask) == "" {
			t.Errorf("a request about a picture must get the manifest: %q", ask)
		}
	}

	// And ordinary conversation must not. The note is cheap, not free, and a
	// picture list on a turn about dinner is noise that invites the model to
	// reference something nobody asked about.
	for _, ask := range []string{
		"what's the weather looking like tomorrow",
		"remind me to call the plumber",
		"that was an epic fail",
		"she gifted me a bottle of wine",
		"summarize the last three emails",
	} {
		if got := imageSpaceNote(sess, ask); got != "" {
			t.Errorf("ordinary talk must not carry the manifest: %q →\n%s", ask, got)
		}
	}
}

func TestAnEmptySpaceSaysNothing(t *testing.T) {
	sess := noteSession(t) // no images recorded
	if got := imageSpaceNote(sess, "send me that picture again"); got != "" {
		t.Errorf("nothing to list means nothing to say, got:\n%s", got)
	}
	if got := imageSpaceNote(nil, "send me that picture again"); got != "" {
		t.Errorf("a nil session must be silent, got:\n%s", got)
	}
}
