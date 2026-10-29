// Reading a folder of files without ever handing a model a whole one.
//
// This is the half of the app with no framework in it: given a root and
// a query, produce bounded results. Kept separate because every safety
// property that matters here is testable without a server, a session, or
// an LLM — path containment, match caps, line caps, window caps.
//
// The shape of the problem is not the shape of a document corpus, which
// is why this is not a Collection. What lands here wants exact and
// regular-expression matching, a time window, and a few lines either
// side of a hit. Chunking a log, a config tree or a CSV export
// semantically destroys the line structure that makes it searchable, and
// embedding a gigabyte of stack traces buys nothing.

package filestore

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cmcoffee/gohort/core/bundle"
)

// Caps. These are deliberately not configurable.
//
// Every one of them exists because the failure they prevent is a turn
// that dumps a file into the context window and takes the conversation
// with it. An operator who could raise them would raise them exactly
// once, in the moment they were most sure they needed the whole file.
const (
	maxMatches     = 1000    // matches one search keeps; the reply's first page shows what fits, read_output the rest
	maxLineRunes   = 400     // a single line is truncated past this
	maxContext     = 8       // lines either side of a hit
	maxWindowLines = 400     // lines one read may return
	maxFilesWalked = 5000    // files considered in one search
	maxFileBytes   = 1 << 30 // skip anything larger; a 1GB log is not searched line by line
	maxLineBytes   = 4 << 20 // a line is searched in its first this many bytes; the rest is skipped, and the reply says so
)

// Bounds on the WORK a search may do, as opposed to the answer it may
// return. The caps above stop a big reply; these stop a search that
// never comes back.
//
// A tool handler gets no context to cancel with, so nothing upstream can
// stop this: not the agent loop, not a cancelled turn, not the person
// watching. The turn simply stops producing output, which is
// indistinguishable from a hung model. It looked exactly like that the
// first time a search ran against a real folder.
//
// The reachable worst case is not exotic: maxFilesWalked files of up to
// maxFileBytes each is five thousand gigabyte scans, and a single
// character device (or a FIFO waiting for a writer) is unbounded on its
// own.
// These are RUNAWAY guards, not a truncation policy. A big bundle takes
// the time it takes, and cutting a legitimate three-minute scan at
// twenty seconds trades a slow answer for a wrong one — the model gets a
// partial result and reports an absence nobody established.
//
// So they are set where "still working" stops being plausible and
// "something is stuck" starts. What makes waiting bearable is the
// heartbeat on the activity pane (wrapToolsForActivity), not a short
// clock here.
const (
	searchDeadline = 15 * time.Minute // wall clock for ONE search
	maxScanBytes   = 64 << 30         // total bytes read across one search
)

// LogFile is one file under a root.
type LogFile struct {
	Rel      string    `json:"rel"` // path relative to the root, the handle everything else takes
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Gzipped  bool      `json:"gzipped,omitempty"`
}

// Match is one hit, with its surroundings.
type Match struct {
	File   string   `json:"file"`
	Line   int      `json:"line"`
	Text   string   `json:"text"`
	Before []string `json:"before,omitempty"`
	After  []string `json:"after,omitempty"`
	// Record says how Before and After were drawn when the search asked for
	// records; nil otherwise.
	Record *bundle.RecordView `json:"record,omitempty"`
}

// SearchOpts is what a caller may vary.
type SearchOpts struct {
	Pattern string // regular expression; a plain string is a valid one
	// Patterns are further patterns looked for in the same pass (core/bundle
	// lanes.go): each answered as if searched alone, in SearchResult.Lanes.
	// Pattern, when set, is the first of them.
	Patterns   []string
	Exclude    string // optional regular expression: a line Pattern matched is dropped, not shown or counted, if this matches it too; follows IgnoreCase
	Glob       string // optional filename filter, e.g. "*.log"
	IgnoreCase bool
	// Since and Until are a time window applied to LINES, by the timestamp
	// each line carries. A line with none (the body of a stack trace, a
	// wrapped message) inherits the last timestamp above it in its file, so a
	// window never cuts an exception off from the line that dated it; lines
	// before a file's first timestamp are outside any window. A file whose
	// modification time is before Since is skipped unread.
	Since, Until time.Time
	Context      int // lines either side; clamped to maxContext
	// Max is how many matches the search keeps: maxMatches when unset, and
	// never more. The reply shows the first page of them, and read_output
	// reads on through the rest (core/bundle budget.go).
	Max int
	// PerFile caps how many of ONE file's matches may be kept, so a noisy
	// file cannot take the whole budget from the others. Zero means no
	// per-file cap. Lifted when only one file is searched: the cap exists to
	// share the budget, and there is nobody to share it with.
	PerFile int
	// CountOnly returns per-file match totals and no lines at all, over every
	// file, with no Max. It answers how OFTEN, which a capped list cannot.
	CountOnly bool
	// Group files every match under its shape (core/bundle group.go) and
	// returns the shapes instead of lines, over every file, with no Max,
	// PerFile or Context. It answers WHAT is in here, which a search
	// for a pattern the asker already knows cannot.
	Group bool
	// OverTime counts every match by its timestamp (core/bundle overtime.go)
	// and returns rows of counts instead of lines, on the same terms as
	// Group. Bucket is one row's size; zero chooses one from the span.
	OverTime bool
	Bucket   time.Duration
	// Records gives each kept match its whole log record instead of Context
	// lines (core/bundle records.go): from the dated line it belongs to down
	// to the next. Context still applies in a file with no timestamps, where
	// records cannot be told apart.
	Records bool
	// Deadline bounds the whole search. Zero uses searchDeadline. A
	// search that runs out of time returns what it found and says so
	// rather than failing: partial evidence with a warning beats no
	// evidence, and beats a turn that never comes back.
	Deadline time.Duration
}

// FileTally is one searched file's count: every match in it, and how many of
// those were kept (the per-file share is the difference).
type FileTally = bundle.FileTally

// SearchResult is one search's answer plus what it did NOT do.
//
// Every truncation is reported, and they mean different things. The embedded
// Budget says how many matches were kept of how many counted, and WHERE a cut
// fell: CutInFiles are the files that had more than PerFile, Unsearched how
// many files the limit left unread; Cut() is whether any of that happened.
// Stopped is "we gave up before looking everywhere". An investigator who reads
// a partial result as a complete one concludes something is absent when
// nobody looked.
type SearchResult struct {
	// Budget is embedded so its counts read as the result's own: Total,
	// Kept, Tallies (count-only: busiest first), CutInFiles, Unsearched, and
	// the limits that applied (Max, PerFile; PerFile 0 when no share did).
	bundle.Budget
	Matches    []Match
	Stopped    string // empty when the search finished; otherwise why it did not
	Scanned    int    // files actually read
	Considered int    // files the search could have read
	Bytes      int64  // bytes read across those files
	Elapsed    time.Duration
	Untimed    int      // files left out of a time window because they carry no timestamps
	Excluded   int      // lines Pattern matched and Exclude took back out
	LongLines  int      // lines over maxLineBytes, searched only in their first maxLineBytes
	Unreadable []string // "file: why" for each file a read failed in, searched only up to there
	// Groups holds the matches by shape when Group was asked; nil otherwise.
	Groups *bundle.Grouper
	// Histogram holds the matches by time when OverTime was asked; nil
	// otherwise.
	Histogram *bundle.Histogram
	// Lanes is each pattern's answer when the search looked for several, in
	// the order given (Lane gives one as a search of its own). Nil for one
	// pattern, whose answer is the fields above; for several, those hold
	// only what the patterns share (files read, gaps, stops).
	Lanes []bundle.Lane[Match]
}

// Lane is lane i of a search of several patterns as the result a search for
// that pattern alone would have returned.
func (r SearchResult) Lane(i int) SearchResult {
	ln := r.Lanes[i]
	out := r
	out.Lanes = nil
	out.Budget, out.Matches, out.Excluded = ln.Budget, ln.Hits, ln.Excluded
	out.Groups, out.Histogram = ln.Groups, ln.Histogram
	return out
}

// resolveUnder joins rel to root and proves the result is still inside
// it.
//
// This is the only thing standing between a model-supplied filename and
// the rest of the disk, so it resolves symlinks rather than trusting the
// textual path: "../../etc/shadow" is the obvious attack and the boring
// one, but a symlink inside the store pointing out of it is the same
// hole with better manners.
func resolveUnder(root, rel string) (string, error) {
	if strings.TrimSpace(rel) == "" {
		return "", fmt.Errorf("no file named")
	}
	rootAbs, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("that folder is unreadable: %w", err)
	}
	full := filepath.Join(rootAbs, filepath.Clean("/"+rel))
	if inVCSDir(rootAbs, full) {
		return "", errVCSDir
	}
	// EvalSymlinks fails on a path that does not exist, which is a
	// legitimate "no such file" rather than a containment failure.
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no such file in this store: %s", rel)
		}
		return "", err
	}
	// Checked again resolved: a symlink in the store can name a .git
	// folder by another name.
	if inVCSDir(rootAbs, resolved) {
		return "", errVCSDir
	}
	// STRICTLY below, never equal. ".." and "../" clean to the root
	// itself, which an equality-tolerant check waves through: the caller
	// then "reads" a directory (yielding nothing, which reads as an empty
	// file) or resolves the whole store as a subfolder. Neither names a
	// thing that exists, so neither is a valid answer.
	if !strings.HasPrefix(resolved, rootAbs+string(os.PathSeparator)) {
		return "", fmt.Errorf("that path resolves outside the store")
	}
	return resolved, nil
}

// errVCSDir refuses a path inside version-control metadata.
var errVCSDir = fmt.Errorf("that path is inside a version-control folder (.git, .hg or .svn), which can hold credentials and is never read here")

// inVCSDir reports whether path, below root, passes through a version-control
// folder (core/bundle vcs.go).
func inVCSDir(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if bundle.VCSDir(part) {
			return true
		}
	}
	return false
}

// List walks a root and reports what is there, newest first.
//
// Newest first because an investigation almost always starts at the most
// recent file, and because it makes the truncation (when a folder has
// more files than anyone wants listed) drop the least useful end.
func List(root string, glob string) ([]LogFile, error) {
	rootAbs, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("that folder is unreadable: %w", err)
	}
	var out []LogFile
	walked := 0
	err = filepath.Walk(rootAbs, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // an unreadable subtree is not a reason to fail the whole listing
		}
		// Version-control metadata is never listed, so nothing that lists
		// (a search, a span, a listing) reads it: a checkout's .git/config
		// can hold the token it was cloned with.
		if info.IsDir() && path != rootAbs && bundle.VCSDir(info.Name()) {
			return filepath.SkipDir
		}
		if info.IsDir() {
			return nil
		}
		// REGULAR FILES ONLY. A FIFO blocks in os.Open until someone
		// opens the other end — forever, in practice — and a character
		// device like /dev/zero reads without ever reaching EOF. Either
		// one hangs a search that has no way to be cancelled, and both
		// turn up in a captured filesystem tree without anybody putting
		// them there deliberately.
		if !info.Mode().IsRegular() {
			return nil
		}
		walked++
		if walked > maxFilesWalked {
			return io.EOF // sentinel: stop walking, not an error
		}
		rel, rerr := filepath.Rel(rootAbs, path)
		if rerr != nil {
			return nil
		}
		if glob != "" {
			if ok, _ := filepath.Match(glob, filepath.Base(path)); !ok {
				return nil
			}
		}
		out = append(out, LogFile{
			Rel: rel, Size: info.Size(), Modified: info.ModTime(),
			Gzipped: strings.HasSuffix(path, ".gz"),
		})
		return nil
	})
	if err != nil && err != io.EOF {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

// open returns a reader for a log file, transparently decompressing a
// rotated .gz. Rotation is the normal state of a log folder, so a tool
// that cannot read yesterday's file is a tool that answers "nothing
// found" for every question about yesterday.
func open(path string) (io.ReadCloser, error) {
	// Checked BEFORE opening, because opening is itself the blocking act
	// on a FIFO — os.Open waits for a writer. List already filters these
	// out; this is the direct-read path (read_<store>), where the name
	// comes from the model rather than from a walk.
	if fi, err := os.Lstat(path); err == nil && !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file (%s): nothing here reads one",
			filepath.Base(path), fi.Mode().Type())
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(path, ".gz") {
		return f, nil
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("%s looks gzipped but will not decompress: %w", filepath.Base(path), err)
	}
	return gzReadCloser{gz: gz, under: f}, nil
}

// lineReader reads a file a line at a time, as bufio.Scanner does, except
// that neither a long line nor a failed read can pass for the end of the file.
//
// Scanner gives up for good at the first line longer than its buffer, and
// every reader here took that for the end of the file: a search reported
// nothing after one 5MB line of minified JSON, a window read reported the file
// shorter than it is. Here a long line is cut at the cap, said to be cut, and
// the next line reads normally. A read that fails (a truncated .gz, an I/O
// error) is returned as itself once the lines before it are, so a caller can
// say where it stopped rather than report what it did not read as absent.
type lineReader struct {
	rd   *bufio.Reader
	max  int
	buf  []byte
	err  error // the read error after the line being returned, held for the next call
	read int64 // bytes consumed, line endings included
}

func newLineReader(r io.Reader, max int) *lineReader {
	return &lineReader{rd: bufio.NewReaderSize(r, 64<<10), max: max}
}

// next returns the next line without its line ending, at most max bytes of
// it, and whether the rest of it was cut. The line is only valid until the
// next call. io.EOF once there are no more lines; any other error is the read
// failing at this point in the file.
//
// A line that fits the read buffer, which is nearly every line, is returned
// straight out of it and costs no copy.
func (l *lineReader) next() ([]byte, bool, error) {
	if l.err != nil {
		return nil, false, l.err
	}
	frag, err := l.rd.ReadSlice('\n')
	l.read += int64(len(frag))
	if err != bufio.ErrBufferFull {
		if err != nil {
			if len(frag) == 0 {
				return nil, false, err
			}
			l.err = err
		}
		return l.finish(frag, false)
	}
	// Longer than the buffer: gathered across reads, keeping no more than
	// the cap plus room for a CRLF, so an enormous line is never held whole.
	l.buf = l.buf[:0]
	cut := false
	for {
		if room := l.max + 2 - len(l.buf); len(frag) > room {
			l.buf = append(l.buf, frag[:max(room, 0)]...)
			cut = true
		} else {
			l.buf = append(l.buf, frag...)
		}
		if err != bufio.ErrBufferFull {
			break
		}
		frag, err = l.rd.ReadSlice('\n')
		l.read += int64(len(frag))
	}
	if err != nil {
		l.err = err
	}
	return l.finish(l.buf, cut)
}

// finish drops the line ending and applies the cap.
func (l *lineReader) finish(line []byte, cut bool) ([]byte, bool, error) {
	line = bytes.TrimSuffix(line, []byte{'\n'})
	line = bytes.TrimSuffix(line, []byte{'\r'})
	if len(line) > l.max {
		line, cut = line[:l.max], true
	}
	return line, cut, nil
}

type gzReadCloser struct {
	gz    *gzip.Reader
	under *os.File
}

func (g gzReadCloser) Read(p []byte) (int, error) { return g.gz.Read(p) }
func (g gzReadCloser) Close() error {
	g.gz.Close()
	return g.under.Close()
}

// Search runs a pattern across a root and returns bounded matches.
//
// Returns the matches plus whether and where a cap was hit, because "60
// matches" and "the first 60 of many" are different answers and an
// investigator acting on the first as though it were the second draws a
// conclusion from a truncated set.
//
// Search takes a context because its own deadline is a ceiling, not an answer
// to a person. The default budget is fifteen minutes and the agent loop only
// tests for cancellation between rounds, so before this a Stop pressed during a
// search over a large store did nothing at all until the search finished on its
// own terms. Nil ctx reads as uncancellable rather than panicking.
func Search(ctx context.Context, root string, opts SearchOpts) (SearchResult, error) {
	var res SearchResult
	if ctx == nil {
		ctx = context.Background()
	}
	patterns, err := bundle.Patterns(opts.Pattern, opts.Patterns)
	if err != nil {
		return res, err
	}
	prefix := ""
	if opts.IgnoreCase {
		prefix = "(?i)"
	}
	filter, err := bundle.NewFilter(patterns, opts.Exclude, prefix)
	if err != nil {
		return res, err
	}
	if !opts.Since.IsZero() && !opts.Until.IsZero() && opts.Until.Before(opts.Since) {
		return res, fmt.Errorf("the time window ends before it starts")
	}
	if modes := countTrue(opts.CountOnly, opts.Group, opts.OverTime); modes > 1 {
		return res, fmt.Errorf("count, group and over_time are different answers: count says how often a pattern occurs per file, group what kinds of lines there are, over_time when the matching ones happened. Ask for one")
	}
	// A whole-folder answer reads every file and keeps no lines.
	whole := opts.CountOnly || opts.Group || opts.OverTime
	ctxLines := clamp(opts.Context, 0, maxContext)
	if whole {
		ctxLines = 0
	}
	limit := opts.Max
	if limit <= 0 || limit > maxMatches {
		limit = maxMatches
	}
	if whole {
		limit = 0 // counts every match and keeps none
	}

	files, err := List(root, opts.Glob)
	if err != nil {
		return res, err
	}
	rootAbs, _ := filepath.EvalSymlinks(root)
	res.Considered = len(files)
	perFile := opts.PerFile
	if perFile <= 0 || len(files) <= 1 || whole {
		perFile = 0
	}
	// One lane per pattern, each with the limits a search for it alone
	// would have (core/bundle lanes.go).
	lanes := make([]bundle.Lane[Match], len(patterns))
	for l := range lanes {
		ln := &lanes[l]
		ln.Pattern = patterns[l]
		ln.Max, ln.PerFile = limit, perFile
		if opts.Group {
			ln.Groups = bundle.NewGrouper()
		}
		if opts.OverTime {
			ln.Histogram = bundle.NewHistogram()
		}
	}

	allowed := opts.Deadline
	if allowed <= 0 {
		allowed = searchDeadline
	}
	started := time.Now()
	deadline := started.Add(allowed)
	var scanned int64
	win := window{since: opts.Since, until: opts.Until}
	lim := fileLimits{offer: lanes[0].Offer(), group: opts.Group, overTime: opts.OverTime}

	// Several chunks at once, of several files or of one large one, merged in
	// order (core/bundle parallel.go, search_chunks.go): the scan does what
	// one chunk needs and nothing that depends on what came before it; the
	// merge joins a file's chunks, then applies the limit, the byte cap and
	// the stops, in the order a single pass would.
	cs := chunkScan{filter: filter, ctxLines: ctxLines, lim: lim, deadline: deadline}
	chunks := folderChunks(files)
	var acc fileAcc
	skipping := false
	finish := func(lf LogFile) {
		for l := range lanes {
			kept := lanes[l].File(lf.Rel, acc.lanes[l].total, acc.offered[l])
			lanes[l].Hits = append(lanes[l].Hits, bundle.LaneItems(acc.shown, acc.masks, l, kept)...)
			lanes[l].Excluded += acc.lanes[l].excluded
		}
		res.Scanned++
		scanned += acc.read
		res.LongLines += acc.longLines
		if acc.readErr != "" {
			res.Unreadable = append(res.Unreadable, lf.Rel+": "+acc.readErr)
		}
		if win.set() && !acc.timed {
			res.Untimed++
		}
	}
	bundle.EachInOrder(ctx, len(chunks),
		func(ctx context.Context, i int) folderScan {
			c := chunks[i]
			lf := files[c.file]
			if !opts.Since.IsZero() && lf.Modified.Before(opts.Since) {
				return folderScan{skip: true} // written to for the last time before the window opens
			}
			if lf.Size > maxFileBytes {
				return folderScan{skip: true}
			}
			// Checked before a chunk as well as inside one: a folder of ten
			// thousand small files exhausts the clock without any single
			// file being slow.
			if ctx.Err() != nil || time.Now().After(deadline) {
				return folderScan{late: true}
			}
			path := filepath.Join(rootAbs, lf.Rel)
			fw := win
			if win.set() || opts.Group || opts.OverTime {
				fw.format, fw.year = detectFileFormat(path, lf.Modified)
			}
			return folderScan{r: cs.scan(ctx, path, lf.Rel, fw, c)}
		},
		func(i int, s folderScan) bool {
			c := chunks[i]
			lf := files[c.file]
			if c.first {
				if bundle.AllFull(lanes) {
					for l := range lanes {
						lanes[l].Unsearched = len(files) - c.file
					}
					return false
				}
				if skipping = s.skip; skipping {
					return true
				}
				// Cancellation rides beside the clock: a folder of ten
				// thousand small files gives this plenty of chances to
				// notice, and one enormous file gives it none, which is why
				// the scan carries the check too.
				if ctx.Err() != nil {
					return false
				}
				if s.late {
					res.Stopped = fmt.Sprintf("stopped after %s, having read %d of %d files", allowed, res.Scanned, len(files))
					return false
				}
				if scanned >= maxScanBytes {
					res.Stopped = fmt.Sprintf("stopped after reading %d MB, at %d of %d files", scanned>>20, res.Scanned, len(files))
					return false
				}
				acc = newFileAcc(len(lanes))
			} else if skipping {
				return true
			}
			if ctx.Err() != nil {
				return false
			}
			if !acc.done {
				if s.late {
					acc.stopped = true
				} else {
					acc.join(s.r, ctxLines, lim.offer, win, lanes)
				}
			}
			if acc.stopped {
				finish(lf)
				res.Stopped = fmt.Sprintf("stopped inside %s after %s, having read %d of %d files", lf.Rel, time.Since(started).Round(time.Second), res.Scanned, len(files))
				return false
			}
			if c.last {
				finish(lf)
			}
			return true
		})
	if opts.Records && !whole && ctx.Err() == nil {
		folderRecords(ctx, rootAbs, files, lanes, deadline)
	}
	if opts.CountOnly {
		for l := range lanes {
			t := lanes[l].Tallies
			sort.SliceStable(t, func(i, j int) bool { return t[i].Matches > t[j].Matches })
		}
	}
	res.Lanes = lanes
	if len(lanes) == 1 {
		res = res.Lane(0)
	}
	// A cancel inside the LAST file leaves the loop by its own condition, so
	// the error has to be asked for again on the way out or a stopped search
	// reports as a complete one that found little.
	if err := ctx.Err(); err != nil {
		return res, err
	}
	res.Bytes = scanned
	res.Elapsed = time.Since(started)
	return res, nil
}

// folderRecords gives every lane's kept matches their whole records (core/bundle
// records.go). Each file with a kept match is read again from its start up to
// its last match's record, several files at once; only the lines near a match
// are looked at, and the reading stops once the last record is closed. A file
// with no timestamps has no records, and its matches keep their context.
func folderRecords(ctx context.Context, rootAbs string, files []LogFile, lanes []bundle.Lane[Match], deadline time.Time) {
	targets, order := bundle.RecordTargets(lanes, func(m Match) string { return m.File }, func(m Match) int { return m.Line })
	byRel := map[string]LogFile{}
	for _, lf := range files {
		byRel[lf.Rel] = lf
	}
	// recs is nil for a file whose records could not be found: undated,
	// or not read again (a failed open, the clock).
	type fileRecords struct {
		recs    map[int]*bundle.Record
		undated bool
	}
	found := make([]fileRecords, len(order))
	bundle.EachInOrder(ctx, len(order),
		func(ctx context.Context, i int) fileRecords {
			rel := order[i]
			path := filepath.Join(rootAbs, rel)
			format, year := detectFileFormat(path, byRel[rel].Modified)
			if format == bundle.FormatText {
				return fileRecords{undated: true}
			}
			rc, err := open(path)
			if err != nil {
				return fileRecords{}
			}
			defer rc.Close()
			f := bundle.NewRecordFinder(targets[rel],
				func(s string) bool { _, ok := bundle.ParseTime(format, year, s); return ok },
				func(s string) string { return truncateRunes(s, maxLineRunes) })
			spans := bundle.RecordSpans(targets[rel])
			lr := newLineReader(rc, maxLineBytes)
			for n, si := 1, 0; si < len(spans) && !f.Done(); n++ {
				if n%4096 == 0 && (ctx.Err() != nil || time.Now().After(deadline)) {
					return fileRecords{}
				}
				raw, _, err := lr.next()
				if err != nil {
					break
				}
				for si < len(spans) && n > spans[si][1] {
					si++
				}
				if si < len(spans) && n >= spans[si][0] {
					f.Line(n, string(raw))
				}
			}
			return fileRecords{recs: f.Records()}
		},
		func(i int, r fileRecords) bool {
			found[i] = r
			return true
		})
	index := map[string]fileRecords{}
	for i, rel := range order {
		index[rel] = found[i]
	}
	for l := range lanes {
		lanes[l].Hits = bundle.FoldRecords(lanes[l].Hits,
			func(m Match) (*bundle.Record, bool) {
				fr := index[m.File]
				return fr.recs[m.Line], !fr.undated
			},
			func(m *Match, r *bundle.Record, v *bundle.RecordView) {
				m.Record = v
				if r == nil {
					return
				}
				i := m.Line - r.Start
				m.Before, m.Text, m.After = r.Lines[:i:i], r.Lines[i], r.Lines[i+1:]
			},
			func(m *Match) *bundle.RecordView { return m.Record },
			func(m Match) int { return m.Line })
	}
}

// window is a search's time window, with what one file needs to read it.
type window struct {
	since, until time.Time
	format       string // the file's detected log format
	year         int    // for a format that carries no year (syslog)
}

func (w window) set() bool { return !w.since.IsZero() || !w.until.IsZero() }

// holds reports whether a line dated t is inside the window. A zero t (no
// timestamp seen yet in the file) is outside any window.
func (w window) holds(t time.Time) bool {
	if t.IsZero() {
		return false
	}
	if !w.since.IsZero() && t.Before(w.since) {
		return false
	}
	return w.until.IsZero() || !t.After(w.until)
}

// detectFileFormat reads the head of a file to decide how its lines are
// dated, and supplies the year a syslog line leaves out from when the file
// was last written.
func detectFileFormat(path string, modified time.Time) (string, int) {
	rc, err := open(path)
	if err != nil {
		return bundle.FormatText, modified.Year()
	}
	defer rc.Close()
	lr := newLineReader(rc, maxLineBytes)
	var sample []string
	for len(sample) < 60 {
		line, _, err := lr.next()
		if err != nil {
			break
		}
		sample = append(sample, string(line))
	}
	return bundle.DetectFormat(sample), modified.Year()
}

// fileLimits is what one file's scan keeps: its first offer matches
// (Budget.Offer; the merge decides how many of those the search keeps), or,
// for a grouped or over-time search, every match filed into the file's own
// grouping or counts instead of kept as lines.
type fileLimits struct {
	offer           int
	group, overTime bool
}

// folderScan is one chunk's scan as the merge receives it: skipped before
// reading (its file out of the window by date, or too large), not started
// because the clock ran out, or read.
type folderScan struct {
	skip, late bool
	r          chunkResult
}

// fileResult is one file's part of a search, or one chunk's (search_chunks.go),
// before the merge decides how much of it the search keeps.
type fileResult struct {
	// shown are each lane's first lim.offer matches, in order: kept once
	// however many lanes they are offered to, masks[i] saying which, and
	// offered counting them per lane (core/bundle lanes.go).
	shown   []Match
	masks   []uint32
	offered []int
	lanes   []laneCount
	timed   bool // the file carried at least one timestamp
	stopped bool // the deadline or a cancel cut the file short
	read    int64
	// longLines counts lines over maxLineBytes, searched in their first
	// maxLineBytes only. readErr is why the file could not be read to its
	// end, with the line it stopped at; empty when it was.
	longLines int
	readErr   string
}

// laneCount is one pattern's part of a file's search.
type laneCount struct {
	total    int               // every match in the file
	excluded int               // matches the exclude pattern took back out, counted nowhere else
	groups   *bundle.Grouper   // the file's own grouping, when the search groups
	hist     *bundle.Histogram // its own counts, when the search counts over time
}

// newFileResult is an empty part of a search of n patterns.
func newFileResult(n int) fileResult {
	return fileResult{offered: make([]int, n), lanes: make([]laneCount, n)}
}

// Read returns a window of lines around a point in one file.
func Read(root, rel string, around, lines int) ([]string, int, error) {
	path, err := resolveUnder(root, rel)
	if err != nil {
		return nil, 0, err
	}
	if lines <= 0 || lines > maxWindowLines {
		lines = maxWindowLines
	}
	rc, err := open(path)
	if err != nil {
		return nil, 0, err
	}
	defer rc.Close()

	start := 1
	if around > 0 {
		start = around - lines/2
		if start < 1 {
			start = 1
		}
	}
	lr := newLineReader(rc, maxLineBytes)

	// A read that fails partway is an error, not a short window: handing
	// back the lines before it would read as the file ending there.
	var out []string
	n := 0
	for len(out) < lines {
		line, _, err := lr.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, fmt.Errorf("%s could not be read past line %d: %w", rel, n, err)
		}
		n++
		if n < start {
			continue
		}
		out = append(out, truncateRunes(string(line), maxLineRunes))
	}
	return out, start, nil
}

// countTrue is how many of flags are set.
func countTrue(flags ...bool) int {
	n := 0
	for _, f := range flags {
		if f {
			n++
		}
	}
	return n
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func truncateRunes(s string, max int) string {
	// A string of no more bytes than max cannot hold more runes than max,
	// which settles almost every log line without converting it.
	if len(s) <= max {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// Folder is one subfolder of a store root — a ticket, a run, a customer,
// a log bundle. Whatever the deployment happens to group by.
type Folder struct {
	Name     string    `json:"name"`
	Files    int       `json:"files"`
	Bytes    int64     `json:"bytes"`
	Modified time.Time `json:"modified"`
}

// ListFolders reports the subfolders under a root, newest first.
//
// The registered root is a STORE rather than a single investigation,
// which is what lets an agent be attached once and still reach whatever
// lands tomorrow. Attaching per subfolder would mean editing the agent
// every time somebody dropped one in.
// CountFolders answers how many subfolders a store has, and nothing else.
//
// Its own function because the answer costs one directory read, while
// ListFolders below costs a recursive walk of every subfolder — it stats each
// file for size, count and newest mtime so a folder MENU can be ordered and
// labelled. Three callers wanted the number alone and paid the walk for it,
// including two that render on the agent editor. Measured at 273ms of a 296ms
// source listing on one deployment, all of it spent computing sizes that were
// then discarded.
//
// Same rule as ListFolders on what counts: a DirEntry's IsDir, so the two can
// never disagree about the number, and a symlink to a directory is not one in
// either. IsDir here reads the type the directory read already returned, so
// there is no stat per entry.
func CountFolders(root string) (int, error) {
	rootAbs, err := filepath.EvalSymlinks(root)
	if err != nil {
		return 0, fmt.Errorf("that folder is unreadable: %w", err)
	}
	entries, err := os.ReadDir(rootAbs)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			n++
		}
	}
	return n, nil
}

func ListFolders(root string) ([]Folder, error) {
	rootAbs, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("that folder is unreadable: %w", err)
	}
	entries, err := os.ReadDir(rootAbs)
	if err != nil {
		return nil, err
	}
	var out []Folder
	for _, e := range entries {
		// A dot-folder is not a case: a pull's staging folder is one, and
		// listing it would offer a half-copied case as if it were there.
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		b := Folder{Name: e.Name()}
		// Walk for size and count, but stop early: a folder listing is a
		// menu, and a menu that stats a million files is not a menu.
		walked := 0
		_ = filepath.Walk(filepath.Join(rootAbs, e.Name()), func(_ string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			walked++
			if walked > maxFilesWalked {
				return io.EOF
			}
			b.Files++
			b.Bytes += info.Size()
			if info.ModTime().After(b.Modified) {
				b.Modified = info.ModTime()
			}
			return nil
		})
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

// SubRoot resolves an optional subfolder of a store, proving it stays
// inside the registered root.
//
// Empty means the store root itself, and that is what lets one shape
// serve both layouts: a FLAT store (a folder of files) and a GROUPED one
// (a parent whose subfolders are per-ticket, per-run, per-customer)
// differ only in whether the caller names a subfolder. Requiring the
// parameter would have forced every flat store to invent one.
//
// Every subfolder name arrives from a model, so this is the containment
// boundary for all of them.
func SubRoot(root, within string) (string, error) {
	if strings.TrimSpace(within) == "" {
		abs, err := filepath.EvalSymlinks(root)
		if err != nil {
			return "", fmt.Errorf("that folder is unreadable: %w", err)
		}
		return abs, nil
	}
	dir, err := resolveUnder(root, within)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%q is not a subfolder of this store", within)
	}
	return dir, nil
}

// EnsureSub resolves a subfolder for WRITING, creating it when absent.
//
// Containment has to be proved textually BEFORE the directory exists,
// because EvalSymlinks cannot resolve a path that is not there yet — so
// the cleaned join is checked first, and the resolved path is checked
// again afterwards. The second check is what catches a root whose parent
// contains a symlink, which the first cannot see.
//
// Empty means the store root itself, which is the flat-store case.
func EnsureSub(root, within string) (string, error) {
	rootAbs, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("that folder is unreadable: %w", err)
	}
	within = strings.TrimSpace(within)
	if within == "" {
		return rootAbs, nil
	}
	// WRITING takes the stricter rule: one path element, nothing else.
	//
	// Cleaning would happily turn "../escaped" into "escaped" and
	// "/etc/cron.d" into "etc/cron.d" — both contained, neither an
	// escape, and both a surprise: an upload aimed at /etc/cron.d would
	// silently create that tree inside the store. Refusing says what
	// happened. Reading stays permissive (see SubRoot) because a nested
	// path there is a real search result being read back.
	if within != filepath.Base(within) || within == "." || within == ".." {
		return "", fmt.Errorf("a subfolder must be a single name, not a path: %s", within)
	}
	full := filepath.Join(rootAbs, within)
	if !strings.HasPrefix(full, rootAbs+string(os.PathSeparator)) {
		return "", fmt.Errorf("that subfolder does not stay inside the store")
	}
	if err := os.MkdirAll(full, 0o755); err != nil {
		return "", fmt.Errorf("cannot create %s: %w", within, err)
	}
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(resolved, rootAbs+string(os.PathSeparator)) {
		return "", fmt.Errorf("that subfolder resolves outside the store")
	}
	return resolved, nil
}

// FileSpan is the first and last timestamp one file carries, read from its
// head and its tail rather than the whole of it, so a listing can say what
// period each file covers without a search. lastKnown is false for a
// compressed file, whose end cannot be reached without reading all of it.
// ok is false when the head carries no timestamp at all.
func FileSpan(root string, lf LogFile) (first, last time.Time, lastKnown, ok bool) {
	rootAbs, err := filepath.EvalSymlinks(root)
	if err != nil {
		return
	}
	path := filepath.Join(rootAbs, lf.Rel)
	format, year := detectFileFormat(path, lf.Modified)
	rc, err := open(path)
	if err != nil {
		return
	}
	lr := newLineReader(rc, maxLineBytes)
	for n := 0; n < 500; n++ {
		line, _, err := lr.next()
		if err != nil {
			break
		}
		if ts, found := bundle.ParseTime(format, year, string(line)); found {
			first, ok = ts, true
			break
		}
	}
	rc.Close()
	if !ok || lf.Gzipped {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	const tail = 256 << 10
	if lf.Size > tail {
		if _, err := f.Seek(lf.Size-tail, io.SeekStart); err != nil {
			return
		}
	}
	lr = newLineReader(f, maxLineBytes)
	for {
		line, _, err := lr.next()
		if err != nil {
			break
		}
		if ts, found := bundle.ParseTime(format, year, string(line)); found {
			last, lastKnown = ts, true
		}
	}
	return
}
