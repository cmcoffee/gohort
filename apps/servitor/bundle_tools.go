// bundle_tools.go — the Type=="bundle" worker probe tools. These replace the
// SSH run_command / read_log tools: instead of executing anything, the bundle
// worker reads the encrypted evidence store. The recording, plan, and knowledge
// tools servitor already has are reused unchanged.
//
// Every one of these is a LOCAL read, which is what qualifies them for the
// worker allow-list in tool_guard.go.
package servitor

import (
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// errBundleNotLoaded is returned when the store is empty — an unambiguous
// signal so the LLM treats it as "the evidence isn't available" (stop and
// report) rather than "this doesn't appear in the logs" (fabricate a negative).
const errBundleNotLoaded = "BUNDLE NOT INGESTED: this bundle's files are not currently in the store, so listing, search and read cannot run. This is NOT evidence that the thing you searched for is absent. Stop here and tell the user to upload the bundle's files. Do not guess at file names, timestamps, or contents."

// bundleListCap bounds one list_bundle response so a dump with fifty thousand
// files cannot spend the worker's whole context on a directory listing.
const bundleListCap = 300

// bundleCodeTools builds the read/search tools bound to one (user, bundle
// appliance), decrypting the store in memory.
func bundleCodeTools(user, applianceID string) []AgentToolDef {
	return []AgentToolDef{
		{
			Tool: Tool{
				Name:        "bundle_summary",
				Description: "Overview of the whole evidence bundle: how many files, what period they cover, which files are noisiest, and which are present but unread (binaries, archives nothing could open). ALWAYS call this first — it tells you what you are looking at and which file to search, without reading any log content.",
			},
			Handler: func(args map[string]any) (string, error) {
				files := bundleIndex(user, applianceID)
				if len(files) == 0 {
					return errBundleNotLoaded, nil
				}
				return renderBundleSummary(files), nil
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
			Handler: func(args map[string]any) (string, error) {
				files := bundleIndex(user, applianceID)
				if len(files) == 0 {
					return errBundleNotLoaded, nil
				}
				glob, _ := args["glob"].(string)
				glob = strings.TrimSpace(glob)
				var b strings.Builder
				shown := 0
				for _, bf := range files {
					if glob != "" && !matchBundleGlob(glob, bf.Path) {
						continue
					}
					if shown >= bundleListCap {
						fmt.Fprintf(&b, "(%d more files not shown — narrow the glob)\n", len(files)-shown)
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
					"pattern": {Type: "string", Description: "Regular expression to find, e.g. \"connection refused\", \"scheduler.*timeout\", \"req_id=abc123\"."},
					"glob":    {Type: "string", Description: "Optional path filter, e.g. \"*scheduler*\" or \"var/log/*.log\". Empty searches every file."},
					"before":  {Type: "integer", Description: "Context lines to include BEFORE each hit (default 0, max 20)."},
					"after":   {Type: "integer", Description: "Context lines to include AFTER each hit (default 0, max 20). Use ~10 to capture a stack trace under its message."},
					"since":   {Type: "string", Description: "Optional earliest timestamp, e.g. \"2026-03-14\" or \"2026-03-14 02:00:00\"."},
					"until":   {Type: "string", Description: "Optional latest timestamp, same formats as since."},
				},
				Required: []string{"pattern"},
			},
			Handler: func(args map[string]any) (string, error) {
				if bundleFileCount(user, applianceID) == 0 {
					return errBundleNotLoaded, nil
				}
				q := bundleQuery{
					Pattern: strArg(args, "pattern"),
					Glob:    strArg(args, "glob"),
					Before:  clampInt(repoIntArg(args, "before"), 0, 20),
					After:   clampInt(repoIntArg(args, "after"), 0, 20),
				}
				var err error
				if q.Since, err = parseBundleArgTime(strArg(args, "since")); err != nil {
					return "", err
				}
				if q.Until, err = parseBundleArgTime(strArg(args, "until")); err != nil {
					return "", err
				}
				res, err := searchBundle(user, applianceID, q)
				if err != nil {
					return "", err
				}
				return renderBundleSearch(res, q), nil
			},
		},
		{
			Tool: Tool{
				Name:        "read_bundle_file",
				Description: "Read a line range from one file in the bundle (paths as shown by list_bundle / search_bundle). Use it to read around a hit — the lines a search returned are rarely the whole story.",
				Parameters: map[string]ToolParam{
					"path":       {Type: "string", Description: "Bundle-relative file path, e.g. \"var/log/scheduler.log\"."},
					"start_line": {Type: "integer", Description: "1-based first line (default 1)."},
					"end_line":   {Type: "integer", Description: "1-based last line. Defaults to start_line + 200; a range wider than 2000 lines is refused — search instead."},
				},
				Required: []string{"path"},
			},
			Handler: func(args map[string]any) (string, error) {
				if bundleFileCount(user, applianceID) == 0 {
					return errBundleNotLoaded, nil
				}
				path := strArg(args, "path")
				start := repoIntArg(args, "start_line")
				end := repoIntArg(args, "end_line")
				if start <= 0 {
					start = 1
				}
				if end <= 0 {
					end = start + 200
				}
				if end-start+1 > bundleSliceLines {
					return "", fmt.Errorf("that range is %d lines — read at most %d at a time, or use search_bundle to find the part that matters", end-start+1, bundleSliceLines)
				}
				lines, bf, err := readBundleRange(user, applianceID, path, start, end)
				if err != nil {
					return "", err
				}
				if bf.Lines == 0 {
					return fmt.Sprintf("%s is present in the bundle but was not ingested as text (%s). There is nothing to read.", bf.Path, bf.Format), nil
				}
				var b strings.Builder
				fmt.Fprintf(&b, "%s (%s lines total, %s)\n\n", bf.Path, humanCount(bf.Lines), formatBundleSpan(bf))
				for i, ln := range lines {
					fmt.Fprintf(&b, "%d: %s\n", start+i, ln)
				}
				return b.String(), nil
			},
		},
		{
			Tool: Tool{
				Name:        "bundle_timeline",
				Description: "Merge lines from SEVERAL files into one time-ordered sequence. This is what no per-file read can give you: what happened across the whole system in a window, in order. Narrow it with a time window — an unbounded timeline over a large bundle is truncated and tells you little.",
				Parameters: map[string]ToolParam{
					"glob":      {Type: "string", Description: "Optional path filter limiting which files are merged. Empty merges every file that carries timestamps."},
					"since":     {Type: "string", Description: "Earliest timestamp, e.g. \"2026-03-14 02:00:00\". Strongly recommended."},
					"until":     {Type: "string", Description: "Latest timestamp, same format."},
					"max_lines": {Type: "integer", Description: fmt.Sprintf("Maximum lines to return (default %d).", bundleMaxTimelineLines)},
				},
			},
			Handler: func(args map[string]any) (string, error) {
				if bundleFileCount(user, applianceID) == 0 {
					return errBundleNotLoaded, nil
				}
				since, err := parseBundleArgTime(strArg(args, "since"))
				if err != nil {
					return "", err
				}
				until, err := parseBundleArgTime(strArg(args, "until"))
				if err != nil {
					return "", err
				}
				entries, unmergeable, err := bundleTimeline(user, applianceID, strArg(args, "glob"), since, until, repoIntArg(args, "max_lines"))
				if err != nil {
					return "", err
				}
				return renderBundleTimeline(entries, unmergeable), nil
			},
		},
	}
}

// --- rendering ---

// renderBundleFileLine is one line of a bundle listing.
func renderBundleFileLine(bf bundleFile) string {
	switch bf.Format {
	case bundleFormatArchive:
		return fmt.Sprintf("%s  [archive — no built-in expander; contents NOT ingested] %s\n", bf.Path, HumanSize(bf.Bytes))
	case bundleFormatBinary:
		return fmt.Sprintf("%s  [binary — present, not ingested as text] %s\n", bf.Path, HumanSize(bf.Bytes))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s lines, %s, %s", bf.Path, humanCount(bf.Lines), HumanSize(bf.Bytes), bf.Format)
	if bf.Host != "" {
		fmt.Fprintf(&b, ", host=%s", bf.Host)
	}
	fmt.Fprintf(&b, "  [%s]", formatBundleSpan(bf))
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
			parts = append(parts, fmt.Sprintf("%s=%s", k, humanCount(n)))
		}
	}
	return strings.Join(parts, " ")
}

// renderBundleSummary is the whole-bundle overview: scale, span, where the
// noise is, and what is present but unread.
func renderBundleSummary(files []bundleFile) string {
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
		case bundleFormatArchive:
			archives++
			continue
		case bundleFormatBinary:
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
		if f, l, known := bundleFileSpan(bf); known {
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
		len(files), HumanSize(totalBytes), text, humanCount(totalLines), binaries, archives)

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
	noisy := append([]bundleFile(nil), files...)
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
		if bf.Format == bundleFormatArchive || bf.Format == bundleFormatBinary {
			continue
		}
		if listed >= 12 {
			break
		}
		b.WriteString(renderBundleFileLine(bf))
		listed++
	}
	if archives > 0 {
		b.WriteString("\nSome archives could not be opened by the built-in expanders (xz, 7z, zst, encrypted blobs). Their contents are NOT in the store — if the answer might be inside one, say so rather than concluding from what is here.\n")
	}
	return b.String()
}

// renderBundleSearch prints hits, and says plainly what the search did not see.
func renderBundleSearch(res bundleSearchResult, q bundleQuery) string {
	if len(res.Hits) == 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "No matches for %q in %d files scanned.\n", q.Pattern, res.Scanned)
		if res.Skipped > 0 {
			fmt.Fprintf(&b, "%d files were excluded by the path filter or the time window.\n", res.Skipped)
		}
		if res.TimeUnknown > 0 {
			fmt.Fprintf(&b, "%d files were excluded because they carry no parseable timestamps and a time window was set — re-run without since/until to include them.\n", res.TimeUnknown)
		}
		return b.String()
	}
	var b strings.Builder
	withContext := q.Before > 0 || q.After > 0
	lastPath := ""
	for _, h := range res.Hits {
		if withContext {
			if h.Path != lastPath {
				fmt.Fprintf(&b, "\n=== %s ===\n", h.Path)
				lastPath = h.Path
			}
			b.WriteString(strings.Join(h.Context, "\n"))
			b.WriteString("\n--\n")
			continue
		}
		fmt.Fprintf(&b, "%s:%d: %s\n", h.Path, h.Line, h.Text)
	}
	fmt.Fprintf(&b, "\n%d matches across %d files scanned.\n", len(res.Hits), res.Scanned)
	if res.Truncated {
		b.WriteString("TRUNCATED — there are more matches than shown. Narrow the pattern, the glob, or the time window before drawing a conclusion about how often this happens.\n")
	}
	if res.TimeUnknown > 0 {
		fmt.Fprintf(&b, "%d files were skipped because they carry no parseable timestamps and a time window was set.\n", res.TimeUnknown)
	}
	return b.String()
}

// renderBundleTimeline prints a merged timeline and names the files that could
// not be merged into it.
func renderBundleTimeline(entries []bundleTimelineEntry, unmergeable []string) string {
	var b strings.Builder
	if len(entries) == 0 {
		b.WriteString("No timestamped lines in that window.\n")
	} else {
		fmt.Fprintf(&b, "%d lines, time-ordered across %s:\n\n", len(entries), timelineFileCount(entries))
		for _, e := range entries {
			fmt.Fprintf(&b, "%s  %s:%d  %s\n", e.When.Format("2006-01-02 15:04:05"), e.Path, e.Line, e.Text)
		}
	}
	if len(unmergeable) > 0 {
		sort.Strings(unmergeable)
		shown := unmergeable
		if len(shown) > 10 {
			shown = shown[:10]
		}
		fmt.Fprintf(&b, "\nNOT on this timeline (no parseable timestamps): %s", strings.Join(shown, ", "))
		if len(unmergeable) > len(shown) {
			fmt.Fprintf(&b, " and %d more", len(unmergeable)-len(shown))
		}
		b.WriteString("\nThose files may still contain the answer — search them directly.\n")
	}
	return b.String()
}

// timelineFileCount describes how many distinct files fed a timeline.
func timelineFileCount(entries []bundleTimelineEntry) string {
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

// strArg reads a trimmed string argument.
func strArg(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return strings.TrimSpace(s)
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

// parseBundleArgTime accepts the timestamp spellings a person actually types.
// An unparseable value is an ERROR rather than a silently ignored filter: a
// window that quietly did not apply turns "nothing in that hour" into a
// confident, wrong answer.
func parseBundleArgTime(s string) (time.Time, error) {
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
	return time.Time{}, fmt.Errorf("could not read %q as a time — use \"2026-03-14\", \"2026-03-14 02:00:00\", or a full RFC3339 timestamp", s)
}
