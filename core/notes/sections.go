// Sections: a register the agent names, inside the one notes document.
//
// See docs/working-notes-sections.md. An agent could already invent structure
// here — a heading is just text — but not update one part without rewriting all
// of it, because update_notes replaces the whole block. Rewriting a document to
// change one line is how a model drops the rest of it.
//
// So a section is a `## ` heading and nothing else: no separate storage, no
// separate cap, no separate prompt block. The document is still one string
// under one bound, which is what keeps sections COMPETING for the budget rather
// than adding to it.

package notes

import (
	"fmt"
	"sort"
	"strings"
)

// section is one span of the document, heading line included.
//
// raw is the span VERBATIM. Nothing here reformats prose it did not write —
// splitting and re-joining an untouched document returns it byte for byte, and
// only the span being edited is rebuilt. A parser that tidies as it reads is a
// parser that loses things, and the thing it loses is the agent's own state.
type section struct {
	name string // as written, "" for text above the first heading
	raw  string
}

// splitSections cuts a document at its `## ` headings.
//
// Only `## `. A `#` is a document title and `###` is structure INSIDE a
// section: a model writing sub-headings under its own register must not find
// them silently promoted to registers of their own. Headings inside a fenced
// code block are text — the agent's notes are where it parks shell and markdown
// samples, and a fence full of `## comments` should not shatter the document.
func splitSections(text string) []section {
	if text == "" {
		return nil
	}
	var out []section
	var cur section
	fenced := false
	started := false
	for _, line := range strings.SplitAfter(text, "\n") {
		bare := strings.TrimRight(line, "\r\n")
		trimmed := strings.TrimSpace(bare)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fenced = !fenced
		}
		if !fenced && isSectionHeading(bare) {
			if started {
				out = append(out, cur)
			}
			cur = section{name: headingName(bare), raw: line}
			started = true
			continue
		}
		if !started {
			cur = section{}
			started = true
		}
		cur.raw += line
	}
	if started {
		out = append(out, cur)
	}
	return out
}

// joinSections is the exact inverse of splitSections: concatenation, no
// reformatting. join(split(x)) == x for every document, which is the property
// worth a test.
func joinSections(secs []section) string {
	var b strings.Builder
	for _, s := range secs {
		b.WriteString(s.raw)
	}
	return b.String()
}

func isSectionHeading(line string) bool { return strings.HasPrefix(line, "## ") }

func headingName(line string) string {
	return strings.TrimSpace(strings.TrimPrefix(line, "##"))
}

// sectionKey normalizes a name for MATCHING, never for storage.
//
// A model that writes "In Flight" one turn and "in flight" the next would
// otherwise keep two copies of one register, and they will disagree — the whole
// point of a named slot is that writing it twice lands in the same place. The
// name is stored exactly as first written.
func sectionKey(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}

// ApplyNoteSection replaces one section of a notes document and returns the
// whole document back.
//
//   - a section that does not exist is created, appended at the end
//   - an existing one is replaced IN PLACE, keeping its heading as first written
//   - an empty body removes it; removing one that was never there changes nothing
//
// Order is stable on purpose. A block that reshuffles itself every turn cannot be
// read as a diff, and it breaks the prompt prefix for no reason at all.
func ApplyNoteSection(text, name, body string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return text, fmt.Errorf("name the section to update, or pass text alone to rewrite the whole block")
	}
	if strings.ContainsAny(name, "\r\n") {
		return text, fmt.Errorf("a section name is one line")
	}
	body = strings.Trim(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	key := sectionKey(name)
	secs := splitSections(text)

	for i, s := range secs {
		if s.name == "" || sectionKey(s.name) != key {
			continue
		}
		if body == "" {
			secs = append(secs[:i], secs[i+1:]...)
			return tidy(joinSections(secs)), nil
		}
		// The heading is kept as it was written, not as it was addressed: the
		// agent named an existing register, it did not rename it.
		secs[i].raw = "## " + s.name + "\n" + body + "\n\n"
		return tidy(joinSections(secs)), nil
	}
	if body == "" {
		return tidy(text), nil // removing what was never there
	}
	joined := tidy(joinSections(secs))
	if joined != "" {
		joined += "\n\n"
	}
	return joined + "## " + name + "\n" + body + "\n", nil
}

// tidy trims the document's trailing blank lines to one newline, so an edit in
// the middle does not leave the end growing.
func tidy(text string) string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return ""
	}
	return text + "\n"
}

// OverCapAdvice names the biggest sections of a document, for the message an
// over-cap write gets back.
//
// The refusal is where a model decides what to do next, so it carries the one
// fact that makes the decision possible: which register is actually costing.
// Both the tool and the HTTP surface use this, so they cannot word the same
// refusal two ways.
func OverCapAdvice(text string) string {
	secs := splitSections(text)
	type row struct {
		name  string
		runes int
	}
	var rows []row
	for _, s := range secs {
		name := s.name
		if name == "" {
			name = "(untitled)"
		}
		rows = append(rows, row{name, len([]rune(s.raw))})
	}
	if len(rows) < 2 {
		return "" // one section is not a choice; there is nothing to name
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].runes > rows[j].runes })
	if len(rows) > 3 {
		rows = rows[:3]
	}
	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		parts = append(parts, fmt.Sprintf("%q (%d)", r.name, r.runes))
	}
	return "Largest sections: " + strings.Join(parts, ", ") + "."
}
