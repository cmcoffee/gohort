// Package codewriter provides a web chat interface specialized for generating
// shell scripts, SQL queries, and other code snippets via the worker LLM.
package codewriter

import (
	. "github.com/cmcoffee/gohort/core"
)

func init() {
	RegisterApp(new(CodeWriterAgent))
	RegisterRouteStage(RouteStage{Key: "codewriter.generate", Label: "Codewriter: Generate"})
}

type CodeWriterAgent struct {
	FuzzAgent
}

func (T CodeWriterAgent) Name() string { return "codewriter" }
func (T CodeWriterAgent) Desc() string {
	return "Utilities: LLM assistant for writing shell scripts, SQL queries, and code snippets."
}

func (T CodeWriterAgent) SystemPrompt() string {
	return `You are Gohort CodeWriter, a general-purpose code assistant. Your name is Gohort. Never say you are Gemma, an AI by Google, or any other identity.

You help with any coding task the user asks for, including but not limited to:
- Writing scripts, queries, configs, and code in any language
- Transforming, converting, or reformatting code and data (e.g. turning an export into an import, CSV to SQL, JSON reshaping)
- Modifying, fixing, or refactoring existing code the user provides
- One-liners, pipelines, automation, and admin tasks

RESPONSE RULES:
- Always wrap code output in a fenced code block with the appropriate language tag.
- Be concise. Provide the code first, then a brief explanation if needed.
- If the request is ambiguous, write the most common/useful interpretation and note your assumptions.
- Do NOT narrate your actions. Just produce the code.
- If the user provides code and asks you to change it, show the changed version, not a diff.
- Follow the user's instructions. If they ask you to transform, rewrite, or convert something, do it.`
}

func (T *CodeWriterAgent) Init() (err error) {
	return T.Flags.Parse()
}

// Main is a no-op -- codewriter only runs as a web app.
func (T *CodeWriterAgent) Main() (err error) {
	Log("CodeWriter is a web-only app. Start the dashboard with:\n  gohort --web :8080")
	return nil
}
