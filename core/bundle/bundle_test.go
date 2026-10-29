package bundle

// The store's behaviour, driven against an in-memory seam.
//
// Moved here with the code they cover. They used to live in servitor and swap
// its BundleFilesDB global; the store no longer knows what that is, and a test
// that reached for it would be testing the app's wiring rather than this
// package.

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// withMemStore points the store seam at an in-memory database for one test.
//
// It swaps OpenStore rather than a concrete database, which is the seam core
// assigns at init — so these tests exercise the same path production does, and
// a change that broke the wiring would fail here rather than only in an app.
func withMemStore(t testing.TB) {
	t.Helper()
	prev := OpenStore
	m := map[string]*memStore{}
	OpenStore = func(owner, id string) Store {
		k := owner + "/" + id
		if s, ok := m[k]; ok {
			return s
		}
		s := &memStore{tables: map[string]map[string][]byte{}}
		m[k] = s
		return s
	}
	t.Cleanup(func() { OpenStore = prev })
}

// memStore is a Store backed by a map, round-tripping values through gob the
// way the real one does. Gob rather than storing the value directly: kvlite
// serializes, and a test that handed back the same pointer would not notice a
// field the encoder drops.
type memStore struct {
	tables map[string]map[string][]byte
}

func (s *memStore) Set(table, key string, value interface{}) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(value); err != nil {
		panic("memStore encode: " + err.Error())
	}
	if s.tables[table] == nil {
		s.tables[table] = map[string][]byte{}
	}
	s.tables[table][key] = buf.Bytes()
}

func (s *memStore) Get(table, key string, out interface{}) bool {
	b, ok := s.tables[table][key]
	if !ok {
		return false
	}
	return gob.NewDecoder(bytes.NewReader(b)).Decode(out) == nil
}

func (s *memStore) Unset(table, key string) { delete(s.tables[table], key) }

func (s *memStore) Keys(table string) []string {
	out := make([]string, 0, len(s.tables[table]))
	for k := range s.tables[table] {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestNormBundlePathStripsTraversal — the index key does its own checking
// rather than trusting the unpack pass to have done it.
func TestNormBundlePathStripsTraversal(t *testing.T) {
	cases := map[string]string{
		"/var/log/messages":   "var/log/messages",
		"var/log/../messages": "var/messages",
		"../../etc/passwd":    "etc/passwd",
		`var\log\app.log`:     "var/log/app.log",
		"  a/b.log  ":         "a/b.log",
	}
	for in, want := range cases {
		if got := NormPath(in); got != want {
			t.Errorf("NormPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDetectBundleFormat — a format has to explain a MAJORITY of the sample, so
// one stamped banner at the top of a plain text file does not win.
func TestDetectBundleFormat(t *testing.T) {
	syslog := []string{
		"Mar 14 02:11:09 web01 sshd[1234]: Accepted publickey",
		"Mar 14 02:11:10 web01 kernel: [12345.6] eth0 link up",
		"Mar 14 02:11:11 web01 cron[99]: (root) CMD (run-parts)",
	}
	if got := DetectFormat(syslog); got != FormatSyslog {
		t.Errorf("syslog sample detected as %q", got)
	}
	iso := []string{
		"2026-03-14T02:11:09Z INFO starting",
		"2026-03-14T02:11:10Z ERROR failed to bind",
	}
	if got := DetectFormat(iso); got != FormatISO {
		t.Errorf("iso sample detected as %q", got)
	}
	// Slash-dated, as Go's log package writes it, with a zone name after.
	slashed := []string{
		"[2026/10/27 23:25:22 PDT] [http] GET /api/status 200 (14ms)",
		"[2026/10/27 23:25:23 PDT] [scheduler] firing job",
	}
	if got := DetectFormat(slashed); got != FormatISO {
		t.Errorf("slash-dated sample detected as %q", got)
	}
	clf := []string{
		`10.0.0.1 - - [14/Mar/2026:02:11:09 +0000] "GET / HTTP/1.1" 200 12`,
		`10.0.0.2 - - [14/Mar/2026:02:11:10 +0000] "GET /x HTTP/1.1" 404 3`,
	}
	if got := DetectFormat(clf); got != FormatCLF {
		t.Errorf("clf sample detected as %q", got)
	}
	jsonl := []string{
		`{"ts":"2026-03-14T02:11:09Z","level":"info","msg":"up"}`,
		`{"ts":"2026-03-14T02:11:10Z","level":"error","msg":"down"}`,
	}
	if got := DetectFormat(jsonl); got != FormatJSONL {
		t.Errorf("jsonl sample detected as %q", got)
	}
	// A README with one stamped line is text, not an ISO log.
	mostlyProse := []string{
		"Release notes for 4.2", "", "Built 2026-03-14T02:11:09Z by CI",
		"- fixed the scheduler", "- improved logging", "- see the manual",
	}
	if got := DetectFormat(mostlyProse); got != FormatText {
		t.Errorf("mostly-prose sample detected as %q, want text", got)
	}
	if got := DetectFormat(nil); got != FormatText {
		t.Errorf("empty sample detected as %q, want text", got)
	}
}

// TestParseBundleTime covers each format, including the year-less syslog case
// that depends on an externally supplied year.
func TestParseBundleTime(t *testing.T) {
	if ts, ok := ParseTime(FormatSyslog, 2026, "Mar 14 02:11:09 web01 sshd[1]: hi"); !ok {
		t.Error("syslog line did not parse")
	} else if ts.Format(time.RFC3339) != "2026-03-14T02:11:09Z" {
		t.Errorf("syslog parsed to %s", ts.Format(time.RFC3339))
	}
	// With no year available the line must NOT parse: a fabricated year is
	// worse than an unplaced line.
	if _, ok := ParseTime(FormatSyslog, 0, "Mar 14 02:11:09 web01 sshd[1]: hi"); ok {
		t.Error("syslog line parsed with no year supplied — the year would be invented")
	}
	if ts, ok := ParseTime(FormatCLF, 0, `1.2.3.4 - - [14/Mar/2026:02:11:09 +0000] "GET / HTTP/1.1" 200 1`); !ok {
		t.Error("clf line did not parse")
	} else if ts.Format(time.RFC3339) != "2026-03-14T02:11:09Z" {
		t.Errorf("clf parsed to %s", ts.Format(time.RFC3339))
	}
	if ts, ok := ParseTime(FormatISO, 0, "2026-03-14 02:11:09 ERROR x"); !ok {
		t.Error("iso line did not parse")
	} else if ts.Format(time.RFC3339) != "2026-03-14T02:11:09Z" {
		t.Errorf("iso parsed to %s", ts.Format(time.RFC3339))
	}
	// A zone name is read as no zone: the time as written, like a since
	// with no zone.
	if ts, ok := ParseTime(FormatISO, 0, "[2026/10/27 23:25:22.5 PDT] [http] GET / 200"); !ok {
		t.Error("slash-dated line did not parse")
	} else if ts.Format(time.RFC3339Nano) != "2026-10-27T23:25:22.5Z" {
		t.Errorf("slash-dated parsed to %s", ts.Format(time.RFC3339Nano))
	}
	if ts, ok := ParseTime(FormatISO, 0, "2026/10/27T23:25:22+02:00 x"); !ok || ts.Format(time.RFC3339) != "2026-10-27T21:25:22Z" {
		t.Errorf("slash-dated with an offset parsed to %s, %v", ts.Format(time.RFC3339), ok)
	}
	if _, ok := ParseTime(FormatText, 0, "no timestamp here"); ok {
		t.Error("a line with no timestamp reported one")
	}
}

// TestBundleLineInWindowKeepsUntimedLines — a stack trace's continuation lines
// carry no timestamp of their own, and dropping them would sever the trace from
// the message that introduced it.
func TestBundleLineInWindowKeepsUntimedLines(t *testing.T) {
	since := time.Date(2026, 3, 14, 2, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 14, 3, 0, 0, 0, time.UTC)
	if !LineInWindow(FormatISO, 0, "        at com.example.Foo.bar(Foo.java:42)", since, until) {
		t.Error("an untimed continuation line was excluded by the time window")
	}
	if LineInWindow(FormatISO, 0, "2026-03-14T09:00:00Z late", since, until) {
		t.Error("a line after the window was kept")
	}
	if !LineInWindow(FormatISO, 0, "2026-03-14T02:30:00Z inside", since, until) {
		t.Error("a line inside the window was dropped")
	}
}

// TestReadBundleLineTruncates — one pathological line must be cut and marked,
// not abandon the rest of the file the way a bufio.Scanner would.
func TestReadBundleLineTruncates(t *testing.T) {
	long := strings.Repeat("x", maxLineBytes*2)
	rd := bufio.NewReader(strings.NewReader(long + "\nsecond line\n"))
	first, err := readLine(rd)
	if err != nil {
		t.Fatalf("first line: %v", err)
	}
	if !strings.HasSuffix(first, truncMarker) {
		t.Error("the overlong line was not marked as truncated")
	}
	if len(first) > maxLineBytes+len(truncMarker) {
		t.Errorf("truncated line is %d bytes, over the cap", len(first))
	}
	second, _ := readLine(rd)
	if second != "second line" {
		t.Errorf("the line after an overlong one came back as %q — the read did not resynchronize", second)
	}
}

// writeStage lays out a staging directory from a name→content map.
func writeStage(t testing.TB, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const testSyslog = `Mar 14 02:11:09 web01 sshd[1234]: Accepted publickey for root
Mar 14 02:11:20 web01 scheduler[88]: INFO picked up job 4001
Mar 14 02:14:03 web01 scheduler[88]: ERROR connection refused talking to queue
Mar 14 02:14:03 web01 scheduler[88]: ERROR giving up on job 4002
Mar 14 02:15:00 web01 cron[99]: (root) CMD (run-parts /etc/cron.hourly)
`

const testAppLog = `2026-03-14T02:14:02Z WARN queue latency 4200ms
2026-03-14T02:14:03Z ERROR QueueUnavailable: connection refused
2026-03-14T02:14:03Z ERROR   at com.example.Queue.connect(Queue.java:88)
2026-03-14T02:14:09Z INFO retrying in 30s
`

// TestBundleIngestReadSearchTimeline is the end-to-end pass over the store:
// ingest a small dump, then exercise every read path the tools use.
func TestBundleIngestReadSearchTimeline(t *testing.T) {
	withMemStore(t)
	stage := writeStage(t, map[string]string{
		"var/log/messages":    testSyslog,
		"var/log/app/app.log": testAppLog,
		"var/lib/core.dump":   "\x00\x01\x02binary garbage\x00",
		"notes.txt":           "customer says it started around 02:14\n",
	})
	// The syslog year is taken from the file's mtime, so pin it.
	pinned := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(stage, "var/log/messages"), pinned, pinned); err != nil {
		t.Fatal(err)
	}

	st, err := Open("u1", "b1").Ingest(context.Background(), stage)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if st.TextFiles != 3 {
		t.Errorf("ingested %d text files, want 3", st.TextFiles)
	}
	if st.Binaries != 1 {
		t.Errorf("recorded %d binaries, want 1", st.Binaries)
	}
	if _, serr := os.Stat(stage); !os.IsNotExist(serr) {
		t.Error("the staged plaintext survived the ingest")
	}

	// --- index ---
	idx := Open("u1", "b1").Index()
	if len(idx) != 4 {
		t.Fatalf("index has %d entries, want 4", len(idx))
	}
	byPath := map[string]File{}
	for _, bf := range idx {
		byPath[bf.Path] = bf
	}
	sys := byPath["var/log/messages"]
	if sys.Format != FormatSyslog {
		t.Errorf("messages detected as %q, want syslog", sys.Format)
	}
	if sys.Host != "web01" {
		t.Errorf("messages host = %q, want web01", sys.Host)
	}
	if !sys.YearInferred {
		t.Error("a syslog span must be marked year-inferred")
	}
	if !strings.HasPrefix(sys.First, "2026-") {
		t.Errorf("messages First = %q, want the mtime year", sys.First)
	}
	if sys.Severity["ERROR"] != 2 {
		t.Errorf("messages ERROR count = %d, want 2", sys.Severity["ERROR"])
	}
	if got := byPath["var/lib/core.dump"].Format; got != FormatBinary {
		t.Errorf("core.dump format = %q, want binary", got)
	}

	// --- range read ---
	lines, bf, err := Open("u1", "b1").ReadRange("var/log/app/app.log", 2, 3)
	if err != nil {
		t.Fatalf("range read: %v", err)
	}
	if bf.Lines != 4 {
		t.Errorf("app.log has %d lines, want 4", bf.Lines)
	}
	if len(lines) != 2 || !strings.Contains(lines[0], "QueueUnavailable") {
		t.Errorf("range read returned %q", lines)
	}
	if _, _, err := Open("u1", "b1").ReadRange("var/log/app/app.log", 99, 0); err == nil {
		t.Error("a start line past the end of the file should be an error, not an empty result")
	}

	// --- search, with trailing context ---
	res, err := Open("u1", "b1").Search(context.Background(), Query{Pattern: "connection refused", After: 1})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Hits) != 2 {
		t.Fatalf("search found %d hits, want 2 (one per log)", len(res.Hits))
	}
	for _, h := range res.Hits {
		if len(h.Context) != 2 {
			t.Errorf("%s:%d has %d context lines, want 2 (the hit plus one after)", h.Path, h.Line, len(h.Context))
		}
	}
	// A glob narrows to one file.
	res, _ = Open("u1", "b1").Search(context.Background(), Query{Pattern: "connection refused", Glob: "*.log"})
	if len(res.Hits) != 1 {
		t.Errorf("globbed search found %d hits, want 1", len(res.Hits))
	}
	// A bad regex is an error the caller can act on, not zero results.
	if _, err := Open("u1", "b1").Search(context.Background(), Query{Pattern: "("}); err == nil {
		t.Error("an invalid regular expression was accepted")
	}

	// --- timeline: merges both logs, names the file it cannot place ---
	tl, err := Open("u1", "b1").Timeline(context.Background(), TimelineQuery{
		Since: time.Date(2026, 3, 14, 2, 14, 0, 0, time.UTC),
		Until: time.Date(2026, 3, 14, 2, 15, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	entries, unmergeable := tl.Entries, tl.Unmergeable
	if len(entries) == 0 {
		t.Fatal("timeline is empty")
	}
	for i := 1; i < len(entries); i++ {
		if entries[i].When.Before(entries[i-1].When) {
			t.Fatalf("timeline is out of order at %d", i)
		}
	}
	seenPaths := map[string]bool{}
	for _, e := range entries {
		seenPaths[e.Path] = true
	}
	if len(seenPaths) < 2 {
		t.Errorf("timeline drew from %d files, want both logs", len(seenPaths))
	}
	if len(unmergeable) == 0 {
		t.Error("notes.txt carries no timestamps and must be reported as unmergeable, not dropped silently")
	}
}

// TestSearchContextSurvivesHitGrowth is the regression for collecting trailing
// context by index rather than by pointer: the hit slice reallocates as it
// grows, and a pointer into it would leave context written to a stale array.
func TestSearchContextSurvivesHitGrowth(t *testing.T) {
	withMemStore(t)
	var body strings.Builder
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&body, "2026-03-14T02:%02d:00Z ERROR failure number %d\n", i, i)
		fmt.Fprintf(&body, "2026-03-14T02:%02d:01Z INFO trailing detail %d\n", i, i)
	}
	stage := writeStage(t, map[string]string{"app.log": body.String()})
	if _, err := Open("u1", "b1").Ingest(context.Background(), stage); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	res, err := Open("u1", "b1").Search(context.Background(), Query{Pattern: "failure number", After: 1})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Hits) != 50 {
		t.Fatalf("found %d hits, want 50", len(res.Hits))
	}
	for i, h := range res.Hits {
		if len(h.Context) != 2 {
			t.Fatalf("hit %d has %d context lines, want 2 — trailing context was written to a stale array", i, len(h.Context))
		}
		if !strings.Contains(h.Context[1], "trailing detail") {
			t.Fatalf("hit %d trailing context = %q", i, h.Context[1])
		}
	}
}

// Leading context is held as line number and text and only formatted when a
// hit shows it, so the exact "N: text" a hit carries is pinned here: a hit at
// the top of a file with fewer lines above it than asked for, two hits close
// enough to share context lines, and context that crosses a slice boundary.
func TestSearchContextIsExact(t *testing.T) {
	withMemStore(t)
	var body strings.Builder
	for i := 1; i <= SliceLines+2; i++ {
		switch i {
		case 1, 4, 5, SliceLines + 1:
			fmt.Fprintf(&body, "hit %d\n", i)
		default:
			fmt.Fprintf(&body, "plain %d\n", i)
		}
	}
	stage := writeStage(t, map[string]string{"app.log": body.String()})
	if _, err := Open("u1", "b1").Ingest(context.Background(), stage); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	res, err := Open("u1", "b1").Search(context.Background(), Query{Pattern: "^hit", Before: 2, After: 1})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	last := SliceLines + 1
	want := [][]string{
		{"1: hit 1", "2: plain 2"},
		{"2: plain 2", "3: plain 3", "4: hit 4", "5: hit 5"},
		{"3: plain 3", "4: hit 4", "5: hit 5", "6: plain 6"},
		{fmt.Sprintf("%d: plain %d", last-2, last-2), fmt.Sprintf("%d: plain %d", last-1, last-1),
			fmt.Sprintf("%d: hit %d", last, last), fmt.Sprintf("%d: plain %d", last+1, last+1)},
	}
	if len(res.Hits) != len(want) {
		t.Fatalf("found %d hits, want %d", len(res.Hits), len(want))
	}
	for i, h := range res.Hits {
		if strings.Join(h.Context, "|") != strings.Join(want[i], "|") {
			t.Errorf("hit %d (line %d) context = %q, want %q", i, h.Line, h.Context, want[i])
		}
	}
}

// TestExpandTarGzAndRefuseTraversal — a normal member is extracted under the
// archive's own directory; a traversing member is refused.
func TestExpandTarGzAndRefuseTraversal(t *testing.T) {
	withMemStore(t)
	stage := t.TempDir()
	archive := filepath.Join(stage, "dump.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	tw := tar.NewWriter(zw)
	add := func(name, body string) {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0600, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	add("var/log/inside.log", testAppLog)
	add("../../escaped.log", "should never be written\n")
	tw.Close()
	zw.Close()
	f.Close()

	st, err := Open("u1", "b2").Ingest(context.Background(), stage)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if st.Skipped == 0 {
		t.Error("the traversing member was not refused")
	}
	var paths []string
	for _, bf := range Open("u1", "b2").Index() {
		paths = append(paths, bf.Path)
		if strings.Contains(bf.Path, "escaped") {
			t.Errorf("a traversing member was ingested as %q", bf.Path)
		}
	}
	found := false
	for _, p := range paths {
		if strings.HasSuffix(p, "var/log/inside.log") {
			found = true
		}
	}
	if !found {
		t.Errorf("the archive's real member was not ingested; got %v", paths)
	}
}

// TestUnopenedArchiveIsRecordedNotDropped — a format with no built-in expander
// must show up in the listing as present-but-unread. A bundle that silently
// omits an encrypted blob looks like a bundle that simply did not contain it.
func TestUnopenedArchiveIsRecordedNotDropped(t *testing.T) {
	withMemStore(t)
	stage := writeStage(t, map[string]string{
		"payload.tar.xz": "not really xz, but the extension is what dispatches",
		"readme.txt":     "hello\n",
	})
	st, err := Open("u1", "b3").Ingest(context.Background(), stage)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if st.Unopened != 1 {
		t.Errorf("Unopened = %d, want 1", st.Unopened)
	}
	var seen bool
	for _, bf := range Open("u1", "b3").Index() {
		if bf.Path == "payload.tar.xz" {
			seen = true
			if bf.Format != FormatArchive {
				t.Errorf("payload.tar.xz format = %q, want archive", bf.Format)
			}
		}
	}
	if !seen {
		t.Error("the unopened archive is missing from the index entirely")
	}
	// Whether the SUMMARY mentions it is the renderer's promise, not the
	// store's, and the renderer lives with the tools. Asserted there.
}

// TestBundleSlicingRoundTrip pushes past one slice boundary so the range read
// has to stitch two slices together.
func TestBundleSlicingRoundTrip(t *testing.T) {
	withMemStore(t)
	var body strings.Builder
	total := SliceLines*2 + 37
	for i := 1; i <= total; i++ {
		fmt.Fprintf(&body, "line %d\n", i)
	}
	stage := writeStage(t, map[string]string{"big.log": body.String()})
	if _, err := Open("u1", "b4").Ingest(context.Background(), stage); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	bf, ok := Open("u1", "b4").Lookup("big.log")
	if !ok {
		t.Fatal("big.log missing from the index")
	}
	if bf.Lines != total {
		t.Errorf("Lines = %d, want %d", bf.Lines, total)
	}
	if bf.Slices != 3 {
		t.Errorf("Slices = %d, want 3", bf.Slices)
	}
	// A range straddling the first boundary.
	lines, _, err := Open("u1", "b4").ReadRange("big.log", SliceLines-1, SliceLines+2)
	if err != nil {
		t.Fatalf("straddling read: %v", err)
	}
	want := []string{
		fmt.Sprintf("line %d", SliceLines-1),
		fmt.Sprintf("line %d", SliceLines),
		fmt.Sprintf("line %d", SliceLines+1),
		fmt.Sprintf("line %d", SliceLines+2),
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d", len(lines), len(want))
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
	// The very last line, in the short final slice.
	lines, _, err = Open("u1", "b4").ReadRange("big.log", total, total)
	if err != nil {
		t.Fatalf("last-line read: %v", err)
	}
	if len(lines) != 1 || lines[0] != fmt.Sprintf("line %d", total) {
		t.Errorf("last line = %q", lines)
	}
}

// A search over a multi-gigabyte dump is the longest thing this package does,
// and the agent loop can only notice a cancel between rounds — so a search that
// ignored its context was a Stop the user could press and watch do nothing.
func TestBundleSearchStopsWhenTheRunIsCancelled(t *testing.T) {
	withMemStore(t)
	stage := writeStage(t, map[string]string{
		"var/log/messages":    testSyslog,
		"var/log/app/app.log": testAppLog,
	})
	if _, err := Open("u1", "b1").Ingest(context.Background(), stage); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := Open("u1", "b1").Search(ctx, Query{Pattern: "connection refused"})
	if err == nil {
		t.Fatalf("a cancelled search must report the cancellation, got %d hit(s) and no error", len(res.Hits))
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}

	// And the same search on a live context still works — the guard must not
	// have turned every search into a refusal.
	if res, err := Open("u1", "b1").Search(context.Background(), Query{Pattern: "connection refused"}); err != nil || len(res.Hits) == 0 {
		t.Errorf("an uncancelled search should still find things: %d hit(s), err=%v", len(res.Hits), err)
	}

	// A nil context is the pre-context callers' shape and must scan, not panic.
	if _, err := Open("u1", "b1").Search(nil, Query{Pattern: "connection refused"}); err != nil {
		t.Errorf("a nil context should read as uncancellable, got %v", err)
	}
}

// IngestFrom reads a folder somebody else owns: it must leave every file where
// it was, unpack nothing there, and replace the index rather than add to it.
func TestIngestFromLeavesTheFolderAsItWas(t *testing.T) {
	withMemStore(t)
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "var", "log"), 0o755)
	os.WriteFile(filepath.Join(dir, "var", "log", "app.log"), []byte("2026-03-14T02:00:00Z ERROR scheduler stalled\n2026-03-14T02:00:05Z INFO retry\n"), 0o644)
	var zbuf bytes.Buffer
	gz := gzip.NewWriter(&zbuf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "inner.log", Mode: 0o644, Size: 4})
	tw.Write([]byte("hi\n\n"))
	tw.Close()
	gz.Close()
	os.WriteFile(filepath.Join(dir, "more.tar.gz"), zbuf.Bytes(), 0o644)
	before := treeListing(t, dir)

	b := Open("store:cases", "case-1")
	st, err := b.IngestFrom(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if after := treeListing(t, dir); after != before {
		t.Errorf("the folder changed:\nbefore %s\nafter  %s", before, after)
	}
	if st.TextFiles != 1 || st.Unopened != 1 || st.Truncated {
		t.Errorf("stats %+v: want one text file and the packed archive counted unopened", st)
	}
	if bf, ok := b.Lookup("more.tar.gz"); !ok || bf.Format != FormatArchive {
		t.Errorf("the archive should be listed as unopened, got %+v %v", bf, ok)
	}
	if res, _ := b.Search(context.Background(), Query{Pattern: "scheduler stalled"}); len(res.Hits) != 1 {
		t.Errorf("the indexed log should be searchable, got %+v", res)
	}

	os.Remove(filepath.Join(dir, "more.tar.gz"))
	if _, err := b.IngestFrom(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.Lookup("more.tar.gz"); ok || b.FileCount() != 1 {
		t.Errorf("a second pass should replace the index, not add to it: %d files", b.FileCount())
	}
}

func TestIngestTreeStopsAtItsBudgetAndSaysSo(t *testing.T) {
	withMemStore(t)
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d.log", i)), []byte(strings.Repeat("x\n", 50)), 0o644)
	}
	b := Open("store:cases", "case-2")
	var st IngestStats
	if err := ingestTree(context.Background(), b.store(), dir, treeOpts{budget: 150, inPlace: true}, &st); err != nil {
		t.Fatal(err)
	}
	if !st.Truncated || st.Files != 1 {
		t.Errorf("want one file indexed and Truncated set, got %+v", st)
	}
}

// treeListing is every path under dir with its size, in order.
func treeListing(t *testing.T, dir string) string {
	t.Helper()
	var out []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, _ := d.Info()
		rel, _ := filepath.Rel(dir, p)
		out = append(out, fmt.Sprintf("%s:%d", rel, info.Size()))
		return nil
	})
	sort.Strings(out)
	return strings.Join(out, " ")
}

// A bundle's files come back out as their stored lines, across slice
// boundaries, and a binary has none to give.
func TestABundleFileExportsItsLines(t *testing.T) {
	withMemStore(t)
	var body strings.Builder
	for i := 1; i <= SliceLines+5; i++ {
		fmt.Fprintf(&body, "line %d\n", i)
	}
	stage := writeStage(t, map[string]string{
		"big.log":   body.String(),
		"core.dump": "\x00\x01\x02binary garbage\x00",
	})
	b := Open("u1", "b5")
	if _, err := b.Ingest(context.Background(), stage); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	var out bytes.Buffer
	if ok, err := b.Export("big.log", &out); !ok || err != nil {
		t.Fatalf("export: %v %v", ok, err)
	}
	if out.String() != body.String() {
		t.Errorf("the exported file differs from what was ingested: %d bytes vs %d", out.Len(), body.Len())
	}
	out.Reset()
	if ok, err := b.Export("core.dump", &out); ok || err != nil || out.Len() != 0 {
		t.Errorf("a binary has no stored lines to export: %v %v %d", ok, err, out.Len())
	}
	if _, err := b.Export("missing.log", &out); err == nil {
		t.Error("a file not in the bundle was exported")
	}
}

// benchLogLines is a typical application log: a timestamp, a level, and a
// request line, none of which contains the searched-for text, so the benchmark
// measures the cost of looking at every line rather than of reporting hits.
func benchLogLines(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "2026-10-27T12:%02d:%02d.123Z INFO [worker-%d] request id=%08x handled path=/api/v1/files/%d status=200 bytes=%d dur=12ms\n",
			i%60, i%60, i%8, i*7919, i, i*13)
	}
	return b.String()
}

// BenchmarkBundleSearch is a search over 100k lines that finds nothing, the
// shape of most investigation queries: plain, then with context asked for.
func BenchmarkBundleSearch(b *testing.B) {
	withMemStore(b)
	stage := writeStage(b, map[string]string{"app.log": benchLogLines(100000)})
	if _, err := Open("u1", "bench").Ingest(context.Background(), stage); err != nil {
		b.Fatalf("ingest: %v", err)
	}
	for _, q := range []Query{
		{Pattern: "connection reset"},
		{Pattern: "connection reset", Before: 2, After: 2},
		{Pattern: `reset by peer \d+`},
		{Pattern: ".", Group: true},
	} {
		b.Run(fmt.Sprintf("%s/ctx=%d/group=%v", q.Pattern, q.Before, q.Group), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				if _, err := Open("u1", "bench").Search(context.Background(), q); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// Bundle search excludes the same way: case-insensitive like its pattern,
// counted, and still visible as context around a kept hit.
func TestBundleSearchExclude(t *testing.T) {
	withMemStore(t)
	stage := writeStage(t, map[string]string{"app.log": "ERROR db timeout\n" +
		"ERROR GET /HealthCheck failed\n" +
		"ERROR disk full\n"})
	if _, err := Open("u1", "b1").Ingest(context.Background(), stage); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	res, err := Open("u1", "b1").Search(context.Background(), Query{Pattern: "error", Exclude: "healthcheck", After: 1})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Hits) != 2 || res.Hits[0].Line != 1 || res.Hits[1].Line != 3 || res.Excluded != 1 {
		t.Fatalf("hits %+v excluded %d; want lines 1 and 3, 1 excluded", res.Hits, res.Excluded)
	}
	if got := res.Hits[0].Context; len(got) != 2 || got[1] != "2: ERROR GET /HealthCheck failed" {
		t.Errorf("an excluded line should still show as context: %q", got)
	}
	if _, err := Open("u1", "b1").Search(context.Background(), Query{Pattern: "error", Exclude: "("}); err == nil || !strings.Contains(err.Error(), "exclude") {
		t.Errorf("a bad exclude should be named as the problem, got %v", err)
	}
}

// A grouped bundle search reads every file, keeps no hits, and hands back the
// shapes with their counts and spans; exclude and the path filter still apply.
func TestBundleSearchGroups(t *testing.T) {
	withMemStore(t)
	stage := writeStage(t, map[string]string{
		"app.log": "2026-03-14T02:14:02Z ERROR timeout after 3000ms\n" +
			"2026-03-14T02:14:05Z ERROR timeout after 120ms\n" +
			"2026-03-14T02:15:00Z INFO healthcheck ok\n",
		"db.log": "2026-03-14T02:20:00Z ERROR timeout after 9ms\n" +
			"2026-03-14T02:21:00Z ERROR disk full\n",
	})
	if _, err := Open("u1", "b1").Ingest(context.Background(), stage); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	res, err := Open("u1", "b1").Search(context.Background(), Query{Pattern: ".", Exclude: "healthcheck", Group: true, MaxHits: 1})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Hits) != 0 || res.Groups == nil || res.Truncated {
		t.Fatalf("a grouped search should keep no hits and never truncate: hits %d groups %v truncated %v", len(res.Hits), res.Groups, res.Truncated)
	}
	all := res.Groups.Sorted()
	if len(all) != 2 || res.Groups.Lines != 4 || res.Excluded != 1 {
		t.Fatalf("groups %+v lines %d excluded %d", all, res.Groups.Lines, res.Excluded)
	}
	top := all[0]
	if top.Shape != "<*> ERROR timeout after <*>" || top.Count != 3 || top.Files != 2 {
		t.Errorf("top = %q x%d in %d files", top.Shape, top.Count, top.Files)
	}
	if want := time.Date(2026, 3, 14, 2, 14, 2, 0, time.UTC); !top.First.Equal(want) {
		t.Errorf("first = %s, want %s", top.First, want)
	}
	res, _ = Open("u1", "b1").Search(context.Background(), Query{Pattern: "error", Glob: "db.log", Group: true})
	if res.Groups.Lines != 2 {
		t.Errorf("the path filter did not narrow a grouped search: %d lines", res.Groups.Lines)
	}
}

// A timeline filtered by pattern follows one thing through every file in time
// order: it says how many matching lines had no timestamp to place them by, and
// how many exclude took out, rather than leaving them off without a word.
func TestTimelineFollowsAPattern(t *testing.T) {
	withMemStore(t)
	stage := writeStage(t, map[string]string{
		"api.log": "2026-03-14T02:14:01Z GET /order req=7f3a\n" +
			"2026-03-14T02:14:02Z GET /order req=9b11\n" +
			"2026-03-14T02:14:09Z 500 /order req=7f3a\n",
		"db.log": "2026-03-14T02:14:05Z slow query req=7f3a 4200ms\n" +
			"2026-03-14T02:14:06Z healthcheck req=7f3a\n" +
			"2026-03-14T02:14:07Z ERROR deadlock req=7f3a\n" +
			"   at db.Exec req=7f3a\n",
	})
	if _, err := Open("u1", "b1").Ingest(context.Background(), stage); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	res, err := Open("u1", "b1").Timeline(context.Background(), TimelineQuery{Pattern: "req=7F3A", Exclude: "healthcheck"})
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	var got []string
	for _, e := range res.Entries {
		got = append(got, fmt.Sprintf("%s:%d", e.Path, e.Line))
	}
	if want := "[api.log:1 db.log:1 db.log:3 api.log:3]"; fmt.Sprint(got) != want {
		t.Errorf("timeline = %v, want %s", got, want)
	}
	if res.Total != 4 || res.Excluded != 1 || res.Undated != 1 {
		t.Errorf("total %d excluded %d undated %d; want 4, 1, 1", res.Total, res.Excluded, res.Undated)
	}
	if _, err := Open("u1", "b1").Timeline(context.Background(), TimelineQuery{Pattern: "("}); err == nil {
		t.Error("a bad pattern was accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Open("u1", "b1").Timeline(ctx, TimelineQuery{}); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled timeline returned %v", err)
	}
}

// The timeline keeps only the earliest Max lines while it reads. Whatever it
// keeps must be exactly what holding every line, stable-sorting by time and
// cutting at Max gave: files out of time order, lines out of order within a
// file, and ties between files that have to come out in file-then-line order.
func TestTimelineKeepsExactlyTheEarliest(t *testing.T) {
	withMemStore(t)
	files := map[string]string{}
	type line struct {
		path string
		n    int
		when time.Time
	}
	var all []line
	base := time.Date(2026, 3, 14, 2, 0, 0, 0, time.UTC)
	for f, name := range []string{"a.log", "b.log", "c.log"} {
		var body strings.Builder
		for i := 0; i < 400; i++ {
			// Wraps around, so every file revisits the same seconds: ties.
			when := base.Add(time.Duration((i*37+f*11)%97) * time.Second)
			fmt.Fprintf(&body, "%s event %d\n", when.Format(time.RFC3339), i)
			all = append(all, line{name, i + 1, when})
		}
		files[name] = body.String()
	}
	stage := writeStage(t, files)
	if _, err := Open("u1", "b1").Ingest(context.Background(), stage); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].when.Before(all[j].when) })
	for _, max := range []int{1, 7, 300, 2000} {
		res, err := Open("u1", "b1").Timeline(context.Background(), TimelineQuery{Max: max})
		if err != nil {
			t.Fatalf("timeline: %v", err)
		}
		want := all
		if len(want) > max {
			want = want[:max]
		}
		if len(res.Entries) != len(want) || res.Total != len(all) {
			t.Fatalf("max %d: %d entries of total %d, want %d of %d", max, len(res.Entries), res.Total, len(want), len(all))
		}
		for i, e := range res.Entries {
			if e.Path != want[i].path || e.Line != want[i].n || !e.When.Equal(want[i].when) {
				t.Fatalf("max %d: entry %d = %s:%d %s, want %s:%d %s", max, i, e.Path, e.Line, e.When, want[i].path, want[i].n, want[i].when)
			}
		}
	}
}

// An over-time bundle search reads every file, keeps no hits, counts each
// matching line by its own timestamp, and counts the undated ones apart.
func TestBundleSearchOverTime(t *testing.T) {
	withMemStore(t)
	stage := writeStage(t, map[string]string{
		"app.log": "2026-03-14T02:14:02Z ERROR timeout after 3000ms\n" +
			"2026-03-14T02:14:40Z ERROR timeout after 120ms\n" +
			"  timeout detail with no time of its own\n" +
			"2026-03-14T02:16:00Z INFO all fine\n",
		"db.log": "2026-03-14T02:16:30Z ERROR timeout after 9ms\n",
	})
	if _, err := Open("u1", "b1").Ingest(context.Background(), stage); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	res, err := Open("u1", "b1").Search(context.Background(), Query{Pattern: "timeout", OverTime: true, Bucket: time.Minute, MaxHits: 1})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Hits) != 0 || res.Truncated || res.Histogram == nil {
		t.Fatalf("hits %d truncated %v histogram %v", len(res.Hits), res.Truncated, res.Histogram)
	}
	if res.Histogram.Lines != 3 || res.Histogram.Undated != 1 {
		t.Errorf("lines %d undated %d; want 3 and 1", res.Histogram.Lines, res.Histogram.Undated)
	}
	out := res.Histogram.Render(`"timeout"`, time.Minute)
	for _, want := range []string{"02:14 2  ", "02:15 0", "02:16 1  "} {
		if !strings.Contains(out, want) {
			t.Errorf("missing row %q:\n%s", want, out)
		}
	}
	if _, err := Open("u1", "b1").Search(context.Background(), Query{Pattern: "x", OverTime: true, Group: true}); err == nil {
		t.Error("group and over_time together should be refused")
	}
}

// The bundle search shares its limit the way the folder search does: a noisy
// file keeps only its share, every match is still counted so the reply can
// say "of N", a limit reached says which files were never opened, and a count
// reads every file and keeps nothing.
func TestBundleSearchSharesItsLimitAndCountsPastIt(t *testing.T) {
	withMemStore(t)
	stage := writeStage(t, map[string]string{
		"a_noisy.log": strings.Repeat("ERROR retry\n", 300),
		"b_quiet.log": "ERROR disk full\nERROR disk full\n",
		"c_more.log":  strings.Repeat("ERROR later\n", 50),
		"d_last.log":  "ERROR never opened\n",
	})
	if _, err := Open("u1", "b1").Ingest(context.Background(), stage); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	ctx := context.Background()

	res, err := Open("u1", "b1").Search(ctx, Query{Pattern: "error", PerFile: 10})
	if err != nil {
		t.Fatal(err)
	}
	quiet := 0
	for _, h := range res.Hits {
		if h.Path == "b_quiet.log" {
			quiet++
		}
	}
	if quiet != 2 || res.Kept != 10+2+10+1 || res.Total != 353 {
		t.Errorf("quiet kept %d, kept %d of %d; want 2, 23 of 353", quiet, res.Kept, res.Total)
	}
	if len(res.CutInFiles) != 2 || res.CutInFiles[0].File != "a_noisy.log" || res.CutInFiles[0].Matches != 300 || !res.Truncated {
		t.Errorf("the cut files should be named with their full counts: %+v truncated=%v", res.CutInFiles, res.Truncated)
	}

	// The limit reached inside c_more.log: that file is still counted to its
	// end, and d_last.log is never opened, and said to be.
	res, _ = Open("u1", "b1").Search(ctx, Query{Pattern: "error", MaxHits: 305})
	if res.Kept != 305 || res.Total != 352 || res.Unsearched != 1 {
		t.Errorf("kept %d of %d with %d unsearched; want 305 of 352 and 1", res.Kept, res.Total, res.Unsearched)
	}
	if sum := res.Summary(res.Scanned, res.Scanned+res.Unsearched, "glob"); !strings.Contains(sum, "Showing 305 of 352 matches counted in 3 of 4 files") ||
		!strings.Contains(sum, "Cut ACROSS files") {
		t.Errorf("summary:\n%s", sum)
	}

	res, _ = Open("u1", "b1").Search(ctx, Query{Pattern: "error", CountOnly: true})
	if len(res.Hits) != 0 || res.Total != 353 || len(res.Tallies) != 4 || res.Tallies[0].File != "a_noisy.log" || res.Truncated {
		t.Errorf("count: hits %d total %d tallies %+v truncated %v", len(res.Hits), res.Total, res.Tallies, res.Truncated)
	}
	if _, err := Open("u1", "b1").Search(ctx, Query{Pattern: "x", CountOnly: true, Group: true}); err == nil {
		t.Error("count and group together should be refused")
	}
}
