// bundle_tools.go — the Type=="bundle" worker probe tools. These replace the
// SSH run_command / read_log tools: instead of executing anything, the bundle
// worker reads the encrypted evidence store. The recording, plan, and knowledge
// tools servitor already has are reused unchanged.
//
// Every one of these is a LOCAL read, which is what qualifies them for the
// worker allow-list in tool_guard.go.
package core

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cmcoffee/gohort/core/bundle"
)

// errBundleNotLoaded is returned when the store is empty — an unambiguous
// signal so the LLM treats it as "the evidence isn't available" (stop and
// report) rather than "this doesn't appear in the logs" (fabricate a negative).
const errBundleNotLoaded = "BUNDLE NOT INGESTED: this bundle's files are not currently in the store, so listing, search and read cannot run. This is NOT evidence that the thing you searched for is absent. Stop here and tell the user to upload the bundle's files. Do not guess at file names, timestamps, or contents."

// bundleListCap bounds one list_bundle response so a dump with fifty thousand
// files cannot spend the worker's whole context on a directory listing.
const bundleListCap = 300

// BundleTools builds the read/search tools bound to one (user, bundle
// appliance), decrypting the store in memory.
// BundleTools takes the run's context so its one long operation — a regex
// scan over an entire encrypted dump — stops when the run does. The agent loop
// tests for cancellation only between rounds, so a search that ignored its
// context was a Stop the user could press and watch do nothing.
func BundleTools(ctx context.Context, owner, bundleID string) []AgentToolDef {
	return []AgentToolDef{
		{
			Tool: Tool{
				Name:        "bundle_summary",
				Description: "Overview of the whole evidence bundle: how many files, what period they cover, which files are noisiest, and which are present but unread (binaries, archives nothing could open). ALWAYS call this first: it tells you what you are looking at and which file to search, without reading any log content.",
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				files := bundle.Open(owner, bundleID).Index()
				if len(files) == 0 {
					return errBundleNotLoaded, nil
				}
				return RenderBundleSummary(files), nil
			},
		},
		{
			Tool: Tool{
				Name:        "list_bundle",
				Description: "List the files in the bundle with their line counts, detected format, and time span. Use it to find the right file before searching. Optionally filter by a shell-style pattern.",
				Parameters: map[string]ToolParam{
					"glob": {Type: "string", Description: "Optional filter, e.g. \"*.log\", \"var/log/*\", or just a fragment of the path. Empty lists everything."},
				},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				files := bundle.Open(owner, bundleID).Index()
				if len(files) == 0 {
					return errBundleNotLoaded, nil
				}
				glob, _ := args["glob"].(string)
				glob = strings.TrimSpace(glob)
				var b strings.Builder
				shown := 0
				for _, bf := range files {
					if glob != "" && !bundle.MatchGlob(glob, bf.Path) {
						continue
					}
					if shown >= bundleListCap {
						fmt.Fprintf(&b, "(%d more files not shown: narrow the glob)\n", len(files)-shown)
						break
					}
					b.WriteString(renderBundleFileLine(bf))
					shown++
				}
				if shown == 0 {
					return fmt.Sprintf("No files match %q. Call list_bundle with no glob to see what is here.", glob), nil
				}
				return b.String(), nil
			},
		},
		{
			Tool: Tool{
				Name:        "search_bundle",
				Description: "Search the bundle's log files for a REGULAR EXPRESSION (case-insensitive), with optional context lines, path filter, and time window. Your main tool: find the error text, the request id, the hostname, the stack frame. Returns matching lines with file path and line number.",
				Parameters: map[string]ToolParam{
					"pattern":   {Type: "string", Description: "Regular expression to find, e.g. \"connection refused\", \"scheduler.*timeout\", \"req_id=abc123\"."},
					"patterns":  {Type: "array", Items: &ToolParam{Type: "string"}, Description: PatternsHelp},
					"group":     {Type: "boolean", Description: "Group the matching lines by shape instead of listing them: numbers, ids, addresses and times are masked, so lines differing only in those are one shape, each returned with its count, the files and times it spans, and one example line, most frequent first and the rarest after. The first call on an unfamiliar bundle: pattern \".\" groups every line, \"error|warn|exception\" just the trouble. Context lines do not apply."},
					"count":     {Type: "boolean", Description: "Return per-file match totals only, no lines, over every file with no match limit, busiest first. Answers how often, and in which log, which a list of hits cannot. Not with group or over_time."},
					"per_file":  {Type: "integer", Description: fmt.Sprintf("How many hits one file may keep (default %d), so a noisy file cannot crowd out the rest. Lifted when only one file is in scope.", bundle.DefaultPerFile)},
					"over_time": {Type: "boolean", Description: OverTimeHelp},
					"bucket":    {Type: "string", Description: BucketHelp},
					"exclude":   {Type: "string", Description: "Optional regular expression for lines to leave out (case-insensitive): a line the pattern matched is dropped when this matches it too. The way to say \"but not\", which a pattern cannot: pattern \"error\" with exclude \"healthcheck|/status\"."},
					"glob":      {Type: "string", Description: "Optional path filter, e.g. \"*scheduler*\" or \"var/log/*.log\". Empty searches every file."},
					"before":    {Type: "integer", Description: "Context lines to include BEFORE each hit (default 0, max 20)."},
					"after":     {Type: "integer", Description: "Context lines to include AFTER each hit (default 0, max 20)."},
					"record":    {Type: "boolean", Description: RecordHelp},
					"since":     {Type: "string", Description: "Optional earliest timestamp, e.g. \"2026-03-14\" or \"2026-03-14 02:00:00\"."},
					"until":     {Type: "string", Description: "Optional latest timestamp, same formats as since."},
				},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				if bundle.Open(owner, bundleID).FileCount() == 0 {
					return errBundleNotLoaded, nil
				}
				q := bundle.Query{
					Pattern:  bundleStrArg(args, "pattern"),
					Patterns: bundle.PatternsArg(args["patterns"]),
					Exclude:  bundleStrArg(args, "exclude"),
					Glob:     bundleStrArg(args, "glob"),
					Group:    BoolArg(args, "group"),
					Before:   clampInt(IntArg(args, "before"), 0, 20),
					After:    clampInt(IntArg(args, "after"), 0, 20),
					PerFile:  bundle.DefaultPerFile,
				}
				q.OverTime = BoolArg(args, "over_time")
				q.CountOnly = BoolArg(args, "count")
				q.Records = BoolArg(args, "record")
				if n := IntArg(args, "per_file"); n > 0 {
					q.PerFile = n
				}
				var err error
				if q.Bucket, err = bundle.ParseBucket(bundleStrArg(args, "bucket")); err != nil {
					return "", err
				}
				if q.Since, err = ParseBundleArgTime(bundleStrArg(args, "since")); err != nil {
					return "", err
				}
				if q.Until, err = ParseBundleArgTime(bundleStrArg(args, "until")); err != nil {
					return "", err
				}
				res, err := bundle.Open(owner, bundleID).Search(ctx, q)
				if err != nil {
					return "", err
				}
				// Paged like any long reply: the first page arrives, and
				// read_output reads on or greps within the kept hits.
				return SpillOutput(renderBundleSearch(res, q), bundle.SearchPageChars, "read_output"), nil
			},
		},
		{
			Tool: Tool{
				Name:        "read_bundle_file",
				Description: "Read a line range from one file in the bundle (paths as shown by list_bundle / search_bundle). Use it to read around a hit: the lines a search returned are rarely the whole story.",
				Parameters: map[string]ToolParam{
					"path":       {Type: "string", Description: "Bundle-relative file path, e.g. \"var/log/scheduler.log\"."},
					"start_line": {Type: "integer", Description: "1-based first line (default 1)."},
					"end_line":   {Type: "integer", Description: "1-based last line. Defaults to start_line + 200; a range wider than 2000 lines is refused: search instead."},
				},
				Required: []string{"path"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				if bundle.Open(owner, bundleID).FileCount() == 0 {
					return errBundleNotLoaded, nil
				}
				path := bundleStrArg(args, "path")
				start := IntArg(args, "start_line")
				end := IntArg(args, "end_line")
				if start <= 0 {
					start = 1
				}
				if end <= 0 {
					end = start + 200
				}
				if end-start+1 > bundle.SliceLines {
					return "", fmt.Errorf("that range is %d lines: read at most %d at a time, or use search_bundle to find the part that matters", end-start+1, bundle.SliceLines)
				}
				lines, bf, err := bundle.Open(owner, bundleID).ReadRange(path, start, end)
				if err != nil {
					return "", err
				}
				if bf.Lines == 0 {
					return fmt.Sprintf("%s is present in the bundle but was not ingested as text (%s). There is nothing to read.", bf.Path, bf.Format), nil
				}
				var b strings.Builder
				fmt.Fprintf(&b, "%s (%s lines total, %s)\n\n", bf.Path, HumanCount(bf.Lines), bundle.FormatSpan(bf))
				for i, ln := range lines {
					fmt.Fprintf(&b, "%d: %s\n", start+i, ln)
				}
				return b.String(), nil
			},
		},
		{
			Tool: Tool{
				Name:        "bundle_timeline",
				Description: "Merge lines from SEVERAL files into one time-ordered sequence. This is what no per-file read can give you: what happened across the whole system in a window, in order. Narrow it with a time window, and with pattern to follow one thing (a request id, a hostname, a job) through every file in order: an unbounded timeline over a large bundle is cut and tells you little.",
				Parameters: map[string]ToolParam{
					"pattern":   {Type: "string", Description: TimelinePatternHelp},
					"exclude":   {Type: "string", Description: TimelineExcludeHelp},
					"glob":      {Type: "string", Description: "Optional path filter limiting which files are merged. Empty merges every file that carries timestamps."},
					"since":     {Type: "string", Description: "Earliest timestamp, e.g. \"2026-03-14 02:00:00\". Strongly recommended."},
					"until":     {Type: "string", Description: "Latest timestamp, same format."},
					"max_lines": {Type: "integer", Description: fmt.Sprintf("Maximum lines to return (default %d).", bundle.MaxTimelineLines)},
				},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				if bundle.Open(owner, bundleID).FileCount() == 0 {
					return errBundleNotLoaded, nil
				}
				since, err := ParseBundleArgTime(bundleStrArg(args, "since"))
				if err != nil {
					return "", err
				}
				until, err := ParseBundleArgTime(bundleStrArg(args, "until"))
				if err != nil {
					return "", err
				}
				q := bundle.TimelineQuery{
					Pattern: bundleStrArg(args, "pattern"),
					Exclude: bundleStrArg(args, "exclude"),
					Glob:    bundleStrArg(args, "glob"),
					Since:   since,
					Until:   until,
					Max:     IntArg(args, "max_lines"),
				}
				res, err := bundle.Open(owner, bundleID).Timeline(ctx, q)
				if err != nil {
					return "", err
				}
				return renderBundleTimeline(res, q), nil
			},
		},
	}
}

// --- rendering ---

// renderBundleFileLine is one line of a bundle listing.
func renderBundleFileLine(bf bundle.File) string {
	switch bf.Format {
	case bundle.FormatArchive:
		return fmt.Sprintf("%s  [archive: no built-in expander; contents NOT ingested] %s\n", bf.Path, HumanSize(bf.Bytes))
	case bundle.FormatBinary:
		return fmt.Sprintf("%s  [binary: present, not ingested as text] %s\n", bf.Path, HumanSize(bf.Bytes))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s lines, %s, %s", bf.Path, HumanCount(bf.Lines), HumanSize(bf.Bytes), bf.Format)
	if bf.Host != "" {
		fmt.Fprintf(&b, ", host=%s", bf.Host)
	}
	fmt.Fprintf(&b, "  [%s]", bundle.FormatSpan(bf))
	if sev := renderSeverity(bf.Severity); sev != "" {
		fmt.Fprintf(&b, "  %s", sev)
	}
	b.WriteString("\n")
	return b.String()
}

// severityOrder is the fixed print order for a histogram, worst first, so two
// files' severity lines can be read against each other.
var severityOrder = []string{"EMERG", "ALERT", "CRIT", "FATAL", "ERROR", "WARN", "NOTICE", "INFO", "DEBUG", "TRACE"}

// renderSeverity prints the counts that are actually present, worst first.
func renderSeverity(sev map[string]int) string {
	if len(sev) == 0 {
		return ""
	}
	var parts []string
	for _, k := range severityOrder {
		if n := sev[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s=%s", k, HumanCount(n)))
		}
	}
	return strings.Join(parts, " ")
}

// RenderBundleSummary is the whole-bundle overview: scale, span, where the
// noise is, and what is present but unread.
func RenderBundleSummary(files []bundle.File) string {
	var (
		b            strings.Builder
		totalLines   int
		totalBytes   int64
		text         int
		binaries     int
		archives     int
		spanFirst    time.Time
		spanLast     time.Time
		inferredSpan bool
		agg          = map[string]int{}
		hosts        = map[string]bool{}
	)
	for _, bf := range files {
		totalBytes += bf.Bytes
		switch bf.Format {
		case bundle.FormatArchive:
			archives++
			continue
		case bundle.FormatBinary:
			binaries++
			continue
		}
		text++
		totalLines += bf.Lines
		for k, v := range bf.Severity {
			agg[k] += v
		}
		if bf.Host != "" {
			hosts[bf.Host] = true
		}
		if f, l, known := bundle.FileSpan(bf); known {
			if spanFirst.IsZero() || f.Before(spanFirst) {
				spanFirst = f
			}
			if spanLast.IsZero() || l.After(spanLast) {
				spanLast = l
			}
			if bf.YearInferred {
				inferredSpan = true
			}
		}
	}

	fmt.Fprintf(&b, "## Bundle contents\n\n%d files, %s total: %d ingested as text (%s lines), %d binary, %d unopened archives.\n\n",
		len(files), HumanSize(totalBytes), text, HumanCount(totalLines), binaries, archives)

	if !spanFirst.IsZero() {
		fmt.Fprintf(&b, "Time span: %s → %s\n", spanFirst.Format(time.RFC3339), spanLast.Format(time.RFC3339))
		if inferredSpan {
			b.WriteString("Some files use a timestamp format with no year; their year was inferred from the file's modification time. Treat those dates as approximate and say so if the answer turns on them.\n")
		}
		b.WriteString("\n")
	} else {
		b.WriteString("No parseable timestamps: nothing here can be placed on a timeline.\n\n")
	}

	if sev := renderSeverity(agg); sev != "" {
		fmt.Fprintf(&b, "Severity across the bundle: %s\n\n", sev)
	}
	if len(hosts) > 0 {
		names := make([]string, 0, len(hosts))
		for h := range hosts {
			names = append(names, h)
		}
		sort.Strings(names)
		fmt.Fprintf(&b, "Hosts named in the logs: %s\n\n", strings.Join(names, ", "))
	}

	// The files worth opening first: most errors, then largest.
	noisy := append([]bundle.File(nil), files...)
	sort.Slice(noisy, func(i, j int) bool {
		ei := noisy[i].Severity["ERROR"] + noisy[i].Severity["CRIT"] + noisy[i].Severity["FATAL"]
		ej := noisy[j].Severity["ERROR"] + noisy[j].Severity["CRIT"] + noisy[j].Severity["FATAL"]
		if ei != ej {
			return ei > ej
		}
		if noisy[i].Lines != noisy[j].Lines {
			return noisy[i].Lines > noisy[j].Lines
		}
		return noisy[i].Path < noisy[j].Path
	})
	b.WriteString("### Files most likely to matter\n\n")
	listed := 0
	for _, bf := range noisy {
		if bf.Format == bundle.FormatArchive || bf.Format == bundle.FormatBinary {
			continue
		}
		if listed >= 12 {
			break
		}
		b.WriteString(renderBundleFileLine(bf))
		listed++
	}
	if archives > 0 {
		b.WriteString("\nSome archives could not be opened by the built-in expanders (xz, 7z, zst, encrypted blobs). Their contents are NOT in the store: if the answer might be inside one, say so rather than concluding from what is here.\n")
	}
	return b.String()
}

// renderBundleSearch is a bundle search's reply (core/bundle render.go).
func renderBundleSearch(res bundle.SearchResult, q bundle.Query) string {
	return bundle.RenderSearch(res, q, "read_bundle_file")
}

// OverTimeHelp and BucketHelp describe the over-time search mode, shared by
// every search tool that offers it (a bundle's, a file store's) so each says
// the same thing about the same parameter.
const (
	OverTimeHelp = "Count the matching lines by time instead of listing them: one row per minute, hour or day with its count and a bar, from the first match to the last, empty rows in between shown as 0. Answers when something started, whether it is a spike or a trickle, and whether it stopped. Pattern, exclude and the time window still apply; context and paging do not, and it does not combine with group or count."
	BucketHelp   = "With over_time: the size of one row, e.g. \"30s\", \"5m\", \"1h\", \"1d\". Omit it and one is chosen to fit the span in about 60 rows."
)

// RecordHelp describes the whole-record parameter, shared by every search
// tool that offers it.
const RecordHelp = "Return each match's whole log entry instead of a fixed number of context lines: from the timestamped line it belongs to down to the line before the next timestamp. A match on an error message comes with its whole stack trace, and a match deep in a trace (\"Caused by\", a frame) comes with the dated line it belongs to. Matches inside one entry are shown once. An entry over 100 lines either way is cut and says where. Use it for exceptions, tracebacks and wrapped messages."

// PatternsHelp describes the several-patterns parameter, shared by every
// search tool that offers it.
const PatternsHelp = "Several regular expressions to look for in ONE pass, instead of pattern or with it, up to 8: e.g. [\"timeout\", \"connection refused\", \"out of memory\"]. Each is answered in its own section exactly as if searched alone (its own count, hits and limits), so a noisy one cannot crowd out a rare one, which a single \"a|b|c\" pattern lets happen. Every other parameter applies to each. Use it whenever you would otherwise search several times in a row."

// TimelinePatternHelp and TimelineExcludeHelp describe a timeline's line
// filters. Shared so every timeline tool (a bundle's, a file store's evidence)
// says the same thing about the same parameter.
const (
	TimelinePatternHelp = "Optional regular expression (case-insensitive): only lines matching it go on the timeline. Follows one thing through every file in time order, e.g. a request id \"req=7f3a\", a hostname, a job name."
	TimelineExcludeHelp = "Optional regular expression for lines to leave off: a line pattern kept is dropped when this matches it too, e.g. \"healthcheck\"."
)

// renderBundleTimeline prints a merged timeline and names the files that could
// not be merged into it.
func renderBundleTimeline(res bundle.TimelineResult, q bundle.TimelineQuery) string {
	var b strings.Builder
	entries := res.Entries
	if len(entries) == 0 {
		if q.Pattern != "" {
			fmt.Fprintf(&b, "No timestamped lines matching %q in that window.\n", q.Pattern)
		} else {
			b.WriteString("No timestamped lines in that window.\n")
		}
	} else {
		fmt.Fprintf(&b, "%d lines, time-ordered across %s:\n\n", len(entries), timelineFileCount(entries))
		for _, e := range entries {
			fmt.Fprintf(&b, "%s  %s:%d  %s\n", e.When.Format("2006-01-02 15:04:05"), e.Path, e.Line, e.Text)
		}
	}
	// The cut, said: a timeline keeps its EARLIEST lines, so one that was cut
	// stops partway through the story and has to say where to pick it up.
	if res.Total > len(entries) {
		fmt.Fprintf(&b, "\nCUT: these are the earliest %d of %d lines. To read on, repeat with since set to %s, or narrow with pattern or glob.\n",
			len(entries), res.Total, entries[len(entries)-1].When.Format("2006-01-02 15:04:05"))
	}
	if res.Excluded > 0 {
		fmt.Fprintf(&b, "%d more line(s) matched but were left out by exclude.\n", res.Excluded)
	}
	if res.Undated > 0 {
		fmt.Fprintf(&b, "%d line(s) matched the pattern but carry no timestamp of their own (a stack frame, a wrapped message), so they have no place on a timeline: search for them to read them in context.\n", res.Undated)
	}
	if len(res.Unmergeable) > 0 {
		unmergeable := append([]string(nil), res.Unmergeable...)
		sort.Strings(unmergeable)
		shown := unmergeable
		if len(shown) > 10 {
			shown = shown[:10]
		}
		fmt.Fprintf(&b, "\nNOT on this timeline (no parseable timestamps): %s", strings.Join(shown, ", "))
		if len(unmergeable) > len(shown) {
			fmt.Fprintf(&b, " and %d more", len(unmergeable)-len(shown))
		}
		b.WriteString("\nThose files may still contain the answer: search them directly.\n")
	}
	return b.String()
}

// timelineFileCount describes how many distinct files fed a timeline.
func timelineFileCount(entries []bundle.TimelineEntry) string {
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Path] = true
	}
	if len(seen) == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", len(seen))
}

// --- small argument helpers ---

// bundleStrArg reads a trimmed string argument.
//
// StringArg rather than a type assertion on string: a model that answers a
// glob with a number should get "123" and a useless match, not "" and a
// silent search of the whole bundle. Trimmed because every one of these is a
// pattern or a path, where a stray space changes the result.
func bundleStrArg(args map[string]any, key string) string {
	return strings.TrimSpace(StringArg(args, key))
}

// clampInt bounds n to [lo, hi].
func clampInt(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

// ParseBundleArgTime accepts the timestamp spellings a person actually types.
// An unparseable value is an ERROR rather than a silently ignored filter: a
// window that quietly did not apply turns "nothing in that hour" into a
// confident, wrong answer.
func ParseBundleArgTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{
		time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05",
		"2006-01-02 15:04", "2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("could not read %q as a time, use \"2026-03-14\", \"2026-03-14 02:00:00\", or a full RFC3339 timestamp", s)
}
