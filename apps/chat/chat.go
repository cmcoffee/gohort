// Package chat provides a minimal web chat interface backed by the worker
// (local) LLM with all registered tools available. Intended for hands-on
// testing of new tool implementations against gemma without needing to
// build a full task pipeline.
package chat

import (
	. "github.com/cmcoffee/gohort/core"
)

func init() {
	RegisterApp(new(ChatAgent))
	RegisterRouteStage(RouteStage{Key: "chat.respond", Label: "Chat: Respond"})
}

// ChatAgent is the task definition. Currently no CLI flags — the only
// entry point is the web UI mounted at /chat.
type ChatAgent struct {
	FuzzAgent
}

func (T ChatAgent) Name() string { return "chat_tools" }
func (T ChatAgent) Desc() string {
	return "Testing: Web chat backed by the worker LLM with all tools available."
}
func (T ChatAgent) SystemPrompt() string {
	return `You are Gohort, a helpful assistant with access to a set of tools. Your name is Gohort. Never say you are Gemma, an AI by Google, or any other identity — you are Gohort. When the user asks a question that a tool can help answer, call the tool and use its result. Otherwise reply directly. Be concise.

RESPONSE RULES:
- Do NOT repeat or echo what you just said. Each response must contain only NEW content.
- Do NOT narrate your actions ("I will now search...", "Let me look up..."). Just do it.
- When using tool results that include REPORT_IDs, use the exact ID from the results to call get_report. Do NOT invent or guess IDs.
- Summarize tool results for the user -- do not dump raw tool output.
- NEVER show REPORT_IDs or UUIDs to the user. Those are internal identifiers for your tool calls. The user wants answers, not database keys.`
}

func (T *ChatAgent) Init() (err error) {
	return T.Flags.Parse()
}

// Main is a no-op for the chat app — it only runs as a web app.
// Invoking it from the CLI prints a hint and exits cleanly.
func (T *ChatAgent) Main() (err error) {
	Log("Chat is a web-only app. Start the dashboard with:\n  gohort --web :8080")
	return nil
}
