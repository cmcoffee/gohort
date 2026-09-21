// The shipped style rules that have no transform behind them.
//
// Its sibling in core/textutil holds the two MECHANICAL ones, where the
// correct output is a function of the wrong input and the rule therefore holds
// whether or not the model cooperates. This file is for the other kind: a rule
// that can only ASK, because what it forbids is a shape of reasoning rather
// than a character or a word. The page already draws that distinction, so a
// reader can tell a guarantee from a request.
//
// Registered here rather than beside a transform that does not exist, and in
// prompts rather than textutil, because a rule with nothing to strip has no
// business in a text-utility package.

package prompts

// RuleNoAttemptNarration is the key for the rule below. Exported so a surface
// can name it and an operator can turn it off.
const RuleNoAttemptNarration = "style.no_attempt_narration"

func init() {
	// What this is about, from the report that produced it: an agent hits a
	// transient failure, writes a paragraph about it, tries something else,
	// succeeds, and the reader has read three sentences of process to reach
	// one sentence of answer.
	//
	// The test is NOT whether the agent recovered. It is whether the failure
	// changed what the reader should believe. A retry that produced the same
	// result changes nothing and is noise; a source that could not be reached
	// changes the basis of the answer and is part of it. Without that second
	// clause the rule would teach the model to hide the material case along
	// with the trivial one, which is the opposite of what it is for.
	//
	// Nothing is concealed either way: a failed call is already in the
	// activity record, the tool-call list and the trail. This keeps it out of
	// PROSE, where the reader did not ask for it.
	//
	// Written without an em-dash so it does not model what its sibling rule
	// forbids, and without narrating an attempt of its own.
	RegisterStyleRule(StyleRule{
		Key: RuleNoAttemptNarration,
		Text: "Do not narrate attempts. A call that failed and then worked, a retry, a route you abandoned: leave it out. " +
			"It is already in the activity record, and the reader wants the answer rather than the process. Say what you found. " +
			"One exception, and it is not optional: when a failure changed the ANSWER, because you could not reach a source, " +
			"skipped a step, or fell back to something lesser, say so plainly in one line. That is part of the answer, " +
			"not a note about how it went.",
	})
}
