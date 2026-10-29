package bundle

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// rowCounts reads the count column out of a rendered histogram, label->count.
func rowCounts(t *testing.T, out string) map[string]int {
	t.Helper()
	rows := map[string]int{}
	for _, ln := range strings.Split(out, "\n") {
		f := strings.Fields(ln)
		if len(f) < 2 {
			continue
		}
		if _, err := time.Parse("15:04", f[0]); err != nil {
			continue
		}
		n, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		rows[f[0]] = n
	}
	return rows
}

// The rows run from the first match to the last, an empty bucket between them
// is drawn as 0 rather than skipped, and every line lands in its own row.
func TestHistogramRowsRunFirstToLastWithGaps(t *testing.T) {
	h := NewHistogram()
	at := func(hm string, sec int) time.Time {
		tm, _ := time.Parse("2006-01-02 15:04", "2026-10-27 "+hm)
		return tm.Add(time.Duration(sec) * time.Second)
	}
	for i := 0; i < 3; i++ {
		h.Add(at("02:11", i))
	}
	for i := 0; i < 50; i++ {
		h.Add(at("02:14", i))
	}
	h.Add(at("02:16", 59))
	h.Add(time.Time{})
	// Left to choose, it picks the finest size that fits in about 60 rows:
	// six minutes is 36 rows of ten seconds.
	if out := h.Render(`"timeout"`, 0); !strings.Contains(out, "per 10s.") {
		t.Errorf("automatic bucket over six minutes:\n%.200s", out)
	}
	out := h.Render(`"timeout"`, time.Minute)
	if !strings.Contains(out, `"timeout": 54 matching line(s), 2026-10-27 02:11:00 to 2026-10-27 02:16:59, per 1m.`) {
		t.Fatalf("header:\n%s", out)
	}
	got := rowCounts(t, out)
	want := map[string]int{"02:11": 3, "02:12": 0, "02:13": 0, "02:14": 50, "02:15": 0, "02:16": 1}
	if len(got) != len(want) {
		t.Fatalf("rows %v, want %v\n%s", got, want, out)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("row %s = %d, want %d", k, got[k], v)
		}
	}
	if !strings.Contains(out, "1 more matching line(s) carry no timestamp") {
		t.Errorf("the undated line is not reported:\n%s", out)
	}
	// The row with one line still draws something next to the peak of 50.
	for _, ln := range strings.Split(out, "\n") {
		if strings.HasPrefix(ln, "02:16") && !strings.ContainsAny(ln, "▏▎▍▌▋▊▉█") {
			t.Errorf("a non-empty row drew no bar: %q", ln)
		}
	}
}

func TestParseBucket(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"": 0, "30s": 30 * time.Second, "5m": 5 * time.Minute, "1h": time.Hour,
		"1d": 24 * time.Hour, "7D": 7 * 24 * time.Hour,
	} {
		if got, err := ParseBucket(in); err != nil || got != want {
			t.Errorf("ParseBucket(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"x", "0s", "500ms", "-1h", "0d"} {
		if _, err := ParseBucket(bad); err == nil {
			t.Errorf("ParseBucket(%q) accepted", bad)
		}
	}
}

// A requested bucket is used when it fits, and widened, saying so, when it
// would draw more rows than a reply should carry.
func TestHistogramHonoursOrWidensTheAskedBucket(t *testing.T) {
	h := NewHistogram()
	base := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 48; i++ {
		h.Add(base.Add(time.Duration(i) * time.Hour))
	}
	if out := h.Render("x", 6*time.Hour); !strings.Contains(out, "per 6h.") || strings.Contains(out, "widened") {
		t.Errorf("a 6h bucket over two days should be used as asked:\n%s", out)
	}
	out := h.Render("x", time.Second)
	if !strings.Contains(out, "Bucket widened from 1s to") || strings.Count(out, "\n") > maxRows+10 {
		t.Errorf("a 1s bucket over two days should be widened, and say so:\n%.400s", out)
	}
}

// Past maxHistogramKeys distinct seconds the counts roll up to minutes: memory
// stays bounded, no line is lost, and a bucket finer than what is kept is
// widened with a sentence saying so.
func TestHistogramRollsUpWithoutLosingLines(t *testing.T) {
	h := NewHistogram()
	base := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	n := maxHistogramKeys + 500
	for i := 0; i < n; i++ {
		h.Add(base.Add(time.Duration(i) * time.Second))
	}
	if h.res != time.Minute || len(h.counts) > maxHistogramKeys {
		t.Fatalf("res %s with %d keys after %d distinct seconds", h.res, len(h.counts), n)
	}
	total := 0
	for _, c := range h.counts {
		total += c
	}
	if total != n || h.Lines != n {
		t.Errorf("rolled-up total %d, lines %d, want %d", total, h.Lines, n)
	}
	out := h.Render("x", 30*time.Second)
	if !strings.Contains(out, "Bucket widened from 30s to 10m") {
		t.Errorf("a bucket finer than the kept resolution should be widened, and say so:\n%.300s", out)
	}
	sum := 0
	for _, ln := range strings.Split(out, "\n") {
		if f := strings.Fields(ln); len(f) >= 3 && strings.HasPrefix(ln, "2026-") {
			n, _ := strconv.Atoi(f[2])
			sum += n
		}
	}
	if sum != n {
		t.Errorf("the drawn rows add up to %d, want %d", sum, n)
	}
}

// Matches with no timestamps at all are said to exist, not drawn as nothing.
func TestHistogramWithOnlyUndatedLinesSaysSo(t *testing.T) {
	h := NewHistogram()
	h.Add(time.Time{})
	h.Add(time.Time{})
	if out := h.Render(`"x"`, 0); !strings.Contains(out, "2 line(s) matched, but none carries a timestamp") {
		t.Errorf("got %q", out)
	}
}
