// Builder's instructions as a prompt block.
//
// Builder's standing prompt lived only in its seed document, so the one set
// of instructions that most decides how things get built was the one set an
// operator could not reach: not on the Prompts page, and not in a tuning
// variant. It is now a block like the framework's own, which makes it
// editable on the Prompts page with a revision behind every change, tunable
// in a variant, and promotable from the tuning harness.
//
// The block holds the RAW document, placeholders and all. The placeholders
// ({{name}}) are expanded every time the prompt is read, after any override,
// so an edited prompt still carries today's tool lists rather than the ones
// it was edited against.

package orchestrate

import (
	. "github.com/cmcoffee/gohort/core"
)

// BuilderPromptKey is the prompt-registry key for Builder's instructions.
const BuilderPromptKey = "agent.builder"

func init() {
	builtinDocsOnce.Do(func() { builtinDocs = loadBuiltinDocs() })
	for _, rec := range builtinDocs {
		if rec.ID != "seed-builder" {
			continue
		}
		RegisterPromptBlock(PromptBlock{
			Key:      BuilderPromptKey,
			Title:    "Builder's instructions",
			Category: "Builder",
			Gate:     "Every Builder turn. Placeholders like {{name}} are filled in each time the prompt is used: keep them.",
			Text:     rec.OrchestratorPrompt,
		})
	}
}

// builderPrompt is the raw prompt Builder runs with: the operator's edit when
// there is one, else what shipped.
func builderPrompt(shipped string) string {
	return EffectivePromptText(BuilderPromptKey, shipped)
}
