//go:build darwin

package imsg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An audio file is named for what it is, on either list: a song sent on the
// image list went out as phantom-img-*.jpg, and on the video list it would
// have been .mp4. Images and video keep their own names.
func TestAudioIsNamedAsAudio(t *testing.T) {
	cases := []struct {
		data []byte
		want string
	}{
		{[]byte("ID3\x03\x00\x00\x00"), ".mp3"},
		{[]byte{0xFF, 0xFB, 0x90, 0x44}, ".mp3"},
		{[]byte{0xFF, 0xF1, 0x50, 0x80}, ".aac"},
		{[]byte("RIFF\x00\x00\x00\x00WAVEfmt "), ".wav"},
		{[]byte("OggS\x00\x02"), ".ogg"},
		{[]byte("fLaC\x00\x00"), ".flac"},
		{[]byte("\x00\x00\x00\x20ftypM4A \x00\x00"), ".m4a"},
	}
	for _, c := range cases {
		if got := audioExt(c.data); got != c.want {
			t.Errorf("audioExt(%q) = %q, want %q", c.data, got, c.want)
		}
		if got := videoExt(c.data); got != c.want {
			t.Errorf("videoExt(%q) = %q, want the audio name %q", c.data, got, c.want)
		}
	}
	for _, notAudio := range [][]byte{
		{0xFF, 0xD8, 0xFF, 0xE0}, // JPEG
		[]byte("\x89PNG\r\n\x1a\n"),
		[]byte("\x00\x00\x00\x20ftypisom"), // MP4 video
	} {
		if a := audioExt(notAudio); a != "" {
			t.Errorf("%q is not audio, got %q", notAudio, a)
		}
	}
	if videoExt([]byte("\x00\x00\x00\x20ftypisom")) != ".mp4" {
		t.Error("an mp4 video keeps .mp4")
	}
}

// A spoken reply is written under the name the server gave it, in a directory
// of its own, so Messages shows it by what it says; anything else, or a clip
// with no name, keeps a temp name. Both under $TMPDIR, which Messages can read.
func TestClipTempPath(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	p, err := clipTempPath("audio", "Wren - Sure, at noon.", ".mp3")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(p) != "Wren - Sure, at noon..mp3" || !strings.HasPrefix(filepath.Base(filepath.Dir(p)), "gohort-voice-") {
		t.Errorf("named clip path: %s", p)
	}
	os.WriteFile(p, []byte("x"), 0o600)
	removeClip(p)
	if _, err := os.Stat(filepath.Dir(p)); !os.IsNotExist(err) {
		t.Errorf("the clip's directory should go with it: %v", err)
	}
	for _, c := range []struct{ kind, name string }{{"video", "Wren - hi"}, {"audio", ""}, {"audio", "../.."}} {
		p, err := clipTempPath(c.kind, c.name, ".mp3")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(filepath.Base(p), "phantom-vid-") && c.name != "../.." {
			t.Errorf("%s %q should keep a temp name: %s", c.kind, c.name, p)
		}
		if strings.Contains(p, "..") && c.name == "../.." {
			t.Errorf("a name must not climb out of its directory: %s", p)
		}
		removeClip(p)
	}
}

func TestSafeClipName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Sure, at noon.", "Sure, at noon."},
		{"a/b\\c:d", "a-b-c-d"},
		{"line\nbreak", "line break"},
		{"..hidden", "hidden"},
		{"", ""},
	}
	for _, c := range cases {
		if got := safeClipName(c.in); got != c.want {
			t.Errorf("safeClipName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
