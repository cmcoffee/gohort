package filenotes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(name), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A made file is noted with what made it, when, and the request it answered;
// a title the agent gives is kept when the file is made again, and the line
// says all of it.
func TestANoteSaysWhatAFileIs(t *testing.T) {
	dir := t.TempDir()
	song := write(t, dir, "generated_music-6.mp3")
	at := time.Date(2026, 10, 28, 20, 6, 0, 0, time.Local)
	Record(song, Note{By: "generate_music", Request: "turn this in to a country rock song,\n generate the music", Made: at})
	SetTitle(song, "Gas Prices Are Too High")
	if got, want := LineFor(song), `"Gas Prices Are Too High", made by generate_music Oct 28 20:06, for "turn this in to a country rock song, generate the music"`; got != want {
		t.Errorf("line:\n got %s\nwant %s", got, want)
	}
	Record(song, Note{By: "generate_music", Made: at})
	if n, _ := Get(song); n.Title != "Gas Prices Are Too High" {
		t.Errorf("remaking a file keeps its title: %+v", n)
	}
	if LineFor(filepath.Join(dir, "never-noted.png")) != "" {
		t.Error("a file nothing noted has no line")
	}
}

// A copy into another folder carries the note, marked with whom it came from;
// a note for a file that is gone is dropped; the notes' own file is never
// noted; a long request is cut.
func TestNotesTravelAndTidy(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	src := write(t, a, "song.mp3")
	Record(src, Note{Title: "First", By: "generate_music", Request: strings.Repeat("word ", 100)})
	if n, _ := Get(src); len([]rune(n.Request)) > maxRequest {
		t.Errorf("request kept at %d runes", len([]rune(n.Request)))
	}
	dst := write(t, b, "song-2.mp3")
	Copy(src, dst, "Lyric & Music Generator")
	if n, ok := Get(dst); !ok || n.Title != "First" || n.From != "Lyric & Music Generator" {
		t.Errorf("the copy's note: %+v", n)
	}
	gone := write(t, a, "gone.png")
	Record(gone, Note{By: "image"})
	os.Remove(gone)
	Record(src, Note{By: "generate_music"})
	if _, ok := Get(gone); ok {
		t.Error("a note for a removed file is dropped when the folder's notes are next written")
	}
	Record(filepath.Join(a, Sidecar), Note{By: "x"})
	if _, ok := Get(filepath.Join(a, Sidecar)); ok {
		t.Error("the notes' own file is never noted")
	}
	SetTitle(write(t, a, "untracked.png"), "A cat")
	if n, _ := Get(filepath.Join(a, "untracked.png")); n.Title != "A cat" || n.Made.IsZero() {
		t.Errorf("titling a file nothing noted starts a note: %+v", n)
	}
}
