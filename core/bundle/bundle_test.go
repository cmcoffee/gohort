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
func withMemStore(t *testing.T) {
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
func writeStage(t *testing.T, files map[string]string) string {
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
	res, err := Open("u1", "b1").Search(Query{Pattern: "connection refused", After: 1})
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
	res, _ = Open("u1", "b1").Search(Query{Pattern: "connection refused", Glob: "*.log"})
	if len(res.Hits) != 1 {
		t.Errorf("globbed search found %d hits, want 1", len(res.Hits))
	}
	// A bad regex is an error the caller can act on, not zero results.
	if _, err := Open("u1", "b1").Search(Query{Pattern: "("}); err == nil {
		t.Error("an invalid regular expression was accepted")
	}

	// --- timeline: merges both logs, names the file it cannot place ---
	entries, unmergeable, err := Open("u1", "b1").Timeline("", time.Date(2026, 3, 14, 2, 14, 0, 0, time.UTC),
		time.Date(2026, 3, 14, 2, 15, 0, 0, time.UTC), 0)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
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
	res, err := Open("u1", "b1").Search(Query{Pattern: "failure number", After: 1})
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
