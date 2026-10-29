// Reading one large file on several cores.
//
// Files were the unit of parallel reading, so a folder holding one big log
// (the usual shape of a support dump) still pinned one core. A plain file is
// now read in byte ranges: a chunk starts at the first line that begins inside
// its range and keeps every line that begins there, so a line straddling the
// boundary is read once, whole, by the chunk it starts in. A .gz can only be
// read from its start and stays one chunk; so does anything no larger than a
// chunk, which is the one path small files take too.
//
// What a chunk cannot know on its own the merge supplies, in file order
// (fileAcc.join): its line numbers (it counts from 1; the merge adds the lines
// before it), the context of a hit near its edges (it hands back its first and
// last lines), and, in a windowed search, the time of the lines above its
// first timestamp, which take the time of the line above them in the chunk
// before. Those are held apart until the merge knows that time, then kept or
// dropped together.
package filestore

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/cmcoffee/gohort/core/bundle"
)

// chunkBytes is the size of one chunk of a plain file. A variable so a test
// can put chunk boundaries everywhere.
var chunkBytes int64 = 8 << 20

// folderChunk is one byte range of one file: [from, to) of the file at index
// file, and whether it is that file's first and last chunk. The last reads on
// to the end of the file, whatever its size was when listed.
type folderChunk struct {
	file        int
	from, to    int64
	first, last bool
}

// folderChunks splits files into chunks, in order.
func folderChunks(files []LogFile) []folderChunk {
	size := chunkBytes
	if size < 1 {
		size = 1
	}
	var out []folderChunk
	for fi, lf := range files {
		if lf.Gzipped || lf.Size <= size {
			out = append(out, folderChunk{file: fi, to: lf.Size, first: true, last: true})
			continue
		}
		for from := int64(0); from < lf.Size; from += size {
			to := from + size
			out = append(out, folderChunk{file: fi, from: from, to: to, first: from == 0, last: to >= lf.Size})
		}
	}
	return out
}

// chunkScan is what reading one chunk needs from its search. Read-only, so
// chunks of one file and of others can share it at once.
type chunkScan struct {
	filter   bundle.Filter
	ctxLines int
	lim      fileLimits
	deadline time.Time
}

// owedAfter is a shown match still collecting its trailing context.
type owedAfter struct{ idx, left int }

// heldLines are a windowed chunk's matches above its first timestamp: they
// take the time of the line above them, in the chunk before, so whether they
// are in the window is the merge's to decide. The first shown entries of the
// chunk's shown list are theirs.
type heldLines struct {
	shown, total, excluded, hist int
	groups                       *bundle.Grouper
}

// chunkResult is one chunk's part of a file's search, before the merge joins
// the file's chunks and decides what the search keeps.
type chunkResult struct {
	fileResult
	held        heldLines
	lines       int         // lines in the chunk
	head, tail  []string    // its first and last ctxLines lines, as context shows them
	owed        []owedAfter // shown matches still owed trailing context at its end
	lastTime    time.Time   // its last timestamp, in a windowed search; zero for none
	readFailed  bool        // a read failed after readErrLine lines (readErr says how)
	readErrLine int
}

// scan reads one chunk of one file.
func (s chunkScan) scan(ctx context.Context, path, rel string, win window, c folderChunk) chunkResult {
	var r chunkResult
	if s.lim.group {
		r.groups = bundle.NewGrouper()
	}
	if s.lim.overTime {
		r.hist = bundle.NewHistogram()
	}
	// Above this chunk's first timestamp the window's clock is the chunk
	// before's, unknown here; the first chunk has no before, and its undated
	// opening lines are outside any window, as they always were.
	holding := win.set() && c.from > 0
	if holding && s.lim.group {
		r.held.groups = bundle.NewGrouper()
	}
	var rd io.Reader
	var start int64
	if c.from == 0 {
		rc, err := open(path)
		if err != nil {
			r.readErr = err.Error()
			return r
		}
		defer rc.Close()
		rd = rc
	} else {
		f, err := os.Open(path)
		if err != nil {
			r.readErr = err.Error()
			return r
		}
		defer f.Close()
		if _, err := f.Seek(c.from-1, io.SeekStart); err != nil {
			r.readErr = err.Error()
			return r
		}
		rd, start = f, c.from-1
	}
	lr := newLineReader(rd, maxLineBytes)
	if c.from > 0 {
		// The line holding byte from-1 began in the chunk before, and is
		// read there. Skip to its end: when that byte is a line's own
		// newline this takes just it, and the chunk starts on the line that
		// begins at from.
		if _, _, err := lr.next(); err != nil {
			return r
		}
	}

	var ring []string
	var waiting []owedAfter
	var lineTime time.Time
	lineNo := 0
	for {
		// A line belongs to the chunk it begins in.
		if !c.last && start+lr.read >= c.to {
			break
		}
		raw, cut, err := lr.next()
		if err != nil {
			if err != io.EOF {
				r.readFailed, r.readErr, r.readErrLine = true, err.Error(), lineNo
			}
			break
		}
		lineNo++
		r.read = lr.read
		if cut {
			r.longLines++
		}
		// One clock read per 4096 lines rather than per line: the check
		// has to be cheap enough that it is never the reason to skip it.
		// A .gz that decompresses to something enormous is caught here
		// and nowhere else — its on-disk size passed the file cap.
		if lineNo%4096 == 0 && (time.Now().After(s.deadline) || ctx.Err() != nil) {
			r.stopped = true
			r.lines = lineNo
			return r
		}
		// A line is matched and dated WHOLE, and cut to maxLineRunes only for
		// what is shown. Matching the cut line was a silent miss: a hit past
		// the cap (deep in a JSON line, at the end of a long stack frame)
		// was not found, and the search reported it absent.
		//
		// The strings are made only when something needs them: the context
		// ring and the window's clock read every line, a plain search reads
		// none but its hits. Making them for every line was an allocation per
		// line of the file, for lines that almost never match.
		var full, line string
		if s.ctxLines > 0 || win.set() {
			full = string(raw)
			line = truncateRunes(full, maxLineRunes)
		}
		if s.ctxLines > 0 && len(r.head) < s.ctxLines {
			r.head = append(r.head, line)
		}

		for i := 0; i < len(waiting); {
			w := waiting[i]
			r.shown[w.idx].After = append(r.shown[w.idx].After, line)
			w.left--
			if w.left == 0 {
				waiting = append(waiting[:i], waiting[i+1:]...)
				continue
			}
			waiting[i] = w
			i++
		}

		inWindow := true
		if win.set() {
			if ts, ok := bundle.ParseTime(win.format, win.year, full); ok {
				lineTime, r.lastTime = ts, ts
				r.timed = true
				holding = false
			}
			// A held line is in the window if the time above it is; the
			// merge decides, so it is let through here and counted apart.
			inWindow = holding || win.holds(lineTime)
		}

		keep := inWindow && s.filter.Match.Match(raw)
		// Exclusion is tested last, on lines already in, so the count of
		// what it removed is only what the search would otherwise have
		// counted. The line still feeds the context ring below: it is
		// dropped as a hit, not from the file.
		if keep && s.filter.Excludes(raw) {
			if holding {
				r.held.excluded++
			} else {
				r.excluded++
			}
			keep = false
		}
		if keep {
			if holding {
				r.held.total++
			} else {
				r.total++
			}
			switch {
			case r.groups != nil || r.hist != nil:
				// Dated by the window's clock when there is one (an undated
				// line takes the time above it), else by the line itself
				// when the file's format carries times at all. A held line's
				// time is the merge's to supply.
				when := lineTime
				if !win.set() && win.format != "" && win.format != bundle.FormatText {
					when, _ = bundle.ParseTime(win.format, win.year, string(raw))
				}
				switch {
				case holding && r.held.groups != nil:
					r.held.groups.AddBytes(rel, lineNo, raw, time.Time{})
				case holding:
					r.held.hist++
				case r.groups != nil:
					r.groups.AddBytes(rel, lineNo, raw, when)
				default:
					r.hist.Add(when)
				}
			// Held and settled matches each keep up to the offer: whether
			// the held ones count is not known yet, and the settled ones
			// must not have given up room to them.
			case (holding && r.held.shown < s.lim.offer) || (!holding && len(r.shown)-r.held.shown < s.lim.offer):
				if full == "" {
					line = truncateRunes(string(raw), maxLineRunes)
				}
				m := Match{File: rel, Line: lineNo, Text: line}
				if s.ctxLines > 0 && len(ring) > 0 {
					m.Before = append(m.Before, ring...)
				}
				r.shown = append(r.shown, m)
				if holding {
					r.held.shown++
				}
				if s.ctxLines > 0 {
					waiting = append(waiting, owedAfter{idx: len(r.shown) - 1, left: s.ctxLines})
				}
			}
		}

		if s.ctxLines > 0 {
			ring = append(ring, line)
			if len(ring) > s.ctxLines {
				ring = ring[1:]
			}
		}
	}
	r.lines = lineNo
	r.read = lr.read
	r.tail = append([]string(nil), ring...)
	r.owed = waiting
	return r
}

// fileAcc is one file as its chunks are joined: the file's result so far, and
// what the next chunk needs from the ones before it.
type fileAcc struct {
	fileResult
	lines    int         // lines joined so far: the next chunk's numbering starts after them
	lastTime time.Time   // the last timestamp joined so far, for the next chunk's held lines
	tail     []string    // the last ctxLines lines joined so far, for leading context
	owed     []owedAfter // kept matches still owed trailing context
	done     bool        // a read failed: the file ends there, as a single pass would end
}

// join adds the next chunk of the file, in order. Groups and counts over time
// go straight into res, which merges them in the same order.
func (a *fileAcc) join(c chunkResult, ctxLines, offer int, win window, res *SearchResult) {
	// Trailing context owed by matches in earlier chunks, from this one's
	// first lines; a chunk too short to settle it passes the rest on.
	for _, line := range c.head {
		if len(a.owed) == 0 {
			break
		}
		kept := a.owed[:0]
		for _, o := range a.owed {
			a.shown[o.idx].After = append(a.shown[o.idx].After, line)
			if o.left--; o.left > 0 {
				kept = append(kept, o)
			}
		}
		a.owed = kept
	}
	// The held lines are in the window if the time above them is.
	accept := c.held.shown > 0 || c.held.total > 0 || c.held.excluded > 0 || c.held.hist > 0 || c.held.groups != nil
	accept = accept && !a.lastTime.IsZero() && win.holds(a.lastTime)
	from := c.held.shown
	if accept {
		from = 0
	}
	for j := from; j < len(c.shown); j++ {
		m := c.shown[j]
		chunkLine := m.Line
		m.Line += a.lines
		// Leading context that began before this chunk, from the lines
		// joined so far.
		if ctxLines > 0 && chunkLine-1 < ctxLines {
			missing := ctxLines - (chunkLine - 1)
			if missing > len(a.tail) {
				missing = len(a.tail)
			}
			if missing > 0 {
				m.Before = append(append([]string(nil), a.tail[len(a.tail)-missing:]...), m.Before...)
			}
		}
		if len(a.shown) >= offer {
			continue
		}
		a.shown = append(a.shown, m)
		for _, o := range c.owed {
			if o.idx == j {
				a.owed = append(a.owed, owedAfter{idx: len(a.shown) - 1, left: o.left})
			}
		}
	}
	a.total += c.total
	a.excluded += c.excluded
	if accept {
		a.total += c.held.total
		a.excluded += c.held.excluded
		if c.held.groups != nil {
			c.held.groups.Stamp(a.lastTime)
			c.held.groups.Offset(a.lines)
			res.Groups.Merge(c.held.groups)
		}
		if res.Histogram != nil && c.held.hist > 0 {
			res.Histogram.AddN(a.lastTime, c.held.hist)
		}
	}
	c.groups.Offset(a.lines) // its examples were numbered from the chunk's first line
	res.Groups.Merge(c.groups)
	res.Histogram.Merge(c.hist)
	if !c.lastTime.IsZero() {
		a.lastTime = c.lastTime
	}
	if ctxLines > 0 {
		a.tail = append(a.tail, c.tail...)
		if len(a.tail) > ctxLines {
			a.tail = append([]string(nil), a.tail[len(a.tail)-ctxLines:]...)
		}
	}
	switch {
	case c.readFailed:
		a.readErr = fmt.Sprintf("could not be read past line %d: %v", a.lines+c.readErrLine, c.readErr)
		a.done = true
	case c.readErr != "":
		a.readErr = c.readErr
		a.done = true
	}
	a.lines += c.lines
	a.timed = a.timed || c.timed
	a.read += c.read
	a.longLines += c.longLines
	a.stopped = a.stopped || c.stopped
}
