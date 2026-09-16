package core

// Paged tool output.
//
// A tool reply is capped so one call cannot fill the context, and the cap
// used to be the end of the road: fetch_knowledge_doc read a document from
// the top and stopped, and a command's output was cut at 10,000 characters
// with a note saying how much there had been. An agent that needed the
// rest had no move — re-reading returned the same opening slice, and
// re-running a command is not free and not always safe.
//
// The fix is the same shape everywhere: the reply carries an offset the
// agent can pass back to read the next window. For a document the window
// is cut from a fresh reassembly each call, so nothing is kept. For command
// output the full capture has to be kept the moment it spills (see
// OutputStore below), because the source cannot be asked again.

import (
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// WindowText returns up to max bytes of text starting at offset, and the
// absolute offset just past what was returned — the value a caller prints
// for the agent to pass back as the next offset. A window that would end
// mid-document is shortened to the last paragraph break past its midpoint,
// or failing that the last line break, so a page never ends mid-sentence
// when a boundary is available; a window that reaches the end of the text
// is returned whole. Offsets are clamped into the text and moved back off
// a multi-byte character's tail, and the cut never lands inside one, so any
// offset the agent passes is safe. max <= 0 returns everything from offset.
func WindowText(text string, offset, max int) (window string, end int) {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(text) {
		return "", len(text)
	}
	for offset > 0 && !utf8.RuneStart(text[offset]) {
		offset--
	}
	rest := text[offset:]
	if max <= 0 || len(rest) <= max {
		return rest, len(text)
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(rest[cut]) {
		cut--
	}
	page := rest[:cut]
	if idx := strings.LastIndex(page, "\n\n"); idx > cut/2 {
		page = page[:idx]
	} else if idx := strings.LastIndex(page, "\n"); idx > cut/2 {
		page = page[:idx]
	}
	return page, offset + len(page)
}

// --- spilled command output ------------------------------------------------

// Spilled output is kept in memory only, for a bounded time, under an
// unguessable id. Nothing is written to disk: a command's output can hold
// anything the appliance holds. The bounds keep one runaway `cat` from
// holding memory for the day.
const (
	spilledOutputTTL        = 30 * time.Minute
	spilledOutputEntryBytes = 2 << 20  // per capture; beyond this the head is kept and the note says so
	spilledOutputTotalBytes = 16 << 20 // across every capture; oldest evicted first
)

type spilledEntry struct {
	text string
	kept time.Time
}

var spilled struct {
	mu      sync.Mutex
	entries map[string]*spilledEntry
	total   int
}

// spilledNow is time.Now, swappable so a test can age entries.
var spilledNow = time.Now

// keepSpilled stores text and returns its id, evicting expired entries and
// then the oldest until the total fits.
func keepSpilled(text string) string {
	spilled.mu.Lock()
	defer spilled.mu.Unlock()
	if spilled.entries == nil {
		spilled.entries = map[string]*spilledEntry{}
	}
	now := spilledNow()
	for id, e := range spilled.entries {
		if now.Sub(e.kept) > spilledOutputTTL {
			spilled.total -= len(e.text)
			delete(spilled.entries, id)
		}
	}
	for spilled.total+len(text) > spilledOutputTotalBytes && len(spilled.entries) > 0 {
		var oldest string
		var oldestAt time.Time
		for id, e := range spilled.entries {
			if oldest == "" || e.kept.Before(oldestAt) {
				oldest, oldestAt = id, e.kept
			}
		}
		spilled.total -= len(spilled.entries[oldest].text)
		delete(spilled.entries, oldest)
	}
	id := UUIDv4()
	spilled.entries[id] = &spilledEntry{text: text, kept: now}
	spilled.total += len(text)
	return id
}

// lookupSpilled returns a kept capture, or false when the id is unknown or
// expired.
func lookupSpilled(id string) (string, bool) {
	spilled.mu.Lock()
	defer spilled.mu.Unlock()
	e, ok := spilled.entries[id]
	if !ok {
		return "", false
	}
	if spilledNow().Sub(e.kept) > spilledOutputTTL {
		spilled.total -= len(e.text)
		delete(spilled.entries, id)
		return "", false
	}
	return e.text, true
}

// SpillOutput caps a tool's captured text at max characters for the reply.
// Text within the cap is returned as is. Text over it is kept in the spill
// store and the first window is returned with a note naming the id and the
// offset to pass back to tool, so the agent reads the rest by paging instead
// of re-running the command — which was the only move it had before, and
// one that costs a round trip and is not always safe to make twice.
func SpillOutput(text string, max int, tool string) string {
	if max <= 0 || len(text) <= max {
		return text
	}
	kept := text
	clipped := ""
	if len(kept) > spilledOutputEntryBytes {
		kept, _ = WindowText(kept, 0, spilledOutputEntryBytes)
		clipped = fmt.Sprintf(" Only the first %d chars of %d were kept.", len(kept), len(text))
	}
	id := keepSpilled(kept)
	window, end := WindowText(kept, 0, max)
	return window + spillNote(tool, id, 0, end, len(kept), kept) + clipped
}

// PageOutput returns the window of a kept capture starting at offset, with
// the same note SpillOutput writes when more remains. An unknown id is an
// error the agent can act on: the capture expired, or was made by another
// instance (a peer's exec keeps its own store).
func PageOutput(id string, offset, max int, tool string) (string, error) {
	text, ok := lookupSpilled(strings.TrimSpace(id))
	if !ok {
		return "", fmt.Errorf("output_id %q is unknown here — the capture has expired (kept %s), or was made by another instance; re-run the command", id, spilledOutputTTL)
	}
	if offset >= len(text) {
		return fmt.Sprintf("offset %d is past the end of this output (%d chars); it has been read in full.", offset, len(text)), nil
	}
	window, end := WindowText(text, offset, max)
	if end >= len(text) {
		return window + fmt.Sprintf("\n... [end of output: chars %d–%d of %d]", offset, end, len(text)), nil
	}
	return window + spillNote(tool, id, offset, end, len(text), text), nil
}

// spillNote is the truncation trailer: where the window sat, how to read on
// without re-running, and the one narrowing move that is usually better than
// reading on.
func spillNote(tool, id string, offset, end, total int, text string) string {
	shownFrom := strings.Count(text[:offset], "\n") + 1
	shownTo := strings.Count(text[:end], "\n") + 1
	lines := strings.Count(text, "\n") + 1
	return fmt.Sprintf("\n... [TRUNCATED: showing chars %d–%d of %d (lines %d–%d of %d). "+
		"Read on WITHOUT re-running: %s(output_id=%q, offset=%d). "+
		"Or narrow it: re-run with `| grep KEYWORD`.]",
		offset, end, total, shownFrom, shownTo, lines, tool, id, end)
}
