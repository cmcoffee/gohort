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
	"strings"
	"testing"
	"time"
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
	out := renderMatches(res)
	if !strings.Contains(out, "Cut WITHIN files") || !strings.Contains(out, "noisy.log (15 of 300)") || !strings.Contains(out, "per_file=15") {
		t.Errorf("the reply should state the limit and where it cut:\n%s", out)
	}
}

func TestANarrowSearchCanRaiseTheBudget(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "app.log", repeatLines("ERROR x", 500), time.Minute)
	// One file: the per-file cap lifts, and max_matches is the only limit.
	res, _ := Search(context.Background(), dir, SearchOpts{Pattern: "ERROR", PerFile: defaultPerFile, Max: 150})
	if len(res.Matches) != 150 || res.PerFile != 0 {
		t.Errorf("a single-file search should show what was asked for: %d shown, per_file=%d", len(res.Matches), res.PerFile)
	}
	res, _ = Search(context.Background(), dir, SearchOpts{Pattern: "ERROR", Max: 100000})
	if len(res.Matches) != maxMatchesCeil {
		t.Errorf("max_matches is bounded at %d, got %d", maxMatchesCeil, len(res.Matches))
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

func TestSkipPagesThroughTheSameResult(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 130; i++ {
		fmt.Fprintf(&b, "ERROR n=%d\n", i)
	}
	writeLog(t, dir, "app.log", b.String(), time.Minute)
	first, _ := Search(context.Background(), dir, SearchOpts{Pattern: "ERROR"})
	second, _ := Search(context.Background(), dir, SearchOpts{Pattern: "ERROR", Skip: len(first.Matches)})
	if len(second.Matches) == 0 || second.Matches[0].Line != len(first.Matches)+1 || second.Skipped != len(first.Matches) {
		t.Fatalf("page two should start where page one ended: %+v", second.Matches[:1])
	}
	if out := renderMatches(first); !strings.Contains(out, fmt.Sprintf("skip=%d", len(first.Matches))) {
		t.Errorf("a capped reply should say how to get the next page:\n%s", out)
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
	if out := renderMatches(res); !strings.Contains(out, "Cut ACROSS files") || !strings.Contains(out, "count=true") {
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
