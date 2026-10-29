// A match's whole record.
//
// A log entry is often more than one line: a message and the stack trace under
// it, a wrapped SQL statement, a multi-line config dump. Its first line carries
// the timestamp and the rest carry none. Fixed context lines cut such an entry
// wherever the count runs out: ten lines of a forty-line trace, or a match
// on "Caused by" shown without the message that dated it. A record is the
// entry itself: from the dated line a match belongs to, down to the line
// before the next dated one.
//
// Records are found after a search, for the matches it kept, by reading just
// the lines around them again: the search's scan stays as it was, and the
// cost is the kept matches', not the bundle's.
package bundle

import (
	"sort"
	"strconv"
	"strings"
)

// RecordLines is how far a record is followed each way from its match: up to
// this many lines above to find its dated line, and this many below. A record
// longer than that is cut, and says so.
const RecordLines = 100

// Record is one log record as found around a match: Lines are the lines from
// Start on, as shown.
type Record struct {
	Start   int
	Lines   []string
	HeadCut bool // its dated line is further up than RecordLines: it begins above Start
	TailCut bool // it goes on past the last line shown
	tail    int  // lines collected below the match that opened it
}

// RecordView is what a match's reply says about its record, when the search
// asked for records.
type RecordView struct {
	First   int   `json:"first,omitempty"` // the first and last line shown
	Last    int   `json:"last,omitempty"`
	HeadCut bool  `json:"head_cut,omitempty"`
	TailCut bool  `json:"tail_cut,omitempty"`
	AlsoAt  []int `json:"also_at,omitempty"` // further matches inside this record, shown in it rather than again
	// Undated is a file with no timestamps, where one record cannot be
	// told from the next: the match keeps plain context lines.
	Undated bool `json:"undated,omitempty"`
}

// RecordFinder finds the records around target lines in one file, fed its
// lines in ascending order. Gaps are allowed (a bundle feeds only the slices
// near its targets), so long as each target's RecordLines either side are fed.
type RecordFinder struct {
	// Dated reports whether a line carries a timestamp of its own: a
	// record's first line. Show is a line as a reply shows it.
	Dated func(string) bool
	Show  func(string) string

	targets []int
	next    int
	ring    []ringLine // the last RecordLines lines fed, contiguous
	open    *Record    // the record still collecting its tail, if any
	found   map[int]*Record
}

type ringLine struct {
	n     int
	text  string
	dated int8 // 0 unknown, 1 yes, -1 no: dating is parsing, done only when needed
}

func (l *ringLine) isDated(f *RecordFinder) bool {
	if l.dated == 0 {
		l.dated = -1
		if f.Dated(l.text) {
			l.dated = 1
		}
	}
	return l.dated > 0
}

// NewRecordFinder finds the records of targets, line numbers in ascending
// order.
func NewRecordFinder(targets []int, dated func(string) bool, show func(string) string) *RecordFinder {
	return &RecordFinder{Dated: dated, Show: show, targets: targets, found: map[int]*Record{}}
}

// Line feeds line n.
func (f *RecordFinder) Line(n int, text string) {
	if len(f.ring) > 0 && f.ring[len(f.ring)-1].n != n-1 {
		f.ring = f.ring[:0]
		f.close(true)
	}
	cur := ringLine{n: n, text: text}
	// The open record takes this line, unless it begins the next record or
	// the record has run to its limit.
	if f.open != nil {
		switch {
		case cur.isDated(f):
			f.close(false)
		case f.open.tail >= RecordLines:
			f.close(true)
		default:
			f.open.Lines = append(f.open.Lines, f.Show(text))
			f.open.tail++
		}
	}
	for f.next < len(f.targets) && f.targets[f.next] < n {
		f.next++ // fed past without its line: a target outside the file
	}
	if f.next < len(f.targets) && f.targets[f.next] == n {
		for f.next < len(f.targets) && f.targets[f.next] == n {
			f.next++
		}
		if f.open != nil {
			// Inside the record a match above it opened: the same record.
			f.found[n] = f.open
		} else {
			f.found[n] = f.begin(&cur)
		}
	}
	f.ring = append(f.ring, cur)
	if len(f.ring) > RecordLines {
		f.ring = f.ring[1:]
	}
}

// begin opens the record holding cur: back through the ring to the dated line
// above it, or as far as the ring reaches.
func (f *RecordFinder) begin(cur *ringLine) *Record {
	r := &Record{Start: cur.n}
	if !cur.isDated(f) {
		k := -1
		for i := len(f.ring) - 1; i >= 0; i-- {
			if f.ring[i].isDated(f) {
				k = i
				break
			}
		}
		from := k
		if k < 0 {
			from = 0
			// No dated line within reach: the record begins further up,
			// unless what was reached is the file's first line.
			r.HeadCut = len(f.ring) == 0 && cur.n > 1 || len(f.ring) > 0 && f.ring[0].n > 1
		}
		for i := from; i < len(f.ring); i++ {
			r.Lines = append(r.Lines, f.Show(f.ring[i].text))
		}
		if len(f.ring) > 0 {
			r.Start = f.ring[from].n
		}
	}
	r.Lines = append(r.Lines, f.Show(cur.text))
	f.open = r
	return r
}

func (f *RecordFinder) close(cut bool) {
	if f.open != nil {
		f.open.TailCut = cut
		f.open = nil
	}
}

// Done reports whether every target's record is found and closed: a reader
// can stop.
func (f *RecordFinder) Done() bool { return f.next >= len(f.targets) && f.open == nil }

// Records is each target's record, after the last line is fed. A record still
// open ran to the end of what was fed, the file's end.
func (f *RecordFinder) Records() map[int]*Record {
	f.open = nil
	return f.found
}

// RecordSpans is the line ranges a reader must feed for targets' records,
// [from, to] inclusive and merged: each target's RecordLines either side,
// and one more below, to tell a record that ends at the limit from one cut
// by it.
func RecordSpans(targets []int) [][2]int {
	var out [][2]int
	for _, h := range targets {
		from, to := max(h-RecordLines, 1), h+RecordLines+1
		if n := len(out); n > 0 && from <= out[n-1][1]+1 {
			out[n-1][1] = max(out[n-1][1], to)
			continue
		}
		out = append(out, [2]int{from, to})
	}
	return out
}

// RecordTargets is the lines whose records a search must find, per file: every
// lane's matches, once each, in order. path and line read a match.
func RecordTargets[T any](lanes []Lane[T], path func(T) string, line func(T) int) (map[string][]int, []string) {
	byFile := map[string][]int{}
	var order []string
	for _, ln := range lanes {
		for _, h := range ln.Hits {
			p := path(h)
			if _, ok := byFile[p]; !ok {
				order = append(order, p)
			}
			byFile[p] = append(byFile[p], line(h))
		}
	}
	for p, lines := range byFile {
		byFile[p] = sortedUnique(lines)
	}
	return byFile, order
}

func sortedUnique(in []int) []int {
	out := append([]int(nil), in...)
	sort.Ints(out)
	n := 0
	for i, v := range out {
		if i == 0 || v != out[n-1] {
			out[n] = v
			n++
		}
	}
	return out[:n]
}

// FoldRecords gives each match its record, in a lane's list of matches: set
// fills one match from its record, and a later match inside a record already
// given is folded into it (listed in AlsoAt) rather than shown again. A match
// with no record (its file is undated) is marked so and keeps its context.
func FoldRecords[T any](hits []T, rec func(T) (*Record, bool), set func(*T, *Record, *RecordView), view func(*T) *RecordView, line func(T) int) []T {
	out := hits[:0:0]
	var last *Record
	for _, h := range hits {
		r, dated := rec(h)
		if !dated {
			set(&h, nil, &RecordView{Undated: true})
			out = append(out, h)
			last = nil
			continue
		}
		if r != nil && r == last {
			v := view(&out[len(out)-1])
			v.AlsoAt = append(v.AlsoAt, line(h))
			continue
		}
		if r != nil {
			set(&h, r, &RecordView{First: r.Start, Last: r.Start + len(r.Lines) - 1, HeadCut: r.HeadCut, TailCut: r.TailCut})
		}
		out = append(out, h)
		last = r
	}
	return out
}

// RecordNotes are the lines a reply puts around a record: where it was cut,
// and the other matches inside it.
func RecordNotes(v *RecordView, readTool string) (before, after string) {
	if v == nil {
		return "", ""
	}
	if v.Undated {
		return "", "(this file carries no timestamps, so one record cannot be told from the next: plain context lines shown)\n"
	}
	if v.HeadCut {
		before = "(the record begins more than " + strconv.Itoa(RecordLines) + " lines above: read with " + readTool + " before line " + strconv.Itoa(v.First) + " for its start)\n"
	}
	if v.TailCut {
		after += "(the record goes on past line " + strconv.Itoa(v.Last) + ": read with " + readTool + " for the rest)\n"
	}
	if len(v.AlsoAt) > 0 {
		parts := make([]string, len(v.AlsoAt))
		for i, n := range v.AlsoAt {
			parts[i] = strconv.Itoa(n)
		}
		after += "(also matched inside this record at line(s) " + strings.Join(parts, ", ") + ")\n"
	}
	return before, after
}
