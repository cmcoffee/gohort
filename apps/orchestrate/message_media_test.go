package orchestrate

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A song attached to a message rides the media list, which the transport sends
// as a file; an image stays an image. The collector gathered images only, so an
// MP3 went out as phantom-img-*.jpg and a video was dropped outright.
func TestAnAttachedSongIsNotSentAsAnImage(t *testing.T) {
	ws := t.TempDir()
	for name, body := range map[string]string{"song.mp3": "ID3song", "pic.png": "\x89PNGpic", "clip.mp4": "clip"} {
		if err := os.WriteFile(filepath.Join(ws, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	sess := &ToolSession{WorkspaceDir: ws}
	images, videos := messageMedia(sess, map[string]any{"attachments": []any{"song.mp3", "pic.png", "clip.mp4"}}, "here it is")
	if len(images) != 1 || len(videos) != 2 {
		t.Fatalf("want 1 image and 2 media files (song + clip), got %d images, %d media", len(images), len(videos))
	}
	images, videos = messageMedia(sess, map[string]any{"attachment": "song.mp3"}, "")
	if len(images) != 0 || len(videos) != 1 {
		t.Errorf("the singular alias routes the same way: %d images, %d media", len(images), len(videos))
	}
}
