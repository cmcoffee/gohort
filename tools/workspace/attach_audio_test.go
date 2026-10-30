package workspace

// A song attached from the workspace goes out as audio. The type was read
// from the bytes alone, and Go's sniffer cannot place an untagged MP3, FLAC,
// raw AAC, Ogg or Opus file, so those went out as generic files, which a
// messaging channel never sends: the picture arrived and the song did not.

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/filenotes"
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

// A song with a title goes out with the title in its tag, so a messaging
// connector can name the clip by it: it arrived as phantom-vid-*.mp3. One
// with no title goes out as it is.
func TestASongGoesOutWithItsTitle(t *testing.T) {
	sess := &ToolSession{Username: "alice", AgentID: "agent1", WorkspaceDir: t.TempDir()}
	song := []byte{0xff, 0xfb, 0x90, 0x64, 0x00, 0x0f, 0xf0, 0x00, 0x00, 0x69}
	for _, name := range []string{"titled.mp3", "untitled.mp3"} {
		os.WriteFile(filepath.Join(sess.WorkspaceDir, name), song, 0o644)
	}
	filenotes.SetTitle(filepath.Join(sess.WorkspaceDir, "titled.mp3"), "Money in the Cloud")
	for _, name := range []string{"titled.mp3", "untitled.mp3"} {
		if _, err := AttachWorkspaceFile(sess, name, "", false); err != nil {
			t.Fatal(err)
		}
	}
	if len(sess.Videos) != 2 {
		t.Fatalf("both songs go out: %d", len(sess.Videos))
	}
	titled, _ := base64.StdEncoding.DecodeString(sess.Videos[0])
	if !bytes.HasPrefix(titled, []byte("ID3")) || !bytes.Contains(titled, []byte("TIT2")) || !bytes.HasSuffix(titled, song) {
		t.Errorf("the titled song carries its title ahead of the same audio: %q", titled)
	}
	if untitled, _ := base64.StdEncoding.DecodeString(sess.Videos[1]); !bytes.Equal(untitled, song) {
		t.Errorf("an untitled song goes out unchanged: %q", untitled)
	}
}
