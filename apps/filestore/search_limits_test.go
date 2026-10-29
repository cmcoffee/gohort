package filestore

// What an investigating agent asked of the search, from a real session over a
// 1,378-file support bundle: a noisy file must not starve the rest, the caller
// can raise the budget for a narrow search, count without reading, page, see
// where a cut happened, window by time without dates smuggled into the regex
// (which broke on tracebacks), and see what period each file covers.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/bundle"
)

// writeLog writes a file and dates it, so newest-first order is fixed.
func writeLog(t *testing.T, dir, name, body string, age time.Duration) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatal(err)
	}
}

func repeatLines(line string, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(line + "\n")
	}
	return b.String()
}

func TestANoisyFileCannotStarveTheOthers(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "noisy.log", repeatLines("ERROR retry", 300), time.Minute)
	writeLog(t, dir, "quiet.log", repeatLines("ERROR disk full", 3), time.Hour)

	res, err := Search(context.Background(), dir, SearchOpts{Pattern: "ERROR", PerFile: defaultPerFile})
	if err != nil {
		t.Fatal(err)
	}
	quiet := 0
	for _, m := range res.Matches {
		if m.File == "quiet.log" {
			quiet++
		}
	}
	if quiet != 3 {
		t.Errorf("the quiet file's matches were crowded out: %d of 3 shown", quiet)
	}
	if len(res.CutInFiles) != 1 || res.CutInFiles[0].File != "noisy.log" || res.CutInFiles[0].Matches != 300 {
		t.Errorf("the cut should be named, with the file's full count: %+v", res.CutInFiles)
	}
	out := renderMatches(res, "read_logs")
	if !strings.Contains(out, "Cut WITHIN files") || !strings.Contains(out, fmt.Sprintf("noisy.log (%d of 300)", defaultPerFile)) ||
		!strings.Contains(out, fmt.Sprintf("per_file=%d", defaultPerFile)) {
		t.Errorf("the reply should state the limit and where it cut:\n%s", out)
	}
}

func TestASearchKeepsUpToItsLimit(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "app.log", repeatLines("ERROR x", maxMatches+500), time.Minute)
	// One file: the per-file share lifts, and the limit is the only one.
	res, _ := Search(context.Background(), dir, SearchOpts{Pattern: "ERROR", PerFile: defaultPerFile, Max: 150})
	if len(res.Matches) != 150 || res.PerFile != 0 {
		t.Errorf("a single-file search should keep what was asked for: %d kept, per_file=%d", len(res.Matches), res.PerFile)
	}
	res, _ = Search(context.Background(), dir, SearchOpts{Pattern: "ERROR", Max: 100000})
	if len(res.Matches) != maxMatches || res.Total != maxMatches+500 || !res.Cut() {
		t.Errorf("the limit is %d: kept %d of %d, cut=%v", maxMatches, len(res.Matches), res.Total, res.Cut())
	}
}

func TestCountAnswersHowOftenWithoutLines(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "a.log", repeatLines("ERROR a", 7), time.Minute)
	writeLog(t, dir, "b.log", repeatLines("ERROR b", 400), time.Hour)
	writeLog(t, dir, "c.log", repeatLines("INFO fine", 10), 2*time.Hour)
	res, err := Search(context.Background(), dir, SearchOpts{Pattern: "ERROR", CountOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) != 0 || res.Total != 407 || len(res.Tallies) != 2 || res.Tallies[0].File != "b.log" {
		t.Fatalf("count should total every file, busiest first, with no lines: %+v", res)
	}
	out := renderCounts(res, "ERROR")
	if !strings.Contains(out, "407 matches") || !strings.Contains(out, "400  b.log") || !strings.Contains(out, "of 3 searched") {
		t.Errorf("count reply:\n%s", out)
	}
}

// Paging is the generic kind, not the search's own: a reply longer than a page
// is kept and arrives as its first page, with the summary on it, and
// read_output reads on or greps within the kept matches without searching
// again. What it reaches past the first page is the same search's later
// matches.
func TestALongReplyPagesThroughReadOutput(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 1200; i++ {
		fmt.Fprintf(&b, "ERROR n=%d\n", i)
	}
	writeLog(t, dir, "app.log", b.String(), time.Minute)
	opts := SearchOpts{Pattern: "ERROR", Context: 1}
	res, _ := Search(context.Background(), dir, opts)
	reply := searchReply(res, opts, "read_logs")
	if !strings.HasPrefix(reply, fmt.Sprintf("Showing %d of 1200 matches", maxMatches)) {
		t.Fatalf("the first page should open with the summary:\n%.300s", reply)
	}
	m := regexp.MustCompile(`output_id="([^"]+)", offset=(\d+)`).FindStringSubmatch(reply)
	if m == nil || len(reply) > 2*bundle.SearchPageChars {
		t.Fatalf("a long reply should arrive as one page with a read_output trailer (%d chars):\n%s", len(reply), reply[len(reply)-400:])
	}
	if strings.Contains(reply, "n=900\n") {
		t.Fatal("match 900 should not be on the first page")
	}
	offset, _ := strconv.Atoi(m[2])
	next, err := OutputPage{ID: m[1], Offset: offset, Max: bundle.SearchPageChars, Tool: "read_output"}.Read()
	if err != nil || !strings.Contains(next, "> ERROR n=") {
		t.Fatalf("read_output should read on through the matches: %v\n%.300s", err, next)
	}
	found, err := OutputPage{ID: m[1], Grep: "n=900$", Tool: "read_output"}.Read()
	if err != nil || !strings.Contains(found, "ERROR n=900") {
		t.Errorf("grep within the kept reply should find match 900: %v\n%s", err, found)
	}
}

func TestACutAcrossFilesSaysWhatWasNotSearched(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		writeLog(t, dir, fmt.Sprintf("f%d.log", i), repeatLines("ERROR y", 20), time.Duration(i+1)*time.Minute)
	}
	res, _ := Search(context.Background(), dir, SearchOpts{Pattern: "ERROR", Max: 30})
	if res.Unsearched == 0 {
		t.Fatalf("the global cap should leave files unsearched, and say how many: %+v", res)
	}
	if out := renderMatches(res, "read_logs"); !strings.Contains(out, "Cut ACROSS files") || !strings.Contains(out, "count=true") {
		t.Errorf("reply:\n%s", out)
	}
}

// Dates in the regex broke on a traceback, whose error line carries no
// timestamp. The window reads each line's own time, and an undated line takes
// the time of the dated line above it.
func TestATimeWindowKeepsATracebackWithItsTimestamp(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "app.log", strings.Join([]string{
		"2026-08-16 10:00:00 ERROR pymysql.err.OperationalError: old one",
		"2026-08-17 02:00:00 ERROR request failed",
		"Traceback (most recent call last):",
		`  File "db.py", line 12, in query`,
		"pymysql.err.OperationalError: (2006, 'MySQL server has gone away')",
		"2026-08-18 09:00:00 INFO recovered",
	}, "\n")+"\n", time.Minute)
	writeLog(t, dir, "undated.txt", "OperationalError with no date anywhere\n", time.Hour)

	since, _ := time.Parse("2006-01-02", "2026-08-17")
	until := since.Add(24*time.Hour - time.Second)
	res, err := Search(context.Background(), dir, SearchOpts{Pattern: "OperationalError", Since: since, Until: until})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) != 1 || !strings.Contains(res.Matches[0].Text, "gone away") {
		t.Fatalf("the traceback's error line belongs to 2026-08-17 and only it should match: %+v", res.Matches)
	}
	if res.Untimed != 1 {
		t.Errorf("the undated file is left out of a window, and counted: untimed=%d", res.Untimed)
	}
	later, _ := time.Parse("2006-01-02", "2026-08-18")
	res, _ = Search(context.Background(), dir, SearchOpts{Pattern: "OperationalError", Since: later})
	if len(res.Matches) != 0 {
		t.Errorf("nothing matches after 08-18: %+v", res.Matches)
	}
}

func TestTheListingShowsWhatPeriodEachFileCovers(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "app.log", "2026-08-17 02:00:00 INFO start\nplain line\n2026-08-19 23:30:00 INFO end\n", time.Minute)
	writeLog(t, dir, "notes.txt", "no dates here\n", time.Hour)
	files, _ := List(dir, "")
	spans := map[string]string{}
	for _, f := range files {
		spans[f.Rel] = describeSpan(dir, f)
	}
	if spans["app.log"] != ", covers 2026-08-17 02:00:00 to 2026-08-19 23:30:00" {
		t.Errorf("app.log span = %q", spans["app.log"])
	}
	if spans["notes.txt"] != ", no timestamps" {
		t.Errorf("notes.txt span = %q", spans["notes.txt"])
	}
}

// A search of several patterns heads its reply with a line per pattern, then
// gives each its own section, the rarest first: a rare pattern's matches are
// on the first page however many the noisy one brings.
func TestSeveralPatternsReplyRarestFirst(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 0; i < 900; i++ {
		fmt.Fprintf(&b, "2026-10-27T06:%02d:%02dZ ERROR request %d timed out upstream\n", i/60%60, i%60, i)
		if i == 850 {
			b.WriteString("2026-10-27T06:59:00Z kernel: oom-killer invoked\n")
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "app.log"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := SearchOpts{Patterns: []string{"timed out", "oom-killer", "refused"}, Context: 1, PerFile: defaultPerFile}
	res, err := Search(context.Background(), dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	reply := searchReply(res, opts, "read_logs")
	for _, want := range []string{
		"3 patterns searched in one pass",
		`"timed out": 900 match(es) in 1 file(s)`,
		`"refused": no matches`,
		"852> 2026-10-27T06:59:00Z kernel: oom-killer invoked",
		"900 match(es) in 1 file(s), of 1 searched", // the noisy pattern's own summary: its share is lifted for one file
	} {
		if !strings.Contains(reply, want) {
			t.Errorf("first page is missing %q:\n%.2000s", want, reply)
		}
	}
	if strings.Index(reply, `pattern "oom-killer"`) > strings.Index(reply, `pattern "timed out"`) {
		t.Errorf("the rare pattern's section should come first:\n%.2000s", reply)
	}
}
