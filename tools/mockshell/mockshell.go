// Package mockshell provides a chat tool that simulates a Unix shell
// by dispatching commands to an LLM rather than executing them. The
// point isn't functional utility — it's to observe what a tool-using
// LLM does when handed something that LOOKS like a shell. Nothing is
// actually run: no files created, no processes spawned, no state kept.
// Every call is a fresh LLM completion that invents plausible stdout.
package mockshell

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func init() { RegisterChatTool(new(MockShellTool)) }

// MockShellTool takes a command string, asks the shared worker LLM
// to imagine what a Unix shell would print in response, and returns
// that as the tool output. Stateless — no filesystem, no environment
// persistence across calls. If the user runs `mkdir foo` and then
// `ls`, the second call has no memory of the first unless the outer
// chat agent re-supplies that context in its prompt. Deliberate:
// state would add complexity without making the observation cleaner.
type MockShellTool struct{}

func (t *MockShellTool) Name() string { return "mock_shell" }

func (t *MockShellTool) Desc() string {
	return `Simulate a Unix shell command. An LLM imagines the output — nothing is actually executed, no files are created, no state persists between calls. Useful for exploration and for watching how you reason over shell-like output. Output looks real; treat it as plausible fiction, not evidence of system state.`
}

func (t *MockShellTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"command": {
			Type:        "string",
			Description: `The shell command to simulate (e.g. "ls -la /tmp", "cat /etc/os-release", "whoami").`,
		},
	}
}

// mockShellSystemPrompt is kept terse on purpose — heavy persona
// scaffolding steers the model's "shell voice" in specific directions
// and defeats the experiment. The only hard rule is "no explanation,
// no disclaimer" so the output is usable as pseudo-stdout.
const mockShellSystemPrompt = `You are a Unix shell running on a typical Linux system. The current user is "user" with home directory "/home/user". The current directory, if not specified, is "/home/user".

Respond to the command with only what the shell would print to stdout or stderr. Do NOT explain the command. Do NOT add a preamble or disclaimer. Do NOT mention that you are a model or that the output is simulated. Output only the shell's response — raw text, no markdown, no code fences.

If the command would produce no output (like a successful cd or mkdir), output nothing or just the next prompt line. If the command is invalid or the file/command doesn't exist, produce a realistic error message that a shell would give.`

func (t *MockShellTool) Run(args map[string]any) (string, error) {
	cmd, _ := args["command"].(string)
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "", fmt.Errorf("command is required")
	}

	llm := SharedWorkerLLM()
	if llm == nil {
		return "", fmt.Errorf("mock_shell requires the shared worker LLM; no LLM is configured for this process")
	}

	resp, err := llm.Chat(context.Background(),
		[]Message{{Role: "user", Content: cmd}},
		WithSystemPrompt(mockShellSystemPrompt),
		WithMaxTokens(1024),
		WithTemperature(0.4),
		WithThink(false),
	)
	if err != nil {
		return "", fmt.Errorf("mock_shell LLM call failed: %w", err)
	}
	out := strings.TrimSpace(ResponseText(resp))
	if out == "" {
		// Successful silent commands (cd, mkdir, etc.) have no output.
		// Return a single empty line so the outer chat loop has
		// something to append rather than a literal empty string,
		// which some tool-loop code treats as a failure.
		return "", nil
	}
	// Strip a leading prompt line if the model emitted one, since the
	// chat isn't a pty. Common shapes: "user@host:~$ cmd\n<output>".
	if lines := strings.SplitN(out, "\n", 2); len(lines) == 2 {
		first := strings.TrimSpace(lines[0])
		if strings.Contains(first, "$") && strings.HasSuffix(first, cmd) {
			out = lines[1]
		}
	}
	return out, nil
}
