// Per-agent prompt context assembly. Memory under the new model is
// 100% LLM-driven via the store_fact tool (Explicit Memory) and
// memory_save (Reference Memory). The pre-existing "Notes" path —
// freeform paragraphs written by a post-turn consolidator — is
// removed; this file no longer carries that layer.
//
// What lives here now:
//   - prependAgentContext: stitches rules + facts (Explicit Memory)
//     into the top of the system prompt
//   - renderRulesPromptSection: the user-authored rules block

package orchestrate

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// renderRulesPromptSection produces the system-prompt block for the
// agent's user-authored operating policy. Lands at the very top of
// both system prompts (above facts and above the persona) so it's
// the strongest signal in the prompt order. Empty rules → empty
// string so callers can prepend unconditionally.
func renderRulesPromptSection(rules string) string {
	rules = strings.TrimSpace(rules)
	if rules == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Rules — operating policy you must follow\n\n")
	b.WriteString("These rules are non-negotiable and apply to every turn. If a rule conflicts with anything else in this prompt, the rule wins.\n\n")
	for _, ln := range strings.Split(rules, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		b.WriteString("- ")
		b.WriteString(ln)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

// prependAgentContext applies the agent's user-facing context blocks
// (Rules + Explicit Memory facts) ahead of the supplied base prompt.
// Encapsulates the order — rules first (strongest), then facts, then
// the persona — so the two callers (orchestrator + worker) can't
// drift. Facts are loaded from the core MemoryFact store (the LLM-
// in-band store_fact tool); the block's header + intro copy is
// driven by the agent's MemoryMode (agent / chatbot).
func prependAgentContext(base string, agent AgentRecord, facts []MemoryFact) string {
	out := base
	header, intro, _ := memoryModeCopy(agent.MemoryMode)
	if f := RenderMemoryFactsBlockWith(facts, header, intro); f != "" {
		out = f + out
	}
	if r := renderRulesPromptSection(agent.Rules); r != "" {
		out = r + out
	}
	return out
}

// memoryModeCopy returns the (header, intro, storeToolSuffix) triple
// for the given MemoryMode. Empty or unrecognized → "agent" defaults.
//
//   - header: rendered atop the always-in-prompt facts block
//   - intro: explanatory paragraph under the header (tells the LLM
//     how to read and use the facts)
//   - storeToolSuffix: appended to the store_fact tool description
//     so the LLM understands WHAT to put there in this mode
//
// Two modes:
//
//   "agent" (default, narrow): store_fact is reserved for
//     generalized lessons — the kind of design principle or recurring
//     gotcha you'd want to recall across every future job. Anything
//     more specific (an API quirk, a working approach, a configuration
//     detail) belongs in Reference Memory via memory_save — searchable
//     by similarity, not always in prompt.
//
//   "chatbot" (broader): store_fact is generalized lessons PLUS user
//     personalization (name, preferences, recurring details) PLUS
//     memorable notes that keep conversation coherent across sessions
//     ("the project we discussed last week", "the user is on the beta
//     plan"). Reference Memory works the same way as in agent mode.
//
// Both modes use Reference Memory (memory_save) identically; the mode
// only widens or narrows what fits in the Explicit Memory bucket.
func memoryModeCopy(mode string) (header, intro, storeToolSuffix string) {
	switch mode {
	case "chatbot":
		return "## Saved notes",
			"What you've kept in mind across sessions with this user — generalized lessons (design principles, recurring gotchas) PLUS personalization (their name, preferences, recurring details) PLUS conversation-coherence notes (\"the project we discussed last week\", \"the user is on the beta plan\"). Apply silently; don't list them back. Each entry is numbered so you can reference an index when one is no longer accurate.",
			"This agent is in CHATBOT MODE — Explicit Memory is broad. Right for store_fact: generalized lessons (\"X always fails in dev environments\"), user personalization (\"prefers concise replies\", \"works at Acme on the platform team\"), memorable notes that keep conversations coherent (\"the project we've been discussing is named Atlas\"). API specifics, working approaches for a specific task, paragraph-length findings — those still belong in Reference Memory via memory_save (searchable by similarity, not always in prompt). The rule is the same: always-in-prompt vs. searchable; chatbot mode just widens what counts as worth always seeing."
	default: // "" or "agent"
		return "## Lessons learned",
			"Generalized lessons from prior sessions — design principles, recurring gotchas, things that bit you and the workaround that worked. Apply when the current task touches similar territory. Each lesson is numbered so you can reference an index when forgetting one that's no longer accurate.",
			"This agent is in AGENT MODE — Explicit Memory is narrow. Use store_fact ONLY for GENERALIZED lessons that apply across many future jobs: design principles (\"check the sandbox before authoring tools that depend on binaries\"), recurring gotchas (\"endpoint X returns 200 with empty body on missing key, not 404, so check for that pattern\"), \"X fails, route Y instead\" type rules. Specific API details, working approaches for a single task, paragraph-length findings — those go in Reference Memory via memory_save (searchable by similarity, not always in prompt). NOT for user personalization (\"user prefers concise replies\", \"user's name is Sarah\") — that's chatbot-mode territory and this agent is task-focused. If the lesson reads \"and then it worked\" with no generalized trap underneath — discard, that's a success story, not a lesson."
	}
}
