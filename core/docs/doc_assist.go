// Shared framing for the "draft this document with me" conversation the
// writer apps offer, plus the fence helper it needs. Kept here so
// CodeWriter, TechWriter, and whatever grows the affordance next all
// brief the model the same way.

package docs

import (
	"fmt"
	"strings"

	"github.com/cmcoffee/gohort/core/textutil"
)

// DocFence returns a code fence long enough to wrap s without being
// closed early by a fence inside it. A markdown snippet routinely
// CONTAINS ``` blocks, so the fixed three-backtick wrapper used
// everywhere here would terminate at the snippet's first inner fence and
// hand the model a truncated script plus the remainder as loose prose.
// Rare enough to ignore for bash or SQL; guaranteed the moment markdown
// became a language.
func DocFence(s string) string {
	longest := 0
	run := 0
	for _, r := range s {
		if r == '`' {
			run++
			if run > longest {
				longest = run
			}
			continue
		}
		run = 0
	}
	n := longest + 1
	if n < 3 {
		n = 3
	}
	return strings.Repeat("`", n)
}

// BuildDocAssistPrompt frames a conversation about one markdown
// document, or one section of it. The draft rides here rather than in
// the message list because the user edits it directly between turns —
// a stale copy buried in history would compete with the live one.
func BuildDocAssistPrompt(name, section, draft, refContext string) string {
	var b strings.Builder
	b.WriteString("You are helping a user write a markdown document. Talk with them about it and revise it on request.\n\n")
	b.WriteString("Write documentation prose: plain, specific, and skimmable. Prefer a concrete instruction over a description of one. Keep the heading structure the document already has unless asked to change it.\n\n")

	if strings.TrimSpace(name) != "" {
		fmt.Fprintf(&b, "## Document\n\n%s\n\n", strings.TrimSpace(name))
	}
	if section != "" {
		fmt.Fprintf(&b, "## What you are writing\n\nThe %q section. Return that section's body only — no heading, and do not restate what other sections cover.\n\n", section)
		if strings.TrimSpace(draft) == "" {
			b.WriteString("## Current section\n\n(empty — nothing written yet)\n\n")
		} else {
			fmt.Fprintf(&b, "## Current section\n\n%s\n\n", draft)
		}
	} else {
		b.WriteString("## What you are writing\n\nThe whole document.\n\n")
		if strings.TrimSpace(draft) == "" {
			b.WriteString("## Current draft\n\n(empty — nothing written yet)\n\n")
		} else {
			fmt.Fprintf(&b, "## Current draft\n\n%s\n\n", draft)
		}
	}
	if strings.TrimSpace(refContext) != "" {
		f := DocFence(refContext)
		fmt.Fprintf(&b, "## Reference material the user attached\n\n%s\n%s\n%s\n\n", f, refContext, f)
	}
	b.WriteString("## How to reply\n\n")
	b.WriteString(textutil.DraftReplyContract)
	return b.String()
}
