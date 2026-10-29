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
	scanSlices(store, bf, 0, bf.Slices, fn)
}

// scanSlices is scanFile over slices [from, to) only: one chunk of a file.
func scanSlices(store Store, bf File, from, to int, fn func(lineNo int, text string) bool) {
	for idx := from; idx < to; idx++ {
		lines := sliceLinesAt(store, bf, idx)
		base := idx*SliceLines + 1
		for i, ln := range lines {
			if !fn(base+i, ln) {
				return
			}
		}
	}
}

// --- chunks ---

// ChunkSlices is how many slices one chunk of a file holds: the unit a search
// or timeline reads on one core. A large log is one file but many chunks, so
// it is read on several cores at once instead of one; a small one is a single
// chunk, read as it always was. A variable so a test can put a chunk boundary
// at every slice.
var ChunkSlices = 8

// chunk is a run of one file's slices: [from, to) of the file at index file
// in the list being read, and whether it is that file's first and last.
type chunk struct {
	file        int
	from, to    int
	first, last bool
}

// chunksOf splits files into chunks, in order. A file with no slices (a
// binary, an unopened archive, an empty file) is one empty chunk, so it is
// still counted as read.
func chunksOf(files []File) []chunk {
	size := ChunkSlices
	if size < 1 {
		size = 1
	}
	var out []chunk
	for fi, bf := range files {
		if bf.Slices <= 0 {
			out = append(out, chunk{file: fi, first: true, last: true})
			continue
		}
		for from := 0; from < bf.Slices; from += size {
			to := from + size
			if to > bf.Slices {
				to = bf.Slices
			}
			out = append(out, chunk{file: fi, from: from, to: to, first: from == 0, last: to == bf.Slices})
		}
	}
	return out
}

// --- search ---

// Query is one search over a bundle. Every field except Pattern narrows;
// an empty query field means "no restriction on this axis".
type Query struct {
	Pattern string // regular expression
	// Patterns are further patterns looked for in the same pass (lanes.go):
	// each answered as if searched alone, in SearchResult.Lanes. Pattern,
	// when set, is the first of them.
	Patterns []string
	Exclude  string // optional regular expression: a line Pattern matched is dropped if this matches it too
	Glob     string // path filter, shell-style (path.Match), matched against the full path
	Before   int    // context lines before each hit
	After    int    // context lines after each hit
	Since    time.Time
	Until    time.Time
	MaxHits  int // hits kept; MaxSearchHits when unset
	PerFile  int // one file's share of MaxHits; 0 for none
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
	// Records gives each kept hit its whole log record instead of Before and
	// After lines (records.go): from the dated line it belongs to down to the
	// next. Before and After still apply in a file with no timestamps, where
	// records cannot be told apart, and default to RecordFallback there.
	Records bool
}

// RecordFallback is the context lines either side of a hit asked for with its
// record, in a file whose records cannot be found.
const RecordFallback = 3

// Hit is one matching line plus its requested context.
type Hit struct {
	Path    string
	Line    int
	Text    string
	Context []string // rendered "N: text", including the hit line itself
	// Record says how Context was drawn when the query asked for records;
	// nil otherwise.
	Record *RecordView
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
	// Lanes is each pattern's answer when the query looked for several, in
	// the order given (Lane gives one as a search of its own). Nil for one
	// pattern, whose answer is the fields above; for several, those hold
	// only what the patterns share (Scanned, Skipped, TimeUnknown).
	Lanes []Lane[Hit]
}

// Lane is lane i of a search of several patterns as the result a search for
// that pattern alone would have returned.
func (r SearchResult) Lane(i int) SearchResult {
	ln := r.Lanes[i]
	out := r
	out.Lanes = nil
	out.Budget, out.Hits, out.Excluded = ln.Budget, ln.Hits, ln.Excluded
	out.Groups, out.Histogram = ln.Groups, ln.Histogram
	out.Truncated = ln.Cut()
	return out
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
	var src Source
	if store := b.store(); store != nil {
		src = storeSource{b: b, store: store}
	}
	return SearchSource(ctx, src, q)
}

// SearchSource is Search over any Source (source.go): a bundle's store, or
// another store of files that wants the same search.
func SearchSource(ctx context.Context, src Source, q Query) (SearchResult, error) {
	var res SearchResult
	if ctx == nil {
		ctx = context.Background()
	}
	patterns, err := Patterns(q.Pattern, q.Patterns)
	if err != nil {
		return res, err
	}
	f, err := NewFilter(patterns, q.Exclude, "(?i)")
	if err != nil {
		return res, err
	}
	if src == nil {
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
	// The files the path filter and the window leave in, settled before any
	// is read: the share needs to know how many there are, and the scan works
	// through a fixed list.
	windowed := !q.Since.IsZero() || !q.Until.IsZero()
	var todo []File
	for _, bf := range src.Files() {
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
			if (!q.Until.IsZero() && first.After(q.Until)) || (!q.Since.IsZero() && last.Before(q.Since)) {
				res.Skipped++
				continue
			}
		}
		todo = append(todo, bf)
	}
	// One lane per pattern, each with the limits a search for it alone
	// would have (lanes.go).
	lanes := make([]Lane[Hit], len(patterns))
	for l := range lanes {
		ln := &lanes[l]
		ln.Pattern = patterns[l]
		ln.Max, ln.PerFile = q.MaxHits, q.PerFile
		// The share exists to divide the limit between files; with one
		// file in scope there is nobody to divide it with.
		if len(todo) <= 1 {
			ln.PerFile = 0
		}
		// A whole-bundle answer counts every match and keeps none.
		if modes == 1 {
			ln.Max, ln.PerFile = 0, 0
		}
		if q.Group {
			ln.Groups = NewGrouper()
		}
		if q.OverTime {
			ln.Histogram = NewHistogram()
		}
	}
	switch {
	case modes == 1:
		q.Before, q.After, q.Records = 0, 0, false
	case q.Records && q.Before == 0 && q.After == 0:
		q.Before, q.After = RecordFallback, RecordFallback
	}
	fs := fileSearch{src: src, height: src.Height(), f: f, q: q, windowed: windowed, offer: lanes[0].Offer()}
	// Several chunks at once, of several files or of one large one, merged in
	// order (parallel.go). A file's chunks add up to the file: each lane's
	// hits up to its offer, its total, and then the lane's budget decides
	// what of it is kept, at the file's last chunk, as it did for a file
	// read whole.
	chunks := chunksOf(todo)
	var cur fileHits
	EachInOrder(ctx, len(chunks),
		func(ctx context.Context, i int) fileHits {
			c := chunks[i]
			return fs.scan(ctx, todo[c.file], c.from, c.to)
		},
		func(i int, r fileHits) bool {
			c := chunks[i]
			if c.first {
				// Kept all it may: the files not yet opened are counted, so
				// the reply can say they were never searched.
				if AllFull(lanes) {
					for l := range lanes {
						lanes[l].Unsearched = len(todo) - c.file
					}
					return false
				}
				if ctx.Err() != nil {
					return false
				}
				res.Scanned++
				cur = fileHits{lanes: make([]laneHits, len(lanes)), offered: make([]int, len(lanes))}
			}
			for l, rl := range r.lanes {
				cur.lanes[l].total += rl.total
				lanes[l].Excluded += rl.excluded
				lanes[l].Groups.Merge(rl.groups)
				lanes[l].Histogram.Merge(rl.hist)
			}
			for j, h := range r.hits {
				if m := TakeLanes(cur.offered, r.masks[j], fs.offer); m != 0 {
					cur.hits = append(cur.hits, h)
					cur.masks = append(cur.masks, m)
				}
			}
			if c.last {
				for l := range lanes {
					kept := lanes[l].File(todo[c.file].Path, cur.lanes[l].total, cur.offered[l])
					lanes[l].Hits = append(lanes[l].Hits, LaneItems(cur.hits, cur.masks, l, kept)...)
				}
			}
			return true
		})
	if err := ctx.Err(); err != nil {
		return res, err
	}
	if q.Records {
		records(ctx, src, lanes)
		if err := ctx.Err(); err != nil {
			return res, err
		}
	}
	for l := range lanes {
		if q.CountOnly {
			t := lanes[l].Tallies
			sort.SliceStable(t, func(i, j int) bool { return t[i].Matches > t[j].Matches })
		}
		res.Truncated = res.Truncated || lanes[l].Cut()
	}
	res.Lanes = lanes
	if len(lanes) == 1 {
		res = res.Lane(0)
	}
	return res, nil
}

// records gives every lane's kept hits their whole records (records.go),
// decrypting only the slices around them: a hit's RecordLines either side.
// Several lanes' hits in one file are found in one reading of it.
func records(ctx context.Context, src Source, lanes []Lane[Hit]) {
	targets, order := RecordTargets(lanes, func(h Hit) string { return h.Path }, func(h Hit) int { return h.Line })
	height := src.Height()
	index := map[string]File{}
	for _, bf := range src.Files() {
		index[bf.Path] = bf
	}
	found := map[string]map[int]*Record{}
	for _, p := range order {
		if ctx.Err() != nil {
			return
		}
		bf := index[p]
		if _, _, known := FileSpan(bf); !known {
			continue // no timestamps: no record can be told from the next
		}
		year := fileYear(bf)
		f := NewRecordFinder(targets[p],
			func(s string) bool { _, ok := ParseTime(bf.Format, year, s); return ok },
			// Copied: the line is a piece of its whole decrypted slice.
			func(s string) string { return strings.Clone(strings.TrimRight(s, "\r")) })
		slices := map[int][]string{}
		for _, sp := range RecordSpans(targets[p]) {
			for n := sp[0]; n <= sp[1] && n <= bf.Lines; n++ {
				si := (n - 1) / height
				lines, ok := slices[si]
				if !ok {
					lines = sourceLines(src, bf, si)
					slices[si] = lines
				}
				if j := (n - 1) % height; j < len(lines) {
					f.Line(n, lines[j])
				}
			}
		}
		found[p] = f.Records()
	}
	for l := range lanes {
		lanes[l].Hits = FoldRecords(lanes[l].Hits,
			func(h Hit) (*Record, bool) {
				recs, dated := found[h.Path]
				return recs[h.Line], dated
			},
			func(h *Hit, r *Record, v *RecordView) {
				h.Record = v
				if r == nil {
					return
				}
				h.Context = make([]string, len(r.Lines))
				for i, line := range r.Lines {
					h.Context[i] = numberLine(r.Start+i, line)
				}
			},
			func(h *Hit) *RecordView { return h.Record },
			func(h Hit) int { return h.Line })
	}
}

// fileSearch is what one file's scan needs from its search: the source and
// its slice height, the line test, the query, and how many hits a file may
// keep (Budget.Offer). Read-only, so scans of several files can share it at
// once.
type fileSearch struct {
	src      Source
	height   int
	f        Filter
	q        Query
	windowed bool
	offer    int
}

// fileHits is one file's part of a search, or one chunk's, before the merge
// decides how much of it the search keeps.
type fileHits struct {
	// hits are each lane's first offer matches, in order, with context:
	// kept once however many lanes they are offered to, masks[i] saying
	// which (lanes.go).
	hits    []Hit
	masks   []uint32
	lanes   []laneHits
	offered []int // per lane, hits offered so far: the merge's count for a whole file
}

// laneHits is one pattern's part of a file's search.
type laneHits struct {
	total, excluded int
	groups          *Grouper   // when the query groups
	hist            *Histogram // when it counts over time
}

// scan reads slices [from, to) of one file, collecting its hits with context. It keeps a rolling
// window of preceding lines rather than the whole file, and fills trailing
// context by continuing the scan past each hit. It reads the whole file even
// once nothing more will be kept, because the count is part of the answer:
// "25 kept of 340" needs the 340.
//
// Hits still owed trailing context are tracked by INDEX into r.hits, never by
// pointer: r.hits grows during the scan, and a pointer into it would be left
// addressing a stale backing array the moment append reallocates — trailing
// context would then be written to a copy nobody reads.
func (s fileSearch) scan(ctx context.Context, bf File, from, to int) fileHits {
	r := fileHits{lanes: make([]laneHits, s.f.Lanes()), offered: make([]int, s.f.Lanes())}
	for l := range r.lanes {
		if s.q.Group {
			r.lanes[l].groups = NewGrouper()
		}
		if s.q.OverTime {
			r.lanes[l].hist = NewHistogram()
		}
	}
	q := s.q
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
	// A chunk that does not start the file takes its leading context from
	// the slice before it, so a hit on a chunk's first line reads exactly as
	// it would have in the whole file. A slice is far longer than any
	// context asked for, so one slice back always covers it.
	if q.Before > 0 && from > 0 {
		lines := sourceLines(s.src, bf, from-1)
		base := (from-1)*s.height + 1
		start := len(lines) - q.Before
		if start < 0 {
			start = 0
		}
		for j := start; j < len(lines); j++ {
			prev = append(prev, seen{n: base + j, text: lines[j]})
		}
	}
	year := fileYear(bf)
	line := func(n int, text string) bool {
		// One file can be the whole search, so the merge's check is not
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
				r.hits[p.idx].Context = append(r.hits[p.idx].Context, numbered)
				if p.remaining--; p.remaining > 0 {
					kept = append(kept, p)
				}
			}
			pending = kept
		}

		mask := s.f.WhichString(text)
		if mask != 0 && s.windowed && !LineInWindow(bf.Format, year, text, q.Since, q.Until) {
			mask = 0
		}
		// Exclusion is tested last, on lines already in, so excluded counts
		// only what the search would otherwise have shown.
		if mask != 0 && s.f.ExcludesString(text) {
			for l := range r.lanes {
				if mask&(1<<l) != 0 {
					r.lanes[l].excluded++
				}
			}
			mask = 0
		}
		if mask != 0 {
			// A grouped search files the line under its shape, and an
			// over-time one under its time; neither keeps a hit. Dated from
			// the line itself, when the file's format carries times at all:
			// a line with none is counted as undated, never placed at a
			// guessed time.
			var when time.Time
			if (q.Group || q.OverTime) && bf.Format != FormatText {
				when, _ = ParseTime(bf.Format, year, text)
			}
			for l := range r.lanes {
				if mask&(1<<l) == 0 {
					continue
				}
				ln := &r.lanes[l]
				ln.total++
				if ln.groups != nil {
					ln.groups.Add(bf.Path, n, strings.TrimRight(text, "\r"), when)
				} else if ln.hist != nil {
					ln.hist.Add(when)
				}
			}
			if offered := TakeLanes(r.offered, mask, s.offer); offered != 0 {
				// Copied, not sliced: the line is a piece of its whole
				// decrypted slice, and a kept hit holding the piece would hold
				// the slice for as long as the reply lives.
				hit := Hit{Path: bf.Path, Line: n, Text: strings.Clone(strings.TrimRight(text, "\r"))}
				if q.Before > 0 || q.After > 0 {
					hit.Context = make([]string, 0, len(prev)+1+q.After)
					for _, p := range prev {
						hit.Context = append(hit.Context, numberLine(p.n, p.text))
					}
					hit.Context = append(hit.Context, numberLine(n, text))
				}
				r.hits = append(r.hits, hit)
				r.masks = append(r.masks, offered)
				if q.After > 0 {
					pending = append(pending, owed{idx: len(r.hits) - 1, remaining: q.After})
				}
			}
		}

		if q.Before > 0 {
			prev = append(prev, seen{n: n, text: text})
			if len(prev) > q.Before {
				prev = prev[1:]
			}
		}
		return true
	}
	for idx := from; idx < to && ctx.Err() == nil; idx++ {
		body := s.src.Slice(bf, idx)
		// A slice no pattern could match is passed over without being split
		// into lines, when nothing reaches across it: no hit above it still
		// owed trailing context, and no leading context to carry past it. In
		// a sparse search that is most of them.
		if body == "" || len(pending) == 0 && q.Before == 0 && !s.f.Possible(body) {
			continue
		}
		base := idx*s.height + 1
		stopped := false
		for i, text := range strings.Split(body, "\n") {
			if !line(base+i, text) {
				stopped = true
				break
			}
		}
		if stopped {
			break
		}
	}
	// Trailing context still owed at the chunk's end comes from the slice
	// after it, as it would have from the next lines of the whole file.
	if len(pending) > 0 && to < bf.Slices && ctx.Err() == nil {
		lines := sourceLines(s.src, bf, to)
		base := to*s.height + 1
		for j := 0; j < len(lines) && len(pending) > 0; j++ {
			numbered := numberLine(base+j, lines[j])
			kept := pending[:0]
			for _, p := range pending {
				r.hits[p.idx].Context = append(r.hits[p.idx].Context, numbered)
				if p.remaining--; p.remaining > 0 {
					kept = append(kept, p)
				}
			}
			pending = kept
		}
	}
	return r
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
	// The files the path filter and the window leave in, settled first: the
	// scan works through a fixed list.
	var todo []File
	for _, bf := range b.Index() {
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
		todo = append(todo, bf)
	}
	// Several chunks at once, of several files or of one large one
	// (parallel.go). Each keeps its own earliest Max and the merge offers
	// them on in order: the earliest Max overall are each among their own
	// chunk's earliest Max, and a chunk hands its lines on in time order
	// with ties in line order, so ties still break file first, then line, as
	// a single pass breaks them.
	keep := &earliest{max: q.Max}
	chunks := chunksOf(todo)
	EachInOrder(ctx, len(chunks),
		func(ctx context.Context, i int) fileTimeline {
			c := chunks[i]
			return timelineFile(ctx, store, todo[c.file], c.from, c.to, f, q)
		},
		func(i int, r fileTimeline) bool {
			if ctx.Err() != nil {
				return false
			}
			for _, e := range r.kept {
				keep.offer(e)
			}
			res.Total += r.total
			res.Undated += r.undated
			res.Excluded += r.excluded
			return true
		})
	if err := ctx.Err(); err != nil {
		return res, err
	}
	res.Entries = keep.sorted()
	return res, nil
}

// fileTimeline is one chunk's part of a timeline: its own earliest lines, in
// time order, and what it counted.
type fileTimeline struct {
	kept                     []TimelineEntry
	total, undated, excluded int
}

// timelineFile reads slices [from, to) of one file for a timeline, keeping
// their earliest q.Max lines in the window that the line filters let through.
func timelineFile(ctx context.Context, store Store, bf File, from, to int, f Filter, q TimelineQuery) fileTimeline {
	var r fileTimeline
	keep := &earliest{max: q.Max}
	year := fileYear(bf)
	scanSlices(store, bf, from, to, func(n int, text string) bool {
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
				r.undated++
			}
			return true
		}
		if (!q.Since.IsZero() && ts.Before(q.Since)) || (!q.Until.IsZero() && ts.After(q.Until)) {
			return true
		}
		if f.ExcludesString(text) {
			r.excluded++
			return true
		}
		r.total++
		keep.offer(TimelineEntry{When: ts, Path: bf.Path, Line: n, Text: text})
		return true
	})
	r.kept = keep.sorted()
	return r
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
