// Assist-conversation reply parsing. An assist turn asks the model for
// two things at once: a sentence for the chat pane, and (usually) the
// complete revised document. This splits them.
//
// Shared because every surface that offers "draft this with me" needs
// the identical contract — the agent editor's prompt sections, a
// codewriter markdown document, and whatever comes next. One copy means
// the instruction handed to the model and the parser that reads it back
// cannot drift apart.

package textutil

import "strings"

// DraftFence is the marker the model wraps a revised draft in.
// Deliberately not a bare ``` — the documents this carries (prompts,
// runbooks, READMEs) routinely CONTAIN fenced examples, and a plain
// fence would be ambiguous with them.
const DraftFence = "```draft"

// DraftReplyContract is the instruction block describing how to answer.
// Callers append it to their system prompt so the wording the model
// receives always matches SplitDraftReply's expectations.
const DraftReplyContract = "Answer in one or two sentences, saying what you changed and why. Then, WHEN AND ONLY WHEN you are proposing new text, follow it with the complete revised value in a fenced block tagged `draft`:\n\n" +
	DraftFence + "\n...the entire new value, not a diff or an excerpt...\n```\n\n" +
	"Rules:\n" +
	"- The block must hold the WHOLE value. It replaces the draft outright.\n" +
	"- No heading for the value itself, and no surrounding quotes.\n" +
	"- Answering a question, or explaining a choice, needs no block at all. Omit it rather than restating an unchanged draft.\n" +
	"- Change what was asked for. Leave the rest of the draft alone.\n"

// SplitDraftReply separates the model's conversational sentence from the
// revised value it fenced. value is empty when the model answered
// without proposing text, which is a normal outcome: "why is this
// section here?" deserves an answer, not a rewrite.
//
// The closing fence is found from the END of the response, so a draft
// containing its own ``` blocks survives intact rather than being
// truncated at the first inner fence.
func SplitDraftReply(raw string) (reply, value string) {
	s := strings.TrimSpace(raw)
	open := strings.Index(s, DraftFence)
	if open < 0 {
		return s, ""
	}
	reply = strings.TrimSpace(s[:open])
	rest := s[open+len(DraftFence):]
	rest = strings.TrimPrefix(rest, "\r")
	rest = strings.TrimPrefix(rest, "\n")
	if end := strings.LastIndex(rest, "```"); end >= 0 {
		value = rest[:end]
	} else {
		// Unterminated fence: the model ran out of room or forgot to
		// close it. Everything after the marker is still the draft, and
		// keeping it beats discarding a whole generation over a missing
		// three characters.
		value = rest
	}
	return reply, strings.Trim(value, "\r\n")
}

// StripLeadingHeading drops a markdown heading from the front of a
// section-scoped reply when it merely restates the section's own name.
// Models do this no matter how the instruction is phrased, and letting
// it through nests a second "## Rules" inside the Rules section. A
// heading that names something ELSE is content the model chose to
// structure, and is left alone.
func StripLeadingHeading(value, section string) string {
	s := strings.TrimLeft(value, "\n\r\t ")
	if !strings.HasPrefix(s, "#") {
		return value
	}
	line, rest, _ := strings.Cut(s, "\n")
	head := strings.TrimSpace(strings.TrimLeft(line, "#"))
	if !strings.EqualFold(head, strings.TrimSpace(section)) {
		return value
	}
	return strings.TrimLeft(rest, "\n\r")
}
