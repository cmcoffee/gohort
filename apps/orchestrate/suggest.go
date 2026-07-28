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

// suggestableFields lists agent fields the suggest endpoint will honor,
// in the order they're shown to the model as context. Keeps the LLM from
// being asked to fill the ID slot or other auto-managed values.
//
// ONE list: it gates the request AND drives the record dump in
// buildSuggestPrompt. These were two hardcoded copies, which meant a
// field added to the gate but missed in the dump would be suggestable
// while staying invisible as context for every other field.
var suggestableFields = []string{
	"name",
	"description",
	"orchestrator_prompt",
	"rules",
	"plan_guidance",
	"max_plan_steps",
	"max_worker_rounds",
}

var fieldsSuggestable = func() map[string]bool {
	m := make(map[string]bool, len(suggestableFields))
	for _, f := range suggestableFields {
		m[f] = true
	}
	return m
}()

// sectionGuidance returns the declared Help for one section of a
// sections-backed field, so the model is told what that section is for
// in the same words the author reads under it. Empty when the field has
// no declared outline or the title isn't one of its slots (a section the
// user added themselves — the title alone has to carry the intent).
func sectionGuidance(field, section string) string {
	if field != "orchestrator_prompt" {
		return ""
	}
	for _, s := range orchestratorPromptSections {
		if strings.EqualFold(strings.TrimSpace(s.Title), strings.TrimSpace(section)) {
			return s.Help
		}
	}
	return ""
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
		Field string `json:"field"`
		// Section, when set, narrows the request to ONE section of a
		// sections-backed field: the reply is that section's body only,
		// and the rest of the document is left alone. Asking for a part
		// beats regenerating the whole prompt, which throws away the
		// parts already working.
		Section string `json:"section"`
		Hint    string `json:"hint"`
		// Message, Draft, and History drive the assist workbench: an
		// ongoing conversation about a value the user can see and edit
		// between turns. Message set = conversation mode; the reply
		// carries both a sentence for the chat and (usually) the revised
		// text. Empty Message keeps the original one-shot behavior, so
		// short fields and the wizard are untouched.
		Message string        `json:"message"`
		Draft   string        `json:"draft"`
		History []suggestTurn `json:"history"`
		// AssistPrompt is the field's own framing, declared in Go on the
		// FormField and round-tripped through the browser. Advisory: it
		// is composed INTO the system prompt below, never substituted
		// for it, so it can shape the advice but cannot reach another
		// field or escape the response contract. Bounded to keep a
		// tampered client from turning this into a general LLM proxy.
		AssistPrompt string `json:"assist_prompt"`

		Record map[string]any `json:"record"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	req.Field = strings.TrimSpace(req.Field)
	req.Section = strings.TrimSpace(req.Section)
	if !fieldsSuggestable[req.Field] {
		http.Error(w, "field not suggestable", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	// Conversation mode: the draft is context in the system prompt (it
	// changes every turn, including from the user's own edits) and the
	// message list is the pure back-and-forth.
	if strings.TrimSpace(req.Message) != "" {
		msgs := make([]Message, 0, len(req.History)+1)
		for _, t := range req.History {
			role := strings.TrimSpace(t.Role)
			if role != "user" && role != "assistant" {
				continue
			}
			if strings.TrimSpace(t.Content) == "" {
				continue
			}
			msgs = append(msgs, Message{Role: role, Content: t.Content})
		}
		msgs = append(msgs, Message{Role: "user", Content: req.Message})

		resp, err := T.LLM.Chat(ctx, msgs,
			WithSystemPrompt(buildAssistSystemPrompt(req.Field, req.Section, req.Draft, req.AssistPrompt, req.Record)),
			WithRouteKey("app.orchestrate.suggest"),
			WithThink(false),
		)
		if err != nil {
			http.Error(w, "assist failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		if resp == nil {
			http.Error(w, "empty response", http.StatusBadGateway)
			return
		}
		reply, value := splitDraftReply(resp.Content)
		if value != "" {
			value = cleanSuggestion(req.Field, value)
			if req.Section != "" {
				value = stripSectionHeading(value, req.Section)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"reply": reply, "value": value})
		return
	}

	prompt := buildSuggestPrompt(req.Field, req.Section, req.Hint, req.Record)
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
	if req.Section != "" {
		value = stripSectionHeading(value, req.Section)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"value": value})
}

// suggestTurn is one exchange in an assist conversation.
type suggestTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// draftFence / splitDraftReply / stripSectionHeading now live in
// core/textutil (DraftFence, SplitDraftReply, StripLeadingHeading) so
// this endpoint and codewriter's assist endpoint parse one contract.
// These aliases keep the local call sites and their tests readable.
const draftFence = DraftFence

var (
	splitDraftReply     = SplitDraftReply
	stripSectionHeading = StripLeadingHeading
)

// buildAssistSystemPrompt frames a conversation about one value. The
// draft rides in the system prompt rather than the message list because
// it changes every turn — the user edits it directly between sends — and
// a stale copy buried in history would compete with the live one.
// assistPromptMax bounds the field-declared framing. Long enough for a
// real brief, short enough that the endpoint can't be repurposed as a
// general-purpose LLM proxy by a client that rewrites the payload.
const assistPromptMax = 2000

func buildAssistSystemPrompt(field, section, draft, assistPrompt string, record map[string]any) string {
	var b strings.Builder
	b.WriteString("You are helping a user write ONE value in an AI agent's definition. Talk with them about it and revise it on request.\n\n")

	// The field's own framing goes first, so it colors everything that
	// follows — but it lands INSIDE this prompt, under our headings and
	// above our response contract, never in place of them.
	if ap := strings.TrimSpace(assistPrompt); ap != "" {
		if len(ap) > assistPromptMax {
			ap = ap[:assistPromptMax]
		}
		b.WriteString("## What this assistant is for\n\n")
		b.WriteString(ap)
		b.WriteString("\n\n")
	}

	if section != "" {
		fmt.Fprintf(&b, "## What you are writing\n\nThe %q section of the `%s` field.\n\n", section, field)
		if g := sectionGuidance(field, section); g != "" {
			b.WriteString("### What this section is for\n")
			b.WriteString(g)
			b.WriteString("\n\n")
		}
	} else {
		fmt.Fprintf(&b, "## What you are writing\n\nThe `%s` field.\n\n", field)
		if g := fieldGuidance(field); g != "" {
			b.WriteString("### What good looks like\n")
			b.WriteString(g)
			b.WriteString("\n\n")
		}
	}

	b.WriteString("## Current draft\n\n")
	if strings.TrimSpace(draft) == "" {
		b.WriteString("(empty — nothing written yet)\n\n")
	} else {
		b.WriteString(draft)
		b.WriteString("\n\n")
	}

	if record != nil {
		var ctx strings.Builder
		for _, k := range suggestableFields {
			v, ok := record[k]
			if !ok || v == nil {
				continue
			}
			s := strings.TrimSpace(fmt.Sprintf("%v", v))
			if s == "" {
				continue
			}
			fmt.Fprintf(&ctx, "### %s\n%s\n\n", k, s)
		}
		if ctx.Len() > 0 {
			b.WriteString("## The rest of the agent, for context\n\n")
			b.WriteString(ctx.String())
		}
	}

	b.WriteString("## How to reply\n\n")
	b.WriteString(DraftReplyContract)
	return b.String()
}

const suggestSystemPrompt = `You are an editor helping a user fill in one field of an AI agent definition. The user shows you the current state of the agent (some fields filled, some blank) plus the field they want help with. You return ONLY the new value for that field — no commentary, no explanation, no markdown headers, no quotes around the value.

Be concise. The user is configuring a tool, not asking for an essay.`

func buildSuggestPrompt(field, section, hint string, record map[string]any) string {
	var b strings.Builder
	b.WriteString("## Agent under construction\n\n")
	if record == nil {
		b.WriteString("(no fields filled yet)\n\n")
	} else {
		for _, k := range suggestableFields {
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
	if section != "" {
		// Section mode: the field's CURRENT value is already dumped above
		// as context, so the model can see what the rest of the document
		// says and write something that fits beside it rather than
		// restating it.
		fmt.Fprintf(&b, "## Section to write\n\nThe %q section of the `%s` field.\n\n", section, field)
		if g := sectionGuidance(field, section); g != "" {
			b.WriteString("### What this section is for\n")
			b.WriteString(g)
			b.WriteString("\n\n")
		}
		if h := strings.TrimSpace(hint); h != "" {
			b.WriteString("### User's guidance\n")
			b.WriteString(h)
			b.WriteString("\n\n")
		}
		b.WriteString("## Your reply\n\n")
		fmt.Fprintf(&b, "Return ONLY the body of the %q section — no heading, no preamble, no explanation, no surrounding quotes. Do not restate what other sections already cover. If the section is a list of constraints or steps, return one per line and nothing else.", section)
		return b.String()
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
