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
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
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
	maxMatches     = 60      // hits returned by one search, unless the caller asks for more
	maxMatchesCeil = 200     // the most a caller may ask for; see SearchOpts.Max
	maxCountFiles  = 200     // files listed by a count-only search
	maxLineRunes   = 400     // a single line is truncated past this
	maxContext     = 8       // lines either side of a hit
	maxWindowLines = 400     // lines one read may return
	maxFilesWalked = 5000    // files considered in one search
	maxFileBytes   = 1 << 30 // skip anything larger; a 1GB log is not searched line by line
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
}

// SearchOpts is what a caller may vary.
type SearchOpts struct {
	Pattern    string // regular expression; a plain string is a valid one
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
	// Max is how many matches the reply shows: maxMatches when unset, never
	// more than maxMatchesCeil. A caller raises it for a narrow, deliberate
	// search; it is not a way to read a noisy file whole.
	Max int
	// PerFile caps how many of ONE file's matches may be shown, so a noisy
	// file cannot take the whole budget from the others. Zero means no
	// per-file cap. Lifted when only one file is searched: the cap exists to
	// share the budget, and there is nobody to share it with.
	PerFile int
	// Skip drops this many matches from the front of the result, for paging:
	// the same search with Skip set to what was shown returns the next page.
	Skip int
	// CountOnly returns per-file match totals and no lines at all, over every
	// file, with no Max. It answers how OFTEN, which a capped list cannot.
	CountOnly bool
	// Deadline bounds the whole search. Zero uses searchDeadline. A
	// search that runs out of time returns what it found and says so
	// rather than failing: partial evidence with a warning beats no
	// evidence, and beats a turn that never comes back.
	Deadline time.Duration
}

// FileTally is one searched file's count: every match in it, and how many of
// those were let into the result (the per-file cap is the difference).
type FileTally struct {
	File    string `json:"file"`
	Matches int    `json:"matches"`
	Kept    int    `json:"kept"`
}

// SearchResult is one search's answer plus what it did NOT do.
//
// Every truncation is reported, and they mean different things. Capped is
// "there are more matches than we return", and it says WHERE: CutInFiles are
// the files that had more than PerFile, Unsearched how many files the global
// cap left unread. Stopped is "we gave up before looking everywhere". An
// investigator who reads a partial result as a complete one concludes
// something is absent when nobody looked.
type SearchResult struct {
	Matches    []Match
	Capped     bool
	Stopped    string // empty when the search finished; otherwise why it did not
	Scanned    int    // files actually read
	Considered int    // files the search could have read
	Bytes      int64  // bytes read across those files
	Elapsed    time.Duration
	Max        int         // the limits that applied, so the reply can state them
	PerFile    int         // 0 when no per-file cap applied
	Skipped    int         // matches dropped by Skip
	Tallies    []FileTally // files with at least one match, in search order (count-only: by count)
	CutInFiles []FileTally // files that had more matches than PerFile allowed
	Unsearched int         // files the global cap left unread
	Untimed    int         // files left out of a time window because they carry no timestamps
	Total      int         // matches counted across the files searched
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
	// EvalSymlinks fails on a path that does not exist, which is a
	// legitimate "no such file" rather than a containment failure.
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no such file in this store: %s", rel)
		}
		return "", err
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
	pattern := strings.TrimSpace(opts.Pattern)
	if pattern == "" {
		return res, fmt.Errorf("no pattern given")
	}
	if opts.IgnoreCase {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return res, fmt.Errorf("that pattern is not a valid regular expression: %w", err)
	}
	if !opts.Since.IsZero() && !opts.Until.IsZero() && opts.Until.Before(opts.Since) {
		return res, fmt.Errorf("the time window ends before it starts")
	}
	ctxLines := clamp(opts.Context, 0, maxContext)
	if opts.CountOnly {
		ctxLines = 0
	}
	limit := opts.Max
	if limit <= 0 {
		limit = maxMatches
	}
	if limit > maxMatchesCeil {
		limit = maxMatchesCeil
	}
	skip := opts.Skip
	if skip < 0 {
		skip = 0
	}

	files, err := List(root, opts.Glob)
	if err != nil {
		return res, err
	}
	rootAbs, _ := filepath.EvalSymlinks(root)
	res.Considered = len(files)
	res.Max = limit
	perFile := opts.PerFile
	if perFile <= 0 || len(files) <= 1 || opts.CountOnly {
		perFile = 0
	}
	res.PerFile = perFile

	budget := opts.Deadline
	if budget <= 0 {
		budget = searchDeadline
	}
	started := time.Now()
	deadline := started.Add(budget)
	var scanned int64
	win := window{since: opts.Since, until: opts.Until}

	for i, lf := range files {
		if !opts.CountOnly && len(res.Matches) >= limit {
			res.Capped = true
			res.Unsearched = len(files) - i
			break
		}
		if !opts.Since.IsZero() && lf.Modified.Before(opts.Since) {
			continue // written to for the last time before the window opens
		}
		if lf.Size > maxFileBytes {
			continue
		}
		// Cancellation rides beside the clock, on the same reasoning: a
		// folder of ten thousand small files gives the outer loop plenty of
		// chances to notice, and one enormous file gives it none — which is
		// why searchFile carries the check too.
		if err := ctx.Err(); err != nil {
			return res, err
		}
		// Checked between files as well as inside one: a folder of ten
		// thousand small files exhausts the clock without any single
		// file being slow.
		if time.Now().After(deadline) {
			res.Stopped = fmt.Sprintf("stopped after %s, having read %d of %d files", budget, res.Scanned, len(files))
			break
		}
		if scanned >= maxScanBytes {
			res.Stopped = fmt.Sprintf("stopped after reading %d MB, at %d of %d files", scanned>>20, res.Scanned, len(files))
			break
		}
		path := filepath.Join(rootAbs, lf.Rel)
		fw := win
		if win.set() {
			fw.format, fw.year = detectFileFormat(path, lf.Modified)
		}
		room := limit - len(res.Matches)
		if opts.CountOnly {
			room = 0
		}
		fr := searchFile(ctx, path, lf.Rel, re, ctxLines, fileLimits{keep: perFile, skip: skip, room: room}, fw, deadline)
		res.Matches = append(res.Matches, fr.shown...)
		skip -= fr.skipped
		res.Skipped += fr.skipped
		res.Scanned++
		scanned += fr.read
		res.Total += fr.total
		if win.set() && !fr.timed {
			res.Untimed++
		}
		if fr.total > 0 {
			t := FileTally{File: lf.Rel, Matches: fr.total, Kept: fr.kept}
			res.Tallies = append(res.Tallies, t)
			if perFile > 0 && fr.total > perFile {
				res.CutInFiles = append(res.CutInFiles, t)
			}
		}
		if fr.stopped {
			res.Stopped = fmt.Sprintf("stopped inside %s after %s, having read %d of %d files", lf.Rel, time.Since(started).Round(time.Second), res.Scanned, len(files))
			break
		}
		if !opts.CountOnly && fr.overflow {
			res.Capped = true
		}
	}
	if len(res.CutInFiles) > 0 {
		res.Capped = true
	}
	if opts.CountOnly {
		sort.SliceStable(res.Tallies, func(i, j int) bool { return res.Tallies[i].Matches > res.Tallies[j].Matches })
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
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var sample []string
	for len(sample) < 60 && sc.Scan() {
		sample = append(sample, sc.Text())
	}
	return bundle.DetectFormat(sample), modified.Year()
}

// fileLimits is what one file may contribute: keep caps the matches it lets
// into the stream (0 = no cap), skip is how many stream matches are still to
// be dropped for paging, room is how many may still be shown.
type fileLimits struct {
	keep, skip, room int
}

// fileResult is one file's part of a search.
type fileResult struct {
	shown    []Match
	total    int  // every match in the file
	kept     int  // matches let into the stream (shown + skipped)
	skipped  int  // stream matches dropped for paging
	overflow bool // stream matches this file had beyond the room left
	timed    bool // the file carried at least one timestamp
	stopped  bool // the deadline or a cancel cut the file short
	read     int64
}

// searchFile scans one file, keeping a small ring of preceding lines so a
// hit can carry its context without a second pass over the file. It reads
// the whole file even once nothing more will be shown, because the COUNT is
// part of the answer: "15 shown of 340" needs the 340.
func searchFile(ctx context.Context, path, rel string, re *regexp.Regexp, ctxLines int, lim fileLimits, win window, deadline time.Time) fileResult {
	var fr fileResult
	rc, err := open(path)
	if err != nil {
		return fr
	}
	defer rc.Close()

	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var ring []string
	// pending tracks shown hits still collecting their trailing context.
	type pending struct {
		idx  int
		left int
	}
	var waiting []pending
	var lineTime time.Time

	lineNo := 0
	for sc.Scan() {
		lineNo++
		fr.read += int64(len(sc.Bytes())) + 1
		// One clock read per 4096 lines rather than per line: the check
		// has to be cheap enough that it is never the reason to skip it.
		// A .gz that decompresses to something enormous is caught here
		// and nowhere else — its on-disk size passed the file cap.
		if lineNo%4096 == 0 && (time.Now().After(deadline) || ctx.Err() != nil) {
			fr.stopped = true
			return fr
		}
		line := truncateRunes(sc.Text(), maxLineRunes)

		for i := 0; i < len(waiting); {
			w := waiting[i]
			fr.shown[w.idx].After = append(fr.shown[w.idx].After, line)
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
			if ts, ok := bundle.ParseTime(win.format, win.year, line); ok {
				lineTime = ts
				fr.timed = true
			}
			inWindow = win.holds(lineTime)
		}

		if inWindow && re.MatchString(line) {
			fr.total++
			switch {
			case lim.keep > 0 && fr.kept >= lim.keep:
				// Past this file's share: counted, not kept.
			case fr.skipped < lim.skip:
				fr.kept++
				fr.skipped++
			case len(fr.shown) < lim.room:
				fr.kept++
				m := Match{File: rel, Line: lineNo, Text: line}
				if ctxLines > 0 && len(ring) > 0 {
					m.Before = append(m.Before, ring...)
				}
				fr.shown = append(fr.shown, m)
				if ctxLines > 0 {
					waiting = append(waiting, pending{idx: len(fr.shown) - 1, left: ctxLines})
				}
			default:
				fr.overflow = true
			}
		}

		if ctxLines > 0 {
			ring = append(ring, line)
			if len(ring) > ctxLines {
				ring = ring[1:]
			}
		}
	}
	return fr
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
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var out []string
	n := 0
	for sc.Scan() {
		n++
		if n < start {
			continue
		}
		if len(out) >= lines {
			break
		}
		out = append(out, truncateRunes(sc.Text(), maxLineRunes))
	}
	return out, start, nil
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
		if !e.IsDir() {
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
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for n := 0; n < 500 && sc.Scan(); n++ {
		if ts, found := bundle.ParseTime(format, year, sc.Text()); found {
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
	sc = bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if ts, found := bundle.ParseTime(format, year, sc.Text()); found {
			last, lastKnown = ts, true
		}
	}
	return
}
