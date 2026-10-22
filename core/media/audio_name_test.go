package media

import "testing"

// A song the sniffer cannot place is audio by its name; anything the sniffer
// did place keeps its answer.
func TestUnsniffedAudioGoesByName(t *testing.T) {
	for _, c := range []struct {
		sniffed, name string
		want          bool
	}{
		{"application/octet-stream", "song.mp3", true},
		{"application/octet-stream", "Song.FLAC", true},
		{"application/ogg", "voice.opus", true},
		{"application/ogg", "clip.ogg", true},
		{"application/octet-stream", "archive.zip", false},
		{"text/plain; charset=utf-8", "notes.mp3", false},
		{"application/pdf", "report.m4a", false},
	} {
		if got := IsUnsniffedAudio(c.sniffed, c.name); got != c.want {
			t.Errorf("IsUnsniffedAudio(%q, %q) = %v, want %v", c.sniffed, c.name, got, c.want)
		}
	}
}
