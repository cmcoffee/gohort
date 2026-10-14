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

// DiscussReplyContract is DraftReplyContract's other rhythm: talk it through
// first, write once. The revise-on-request contract proposes text on almost
// every turn, which suits "tighten the second paragraph" and is the wrong
// shape for "help me write this section": the first draft lands before the
// person has said what the section is for, and the conversation becomes
// correcting a guess. Here the model asks, proposes, and waits for a yes;
// the text comes back only then, in the same fenced block, so
// SplitDraftReply reads both contracts alike.
const DiscussReplyContract = "This is a conversation BEFORE anything is written. Do not write the text yet.\n\n" +
	"1. Ask the questions whose answers would change what you write: who reads it, what it has to cover, what they should be able to do afterwards, facts you would otherwise guess. At most three at a time, the most important first. Do not ask what the current text, the instructions above or the user have already settled, and look things up yourself when you can rather than asking.\n" +
	"2. When you know enough, say in a few short lines what you will write (its points, in order) and ask whether to go ahead.\n" +
	"3. Only when the user agrees, or tells you to write it now, reply with one sentence and then the complete text in a fenced block tagged `draft`:\n\n" +
	DraftFence + "\n...the entire text, not a diff or an excerpt...\n```\n\n" +
	"Rules:\n" +
	"- No draft block until the user has agreed or asked for it. Questions and proposals are plain replies.\n" +
	"- Told to write it now before you have asked everything, write it from what you know and say in that one sentence what you had to assume.\n" +
	"- The block holds the WHOLE text and replaces the current one outright. No heading for it, and no surrounding quotes.\n" +
	"- After writing, keep talking: a change the user asks for is a new complete block.\n"

// AssistModeDiscuss is the assist mode that talks it through before writing.
// Any other mode, including none, is revise-on-request.
const AssistModeDiscuss = "discuss"

// AssistReplyContract is the reply contract for an assist turn in mode: the
// discuss rhythm for AssistModeDiscuss, revise-on-request otherwise. The
// assist window sends its mode on every turn and the person can switch it
// mid-conversation, so an endpoint picks the contract per turn, here, rather
// than each deciding what an unknown mode means.
func AssistReplyContract(mode string) string {
	if strings.TrimSpace(mode) == AssistModeDiscuss {
		return DiscussReplyContract
	}
	return DraftReplyContract
}

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
