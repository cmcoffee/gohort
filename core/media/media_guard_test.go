package media

// ffmpeg picks its demuxer from the content, and a playlist names other files
// to read. Media inputs that are playlists or concat lists are refused.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAPlaylistIsNotMedia(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"hls.mp4":    "#EXTM3U\n#EXTINF:1,\nfile:///etc/passwd\n",
		"concat.mp4": "ffconcat version 1.0\nfile '/etc/passwd'\n",
		"bom.mp4":    "\xef\xbb\xbf#EXTM3U\n",
	} {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, []byte(body), 0600)
		if checkMediaFile(p) == nil {
			t.Errorf("%s was accepted as media", name)
		}
	}
	p := filepath.Join(dir, "real.mp4")
	_ = os.WriteFile(p, []byte("\x00\x00\x00\x18ftypmp42"), 0600)
	if err := checkMediaFile(p); err != nil {
		t.Errorf("an mp4 header was refused: %v", err)
	}
}
