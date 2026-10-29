// Counting matches over time: when something happened, not just whether.
//
// The first questions in an incident are about time: when did this start, is
// it a spike or a trickle, did it stop after the fix, did A come before B. A
// list of hits answers them only by paging through timestamps by eye, and a
// count per file not at all. A Histogram files each matching line's timestamp
// into a bucket and draws one row per bucket, a few dozen rows however large
// the bundle.
//
// Memory is bounded without knowing the span in advance: counts are kept per
// second, and when the distinct seconds pass maxHistogramKeys they are rolled
// up into minutes, then hours, then days. The display bucket is chosen at the
// end from the span actually seen, never finer than what was kept.
package bundle

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// maxHistogramKeys bounds the distinct instants a Histogram holds before
	// rolling up to a coarser resolution.
	maxHistogramKeys = 100000
	// autoRows is what an automatic bucket aims to fit within; maxRows is the
	// most a requested bucket may produce before it is widened.
	autoRows = 60
	maxRows  = 300
	// barWidth is the widest a row's bar is drawn.
	barWidth = 30
)

// histogramResolutions are the steps counts roll up through.
var histogramResolutions = []time.Duration{time.Second, time.Minute, time.Hour, 24 * time.Hour}

// bucketSizes are the display buckets an automatic choice picks from, each a
// whole multiple of the resolution below it.
var bucketSizes = []time.Duration{
	time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 15 * time.Second, 30 * time.Second,
	time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 30 * time.Minute,
	time.Hour, 2 * time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour,
	24 * time.Hour, 2 * 24 * time.Hour, 7 * 24 * time.Hour, 30 * 24 * time.Hour,
}

// Histogram counts lines by their timestamps.
type Histogram struct {
	res         time.Duration
	counts      map[int64]int // instant / res -> lines
	first, last time.Time
	Lines       int // dated lines counted
	Undated     int // lines with no timestamp to place them by
}

// NewHistogram makes an empty Histogram.
func NewHistogram() *Histogram {
	return &Histogram{res: time.Second, counts: map[int64]int{}}
}

// Add counts one line at t; a zero t counts it as undated.
func (h *Histogram) Add(t time.Time) {
	if t.IsZero() {
		h.Undated++
		return
	}
	h.Lines++
	if h.first.IsZero() || t.Before(h.first) {
		h.first = t
	}
	if t.After(h.last) {
		h.last = t
	}
	h.counts[h.key(t)]++
	if len(h.counts) > maxHistogramKeys {
		h.rollUp()
	}
}

func (h *Histogram) key(t time.Time) int64 {
	return floorDiv(t.Unix(), int64(h.res/time.Second))
}

// rollUp moves the counts to the next coarser resolution.
func (h *Histogram) rollUp() {
	for i, r := range histogramResolutions {
		if r == h.res && i+1 < len(histogramResolutions) {
			next := histogramResolutions[i+1]
			factor := int64(next / r)
			rolled := make(map[int64]int, len(h.counts)/int(factor)+1)
			for k, n := range h.counts {
				rolled[floorDiv(k, factor)] += n
			}
			h.res, h.counts = next, rolled
			return
		}
	}
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// ParseBucket reads a bucket size: "30s", "5m", "1h", "1d", "7d". Empty is
// zero, which Render reads as "choose one".
func ParseBucket(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, nil
	}
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("bucket %q is not a size like \"30s\", \"5m\", \"1h\" or \"1d\"", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < time.Second {
		return 0, fmt.Errorf("bucket %q is not a size like \"30s\", \"5m\", \"1h\" or \"1d\"", s)
	}
	return d.Truncate(time.Second), nil
}

// chooseBucket picks the display bucket: the requested one when it fits, else
// the smallest standard size that does. Returns the bucket and, when it is not
// what was asked for, a sentence saying why.
func (h *Histogram) chooseBucket(asked time.Duration) (time.Duration, string) {
	span := h.last.Sub(h.first)
	rows := func(b time.Duration) int64 { return int64(span/b) + 1 }
	if asked > 0 {
		// Never finer than the resolution held, and a whole number of it,
		// or the rows could not be counted exactly.
		b := asked
		if b%h.res != 0 {
			b = (b/h.res + 1) * h.res
		}
		if rows(b) > maxRows {
			for _, c := range bucketSizes {
				if c >= b && c%h.res == 0 && rows(c) <= maxRows {
					b = c
					break
				}
			}
		}
		if b != asked {
			return b, fmt.Sprintf("Bucket widened from %s to %s to fit the span in at most %d rows.", fmtBucket(asked), fmtBucket(b), maxRows)
		}
		return b, ""
	}
	for _, c := range bucketSizes {
		if c >= h.res && c%h.res == 0 && rows(c) <= autoRows {
			return c, ""
		}
	}
	return bucketSizes[len(bucketSizes)-1], ""
}

// fmtBucket names a bucket size the way ParseBucket reads one.
func fmtBucket(d time.Duration) string {
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d >= time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return fmt.Sprintf("%ds", d/time.Second)
}

// Render draws the counts, one row per bucket from the first that holds a
// line to the last. An empty bucket between them is drawn as 0, since a gap
// is part of the answer. what names the search in the header. asked is the
// requested bucket, zero to choose one.
func (h *Histogram) Render(what string, asked time.Duration) string {
	var b strings.Builder
	if h.Lines == 0 {
		if h.Undated > 0 {
			fmt.Fprintf(&b, "%s: %d line(s) matched, but none carries a timestamp, so there is nothing to place on a time axis. Search for them to read them instead.\n", what, h.Undated)
		}
		return b.String()
	}
	bucket, note := h.chooseBucket(asked)
	start := h.first.Truncate(bucket)
	n := int(h.last.Sub(start)/bucket) + 1
	rows := make([]int, n)
	for k, c := range h.counts {
		at := time.Unix(k*int64(h.res/time.Second), 0).UTC()
		i := int(at.Sub(start) / bucket)
		if i < 0 {
			i = 0
		}
		if i >= n {
			i = n - 1
		}
		rows[i] += c
	}
	peak := 0
	for _, c := range rows {
		if c > peak {
			peak = c
		}
	}
	sameDay := h.first.Format("2006-01-02") == h.last.Format("2006-01-02")
	layout := rowLayout(bucket, sameDay)
	fmt.Fprintf(&b, "%s: %d matching line(s), %s to %s, per %s.\n", what, h.Lines,
		h.first.Format("2006-01-02 15:04:05"), h.last.Format("2006-01-02 15:04:05"), fmtBucket(bucket))
	if note != "" {
		b.WriteString(note + "\n")
	}
	b.WriteString("\n")
	width := len(strconv.Itoa(peak))
	for i, c := range rows {
		label := start.Add(time.Duration(i) * bucket).Format(layout)
		fmt.Fprintf(&b, "%s %*d  %s\n", label, width, c, bar(c, peak))
	}
	if h.Undated > 0 {
		fmt.Fprintf(&b, "\n%d more matching line(s) carry no timestamp of their own (a stack frame, a wrapped message) and are not counted above.\n", h.Undated)
	}
	return b.String()
}

// rowLayout is a row's time label: no date when every row is on one day (the
// header carries it), no seconds when the bucket is a minute or more.
func rowLayout(bucket time.Duration, sameDay bool) string {
	switch {
	case bucket >= 24*time.Hour:
		return "2006-01-02"
	case bucket >= time.Minute && sameDay:
		return "15:04"
	case bucket >= time.Minute:
		return "2006-01-02 15:04"
	case sameDay:
		return "15:04:05"
	}
	return "2006-01-02 15:04:05"
}

// bar draws c against peak in eighths of a block, so a row with any lines at
// all always shows something.
func bar(c, peak int) string {
	if c == 0 || peak == 0 {
		return ""
	}
	eighths := c * barWidth * 8 / peak
	if eighths == 0 {
		eighths = 1
	}
	parts := []string{"", "▏", "▎", "▍", "▌", "▋", "▊", "▉"}
	return strings.Repeat("█", eighths/8) + parts[eighths%8]
}
