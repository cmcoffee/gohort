// Answer's orchestrator+worker pattern. The lead LLM drives the
// research strategy — picks search queries, decides which results to
// fetch, calls workers (web_search, fetch_url, browse_page) to do the
// IO. Mirrors servitor's pattern, swapping appliance state for web
// research state.
//
// On entry, the orchestrator picks a topic for the question, pulls
// any existing facts under that topic into its system prompt, then
// iterates: search → fetch → synthesize. As it learns durable facts,
// it calls save_fact to store them under the topic so future
// questions on the same topic skip work it's already done.
//
// Later: this same shape will gain delegate-to-research and
// delegate-to-debate tools so deeper questions can route to the
// appropriate heavy backend.

package answer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// OrchestratorResult is what RunOrchestrator returns. Same shape as
// the old QuickAnswerResult so the web layer can swap implementations
// without UI changes.
type OrchestratorResult struct {
	Answer  string
	Sources map[int]string
	Topic   string // the topic the orchestrator chose for this question
}

// RunOrchestrator is the new entry point that replaces RunQuickAnswer.
// Drives an agent loop where the LLM picks tools to research the
// question, with prior facts pre-loaded as context.
func RunOrchestrator(ctx context.Context, agent *AppCore, db Database, user, question string, emit func(status string)) (OrchestratorResult, error) {
	if err := agent.RequireLLM(); err != nil {
		return OrchestratorResult{}, err
	}

	// Step 1: pick a topic for this question. Cheap classifier call.
	topic := classifyTopic(ctx, agent, user, question, db)
	if emit != nil && topic != "" {
		emit(fmt.Sprintf("Topic: %s", topic))
	}

	// Step 2: pull existing facts for the topic; surface them to the
	// orchestrator as context. Empty when this is a fresh topic.
	priorFacts := FactsForTopic(db, user, topic)
	if emit != nil && len(priorFacts) > 0 {
		emit(fmt.Sprintf("Loaded %d prior fact(s) about %s", len(priorFacts), topic))
	}

	// Step 3: run the orchestrator agent loop. Tools available:
	// search/fetch/browse for IO, save_fact / list_facts for the
	// topic-scoped knowledge store.
	tools := orchestratorTools(db, user, topic)
	systemPrompt := buildOrchestratorSystem(question, topic, priorFacts)

	if emit != nil {
		emit("Researching...")
	}

	resp, _, err := agent.RunAgentLoop(ctx,
		[]Message{{Role: "user", Content: question}},
		AgentLoopConfig{
			SystemPrompt: systemPrompt,
			Tools:        tools,
			MaxRounds:    16,
			RouteKey:     "app.answer",
			ChatOptions:  orchestratorThinkOpts(),
		},
	)
	if err != nil {
		return OrchestratorResult{}, err
	}

	answer := ""
	if resp != nil {
		answer = strings.TrimSpace(resp.Content)
	}

	// Pull cited sources from the answer text. citedSources lives in
	// quickanswer.go (research package) — defer to it via the existing
	// helper since the citation regex is shared.
	srcs := extractSources(answer)

	return OrchestratorResult{
		Answer:  answer,
		Sources: srcs,
		Topic:   topic,
	}, nil
}

// classifyTopic asks the worker LLM to pick a short topic slug for
// the question. Used to namespace stored facts. Falls back to "general"
// on any error so the orchestrator still runs.
func classifyTopic(ctx context.Context, agent *AppCore, user, question string, db Database) string {
	existing := ListAnswerTopics(db, user)
	hint := ""
	if len(existing) > 0 {
		hint = "\n\nExisting topics you may want to reuse: " + strings.Join(existing, ", ")
	}
	sys := "Pick a short topic slug (snake_case, 1-3 words) that best categorizes the user's question for knowledge-base storage. Reuse existing topics when applicable. Reply with ONLY the slug, no explanation." + hint
	resp, err := agent.WorkerChat(ctx,
		[]Message{{Role: "user", Content: question}},
		WithSystemPrompt(sys), WithMaxTokens(20),
	)
	if err != nil || resp == nil {
		return "general"
	}
	topic := strings.TrimSpace(resp.Content)
	topic = strings.ToLower(topic)
	topic = strings.NewReplacer(" ", "_", "-", "_", "/", "_").Replace(topic)
	// Strip anything that isn't snake-safe.
	cleaned := make([]byte, 0, len(topic))
	for i := 0; i < len(topic); i++ {
		c := topic[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '.' {
			cleaned = append(cleaned, c)
		}
	}
	topic = string(cleaned)
	if topic == "" {
		topic = "general"
	}
	return topic
}

// buildOrchestratorSystem renders the orchestrator's system prompt,
// injecting any prior facts under the topic so the LLM can lean on
// them instead of re-discovering.
func buildOrchestratorSystem(question, topic string, priorFacts []AnswerFact) string {
	var b strings.Builder
	fmt.Fprintf(&b, `You are a research orchestrator. Your job: produce a clear, factual answer to the user's question by searching the web, fetching articles, and synthesizing what you find.

Today: %s
Topic for this question: %s

WORKFLOW:
1. If prior facts (below) already answer the question, lead with them — no need to re-search what you already know. Cite the source URLs they came from.
2. Otherwise, use web_search to find relevant articles. Use fetch_url to read promising results. Don't fetch more than necessary — 3-5 sources is usually plenty.
3. As you discover durable, factual information, save it via save_fact so future questions on this topic can reuse it. Don't save speculation, opinions, or things that change frequently — only facts you'd be willing to state confidently again next week.
4. When you have enough to answer well, write a clear synthesis. Include numeric citations [1], [2] to specific sources, with a short list of those sources at the end. Be direct and specific; no hedging or filler.

CITATION FORMAT:
- Inline: "TS3 WebQuery uses port 10080 by default [1]."
- Footer: a numbered list of source URLs.

`, time.Now().Format("Monday, January 2, 2006"), topic)

	if len(priorFacts) > 0 {
		b.WriteString("PRIOR FACTS UNDER TOPIC \"")
		b.WriteString(topic)
		b.WriteString("\" (you previously researched these — reuse where applicable):\n")
		b.WriteString(FormatFactsForPrompt(priorFacts))
		b.WriteString("\n")
	} else {
		b.WriteString("PRIOR FACTS: none yet for this topic.\n\n")
	}

	return b.String()
}

// orchestratorTools assembles the tool set the orchestrator gets
// access to. Built-in research tools plus per-topic fact CRUD.
func orchestratorTools(db Database, user, topic string) []AgentToolDef {
	var tools []AgentToolDef

	// Pull search/fetch/browse from the static registry by name. The
	// answer app has these enabled in its tier; we just package them
	// for the agent loop config.
	wantedNames := []string{"web_search", "fetch_url", "browse_page", "screenshot_page"}
	for _, name := range wantedNames {
		t, ok := LookupChatTool(name)
		if !ok {
			continue
		}
		tools = append(tools, chatToolToAgentDef(t))
	}

	// Add the orchestrator's own fact-management tool.
	tools = append(tools, factTool(db, user, topic))
	return tools
}

// chatToolToAgentDef wraps a registered ChatTool into the AgentToolDef
// shape RunAgentLoop expects. Same conversion the chat agent does.
// Caps come from the optional CapabilityTool interface (some tools
// don't implement it — treated as unannotated, passes cap filter).
func chatToolToAgentDef(t ChatTool) AgentToolDef {
	var caps []Capability
	if c, ok := t.(CapabilityTool); ok {
		caps = c.Caps()
	}
	return AgentToolDef{
		Tool: Tool{
			Name:        t.Name(),
			Description: t.Desc(),
			Parameters:  t.Params(),
			Caps:        caps,
		},
		Handler: func(args map[string]any) (string, error) { return t.Run(args) },
	}
}

// factTool builds the agent-loop-shaped tool that lets the
// orchestrator save / list / delete facts under the current topic.
// Topic and user are pre-bound so the LLM can't write outside its
// own scope.
func factTool(db Database, user, topic string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "save_fact",
			Description: "Save a durable fact you discovered during research, scoped to this question's topic. Future questions on the same topic will see it as prior knowledge. Use ONLY for facts you'd state confidently again next week — not opinions, speculation, or rapidly-changing data. Args: action ('save' | 'list' | 'delete'), key (snake_case), value, sources (URLs), tags (optional), ttl ('long' default / 'short' for time-sensitive).",
			Parameters: map[string]ToolParam{
				"action":  {Type: "string", Description: "save | list | delete"},
				"key":     {Type: "string", Description: "Fact key (snake_case). Required for save and delete."},
				"value":   {Type: "string", Description: "Fact value. Required for save."},
				"sources": {Type: "array", Description: "URLs the fact was learned from."},
				"tags":    {Type: "array", Description: "Optional categorization tags."},
				"ttl":     {Type: "string", Description: "'long' (30d, default) or 'short' (1h, for time-sensitive findings)."},
			},
			Required: []string{"action"},
			Caps:     []Capability{CapWrite},
		},
		Handler: func(args map[string]any) (string, error) {
			action := strings.TrimSpace(StringArg(args, "action"))
			switch action {
			case "save":
				key := strings.TrimSpace(StringArg(args, "key"))
				value := strings.TrimSpace(StringArg(args, "value"))
				if key == "" || value == "" {
					return "", fmt.Errorf("save requires key and value")
				}
				sources := stringArray(args["sources"])
				tags := stringArray(args["tags"])
				ttl := strings.TrimSpace(StringArg(args, "ttl"))
				StoreAnswerFact(db, user, topic, key, value, tags, sources, ttl)
				return fmt.Sprintf("Fact saved under topic %q: %s = %s", topic, key, value), nil
			case "list":
				facts := FactsForTopic(db, user, topic)
				if len(facts) == 0 {
					return fmt.Sprintf("No facts stored under topic %q.", topic), nil
				}
				return FormatFactsForPrompt(facts), nil
			case "delete":
				key := strings.TrimSpace(StringArg(args, "key"))
				if key == "" {
					return "", fmt.Errorf("delete requires key")
				}
				if err := DeleteAnswerFact(db, user, topic, key); err != nil {
					return "", err
				}
				return fmt.Sprintf("Fact %q deleted from topic %q.", key, topic), nil
			default:
				return "", fmt.Errorf("action must be save | list | delete (got %q)", action)
			}
		},
	}
}

// orchestratorThinkOpts mirrors the servitor pattern — pulls the
// per-route thinking budget from admin config when set, falls back
// to the agent loop's dynamic scaling otherwise.
func orchestratorThinkOpts() []ChatOption {
	if b := RouteThinkBudget("app.answer"); b != nil {
		return []ChatOption{WithThinkBudget(*b)}
	}
	return nil
}

// extractSources pulls [N] citation markers out of the final answer
// text and looks up corresponding URLs from the orchestrator's
// fetch/search trace. For now we extract URLs from the answer text
// itself when sources are listed inline; a more sophisticated
// implementation would track every fetch_url result and dereference
// the [N] markers against that map.
//
// TODO: track per-orchestrator-run source registry and resolve
// citation markers against it. Current implementation handles the
// common case where the answer ends with a numbered source list.
func extractSources(answer string) map[int]string {
	out := map[int]string{}
	// Look for lines like "[1] https://..." or "1. https://..." in the
	// trailing section. Crude but workable for v1.
	lines := strings.Split(answer, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		// "[N]" prefix
		if strings.HasPrefix(line, "[") {
			closeIdx := strings.Index(line, "]")
			if closeIdx > 1 {
				numStr := line[1:closeIdx]
				if n := parseIntSafe(numStr); n > 0 {
					rest := strings.TrimSpace(line[closeIdx+1:])
					if u := firstURL(rest); u != "" {
						out[n] = u
					}
				}
			}
		}
		// "N." prefix
		if dotIdx := strings.Index(line, "."); dotIdx > 0 {
			numStr := line[:dotIdx]
			if n := parseIntSafe(numStr); n > 0 {
				rest := strings.TrimSpace(line[dotIdx+1:])
				if u := firstURL(rest); u != "" {
					out[n] = u
				}
			}
		}
	}
	return out
}

func parseIntSafe(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func firstURL(s string) string {
	idx := strings.Index(s, "http")
	if idx < 0 {
		return ""
	}
	rest := s[idx:]
	end := strings.IndexAny(rest, " \t\n)\"'>")
	if end < 0 {
		return rest
	}
	return rest[:end]
}

func stringArray(raw any) []string {
	if raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return nil
		}
		var out []string
		if json.Unmarshal([]byte(s), &out) == nil {
			return out
		}
		return []string{s}
	}
	return nil
}
