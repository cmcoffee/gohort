package orchestrate

import (
	"context"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// registerWhenToUseGenerator wires core's WhenToUseFunc shim to this app's
// worker LLM. saveAgent / SaveSkill / the collection handlers call
// core.GenerateWhenToUse on a description change; this is what actually
// runs the model. Registered once from Routes(). Kept on the worker (not
// the lead) — it's a cheap one-liner, not a reasoning task.
func (T *OrchestrateApp) registerWhenToUseGenerator() {
	WhenToUseFunc = func(ctx context.Context, kind, name, description string) (string, error) {
		sys := "You write ONE compact routing cue that helps another AI decide WHEN to reach for a " + kind + ". " +
			"Given the " + kind + "'s name and its user-facing description, output a single line (about 220 characters max) naming the concrete situations, question types, or subject matter that should trigger picking it. " +
			"Lead with the trigger conditions — the kinds of questions or tasks it fits — not a restatement of what it is. " +
			"Enumerate the domain subjects it implies (a 'geopolitics' description covers war, sanctions, elections, treaties). " +
			"Output the cue only: no preamble, no quotes, no markdown, no trailing label."
		usr := "Name: " + name + "\nDescription: " + description + "\n\nWrite its \"when to use\" cue:"
		// Tight thinking budget rather than disabled — Qwen degenerates
		// with thinking fully off (see the no-think degeneration note).
		resp, err := T.WorkerChat(ctx,
			[]Message{{Role: "user", Content: usr}},
			WithSystemPrompt(sys), WithMaxTokens(160), WithThinkBudget(256),
		)
		if err != nil {
			return "", err
		}
		if resp == nil {
			return "", nil
		}
		return strings.TrimSpace(resp.Content), nil
	}
}
