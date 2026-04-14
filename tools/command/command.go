package command

import (
	"fmt"
	"os/exec"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func init() { RegisterChatTool(new(CommandTool)) }

// CommandTool runs a shell command and returns its output.
type CommandTool struct{}

func (t *CommandTool) Name() string { return "run_command" }
func (t *CommandTool) Desc() string {
	return "Execute a shell command on the local system and return its combined stdout/stderr output. Prefer running one command per call. Example: {\"command\": \"df -h\"}"
}

func (t *CommandTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"command": {Type: "string", Description: "A shell command string to execute, e.g. \"ls -la /tmp\" or \"free -m\". Must be a plain string, not an array or object."},
	}
}

// NeedsConfirm implements ConfirmableTool — shell commands always require user approval.
func (t *CommandTool) NeedsConfirm() bool { return true }

func (t *CommandTool) Run(args map[string]any) (string, error) {
	cmd := StringArg(args, "command")
	if cmd == "" {
		return "", fmt.Errorf("command is required")
	}

	// Check if the base command exists before executing.
	base := strings.Fields(cmd)
	if len(base) > 0 {
		if _, err := exec.LookPath(base[0]); err != nil {
			return "", fmt.Errorf("command not found: %s", base[0])
		}
	}

	out, err := exec.Command("sh", "-c", cmd).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("Command failed: %s\nOutput: %s", err, string(out)), nil
	}
	return string(out), nil
}
