package editmatch

// What a missed edit says. "old_text was not found" alone sent the agent
// back to guess again: it had written an anchor from memory (two blocks it
// thought were neighbours, a line it misremembered), and nothing told it
// which part was wrong or where the real lines are. So a miss now shows the
// file as it is where the anchor's own lines are found, numbered, to copy
// from, and says when only spacing differs.

import (
	"fmt"
	"strings"
)

// hintLines bounds the file shown back for a missed anchor.
const hintLines = 24

// Hint is what to say after old_text was not found in content: where
// its first and last lines are, with the file's real text around them.
func Hint(content, oldText string) string {
	lines := strings.Split(content, "\n")
	if at := findCollapsed(content, oldText); at >= 0 {
		line := strings.Count(content[:at], "\n") + 1
		return fmt.Sprintf(" It matches apart from spaces, tabs or line breaks at line %d: copy the anchor from these lines exactly.\n%s",
			line, numbered(lines, line, strings.Count(strings.TrimSpace(oldText), "\n")+1))
	}
	want := anchorLines(oldText)
	if len(want) == 0 {
		return " Read the file again and copy the anchor exactly."
	}
	first, last := want[0], want[len(want)-1]
	fAt, lAt := findLine(lines, first), findLine(lines, last)
	var b strings.Builder
	switch {
	case fAt < 0 && lAt < 0:
		b.WriteString(" Neither its first nor its last line is in the file, so the anchor is not text the file holds now. Search for what you mean to change, read those lines, and copy the anchor from them.")
		return b.String()
	case fAt >= 0 && lAt >= 0 && lAt >= fAt:
		fmt.Fprintf(&b, " Its first line is at line %d and its last at line %d, but the lines between differ from the file's. Here they are as the file has them:\n", fAt+1, lAt+1)
		b.WriteString(numbered(lines, fAt+1, lAt-fAt+1))
	case fAt >= 0 && lAt >= 0:
		fmt.Fprintf(&b, " Its first line is at line %d but its last is at line %d, before it: those are not neighbours in the file. Anchor on one place at a time:\n", fAt+1, lAt+1)
		b.WriteString(numbered(lines, fAt+1, len(want)))
		b.WriteString("\n...\n")
		b.WriteString(numbered(lines, lAt+1-len(want)+1, len(want)))
	case fAt >= 0:
		fmt.Fprintf(&b, " Its first line is at line %d, but what follows it differs. The file there:\n", fAt+1)
		b.WriteString(numbered(lines, fAt+1, len(want)+2))
	default:
		fmt.Fprintf(&b, " Its last line is at line %d, but what comes before it differs. The file there:\n", lAt+1)
		b.WriteString(numbered(lines, lAt+1-len(want)-1, len(want)+2))
	}
	return b.String()
}

// anchorLines are old_text's lines that say something: blank lines and a
// lone brace say nothing about where the anchor is.
func anchorLines(oldText string) []string {
	var out []string
	for _, l := range strings.Split(oldText, "\n") {
		t := strings.TrimSpace(l)
		if len(t) < 4 {
			continue
		}
		out = append(out, t)
	}
	return out
}

// findLine is the first line of lines whose text, trimmed, is want; -1 when
// none is.
func findLine(lines []string, want string) int {
	for i, l := range lines {
		if strings.TrimSpace(l) == want {
			return i
		}
	}
	return -1
}

// findCollapsed is where oldText is in content when every run of whitespace
// in both is read as one space: the byte offset in content, -1 when not.
func findCollapsed(content, oldText string) int {
	want := strings.Join(strings.Fields(oldText), " ")
	if want == "" {
		return -1
	}
	// Walk content's words, keeping each one's offset, and look for want's
	// words in a row.
	var words []string
	var offs []int
	inWord := false
	for i, r := range content {
		space := r == ' ' || r == '\t' || r == '\n' || r == '\r'
		if !space && !inWord {
			offs = append(offs, i)
		}
		inWord = !space
	}
	words = strings.Fields(content)
	n := len(strings.Fields(want))
	for i := 0; i+n <= len(words); i++ {
		if strings.Join(words[i:i+n], " ") == want {
			return offs[i]
		}
	}
	return -1
}

// numbered is count lines of lines from line (1-based), numbered the way
// read_file numbers them, capped at hintLines.
func numbered(lines []string, line, count int) string {
	if line < 1 {
		line = 1
	}
	if count > hintLines {
		count = hintLines
	}
	var b strings.Builder
	for i := line; i < line+count && i <= len(lines); i++ {
		fmt.Fprintf(&b, "%6d\t%s\n", i, lines[i-1])
	}
	return strings.TrimRight(b.String(), "\n")
}
