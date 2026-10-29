// Package filenotes keeps what each made file is.
//
// A file an agent makes is known by its name, and a generated name says
// nothing: a music agent's folder held generated_music.mp3 and
// generated_music-2 through -5, so neither the agent nor the person could tell
// which was "Gas Prices Are Too High", and an older song went out as the new
// one. A voice memo stopped being mistaken for a picture once it reached the
// agent as what was said; a made file gets the same, a line saying what it
// is, kept beside it and shown wherever the agent sees it.
//
// The framework records a note when a tool makes a file: what made it, when,
// and the person's words it answered. The agent may give it a title, or
// correct one. A copy into another folder (an agent handing a file back to the
// one that ran it) carries the note with it, under the copy's name.
//
// Notes live in one hidden file per folder (Sidecar), keyed by file name, so a
// note is found from the file's own path wherever the file is.
package filenotes

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Sidecar is the hidden file in each folder that holds its files' notes.
const Sidecar = ".gohort-files.json"

// Note is what one made file is.
type Note struct {
	// Title is what it is called, when a tool or the agent named it ("Gas
	// Prices Are Too High").
	Title string `json:"title,omitempty"`
	// By is the tool, or agent, that made it; From is the agent it was
	// copied from, for a file handed back.
	By   string `json:"by,omitempty"`
	From string `json:"from,omitempty"`
	// Request is the person's words it answered, as they wrote them.
	Request string    `json:"request,omitempty"`
	Made    time.Time `json:"made"`
}

// mu serializes this process's read-modify-write of every sidecar. Notes are
// written when files are made and titled, rarely and briefly.
var mu sync.Mutex

// maxRequest bounds the request a note keeps: enough to tell two requests
// apart, not the whole message.
const maxRequest = 160

// Record notes the file at path as made now, keeping a title it already had
// when n names none. A path whose folder cannot be written is not noted.
func Record(path string, n Note) {
	if n.Made.IsZero() {
		n.Made = time.Now()
	}
	n.Request = clip(oneLine(n.Request), maxRequest)
	n.Title = clip(oneLine(n.Title), maxRequest)
	update(path, func(cur Note, ok bool) Note {
		if ok && n.Title == "" {
			n.Title = cur.Title
		}
		return n
	})
}

// SetTitle names the file at path, keeping the rest of its note (or starting
// one, for a file nothing noted).
func SetTitle(path, title string) {
	title = clip(oneLine(title), maxRequest)
	update(path, func(cur Note, ok bool) Note {
		if !ok {
			cur.Made = modTime(path)
		}
		cur.Title = title
		return cur
	})
}

// Get is the note for the file at path.
func Get(path string) (Note, bool) {
	mu.Lock()
	defer mu.Unlock()
	n, ok := load(filepath.Dir(path))[filepath.Base(path)]
	return n, ok
}

// Copy gives the copy at dst the note of the file at src, marked as handed
// back from agent. Nothing to copy, nothing done.
func Copy(src, dst, agent string) {
	n, ok := Get(src)
	if !ok {
		return
	}
	if agent != "" {
		n.From = agent
	}
	update(dst, func(Note, bool) Note { return n })
}

// Line is the note as one line, for a listing or a reply: the title quoted,
// then who made it and when, and the request it answered.
func (n Note) Line() string {
	var parts []string
	if n.Title != "" {
		parts = append(parts, `"`+n.Title+`"`)
	}
	made := "made"
	if n.By != "" {
		made += " by " + n.By
	}
	if n.From != "" && n.From != n.By {
		made += " (from " + n.From + ")"
	}
	if !n.Made.IsZero() {
		made += " " + n.Made.Format("Jan 2 15:04")
	}
	parts = append(parts, made)
	if n.Request != "" {
		parts = append(parts, `for "`+n.Request+`"`)
	}
	return strings.Join(parts, ", ")
}

// LineFor is Line for the file at path, or "" when it has no note.
func LineFor(path string) string {
	if n, ok := Get(path); ok {
		return n.Line()
	}
	return ""
}

// update changes one file's note under the lock, dropping notes for files
// that no longer exist while it is there.
func update(path string, change func(cur Note, ok bool) Note) {
	dir, name := filepath.Dir(path), filepath.Base(path)
	if name == Sidecar || name == "." || name == string(filepath.Separator) {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	notes := load(dir)
	cur, ok := notes[name]
	notes[name] = change(cur, ok)
	for n := range notes {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			delete(notes, n)
		}
	}
	save(dir, notes)
}

func load(dir string) map[string]Note {
	notes := map[string]Note{}
	if b, err := os.ReadFile(filepath.Join(dir, Sidecar)); err == nil {
		json.Unmarshal(b, &notes)
	}
	return notes
}

// save writes the sidecar whole, through a temporary file, so a reader never
// sees half of one.
func save(dir string, notes map[string]Note) {
	b, err := json.MarshalIndent(notes, "", "  ")
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, Sidecar+".*")
	if err != nil {
		return
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return
	}
	tmp.Close()
	if err := os.Rename(tmp.Name(), filepath.Join(dir, Sidecar)); err != nil {
		os.Remove(tmp.Name())
	}
}

func modTime(path string) time.Time {
	if info, err := os.Stat(path); err == nil {
		return info.ModTime()
	}
	return time.Now()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
