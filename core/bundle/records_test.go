package bundle

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// findRecords feeds lines (1-based) to a finder, dated being lines that start
// with "T", and returns each target's record as "start: lines" plus its cuts.
func findRecords(lines []string, targets []int, spans bool) map[int]string {
	f := NewRecordFinder(targets, func(s string) bool { return strings.HasPrefix(s, "T") }, func(s string) string { return s })
	if spans {
		for _, sp := range RecordSpans(targets) {
			for n := sp[0]; n <= sp[1] && n <= len(lines); n++ {
				f.Line(n, lines[n-1])
			}
		}
	} else {
		for n, l := range lines {
			if f.Done() {
				break
			}
			f.Line(n+1, l)
		}
	}
	out := map[int]string{}
	for n, r := range f.Records() {
		out[n] = fmt.Sprintf("%d: %s head=%v tail=%v", r.Start, strings.Join(r.Lines, ","), r.HeadCut, r.TailCut)
	}
	return out
}

func TestRecordsAreTheDatedLineAndItsContinuation(t *testing.T) {
	lines := []string{"pre", "T1", "a", "b", "T2", "T3", "c", "d", "e"}
	for _, spans := range []bool{false, true} {
		got := findRecords(lines, []int{1, 3, 4, 6, 8}, spans)
		want := map[int]string{
			1: "1: pre head=false tail=false", // above the first timestamp, from the file's start
			3: "2: T1,a,b head=false tail=false",
			4: "2: T1,a,b head=false tail=false", // the same record as 3
			6: "6: T3,c,d,e head=false tail=false",
			8: "6: T3,c,d,e head=false tail=false",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("spans=%v:\n got %v\nwant %v", spans, got, want)
		}
	}
}

// A record longer than RecordLines is cut on the side it runs past, and says
// so; one ending exactly at the limit is not cut.
func TestRecordsAreCutAtTheLimitAndSaySo(t *testing.T) {
	lines := []string{"T0"}
	for i := 0; i < RecordLines+50; i++ {
		lines = append(lines, fmt.Sprintf("u%d", i))
	}
	for _, spans := range []bool{false, true} {
		// A match deep in the trace: its dated line is out of reach above,
		// and the trace runs on below past the limit.
		got := findRecords(lines, []int{RecordLines + 10}, spans)[RecordLines+10]
		if !strings.HasPrefix(got, fmt.Sprintf("%d: ", 10)) || !strings.HasSuffix(got, "head=true tail=false") {
			t.Errorf("spans=%v: deep match: %.80s ... %s", spans, got, got[len(got)-25:])
		}
		got = findRecords(lines, []int{1}, spans)[1]
		if !strings.HasSuffix(got, "head=false tail=true") || strings.Count(got, ",") != RecordLines {
			t.Errorf("spans=%v: long record from its dated line: %d lines, %s", spans, strings.Count(got, ",")+1, got[len(got)-25:])
		}
	}
	exact := append([]string{"T0"}, make([]string, RecordLines)...)
	exact = append(exact, "T1")
	if got := findRecords(exact, []int{1}, false)[1]; !strings.HasSuffix(got, "tail=false") {
		t.Errorf("a record ending at the limit is not cut: %s", got[len(got)-25:])
	}
}

func TestRecordSpansMerge(t *testing.T) {
	got := RecordSpans([]int{5, 150, 600})
	want := [][2]int{{1, 251}, {500, 701}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("spans %v, want %v", got, want)
	}
}

// Matches inside one record are shown once, the later ones listed in it.
func TestFoldRecords(t *testing.T) {
	type hit struct {
		line int
		ctx  string
		v    *RecordView
	}
	a := &Record{Start: 2, Lines: []string{"T1", "a", "b"}}
	b := &Record{Start: 6, Lines: []string{"T3", "c"}, TailCut: true}
	recs := map[int]*Record{3: a, 4: a, 6: b}
	hits := []hit{{line: 3}, {line: 4}, {line: 6}}
	out := FoldRecords(hits,
		func(h hit) (*Record, bool) { return recs[h.line], true },
		func(h *hit, r *Record, v *RecordView) { h.v = v; h.ctx = strings.Join(r.Lines, ",") },
		func(h *hit) *RecordView { return h.v },
		func(h hit) int { return h.line })
	if len(out) != 2 || out[0].ctx != "T1,a,b" || !reflect.DeepEqual(out[0].v.AlsoAt, []int{4}) || !out[1].v.TailCut {
		t.Errorf("folded: %+v / %+v", out[0], out[1])
	}
	undated := FoldRecords(hits[:1],
		func(h hit) (*Record, bool) { return nil, false },
		func(h *hit, r *Record, v *RecordView) { h.v = v },
		func(h *hit) *RecordView { return h.v },
		func(h hit) int { return h.line })
	if !undated[0].v.Undated {
		t.Errorf("a match in an undated file should say so: %+v", undated[0])
	}
}

// A bundle search asked for records returns each hit's whole entry: a match
// deep in a stack trace comes with the dated line above it and the rest of
// the trace, even across a slice boundary; two matches in one trace are one
// record; a file with no timestamps keeps plain context and says why.
func TestBundleSearchReturnsWholeRecords(t *testing.T) {
	withMemStore(t)
	var b strings.Builder
	for i := 1; i <= SliceLines+300; i++ {
		fmt.Fprintf(&b, "2026-03-14T01:%02d:%02dZ INFO tick %d\n", (i/60)%60, i%60, i)
		if i == SliceLines-3 { // a trace that straddles the first slice boundary
			b.WriteString("2026-03-14T02:00:00Z ERROR request failed\njava.lang.IllegalStateException: boom\n\tat a.b(C.java:1)\n\tat d.e(F.java:2)\nCaused by: java.io.IOException: reset\n\tat g.h(I.java:3)\n\tat j.k(L.java:4)\n")
		}
	}
	stage := writeStage(t, map[string]string{
		"app.log":   b.String(),
		"notes.txt": "plain\ntext with Caused by: nothing\nno times here\n",
	})
	if _, err := Open("u1", "b1").Ingest(context.Background(), stage); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	prevW, prevC := SearchWorkers, ChunkSlices
	defer func() { SearchWorkers, ChunkSlices = prevW, prevC }()
	for _, chunked := range []bool{false, true} {
		SearchWorkers, ChunkSlices = 1, 1<<30
		if chunked {
			SearchWorkers, ChunkSlices = 4, 1
		}
		res, err := Open("u1", "b1").Search(context.Background(), Query{Pattern: `IOException|j\.k\(`, Records: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Hits) != 1 || res.Hits[0].Path != "app.log" {
			t.Fatalf("chunked=%v: want the one trace: %+v", chunked, res.Hits)
		}
		trace := res.Hits[0]
		first := SliceLines - 2
		if len(trace.Context) != 7 || trace.Context[0] != fmt.Sprintf("%d: 2026-03-14T02:00:00Z ERROR request failed", first) ||
			trace.Context[6] != fmt.Sprintf("%d: \tat j.k(L.java:4)", first+6) {
			t.Errorf("chunked=%v: the record should be the dated line and its whole trace:\n%s", chunked, strings.Join(trace.Context, "\n"))
		}
		if trace.Record == nil || !reflect.DeepEqual(trace.Record.AlsoAt, []int{first + 6}) || trace.Record.HeadCut || trace.Record.TailCut {
			t.Errorf("chunked=%v: the second match in the trace should fold into the first: %+v", chunked, trace.Record)
		}
	}
	res, _ := Open("u1", "b1").Search(context.Background(), Query{Pattern: "Caused by", Records: true})
	for _, h := range res.Hits {
		if h.Path == "notes.txt" && (h.Record == nil || !h.Record.Undated || len(h.Context) != 3) {
			t.Errorf("an undated file keeps context lines and says why: %+v", h)
		}
	}
}
