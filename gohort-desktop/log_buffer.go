// In-app log buffer + nfo interception. Captures every line nfo
// writes (Log / Debug / Err / Warn / Notice / Trace / Fatal) into a
// bounded ring so the Show Logs menu item can render the recent
// history in an overlay without the user needing to tail stderr in
// a terminal. Stderr output is preserved — we wrap each per-flag
// writer in an io.MultiWriter, not replace it.
//
// Why we have to do this: gohort-desktop runs as a packaged .app
// when installed, so there's no terminal attached. Operators who
// don't launch from a shell (the common case post-install) can't
// see why the WS bridge isn't connecting / what the tool invocation
// failed with. The in-app viewer is the only practical channel.

package main

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
	"github.com/cmcoffee/snugforge/nfo"
)

const (
	// logBufferCap — max lines retained. 2000 is enough for ~30
	// minutes of normal use (bridge connect, a handful of tool
	// invocations, the reconnect retries that fire on idle), and
	// keeps memory bounded at a few hundred KB even with long
	// stack traces.
	logBufferCap = 2000
)

// LogLine is one entry. Stamped at capture time so the viewer can
// render with a relative or absolute timestamp without re-parsing
// whatever nfo emitted.
type LogLine struct {
	When  time.Time `json:"when"`
	Level string    `json:"level"`
	Text  string    `json:"text"`
}

// logBuffer is the ring. Writes happen on the originating goroutine
// (nfo calls write through to our io.Writer, no buffering); reads
// happen on the Wails event loop when the modal polls. The
// per-line mutex keeps the slice append/trim atomic relative to
// reads.
type logBuffer struct {
	mu    sync.Mutex
	lines []LogLine
}

// log_buffer is global because nfo.SetOutput takes a single writer
// per flag — installing once at startup is the cleanest model, and
// we want every later GetLogs call to see the same buffer.
var log_buffer = &logBuffer{}

// installLogCapture wraps every standard nfo output flag with a
// MultiWriter(original, levelWriter) so log lines hit BOTH stderr
// (where the OS / dev console can grab them) AND our ring buffer.
// Idempotent — calling more than once just re-wraps which is fine.
//
// Trace and Debug are NOT included by default since nfo doesn't
// route those flags through textout unless explicitly enabled, and
// surfacing every trace would flood the ring buffer.
func installLogCapture() {
	wrap := func(flag uint32, level string) {
		existing := nfo.GetOutput(flag)
		wrapped := io.MultiWriter(existing, &levelWriter{level: level})
		nfo.SetOutput(flag, wrapped)
	}
	wrap(nfo.INFO, "info")
	wrap(nfo.ERROR, "error")
	wrap(nfo.WARN, "warn")
	wrap(nfo.NOTICE, "notice")
	wrap(nfo.FATAL, "fatal")
}

// levelWriter is the io.Writer end of the tee. nfo writes one full
// log frame per call (level prefix + message + newline), so we
// split on '\n' to capture each line as its own entry — multi-line
// messages (stack traces from panics) become multiple LogLines, which
// is what the viewer wants.
type levelWriter struct {
	level string
}

func (w *levelWriter) Write(p []byte) (int, error) {
	for _, raw := range bytes.Split(p, []byte{'\n'}) {
		line := strings.TrimRight(string(raw), "\r")
		if line == "" {
			continue
		}
		log_buffer.append(LogLine{
			When:  time.Now(),
			Level: w.level,
			Text:  line,
		})
	}
	return len(p), nil
}

func (b *logBuffer) append(ln LogLine) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, ln)
	if len(b.lines) > logBufferCap {
		// Drop oldest. Cheap reslice; underlying array grows once
		// then stays capped.
		b.lines = b.lines[len(b.lines)-logBufferCap:]
	}
}

// Lines returns a snapshot of the buffer. Safe to call from any
// goroutine; the returned slice is owned by the caller.
func (b *logBuffer) Lines() []LogLine {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]LogLine, len(b.lines))
	copy(out, b.lines)
	return out
}

// LinesSince returns lines logged AFTER the given timestamp. The
// viewer uses this for cheap polling refreshes — only fresh lines
// cross the Wails bridge, not the full buffer on every tick.
func (b *logBuffer) LinesSince(t time.Time) []LogLine {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, ln := range b.lines {
		if ln.When.After(t) {
			out := make([]LogLine, len(b.lines)-i)
			copy(out, b.lines[i:])
			return out
		}
	}
	return nil
}

// boot-time emit so the buffer has SOMETHING when the user opens
// the viewer immediately after launch (otherwise they see "no logs"
// and think the viewer is broken).
func init() {
	log_buffer.append(LogLine{
		When:  time.Now(),
		Level: "info",
		Text:  "[gohort-desktop] log buffer initialized",
	})
	// core import retained because main.go consumes core.Log; lint
	// would otherwise flag this file's import as unused.
	_ = core.Log
}
