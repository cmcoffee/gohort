// Package dual provides a split-screen orchestrator/worker interface.
// The left pane runs a thinking worker LLM that plans and delegates tasks.
// The right pane shows the fast no-think worker LLM executing those tasks.
// The worker can save learned procedures to the database for future reference.
package dual

import (
	"fmt"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func init() { RegisterApp(new(DualAgent)) }

type DualAgent struct {
	AppCore
}

func (T DualAgent) Name() string { return "orchestrate" }
func (T DualAgent) Desc() string {
	return "Apps: Split-screen orchestrator (thinking) and worker (direct) LLMs."
}

func (T *DualAgent) Init() error { return T.Flags.Parse() }

func (T *DualAgent) Main() error {
	Log("Orchestrate is a dashboard-only app. Start with:\n  gohort serve :8080")
	return nil
}

// SystemPrompt is the orchestrator (left) system prompt.
func (T *DualAgent) SystemPrompt() string {
	today := time.Now().Format("Monday, January 2, 2006")
	return fmt.Sprintf("Today is %s.\n\n", today) + orchestratorPrompt
}

// workerSystemPromptFor builds the worker (right) system prompt,
// injecting any saved procedures from the user's database.
func workerSystemPromptFor(udb Database) string {
	today := time.Now().Format("Monday, January 2, 2006")
	return fmt.Sprintf("Today is %s.\n\n", today) + workerBasePrompt + buildProcedureSection(udb)
}

const orchestratorPrompt = `You are an AI orchestrator with extended thinking enabled. Your role is to plan, coordinate, and synthesize — not to execute details yourself.

When given a request:
1. Think through the goal, constraints, and best approach
2. Break it into specific, self-contained subtasks
3. Delegate each subtask to the worker using the delegate tool
4. Adapt if a worker result is incomplete or needs a follow-up task
5. Synthesize all results into a clear, complete response for the user

DELEGATION RULES:
- Give the worker one focused task at a time
- Include all context the worker needs to act without guessing
- For multi-step operations (e.g. connect to DB, run queries, format output), describe the full sequence in the task
- The worker is fast but not reflective — do analysis and synthesis yourself
- If the worker's result is incomplete, delegate a refined follow-up task`

const workerBasePrompt = `You are a fast, execution-focused AI worker. You receive specific task instructions from the orchestrator and complete them accurately and concisely.

Respond directly. No preamble or meta-commentary. Give the orchestrator exactly what it asked for.

When you figure out a reusable non-obvious procedure through trial and error, save it using the format described below. Only save multi-step procedures that encode real knowledge — not trivial single-step answers.`
