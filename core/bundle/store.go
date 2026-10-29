// The store: uploaded evidence (a support dump, a log tarball, a diagnostic
// capture) unpacked, ingested into a padlock-encrypted key-value store,
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
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
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

// Export writes one file's stored lines to w, a line per line, and reports
// whether the file had any to write. What comes back is what was indexed, not
// what was uploaded: a line cut at ingest stays cut (it carries the marker),
// line endings are "\n", and a binary or unopened archive has no stored lines
// at all, so it writes nothing and reports false.
func (b Bundle) Export(p string, w io.Writer) (bool, error) {
	bf, ok := b.Lookup(p)
	if !ok {
		return false, fmt.Errorf("no file %q in this bundle", p)
	}
	if bf.Format == FormatBinary || bf.Format == FormatArchive {
		return false, nil
	}
	store := b.store()
	if store == nil {
		return false, fmt.Errorf("bundle store unavailable")
	}
	bw := bufio.NewWriter(w)
	for idx := 0; idx < bf.Slices; idx++ {
		for _, ln := range sliceLinesAt(store, bf, idx) {
			bw.WriteString(ln)
			bw.WriteByte('\n')
		}
	}
	return true, bw.Flush()
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
	Pattern string // regular expression
	Exclude string // optional regular expression: a line Pattern matched is dropped if this matches it too
	Glob    string // path filter, shell-style (path.Match), matched against the full path
	Before  int    // context lines before each hit
	After   int    // context lines after each hit
	Since   time.Time
	Until   time.Time
	MaxHits int // hits kept; MaxSearchHits when unset
	PerFile int // one file's share of MaxHits; 0 for none
	// CountOnly counts every match per file and keeps none: how often, and
	// where, over the whole bundle.
	CountOnly bool
	// Group collects the matching lines into groups by shape (see group.go)
	// instead of returning them: every file is read, no hit is kept, and
	// Before, After, MaxHits and PerFile do not apply.
	Group bool
	// OverTime counts the matching lines by their timestamps (overtime.go)
	// instead of returning them, on the same terms as Group. Bucket is the
	// size of one row; zero chooses one from the span.
	OverTime bool
	Bucket   time.Duration
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
	// Budget is embedded so its counts read as the result's own: Total
	// counted, Kept, Tallies (count-only: busiest first), CutInFiles,
	// Unsearched (budget.go).
	Budget
	Hits      []Hit
	Truncated bool // the hits are not the whole answer: Budget.Cut()
	Scanned   int  // files actually scanned
	Skipped   int  // files excluded by the glob or the time window
	// Excluded counts lines the pattern matched and Exclude took back out,
	// so a search that shows nothing can say whether it found nothing.
	Excluded int
	// TimeUnknown counts files skipped by a time window because their format
	// carries no parseable timestamp. A window silently dropping the one file
	// that had the answer is exactly the failure worth naming.
	TimeUnknown int
	// Groups holds the matches by shape when the query asked for Group;
	// nil otherwise.
	Groups *Grouper
	// Histogram holds the matches by time when the query asked for
	// OverTime; nil otherwise.
	Histogram *Histogram
}

// Search runs a regex across the bundle's files with optional path, time,
// and context narrowing.
// Search takes a context because a search over a multi-gigabyte dump is the
// longest thing this package does, and the agent loop can only notice a cancel
// between rounds — a tool handler that ignores its context holds the round open
// however hard the user presses stop. Checked between files and periodically
// inside one, since a single 2GB log is one iteration of the outer loop.
//
// A nil ctx is treated as an uncancellable one rather than panicking: the
// callers that predate this are all real, and none of them should crash.
// searchCancelEvery is how often the line scanner tests for cancellation.
// Small enough that a stop feels immediate on any real file, large enough that
// the check does not show up next to the regexp match that is the actual work.
const searchCancelEvery = 2048

func (b Bundle) Search(ctx context.Context, q Query) (SearchResult, error) {
	var res SearchResult
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(q.Pattern) == "" {
		return res, fmt.Errorf("pattern is required")
	}
	re, err := CompileMatcher("(?i)" + q.Pattern)
	if err != nil {
		return res, fmt.Errorf("pattern %q is not a valid regular expression: %w", q.Pattern, err)
	}
	f := Filter{Match: re}
	if strings.TrimSpace(q.Exclude) != "" {
		if f.Exclude, err = CompileMatcher("(?i)" + q.Exclude); err != nil {
			return res, fmt.Errorf("exclude %q is not a valid regular expression: %w", q.Exclude, err)
		}
	}
	store := b.store()
	if store == nil {
		return res, fmt.Errorf("bundle store unavailable")
	}
	if q.MaxHits <= 0 {
		q.MaxHits = MaxSearchHits
	}
	modes := 0
	for _, on := range []bool{q.CountOnly, q.Group, q.OverTime} {
		if on {
			modes++
		}
	}
	if modes > 1 {
		return res, fmt.Errorf("count, group and over_time are different answers: count says how often a pattern occurs per file, group what kinds of lines there are, over_time when the matching ones happened. Ask for one")
	}
	if q.Group {
		res.Groups = NewGrouper()
	}
	if q.OverTime {
		res.Histogram = NewHistogram()
	}
	index := b.Index()
	inScope := 0
	for _, bf := range index {
		if q.Glob == "" || MatchGlob(q.Glob, bf.Path) {
			inScope++
		}
	}
	budget := &res.Budget
	budget.Max, budget.PerFile = q.MaxHits, q.PerFile
	// The share exists to divide the limit between files; with one file in
	// scope there is nobody to divide it with.
	if inScope <= 1 {
		budget.PerFile = 0
	}
	// A whole-bundle answer counts every match and keeps none.
	if modes == 1 {
		q.Before, q.After = 0, 0
		budget.Max, budget.PerFile = 0, 0
	}
	windowed := !q.Since.IsZero() || !q.Until.IsZero()
	for i, bf := range index {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		// Kept all it may: the files not yet opened are counted, so the
		// reply can say they were never searched.
		if budget.Full() {
			for _, rest := range index[i:] {
				if q.Glob == "" || MatchGlob(q.Glob, rest.Path) {
					budget.Unsearched++
				}
			}
			break
		}
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
		budget.Open(bf.Path)
		searchOneFile(ctx, store, bf, f, q, windowed, &res)
		budget.Close()
	}
	if q.CountOnly {
		sort.SliceStable(res.Tallies, func(i, j int) bool { return res.Tallies[i].Matches > res.Tallies[j].Matches })
	}
	res.Truncated = res.Cut()
	return res, nil
}

// searchOneFile scans a single file, collecting hits with context. It
// keeps a rolling window of preceding lines rather than the whole file, and
// fills trailing context by continuing the scan past each hit. It reads the
// whole file even once nothing more will be kept, because the count is part
// of the answer: "25 kept of 340" needs the 340.
//
// Hits still owed trailing context are tracked by INDEX into res.Hits, never by
// pointer: res.Hits grows during the scan, and a pointer into it would be left
// addressing a stale backing array the moment append reallocates — trailing
// context would then be written to a copy nobody reads.
func searchOneFile(ctx context.Context, store Store, bf File, f Filter, q Query, windowed bool, res *SearchResult) {
	type owed struct{ idx, remaining int }
	// seen is a line held for leading context. Held as its number and text
	// rather than as the "N: text" it may be shown as: the text is a piece of
	// the slice already in memory, so holding it costs nothing, and only the
	// few lines that end up beside a hit are ever formatted.
	type seen struct {
		n    int
		text string
	}
	var (
		prev    []seen // the last q.Before lines
		pending []owed
	)
	year := fileYear(bf)
	scanFile(store, bf, func(n int, text string) bool {
		// One file can be the whole search, so the outer loop's check is not
		// enough on its own. Sampled rather than per-line: ctx.Err() takes a
		// lock, and this callback runs once per line of every log in the dump.
		if n%searchCancelEvery == 0 && ctx.Err() != nil {
			return false
		}
		// Pay down trailing context owed to earlier hits.
		if len(pending) > 0 {
			numbered := numberLine(n, text)
			kept := pending[:0]
			for _, p := range pending {
				res.Hits[p.idx].Context = append(res.Hits[p.idx].Context, numbered)
				if p.remaining--; p.remaining > 0 {
					kept = append(kept, p)
				}
			}
			pending = kept
		}

		keep := f.Match.MatchString(text) && (!windowed || LineInWindow(bf.Format, year, text, q.Since, q.Until))
		// Exclusion is tested last, on lines already in, so Excluded counts
		// only what the search would otherwise have shown.
		if keep && f.ExcludesString(text) {
			res.Excluded++
			keep = false
		}
		// A grouped search files the line under its shape, and an over-time
		// one under its time; neither keeps a hit. Dated from the line
		// itself, when the file's format carries times at all: a line with
		// none is counted as undated, never placed at a guessed time.
		// Every match is counted by the budget, kept or not.
		kept := keep && res.Budget.Keep()
		if keep && (res.Groups != nil || res.Histogram != nil) {
			var when time.Time
			if bf.Format != FormatText {
				when, _ = ParseTime(bf.Format, year, text)
			}
			if res.Groups != nil {
				res.Groups.Add(bf.Path, n, strings.TrimRight(text, "\r"), when)
			} else {
				res.Histogram.Add(when)
			}
		}
		if kept {
			hit := Hit{Path: bf.Path, Line: n, Text: strings.TrimRight(text, "\r")}
			if q.Before > 0 || q.After > 0 {
				hit.Context = make([]string, 0, len(prev)+1+q.After)
				for _, p := range prev {
					hit.Context = append(hit.Context, numberLine(p.n, p.text))
				}
				hit.Context = append(hit.Context, numberLine(n, text))
			}
			res.Hits = append(res.Hits, hit)
			if q.After > 0 {
				pending = append(pending, owed{idx: len(res.Hits) - 1, remaining: q.After})
			}
		}

		if q.Before > 0 {
			prev = append(prev, seen{n: n, text: text})
			if len(prev) > q.Before {
				prev = prev[1:]
			}
		}
		return true
	})
}

// numberLine is a context line as a search shows it: "N: text". Formatted
// only for a line that is shown; a search looks at every line of the dump and
// shows almost none of them.
func numberLine(n int, text string) string {
	return strconv.Itoa(n) + ": " + text
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

// TimelineQuery narrows a merged timeline. Every field narrows; an empty one
// means no restriction on that axis.
type TimelineQuery struct {
	// Pattern keeps only the lines it matches (case-insensitive, as Search):
	// "follow request abc123 through every service, in order" in one call,
	// where without it that is a search per file and a merge by hand.
	Pattern string
	Exclude string // drops a line Pattern kept when this matches it too
	Glob    string // path filter, as Search's
	Since   time.Time
	Until   time.Time
	Max     int // lines returned; MaxTimelineLines when unset
}

// TimelineResult is a timeline plus everything it left off, each said, so a
// cut or filtered timeline never reads as the whole story.
type TimelineResult struct {
	Entries []TimelineEntry // the earliest Max lines, in time order
	// Total is how many lines belonged on the timeline before Max cut it:
	// more than len(Entries) means the reply is the start of a longer story.
	Total int
	// Unmergeable names the files with no parseable timestamps, which no
	// timeline can place.
	Unmergeable []string
	// Undated counts lines Pattern matched that carry no timestamp of their
	// own (a stack frame, a wrapped message): there is no time to put them
	// at, so they are not on it, and the reply says they exist.
	Undated int
	// Excluded counts lines Pattern matched that Exclude took back out.
	Excluded int
}

// Timeline merges lines from several files into one time-ordered slice —
// the view a support dump is usually read for, and the one thing no per-file
// tool can produce. Files whose format carries no parseable timestamp cannot be
// merged and are named in Unmergeable rather than dropped silently.
//
// It keeps only the earliest Max lines while it reads, in a heap, rather than
// every line in the window and a sort at the end: an unbounded timeline over a
// multi-gigabyte bundle used to hold millions of entries to return three
// hundred. A file whose span is disjoint from the window is skipped unread, and
// the run stops at ctx, the same as Search.
func (b Bundle) Timeline(ctx context.Context, q TimelineQuery) (TimelineResult, error) {
	var res TimelineResult
	if ctx == nil {
		ctx = context.Background()
	}
	store := b.store()
	if store == nil {
		return res, fmt.Errorf("bundle store unavailable")
	}
	if q.Max <= 0 {
		q.Max = MaxTimelineLines
	}
	var f Filter
	if strings.TrimSpace(q.Pattern) != "" {
		re, err := CompileMatcher("(?i)" + q.Pattern)
		if err != nil {
			return res, fmt.Errorf("pattern %q is not a valid regular expression: %w", q.Pattern, err)
		}
		f.Match = re
	}
	if strings.TrimSpace(q.Exclude) != "" {
		ex, err := CompileMatcher("(?i)" + q.Exclude)
		if err != nil {
			return res, fmt.Errorf("exclude %q is not a valid regular expression: %w", q.Exclude, err)
		}
		f.Exclude = ex
	}
	keep := &earliest{max: q.Max}
	for _, bf := range b.Index() {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if q.Glob != "" && !MatchGlob(q.Glob, bf.Path) {
			continue
		}
		first, last, known := FileSpan(bf)
		if !known {
			res.Unmergeable = append(res.Unmergeable, bf.Path)
			continue
		}
		if (!q.Until.IsZero() && first.After(q.Until)) || (!q.Since.IsZero() && last.Before(q.Since)) {
			continue
		}
		year := fileYear(bf)
		scanFile(store, bf, func(n int, text string) bool {
			if n%searchCancelEvery == 0 && ctx.Err() != nil {
				return false
			}
			// The pattern first: with its prefilter it rejects a line far
			// faster than the timestamp regexp can date one.
			if f.Match != nil && !f.Match.MatchString(text) {
				return true
			}
			ts, ok := ParseTime(bf.Format, year, text)
			if !ok {
				if f.Match != nil {
					res.Undated++
				}
				return true
			}
			if (!q.Since.IsZero() && ts.Before(q.Since)) || (!q.Until.IsZero() && ts.After(q.Until)) {
				return true
			}
			if f.ExcludesString(text) {
				res.Excluded++
				return true
			}
			res.Total++
			keep.offer(TimelineEntry{When: ts, Path: bf.Path, Line: n, Text: text})
			return true
		})
	}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	res.Entries = keep.sorted()
	return res, nil
}

// earliest holds the max earliest entries offered to it: a max-heap on time,
// so the latest one held is the one a newcomer has to beat. Ties keep the
// order entries were offered in (file, then line), which is the order the old
// stable sort gave them.
type earliest struct {
	max   int
	items []heldEntry
	seq   int
}

type heldEntry struct {
	TimelineEntry
	seq int
}

// later reports whether a sorts after b.
func (a heldEntry) later(b heldEntry) bool {
	if !a.When.Equal(b.When) {
		return a.When.After(b.When)
	}
	return a.seq > b.seq
}

// offer keeps e when it is among the earliest seen. A kept entry's text is
// copied: a line is a piece of its whole decrypted slice, and holding the
// piece would hold the slice.
func (h *earliest) offer(e TimelineEntry) {
	h.seq++
	he := heldEntry{TimelineEntry: e, seq: h.seq}
	if len(h.items) == h.max {
		if !h.items[0].later(he) {
			return
		}
		he.Text = strings.Clone(strings.TrimRight(he.Text, "\r"))
		h.items[0] = he
		h.down(0)
		return
	}
	he.Text = strings.Clone(strings.TrimRight(he.Text, "\r"))
	h.items = append(h.items, he)
	for i := len(h.items) - 1; i > 0; {
		parent := (i - 1) / 2
		if !h.items[i].later(h.items[parent]) {
			break
		}
		h.items[i], h.items[parent] = h.items[parent], h.items[i]
		i = parent
	}
}

// down restores the heap below i after its entry was replaced.
func (h *earliest) down(i int) {
	for {
		top, l, r := i, 2*i+1, 2*i+2
		if l < len(h.items) && h.items[l].later(h.items[top]) {
			top = l
		}
		if r < len(h.items) && h.items[r].later(h.items[top]) {
			top = r
		}
		if top == i {
			return
		}
		h.items[i], h.items[top] = h.items[top], h.items[i]
		i = top
	}
}

// sorted returns what was kept, earliest first.
func (h *earliest) sorted() []TimelineEntry {
	sort.Slice(h.items, func(i, j int) bool { return h.items[j].later(h.items[i]) })
	out := make([]TimelineEntry, len(h.items))
	for i, he := range h.items {
		out[i] = he.TimelineEntry
	}
	return out
}
