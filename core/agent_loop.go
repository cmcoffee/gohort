package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/cmcoffee/snugforge/nfo"
)

// ToolHandlerFunc is a function that executes a tool call and returns its output.
type ToolHandlerFunc func(args map[string]any) (string, error)

// ErrToolDenied is returned when the user denies a tool call.
var ErrToolDenied = fmt.Errorf("tool call denied by user")

// AgentToolDef combines a tool definition with its handler.
type AgentToolDef struct {
	Tool    Tool
	Handler ToolHandlerFunc

	// NeedsConfirm indicates that this tool requires user approval before
	// execution. When true, the agent loop will display the tool name and
	// arguments and prompt the user to allow or deny the call.
	NeedsConfirm bool
}

// ConfirmFunc is called to ask the user whether a tool call should proceed.
// It receives the tool name and a human-readable summary of the arguments.
// Return true to allow, false to deny.
type ConfirmFunc func(toolName string, argsSummary string) bool

// StepInfo provides observability into each round of the agent loop.
type StepInfo struct {
	Round      int        // Current round number (1-based).
	Content    string     // Text content from the LLM this round.
	ToolCalls  []ToolCall // Tool calls the LLM requested this round.
	ToolErrors int        // Number of tool calls that returned errors.
	Done       bool       // True if this is the final round (no more tool calls).
}

// StepCallback is called after each round of the agent loop for observability.
type StepCallback func(step StepInfo)

// AgentLoopConfig configures a RunAgentLoop invocation.
type AgentLoopConfig struct {
	// SystemPrompt sets the system prompt for the LLM.
	SystemPrompt string

	// Tools defines the tools available to the LLM and their handlers.
	Tools []AgentToolDef

	// MaxRounds limits how many LLM call rounds before stopping. Default 10.
	MaxRounds int

	// OnStep is called after each LLM round for logging/observability. Optional.
	OnStep StepCallback

	// Stream enables streaming mode. When set, LLM responses are streamed
	// through this handler as they arrive. Optional.
	Stream StreamHandler

	// Confirm is called when a tool with NeedsConfirm is about to execute.
	// If nil, a default terminal prompt is used (y/n).
	Confirm ConfirmFunc

	// ChatOptions are additional options passed to every LLM call.
	ChatOptions []ChatOption

	// PromptTools describes tools in the system prompt as text instead of
	// using native function calling. The LLM responds with plain text
	// containing tool calls in a defined format, which the loop parses and
	// executes. Results are sent back as regular user messages, giving the
	// caller full control over context. This works reliably with models
	// that have poor or no native tool support (e.g. Gemma via Ollama).
	PromptTools bool
}

// defaultConfirm prompts the user in the terminal with a Claude Code-style
// confirmation showing the tool name and arguments.
func defaultConfirm(toolName string, argsSummary string) bool {
	PleaseWait.Hide()
	fmt.Fprintf(os.Stderr, "\n\033[1;33m  ╭─ Tool Call ─────────────────────────\033[0m\n")
	fmt.Fprintf(os.Stderr, "\033[1;33m  │\033[0m \033[1m%s\033[0m\n", toolName)
	if argsSummary != "" {
		for _, line := range strings.Split(argsSummary, "\n") {
			fmt.Fprintf(os.Stderr, "\033[1;33m  │\033[0m   %s\n", line)
		}
	}
	fmt.Fprintf(os.Stderr, "\033[1;33m  ╰──────────────────────────────────────\033[0m\n")
	result := nfo.GetConfirm("  Allow this tool call?")
	PleaseWait.Show()
	return result
}

// formatArgs formats tool call arguments as a human-readable summary.
func formatArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	var lines []string
	for k, v := range args {
		display := stringify(v)
		if len(display) > 200 {
			display = display[:200] + "..."
		}
		lines = append(lines, fmt.Sprintf("%s: %s", k, display))
	}
	return strings.Join(lines, "\n")
}

// Run is a convenience method that resolves tools from SetTools(), uses the
// stored system prompt, and applies MaxRounds, then calls RunAgentLoop.
// Additional ChatOptions can be passed for per-call settings like WithMaxTokens.
func (T *AppCore) Run(ctx context.Context, messages []Message, opts ...ChatOption) (*Response, []Message, error) {
	if err := T.RequireLLM(); err != nil {
		return nil, messages, err
	}

	var tools []AgentToolDef
	if len(T.tools) > 0 {
		var err error
		tools, err = GetAgentTools(T.tools...)
		if err != nil {
			return nil, messages, err
		}
	}

	return T.RunAgentLoop(ctx, messages, AgentLoopConfig{
		SystemPrompt: T.systemPrompt,
		Tools:        tools,
		MaxRounds:    T.MaxRounds,
		PromptTools:  T.PromptTools,
		ChatOptions:  opts,
		OnStep: func(step StepInfo) {
			if step.Done {
				return
			}
			for _, tc := range step.ToolCalls {
				Debug("[agent] round %d: called tool '%s'", step.Round, tc.Name)
				if step.ToolErrors > 0 {
					Debug("[agent] round %d: %d tool error(s)", step.Round, step.ToolErrors)
				}
			}
		},
	})
}

// RunAgentLoop runs an autonomous agent loop: the LLM receives the initial
// messages, can call tools, observe results, and continue reasoning until it
// produces a final text response or hits MaxRounds.
//
// The returned Response is from the final LLM call. The returned []Message
// contains the full conversation history including all tool interactions.
func (T *AppCore) RunAgentLoop(ctx context.Context, messages []Message, cfg AgentLoopConfig) (*Response, []Message, error) {
	if T.LLM == nil {
		return nil, messages, fmt.Errorf("LLM is not configured")
	}

	maxRounds := cfg.MaxRounds
	if maxRounds <= 0 {
		maxRounds = 10
	}

	confirmFn := cfg.Confirm
	if confirmFn == nil {
		confirmFn = defaultConfirm
	}

	// Build tool definitions and lookup maps.
	var toolDefs []Tool
	handlers := make(map[string]ToolHandlerFunc)
	needsConfirm := make(map[string]bool)
	for _, td := range cfg.Tools {
		toolDefs = append(toolDefs, td.Tool)
		handlers[td.Tool.Name] = td.Handler
		if td.NeedsConfirm {
			needsConfirm[td.Tool.Name] = true
		}
	}

	history := make([]Message, len(messages))
	copy(history, messages)

	// In PromptTools mode, inject tool descriptions into the system
	// prompt instead of using native function calling. Everything stays
	// as plain text — tool calls are parsed from <tool_call> tags and
	// results are sent back as regular user messages.
	systemPrompt := cfg.SystemPrompt
	if cfg.PromptTools && len(cfg.Tools) > 0 {
		systemPrompt += BuildToolPrompt(cfg.Tools)
	}

	var lastResp *Response

	for round := 1; round <= maxRounds; round++ {
		opts := append([]ChatOption{}, cfg.ChatOptions...)
		if systemPrompt != "" {
			opts = append(opts, WithSystemPrompt(systemPrompt))
		}
		// Only offer native tools when NOT in PromptTools mode.
		if !cfg.PromptTools && len(toolDefs) > 0 && round < maxRounds {
			opts = append(opts, WithTools(toolDefs))
		}

		var resp *Response
		var err error
		if cfg.Stream != nil {
			resp, err = T.ChatStreamWithReport(ctx, history, cfg.Stream, opts...)
		} else {
			resp, err = T.WorkerChat(ctx, history, opts...)
		}
		if err != nil {
			return resp, history, err
		}
		lastResp = resp

		Debug("[agent_loop] round %d: content=%d chars, reasoning=%d chars, tool_calls=%d", round, len(resp.Content), len(resp.Reasoning), len(resp.ToolCalls))

		// Thinking models may place their response entirely in the
		// reasoning field. Promote reasoning to content when there is
		// no content or tool calls so text-based tool parsing can work.
		if resp.Content == "" && len(resp.ToolCalls) == 0 && resp.Reasoning != "" {
			Debug("[agent_loop] promoting reasoning to content (%d chars)", len(resp.Reasoning))
			resp.Content = resp.Reasoning
		}

		// PromptTools path: parse <tool_call> tags from the text response.
		// Everything is plain text — no native ToolCall/ToolResult objects.
		if cfg.PromptTools {
			tc, preamble := ParsePromptToolCall(resp.Content, handlers)
			if tc == nil {
				// No tool call — LLM is done. Record and return.
				history = append(history, Message{Role: "assistant", Content: resp.Content})
				if cfg.OnStep != nil {
					cfg.OnStep(StepInfo{Round: round, Content: resp.Content, Done: true})
				}
				return resp, history, nil
			}

			Debug("[agent_loop] prompt-tool call: %s(%s)", tc.Name, formatArgs(tc.Args))

			// Record the assistant's message (preamble only, strip the tag).
			if preamble != "" {
				history = append(history, Message{Role: "assistant", Content: preamble})
			}

			// Confirmation check.
			if needsConfirm[tc.Name] {
				if !confirmFn(tc.Name, formatArgs(tc.Args)) {
					Debug("[agent_loop] prompt-tool denied: %s", tc.Name)
					history = append(history, Message{
						Role:    "user",
						Content: fmt.Sprintf("Tool call to %s was denied.", tc.Name),
					})
					if cfg.OnStep != nil {
						cfg.OnStep(StepInfo{Round: round, ToolCalls: []ToolCall{*tc}, ToolErrors: 1})
					}
					continue
				}
			}

			// Execute the tool.
			output, toolErr := handlers[tc.Name](tc.Args)
			toolErrors := 0
			var resultText string
			if toolErr != nil {
				resultText = fmt.Sprintf("Tool %s returned an error: %s", tc.Name, toolErr)
				toolErrors = 1
			} else {
				resultText = fmt.Sprintf("Tool result from %s:\n%s", tc.Name, output)
			}
			Debug("[agent_loop] prompt-tool result: %s", resultText)

			// Send result back as a plain user message.
			history = append(history, Message{Role: "user", Content: resultText})

			if cfg.OnStep != nil {
				cfg.OnStep(StepInfo{Round: round, ToolCalls: []ToolCall{*tc}, ToolErrors: toolErrors})
			}
			continue
		}

		// Native tool path (existing behavior).

		// Record assistant response.
		history = append(history, Message{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})

		// If no tool calls, check if the model emitted a tool call as
		// text (common with models that don't support function calling).
		if len(resp.ToolCalls) == 0 {
			if parsed := parseTextToolCall(resp.Content, handlers); parsed != nil {
				Debug("[agent_loop] parsed text-based tool call: %s", parsed.Name)
				resp.ToolCalls = []ToolCall{*parsed}
				resp.Content = ""
				// Rewrite the history entry we just appended.
				history[len(history)-1] = Message{
					Role:      "assistant",
					ToolCalls: resp.ToolCalls,
				}
			}
		}

		// If still no tool calls, the LLM is done reasoning.
		if len(resp.ToolCalls) == 0 {
			if cfg.OnStep != nil {
				cfg.OnStep(StepInfo{
					Round:   round,
					Content: resp.Content,
					Done:    true,
				})
			}
			return resp, history, nil
		}

		// Execute tool calls and collect results.
		// Independent calls run in parallel; confirmable tools are
		// checked serially first to avoid concurrent prompts.
		results := make([]ToolResult, len(resp.ToolCalls))
		toolErrors := 0

		// First pass: resolve handlers and handle confirmations serially.
		type toolWork struct {
			index   int
			tc      ToolCall
			handler ToolHandlerFunc
		}
		var work []toolWork

		for i, tc := range resp.ToolCalls {
			Debug("[agent_loop] tool call: %s(%s)", tc.Name, formatArgs(tc.Args))

			handler, ok := handlers[tc.Name]
			if !ok {
				errMsg := fmt.Sprintf("Error: unknown tool '%s'", tc.Name)
				Debug("[agent_loop] %s", errMsg)
				results[i] = ToolResult{ID: tc.ID, Content: errMsg, IsError: true}
				toolErrors++
				continue
			}

			if needsConfirm[tc.Name] {
				if !confirmFn(tc.Name, formatArgs(tc.Args)) {
					Debug("[agent_loop] tool call denied by user: %s", tc.Name)
					results[i] = ToolResult{ID: tc.ID, Content: "Error: tool call denied by user", IsError: true}
					toolErrors++
					continue
				}
			}

			work = append(work, toolWork{index: i, tc: tc, handler: handler})
		}

		// Second pass: execute approved tool calls in parallel.
		if len(work) == 1 {
			// Single call — no goroutine overhead.
			w := work[0]
			output, err := w.handler(w.tc.Args)
			if err != nil {
				Debug("[agent_loop] tool error: %s: %s", w.tc.Name, err)
				results[w.index] = ToolResult{ID: w.tc.ID, Content: fmt.Sprintf("Error: %s", err), IsError: true}
				toolErrors++
			} else {
				Debug("[agent_loop] tool result: %s: %s", w.tc.Name, output)
				results[w.index] = ToolResult{ID: w.tc.ID, Content: output}
			}
		} else if len(work) > 1 {
			var wg sync.WaitGroup
			var errCount int32
			for _, w := range work {
				wg.Add(1)
				go func(w toolWork) {
					defer wg.Done()
					output, err := w.handler(w.tc.Args)
					if err != nil {
						Debug("[agent_loop] tool error: %s: %s", w.tc.Name, err)
						results[w.index] = ToolResult{ID: w.tc.ID, Content: fmt.Sprintf("Error: %s", err), IsError: true}
						atomic.AddInt32(&errCount, 1)
					} else {
						Debug("[agent_loop] tool result: %s: %s", w.tc.Name, output)
						results[w.index] = ToolResult{ID: w.tc.ID, Content: output}
					}
				}(w)
			}
			wg.Wait()
			toolErrors += int(atomic.LoadInt32(&errCount))
		}

		// Add tool results to history for the next LLM round.
		history = append(history, Message{
			Role:        "user",
			ToolResults: results,
		})

		if cfg.OnStep != nil {
			cfg.OnStep(StepInfo{
				Round:      round,
				Content:    resp.Content,
				ToolCalls:  resp.ToolCalls,
				ToolErrors: toolErrors,
				Done:       false,
			})
		}
	}

	return lastResp, history, nil
}

// parseTextToolCall attempts to extract a tool call from text content when the
// model doesn't use structured tool calling. It first looks for JSON objects
// with "name" and "parameters"/"arguments" keys. If that fails, it falls back
// to scanning for known tool names mentioned in natural language (common with
// thinking models that reason about tool calls without emitting them).
func parseTextToolCall(content string, handlers map[string]ToolHandlerFunc) *ToolCall {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}

	// Try structured JSON tool call first.
	if tc := parseJSONToolCall(content, handlers); tc != nil {
		return tc
	}

	// Fallback: scan for a known tool name mentioned in the text.
	// Thinking models often reason like "call run_healthcheck with args ..."
	// without emitting the actual structured call.
	return parseNaturalToolCall(content, handlers)
}

// parseJSONToolCall extracts a tool call from a JSON object in the text.
func parseJSONToolCall(content string, handlers map[string]ToolHandlerFunc) *ToolCall {
	// Find the first '{' and last '}' to extract a JSON object.
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start < 0 || end <= start {
		return nil
	}
	jsonStr := content[start : end+1]

	var raw map[string]interface{}
	if json.Unmarshal([]byte(jsonStr), &raw) != nil {
		return nil
	}

	name, _ := raw["name"].(string)
	if name == "" {
		return nil
	}

	// Only treat it as a tool call if the name matches a registered handler.
	if _, ok := handlers[name]; !ok {
		return nil
	}

	// Extract arguments from "parameters" or "arguments".
	args := make(map[string]any)
	var params map[string]interface{}
	if p, ok := raw["parameters"].(map[string]interface{}); ok {
		params = p
	} else if a, ok := raw["arguments"].(map[string]interface{}); ok {
		params = a
	}
	for k, v := range params {
		args[k] = v
	}

	return &ToolCall{
		ID:   fmt.Sprintf("text_%s", UUIDv4()),
		Name: name,
		Args: args,
	}
}

// parseNaturalToolCall scans text for a known tool name and extracts any
// arguments that follow it. This handles thinking models that reason about
// which tool to call but stop before emitting a structured call.
func parseNaturalToolCall(content string, handlers map[string]ToolHandlerFunc) *ToolCall {
	lower := strings.ToLower(content)

	// Find the best (longest) matching tool name in the text.
	var bestName string
	var bestPos int = -1
	for name := range handlers {
		pos := strings.LastIndex(lower, strings.ToLower(name))
		if pos >= 0 && (bestPos < 0 || len(name) > len(bestName)) {
			bestName = name
			bestPos = pos
		}
	}

	if bestName == "" {
		return nil
	}

	Debug("[agent_loop] extracted tool call from reasoning: %s", bestName)

	// Try to extract args after the tool name mention.
	args := make(map[string]any)
	after := strings.TrimSpace(content[bestPos+len(bestName):])

	// Look for --flag patterns (e.g. "--to user@example.com").
	var flag_args []string
	for _, part := range strings.Fields(after) {
		if strings.HasPrefix(part, "--") {
			flag_args = append(flag_args, part)
		} else if len(flag_args) > 0 {
			// Attach value to the previous flag.
			flag_args = append(flag_args, part)
		}
	}
	if len(flag_args) > 0 {
		args["args"] = strings.Join(flag_args, " ")
	}

	return &ToolCall{
		ID:   fmt.Sprintf("text_%s", UUIDv4()),
		Name: bestName,
		Args: args,
	}
}

// BuildToolPrompt generates a text description of available tools for
// injection into the system prompt when PromptTools mode is enabled.
func BuildToolPrompt(tools []AgentToolDef) string {
	if len(tools) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n\nYou have access to the following tools:\n\n")
	for _, td := range tools {
		b.WriteString(fmt.Sprintf("### %s\n%s\n", td.Tool.Name, td.Tool.Description))
		if len(td.Tool.Parameters) > 0 {
			b.WriteString("Parameters:\n")
			for name, p := range td.Tool.Parameters {
				req := ""
				for _, r := range td.Tool.Required {
					if r == name {
						req = " (required)"
						break
					}
				}
				b.WriteString(fmt.Sprintf("  - %s (%s%s): %s\n", name, p.Type, req, p.Description))
			}
		}
		b.WriteString("\n")
	}
	b.WriteString(`To use a tool, respond with EXACTLY this format on its own line:
<tool_call>
{"name": "tool_name", "arguments": {"param": "value"}}
</tool_call>

After you see the tool result, continue your response to the user.
If you do not need a tool, respond normally without any <tool_call> tags.
Only call ONE tool at a time. Wait for the result before calling another.
`)
	return b.String()
}

// ParsePromptToolCall extracts a tool call from <tool_call> tags in the
// LLM's text response. Returns the parsed ToolCall and the surrounding
// text (before the tag) so the caller can preserve any preamble.
func ParsePromptToolCall(content string, handlers map[string]ToolHandlerFunc) (*ToolCall, string) {
	start := strings.Index(content, "<tool_call>")
	if start < 0 {
		return nil, content
	}
	end := strings.Index(content, "</tool_call>")
	if end < 0 || end <= start {
		return nil, content
	}

	preamble := strings.TrimSpace(content[:start])
	jsonStr := strings.TrimSpace(content[start+len("<tool_call>") : end])

	var raw map[string]interface{}
	if json.Unmarshal([]byte(jsonStr), &raw) != nil {
		return nil, content
	}

	name, _ := raw["name"].(string)
	if name == "" {
		return nil, content
	}
	if _, ok := handlers[name]; !ok {
		return nil, content
	}

	args := make(map[string]any)
	if a, ok := raw["arguments"].(map[string]interface{}); ok {
		for k, v := range a {
			args[k] = v
		}
	}

	return &ToolCall{
		ID:   fmt.Sprintf("prompt_%s", UUIDv4()),
		Name: name,
		Args: args,
	}, preamble
}
