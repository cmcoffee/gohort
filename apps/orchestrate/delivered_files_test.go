package orchestrate

import (
	"encoding/base64"
	"testing"

	. "github.com/cmcoffee/oddjob/core"
)

// What a turn delivered that is not a picture is kept with enough to offer it
// again: a song as audio (it rides the video list), a clip as video, a
// document as a file. Only pictures, audio and video render in the page; the
// rest is a download, so a kept HTML or SVG file cannot run as the viewer.
func TestDeliveredSongsAndFilesAreKept(t *testing.T) {
	saved := ImageDir()
	SetImageDir(t.TempDir())
	t.Cleanup(func() { SetImageDir(saved) })

	song := base64.StdEncoding.EncodeToString(append([]byte("ID3\x03\x00"), make([]byte, 64)...))
	clip := base64.StdEncoding.EncodeToString(append([]byte("\x00\x00\x00\x20ftypisom"), make([]byte, 64)...))
	doc := FileAttachment{Name: "report.html", MimeType: "text/html; charset=utf-8", Data: base64.StdEncoding.EncodeToString([]byte("<p>hi</p>"))}
	kept := keepDeliveredFiles("alice", []string{song, clip}, []FileAttachment{doc})
	if len(kept) != 3 {
		t.Fatalf("want 3 kept, got %+v", kept)
	}
	if kept[0].Kind != "audio" || kept[1].Kind != "video" || kept[2].Kind != "file" || kept[2].Name != "report.html" {
		t.Errorf("kinds and names: %+v", kept)
	}
	if _, mt, err := LoadChatAttachment("alice", kept[0].ID); err != nil || mt != "audio/mpeg" {
		t.Errorf("the song reloads as audio: %q %v", mt, err)
	}
	for mt, want := range map[string]bool{"audio/mpeg": true, "video/mp4": true, "image/png": true, "image/svg+xml": false, "text/html; charset=utf-8": false, "application/pdf": false} {
		if inlineAttachmentType(mt) != want {
			t.Errorf("inline(%q) should be %v", mt, want)
		}
	}
	if keepDeliveredFiles("", []string{song}, nil) != nil {
		t.Error("nothing is kept without an owner")
	}
}
