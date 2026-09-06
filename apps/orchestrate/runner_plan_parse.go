package orchestrate

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// --- helpers ---------------------------------------------------------------

// toLLMMessages converts the on-disk ChatMessage slice to the LLM
// Message slice the Chat call expects. Identity-shaped — the framework's
// Message type uses Role + Content.
func toLLMMessages(msgs []ChatMessage) []Message {
	out := make([]Message, 0, len(msgs))
	for mi, m := range msgs {
		// Shared with the dispatch builder (llmHistoryContent): a named speaker's
		// user turn keeps their name, an automated report card keeps its origin
		// marker. Rendering history here on its own is what let a group room's
		// participants collapse into one anonymous "user" voice in web chat.
		base := Message{Role: m.Role, Content: llmHistoryContent(m)}
		// Preserve tool calls + results across turns. ChatMessage.ToolCalls
		// (set on the assistant turn that fired them) gets expanded into
		// the LLM-protocol shape: the assistant message carries ToolCalls,
		// followed by a synthetic user message carrying ToolResults.
		// Without this, the LLM only sees the bare text content from
		// prior turns and has to re-derive what actually happened —
		// which manifests as "looping on what we're even talking about"
		// and re-asking questions whose answers came from tool returns.
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			calls := make([]ToolCall, 0, len(m.ToolCalls))
			results := make([]ToolResult, 0, len(m.ToolCalls))
			for ti, tc := range m.ToolCalls {
				// Stable per-(message, tool-index) IDs so the
				// assistant's ToolCall.ID matches the corresponding
				// ToolResult.ID within this conversion.
				id := fmt.Sprintf("hist-%d-%d", mi, ti)
				calls = append(calls, ToolCall{
					ID:   id,
					Name: tc.Name,
					Args: tc.Args,
				})
				content := tc.Result
				isErr := false
				if tc.Err != "" {
					content = "Error: " + tc.Err
					isErr = true
				}
				results = append(results, ToolResult{
					ID:      id,
					Content: content,
					IsError: isErr,
				})
			}
			base.ToolCalls = calls
			out = append(out, base)
			out = append(out, Message{
				Role:        "user",
				ToolResults: results,
			})
			continue
		}
		out = append(out, base)
	}
	return out
}

// parsePlanSteps coerces the orchestrator's plan_set "steps" arg into
// PlanStep records. Accepts multiple shapes (LLMs occasionally regress
// to older ones even after the schema upgrade):
//
//   - []any of map[string]any with title + optional intent + optional worker_brief
//   - []any of strings (legacy: title-only)
//
// Empty / whitespace titles are dropped silently. Length is clipped
// to maxSteps with a log when the orchestrator overshoots its budget.
// normalizeFormStep coerces one entry from the ask_user_form tool's
// `steps` array into the shape the client renderer expects:
// {question, options:[...], multi:bool, type, placeholder}. Defensive
// against the LLM varying types (string options vs []any, missing fields).
// A non-empty `type` marks the step as a typed entry FIELD (text / number /
// textarea / select / password); the renderer switches to an all-at-once
// form when any step carries one.
// normalizeFormStep renders one authored step into the wire shape the ask-card
// renderer reads. The second return is a warning for the turn diagnostics when
// something about the step had to be overridden — a step that quietly changes
// shape is the kind of thing nobody can attribute afterward.
func normalizeFormStep(m map[string]any) (map[string]any, string) {
	out := map[string]any{
		"question": strings.TrimSpace(stringArg(m, "question")),
	}
	opts := formStepOptions(m)
	if len(opts) > 0 {
		out["options"] = opts
	}
	if v, ok := m["multi"].(bool); ok && v {
		out["multi"] = true
	}
	// Entry-field metadata. Only accept known types so a stray value can't
	// produce a broken input; anything else falls back to a choice/open step.
	warn := ""
	switch t := strings.ToLower(strings.TrimSpace(stringArg(m, "type"))); t {
	case "select":
		// A dropdown with nothing in it is worse than no dropdown: the control
		// renders, shows its "— choose —" placeholder, and there is nothing to
		// pick, so the question cannot be answered at all. Reported as "it just
		// says choose answer with nothing to select".
		//
		// A text field is the safe degradation — the user can still reply — so
		// the type is dropped rather than the step. The single-question path
		// already reasons this way ("the card earns its place exactly when it
		// has choices to click"); this is the same rule for form steps.
		if len(opts) == 0 {
			warn = fmt.Sprintf("A form step asked for a dropdown but listed no options, so it rendered as a text field: %q", out["question"])
			break
		}
		out["type"] = t
	case "text", "number", "textarea", "password":
		out["type"] = t
	}
	if ph := strings.TrimSpace(stringArg(m, "placeholder")); ph != "" {
		out["placeholder"] = ph
	}
	return out, warn
}

// formStepOptionKeys are the names a model writes the choices under. "options"
// is the schema's; the rest are what it reaches for when it is pattern-matching
// against some other form shape it has seen.
//
// Reading the aliases rather than only the documented key is the same
// concession stringSliceFromArgs already makes for the option VALUES, and for
// the same reason: a set of choices that arrives under a near-miss key is not a
// question with no answers, and rendering it as one strands the turn.
var formStepOptionKeys = []string{"options", "choices", "opts", "values", "answers"}

func formStepOptions(m map[string]any) []string {
	for _, k := range formStepOptionKeys {
		if opts := stringSliceFromArgs(m, k); len(opts) > 0 {
			return opts
		}
	}
	return nil
}

// looksLikeVacuousPlan returns a non-empty reason string when the plan
// consists entirely of no-op / "just respond" steps. Returns "" when
// the plan looks like real decomposition. Matches case-insensitively
// against title+intent+worker_brief for known empty-step phrasings.
//
// Triggers seen in real traces:
//   - title: "Respond to user", intent: "respond directly", brief: "..."
//   - "Acknowledge the message", "Reply to the user", "Compose the answer"
//   - briefs that name respond_directly as the only tool to use
//
// We reject when EVERY step matches at least one of these patterns —
// a single ack step at the end of a real plan is fine.
func looksLikeVacuousPlan(steps []PlanStep) string {
	if len(steps) == 0 {
		return ""
	}
	emptyPatterns := []string{
		"respond_directly",
		"respond to the user",
		"respond to user",
		"reply to the user",
		"reply to user",
		"compose the reply",
		"compose the response",
		"compose the answer",
		"acknowledge the message",
		"acknowledge the user",
		"answer directly",
		"just respond",
		"summarize and reply",
		"summarize and respond",
		"final reply",
		"final response",
	}
	matchesEmpty := func(s string) bool {
		low := strings.ToLower(s)
		for _, p := range emptyPatterns {
			if strings.Contains(low, p) {
				return true
			}
		}
		return false
	}
	for _, st := range steps {
		blob := st.Title + " || " + st.Intent + " || " + st.WorkerBrief
		if !matchesEmpty(blob) {
			return ""
		}
	}
	return "every step is a 'just respond' / no-op step"
}

// sanitizePlanText cleans a plan-step title/intent a model handed over
// JSON-ENCODED instead of as plain text — the "messed-up plan" symptom: literal
// \n / \t / \" escape sequences plus a stray closing quote+comma leaked from an
// adjacent JSON field (e.g. a malformed ask_user payload dropped into a step).
// The framework stores tool args verbatim, so it decodes them here at the parse
// boundary — the one place every plan-card surface (live + replayed) funnels
// through. Conservative: only unescapes when literal escapes are present, only
// strips the exact quote / quote-comma leak signature.
func sanitizePlanText(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSpace(strings.TrimSuffix(s, `",`))
	s = strings.TrimSpace(strings.TrimSuffix(s, `"`))
	s = strings.TrimPrefix(s, `"`)
	if strings.Contains(s, `\n`) || strings.Contains(s, `\t`) || strings.Contains(s, `\"`) {
		s = strings.NewReplacer(`\n`, "\n", `\t`, "\t", `\r`, "\r", `\"`, `"`, `\\`, `\`).Replace(s)
	}
	return strings.TrimSpace(s)
}

func parsePlanSteps(raw any, maxSteps int) []PlanStep {
	var out []PlanStep
	add := func(title, intent, brief string, tools []string) {
		title = sanitizePlanText(title)
		if title == "" {
			return
		}
		out = append(out, PlanStep{
			ID:          len(out) + 1,
			Title:       title,
			Intent:      sanitizePlanText(intent),
			WorkerBrief: strings.TrimSpace(brief),
			Tools:       tools,
			Status:      StepPending,
		})
	}
	switch v := raw.(type) {
	case []any:
		for _, x := range v {
			switch s := x.(type) {
			case string:
				add(s, "", "", nil)
			case map[string]any:
				add(stringArg(s, "title"), stringArg(s, "intent"), stringArg(s, "worker_brief"), stringSliceFromArgs(s, "tools"))
			}
		}
	case []string:
		for _, s := range v {
			add(s, "", "", nil)
		}
	case []map[string]any:
		for _, m := range v {
			add(stringArg(m, "title"), stringArg(m, "intent"), stringArg(m, "worker_brief"), stringSliceFromArgs(m, "tools"))
		}
	}
	if len(out) > maxSteps {
		Log("[orchestrate.plan] LLM returned %d steps; clipping to MaxPlanSteps=%d",
			len(out), maxSteps)
		out = out[:maxSteps]
	}
	return out
}
