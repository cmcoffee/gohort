// Filesystem query tools — stat, head, tail, read_lines, grep —
// that run on the desktop host and ship only the small targeted
// slice across the WS bridge to the server. The right answer for
// "read this 200 MiB log" is NOT to send 200 MiB; it's to run the
// query where the file lives and return the matches.
//
// All five share the same read-allowlist as filesystem.read_local_file
// (core.PathAllowed). Adding a folder via "Add Allowed Folder…" once
// makes it queryable by every tool here.

package filesystem

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
)

// MAX_QUERY_RETURN_BYTES caps any single query response. Keeps a
// degenerate input (e.g. grep="." against a giant log) from blowing
// the WS payload size. Mirrors the server-side workspace query cap
// so the LLM sees consistent ceilings whether the file lives on the
// server or the client.
const MAX_QUERY_RETURN_BYTES = 32 * 1024

// MAX_GREP_MATCHES caps grep matches per call. Same reasoning as
// the byte cap — protects against degenerate patterns.
const MAX_GREP_MATCHES = 200

func init() {
	core.RegisterTool(&stat_file_tool{})
	core.RegisterTool(&head_file_tool{})
	core.RegisterTool(&tail_file_tool{})
	core.RegisterTool(&read_lines_tool{})
	core.RegisterTool(&grep_file_tool{})
}

// --- shared path resolution ---

// resolve_query_path normalizes + symlink-resolves + allowlist-checks
// a path. All five tools call this BEFORE opening the file so the
// allowlist + symlink-safety rules are enforced in one place.
func resolve_query_path(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	if !core.PathAllowedOrConsent(abs) {
		return "", fmt.Errorf("refused: %s is not under an allowed read root (allowed: %v) — operator can add a root via Account → Add Allowed Folder…", abs, core.AllowedReadRoots())
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("stat: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory — use filesystem.list_directory", abs)
	}
	return abs, nil
}

// --- filesystem.stat_file ---

type stat_file_tool struct{}

func (t *stat_file_tool) Name() string { return "filesystem.stat_file" }

func (t *stat_file_tool) Desc() string {
	return "Inspect a file on the host filesystem of the connected gohort-desktop: " +
		"size, mtime, line count, and a kind hint (json-like / log-lines / " +
		"csv-like / html / xml-like / text / binary / empty). Call FIRST " +
		"when you don't know what's in a file — the kind hint tells you " +
		"which query tool to reach for next (json-like → grep for the " +
		"field; log-lines → tail; csv-like → head for the header)."
}

func (t *stat_file_tool) Params() map[string]core.ToolParam {
	return map[string]core.ToolParam{
		"path": {Type: "string", Description: "Absolute path to inspect. Must resolve under one of the allowlisted roots."},
	}
}

func (t *stat_file_tool) Required() []string { return []string{"path"} }
func (t *stat_file_tool) Enabled() bool      { return true }

func (t *stat_file_tool) Handler() core.ToolHandler {
	return func(args map[string]any) (string, error) {
		path, _ := args["path"].(string)
		abs, err := resolve_query_path(path)
		if err != nil {
			return "", fmt.Errorf("filesystem.stat_file: %w", err)
		}
		return format_stat(abs)
	}
}

// --- filesystem.head_file ---

type head_file_tool struct{}

func (t *head_file_tool) Name() string { return "filesystem.head_file" }

func (t *head_file_tool) Desc() string {
	return "Return the first N lines of a file on the gohort-desktop host " +
		"(default 50). Cheap targeted slice — the right answer when you " +
		"need the start of a log, the header row of a CSV, or just want " +
		"to see the shape of a file without dragging the whole thing " +
		"across the bridge."
}

func (t *head_file_tool) Params() map[string]core.ToolParam {
	return map[string]core.ToolParam{
		"path":  {Type: "string", Description: "Absolute path to read."},
		"lines": {Type: "integer", Description: "Number of lines (default 50)."},
	}
}

func (t *head_file_tool) Required() []string { return []string{"path"} }
func (t *head_file_tool) Enabled() bool      { return true }

func (t *head_file_tool) Handler() core.ToolHandler {
	return func(args map[string]any) (string, error) {
		path, _ := args["path"].(string)
		n := int_arg_or(args, "lines", 50)
		abs, err := resolve_query_path(path)
		if err != nil {
			return "", fmt.Errorf("filesystem.head_file: %w", err)
		}
		return run_head(abs, n)
	}
}

// --- filesystem.tail_file ---

type tail_file_tool struct{}

func (t *tail_file_tool) Name() string { return "filesystem.tail_file" }

func (t *tail_file_tool) Desc() string {
	return "Return the last N lines of a file on the gohort-desktop host " +
		"(default 50). Use for log tails, recently-appended output, the " +
		"final summary of a tool that streamed to a file."
}

func (t *tail_file_tool) Params() map[string]core.ToolParam {
	return map[string]core.ToolParam{
		"path":  {Type: "string", Description: "Absolute path to read."},
		"lines": {Type: "integer", Description: "Number of lines (default 50)."},
	}
}

func (t *tail_file_tool) Required() []string { return []string{"path"} }
func (t *tail_file_tool) Enabled() bool      { return true }

func (t *tail_file_tool) Handler() core.ToolHandler {
	return func(args map[string]any) (string, error) {
		path, _ := args["path"].(string)
		n := int_arg_or(args, "lines", 50)
		abs, err := resolve_query_path(path)
		if err != nil {
			return "", fmt.Errorf("filesystem.tail_file: %w", err)
		}
		return run_tail(abs, n)
	}
}

// --- filesystem.read_file_range ---

type read_lines_tool struct{}

func (t *read_lines_tool) Name() string { return "filesystem.read_file_range" }

func (t *read_lines_tool) Desc() string {
	return "Return a specific line range from a file on the gohort-desktop " +
		"host. 1-indexed, inclusive. Use after stat or grep tells you where " +
		"to look — lets you pull exactly the slice you need without dragging " +
		"the rest into context."
}

func (t *read_lines_tool) Params() map[string]core.ToolParam {
	return map[string]core.ToolParam{
		"path":  {Type: "string", Description: "Absolute path to read."},
		"start": {Type: "integer", Description: "First line to return (1-indexed)."},
		"end":   {Type: "integer", Description: "Last line to return (inclusive). Capped at start+1000."},
	}
}

func (t *read_lines_tool) Required() []string { return []string{"path", "start"} }
func (t *read_lines_tool) Enabled() bool      { return true }

func (t *read_lines_tool) Handler() core.ToolHandler {
	return func(args map[string]any) (string, error) {
		path, _ := args["path"].(string)
		start := int_arg_or(args, "start", 0)
		end := int_arg_or(args, "end", 0)
		abs, err := resolve_query_path(path)
		if err != nil {
			return "", fmt.Errorf("filesystem.read_file_range: %w", err)
		}
		return run_read_lines(abs, start, end)
	}
}

// --- filesystem.grep_file ---

type grep_file_tool struct{}

func (t *grep_file_tool) Name() string { return "filesystem.grep_file" }

func (t *grep_file_tool) Desc() string {
	return "Search a file on the gohort-desktop host for a regex pattern " +
		"(RE2 syntax — Go's regexp, NO PCRE-only constructs). Returns " +
		"matching lines with line numbers; optional `context` adds lines " +
		"before/after each match (max 5). Right tool for finding specific " +
		"entries inside a huge log or config — the matches come back, the " +
		"whole file never crosses the bridge."
}

func (t *grep_file_tool) Params() map[string]core.ToolParam {
	return map[string]core.ToolParam{
		"path":        {Type: "string", Description: "Absolute path to search."},
		"pattern":     {Type: "string", Description: "RE2 regex matched against each line."},
		"context":     {Type: "integer", Description: "Lines of context before and after each match (default 0, max 5)."},
		"max_matches": {Type: "integer", Description: "Stop after this many matches (default 200)."},
	}
}

func (t *grep_file_tool) Required() []string { return []string{"path", "pattern"} }
func (t *grep_file_tool) Enabled() bool      { return true }

func (t *grep_file_tool) Handler() core.ToolHandler {
	return func(args map[string]any) (string, error) {
		path, _ := args["path"].(string)
		pattern, _ := args["pattern"].(string)
		context := int_arg_or(args, "context", 0)
		max_matches := int_arg_or(args, "max_matches", 0)
		abs, err := resolve_query_path(path)
		if err != nil {
			return "", fmt.Errorf("filesystem.grep_file: %w", err)
		}
		return run_grep(abs, pattern, context, max_matches)
	}
}

// --- implementations ---
//
// These mirror the server-side workspace query implementations
// (tools/files/query.go) but resolve against the host filesystem
// instead of a workspace dir. Output formatting is intentionally
// IDENTICAL — "N: text" for plain lines, "N:text" / "N-text" for
// grep-style match/context — so the LLM sees a consistent shape no
// matter where the file lives.

func format_stat(abs string) (string, error) {
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("stat: %w", err)
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", fmt.Errorf("open: %w", err)
	}
	defer f.Close()
	const sniff_bytes = 4096
	sniff := make([]byte, sniff_bytes)
	read, _ := f.Read(sniff)
	sniff = sniff[:read]
	kind := sniff_kind(sniff)
	line_count := -1
	if info.Size() < 50*1024*1024 {
		if _, err := f.Seek(0, 0); err == nil {
			scanner := bufio.NewScanner(f)
			scanner.Buffer(make([]byte, 64*1024), 1024*1024)
			n := 0
			for scanner.Scan() {
				n++
			}
			if scanner.Err() == nil {
				line_count = n
			}
		}
	}
	var first_line string
	if newline := strings.IndexByte(string(sniff), '\n'); newline >= 0 {
		first_line = strings.TrimSpace(string(sniff[:newline]))
	} else {
		first_line = strings.TrimSpace(string(sniff))
	}
	if len(first_line) > 200 {
		first_line = first_line[:200] + "…"
	}
	line_count_str := "(unknown — file too large to count cheaply)"
	if line_count >= 0 {
		line_count_str = fmt.Sprintf("%d", line_count)
	}
	return fmt.Sprintf(
		"path: %s\nsize: %d bytes\nmtime: %s\nlines: %s\nkind: %s\nfirst_line: %q",
		abs, info.Size(), info.ModTime().Format("2006-01-02 15:04:05"),
		line_count_str, kind, first_line,
	), nil
}

func run_head(abs string, n int) (string, error) {
	if n <= 0 {
		n = 50
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", fmt.Errorf("open: %w", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var b strings.Builder
	line_no := 0
	for scanner.Scan() {
		line_no++
		fmt.Fprintf(&b, "%d: %s\n", line_no, scanner.Text())
		if line_no >= n || b.Len() >= MAX_QUERY_RETURN_BYTES {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	out := b.String()
	if b.Len() >= MAX_QUERY_RETURN_BYTES {
		out += "\n... [output cap; ask for a narrower slice with read_file_range]"
	}
	return out, nil
}

func run_tail(abs string, n int) (string, error) {
	if n <= 0 {
		n = 50
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", fmt.Errorf("open: %w", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	ring := make([]string, 0, n+1)
	total := 0
	for scanner.Scan() {
		total++
		ring = append(ring, scanner.Text())
		if len(ring) > n {
			ring = ring[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	start_line := total - len(ring) + 1
	if start_line < 1 {
		start_line = 1
	}
	var b strings.Builder
	for i, ln := range ring {
		fmt.Fprintf(&b, "%d: %s\n", start_line+i, ln)
		if b.Len() >= MAX_QUERY_RETURN_BYTES {
			b.WriteString("\n... [output cap; ask for a narrower slice with read_file_range]")
			break
		}
	}
	return b.String(), nil
}

func run_read_lines(abs string, start, end int) (string, error) {
	if start <= 0 {
		start = 1
	}
	if end <= 0 || end-start > 1000 {
		end = start + 1000
	}
	if end < start {
		return "", fmt.Errorf("end line %d is before start %d", end, start)
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", fmt.Errorf("open: %w", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	line_no := 0
	var b strings.Builder
	for scanner.Scan() {
		line_no++
		if line_no < start {
			continue
		}
		if line_no > end {
			break
		}
		fmt.Fprintf(&b, "%d: %s\n", line_no, scanner.Text())
		if b.Len() >= MAX_QUERY_RETURN_BYTES {
			b.WriteString("\n... [output cap; ask for a narrower slice]")
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	if b.Len() == 0 {
		return fmt.Sprintf("[no lines in range %d–%d; file has %d line(s)]", start, end, line_no), nil
	}
	return b.String(), nil
}

func run_grep(abs, pattern string, context, max_matches int) (string, error) {
	if strings.TrimSpace(pattern) == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if context < 0 {
		context = 0
	}
	if context > 5 {
		context = 5
	}
	if max_matches <= 0 {
		max_matches = MAX_GREP_MATCHES
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid pattern: %w", err)
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", fmt.Errorf("open: %w", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	before := make([]string, 0, context)
	pending_after := 0
	var b strings.Builder
	matches := 0
	line_no := 0
	for scanner.Scan() {
		line_no++
		text := scanner.Text()
		switch {
		case re.MatchString(text):
			if matches > 0 && pending_after == 0 {
				b.WriteString("--\n")
			}
			start_of_before := line_no - len(before)
			for i, ln := range before {
				fmt.Fprintf(&b, "%d-%s\n", start_of_before+i, ln)
			}
			before = before[:0]
			fmt.Fprintf(&b, "%d:%s\n", line_no, text)
			matches++
			pending_after = context
		case pending_after > 0:
			fmt.Fprintf(&b, "%d-%s\n", line_no, text)
			pending_after--
		default:
			if context > 0 {
				before = append(before, text)
				if len(before) > context {
					before = before[1:]
				}
			}
		}
		if matches >= max_matches {
			fmt.Fprintf(&b, "\n... [match cap %d reached; tighten pattern or reduce context]\n", max_matches)
			break
		}
		if b.Len() >= MAX_QUERY_RETURN_BYTES {
			b.WriteString("\n... [output cap; tighten pattern or reduce context]\n")
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	if matches == 0 {
		return fmt.Sprintf("[no matches for %q in %s (scanned %d line(s))]", pattern, abs, line_no), nil
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// sniff_kind mirrors the server-side equivalent in tools/files/query.go.
// Kept separate (rather than importing) so the desktop binary stays
// independently buildable — no transitive pull of the server packages.
func sniff_kind(sniff []byte) string {
	if len(sniff) == 0 {
		return "empty"
	}
	for _, b := range sniff {
		if b == 0 {
			return "binary"
		}
	}
	s := strings.TrimSpace(string(sniff))
	if len(s) == 0 {
		return "whitespace-only"
	}
	switch s[0] {
	case '{', '[':
		return "json-like"
	case '<':
		lower := strings.ToLower(s)
		if strings.HasPrefix(lower, "<!doctype html") || strings.HasPrefix(lower, "<html") {
			return "html"
		}
		return "xml-like"
	}
	lines := strings.SplitN(s, "\n", 6)
	tsy := 0
	for _, ln := range lines {
		if log_line_prefix_re.MatchString(ln) {
			tsy++
		}
	}
	if tsy >= 3 {
		return "log-lines"
	}
	if strings.Count(lines[0], ",") >= 2 {
		fields := strings.Count(lines[0], ",")
		balanced := 0
		for i := 1; i < len(lines); i++ {
			if strings.Count(lines[i], ",") == fields {
				balanced++
			}
		}
		if balanced >= 2 {
			return "csv-like"
		}
	}
	return "text"
}

var log_line_prefix_re = regexp.MustCompile(`^(\[?\d{4}-\d{2}-\d{2}|\[?\d{2}:\d{2}:\d{2})`)

// int_arg_or extracts an integer-valued arg, tolerating the int /
// float64 / numeric-string shapes LLMs commonly emit. Returns def on
// missing/invalid input.
func int_arg_or(args map[string]any, key string, def int) int {
	v, ok := args[key]
	if !ok || v == nil {
		return def
	}
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(t), "%d", &n); err == nil {
			return n
		}
	}
	return def
}
