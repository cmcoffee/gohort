package imagefetch

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func tinyPNG(t *testing.T, shade uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 3, 3))
	img.Set(0, 0, color.RGBA{shade, shade, shade, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// An edit of a person's picture that came in a while before is stopped
// once, saying when it came in, who sent it and the newer one; the same call
// again goes ahead. A picture just sent, or one the agent made, is not
// stopped, and neither is a picture named by a kept name.
func TestAnEditOfAPersonsOldPictureChecksOnce(t *testing.T) {
	saved := ImageDir()
	SetImageDir(t.TempDir())
	t.Cleanup(func() { SetImageDir(saved) })
	prev := CaptionOnRecord
	CaptionOnRecord = false
	t.Cleanup(func() { CaptionOnRecord = prev })
	sess := &ToolSession{Username: "alice", WorkspaceDir: t.TempDir(), ChatSessionID: "chat-1"}

	_, old := RecordRecentImageStable(sess, tinyPNG(t, 10), "received from Sam", ImageFromUser)
	_, made := RecordRecentImageStable(sess, tinyPNG(t, 20), "generated: a cat", ImageFromGenerated)
	_, newer := RecordRecentImageStable(sess, tinyPNG(t, 30), "received from Rory", ImageFromUser)
	edit := func(pic string) map[string]any {
		return map[string]any{"action": "edit", "picture": pic, "prompt": "replace the lady with Craig"}
	}

	now := time.Now()
	for _, pic := range []string{old, made, newer} {
		if err := stalePictureCheck(sess, edit(pic), now); err != nil {
			t.Errorf("%s just came in and was stopped: %v", pic, err)
		}
	}

	later := now.Add(time.Hour)
	err := stalePictureCheck(sess, edit(old), later)
	if err == nil {
		t.Fatal("an edit of a person's picture from an hour ago was not checked")
	}
	for _, want := range []string{old, "an hour ago", "received from Sam", "newer picture from a person is " + newer, "send it again", "same call again"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the check does not say %q:\n%s", want, err)
		}
	}
	if err := stalePictureCheck(sess, edit(old), later); err != nil {
		t.Errorf("the same call again was stopped again: %v", err)
	}
	if err := stalePictureCheck(sess, edit(made), later); err != nil {
		t.Errorf("the agent's own picture was stopped: %v", err)
	}
	if err := stalePictureCheck(sess, edit("image#ken_ref"), later); err != nil {
		t.Errorf("a kept picture was stopped: %v", err)
	}
	other := &ToolSession{Username: "alice", WorkspaceDir: sess.WorkspaceDir, ChatSessionID: "chat-2"}
	if err := stalePictureCheck(other, edit(old), later); err == nil {
		t.Error("another conversation did not check for itself")
	}
}
