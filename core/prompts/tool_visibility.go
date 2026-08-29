// What the user can see of a tool call, and what they cannot.
//
// The gap this closes was an UNSTATED requirement, not an ignored one. Nothing
// anywhere told an agent that tool results are invisible to the reader, so a
// model guessing they were shown was not breaking a rule; it was guessing, and
// guessing wrong is what you get for never saying.
//
// Observed 2026-08-29: asked for a joke, the agent called get_joke, got "Why
// did the database administrator leave his wife? She had one-to-many
// relationships.", and replied "Ha! That's a tech dad joke. Hope that helps you
// unwind." It reacted to a punchline the user never saw. The tool card that
// carries the result is collapsed in the chat, so from the reader's side the
// agent laughed at nothing.
//
// The clause names the ACTION case explicitly. Without that it would read as
// "always repeat the result", and an agent that just created a calendar entry
// would start pasting the API's JSON back at someone who asked it to book a
// meeting.
//
// Registered rather than only concatenated: it is visible on the Prompts page,
// overridable, and switchable, like the rules beside it. A fourteenth clause
// nobody can see was the thing worth not adding.

package prompts

// ToolVisibilityKey identifies the block on the Prompts page.
const ToolVisibilityKey = "framework.tool_visibility"

const toolVisibilityRule = "[Tool results: the user does NOT see what a tool returned. Only your own words reach them, so anything from a result that the user needs (the joke, the number, the headline, the address, the quote, the answer) has to be written out in your reply. Reacting to a result without repeating it (\"Ha, good one\", \"That's higher than I expected\", \"Found it\") leaves the user reading a response to something invisible. A tool that performed an ACTION rather than returning content is the other case: say what happened, do not paste the result back.]"

func init() {
	RegisterPromptBlock(PromptBlock{
		Key:      ToolVisibilityKey,
		Title:    "Tool results are not shown to the user",
		Category: "Grounding",
		Gate:     "Every reply from an agent that has tools.",
		Text:     toolVisibilityRule,
	})
}

// ToolVisibilityClause is what the assembler appends: the operator's text when
// one is set, nothing at all when the block is switched off.
func ToolVisibilityClause() string {
	return EffectiveBlockText(ToolVisibilityKey, toolVisibilityRule)
}
