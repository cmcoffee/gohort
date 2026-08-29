// The house-style rules, declared where their transforms live.
//
// Each is registered as an operator-visible block AND bound to the transform
// that guarantees it, in one place, so the sentence an agent is told and the
// code that enforces it cannot drift apart. Turning either rule off on the
// Prompts page stops both halves together.
//
// These are the two rules that are fully mechanical: the correct output is a
// function of the wrong input, so they hold whether or not the model
// cooperates. The [Style:] clause in the system prompt asked for both and was
// broken on both inside six turns of small talk, which is what moved them here.

package textutil

import "github.com/cmcoffee/gohort/core/prompts"

// Keys for the two shipped style rules. Exported so a surface can name them and
// so one key gates both halves: the sentence in the clause and the transform at
// the delivery boundary.
const (
	RuleNoEmDash        = "style.no_em_dash"
	RuleNoFillerClassic = "style.no_filler_classic"
)

func init() {
	// Registration order is the order they appear in the clause, and it matches
	// the wording the prompt already carried.
	prompts.RegisterStyleRule(prompts.StyleRule{
		Key:  RuleNoFillerClassic,
		Text: "Stop reaching for the word \"classic\"; you lean on it as filler. Drop it unless it's literally accurate (a \"classic car\", a named \"classic\" edition), never as a generic intensifier for something ordinary.",
	})
	prompts.RegisterRuleEnforcer(RuleNoFillerClassic, StripFillerClassic)

	prompts.RegisterStyleRule(prompts.StyleRule{
		Key:  RuleNoEmDash,
		Text: "Do NOT use em-dashes (the \"—\" character, U+2014) at all. Where you'd reach for one, use a comma, parentheses, a colon, or two sentences instead.",
	})
	prompts.RegisterRuleEnforcer(RuleNoEmDash, StripEmDashes)
}
