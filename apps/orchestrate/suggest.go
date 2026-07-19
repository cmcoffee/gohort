// Per-field AI suggestions for the agent editor. The framework's
// FormField ✨ Suggest button POSTs {field, hint, record} here; we
// compose a tight per-field prompt, call the worker LLM, and return
// {value}. Worker tier (private routing) — no third-party leakage.
//
// Number fields get integer-parsed responses; string fields pass
// through. The client-side setter knows how to apply each per
// field type, so the server can stay shape-agnostic.

package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// fieldsSuggestable lists agent fields the suggest endpoint will
// honor. Keeps the LLM from being asked to fill the ID slot or
// other auto-managed values.
var fieldsSuggestable = map[string]bool{
	"name":                true,
	"description":         true,
	"orchestrator_prompt": true,
	"rules":               true,
	"plan_guidance":       true,
	"max_plan_steps":      true,
	"max_worker_rounds":   true,
}

// fieldGuidance returns the per-field "what good looks like" guidance
// folded into the suggest prompt. Each entry describes the field's
// purpose, length expectations, and any anti-patterns to avoid.
func fieldGuidance(field string) string {
	switch field {
	case "name":
		return "A short, human-readable agent name (2-5 words). Examples: \"Research Helper\", \"Code Reviewer\", \"Travel Planner\"."
	case "description":
		return "One sentence summarizing what this agent is for. Examples: \"Decomposes research questions into subquestions, drafts factual answers, synthesizes.\""
	case "orchestrator_prompt":
		return "System prompt for the THINKING LLM that talks to the user, decomposes work into plan steps, AUTHORS A WORKER BRIEF for each step, and synthesizes the final reply. 4-10 sentences. Cover: persona, decomposition approach, how to brief workers well (be specific about deliverable, format, tools to prefer, what to avoid), and synthesis style. Do not include tool lists — the framework appends those. Do not mention plan_set/ask_user by name — the framework wires those automatically. There is NO separate worker_prompt; the orchestrator owns worker behavior per-step via worker_brief."
	case "rules":
		return "Non-negotiable operating-policy rules, one per line. Apply to both orchestrator AND worker at the very top of the prompt. Use for hard constraints (\"always cite a URL\", \"never quote prices from training, fetch live\", \"output code in code blocks\"). Each line a single rule, no numbering or bullets needed."
	case "plan_guidance":
		return "Optional nudge for decomposition style appended to the orchestrator prompt. 1-3 short sentences. Examples: \"Prefer 2-3 steps over fragmenting. Always start by restating the goal.\""
	case "max_plan_steps":
		return fmt.Sprintf("Integer 1-12. How many steps the orchestrator may commit to per turn. Default %d. Pick 7-10 for deep-research or thorough agents, 1-2 for snappy lookup agents.", defaultMaxPlanSteps)
	case "max_worker_rounds":
		return fmt.Sprintf("Integer 1-20. How many LLM call + tool-execution cycles the worker may run per step. Default %d. Raise when the worker chains many tool calls (18+), lower for single-tool answers (3).", defaultMaxWorkerRounds)
	}
	return ""
}

func (T *OrchestrateApp) handleAgentSuggest(w http.ResponseWriter, r *http.Request) {
	_, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if T.LLM == nil {
		http.Error(w, "worker LLM not configured", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Field  string         `json:"field"`
		Hint   string         `json:"hint"`
		Record map[string]any `json:"record"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	req.Field = strings.TrimSpace(req.Field)
	if !fieldsSuggestable[req.Field] {
		http.Error(w, "field not suggestable", http.StatusBadRequest)
		return
	}

	prompt := buildSuggestPrompt(req.Field, req.Hint, req.Record)
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	resp, err := T.LLM.Chat(ctx,
		[]Message{{Role: "user", Content: prompt}},
		WithSystemPrompt(suggestSystemPrompt),
		WithRouteKey("app.orchestrate.suggest"),
		WithThink(false),
	)
	if err != nil {
		http.Error(w, "suggest failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if resp == nil {
		http.Error(w, "empty response", http.StatusBadGateway)
		return
	}
	value := cleanSuggestion(req.Field, resp.Content)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"value": value})
}

const suggestSystemPrompt = `You are an editor helping a user fill in one field of an AI agent definition. The user shows you the current state of the agent (some fields filled, some blank) plus the field they want help with. You return ONLY the new value for that field — no commentary, no explanation, no markdown headers, no quotes around the value.

Be concise. The user is configuring a tool, not asking for an essay.`

func buildSuggestPrompt(field, hint string, record map[string]any) string {
	var b strings.Builder
	b.WriteString("## Agent under construction\n\n")
	if record == nil {
		b.WriteString("(no fields filled yet)\n\n")
	} else {
		for _, k := range []string{"name", "description", "orchestrator_prompt", "rules", "plan_guidance", "max_plan_steps", "max_worker_rounds"} {
			v, ok := record[k]
			if !ok || v == nil {
				continue
			}
			s := strings.TrimSpace(fmt.Sprintf("%v", v))
			if s == "" {
				continue
			}
			fmt.Fprintf(&b, "### %s\n%s\n\n", k, s)
		}
	}
	b.WriteString("## Field to suggest\n\n")
	b.WriteString("`")
	b.WriteString(field)
	b.WriteString("`")
	b.WriteString("\n\n")
	if g := fieldGuidance(field); g != "" {
		b.WriteString("### What good looks like\n")
		b.WriteString(g)
		b.WriteString("\n\n")
	}
	if h := strings.TrimSpace(hint); h != "" {
		b.WriteString("### User's guidance\n")
		b.WriteString(h)
		b.WriteString("\n\n")
	}
	b.WriteString("## Your reply\n\n")
	b.WriteString("Return ONLY the new value for the field — no preamble, no explanation, no surrounding quotes. Just the value as it should appear in the form input.")
	return b.String()
}

// cleanSuggestion strips wrappers the LLM may add despite instructions
// (quotes, code fences, leading "Here's a suggestion:" preambles).
// Number fields also get coerced into a plain integer string.
func cleanSuggestion(field, raw string) string {
	s := strings.TrimSpace(raw)
	// Strip a single pair of surrounding quotes or backticks.
	if len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if (first == '"' && last == '"') ||
			(first == '\'' && last == '\'') ||
			(first == '`' && last == '`') {
			s = strings.TrimSpace(s[1 : len(s)-1])
		}
	}
	// Strip a leading triple-backtick code fence (with or without lang).
	if strings.HasPrefix(s, "```") {
		// Drop the opening fence line.
		if nl := strings.IndexByte(s, '\n'); nl >= 0 {
			s = s[nl+1:]
		}
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	}
	if field == "max_plan_steps" || field == "max_worker_rounds" {
		// Pull the first integer out of the response so "5" /
		// "5 steps" / "I suggest 5" all land as 5.
		n := 0
		started := false
		for _, c := range s {
			if c >= '0' && c <= '9' {
				n = n*10 + int(c-'0')
				started = true
			} else if started {
				break
			}
		}
		if !started {
			return ""
		}
		return fmt.Sprintf("%d", n)
	}
	return s
}
