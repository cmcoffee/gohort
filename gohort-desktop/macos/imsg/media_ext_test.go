//go:build darwin

package imsg

import "testing"

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
