package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"

	. "github.com/cmcoffee/gohort/core"

	"github.com/cmcoffee/snugforge/eflag"
	"github.com/cmcoffee/snugforge/nfo"
)

// chatSession manages the interactive chat REPL.
type chatSession struct {
	llm        LLM
	tools      []Tool
	agents     map[string]*menu_elem
	chatTools  map[string]ChatTool
	history    []Message
	system     string
}

// agentFlagHelp introspects an agent's flags by creating a temporary clone,
// initializing it to register flags, and returning a description of each flag.
// This works generically for any agent that registers flags in Init().
func agentFlagHelp(entry *menu_elem) string {
	// Create a temporary copy of the agent so we don't modify the original.
	tmp_agent := reflect.New(reflect.TypeOf(entry.agent).Elem()).Interface().(Agent)

	// Set up a throwaway flagset with --help to trigger flag registration.
	flags := &FlagSet{EFlagSet: NewFlagSet(entry.name, ReturnErrorOnly)}
	flags.FlagArgs = []string{"--help"}
	set_agent_flags(tmp_agent, *flags)
	set_agent_db(tmp_agent, get_agentstore(strings.Split(entry.name, ":")[0]))

	// Call Init to register flags; ignore the expected help/parse error.
	tmp_agent.Init()

	// Collect flag descriptions.
	var parts []string
	skip := map[string]bool{"serial": true, "debug": true, "snoop": true, "quiet": true, "repeat": true}

	flags.VisitAll(func(f *eflag.Flag) {
		if skip[f.Name] || f.Usage == "" {
			return
		}
		if f.DefValue != "" {
			parts = append(parts, fmt.Sprintf("  --%s: %s (default: %s)", f.Name, f.Usage, f.DefValue))
		} else {
			parts = append(parts, fmt.Sprintf("  --%s: %s", f.Name, f.Usage))
		}
	})

	if len(parts) == 0 {
		return ""
	}
	return "\nFlags:\n" + strings.Join(parts, "\n")
}

// listTools returns a summary of all available tools and agents.
// Use get_tool_info(name="tool_name") to get full details for a specific tool.
func (cs *chatSession) listTools() string {
	var lines []string
	for name, ct := range cs.chatTools {
		lines = append(lines, fmt.Sprintf("  %s - %s", name, ct.Desc()))
	}
	for _, name := range command.agents {
		if e, ok := command.entries[name]; ok {
			lines = append(lines, fmt.Sprintf("  run_%s - %s", name, e.desc))
		}
	}
	for _, name := range command.admin_agents {
		if e, ok := command.entries[name]; ok {
			lines = append(lines, fmt.Sprintf("  run_%s - %s", name, e.desc))
		}
	}
	return "Available tools and agents:\n" + strings.Join(lines, "\n") + "\n\nUse get_tool_info(name=\"tool_name\") for details on a specific tool."
}

// toolInfo builds detailed info for a tool or agent by name.
func (cs *chatSession) toolInfo(name string) string {
	// Check chat tools first.
	if ct, ok := cs.chatTools[name]; ok {
		var b strings.Builder
		fmt.Fprintf(&b, "Tool: %s\n%s\n", name, ct.Desc())
		if params := ct.Params(); len(params) > 0 {
			b.WriteString("\nParameters (pass as JSON to run_tool args):\n")
			for pname, p := range params {
				fmt.Fprintf(&b, "  %s (%s): %s\n", pname, p.Type, p.Description)
			}
		}
		if c, ok := ct.(ConfirmableTool); ok && c.NeedsConfirm() {
			b.WriteString("\nNote: This tool requires user confirmation before execution.\n")
		}
		return b.String()
	}

	// Check agent tools (strip "run_" prefix).
	agent_name := strings.TrimPrefix(name, "run_")
	if e, ok := cs.agents[agent_name]; ok {
		var b strings.Builder
		fmt.Fprintf(&b, "Agent: %s\n%s\n", agent_name, e.desc)
		fmt.Fprintf(&b, "\nUsage: call run_tool with name=\"run_%s\" and args as a space-separated flag string.\n", agent_name)
		if flags := agentFlagHelp(e); flags != "" {
			b.WriteString(flags)
			b.WriteString("\n")
		} else {
			b.WriteString("\nNo flags available — call with empty args.\n")
		}
		return b.String()
	}

	// Unknown or empty name — return the full tool list as a fallback.
	return fmt.Sprintf("Unknown tool or agent: %s\n\n%s", name, cs.listTools())
}

// toolNeedsConfirm checks if a tool requires user confirmation before execution.
func (cs *chatSession) toolNeedsConfirm(name string) bool {
	if ct, ok := cs.chatTools[name]; ok {
		if c, ok := ct.(ConfirmableTool); ok {
			return c.NeedsConfirm()
		}
	}
	// Agents always need confirmation since they can run commands, send email, etc.
	agent_name := strings.TrimPrefix(name, "run_")
	if _, ok := cs.agents[agent_name]; ok {
		return true
	}
	return false
}

// runTool dispatches a tool or agent call by name.
func (cs *chatSession) runTool(name string, args map[string]any) (string, bool, error) {
	// Check chat tools.
	if ct, ok := cs.chatTools[name]; ok {
		needs_confirm := false
		if c, ok := ct.(ConfirmableTool); ok {
			needs_confirm = c.NeedsConfirm()
		}
		result, err := ct.Run(args)
		return result, needs_confirm, err
	}

	// Check agents (strip "run_" prefix).
	agent_name := strings.TrimPrefix(name, "run_")
	if _, ok := cs.agents[agent_name]; ok {
		result, err := cs.runAgent(agent_name, StringArg(args, "args"))
		return result, false, err
	}

	return "", false, fmt.Errorf("unknown tool or agent: %s", name)
}

// newChatSession creates a chat session from the current menu state.
func newChatSession(llm LLM) *chatSession {
	cs := &chatSession{
		llm:       llm,
		agents:    make(map[string]*menu_elem),
		chatTools: make(map[string]ChatTool),
	}

	// Register agents.
	command.mutex.RLock()
	defer command.mutex.RUnlock()

	for _, name := range command.agents {
		if e, ok := command.entries[name]; ok {
			cs.agents[name] = e
		}
	}
	for _, name := range command.admin_agents {
		if e, ok := command.entries[name]; ok {
			cs.agents[name] = e
		}
	}

	// Register chat tools.
	for name, ct := range chatTools {
		cs.chatTools[name] = ct
	}

	// Build the three meta-tools that the LLM sees.
	cs.tools = []Tool{
		{
			Name:        "list_tools",
			Description: "List all available tools and agents with short descriptions.",
			Parameters:  map[string]ToolParam{},
		},
		{
			Name:        "get_tool_info",
			Description: "Get detailed usage info for a tool. Requires a tool name. Example: get_tool_info(name=\"get_local_time\") or get_tool_info(name=\"run_healthcheck\").",
			Required:    []string{"name"},
			Parameters: map[string]ToolParam{
				"name": {Type: "string", Description: "The tool name to look up, e.g. 'get_local_time' or 'run_healthcheck'. Get names from list_tools."},
			},
		},
		{
			Name:        "run_tool",
			Description: "Execute a tool or agent by name. Call get_tool_info first to learn required parameters.",
			Parameters: map[string]ToolParam{
				"name": {Type: "string", Description: "Name of the tool or agent to run."},
				"args": {Type: "string", Description: "Arguments as a JSON object string for tools (e.g. '{\"command\":\"df -h\"}') or space-separated flags for agents (e.g. '--to user@example.com')."},
			},
		},
	}

	// Build a brief list of available tool/agent names.
	var tool_names []string
	for name := range cs.chatTools {
		tool_names = append(tool_names, name)
	}
	for name := range cs.agents {
		tool_names = append(tool_names, fmt.Sprintf("run_%s", name))
	}

	// Include configured mail recipient context if available.
	var mail_context string
	if mail_cfg := load_mail_config(); mail_cfg.Recipient != "" {
		mail_context = fmt.Sprintf("\nConfigured default report recipient: %s\n", mail_cfg.Recipient)
	}

	cs.system = fmt.Sprintf(`You are Fuzz, a helpful AI assistant with access to tools and agents.
%s
Available: %s

To use a tool:
1. Call list_tools to see what is available.
2. Call get_tool_info(name="tool_name") to learn its parameters. You MUST provide the name parameter.
3. Call run_tool(name="tool_name", args="...") to execute it.

Be concise and helpful.`, mail_context, strings.Join(tool_names, ", "))

	return cs
}

// run starts the interactive chat loop.
func (cs *chatSession) run() error {
	PleaseWait.Hide()

	// Print header.
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "  \033[1;36mFuzz Chat\033[0m v%s\n", VERSION)
	fmt.Fprintf(os.Stderr, "  \033[2mType your message below. Press Ctrl+C to exit.\033[0m\n")
	fmt.Fprintf(os.Stderr, "\n")

	for {
		// Prompt.
		PleaseWait.Hide()
		input := nfo.GetInput("\033[1;35m>\033[0m ")
		input = strings.TrimSpace(input)

		if input == "" {
			continue
		}

		// Exit commands.
		switch strings.ToLower(input) {
		case "/exit", "/quit", "exit", "quit":
			fmt.Fprintf(os.Stderr, "\n\033[2mGoodbye.\033[0m\n")
			return nil
		case "/clear":
			cs.history = nil
			fmt.Fprintf(os.Stderr, "\033[2mConversation cleared.\033[0m\n\n")
			continue
		case "/help":
			cs.printHelp()
			continue
		}

		// Add user message to history.
		cs.history = append(cs.history, Message{Role: "user", Content: input})

		// Send to LLM and handle response (including tool call loops).
		err := cs.sendAndHandle()
		PleaseWait.Hide()
		if err != nil {
			fmt.Fprintf(os.Stderr, "\n\033[1;31mError: %s\033[0m\n\n", err)
			// Remove the failed user message so the user can retry.
			cs.history = cs.history[:len(cs.history)-1]
			continue
		}

		fmt.Fprintf(os.Stderr, "\n")
	}
}

// maxToolRounds limits how many consecutive tool call rounds before forcing a text response.
const maxToolRounds = 10

// stopSpinner hides the PleaseWait animation and clears the spinner line.
func stopSpinner() {
	PleaseWait.Hide()
	Flash("")
}

// startSpinner shows a Claude Code-style thinking animation.
func startSpinner() {
	PleaseWait.Set(
		func() string { return "\033[2mThinking...\033[0m" },
		[]string{"\033[1;36m⠋\033[0m", "\033[1;36m⠙\033[0m", "\033[1;36m⠹\033[0m", "\033[1;36m⠸\033[0m", "\033[1;36m⠼\033[0m", "\033[1;36m⠴\033[0m", "\033[1;36m⠦\033[0m", "\033[1;36m⠧\033[0m", "\033[1;36m⠇\033[0m", "\033[1;36m⠏\033[0m"},
	)
	PleaseWait.Show()
}

// sendAndHandle sends the current conversation to the LLM, streams the response,
// and handles any tool calls in a loop until the LLM produces a final text response.
func (cs *chatSession) sendAndHandle() error {
	// Build the three meta-tool handlers.
	agentTools := []AgentToolDef{
		{
			Tool: cs.tools[0], // list_tools
			Handler: func(args map[string]any) (string, error) {
				return cs.listTools(), nil
			},
		},
		{
			Tool: cs.tools[1], // get_tool_info
			Handler: func(args map[string]any) (string, error) {
				name := StringArg(args, "name")
				Debug("[chat] get_tool_info: name=%q", name)
				result := cs.toolInfo(name)
				Debug("[chat] get_tool_info result:\n%s", result)
				return result, nil
			},
		},
		{
			Tool: cs.tools[2], // run_tool
			Handler: func(args map[string]any) (string, error) {
				name := StringArg(args, "name")

				// Parse args: try JSON first, fall back to raw string.
				tool_args := make(map[string]any)
				raw_args := StringArg(args, "args")
				if raw_args != "" {
					var parsed map[string]interface{}
					if json.Unmarshal([]byte(raw_args), &parsed) == nil {
						for k, v := range parsed {
							tool_args[k] = v
						}
					} else {
						// Not JSON — treat as flag string for agents.
						tool_args["args"] = raw_args
					}
				}

				// Check if the underlying tool requires confirmation.
				if cs.toolNeedsConfirm(name) {
					stopSpinner()
					fmt.Fprintf(os.Stderr, "\033[1;33m  ╭─ Tool Call ─────────────────────────\033[0m\n")
					fmt.Fprintf(os.Stderr, "\033[1;33m  │\033[0m \033[1m%s\033[0m\n", name)
					if raw_args != "" {
						for _, line := range strings.Split(raw_args, "\n") {
							fmt.Fprintf(os.Stderr, "\033[1;33m  │\033[0m   %s\n", line)
						}
					}
					fmt.Fprintf(os.Stderr, "\033[1;33m  ╰──────────────────────────────────────\033[0m\n")
					if !nfo.GetConfirm("  Allow this tool call?") {
						startSpinner()
						return "Error: tool call denied by user", nil
					}
					startSpinner()
				}

				result, _, err := cs.runTool(name, tool_args)
				return result, err
			},
		},
	}

	fa := &FuzzAgent{LLM: cs.llm}

	startSpinner()

	waiting := true

	resp, history, err := fa.RunAgentLoop(context.Background(), cs.history, AgentLoopConfig{
		SystemPrompt: cs.system,
		Tools:        agentTools,
		MaxRounds:    maxToolRounds,
		ChatOptions:  []ChatOption{WithMaxTokens(4096)},
		Confirm: func(toolName string, argsSummary string) bool {
			if waiting {
				stopSpinner()
				waiting = false
			}
			fmt.Fprintf(os.Stderr, "\033[1;33m  ╭─ Tool Call ─────────────────────────\033[0m\n")
			fmt.Fprintf(os.Stderr, "\033[1;33m  │\033[0m \033[1m%s\033[0m\n", toolName)
			if argsSummary != "" {
				for _, line := range strings.Split(argsSummary, "\n") {
					fmt.Fprintf(os.Stderr, "\033[1;33m  │\033[0m   %s\n", line)
				}
			}
			fmt.Fprintf(os.Stderr, "\033[1;33m  ╰──────────────────────────────────────\033[0m\n")
			result := nfo.GetConfirm("  Allow this tool call?")
			startSpinner()
			waiting = true
			return result
		},
		Stream: func(chunk string) {
			if waiting {
				stopSpinner()
				waiting = false
			}
			fmt.Fprint(os.Stderr, chunk)
		},
		OnStep: func(step StepInfo) {
			if waiting {
				stopSpinner()
				waiting = false
			}
			if step.Done {
				return
			}
			for _, tc := range step.ToolCalls {
				switch tc.Name {
				case "list_tools", "get_tool_info":
					// Silent discovery steps.
				case "run_tool":
					tool_name := StringArg(tc.Args, "name")
					fmt.Fprintf(os.Stderr, "\033[1;33m  Ran '%s'", tool_name)
					if arg_str := StringArg(tc.Args, "args"); arg_str != "" {
						fmt.Fprintf(os.Stderr, " %s", arg_str)
					}
					fmt.Fprintf(os.Stderr, "\033[0m\n")
				}
			}
			// Restart spinner for next LLM round.
			startSpinner()
			waiting = true
		},
	})
	if err != nil {
		return err
	}

	cs.history = history

	// If the LLM produced no visible output (empty response after tool calls
	// or max rounds), let the user know rather than printing blank.
	if resp != nil {
		content := strings.TrimSpace(resp.Content)
		if content == "" && len(resp.ToolCalls) > 0 {
			fmt.Fprintf(os.Stderr, "\n\033[2m(agent produced no response)\033[0m\n")
		} else {
			fmt.Fprintf(os.Stderr, "\n")
		}
	}

	return nil
}

// runAgent executes a registered agent and captures its stdout/stderr output.
func (cs *chatSession) runAgent(name string, argsStr string) (string, error) {
	entry, ok := cs.agents[name]
	if !ok {
		return "", fmt.Errorf("unknown agent: %s", name)
	}

	var args []string
	if argsStr != "" {
		args = splitArgs(argsStr)
	}

	return execAgent(entry, args)
}

// splitArgs splits a string into arguments, respecting quoted strings.
func splitArgs(s string) []string {
	var args []string
	var current strings.Builder
	inQuote := false
	quoteChar := byte(0)

	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case inQuote:
			if ch == quoteChar {
				inQuote = false
			} else {
				current.WriteByte(ch)
			}
		case ch == '"' || ch == '\'':
			inQuote = true
			quoteChar = ch
		case ch == ' ' || ch == '\t':
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(ch)
		}
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args
}

// printHelp displays chat commands.
func (cs *chatSession) printHelp() {
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "  \033[1mChat Commands:\033[0m\n")
	fmt.Fprintf(os.Stderr, "    /help     Show this help\n")
	fmt.Fprintf(os.Stderr, "    /clear    Clear conversation history\n")
	fmt.Fprintf(os.Stderr, "    /exit     Exit chat\n")
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "  \033[1mAvailable Agents:\033[0m\n")
	for name, entry := range cs.agents {
		fmt.Fprintf(os.Stderr, "    %-12s %s\n", name, entry.desc)
	}
	fmt.Fprintf(os.Stderr, "\n")
	if len(cs.chatTools) > 0 {
		fmt.Fprintf(os.Stderr, "  \033[1mAvailable Tools:\033[0m\n")
		for name, ct := range cs.chatTools {
			fmt.Fprintf(os.Stderr, "    %-20s %s\n", name, ct.Desc())
		}
		fmt.Fprintf(os.Stderr, "\n")
	}
}

// startChat initializes and runs the interactive chat session.
func startChat() error {
	init_database()

	cfg := load_llm_config()
	if cfg.Provider == "" {
		return fmt.Errorf("no LLM provider configured, run: %s --setup", os.Args[0])
	}

	llm, err := NewLLMFromConfig(cfg)
	if err != nil {
		return err
	}

	session := newChatSession(llm)
	return session.run()
}
