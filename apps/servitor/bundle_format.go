// bundle_format.go — reading shape out of a log file: what format it is, what
// time each line carries, how severe it claims to be, which host emitted it.
//
// All of it is derived once during the ingest pass and stored on the file's
// index entry, because the alternative is re-deriving it on every question. The
// detection is deliberately shallow — five formats, matched on a sample — since
// the cost of being wrong is a file reported as "text" and searched literally,
// which is exactly what an unrecognized format should get anyway.
//
// Timestamps without a zone are read as UTC. Nothing in a bare log line says
// otherwise, and a consistent wrong zone still orders a timeline correctly,
// where a guessed local zone silently shifts one file against another.
package servitor

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	// bundleMaxSearchHits caps one search_bundle call. Generous compared to
	// the repo store's 60 because a log search legitimately wants to see a
	// burst, and the result is truncation-marked either way.
	bundleMaxSearchHits = 200
	// bundleMaxTimelineLines caps one merged timeline.
	bundleMaxTimelineLines = 300
	// bundleFormatSample is how many lines are examined to decide a file's
	// format. Enough to get past a header banner, small enough that the
	// decision costs one slice.
	bundleFormatSample = 60
)

// Format names stored on bundleFile.Format.
const (
	bundleFormatSyslog = "syslog" // "Mar 14 02:11:09 host proc[1]: msg"
	bundleFormatISO    = "iso"    // leading ISO-8601 / RFC3339 timestamp
	bundleFormatCLF    = "clf"    // Common/Combined Log Format (web access logs)
	bundleFormatJSONL  = "jsonl"  // one JSON object per line
	bundleFormatText   = "text"   // nothing recognized
)

var (
	// reISOTime matches an ISO-8601 date-time anywhere in a line, with an
	// optional fractional part and an optional zone. Anywhere rather than
	// anchored because a JSON line carries it inside a field.
	reISOTime = regexp.MustCompile(`(\d{4})-(\d{2})-(\d{2})[T ](\d{2}):(\d{2}):(\d{2})(\.\d+)?(Z|[+-]\d{2}:?\d{2})?`)
	// reCLFTime matches the bracketed timestamp of a web access log.
	reCLFTime = regexp.MustCompile(`\[(\d{2}/\w{3}/\d{4}:\d{2}:\d{2}:\d{2} [+-]\d{4})\]`)
	// reSyslogTime matches the classic BSD syslog prefix — which carries no
	// year, the reason bundleFile.YearInferred exists.
	reSyslogTime = regexp.MustCompile(`^([A-Z][a-z]{2}) {1,2}(\d{1,2}) (\d{2}:\d{2}:\d{2})`)
	// reSyslogHost pulls the hostname that follows the syslog timestamp.
	reSyslogHost = regexp.MustCompile(`^[A-Z][a-z]{2} {1,2}\d{1,2} \d{2}:\d{2}:\d{2} (\S+) `)
	// reSeverity matches an uppercase severity token standing on its own.
	// Uppercase only: "error" inside prose is not a severity field, and
	// counting it would make every README look like an outage.
	reSeverity = regexp.MustCompile(`\b(EMERG|ALERT|CRITICAL|CRIT|FATAL|PANIC|ERROR|ERR|WARNING|WARN|NOTICE|INFO|DEBUG|TRACE)\b`)
)

// severityCanon collapses the spellings that mean the same thing, so a
// histogram over a bundle whose files disagree still adds up.
var severityCanon = map[string]string{
	"EMERG": "EMERG", "ALERT": "ALERT",
	"CRITICAL": "CRIT", "CRIT": "CRIT",
	"FATAL": "FATAL", "PANIC": "FATAL",
	"ERROR": "ERROR", "ERR": "ERROR",
	"WARNING": "WARN", "WARN": "WARN",
	"NOTICE": "NOTICE", "INFO": "INFO",
	"DEBUG": "DEBUG", "TRACE": "TRACE",
}

// detectBundleFormat decides a file's format from a sample of its lines. A
// format has to explain a majority of the non-empty sample to win, so a log
// with one ISO-stamped banner at the top is not misread as an ISO log.
func detectBundleFormat(sample []string) string {
	var nonEmpty int
	counts := map[string]int{}
	for _, ln := range sample {
		t := strings.TrimSpace(ln)
		if t == "" {
			continue
		}
		nonEmpty++
		switch {
		case reCLFTime.MatchString(t):
			counts[bundleFormatCLF]++
		case reSyslogTime.MatchString(t):
			counts[bundleFormatSyslog]++
		case strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}") && reISOTime.MatchString(t):
			counts[bundleFormatJSONL]++
		case reISOTime.MatchString(t):
			counts[bundleFormatISO]++
		}
	}
	if nonEmpty == 0 {
		return bundleFormatText
	}
	best, bestN := bundleFormatText, 0
	// Iterated in a fixed order so a tie resolves the same way every ingest;
	// ranging a map here would make the stored format non-deterministic.
	for _, f := range []string{bundleFormatCLF, bundleFormatSyslog, bundleFormatJSONL, bundleFormatISO} {
		if counts[f] > bestN {
			best, bestN = f, counts[f]
		}
	}
	if bestN*2 < nonEmpty {
		return bundleFormatText
	}
	return best
}

// parseBundleTime extracts the timestamp from one line. year supplies the
// missing year for formats that carry none (syslog); it is ignored otherwise.
func parseBundleTime(format string, year int, line string) (time.Time, bool) {
	switch format {
	case bundleFormatCLF:
		if m := reCLFTime.FindStringSubmatch(line); m != nil {
			if t, err := time.Parse("02/Jan/2006:15:04:05 -0700", m[1]); err == nil {
				return t.UTC(), true
			}
		}
		return time.Time{}, false
	case bundleFormatSyslog:
		m := reSyslogTime.FindStringSubmatch(line)
		if m == nil {
			return time.Time{}, false
		}
		if year <= 0 {
			return time.Time{}, false
		}
		t, err := time.ParseInLocation("2006 Jan 2 15:04:05",
			fmt.Sprintf("%d %s %s %s", year, m[1], m[2], m[3]), time.UTC)
		if err != nil {
			return time.Time{}, false
		}
		return t, true
	default:
		// iso / jsonl / text all resolve through the same ISO matcher: a
		// "text" file with timestamps is more useful searched by time than
		// declared untimed.
		m := reISOTime.FindString(line)
		if m == "" {
			return time.Time{}, false
		}
		return parseISOish(m)
	}
}

// parseISOish parses the ISO-8601 variants reISOTime can match, most specific
// first. A value with no zone is read as UTC (see the file header).
func parseISOish(s string) (time.Time, bool) {
	s = strings.Replace(s, " ", "T", 1)
	for _, layout := range []string{
		time.RFC3339Nano, time.RFC3339,
		"2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// bundleLineInWindow reports whether a line's own timestamp falls inside the
// window. A line with no parseable timestamp is KEPT: continuation lines of a
// stack trace carry no time of their own, and dropping them would cut the
// exception off from its message.
func bundleLineInWindow(format string, year int, line string, since, until time.Time) bool {
	ts, ok := parseBundleTime(format, year, line)
	if !ok {
		return true
	}
	if !since.IsZero() && ts.Before(since) {
		return false
	}
	if !until.IsZero() && ts.After(until) {
		return false
	}
	return true
}

// bundleFileSpan returns the file's parsed first/last timestamps. known is
// false when the format carries no timestamp, which is the signal callers use
// to exclude the file from a timeline rather than mis-order it.
func bundleFileSpan(bf bundleFile) (first, last time.Time, known bool) {
	f, err1 := time.Parse(time.RFC3339, bf.First)
	l, err2 := time.Parse(time.RFC3339, bf.Last)
	if err1 != nil || err2 != nil {
		return time.Time{}, time.Time{}, false
	}
	return f, l, true
}

// bundleFileYear supplies the year for a format that omits it. Taken from the
// span recorded at ingest (which took it from the staged file's modification
// time), falling back to the ingest timestamp.
func bundleFileYear(bf bundleFile) int {
	if t, err := time.Parse(time.RFC3339, bf.First); err == nil {
		return t.Year()
	}
	if t, err := time.Parse(time.RFC3339, bf.Ingested); err == nil {
		return t.Year()
	}
	return 0
}

// bundleSeverity returns the canonical severity token a line declares, or ""
// for none. Only the FIRST match counts — a line reading "ERROR: failed to
// parse DEBUG flag" is an error, not one of each.
func bundleSeverity(line string) string {
	m := reSeverity.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	return severityCanon[m[1]]
}

// bundleHost extracts the emitting host, for the one format that names it.
func bundleHost(format, line string) string {
	if format != bundleFormatSyslog {
		return ""
	}
	if m := reSyslogHost.FindStringSubmatch(line); m != nil {
		return m[1]
	}
	return ""
}

// formatBundleSpan renders a file's time span for display, carrying the
// year-inferred caveat with it. The caveat travels with the value rather than
// being mentioned once elsewhere: a span that is silently a year off is worse
// than no span at all.
func formatBundleSpan(bf bundleFile) string {
	if bf.First == "" || bf.Last == "" {
		return "no timestamps"
	}
	span := bf.First + " → " + bf.Last
	if bf.YearInferred {
		span += " (year inferred from file mtime — the log itself carries none)"
	}
	return span
}
