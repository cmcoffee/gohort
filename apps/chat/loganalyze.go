package chat

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func init() { RegisterChatTool(new(LogAnalyzeTool)) }

// logTailCapBytes bounds the log text passed to the LLM. When the
// input exceeds this, the head is dropped and a marker prefixed.
// Tail bias because recent events tend to carry the signal — the
// crash line, the last successful heartbeat, the first ERR after a
// stretch of INFO. 30KB fits comfortably in any lead-LLM context
// window alongside the system prompt and response budget.
const logTailCapBytes = 30000

// LogAnalyzeTool dispatches a senior-SRE log analysis pass against a
// pasted log snippet via the shared lead LLM. Stateless: every call
// is a fresh completion — no history carried between invocations, no
// DB writes. Designed for an "analyze this with me" chat flow where
// the user pastes a log, the tool produces a structured read, and
// the outer chat conversation handles follow-up questions from the
// tool result already in context.
type LogAnalyzeTool struct{}

func (t *LogAnalyzeTool) Name() string { return "analyze_log" }

func (t *LogAnalyzeTool) Desc() string {
	return `Analyze a log snippet and return a structured read: what the log shows, errors/warnings with probable cause, unusual patterns, and what to look at next if debugging. Supply the log text (up to ~30KB; longer inputs keep only the tail). Optionally supply a focus question to steer the analysis (e.g., "where does the deadlock start"). Uses the lead LLM with thinking enabled — takes a few seconds but produces a real analysis rather than a surface summary.`
}

func (t *LogAnalyzeTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"log": {
			Type:        "string",
			Description: "The log text to analyze. Raw lines, any format (syslog, JSON, stack traces, etc.).",
		},
		"focus": {
			Type:        "string",
			Description: `Optional. A question or topic to focus the analysis on (e.g. "why is X failing", "sequence of events before the crash at 14:22"). Empty for a general summary.`,
		},
	}
}

// analyzeSystemPrompt establishes the analyst persona. Kept concise —
// the signal comes from the log itself, not from elaborate scaffolding.
// Deliberately asks for concrete line references so the response is
// navigable back to the source, not just generic narrative.
const analyzeSystemPrompt = `You are a senior SRE with deep log-analysis experience. Read the log carefully and produce a compact, concrete analysis. When you call out a specific event or error, cite the timestamp or log line it came from so the reader can locate it. Do not invent events not present in the log. If the log is too short or too sparse to support a finding, say so — "the log does not show X" is a useful answer. Avoid boilerplate ("this log shows some errors") — be specific or drop the sentence.`

// generalPromptTemplate is the default request when no focus is given.
const generalPromptTemplate = `Log:

%s

---

Provide:
1. What this log shows — 1-2 sentences.
2. Errors and warnings — each with the line reference and your best read of the probable cause.
3. Unusual patterns worth flagging — timing anomalies, unexpected sequences, silences, repeated retries, etc.
4. What to look at next if debugging — concrete next steps, not generic advice.

Keep the analysis tight. If a section has nothing to report, say so in one line and move on.`

// focusedPromptTemplate is used when the caller supplies a focus field.
const focusedPromptTemplate = `Log:

%s

---

Focus: %s

Answer the focus question based on what the log actually shows. Cite timestamps or line references for any specific events you call out. If the log does not contain enough evidence to answer, say so — don't extrapolate beyond what's in the text. After addressing the focus, add one short section flagging any errors or anomalies the focus question missed that seem worth knowing.`

func (t *LogAnalyzeTool) Run(args map[string]any) (string, error) {
	log, _ := args["log"].(string)
	log = strings.TrimSpace(log)
	if log == "" {
		return "", fmt.Errorf("log is required")
	}

	focus, _ := args["focus"].(string)
	focus = strings.TrimSpace(focus)

	llm := SharedLeadLLM()
	if llm == nil {
		return "", fmt.Errorf("analyze_log requires the shared lead LLM; no LLM is configured for this process")
	}

	// Tail bias when the input overflows — recent events carry more
	// debug signal than the start of a long log in the common case.
	truncated := false
	if len(log) > logTailCapBytes {
		log = log[len(log)-logTailCapBytes:]
		truncated = true
	}
	if truncated {
		log = "[input truncated to last 30KB — earlier lines omitted]\n" + log
	}

	var prompt string
	if focus != "" {
		prompt = fmt.Sprintf(focusedPromptTemplate, log, focus)
	} else {
		prompt = fmt.Sprintf(generalPromptTemplate, log)
	}

	resp, err := llm.Chat(context.Background(),
		[]Message{{Role: "user", Content: prompt}},
		WithSystemPrompt(analyzeSystemPrompt),
		WithMaxTokens(2048),
		WithTemperature(0.2),
		WithThink(true),
	)
	if err != nil {
		return "", fmt.Errorf("analyze_log LLM call failed: %w", err)
	}

	out := strings.TrimSpace(ResponseText(resp))
	if out == "" {
		return "", fmt.Errorf("analyze_log returned empty response")
	}
	return out, nil
}
