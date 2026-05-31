// Workspace file query helpers — head / tail / lines / grep / stat.
// All share the same path-resolution + symlink-safety as the
// ReadFileTool / ListDirectoryTool in files.go. Designed so a tool
// result that spilled to disk can be queried in small targeted slices
// without re-injecting the whole body into LLM history.
//
// All helpers cap their own output at maxQueryReturnBytes (32 KB) so
// no single query can blow context on a degenerate input (e.g.
// grep-pattern that matches every line of a 50 MiB log).
//
// Naming: <verb>FileWS to keep package-qualified call sites readable
// (`files.HeadFileWS(...)` reads as "head file in workspace") and to
// not collide with similarly named exports the host packages may grow.

package files

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// maxQueryReturnBytes caps any query-helper's return string. Smaller
// than maxReadBytes on purpose — queries are meant to be a TARGETED
// slice; if the result feels close to a full file, the caller chose
// the wrong tool.
const maxQueryReturnBytes = 32 * 1024

// maxGrepMatches caps grep matches per call. Without a cap a pattern
// like "." would return every line; the caller can re-call with a
// tighter pattern or pipe through awk via run if they truly need it.
const maxGrepMatches = 200

// HeadFileWS returns the first n lines of a workspace file.
// n<=0 defaults to 50.
func HeadFileWS(sess *ToolSession, rel string, n int) (string, error) {
	if n <= 0 {
		n = 50
	}
	abs, err := resolveFor(sess, rel)
	if err != nil {
		return "", err
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", fmt.Errorf("open %q: %w", rel, err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var b strings.Builder
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		writeLine(&b, lineNo, scanner.Text())
		if lineNo >= n || b.Len() >= maxQueryReturnBytes {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read %q: %w", rel, err)
	}
	out := b.String()
	if b.Len() >= maxQueryReturnBytes {
		out += "\n... [output cap; ask for a narrower slice with read_lines]"
	}
	return out, nil
}

// TailFileWS returns the last n lines of a workspace file.
// n<=0 defaults to 50.
func TailFileWS(sess *ToolSession, rel string, n int) (string, error) {
	if n <= 0 {
		n = 50
	}
	abs, err := resolveFor(sess, rel)
	if err != nil {
		return "", err
	}
	// Stream-and-ring-buffer is simpler + memory-bounded vs. seeking
	// from the end and parsing backwards. Worst case we scan the
	// whole file; for the sizes we deal with (tool spills ≤ a few
	// MiB) this is fine.
	f, err := os.Open(abs)
	if err != nil {
		return "", fmt.Errorf("open %q: %w", rel, err)
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
		return "", fmt.Errorf("read %q: %w", rel, err)
	}
	startLine := total - len(ring) + 1
	if startLine < 1 {
		startLine = 1
	}
	var b strings.Builder
	for i, ln := range ring {
		writeLine(&b, startLine+i, ln)
		if b.Len() >= maxQueryReturnBytes {
			b.WriteString("\n... [output cap; ask for a narrower slice with read_lines]")
			break
		}
	}
	return b.String(), nil
}

// ReadLinesWS returns lines [start, end] (inclusive, 1-indexed) from
// a workspace file. start<=0 is treated as 1; end<=0 means "to EOF
// but capped at start+1000 to prevent runaway slices".
func ReadLinesWS(sess *ToolSession, rel string, start, end int) (string, error) {
	if start <= 0 {
		start = 1
	}
	if end <= 0 || end-start > 1000 {
		end = start + 1000
	}
	if end < start {
		return "", fmt.Errorf("end line %d is before start %d", end, start)
	}
	abs, err := resolveFor(sess, rel)
	if err != nil {
		return "", err
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", fmt.Errorf("open %q: %w", rel, err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNo := 0
	var b strings.Builder
	for scanner.Scan() {
		lineNo++
		if lineNo < start {
			continue
		}
		if lineNo > end {
			break
		}
		writeLine(&b, lineNo, scanner.Text())
		if b.Len() >= maxQueryReturnBytes {
			b.WriteString("\n... [output cap; ask for a narrower slice]")
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read %q: %w", rel, err)
	}
	if b.Len() == 0 {
		return fmt.Sprintf("[no lines in range %d–%d; file has %d line(s)]", start, end, lineNo), nil
	}
	return b.String(), nil
}

// GrepFileWS returns matching lines from a workspace file. pattern
// is a Go regexp. context lines surround each match (0 = match line
// only). Caller can pass maxMatches=0 for the default cap.
func GrepFileWS(sess *ToolSession, rel, pattern string, context, maxMatches int) (string, error) {
	if strings.TrimSpace(pattern) == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if context < 0 {
		context = 0
	}
	if context > 5 {
		context = 5 // cap context per match; the caller can read_lines for a wider window
	}
	if maxMatches <= 0 {
		maxMatches = maxGrepMatches
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid pattern: %w", err)
	}
	abs, err := resolveFor(sess, rel)
	if err != nil {
		return "", err
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", fmt.Errorf("open %q: %w", rel, err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	// Ring of recent lines for the "context before match" window.
	before := make([]string, 0, context)
	// Pending afters: when a match fires we owe `context` more lines.
	pendingAfter := 0
	pendingMatchLine := 0

	var b strings.Builder
	matches := 0
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		text := scanner.Text()
		switch {
		case re.MatchString(text):
			// Emit a separator between non-contiguous match groups.
			if matches > 0 && pendingAfter == 0 {
				b.WriteString("--\n")
			}
			// Flush "before" context.
			startOfBefore := lineNo - len(before)
			for i, ln := range before {
				writeMatchLine(&b, startOfBefore+i, ln, false)
			}
			before = before[:0]
			writeMatchLine(&b, lineNo, text, true)
			matches++
			pendingAfter = context
			pendingMatchLine = lineNo
		case pendingAfter > 0:
			writeMatchLine(&b, lineNo, text, false)
			pendingAfter--
			_ = pendingMatchLine
		default:
			if context > 0 {
				before = append(before, text)
				if len(before) > context {
					before = before[1:]
				}
			}
		}
		if matches >= maxMatches {
			b.WriteString(fmt.Sprintf("\n... [match cap %d reached; tighten pattern or re-call with a smaller context]\n", maxMatches))
			break
		}
		if b.Len() >= maxQueryReturnBytes {
			b.WriteString("\n... [output cap; tighten pattern or reduce context]\n")
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read %q: %w", rel, err)
	}
	if matches == 0 {
		return fmt.Sprintf("[no matches for %q in %s (scanned %d line(s))]", pattern, rel, lineNo), nil
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// StatFileWS returns size, mtime, line count, and a content sniff
// for a workspace file. The sniff distinguishes "looks like JSON",
// "looks like log lines", "looks like CSV", "looks like binary",
// "looks like plain text" — enough for the LLM to pick the right
// follow-up tool.
func StatFileWS(sess *ToolSession, rel string) (string, error) {
	abs, err := resolveFor(sess, rel)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("stat %q: %w", rel, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%q is a directory; use ls", rel)
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", fmt.Errorf("open %q: %w", rel, err)
	}
	defer f.Close()
	// Read up to first 4 KB for the content sniff + count lines.
	const sniffBytes = 4096
	sniff := make([]byte, sniffBytes)
	read, _ := f.Read(sniff)
	sniff = sniff[:read]
	kind := sniffKind(sniff)

	// Line count via streaming scan from the start. For huge files
	// (>50 MB) this becomes expensive — use the byte count divided
	// by an average-line-length heuristic as an upper bound instead.
	lineCount := -1
	if info.Size() < 50*1024*1024 {
		if _, err := f.Seek(0, 0); err == nil {
			scanner := bufio.NewScanner(f)
			scanner.Buffer(make([]byte, 64*1024), 1024*1024)
			n := 0
			for scanner.Scan() {
				n++
			}
			if scanner.Err() == nil {
				lineCount = n
			}
		}
	}
	var firstLine string
	if newline := strings.IndexByte(string(sniff), '\n'); newline >= 0 {
		firstLine = strings.TrimSpace(string(sniff[:newline]))
	} else {
		firstLine = strings.TrimSpace(string(sniff))
	}
	if len(firstLine) > 200 {
		firstLine = firstLine[:200] + "…"
	}
	lineCountStr := "(unknown — file too large to count cheaply)"
	if lineCount >= 0 {
		lineCountStr = fmt.Sprintf("%d", lineCount)
	}
	return fmt.Sprintf(
		"path: %s\nsize: %d bytes\nmtime: %s\nlines: %s\nkind: %s\nfirst_line: %q",
		rel, info.Size(), info.ModTime().Format("2006-01-02 15:04:05"),
		lineCountStr, kind, firstLine,
	), nil
}

// sniffKind classifies a file by its first 4 KB. Heuristic-only —
// the LLM should treat it as a hint, not a guarantee.
func sniffKind(sniff []byte) string {
	if len(sniff) == 0 {
		return "empty"
	}
	// Binary check first — a NUL byte in the first 4 KB rules out
	// text classifications.
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
		if strings.HasPrefix(strings.ToLower(s), "<!doctype html") || strings.HasPrefix(strings.ToLower(s), "<html") {
			return "html"
		}
		return "xml-like"
	}
	// Log-line heuristic: many lines start with a timestamp-ish prefix.
	lines := strings.SplitN(s, "\n", 6)
	tsy := 0
	for _, ln := range lines {
		if logLinePrefixRE.MatchString(ln) {
			tsy++
		}
	}
	if tsy >= 3 {
		return "log-lines"
	}
	// CSV heuristic: first line has commas + balanced field counts on
	// next few lines.
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

// logLinePrefixRE matches the loose pattern most log libraries use
// for line prefixes: a date or time-of-day token at the start.
var logLinePrefixRE = regexp.MustCompile(`^(\[?\d{4}-\d{2}-\d{2}|\[?\d{2}:\d{2}:\d{2})`)

// resolveFor centralizes path resolution + symlink-safety + the
// "needs a session with WorkspaceDir set" precondition every query
// helper shares.
func resolveFor(sess *ToolSession, rel string) (string, error) {
	if sess == nil || sess.WorkspaceDir == "" {
		return "", fmt.Errorf("requires a session with WorkspaceDir set")
	}
	abs, err := ResolveWorkspacePath(sess.WorkspaceDir, rel)
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	return abs, nil
}

// writeLine renders a normal-listing line: "<no>: <text>". Matches
// grep/awk -n output so the LLM reads it as "this is line N".
func writeLine(b *strings.Builder, no int, text string) {
	fmt.Fprintf(b, "%d: %s\n", no, text)
}

// writeMatchLine renders a grep-style line. Match lines get a ':'
// after the line number, context lines get a '-' — matches GNU grep
// -C output verbatim so a human re-running the query gets the same
// shape, and so context vs. match is unambiguous.
func writeMatchLine(b *strings.Builder, no int, text string, isMatch bool) {
	sep := "-"
	if isMatch {
		sep = ":"
	}
	fmt.Fprintf(b, "%d%s%s\n", no, sep, text)
}
