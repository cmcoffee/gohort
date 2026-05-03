// Package orchestrator provides the `delegate` chat tool: a 2-layer
// orchestrator/worker sub-loop the calling LLM can invoke when a single
// request needs several distinct tool chains that don't share context.
//
// Pattern (lifted from the servitor app):
//
//   parent LLM
//      │
//      └── delegate(request, tools?, notes?)
//            │
//            ├── orchestrator (sess.LeadLLM, thinking on)
//            │   - decomposes request into a markdown plan
//            │   - dispatches each subtask via dispatch_worker
//            │   - synthesizes worker outputs into a final answer
//            │
//            └── dispatch_worker(task, tools?)   ← inline tool, only available to orchestrator
//                  └── worker (sess.LLM, thinking off)
//                        - focused subtask, fixed tool whitelist
//                        - returns synthesized findings
//
// Recursion is hard-capped at 2 layers: workers don't see the `delegate`
// tool, only the orchestrator does — so a worker can't spawn its own
// sub-orchestrator.
package orchestrator

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func init() { RegisterChatTool(&DelegateTool{}) }

// DelegateTool exposes the orchestrator/worker pattern as a chat tool.
type DelegateTool struct{}

func (t *DelegateTool) Name() string { return "delegate" }

// Caps: nil — the tool itself just spawns sub-loops. Capability gating
// applies via the underlying tool whitelist; the workers can only call
// tools the parent's session was already permitted to call.
func (t *DelegateTool) Caps() []Capability { return nil }

func (t *DelegateTool) Desc() string {
	return "Hand a multi-step request to a sub-agent that plans the work, executes each step, and returns a synthesized final answer. Use this when the user's request involves several distinct steps that each need their own tools or research (e.g. \"research X, format Y, schedule Z\" or \"find this information, then use it to do that\"). Do not use for single-step requests — just call the relevant tool directly."
}

func (t *DelegateTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"request": {
			Type:        "string",
			Description: "The full request to delegate. Include all context the orchestrator needs to plan and dispatch.",
		},
		"tools": {
			Type:        "array",
			Description: "Optional whitelist of tool names workers may call. If omitted, workers inherit every tool registered in the chat catalog except `delegate` itself. Subset to a tighter list when the request only needs a few tools.",
		},
		"notes": {
			Type:        "string",
			Description: "Optional extra guidance for the orchestrator — constraints, format requirements, or preferences for how the answer should be assembled.",
		},
	}
}

func (t *DelegateTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("delegate requires a session with an LLM configured")
}

func (t *DelegateTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil || sess.LLM == nil {
		return "", fmt.Errorf("delegate requires a session with an LLM")
	}
	request := strings.TrimSpace(StringArg(args, "request"))
	if request == "" {
		return "", fmt.Errorf("request is required")
	}
	notes := strings.TrimSpace(StringArg(args, "notes"))

	// Surface progress to the user. Delegate calls take a while (lead
	// LLM + N worker rounds), so a one-shot "I'm delegating..." status
	// at entry plus per-worker statuses below stop the conversation
	// from looking frozen. No-op when the app has no status channel.
	if sess.StatusCallback != nil {
		sess.StatusCallback("Delegating: " + truncForStatus(request))
	}

	// Resolve worker tool whitelist. nil/empty → inherit all registered
	// tools (minus delegate itself). Explicit list narrows further.
	workerToolNames := resolveWorkerTools(args)

	// Orchestrator uses the lead LLM when available (smarter, plans
	// well). Falls back to the worker LLM if no lead is configured —
	// degraded behavior but the pattern still works.
	workerLLM := sess.LLM
	orchestratorLLM := sess.LeadLLM
	if orchestratorLLM == nil {
		orchestratorLLM = sess.LLM
	}

	// Build the dispatch_worker tool that the orchestrator will use.
	// Closures capture sess + workerLLM + workerToolNames so the
	// orchestrator can fire the worker without further plumbing.
	dispatchWorker := buildDispatchWorker(sess, workerLLM, workerToolNames)

	// Run the orchestrator.
	orchCore := &AppCore{LLM: orchestratorLLM}
	tr := true
	resp, _, err := orchCore.RunAgentLoop(
		context.Background(),
		[]Message{{Role: "user", Content: request}},
		AgentLoopConfig{
			SystemPrompt: orchestratorPrompt(notes),
			Tools:        []AgentToolDef{dispatchWorker},
			MaxRounds:    8,
			ChatOptions:  []ChatOption{WithThink(tr)},
		},
	)
	if err != nil {
		return "", fmt.Errorf("orchestrator: %w", err)
	}
	if resp == nil || strings.TrimSpace(resp.Content) == "" {
		return "(orchestrator produced no final answer)", nil
	}
	return strings.TrimSpace(resp.Content), nil
}

// resolveWorkerTools returns the tool name list workers are allowed to
// call. An explicit `tools` arg wins; otherwise we hand over every
// registered chat tool except delegate (preventing recursion bombs).
func resolveWorkerTools(args map[string]any) []string {
	if raw := SliceArg(args, "tools"); len(raw) > 0 {
		var out []string
		for _, v := range raw {
			s, ok := v.(string)
			if !ok || s == "" || s == "delegate" {
				continue
			}
			out = append(out, s)
		}
		return out
	}
	var out []string
	for _, ct := range RegisteredChatTools() {
		if ct.Name() == "delegate" {
			continue
		}
		out = append(out, ct.Name())
	}
	return out
}

// buildDispatchWorker constructs the inline AgentToolDef the orchestrator
// uses to fire workers. Each call runs a fresh worker RunAgentLoop with
// the closed-over toolset.
func buildDispatchWorker(sess *ToolSession, workerLLM LLM, workerToolNames []string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "dispatch_worker",
			Description: "Dispatch a focused worker to execute one subtask using the available tools. " +
				"Each call is independent — workers don't share state. Pass enough context in the task " +
				"description so the worker doesn't have to re-discover what's already known. Returns the " +
				"worker's synthesized findings as plain text.",
			Parameters: map[string]ToolParam{
				"task": {
					Type:        "string",
					Description: "Single, clear, self-contained subtask. Include any context (paths, IDs, prior findings) the worker needs to act without further questions.",
				},
				"tools": {
					Type:        "array",
					Description: "Optional per-task tool restriction (must be a subset of the workers' allowed tools). Omit to give the worker the full whitelist.",
				},
			},
			Required: []string{"task"},
		},
		Handler: func(args map[string]any) (string, error) {
			task := strings.TrimSpace(StringArg(args, "task"))
			if task == "" {
				return "", fmt.Errorf("task is required")
			}
			if sess.StatusCallback != nil {
				sess.StatusCallback("Working on: " + truncForStatus(task))
			}
			// Apply per-task narrowing within the orchestrator's whitelist.
			taskTools := workerToolNames
			if raw := SliceArg(args, "tools"); len(raw) > 0 {
				allowed := make(map[string]bool, len(workerToolNames))
				for _, n := range workerToolNames {
					allowed[n] = true
				}
				taskTools = nil
				for _, v := range raw {
					if s, ok := v.(string); ok && allowed[s] {
						taskTools = append(taskTools, s)
					}
				}
				if len(taskTools) == 0 {
					return "", fmt.Errorf("none of the requested per-task tools are in the orchestrator's whitelist")
				}
			}

			workerTools, err := GetAgentToolsWithSession(sess, taskTools...)
			if err != nil {
				return "", fmt.Errorf("worker tool resolve: %w", err)
			}

			workerCore := &AppCore{LLM: workerLLM}
			f := false
			resp, _, werr := workerCore.RunAgentLoop(
				context.Background(),
				[]Message{{Role: "user", Content: task}},
				AgentLoopConfig{
					SystemPrompt:    workerPrompt(),
					Tools:           workerTools,
					MaxRounds:       12,
					SerialTools:     true,
					MaskDebugOutput: false,
					ChatOptions:     []ChatOption{WithThink(f), WithTemperature(0.2)},
				},
			)
			if werr != nil {
				return "", fmt.Errorf("worker: %w", werr)
			}
			if resp == nil {
				return "(no findings)", nil
			}
			out := strings.TrimSpace(resp.Content)
			if out == "" {
				return "(worker produced no text output)", nil
			}
			// Cap worker output so a runaway worker doesn't blow the
			// orchestrator's context budget.
			if len(out) > 12000 {
				out = out[:12000] + "\n… [truncated]"
			}
			return out, nil
		},
	}
}

// orchestratorPrompt is the system prompt for the planning layer.
func orchestratorPrompt(notes string) string {
	var b strings.Builder
	b.WriteString("You are an orchestrator coordinating a small team of focused workers to fulfill a complex request.\n\n")
	b.WriteString("Your job: read the request, decompose it into a short markdown plan of independent subtasks, dispatch each subtask via `dispatch_worker`, and synthesize the workers' outputs into a single coherent final answer.\n\n")
	b.WriteString("## Planning\n\n")
	b.WriteString("- Write the plan as a numbered markdown list before dispatching anything. Keep it tight — three to six subtasks is typical; more than that usually means you can collapse some.\n")
	b.WriteString("- Each subtask must be self-contained: name what to do, supply any context (URLs, IDs, prior findings) the worker needs, and state what kind of output you want back (a fact, a summary, a list, a formatted block).\n")
	b.WriteString("- Workers don't share state. If subtask B depends on subtask A's output, dispatch A first, wait for the result, then include the relevant pieces in B's task description.\n\n")
	b.WriteString("## Dispatching\n\n")
	b.WriteString("- Call `dispatch_worker` once per subtask. The worker has the tool catalog you were given access to.\n")
	b.WriteString("- If a worker returns an error or empty findings, decide: retry with a tighter task, route around the failure, or surface it to the user in your final answer.\n")
	b.WriteString("- Do NOT call `delegate` recursively — workers cannot spawn workers, and you should not try to either.\n\n")
	b.WriteString("## Synthesis\n\n")
	b.WriteString("- Your final response is what the calling agent receives as the tool result. Make it directly answer the original request — no \"here's what each worker said\" recap unless the user explicitly wants it.\n")
	b.WriteString("- Cite specific values from worker output verbatim (paths, URLs, numbers). Don't paraphrase concrete details.\n")
	b.WriteString("- If a subtask produced nothing useful, say so plainly rather than padding around it.\n")
	if notes != "" {
		b.WriteString("\n## Caller's Notes\n\n")
		b.WriteString(notes)
		b.WriteString("\n")
	}
	return b.String()
}

// truncForStatus shortens a request or task string to a length that
// reads cleanly as a one-line status message. Cuts at a word boundary
// when possible.
func truncForStatus(s string) string {
	s = strings.TrimSpace(s)
	const max = 80
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	if i := strings.LastIndexAny(cut, " \t\n"); i > max/2 {
		cut = cut[:i]
	}
	return cut + "…"
}

// workerPrompt is the system prompt for the execution layer.
func workerPrompt() string {
	return "You are a focused worker executing one subtask for an orchestrator. " +
		"Use the tools you've been given to complete the task, then return a tight synthesis of what you found or did. " +
		"Do not narrate your process — just do the work and report the result. " +
		"Cite specific values verbatim (paths, URLs, numbers). " +
		"If the task is unclear or impossible with your tools, say so plainly and stop. " +
		"Keep the response under 1500 words; the orchestrator will roll several worker outputs together."
}
