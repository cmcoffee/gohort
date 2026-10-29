package filestore

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cmcoffee/gohort/core/bundle"
)

// A large file is read in byte-range chunks on several cores, and chunked must
// read exactly as whole. The folder is built for the edges: tracebacks whose
// undated lines take the time above them across a chunk boundary (in and out
// of a time window), lines longer than a chunk, a .gz (one chunk whatever its
// size), blank lines. Compared at chunk sizes from one byte, where most chunks
// hold no line of their own, up, on four workers, against whole files read on
// one, in every mode.
func TestChunkedFilesGiveTheWholeFileAnswer(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string, gz bool) {
		data := []byte(body)
		if gz {
			var b bytes.Buffer
			zw := gzip.NewWriter(&b)
			zw.Write(data)
			zw.Close()
			data = b.Bytes()
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for f := 0; f < 3; f++ {
		var b strings.Builder
		for i := 0; i < 60+f*25; i++ {
			fmt.Fprintf(&b, "2026-10-27T%02d:%02d:00Z %s request %d\n", 6+i/20, i%60, []string{"INFO", "ERROR", "WARN"}[(i+f)%3], i)
			switch i % 7 {
			case 2: // a traceback: undated lines that belong to the line above
				fmt.Fprintf(&b, "Traceback (most recent call last):\n  at frame %d\nOperationalError: gone away %d\n", i, i)
			case 4:
				fmt.Fprintf(&b, "ERROR %s tail %d\n", strings.Repeat("x", 90), i) // longer than a small chunk
			case 5:
				b.WriteString("\n")
			}
		}
		write(fmt.Sprintf("app-%d.log", f), b.String(), false)
	}
	write("rotated.log.gz", "2026-10-27T05:00:00Z ERROR in a gz\nOperationalError: gone away gz\n", true)

	prevW, prevC := bundle.SearchWorkers, chunkBytes
	defer func() { bundle.SearchWorkers, chunkBytes = prevW, prevC }()
	run := func(workers int, size int64, opts SearchOpts) SearchResult {
		bundle.SearchWorkers, chunkBytes = workers, size
		res, err := Search(context.Background(), root, opts)
		if err != nil {
			t.Fatalf("%+v: %v", opts, err)
		}
		return res
	}
	since := time.Date(2026, 10, 27, 7, 0, 0, 0, time.UTC)
	until := time.Date(2026, 10, 27, 8, 30, 0, 0, time.UTC)
	cases := map[string]SearchOpts{
		"context":       {Pattern: "error", IgnoreCase: true, Context: 3},
		"shares":        {Pattern: "ERROR", PerFile: 5, Context: 1},
		"limit-fill":    {Pattern: "request", Max: 40, PerFile: 25},
		"exclude":       {Pattern: "ERROR", Exclude: "tail", Context: 2},
		"count":         {Pattern: "gone away", CountOnly: true},
		"group":         {Pattern: ".", Group: true},
		"over-time":     {Pattern: "ERROR", OverTime: true, Bucket: 10 * time.Minute},
		"window":        {Pattern: "OperationalError|ERROR", Since: since, Until: until, Context: 2},
		"window-group":  {Pattern: "gone away", Since: since, Until: until, Group: true},
		"window-time":   {Pattern: "gone away|ERROR", Since: since, OverTime: true, Bucket: 10 * time.Minute},
		"window-count":  {Pattern: "frame", Since: since, CountOnly: true},
		"window-before": {Pattern: "gone away", Until: since, Context: 1},
	}
	for name, opts := range cases {
		whole := run(1, 1<<40, opts)
		for _, size := range []int64{1, 37, 300} {
			got := run(4, size, opts)
			label := fmt.Sprintf("%s, chunks of %d bytes", name, size)
			if !reflect.DeepEqual(whole.Matches, got.Matches) {
				for i := range whole.Matches {
					if i >= len(got.Matches) || !reflect.DeepEqual(whole.Matches[i], got.Matches[i]) {
						var other Match
						if i < len(got.Matches) {
							other = got.Matches[i]
						}
						t.Errorf("%s: match %d differs:\nwhole   %+v\nchunked %+v", label, i, whole.Matches[i], other)
						break
					}
				}
				if len(whole.Matches) != len(got.Matches) {
					t.Errorf("%s: %d matches whole, %d chunked", label, len(whole.Matches), len(got.Matches))
				}
			}
			if !reflect.DeepEqual(whole.Budget, got.Budget) {
				t.Errorf("%s: counts differ:\nwhole   %+v\nchunked %+v", label, whole.Budget, got.Budget)
			}
			if whole.Scanned != got.Scanned || whole.Excluded != got.Excluded || whole.Untimed != got.Untimed ||
				whole.LongLines != got.LongLines || !reflect.DeepEqual(whole.Unreadable, got.Unreadable) {
				t.Errorf("%s: scanned/excluded/untimed/long/unreadable differ: %d/%d/%d/%d/%v vs %d/%d/%d/%d/%v", label,
					whole.Scanned, whole.Excluded, whole.Untimed, whole.LongLines, whole.Unreadable,
					got.Scanned, got.Excluded, got.Untimed, got.LongLines, got.Unreadable)
			}
			if opts.Group && !reflect.DeepEqual(whole.Groups.Sorted(), got.Groups.Sorted()) {
				t.Errorf("%s: groups differ", label)
			}
			if opts.OverTime && whole.Histogram.Render("x", opts.Bucket) != got.Histogram.Render("x", opts.Bucket) {
				t.Errorf("%s: rows over time differ:\n%s\nvs\n%s", label, whole.Histogram.Render("x", opts.Bucket), got.Histogram.Render("x", opts.Bucket))
			}
		}
	}
	// The window cases have to have something to decide, or they prove
	// nothing: undated lines matched inside it.
	if res := run(1, 1<<40, cases["window-group"]); res.Total == 0 {
		t.Fatal("the window-group case matched nothing")
	}
}

// The chunk plan: a plain file over the size in ranges that cover it, a .gz
// and anything small whole.
func TestFolderChunksCoverEachFile(t *testing.T) {
	prev := chunkBytes
	defer func() { chunkBytes = prev }()
	chunkBytes = 10
	got := folderChunks([]LogFile{{Rel: "a", Size: 25}, {Rel: "b.gz", Size: 99, Gzipped: true}, {Rel: "c", Size: 10}, {Rel: "d", Size: 0}})
	want := []folderChunk{
		{file: 0, from: 0, to: 10, first: true}, {file: 0, from: 10, to: 20}, {file: 0, from: 20, to: 30, last: true},
		{file: 1, to: 99, first: true, last: true},
		{file: 2, to: 10, first: true, last: true},
		{file: 3, first: true, last: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("chunks:\n%+v\nwant\n%+v", got, want)
	}
}

// BenchmarkSearchOneLargeFile is one large log, the case chunking is for: at
// one worker and at the default.
func BenchmarkSearchOneLargeFile(b *testing.B) {
	dir := b.TempDir()
	var body strings.Builder
	for i := 0; i < 350000; i++ {
		fmt.Fprintf(&body, "2026-10-27T12:%02d:%02d.123Z INFO [worker-%d] request id=%08x handled path=/api/v1/files/%d status=200 bytes=%d dur=12ms\n",
			i%60, i%60, i%8, i*7919, i, i*13)
	}
	if err := os.WriteFile(filepath.Join(dir, "catalina.out"), []byte(body.String()), 0o644); err != nil {
		b.Fatal(err)
	}
	prev := bundle.SearchWorkers
	defer func() { bundle.SearchWorkers = prev }()
	for _, workers := range []int{1, prev} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			bundle.SearchWorkers = workers
			for i := 0; i < b.N; i++ {
				if _, err := Search(context.Background(), dir, SearchOpts{Pattern: "timeout|refused", IgnoreCase: true}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
