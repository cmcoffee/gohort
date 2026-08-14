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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Caps. These are deliberately not configurable.
//
// Every one of them exists because the failure they prevent is a turn
// that dumps a file into the context window and takes the conversation
// with it. An operator who could raise them would raise them exactly
// once, in the moment they were most sure they needed the whole file.
const (
	maxMatches     = 60      // hits returned by one search
	maxLineRunes   = 400     // a single line is truncated past this
	maxContext     = 8       // lines either side of a hit
	maxWindowLines = 400     // lines one read may return
	maxFilesWalked = 5000    // files considered in one search
	maxFileBytes   = 1 << 30 // skip anything larger; a 1GB log is not searched line by line
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
	Since      time.Time // skip files not modified since
	Context    int       // lines either side; clamped to maxContext
	Max        int       // hits; clamped to maxMatches
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
// Returns the matches plus whether the cap was hit, because "60 matches"
// and "the first 60 of many" are different answers and an investigator
// acting on the first as though it were the second draws a conclusion
// from a truncated set.
func Search(root string, opts SearchOpts) (matches []Match, capped bool, err error) {
	pattern := strings.TrimSpace(opts.Pattern)
	if pattern == "" {
		return nil, false, fmt.Errorf("no pattern given")
	}
	if opts.IgnoreCase {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, false, fmt.Errorf("that pattern is not a valid regular expression: %w", err)
	}
	ctxLines := clamp(opts.Context, 0, maxContext)
	limit := opts.Max
	if limit <= 0 || limit > maxMatches {
		limit = maxMatches
	}

	files, err := List(root, opts.Glob)
	if err != nil {
		return nil, false, err
	}
	rootAbs, _ := filepath.EvalSymlinks(root)

	for _, lf := range files {
		if len(matches) >= limit {
			capped = true
			break
		}
		if !opts.Since.IsZero() && lf.Modified.Before(opts.Since) {
			continue
		}
		if lf.Size > maxFileBytes {
			continue
		}
		got, hitCap := searchFile(filepath.Join(rootAbs, lf.Rel), lf.Rel, re, ctxLines, limit-len(matches))
		matches = append(matches, got...)
		if hitCap {
			capped = true
		}
	}
	return matches, capped, nil
}

// searchFile scans one file, keeping a small ring of preceding lines so a
// hit can carry its context without a second pass over the file.
func searchFile(path, rel string, re *regexp.Regexp, ctxLines, limit int) ([]Match, bool) {
	if limit <= 0 {
		return nil, true
	}
	rc, err := open(path)
	if err != nil {
		return nil, false
	}
	defer rc.Close()

	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var out []Match
	var ring []string
	// pending tracks hits still collecting their trailing context.
	type pending struct {
		idx  int
		left int
	}
	var waiting []pending

	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := truncateRunes(sc.Text(), maxLineRunes)

		for i := 0; i < len(waiting); {
			w := waiting[i]
			out[w.idx].After = append(out[w.idx].After, line)
			w.left--
			if w.left == 0 {
				waiting = append(waiting[:i], waiting[i+1:]...)
				continue
			}
			waiting[i] = w
			i++
		}

		if re.MatchString(line) && len(out) < limit {
			m := Match{File: rel, Line: lineNo, Text: line}
			if ctxLines > 0 && len(ring) > 0 {
				m.Before = append(m.Before, ring...)
			}
			out = append(out, m)
			if ctxLines > 0 {
				waiting = append(waiting, pending{idx: len(out) - 1, left: ctxLines})
			}
			if len(out) >= limit {
				// Keep scanning only long enough to finish the trailing
				// context already promised, then stop.
				if len(waiting) == 0 {
					return out, true
				}
			}
		}

		if ctxLines > 0 {
			ring = append(ring, line)
			if len(ring) > ctxLines {
				ring = ring[1:]
			}
		}
	}
	return out, len(out) >= limit
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
