package orchestrate

// An agent's Rules are one block of text, read as a list of rules. A rule is
// one line, or a heading and the list under it:
//
//	PROBLEM INVESTIGATION:
//	1. Check the logs first.
//	2. Confirm with the user before restarting anything.
//
// is ONE rule. Read a line at a time, as it was everywhere, the heading came
// out as a rule of its own that said nothing ("PROBLEM INVESTIGATION:") and its
// steps as rules that no longer said what they were about. That is how a model
// writes a procedure, and how Builder wrote them. Every reader goes through
// ruleItems, so they agree on what one rule is. The text is stored as written,
// so rules saved before this read correctly without anybody editing them.

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	ruleMarkdownHeadingRe = regexp.MustCompile(`^#{1,6}\s`)
	ruleListLineRe        = regexp.MustCompile(`^(?:\d{1,3}[.)]|[-*+•])\s`)
)

// ruleHeading reports a line that heads a list rather than saying a rule: a
// markdown heading, or a line ending in a colon (bold or not).
func ruleHeading(line string) bool {
	line = strings.TrimSpace(line)
	if ruleMarkdownHeadingRe.MatchString(line) {
		return true
	}
	return strings.HasSuffix(strings.TrimRight(line, "*_ "), ":")
}

// ruleItems is rules as a list of rules, each its lines, trimmed: one line, or
// a heading followed by the list lines (numbered, bulleted or indented) under
// it. A blank line, or a line that is not a list line, ends a heading's list.
func ruleItems(rules string) [][]string {
	var out [][]string
	open := false // the last rule is a heading still taking list lines
	for _, raw := range strings.Split(rules, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			open = false
			continue
		}
		indented := raw[0] == ' ' || raw[0] == '\t'
		if open && (indented || ruleListLineRe.MatchString(line)) {
			out[len(out)-1] = append(out[len(out)-1], line)
			continue
		}
		out = append(out, []string{line})
		open = ruleHeading(line)
	}
	return out
}

// rulesProblem refuses Rules holding a heading with nothing under it: a rule
// that says nothing, and the shape a procedure takes when its steps were left
// out or written as plain sentences the heading cannot claim. Nil when every
// rule says something.
func rulesProblem(rules string) error {
	for _, item := range ruleItems(rules) {
		if len(item) == 1 && ruleHeading(item[0]) {
			return fmt.Errorf("rules: %q is a heading with no list under it, so it would be a rule that says nothing. "+
				"Put the rules it heads under it as a list (\"1. ...\" or \"- ...\"), fold it into each rule "+
				"(\"When investigating a problem, check the logs first\"), or drop it", item[0])
		}
	}
	return nil
}
