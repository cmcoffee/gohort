// The store: uploaded evidence (a support dump, a log tarball, a diagnostic
// capture) unpacked, ingested into a hardware-locked-encrypted key-value store,
// and read back by decrypting in memory. The staged plaintext is discarded once
// ingest completes, so nothing but encrypted, derived content persists at rest.
//
// This is the repo backend's sibling (see repo_backend.go) and deliberately
// mirrors its shape, with one structural difference. A repo file is capped at
// 1 MiB and stored whole; a single log file out of a support dump is routinely
// hundreds of megabytes, and a store that could only hand back whole files
// would decrypt all of it to answer "show me lines 40100-40140". So a bundle
// file is stored as a sequence of fixed-height LINE SLICES, and a range read
// touches only the slices it needs.
//
// The other addition is the per-file INDEX, derived once at ingest: line count,
// detected format, time span, emitting host, severity histogram. Without it
// every question begins with a full scan. With it the lead can say what the
// bundle covers before reading a single line of content.
package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	// filesTable holds one File INDEX entry per ingested file,
	// keyed by the file's normalized path. Deliberately free of content: the
	// whole index is read to answer "what is in here", and carrying bodies
	// would make that read the entire bundle.
	filesTable = "bundle_files"
	// sliceTable holds the content, keyed <fileKey>:<sliceIndex>.
	sliceTable = "bundle_slices"
	// SliceLines is how many lines live in one slice. The tuning
	// trade-off is per-read waste against key count: 2000 lines is a few
	// hundred KB of a typical log, so a 40-line range read decrypts a few
	// hundred KB rather than the whole file, and a 10M-line file needs 5000
	// keys rather than millions.
	SliceLines = 2000
	// maxLineBytes truncates a single pathological line (a minified
	// blob, a base64 core dump on one line). The truncation is marked in the
	// stored text so a reader can tell the line was cut rather than ended.
	maxLineBytes = 32 << 10
	// truncMarker is appended to a line cut by maxLineBytes.
	truncMarker = "…[line truncated]"
)

// File is one ingested file's index entry. Content lives in the slice
// table; this is everything the tools can answer from without decrypting a
// byte of the log itself.
type File struct {
	Path   string // normalized, bundle-relative, forward-slashed
	Key    string // stable hash of Path; the slice-table key prefix
	Bytes  int64
	Lines  int
	Slices int
	// Format is the detected shape: "syslog", "iso", "clf", "jsonl", or
	// "text" when nothing matched. It drives timestamp parsing, and it is
	// reported to the LLM so an answer can say what it was reading.
	Format string
	// First / Last are the earliest and latest timestamps parsed out of the
	// file, RFC3339. Empty when the format carries no timestamp.
	First string
	Last  string
	// YearInferred records that the file's timestamps carry no year (classic
	// syslog) and the year was taken from the staged file's modification
	// time. It is surfaced everywhere the span is, because a span that is
	// off by a year is worse than no span at all if nobody is told.
	YearInferred bool
	// Host is the emitting host, when the format names one (syslog does).
	Host string
	// Severity counts uppercase severity tokens per file. Cheap to derive
	// during the ingest pass and the fastest way to point an investigation
	// at the file that actually went wrong.
	Severity map[string]int
	Ingested string // RFC3339
}

// fileKey derives the slice-table key prefix for a path. Hashed rather
// than used directly because a log path may contain any byte a filesystem
// allows, and the slice key has to append ":<n>" without the result colliding
// with a differently-sliced neighbour.
func fileKey(p string) string {
	sum := sha256.Sum256([]byte(p))
	return hex.EncodeToString(sum[:])[:16]
}

// sliceKey addresses one slice of one file.
func sliceKey(fileKey string, idx int) string {
	return fmt.Sprintf("%s:%d", fileKey, idx)
}

// FileCount reports how many files are ingested — the gate for "this
// bundle has finished ingesting", answered without reading any content.
func (b Bundle) FileCount() int {
	store := b.store()
	if store == nil {
		return 0
	}
	return len(store.Keys(filesTable))
}

// Index returns every ingested file's index entry, path-sorted.
func (b Bundle) Index() []File {
	store := b.store()
	if store == nil {
		return nil
	}
	keys := store.Keys(filesTable)
	out := make([]File, 0, len(keys))
	for _, k := range keys {
		var bf File
		if store.Get(filesTable, k, &bf) {
			out = append(out, bf)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Lookup fetches one index entry by path.
func (b Bundle) Lookup(p string) (File, bool) {
	store := b.store()
	if store == nil {
		return File{}, false
	}
	var bf File
	if !store.Get(filesTable, NormPath(p), &bf) {
		return File{}, false
	}
	return bf, true
}

// Wipe drops every slice and index entry for one bundle. Slices are
// removed via the index (each entry knows its own slice count) with a sweep of
// the slice table afterwards, so a half-written file from an interrupted ingest
// cannot leave orphaned ciphertext behind.
func (b Bundle) Wipe() {
	store := b.store()
	if store == nil {
		return
	}
	for _, k := range store.Keys(sliceTable) {
		store.Unset(sliceTable, k)
	}
	for _, k := range store.Keys(filesTable) {
		store.Unset(filesTable, k)
	}
}

// NormPath canonicalizes a path for use as an index key: forward slashes,
// no leading or trailing separator, no "." or ".." elements. Traversal is
// stripped here as well as at unpack time — this is the key that decides which
// stored file a tool reaches, so it does its own checking rather than trusting
// the caller upstream to have done it.
func NormPath(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	p = path.Clean("/" + p)
	return strings.Trim(p, "/")
}

// --- reads over the encrypted store (decrypt in memory) ---

// sliceLinesAt returns the lines of one slice.
func sliceLinesAt(store Store, bf File, idx int) []string {
	var body string
	if !store.Get(sliceTable, sliceKey(bf.Key, idx), &body) {
		return nil
	}
	if body == "" {
		return nil
	}
	return strings.Split(body, "\n")
}

// ReadRange returns lines [start, end] (1-based, inclusive) of one file,
// decrypting only the slices that overlap the range. An out-of-range start is
// an error rather than an empty result, so a caller that guessed at the line
// numbers is told so instead of concluding the file ends earlier than it does.
func (b Bundle) ReadRange(p string, start, end int) ([]string, File, error) {
	bf, ok := b.Lookup(p)
	if !ok {
		return nil, File{}, fmt.Errorf("no file %q in this bundle", p)
	}
	store := b.store()
	if store == nil {
		return nil, bf, fmt.Errorf("bundle store unavailable")
	}
	if start <= 0 {
		start = 1
	}
	if end <= 0 || end > bf.Lines {
		end = bf.Lines
	}
	if start > bf.Lines {
		return nil, bf, fmt.Errorf("start_line %d is past the end of %s (%d lines)", start, bf.Path, bf.Lines)
	}
	out := make([]string, 0, end-start+1)
	for idx := (start - 1) / SliceLines; idx <= (end-1)/SliceLines; idx++ {
		lines := sliceLinesAt(store, bf, idx)
		base := idx*SliceLines + 1 // line number of lines[0]
		for i, ln := range lines {
			n := base + i
			if n < start {
				continue
			}
			if n > end {
				break
			}
			out = append(out, ln)
		}
	}
	return out, bf, nil
}

// scanFile walks every line of one file in order, slice by slice, so a
// full scan of a large log holds one slice in memory rather than the file.
// Returning false from fn stops the walk.
func scanFile(store Store, bf File, fn func(lineNo int, text string) bool) {
	for idx := 0; idx < bf.Slices; idx++ {
		lines := sliceLinesAt(store, bf, idx)
		base := idx*SliceLines + 1
		for i, ln := range lines {
			if !fn(base+i, ln) {
				return
			}
		}
	}
}

// --- search ---

// Query is one search over a bundle. Every field except Pattern narrows;
// an empty query field means "no restriction on this axis".
type Query struct {
	Pattern  string // regular expression
	Glob     string // path filter, shell-style (path.Match), matched against the full path
	Before   int    // context lines before each hit
	After    int    // context lines after each hit
	Since    time.Time
	Until    time.Time
	MaxHits  int
	MaxFiles int // stop after this many DISTINCT files have produced hits
}

// Hit is one matching line plus its requested context.
type Hit struct {
	Path    string
	Line    int
	Text    string
	Context []string // rendered "N: text", including the hit line itself
}

// SearchResult carries the hits plus what the search had to leave out, so
// a truncated search reports its own truncation instead of reading as a
// complete answer.
type SearchResult struct {
	Hits      []Hit
	Truncated bool // hit MaxHits / MaxFiles before finishing
	Scanned   int  // files actually scanned
	Skipped   int  // files excluded by the glob or the time window
	// TimeUnknown counts files skipped by a time window because their format
	// carries no parseable timestamp. A window silently dropping the one file
	// that had the answer is exactly the failure worth naming.
	TimeUnknown int
}

// Search runs a regex across the bundle's files with optional path, time,
// and context narrowing.
func (b Bundle) Search(q Query) (SearchResult, error) {
	var res SearchResult
	if strings.TrimSpace(q.Pattern) == "" {
		return res, fmt.Errorf("pattern is required")
	}
	re, err := regexp.Compile("(?i)" + q.Pattern)
	if err != nil {
		return res, fmt.Errorf("pattern %q is not a valid regular expression: %w", q.Pattern, err)
	}
	store := b.store()
	if store == nil {
		return res, fmt.Errorf("bundle store unavailable")
	}
	if q.MaxHits <= 0 {
		q.MaxHits = MaxSearchHits
	}
	windowed := !q.Since.IsZero() || !q.Until.IsZero()
	filesHit := 0
	for _, bf := range b.Index() {
		if q.Glob != "" && !MatchGlob(q.Glob, bf.Path) {
			res.Skipped++
			continue
		}
		// A time window can exclude a whole file up front when its span is
		// known and disjoint — the cheapest possible narrowing on a bundle
		// where only one day of one file matters.
		if windowed {
			first, last, known := FileSpan(bf)
			if !known {
				res.TimeUnknown++
				continue
			}
			if !q.Until.IsZero() && first.After(q.Until) {
				res.Skipped++
				continue
			}
			if !q.Since.IsZero() && last.Before(q.Since) {
				res.Skipped++
				continue
			}
		}
		res.Scanned++
		hitsHere := searchOneFile(store, bf, re, q, windowed, &res)
		if hitsHere > 0 {
			filesHit++
		}
		if len(res.Hits) >= q.MaxHits || (q.MaxFiles > 0 && filesHit >= q.MaxFiles) {
			res.Truncated = true
			break
		}
	}
	return res, nil
}

// searchOneFile scans a single file, collecting hits with context. It
// keeps a rolling window of preceding lines rather than the whole file, and
// fills trailing context by continuing the scan past each hit.
//
// Hits still owed trailing context are tracked by INDEX into res.Hits, never by
// pointer: res.Hits grows during the scan, and a pointer into it would be left
// addressing a stale backing array the moment append reallocates — trailing
// context would then be written to a copy nobody reads.
func searchOneFile(store Store, bf File, re *regexp.Regexp, q Query, windowed bool, res *SearchResult) int {
	type owed struct{ idx, remaining int }
	var (
		prev    []string // rolling "N: text" of the last q.Before lines
		pending []owed
		found   int
	)
	year := fileYear(bf)
	scanFile(store, bf, func(n int, text string) bool {
		numbered := fmt.Sprintf("%d: %s", n, text)
		// Pay down trailing context owed to earlier hits.
		if len(pending) > 0 {
			kept := pending[:0]
			for _, p := range pending {
				res.Hits[p.idx].Context = append(res.Hits[p.idx].Context, numbered)
				if p.remaining--; p.remaining > 0 {
					kept = append(kept, p)
				}
			}
			pending = kept
		}

		if re.MatchString(text) && (!windowed || LineInWindow(bf.Format, year, text, q.Since, q.Until)) {
			hit := Hit{Path: bf.Path, Line: n, Text: strings.TrimRight(text, "\r")}
			if q.Before > 0 || q.After > 0 {
				hit.Context = append(append([]string{}, prev...), numbered)
			}
			res.Hits = append(res.Hits, hit)
			found++
			if q.After > 0 {
				pending = append(pending, owed{idx: len(res.Hits) - 1, remaining: q.After})
			}
			if len(res.Hits) >= q.MaxHits {
				res.Truncated = true
				return false
			}
		}

		if q.Before > 0 {
			prev = append(prev, numbered)
			if len(prev) > q.Before {
				prev = prev[1:]
			}
		}
		return true
	})
	return found
}

// MatchGlob matches a shell-style pattern against a path. A pattern with
// no slash matches the BASENAME, because "*.log" meaning "nothing, since every
// path has a directory in front of it" is never what the caller wanted.
func MatchGlob(pattern, p string) bool {
	if strings.Contains(pattern, "/") {
		ok, err := path.Match(pattern, p)
		return err == nil && ok
	}
	ok, err := path.Match(pattern, path.Base(p))
	if err == nil && ok {
		return true
	}
	// Fall back to a substring test so a caller who typed a directory name
	// or a fragment gets the files they clearly meant.
	return strings.Contains(strings.ToLower(p), strings.ToLower(pattern))
}

// --- timeline ---

// TimelineEntry is one time-ordered line drawn from some file.
type TimelineEntry struct {
	When time.Time
	Path string
	Line int
	Text string
}

// Timeline merges lines from several files into one time-ordered slice —
// the view a support dump is usually read for, and the one thing no per-file
// tool can produce. Files whose format carries no parseable timestamp cannot be
// merged and are named in unmergeable rather than dropped silently.
func (b Bundle) Timeline(glob string, since, until time.Time, max int) (entries []TimelineEntry, unmergeable []string, err error) {
	store := b.store()
	if store == nil {
		return nil, nil, fmt.Errorf("bundle store unavailable")
	}
	if max <= 0 {
		max = MaxTimelineLines
	}
	for _, bf := range b.Index() {
		if glob != "" && !MatchGlob(glob, bf.Path) {
			continue
		}
		if _, _, known := FileSpan(bf); !known {
			unmergeable = append(unmergeable, bf.Path)
			continue
		}
		year := fileYear(bf)
		scanFile(store, bf, func(n int, text string) bool {
			ts, ok := ParseTime(bf.Format, year, text)
			if !ok {
				return true
			}
			if !since.IsZero() && ts.Before(since) {
				return true
			}
			if !until.IsZero() && ts.After(until) {
				return true
			}
			entries = append(entries, TimelineEntry{When: ts, Path: bf.Path, Line: n, Text: strings.TrimRight(text, "\r")})
			return true
		})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].When.Before(entries[j].When) })
	if len(entries) > max {
		entries = entries[:max]
	}
	return entries, unmergeable, nil
}
