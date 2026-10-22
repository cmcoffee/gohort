package workspace

// A song attached from the workspace goes out as audio. The type was read
// from the bytes alone, and Go's sniffer cannot place an untagged MP3, FLAC,
// raw AAC, Ogg or Opus file, so those went out as generic files, which a
// messaging channel never sends: the picture arrived and the song did not.

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestASongTheSnifferCannotPlaceIsSentAsAudio(t *testing.T) {
	for name, data := range map[string][]byte{
		"song.mp3":   {0xff, 0xfb, 0x90, 0x64, 0x00, 0x0f, 0xf0, 0x00, 0x00, 0x69}, // an MP3 frame, no ID3 tag
		"song.flac":  []byte("fLaC\x00\x00\x00\x22\x10\x00\x10\x00"),
		"voice.opus": []byte("OggS\x00\x02\x00\x00\x00\x00\x00\x00\x00\x00OpusHead"),
	} {
		sess := &ToolSession{Username: "alice", AgentID: "agent1", WorkspaceDir: t.TempDir()}
		if err := os.WriteFile(filepath.Join(sess.WorkspaceDir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := AttachWorkspaceFile(sess, name, "", false); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(sess.Videos) != 1 || len(sess.Files) != 0 {
			t.Errorf("%s should go out on the audio/video channel, got %d video(s), %d file(s)", name, len(sess.Videos), len(sess.Files))
		}
	}
}

// The name only decides for bytes the sniffer could not place: text named
// .mp3 is still a file.
func TestTextNamedLikeASongIsStillAFile(t *testing.T) {
	sess := &ToolSession{Username: "alice", AgentID: "agent1", WorkspaceDir: t.TempDir()}
	os.WriteFile(filepath.Join(sess.WorkspaceDir, "notes.mp3"), []byte("these are my notes, not a song\n"), 0o644)
	if _, err := AttachWorkspaceFile(sess, "notes.mp3", "", false); err != nil {
		t.Fatal(err)
	}
	if len(sess.Videos) != 0 || len(sess.Files) != 1 {
		t.Errorf("text named .mp3 should stay a file: %d video(s), %d file(s)", len(sess.Videos), len(sess.Files))
	}
}
